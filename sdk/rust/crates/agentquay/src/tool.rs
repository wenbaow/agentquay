//! Tool 元数据与调用分发（设计文档 §4.1、§4.8）。

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use serde::Serialize;
use serde_json::Value;

/// tool 名规范：[a-zA-Z0-9_-]{1,78}。
pub fn is_valid_tool_name(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 78
        && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// appId 规范：[a-z0-9-]{1,48}，禁止 `_` 和 `.`（`_` 保留给合成名解析分隔）。
pub fn is_valid_app_id(app_id: &str) -> bool {
    !app_id.is_empty()
        && app_id.len() <= 48
        && app_id.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
}

/// 一个 Tool 的元数据（注册时上报给 Bridge）。
#[derive(Debug, Clone, Serialize)]
pub struct ToolMetadata {
    pub name: String,
    pub description: String,
    #[serde(rename = "inputSchema")]
    pub input_schema: Value,
    #[serde(rename = "requiresConfirmation")]
    pub requires_confirmation: bool,
    #[serde(rename = "timeoutSeconds")]
    pub timeout_seconds: u32,
    #[serde(rename = "confirmTimeoutSeconds")]
    pub confirm_timeout_seconds: u32,
}

impl ToolMetadata {
    /// 默认执行超时（秒）。
    pub const DEFAULT_TIMEOUT_SECONDS: u32 = 30;
    /// 默认确认超时（秒，独立于执行超时）。
    pub const DEFAULT_CONFIRM_TIMEOUT_SECONDS: u32 = 120;

    #[allow(clippy::too_many_arguments)]
    pub fn new(
        name: impl Into<String>,
        description: impl Into<String>,
        input_schema: Value,
        requires_confirmation: bool,
        timeout_seconds: u32,
        confirm_timeout_seconds: u32,
    ) -> Self {
        Self {
            name: name.into(),
            description: description.into(),
            input_schema,
            requires_confirmation,
            timeout_seconds,
            confirm_timeout_seconds,
        }
    }
}

/// Tool 执行错误（SDK → Bridge 透传，错误码入业务 code 空间）。
#[derive(Debug, Clone)]
pub struct ToolError {
    pub code: String,
    pub message: String,
    pub details: Option<Value>,
}

impl ToolError {
    /// tool 不存在。
    pub fn not_found(tool: String) -> Self {
        Self { code: "TOOL_NOT_FOUND".into(), message: format!("tool 不存在: {tool}"), details: None }
    }

    /// 参数反序列化失败（与注册 schema 不一致时才发生；Bridge 已按 schema 校验过）。
    pub fn invalid_arguments(e: serde_json::Error) -> Self {
        Self { code: "INVALID_ARGUMENTS".into(), message: format!("参数无效: {e}"), details: None }
    }

    /// 工具执行失败（业务错误 / panic / 序列化失败）。
    pub fn execution_error(message: impl Into<String>) -> Self {
        Self { code: "EXECUTION_ERROR".into(), message: message.into(), details: None }
    }

    /// 返回值序列化失败。
    pub fn serialization_error(message: impl Into<String>) -> Self {
        Self { code: "SERIALIZATION_ERROR".into(), message: message.into(), details: None }
    }

    /// 自定义业务错误（带 code）。
    pub fn business(code: impl Into<String>, message: impl Into<String>, details: Option<Value>) -> Self {
        Self { code: code.into(), message: message.into(), details }
    }
}

impl std::fmt::Display for ToolError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.code, self.message)
    }
}

impl std::error::Error for ToolError {}

/// 序列化返回值（过程宏生成代码调用）。
pub fn serialize<T: Serialize>(value: T) -> Result<Value, ToolError> {
    serde_json::to_value(value).map_err(|e| ToolError::serialization_error(e.to_string()))
}

/// `Result<T, ToolError>` 返回：错误直接透传（保留 code / details）。
pub fn finish_tool_result<T: Serialize>(result: Result<T, ToolError>) -> Result<Value, ToolError> {
    result.and_then(serialize)
}

/// `Result<T, E>` 返回：E 按 Display 消息包装为 EXECUTION_ERROR。
pub fn finish_result<T: Serialize, E: std::fmt::Display>(result: Result<T, E>) -> Result<Value, ToolError> {
    match result {
        Ok(v) => serialize(v),
        Err(e) => Err(ToolError::execution_error(e.to_string())),
    }
}

