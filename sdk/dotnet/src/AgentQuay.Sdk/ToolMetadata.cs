using System.Reflection;
using System.Text.Json.Nodes;
using System.Text.RegularExpressions;

namespace AgentQuay;

/// <summary>
/// 一个已注册 Tool 的元数据（注册后由扫描生成，含调用绑定）。
/// </summary>
public sealed class ToolMetadata
{
    /// <summary>tool 名规范：[a-zA-Z0-9_-]{1,78}。</summary>
    public static readonly Regex ToolNamePattern = new(@"^[a-zA-Z0-9_-]{1,78}$", RegexOptions.Compiled);

    /// <summary>appId 规范：[a-z0-9-]{1,48}，禁止 _ 和 .。</summary>
    public static readonly Regex AppIdPattern = new(@"^[a-z0-9-]{1,48}$", RegexOptions.Compiled);

    public string Name { get; }
    public string Description { get; }
    public JsonObject InputSchema { get; }
    public bool RequiresConfirmation { get; }
    public int TimeoutSeconds { get; }
    public int ConfirmTimeoutSeconds { get; }

    /// <summary>调用绑定：目标实例（静态方法为 null）与方法。</summary>
    public object? Target { get; }
    public MethodInfo Method { get; }

    public ToolMetadata(string name, string description, JsonObject inputSchema,
                        bool requiresConfirmation, int timeoutSeconds, int confirmTimeoutSeconds,
                        object? target, MethodInfo method)
    {
        Name = name;
        Description = description;
        InputSchema = inputSchema;
        RequiresConfirmation = requiresConfirmation;
        TimeoutSeconds = timeoutSeconds;
        ConfirmTimeoutSeconds = confirmTimeoutSeconds;
        Target = target;
        Method = method;
    }

    /// <summary>校验 appId 是否合法。</summary>
    public static bool IsValidAppId(string appId)
        => appId != null && AppIdPattern.IsMatch(appId);

    /// <summary>校验 tool 名是否合法。</summary>
    public static bool IsValidToolName(string name)
        => name != null && ToolNamePattern.IsMatch(name);
}