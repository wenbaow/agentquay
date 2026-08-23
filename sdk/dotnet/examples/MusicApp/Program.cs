using AgentQuay;

// AgentQuay C# SDK 示例：音乐应用（设计文档 §4.2）。
//
// 运行前确保 Bridge 可用（SDK 会自动拉起内嵌 Bridge，或先运行 `agentquay start --daemon`）：
//   dotnet run --project examples/MusicApp
//
// 然后用 MCP 客户端连接 http://127.0.0.1:19846/mcp，
// 即可发现并调用 music-app_search / music-app_play / music-app_delete（需确认）。
//
// 注意：示例控制器用实例注册（持有歌曲列表状态）。

var client = await AgentQuayClient.ConnectAsync(
    appId: "music-app",
    appName: "Music Player",
    host: "localhost",
    port: 0,                  // 0 = 从 ~/.agentquay/port 自动读取实际端口
    autoSpawnBridge: true);   // 未检测到服务时自动拉起内嵌 Bridge

client.RegisterTools(new MusicController());

// 页面工具：惰性注册（页面智能路由 §3.2）。页面"未打开"工具也可见——
// 首次调用才创建实例（pageKey 只是 SDK 内部的分组标签，不进协议）。
// 图形应用可再配 SetUIThreadDispatcher + SetPageActivator（导航 + 等待就绪，见 WPF 扩展包）。
client.RegisterTools<PlayerPage>(pageKey: "PlayerPage");

Console.WriteLine("已注册 tools: " + string.Join(", ", client.ListTools()));
Console.WriteLine("工具描述带页面归属（tools/list 中 [Music Player|PlayerPage] 前缀）。");
Console.WriteLine("等待 Agent 调用…（Ctrl+C 退出）");
await client.StartAsync(); // 保持连接，监听调用；被同 appId 新实例替换时抛 ReplacedException

/// <summary>业务模型。</summary>
public sealed class Song
{
    public string? Id { get; set; }
    public string? Title { get; set; }
    public string? Artist { get; set; }
    public int Duration { get; set; }
}

/// <summary>业务控制器：加 [AgentTool] 注解暴露方法给 Agent。</summary>
public sealed class MusicController
{
    private readonly List<Song> _songs = new()
    {
        new Song { Id = "1", Title = "七里香", Artist = "周杰伦", Duration = 243 },
        new Song { Id = "2", Title = "晴天", Artist = "周杰伦", Duration = 269 },
        new Song { Id = "3", Title = "海阔天空", Artist = "Beyond", Duration = 326 },
        new Song { Id = "4", Title = "平凡之路", Artist = "朴树", Duration = 302 },
    };

    [AgentTool("search", Description = "搜索音乐库中的歌曲")]
    public List<Song> Search([AgentParam(Description = "搜索关键词")] string keyword)
    {
        return _songs
            .Where(s => (s.Title ?? "").Contains(keyword) || (s.Artist ?? "").Contains(keyword))
            .ToList();
    }

    [AgentTool("play", Description = "播放指定歌曲")]
    public Dictionary<string, object?> Play([AgentParam(Description = "歌曲ID")] string songId)
    {
        var song = _songs.FirstOrDefault(s => s.Id == songId)
                   ?? throw new ArgumentException("歌曲不存在: " + songId);
        return new Dictionary<string, object?> { ["status"] = "playing", ["song"] = song };
    }

    [AgentTool("delete", Description = "删除歌曲（危险操作，需要用户确认）", RequiresConfirmation = true)]
    public Dictionary<string, object?> Delete([AgentParam(Description = "歌曲ID")] string songId)
    {
        var song = _songs.FirstOrDefault(s => s.Id == songId)
                   ?? throw new ArgumentException("歌曲不存在: " + songId);
        _songs.Remove(song);
        return new Dictionary<string, object?> { ["status"] = "deleted", ["song"] = song };
    }

    /// <summary>异步方法：返回 Task&lt;T&gt;，SDK 自动 unwrap（设计文档 §4.2）。</summary>
    [AgentTool("async_echo", Description = "异步回显（演示 Task unwrap）")]
    public async Task<Dictionary<string, string>> AsyncEcho([AgentParam(Description = "消息")] string message)
    {
        await Task.Yield();
        return new Dictionary<string, string> { ["received"] = message };
    }
}
/// <summary>
/// 页面控制器（惰性注册演示）：页面每次首次调用时经无参构造创建。
/// 真实 WPF/WinUI 应用中此类通常是 Page 子类，配合 SetPageActivator 完成导航与就绪等待。
/// </summary>
public sealed class PlayerPage
{
    private readonly List<string> _history = new();

    [AgentTool("queue", Description = "查看播放队列（PlayerPage 页面工具）")]
    public List<string> Queue()
    {
        _history.Add("queue");
        return _history;
    }

    [AgentTool("enqueue", Description = "加入播放队列（PlayerPage 页面工具）")]
    public int Enqueue(string songId)
    {
        _history.Add(songId);
        return _history.Count;
    }
}
