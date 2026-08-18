using System.Text.Json.Nodes;

namespace AgentQuay;

/// <summary>
/// 应用的启动命令（随 register 上报，供 Bridge 离线自动拉起，设计文档 §5.8）。
/// </summary>
/// <remarks>
/// 自动探测使用 <see cref="Environment.ProcessPath"/>（.NET 6+，单文件发布也正确）；
/// 打包/自定义启动器场景建议显式构造并指定 ExecPath。
/// </remarks>
public sealed record LaunchInfo
{
    /// <summary>可执行文件绝对路径（必填）。</summary>
    public required string ExecPath { get; init; }

    /// <summary>启动参数（argv 数组，禁止 shell 字符串）。</summary>
    public IReadOnlyList<string> Args { get; init; } = Array.Empty<string>();

    /// <summary>工作目录（可空）。</summary>
    public string? Cwd { get; init; }

    /// <summary>是否单实例（已在运行时不再重复拉起）。</summary>
    public bool SingleInstance { get; init; }

    /// <summary>覆盖全局启动等待超时秒数（0 = 用 Bridge 全局默认）。</summary>
    public int LaunchTimeoutSeconds { get; init; }

    /// <summary>
    /// 尽力探测当前进程的启动命令；探测失败返回 null（不阻塞注册，可后续手工 apps add）。
    /// </summary>
    public static LaunchInfo? Detect()
    {
        var exe = Environment.ProcessPath;
        if (string.IsNullOrEmpty(exe))
        {
            return null;
        }
        return new LaunchInfo { ExecPath = exe };
    }

    /// <summary>序列化为注册 payload 的 launch 字段。</summary>
    public JsonObject ToJson()
    {
        var node = new JsonObject { ["execPath"] = ExecPath };
        if (Args is { Count: > 0 })
        {
            node["args"] = new JsonArray(Args.Select(a => (JsonNode)a).ToArray());
        }
        if (!string.IsNullOrEmpty(Cwd))
        {
            node["cwd"] = Cwd;
        }
        node["singleInstance"] = SingleInstance;
        if (LaunchTimeoutSeconds > 0)
        {
            node["launchTimeoutSeconds"] = LaunchTimeoutSeconds;
        }
        return node;
    }
}
