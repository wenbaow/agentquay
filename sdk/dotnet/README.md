# AgentQuay.Sdk — AgentQuay C# SDK

让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。加个 `[AgentTool]` 注解，其余全部由框架处理
（设计文档 §4.2）。

## 依赖

```xml
<PackageReference Include="AgentQuay.Sdk" Version="0.3.3" />
```

- 需要 **.NET 8+**（`ClientWebSocket` 与 `System.Text.Json` 均为 BCL 内置，零第三方运行时依赖）
- 内嵌 Bridge 二进制（Windows/Linux/macOS）随 NuGet 包以 `contentFiles` 分发，自动复制到宿主输出目录

## 用法

```csharp
using AgentQuay;

public class MusicController
{
    [AgentTool("search", Description = "搜索音乐库")]
    public List<Song> Search([AgentParam(Description = "搜索关键词")] string keyword)
    {
        // 业务逻辑
    }

    [AgentTool("play", Description = "播放歌曲")]
    public void Play([AgentParam(Description = "歌曲ID")] string songId)
    {
        // 业务逻辑
    }

    [AgentTool("delete", Description = "删除歌曲", RequiresConfirmation = true)]
    public void Delete([AgentParam(Description = "歌曲ID")] string songId)
    {
        // 危险操作：Bridge 会先向应用弹确认框
    }
}

// 一行启动（设计文档 §4.2）
var client = await AgentQuayClient.ConnectAsync(
    appId: "music-app",          // [a-z0-9-]{1,48}，禁止 _ 和 .
    appName: "Music Player",
    host: "localhost",
    port: 0,                     // 0 = 从 ~/.agentquay/port 自动读取实际端口
    autoSpawnBridge: true);      // 未检测到服务时自动拉起内嵌 Bridge（默认）

client.RegisterTools<MusicController>();
await client.StartAsync();       // 保持连接，监听调用；被同 appId 新实例替换时抛 ReplacedException
```

## 特性

| 能力 | 说明 |
|------|------|
| 注解扫描 | `[AgentTool]`（name 缺省回退方法名）+ `[AgentParam]`（描述/必填）；支持实例与静态方法、`RegisterTools<T>()` / `RegisterTools(Type)` / `RegisterTools(instance)` |
| JSON Schema | `System.Text.Json` 生成：基本类型、string、char、Guid、DateTime、enum、数组、List/Set 集合、`IDictionary`、`T?` / `Nullable<T>` / `string?`（可空）、嵌套 POJO、`[JsonPropertyName]` 重命名 / `[JsonIgnore]`；`CancellationToken` 参数自动忽略 |
| 异步方法 | 返回 `Task<T>` / `Task` / `ValueTask<T>` 自动 unwrap（设计文档 §4.2） |
| 确认流程 | 默认弹原生观感确认框（WinForms 反射加载，超时自动取消；无图形环境回退控制台输入）；可用 `confirmationHandler` 参数自定义 |
| token 持久化 | Windows Credential Manager（advapi32，零依赖）→ `~/.agentquay/tokens.json` 回退；macOS/Linux 使用文件回退 |
| 心跳 + 重连 | 30s 应用层 ping/pong；断线指数退避重连（1s → 30s），自动携带 token；被替换（同 appId 新实例）时 `StartAsync()` 抛 `ReplacedException` |
| auto-spawn | 本地无 Bridge 时自动拉起内嵌二进制（`serve --embedded`，日志写入 `~/.agentquay/logs`），进程退出自动终止 |

## 确认流程自定义

```csharp
var client = await AgentQuayClient.ConnectAsync(
    appId: "music-app",
    appName: "Music Player",
    confirmationHandler: (message, arguments, timeoutSeconds) =>
    {
        Console.WriteLine($"⚠️ {message} {arguments}");
        return Console.ReadLine()?.Trim().ToLowerInvariant() == "y"; // true=确认 / false=取消
    });
```

## 构建与测试

```bash
dotnet build sdk/dotnet/src/AgentQuay.Sdk/AgentQuay.Sdk.csproj   # 编译 SDK
dotnet test  sdk/dotnet/tests/AgentQuay.Sdk.Tests/               # 单测 + 端到端（需先构建 bridge/dist）
dotnet run --project sdk/dotnet/examples/MusicApp                # 示例应用
```

端到端覆盖（`AgentQuayE2ETest`，真实 Go Bridge）：注册/token、`tools/list` 合成名与描述前缀、
正常调用、`-32003` 参数校验、业务错误透传、确认流程（确认/取消 `-32005`）、异步方法 unwrap、
同 appId 替换（`ReplacedException`）、断线重连携带 token。

> 测试通过环境变量 `AGENTQUAY_BRIDGE_BIN` 或自动查找 `bridge/dist/` 定位 Bridge 二进制。

## 打包说明

- NuGet 包名 `AgentQuay.Sdk`，Bridge 二进制存放于包内 `contentFiles/any/any/bridge_bin/`，
  引用包时自动复制到宿主输出目录（`bridge_bin/agentquay-<os>-<arch>[.exe]`）。
- SDK 同时支持从环境变量 `AGENTQUAY_BRIDGE_BIN` / `AGENTQUAY_BRIDGE_DIR`、仓库树
  `bridge_bin/`、`PATH` 定位 Bridge 二进制。