//! Bridge ↔ 桌面应用的 WebSocket 消息格式（设计文档 §3.1、附录 A）。
//!
//! 消息外壳：`{ "type": "...", "payload": {...} }`。

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

// 消息类型（附录 A）
pub const MSG_REGISTER: &str = "register";
pub const MSG_REGISTER_ACK: &str = "register_ack";
pub const MSG_REGISTER_ERROR: &str = "register_error";
pub const MSG_INVOKE: &str = "invoke";
pub const MSG_RESULT: &str = "result";
pub const MSG_CONFIRM: &str = "confirm";
pub const MSG_CONFIRM_RESULT: &str = "confirm_result";
pub const MSG_PING: &str = "ping";
pub const MSG_PONG: &str = "pong";
pub const MSG_DISCONNECT: &str = "disconnect";
pub const MSG_NOTIFICATION: &str = "notification";

// 断开原因
pub const DISCONNECT_NORMAL: &str = "normal";
pub const DISCONNECT_REPLACED: &str = "replaced";
pub const DISCONNECT_SHUTDOWN: &str = "shutdown";
pub const DISCONNECT_MIGRATE: &str = "migrate"; // 让位：应用重连到端口文件指向的更强实例

/// 注册失败原因（register_error 的 code）。
pub const REG_ERR_BAD_REQUEST: &str = "BAD_REQUEST";
pub const REG_ERR_AUTH_FAILED: &str = "AUTH_FAILED";
pub const REG_ERR_UNSUPPORTED_VERSION: &str = "UNSUPPORTED_VERSION";
pub const REG_ERR_INVALID_APP_ID: &str = "INVALID_APP_ID";
pub const REG_ERR_INVALID_TOOL: &str = "INVALID_TOOL";

/// 所有 WebSocket 消息的通用外壳。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Envelope {
    #[serde(rename = "type")]
    pub msg_type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub payload: Option<Value>,
}

impl Envelope {
    pub fn new(msg_type: impl Into<String>, payload: Option<Value>) -> Self {
        Self { msg_type: msg_type.into(), payload }
    }

    /// 解析外壳消息；缺少 type 或非法 JSON 时报错。
    pub fn parse(raw: &str) -> Result<Self, serde_json::Error> {
        serde_json::from_str(raw)
    }

    /// payload 作为对象（无 payload / 非对象时返回空对象）。
    pub fn payload_object(&self) -> Map<String, Value> {
        match &self.payload {
            Some(Value::Object(map)) => map.clone(),
            _ => Map::new(),
        }
    }

    /// 序列化为 JSON 字符串。
    pub fn encode(&self) -> Result<String, serde_json::Error> {
        serde_json::to_string(self)
    }
}

/// Bridge → 应用：调用指定 Tool（§3.1 调用转发）。
#[derive(Debug, Clone, Deserialize)]
pub struct InvokePayload {
    #[serde(rename = "requestId")]
    pub request_id: String,
    pub tool: String,
    pub arguments: Value,
    #[serde(rename = "timeoutSeconds")]
    pub timeout_seconds: Option<u32>,
}

/// Bridge → 应用：请求用户确认（危险操作，§3.3）。
#[derive(Debug, Clone, Deserialize)]
pub struct ConfirmPayload {
    #[serde(rename = "requestId")]
    pub request_id: String,
    pub message: Option<String>,
    pub arguments: Option<Value>,
    #[serde(rename = "timeoutSeconds")]
    pub timeout_seconds: Option<u32>,
}

/// 应用 → Bridge：Tool 执行结果。
#[derive(Debug, Clone, Serialize)]
pub struct ResultPayload<'a> {
    #[serde(rename = "requestId")]
    pub request_id: &'a str,
    pub success: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ErrorInfo>,
}

/// 业务错误信息（应用返回，Bridge 透传）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ErrorInfo {
    pub code: String,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub details: Option<Value>,
}

