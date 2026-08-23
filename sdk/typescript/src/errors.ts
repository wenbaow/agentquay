/**
 * AgentQuay SDK 异常定义（与 Python SDK errors.py / Java SDK AgentQuayException 对齐）。
 */

/** SDK 基类异常。 */
export class AgentQuayError extends Error {
  constructor(message: string) {
    super(message);
    this.name = new.target.name;
  }
}

/** 本地未检测到运行中的 Bridge，且无法自动拉起。 */
export class BridgeUnavailableError extends AgentQuayError {}

/** 自动拉起内嵌 Bridge 失败（端口被占、无权限等）。 */
export class BridgeSpawnError extends BridgeUnavailableError {}

/** 与 Bridge 的 WebSocket 连接断开或失败。 */
export class BridgeConnectionError extends AgentQuayError {}

/** 注册被 Bridge 拒绝（协议不兼容 / appId 非法 / tool 非法）。 */
export class RegistrationError extends AgentQuayError {
  constructor(message: string, readonly code = "UNKNOWN") {
    super(message);
  }
}

/** 认证失败：authToken 与 Bridge 钉扎的 token 不匹配。 */
export class AuthFailedError extends RegistrationError {
  constructor(message: string) {
    super(message, "AUTH_FAILED");
  }
}

/** 协议消息无法解析或非法。 */
export class ProtocolError extends AgentQuayError {}

/** 本连接被同 appId 的新实例替换（Bridge 关闭了本连接）。 */
export class ReplacedError extends AgentQuayError {}

/** 确认流程异常（用户取消 / 确认超时 / 弹窗失败）。 */
export class ConfirmationError extends AgentQuayError {}

/** 工具执行超时（应用可能仍在执行）。 */
export class InvokeTimeoutError extends AgentQuayError {}

/** 工具调用返回错误（业务错误，携带 code/message/details）。 */
export class ToolCallError extends AgentQuayError {
  constructor(
    readonly code: string,
    message: string,
    readonly details?: unknown,
  ) {
    super(`${code}: ${message}`);
  }
}

/** 页面未打开且无惰性工厂，无法调用（工具仍在表内，Agent 收到 PAGE_NOT_FOUND）。 */
export class PageNotFoundError extends AgentQuayError {}

/** 页面激活失败：工厂抛异常 / 导航失败 / 就绪等待失败（PAGE_ACTIVATION_FAILED）。 */
export class PageActivationError extends AgentQuayError {}

/** 页面激活超时（默认 15s，创建/导航/等待整体计时；PAGE_ACTIVATION_TIMEOUT）。 */
export class PageActivationTimeoutError extends PageActivationError {}
