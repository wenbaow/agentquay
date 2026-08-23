package com.agentquay;

/**
 * AgentQuay SDK 异常基类与内置异常。
 */
public class AgentQuayException extends RuntimeException {

    public AgentQuayException(String message) {
        super(message);
    }

    public AgentQuayException(String message, Throwable cause) {
        super(message, cause);
    }

    /** 本地未检测到运行中的 Bridge，且无法自动拉起。 */
    public static class BridgeUnavailableException extends AgentQuayException {
        public BridgeUnavailableException(String message) {
            super(message);
        }

        public BridgeUnavailableException(String message, Throwable cause) {
            super(message, cause);
        }
    }

    /** 自动拉起内嵌 Bridge 失败（端口被占、无权限等）。 */
    public static class BridgeSpawnException extends BridgeUnavailableException {
        public BridgeSpawnException(String message) {
            super(message);
        }

        public BridgeSpawnException(String message, Throwable cause) {
            super(message, cause);
        }
    }

    /** 与 Bridge 的 WebSocket 连接断开或失败。 */
    public static class ConnectionException extends AgentQuayException {
        public ConnectionException(String message) {
            super(message);
        }

        public ConnectionException(String message, Throwable cause) {
            super(message, cause);
        }
    }

    /** 注册被 Bridge 拒绝（协议不兼容 / appId 非法 / tool 非法）。 */
    public static class RegistrationException extends AgentQuayException {
        private final String code;

        public RegistrationException(String code, String message) {
            super("注册被拒绝 [" + code + "]: " + message);
            this.code = code;
        }

        public String getCode() {
            return code;
        }
    }

    /** 认证失败：authToken 与 Bridge 钉扎的 token 不匹配。 */
    public static class AuthFailedException extends RegistrationException {
        public AuthFailedException(String message) {
            super("AUTH_FAILED", message);
        }
    }

    /** 本连接被同 appId 的新实例替换（Bridge 关闭了本连接，不再重连）。 */
    public static class ReplacedException extends AgentQuayException {
        public ReplacedException() {
            super("本连接已被同 appId 的新实例替换");
        }
    }

    /** 协议消息无法解析或非法。 */
    public static class ProtocolException extends AgentQuayException {
        public ProtocolException(String message) {
            super(message);
        }
    }

    /** 工具调用返回业务错误（错误透传）。 */
    public static class ToolCallException extends AgentQuayException {
        private final String code;
        private final Object details;

        public ToolCallException(String code, String message, Object details) {
            super(code + ": " + message);
            this.code = code;
            this.details = details;
        }

        public String getCode() {
            return code;
        }

        public Object getDetails() {
            return details;
        }
    }

    /** 页面未打开且无惰性工厂，无法调用（工具仍在表内，Agent 收到 PAGE_NOT_FOUND）。 */
    public static class PageNotFoundException extends AgentQuayException {
        public PageNotFoundException(String message) {
            super(message);
        }

        public PageNotFoundException(String message, Throwable cause) {
            super(message, cause);
        }
    }

    /** 页面激活失败：工厂抛异常 / 导航失败 / 就绪等待失败（PAGE_ACTIVATION_FAILED）。 */
    public static class PageActivationException extends AgentQuayException {
        public PageActivationException(String message) {
            super(message);
        }

        public PageActivationException(String message, Throwable cause) {
            super(message, cause);
        }
    }

    /** 页面激活超时（默认 15s，创建/导航/等待整体计时；PAGE_ACTIVATION_TIMEOUT）。 */
    public static class PageActivationTimeoutException extends PageActivationException {
        public PageActivationTimeoutException(String message) {
            super(message);
        }
    }
}