/// Tool 提供者：提供元数据 + 调用分发（由 `#[agent_tool]` 生成实现）。
///
/// `invoke` 以 `Arc<Self>` 作为接收者，使同步方法的 `spawn_blocking` 可以
/// 持有所需的 'static 数据；客户端内部统一以 `Arc<dyn ToolProvider>` 存储。
pub trait ToolProvider: Send + Sync + 'static {
    /// Tool 元数据列表（每次连接注册时上报）。
    fn tools(&self) -> Vec<ToolMetadata>;

    /// 执行指定 Tool，返回 JSON 结果或业务错误。
    fn invoke(
        self: Arc<Self>,
        tool: &str,
        args: Value,
    ) -> Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send + 'static>>;
}

/// 注册入口：单个 ToolProvider 或元组（支持 `register_tools((a, b, c))`）。
pub trait IntoProviders {
    fn into_providers(self) -> Vec<Arc<dyn ToolProvider>>;
}

impl<T: ToolProvider> IntoProviders for T {
    fn into_providers(self) -> Vec<Arc<dyn ToolProvider>> {
        vec![Arc::new(self)]
    }
}

macro_rules! tuple_into_providers {
    ($($t:ident),+) => {
        #[allow(non_snake_case)]
        impl<$($t: ToolProvider),+> IntoProviders for ($($t,)+) {
            fn into_providers(self) -> Vec<Arc<dyn ToolProvider>> {
                let ($($t,)+) = self;
                vec![$(Arc::new($t),)+]
            }
        }
    };
}

tuple_into_providers!(A);
tuple_into_providers!(A, B);
tuple_into_providers!(A, B, C);
tuple_into_providers!(A, B, C, D);
tuple_into_providers!(A, B, C, D, E);
tuple_into_providers!(A, B, C, D, E, F);
tuple_into_providers!(A, B, C, D, E, F, G);
tuple_into_providers!(A, B, C, D, E, F, G, H);
tuple_into_providers!(A, B, C, D, E, F, G, H, I);
tuple_into_providers!(A, B, C, D, E, F, G, H, I, J);
tuple_into_providers!(A, B, C, D, E, F, G, H, I, J, K);
tuple_into_providers!(A, B, C, D, E, F, G, H, I, J, K, L);

/// 闭包型 Tool 提供者（`AgentQuayClient::register_fn` 使用，免过程宏）。
pub struct FnToolProvider {
    meta: ToolMetadata,
    f: Box<dyn Fn(Value) -> Result<Value, ToolError> + Send + Sync>,
}

impl FnToolProvider {
    pub fn new(
        name: impl Into<String>,
        description: impl Into<String>,
        requires_confirmation: bool,
        timeout_seconds: u32,
        confirm_timeout_seconds: u32,
        f: impl Fn(Value) -> Result<Value, ToolError> + Send + Sync + 'static,
    ) -> Self {
        let meta = ToolMetadata::new(
            name,
            description,
            serde_json::json!({ "type": "object", "properties": {} }),
            requires_confirmation,
            timeout_seconds,
            confirm_timeout_seconds,
        );
        Self { meta, f: Box::new(f) }
    }
}

impl ToolProvider for FnToolProvider {
    fn tools(&self) -> Vec<ToolMetadata> {
        vec![self.meta.clone()]
    }

    fn invoke(
        self: Arc<Self>,
        _tool: &str,
        args: Value,
    ) -> Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send + 'static>> {
        Box::pin(async move { (self.f)(args) })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tool_name_validation() {
        assert!(is_valid_tool_name("search"));
        assert!(is_valid_tool_name("delete_song-1"));
        assert!(!is_valid_tool_name("bad.name"));
        assert!(!is_valid_tool_name(""));
        assert!(!is_valid_tool_name(&"x".repeat(79)));
    }

    #[test]
    fn app_id_validation() {
        assert!(is_valid_app_id("music-app"));
        assert!(is_valid_app_id("com-example-music"));
        assert!(!is_valid_app_id("music_app"));
        assert!(!is_valid_app_id("music.app"));
        assert!(!is_valid_app_id("MusicApp"));
        assert!(!is_valid_app_id(&"m".repeat(49)));
    }

    #[test]
    fn tuple_into_providers_collects_all() {
        let f1 = FnToolProvider::new("a", "A", false, 30, 120, |_| Ok(Value::Null));
        let f2 = FnToolProvider::new("b", "B", false, 30, 120, |_| Ok(Value::Null));
        let f3 = FnToolProvider::new("c", "C", false, 30, 120, |_| Ok(Value::Null));
        let providers = (f1, f2, f3).into_providers();
        assert_eq!(providers.len(), 3);
        let names: Vec<String> = providers
            .iter()
            .flat_map(|p| p.tools())
            .map(|m| m.name)
            .collect();
        assert_eq!(names, vec!["a", "b", "c"]);
    }
}
