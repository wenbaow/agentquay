"""AgentQuay SDK 异常定义。"""


class AgentQuayError(Exception):
    """SDK 基类异常。"""


class BridgeUnavailableError(AgentQuayError):
    """本地未检测到运行中的 Bridge，且无法自动拉起。"""


class BridgeSpawnError(BridgeUnavailableError):
    """自动拉起内嵌 Bridge 失败（端口被占、无权限等）。"""


class BridgeConnectionError(AgentQuayError):
    """与 Bridge 的 WebSocket 连接断开或失败。"""


class RegistrationError(AgentQuayError):
    """注册被 Bridge 拒绝（协议不兼容 / appId 非法 / tool 非法）。"""


class AuthFailedError(RegistrationError):
    """认证失败：authToken 与 Bridge 钉扎的 token 不匹配。"""


class ProtocolError(AgentQuayError):
    """协议消息无法解析或非法。"""


class ReplacedError(AgentQuayError):
    """本连接被同 appId 的新实例替换（Bridge 关闭了本连接）。"""


class ConfirmationError(AgentQuayError):
    """确认流程异常（用户取消 / 确认超时 / 弹窗失败）。"""


class InvokeTimeoutError(AgentQuayError):
    """工具执行超时（应用可能仍在执行）。"""


class ToolCallError(AgentQuayError):
    """工具调用返回错误（业务错误，携带 code/message/details）。"""

    def __init__(self, code: str, message: str, details=None):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
        self.details = details


class PageNotFoundError(AgentQuayError):
    """页面未打开且无惰性工厂，无法调用（工具仍在表内，Agent 收到 PAGE_NOT_FOUND）。"""


class PageActivationError(AgentQuayError):
    """页面激活失败：工厂抛异常 / 导航失败 / 就绪等待失败（PAGE_ACTIVATION_FAILED）。"""


class PageActivationTimeoutError(PageActivationError):
    """页面激活超时（默认 15s，创建/导航/等待整体计时；PAGE_ACTIVATION_TIMEOUT）。"""
