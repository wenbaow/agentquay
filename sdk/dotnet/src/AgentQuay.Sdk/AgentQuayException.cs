namespace AgentQuay;

/// <summary>
/// AgentQuay SDK 异常基类与内置异常。
/// </summary>
public class AgentQuayException : Exception
{
    public AgentQuayException(string message) : base(message) { }

    public AgentQuayException(string message, Exception innerException) : base(message, innerException) { }
}

/// <summary>本地未检测到运行中的 Bridge，且无法自动拉起。</summary>
public class BridgeUnavailableException : AgentQuayException
{
    public BridgeUnavailableException(string message) : base(message) { }

    public BridgeUnavailableException(string message, Exception innerException) : base(message, innerException) { }
}

/// <summary>自动拉起内嵌 Bridge 失败（端口被占、无权限等）。</summary>
public class BridgeSpawnException : BridgeUnavailableException
{
    public BridgeSpawnException(string message) : base(message) { }

    public BridgeSpawnException(string message, Exception innerException) : base(message, innerException) { }
}

/// <summary>与 Bridge 的 WebSocket 连接断开或失败。</summary>
public class ConnectionException : AgentQuayException
{
    public ConnectionException(string message) : base(message) { }

    public ConnectionException(string message, Exception innerException) : base(message, innerException) { }
}

/// <summary>注册被 Bridge 拒绝（协议不兼容 / appId 非法 / tool 非法）。</summary>
public class RegistrationException : AgentQuayException
{
    public string Code { get; }

    public RegistrationException(string code, string message)
        : base($"注册被拒绝 [{code}]: {message}")
    {
        Code = code;
    }
}

/// <summary>认证失败：authToken 与 Bridge 钉扎的 token 不匹配。</summary>
public class AuthFailedException : RegistrationException
{
    public AuthFailedException(string message) : base("AUTH_FAILED", message) { }
}

/// <summary>本连接被同 appId 的新实例替换（Bridge 关闭了本连接，不再重连）。</summary>
public class ReplacedException : AgentQuayException
{
    public ReplacedException() : base("本连接已被同 appId 的新实例替换") { }
}

/// <summary>协议消息无法解析或非法。</summary>
public class ProtocolException : AgentQuayException
{
    public ProtocolException(string message) : base(message) { }
}

/// <summary>工具调用返回业务错误（错误透传）。</summary>
public class ToolCallException : AgentQuayException
{
    public string Code { get; }
    public object? Details { get; }

    public ToolCallException(string code, string message, object? details)
        : base($"{code}: {message}")
    {
        Code = code;
        Details = details;
    }
}

/// <summary>页面未打开且无惰性工厂，无法调用（工具仍在表内，Agent 收到 PAGE_NOT_FOUND）。</summary>
public class PageNotFoundException : AgentQuayException
{
    public PageNotFoundException(string message) : base(message) { }

    public PageNotFoundException(string message, Exception innerException) : base(message, innerException) { }
}

/// <summary>页面激活失败：工厂抛异常 / 导航失败 / 就绪等待失败（Agent 收到 PAGE_ACTIVATION_FAILED）。</summary>
public class PageActivationException : AgentQuayException
{
    public PageActivationException(string message) : base(message) { }

    public PageActivationException(string message, Exception innerException) : base(message, innerException) { }
}

/// <summary>页面激活超时（默认 15s，创建/导航/等待整体计时；Agent 收到 PAGE_ACTIVATION_TIMEOUT）。</summary>
public class PageActivationTimeoutException : PageActivationException
{
    public PageActivationTimeoutException(string message) : base(message) { }
}