//! AgentQuay SDK 错误类型。

use thiserror::Error;

/// AgentQuay SDK 错误。
#[derive(Debug, Error)]
pub enum AgentQuayError {
    /// 配置不合法（appId / appName 校验失败）。
    #[error("{0}")]
    InvalidConfig(String),

    /// 工具登记失败（tool 名不合法或重复）。
    #[error("{0}")]
    InvalidTool(String),

    /// 本地未检测到运行中的 Bridge，且无法自动拉起。
    #[error("本地未检测到 AgentQuay Bridge: {0}")]
    BridgeUnavailable(String),

    /// 自动拉起内嵌 Bridge 失败或启动超时。
    #[error("拉起内嵌 Bridge 失败: {0}")]
    BridgeSpawn(String),

    /// 与 Bridge 的 WebSocket 连接断开或失败。
    #[error("与 Bridge 的连接失败: {0}")]
    Connection(String),

    /// 注册被 Bridge 拒绝（协议不兼容 / appId 非法 / tool 非法）。
    #[error("注册被拒绝 [{code}]: {message}")]
    Registration {
        code: String,
        message: String,
        #[allow(unused)]
        supported_versions: Vec<String>,
    },

    /// 认证失败：authToken 与 Bridge 钉扎的 token 不匹配（token 可能被轮换）。
    #[error("认证失败: {0}")]
    AuthFailed(String),

    /// 本连接被同 appId 的新实例替换（不再重连）。
    #[error("本连接已被同 appId 的新实例替换")]
    Replaced,

    /// 协议消息无法解析或非法。
    #[error("协议错误: {0}")]
    Protocol(String),

    /// 客户端已关闭，无法再次 connect。
    #[error("客户端已关闭，无法再次 connect")]
    Closed,

    /// 本地 IO 错误（端口文件 / 日志等）。
    #[error("IO 错误: {0}")]
    Io(#[from] std::io::Error),
}
