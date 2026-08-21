//! # AgentQuay Rust SDK（设计文档 §4.8）
//!
//! 让 AI Agent 通过标准 MCP 协议发现并调用桌面应用方法的 Rust SDK。
//! 职责：`#[agent_tool]` 标记 Tool、注册、心跳、调用分发、确认流程、
//! 指数退避重连、token 持久化、auto-spawn 内嵌 Bridge。
//!
//! ## 示例
//!
//! ```ignore
//! use agentquay::{agent_tool, AgentQuayClient};
//!
//! struct MusicController;
//!
//! #[agent_tool]
//! impl MusicController {
//!     #[agent_tool(name = "search", description = "搜索音乐库")]
//!     fn search(&self, keyword: String) -> Vec<String> {
//!         vec![format!("搜索结果: {keyword}")]
//!     }
//!
//!     #[agent_tool(name = "delete", description = "删除歌曲", requires_confirmation = true)]
//!     fn delete(&self, song_id: String) -> Result<(), agentquay::ToolError> {
//!         Ok(())
//!     }
//! }
//!
//! #[tokio::main]
//! async fn main() -> Result<(), Box<dyn std::error::Error>> {
//!     let client = AgentQuayClient::builder()
//!         .app_id("music-app")
//!         .app_name("Music Player")
//!         .build()?;
//!     client.register_tools(MusicController)?;
//!     client.connect().await?; // 保持连接，监听 Agent 调用
//!     Ok(())
//! }
//! ```
//!
//! 参数类型自动由 `schemars` 生成 JSON Schema；返回值自动 `serde_json` 序列化。
//! 注意：应用内 Tool 需在 `connect()` 之前完成注册（协议每次连接只允许一次注册）。

pub mod client;
pub mod confirm;
pub mod error;
mod home;
pub mod protocol;
pub mod spawn;
pub mod token;
pub mod tool;

mod schema;
mod tool_call;

#[doc(hidden)]
pub mod __private {
    //! 过程宏生成代码的运行时依赖（内部 API，勿直接使用）。
    pub use crate::schema::{param, param_schema, schema_generator, subschema_for, Param};
    pub use crate::tool::{finish_result, finish_tool_result, serialize};
}

pub use agentquay_macros::agent_tool;

pub use client::{AgentQuayClient, ClientBuilder};
pub use confirm::{ConfirmationHandler, ConsoleConfirmationHandler};
#[cfg(feature = "native-confirm")]
pub use confirm::NativeConfirmationHandler;
pub use error::AgentQuayError;
pub use protocol::LaunchInfo;
pub use tool::{FnToolProvider, IntoProviders, ToolError, ToolMetadata, ToolProvider};
pub use tool_call::{LoggingToolCallHandler, ToolCallHandler};

// 供过程宏生成代码与用户 derive 使用（agentquay::serde / agentquay::schemars 等）
pub use schemars;
pub use schemars::JsonSchema;
pub use serde;
pub use serde_json;
pub use tokio;

/// SDK 版本号。
pub const SDK_VERSION: &str = env!("CARGO_PKG_VERSION");
/// 与 Bridge 通信的协议版本（§3.1）。
pub const PROTOCOL_VERSION: &str = "1.0";

/// 单元测试共享的环境变量锁（token / spawn 测试会改 HOME 等）。
#[cfg(test)]
pub(crate) static TEST_ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