/// 应用启动命令信息（随 register 上报，§5.8；供 Bridge 离线自动拉起）。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct LaunchInfo {
    /// 可执行文件绝对路径（必填）。
    #[serde(rename = "execPath")]
    pub exec_path: String,
    /// 启动参数（argv 数组，禁止 shell 字符串）。
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<String>,
    /// 工作目录（可空）。
    #[serde(skip_serializing_if = "String::is_empty")]
    pub cwd: String,
    /// 是否单实例（已在运行时不再重复拉起）。
    #[serde(rename = "singleInstance", skip_serializing_if = "is_false")]
    pub single_instance: bool,
    /// 覆盖全局启动等待超时秒数（0 = 用全局默认）。
    #[serde(rename = "launchTimeoutSeconds", skip_serializing_if = "is_zero")]
    pub launch_timeout_seconds: u32,
}

impl LaunchInfo {
    /// 构造启动命令。
    pub fn new(exec_path: impl Into<String>) -> Self {
        Self {
            exec_path: exec_path.into(),
            args: Vec::new(),
            cwd: String::new(),
            single_instance: false,
            launch_timeout_seconds: 0,
        }
    }

    /// 设置启动参数。
    pub fn with_args(mut self, args: Vec<String>) -> Self {
        self.args = args;
        self
    }

    /// 设置工作目录。
    pub fn with_cwd(mut self, cwd: impl Into<String>) -> Self {
        self.cwd = cwd.into();
        self
    }

    /// 标记单实例。
    pub fn single_instance(mut self, v: bool) -> Self {
        self.single_instance = v;
        self
    }

    /// 设置启动等待超时（秒，0 表示用全局默认）。
    pub fn launch_timeout(mut self, seconds: u32) -> Self {
        self.launch_timeout_seconds = seconds;
        self
    }
}

fn is_false(v: &bool) -> bool {
    !*v
}

fn is_zero(v: &u32) -> bool {
    *v == 0
}

/// 应用 → Bridge：用户确认结果。
#[derive(Debug, Clone, Serialize)]
pub struct ConfirmResultPayload<'a> {
    #[serde(rename = "requestId")]
    pub request_id: &'a str,
    pub confirmed: bool,
}

/// 双向心跳（§3.1）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PingPayload {
    pub timestamp: i64,
}

/// 优雅断开通知。
#[derive(Debug, Clone, Deserialize)]
pub struct DisconnectPayload {
    pub reason: String,
}

/// Bridge → 应用：广播通知。
#[derive(Debug, Clone, Deserialize)]
pub struct NotificationPayload {
    pub message: String,
}

/// 当前 Unix 时间戳（心跳用）。
pub fn now_timestamp() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(d) => d.as_secs() as i64,
        Err(_) => 0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn envelope_roundtrip() {
        let env = Envelope::new(MSG_PING, Some(serde_json::json!({ "timestamp": 1700000000 })));
        let raw = env.encode().unwrap();
        assert_eq!(
            raw,
            r#"{"type":"ping","payload":{"timestamp":1700000000}}"#
        );
        let parsed = Envelope::parse(&raw).unwrap();
        assert_eq!(parsed.msg_type, MSG_PING);
        assert_eq!(parsed.payload_object()["timestamp"], 1700000000);
    }

    #[test]
    fn envelope_without_payload_omits_field() {
        let env = Envelope::new(MSG_PONG, None);
        assert_eq!(env.encode().unwrap(), r#"{"type":"pong"}"#);
    }

    #[test]
    fn envelope_missing_type_is_error() {
        assert!(Envelope::parse(r#"{"payload":{}}"#).is_err());
    }

    #[test]
    fn invoke_payload_camel_case() {
        let raw = r#"{"requestId":"req-1","tool":"search","arguments":{"keyword":"周杰伦"},"timeoutSeconds":30}"#;
        let p: InvokePayload = serde_json::from_str(raw).unwrap();
        assert_eq!(p.request_id, "req-1");
        assert_eq!(p.tool, "search");
        assert_eq!(p.timeout_seconds, Some(30));
        assert_eq!(p.arguments["keyword"], "周杰伦");
    }

    #[test]
    fn result_payload_omits_nulls() {
        let ok = ResultPayload {
            request_id: "req-1",
            success: true,
            data: Some(serde_json::json!([1, 2])),
            error: None,
        };
        let raw = serde_json::to_string(&ok).unwrap();
        assert!(!raw.contains("error"));
        assert!(raw.contains(r#""data":[1,2]"#));
    }
}
