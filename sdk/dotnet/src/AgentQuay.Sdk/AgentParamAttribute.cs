namespace AgentQuay;

/// <summary>
/// 方法参数的补充元数据（设计文档 §4.2）。
/// </summary>
[AttributeUsage(AttributeTargets.Parameter, AllowMultiple = false)]
public sealed class AgentParamAttribute : Attribute
{
    /// <summary>参数描述（写入 JSON Schema 的 property description）。</summary>
    public string? Description { get; set; }

    /// <summary>是否必填（默认 true；可空类型 / Nullable&lt;T&gt; 始终视为可选）。</summary>
    public bool Required { get; set; } = true;
}