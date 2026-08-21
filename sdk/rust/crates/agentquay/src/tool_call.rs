//! 工具调用钩子：在业务方法执行前触发，允许 UI 层拦截并响应（设计文档 §6.7）。

use serde_json::Value;

/// 工具调用钩子接口。
///
/// # 示例
///
/// ```rust,ignore
/// use agentquay::{AgentQuayClient, ToolCallHandler};
/// use std::sync::Arc;
///
/// struct MyHandler;
///
/// impl ToolCallHandler for MyHandler {
///     fn on_tool_call(&self, tool_name: &str, arguments: Option<&Value>) {
///         println!("调用工具: {} {:?}", tool_name, arguments);
///     }
/// }
///
/// let client = AgentQuayClient::builder()
///     .app_id("my-app")
///     .app_name("My App")
///     .tool_call_handler(Arc::new(MyHandler))
///     .build()?;
/// ```
pub trait ToolCallHandler: Send + Sync + 'static {
    /// 工具调用前的回调。
    ///
    /// - `tool_name`: 工具名
    /// - `arguments`: 调用参数（可为 None）
    fn on_tool_call(&self, tool_name: &str, arguments: Option<&Value>);
}

/// 一个简单的日志钩子，打印工具调用信息。
#[derive(Clone)]
pub struct LoggingToolCallHandler;

impl ToolCallHandler for LoggingToolCallHandler {
    fn on_tool_call(&self, tool_name: &str, arguments: Option<&Value>) {
        log::info!(
            "工具调用: {} args={}",
            tool_name,
            match arguments {
                Some(args) => serde_json::to_string(args).unwrap_or_default(),
                None => String::new(),
            }
        );
    }
}
