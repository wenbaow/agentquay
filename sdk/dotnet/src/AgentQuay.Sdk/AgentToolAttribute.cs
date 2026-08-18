namespace AgentQuay;

/// <summary>
/// 标记方法为 Agent Tool（设计文档 §4.2）。
/// </summary>
/// <example>
/// <code>
/// [AgentTool("search", Description = "搜索音乐库")]
/// public List&lt;Song&gt; Search([AgentParam(Description = "搜索关键词")] string keyword) { ... }
/// </code>
/// </example>
[AttributeUsage(AttributeTargets.Method, Inherited = true, AllowMultiple = false)]
public sealed class AgentToolAttribute : Attribute
{
    /// <summary>Tool 名（缺省回退到方法名）。</summary>
    public string? Name { get; }

    /// <summary>描述（Agent 侧展示）。</summary>
    public string? Description { get; set; }

    /// <summary>危险操作：调用时先弹确认框，用户确认后才执行。</summary>
    public bool RequiresConfirmation { get; set; } = false;

    /// <summary>执行超时秒数（默认 30，Bridge 侧独立计时）。</summary>
    public int TimeoutSeconds { get; set; } = 30;

    /// <summary>确认超时秒数（默认 120，独立于执行超时）。</summary>
    public int ConfirmTimeoutSeconds { get; set; } = 120;

    public AgentToolAttribute(string? name = null) => Name = name;
}