# AgentQuay SDK 使用教程

> AgentQuay 让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。
> 你只需要在应用方法上加一个注解/装饰器/宏，SDK 负责连接 Bridge、注册工具、
> 心跳、确认、自动重连与 token 钉扎。**零安装、零配置**——没装 Bridge 时 SDK
> 会自动拉起内嵌二进制。

- 整体架构与协议方案：[`README.md`](README.md)
- 仓库结构：`bridge/`（Go 守护进程）、`sdk/<language>/`（各语言 SDK）

---

## 目录

- [1. 它是怎么工作的](#1-它是怎么工作的)
- [2. 核心概念](#2-核心概念)
- [3. SDK 一览](#3-sdk-一览)
- [4. 快速上手（语言无关）](#4-快速上手语言无关)
- [5. 各语言 SDK 使用](#5-各语言-sdk-使用)
  - [5.1 Python](#51-python)
  - [5.2 TypeScript](#52-typescript)
  - [5.3 Java](#53-java)
  - [5.4 C# / .NET](#54-c--net)
  - [5.5 C++（Qt）](#55-cqt)
  - [5.6 Rust](#56-rust)
- [6. 进阶主题](#6-进阶主题)
  - [6.1 危险操作确认](#61-危险操作确认)
  - [6.2 认证与 token 钉扎](#62-认证与-token-钉扎)
  - [6.3 超时与孤儿结果](#63-超时与孤儿结果)
  - [6.4 启动命令上报与离线自动拉起](#64-启动命令上报与离线自动拉起)
  - [6.5 错误处理（错误码）](#65-错误处理错误码)
  - [6.7 工具调用钩子（on_tool_call）](#67-工具调用钩子on_tool_call)
- [6.8 内置工具 app_*](#68-内置工具-app_)
- [7. 把 AI Agent 接进来（MCP）](#7-把-ai-agent-接进来mcp)
- [8. Bridge 管理与运维](#8-bridge-管理与运维)
- [9. 测试与验证](#9-测试与验证)
- [10. 约定、限制与常见问题](#10-约定限制与常见问题)

---

## 1. 它是怎么工作的

```
┌─────────────────────────────────────────────────────────┐
│              AgentQuay Bridge（Go 守护进程）               │
│      单端口 19846：/mcp（Streamable HTTP）+ /ws（WS）      │
│     MCP Server │ 注册中心(token钉扎) │ 消息路由/心跳        │
└────────┬────────────────────────────────┬────────────────┘
         │ MCP (Streamable HTTP)          │ WebSocket
         │ http://127.0.0.1:19846/mcp     │ ws://127.0.0.1:19846/ws
┌────────┴────────┐              ┌────────┴─────────┐
│  AI Agent 工具   │              │  桌面应用进程       │
│  opencode/codex │              │  MusicApp/...     │
│  zcode/...      │              │  (Python SDK)     │
└─────────────────┘              └───────────────────┘
```

一个应用接入 AgentQuay 的完整流程只有三步：

1. **定义工具**：在要暴露给 Agent 的方法上加 `@agent_tool`（各语言对应的
   注解/装饰器/宏），可选地标注描述、超时、是否危险操作。
2. **注册并连接**：创建 `AgentQuayClient`，把控制器类注册进去，然后调用
   `connect()`。SDK 会检测/拉起 Bridge、自动获得并持久化 token、把工具表上报。
3. **Agent 调用**：任何支持 MCP Streamable HTTP 的 Agent 指向
   `http://127.0.0.1:19846/mcp`，就会看到 `{appId}_{toolName}` 形式的工具并可直接调用。

## 2. 核心概念

| 概念 | 说明 |
|------|------|
| **App（应用）** | 一个被 Agent 控制的桌面应用实例。用 `appId` 唯一标识。 |
| **appId** | `[a-z0-9-]{1,48}`，**只允许小写字母/数字/连字符，禁止 `_` 和 `.`**（如 `music-app`）。`_` 被保留用于拼接工具名。 |
| **appName** | 应用显示名，出现在工具描述的 `[应用名]` 前缀里。 |
| **Tool（工具）** | 暴露给 Agent 的方法。tool 名 `[a-zA-Z0-9_-]{1,78}`，缺省回退到方法名。 |
| **Bridge** | Go 守护进程，同时是 MCP Server 和注册/路由中心。默认端口 `19846`（被占用时自动漂移到 19846–19856，端口写入 `~/.agentquay/port`）。 |
| **auto-spawn** | SDK 检测不到本地 Bridge 时自动以 `serve --embedded` 拉起内嵌二进制（各 SDK 内置，~4MB 压缩后）。`auto_spawn_bridge=True` 是默认。 |
| **Token（钉扎认证）** | 首次注册时 Bridge 分配 `aq_`+64hex token 并持久化；之后重连必须携带，防止伪造实例顶替。 |
| **注册/调用** | 应用 → Bridge 走 WebSocket（`/ws`），Agent → Bridge 走 MCP（`/mcp`）。 |
| **心跳与重连** | 每 30s 双向 ping/pong；断线后指数退避重连（1s → 30s），重连自动携带 token。 |
| **确认流程** | `requires_confirmation=True` 的工具，调用前 Bridge 先向应用弹 OS 原生确认框，确认通过才真正执行。 |

## 3. SDK 一览

| 语言 | 包名 | 版本 | 环境要求 | 实现位置 |
|------|------|------|----------|----------|
| Python | `agentquay-sdk`（PyPI） | 0.3.2 | Python ≥ 3.10 | `sdk/python` |
| TypeScript | `@agentquay/sdk`（npm） | 0.3.2 | Node ≥ 18 | `sdk/typescript` |
| Java | `io.github.wenbaow:agentquay-sdk`（Maven） | 0.3.2 | Java 11+，编译器需 `-parameters` | `sdk/java` |
| C# / .NET | `AgentQuay.Sdk`（NuGet） | 0.3.2 | .NET 8+，零第三方依赖 | `sdk/dotnet` |
| C++ / Qt | `agentquay`（源码/CMake，v1 未进包管理器） | 0.3.2 | Qt 6.5+，C++17 | `sdk/cpp` |
| Rust | `agentquay`（crates.io；Bridge 二进制构建期按版本下载） | 0.3.2 | 需自带 `tokio`/`serde`/`schemars` | `sdk/rust` |
| Swift | 设计稿 §4.5（Swift Macro） | — | 尚未实现 | — |

> 各 SDK 一致性：`name` 缺省回退到方法名；`description` 缺省回退到
> 文档注释首行（Python/Rust）；危险操作统一走 `requires_confirmation`；
> 超时统一为 `timeout_seconds`（执行，默认 30s）+ `confirm_timeout_seconds`
> （确认，默认 120s，两者相互独立）。

## 4. 快速上手（语言无关）

以 Python 为例，其余语言见 [§5](#5-各语言-sdk-使用)。

**第 1 步：写一个被 Agent 控制的应用**（`music_app.py`）

```python
import asyncio
from agentquay import AgentQuayClient, agent_tool

class MusicController:
    @agent_tool("search", description="搜索音乐库中的歌曲")
    def search(self, keyword: str, limit: int = 10) -> list[dict]:
        return [{"id": "1", "title": "七里香", "artist": "周杰伦"}][:limit]

    @agent_tool("delete", description="删除歌曲", requires_confirmation=True)
    def delete(self, song_id: str) -> dict:
        return {"status": "deleted", "song_id": song_id}

async def main():
    client = AgentQuayClient(app_id="music-app", app_name="Music Player")
    client.register_tools(MusicController())
    print(f"已注册 tools: {client.list_tools()}")
    await client.connect()      # 保持连接，监听 Agent 调用；Ctrl+C 退出

asyncio.run(main())
```

**第 2 步：跑起来**

```bash
python music_app.py
```

没有安装 Bridge 也没关系——SDK 会自动拉起内嵌 Bridge。也可以手动装系统服务：

```bash
cd bridge
go build -o dist/agentquay ./cmd/agentquay   # 需要 Go 1.25.5+
./dist/agentquay start --daemon
```

**第 3 步：用 Agent 连接**

任何支持 MCP Streamable HTTP 的 Agent（opencode / codex / zcode / Claude
Desktop …），把 MCP 端点配成：

```
http://127.0.0.1:19846/mcp
```

Agent 拉取 `tools/list` 就会看到 `music-app_search` / `music-app_delete` 等工具，
描述带 `[Music Player]` 前缀；直接调用即可，无需额外配置。

---

## 5. 各语言 SDK 使用

### 5.1 Python

**安装**

```bash
pip install agentquay-sdk        # PyPI；依赖 websockets>=12.0
# 可选增强：pip install agentquay-sdk[keyring]     系统钥匙串持久化 token
#           pip install agentquay-sdk[pydantic]   复杂模型的 Schema 增强
```

**定义工具**：`agent_tool(name=None, description="", requires_confirmation=False,
timeout_seconds=30, confirm_timeout_seconds=120)`

- `name` 缺省回退到函数名；`description` 缺省回退到函数 docstring 首行。
- 方法可以是同步的或 `async` 的：同步函数在事件循环外（线程池）执行，天然支持
  阻塞的业务代码；`async` 函数直接 await。
- inputSchema 由 `inspect.signature()` + 类型注解自动生成（Python 3.10+），
  不需要手写 Schema。

```python
from agentquay import agent_tool

class MusicController:
    @agent_tool
    def search(self, keyword: str, limit: int = 10) -> list[dict]:
        """搜索音乐库"""          # description 缺省取这一行

    @agent_tool("delete", description="删除歌曲", requires_confirmation=True)
    async def delete(self, song_id: str) -> dict:
        ...
```

**客户端与连接**：`AgentQuayClient(app_id, app_name, host="127.0.0.1", port=0,
auto_spawn_bridge=True, version="1.0.0", heartbeat_interval=30, max_retry_interval=30,
launch_info=None, auto_report_launch=True, on_confirm=None)`

- `port=0`：从 `~/.agentquay/port` 自动读取真实端口（Bridge 端口漂移时也会自动跟随）。
- `register_tools(instance)` 返回 `self`，可链式调用；`list_tools()` 返回已注册工具名。
- `await client.connect()` **保持连接直到 `close()` 或同 appId 新实例替换**；
  支持 `async with AgentQuayClient(...) as client:` 语法。

```python
import asyncio
from agentquay import AgentQuayClient, agent_tool

async def main():
    client = AgentQuayClient(
        app_id="music-app",
        app_name="Music Player",
        auto_spawn_bridge=True,     # 默认即 True
    )
    client.register_tools(MusicController())
    await client.connect()

asyncio.run(main())
```

**运行示例**：`python examples/music_app.py`（在 `sdk/python` 下）。

**自定义确认**：传入 `on_confirm` 异步回调替代 OS 弹窗，签名
`async (message, arguments) -> bool`：

```python
async def my_confirm(message, arguments):
    print(f"确认请求: {message} {arguments}")
    return True   # 或 False 拒绝

client = AgentQuayClient(..., on_confirm=my_confirm)
```

### 5.2 TypeScript

**安装**

```bash
npm install @agentquay/sdk          # Node >= 18；运行时依赖仅 ws
```

可选对等依赖（缺省自动优雅降级）：`keytar`（系统钥匙串持久化 token，
否则落到 `~/.agentquay/tokens.json`）、`zod` + `zod-to-json-schema`（参数校验）。

**tsconfig**：走 tsc 编译需开启 `"experimentalDecorators": true`；
标准 TS 5 装饰器语义（esbuild/tsx/Babel）同样支持，不强制配置。

**定义工具**：`@AgentTool("name", { description?, requiresConfirmation?,
timeoutSeconds?, confirmTimeoutSeconds?, inputSchema? })`，`name` 缺省回退到方法名。

```typescript
import { AgentQuayClient, AgentTool } from "@agentquay/sdk";

class MusicController {
    @AgentTool("search", { description: "搜索音乐库中的歌曲" })
    search(keyword: string, limit = 10) { /* ... */ }

    @AgentTool("delete", { description: "删除歌曲", requiresConfirmation: true })
    delete(songId: string) { /* ... */ }
}
```

参数 Schema 的三种来源（TS 运行时没有类型信息，SDK 不发明参数装饰器）：

1. `@AgentTool({ inputSchema: {...} })` 直接给 JSON Schema；
2. `registerTools(Cls, { zodSchemas: { search: z.object({ keyword: z.string() }) } })`；
3. 自动从函数源码解析参数生成（空属性 Schema = 任意值，建议生产方式 1/2）。

**客户端与连接**：

```typescript
const client = new AgentQuayClient({
    appId: "music-app",
    appName: "Music Player",
    host: "localhost",
    port: 0,                 // 自动读取 ~/.agentquay/port
    autoSpawnBridge: true,   // 默认 true
});
client.registerTools(MusicController, { zodSchemas: { /* 可选 */ } });
await client.connect();      // 长连接：close() 时返回；ReplacedError 时拒绝
await client.close();
```

> Node 语义：`connect()` 不是"连上即返回"，它会保持进程连接（心跳 + 自动重连），
> 直到 `close()` 被调用才 resolve；中途被同 appId 实例替换会 reject `ReplacedError`。

**运行示例**：`npm run example`（`tsx examples/music_app.ts`）。

**自定义确认**：`onConfirm: (message, arguments, timeoutSeconds) => boolean | Promise<boolean>`。
默认确认是终端 `[y/N]`，非 TTY 环境默认拒绝；GUI 应用务必提供 `onConfirm`。

### 5.3 Java

**Maven**

```xml
<dependency>
  <groupId>io.github.wenbaow</groupId>
  <artifactId>agentquay-sdk</artifactId>
  <version>0.3.2</version>
</dependency>
```

- 需要 **Java 11+**，并**用 `-parameters` 编译**（pom 已开启；Gradle 需
  `options.compilerArgs += ['-parameters']`）——参数绑定靠参数名匹配。
- 零第三方传输依赖：WebSocket 用 JDK 内置 `java.net.http.WebSocket`；
  JSON Schema 用 Jackson；token 持久化可选 `keyring-java`（反射加载，缺省落文件）。

**定义工具**：`@AgentTool(value="", description="", requiresConfirmation=false,
timeoutSeconds=30, confirmTimeoutSeconds=120)` + `@AgentParam(description="", required=true)`。

```java
import com.agentquay.AgentTool;
import com.agentquay.AgentParam;
import java.util.*;

public class MusicController {
    @AgentTool(value = "search", description = "搜索音乐库中的歌曲")
    public List<Song> search(@AgentParam(description = "搜索关键词") String keyword) { ... }

    @AgentTool(value = "delete", description = "删除歌曲", requiresConfirmation = true)
    public Map<String, Object> delete(@AgentParam(description = "歌曲ID") String songId) { ... }

    @AgentTool(value = "async_echo", description = "异步回显")
    public CompletableFuture<Map<String, String>> asyncEcho(
            @AgentParam(description = "消息") String message) { ... }  // CompletableFuture 自动 unwrap
}
```

JSON Schema 由 Jackson 生成，识别基本类型、枚举、List/Set/数组、Map、
`Optional<T>`（自动可空且非必填）、嵌套 POJO（含 `@JsonProperty` 重命名、
`@JsonIgnore`）。`value` 缺省回退到方法名。

**客户端与连接**：

```java
AgentQuayClient client = AgentQuayClient.builder()
        .appId("music-app")
        .appName("Music Player")
        .host("127.0.0.1")
        .port(0)                    // 0 = 自动读取 ~/.agentquay/port
        .autoSpawnBridge(true)      // 默认 true
        .build();
client.registerTools(MusicController.class);   // 或 registerTools(new MusicController())
client.connect();                   // 阻塞保持连接；Swing/GUI 用 connectAsync()
```

- `registerTools(Class)` 自动无参实例化，支持静态方法和继承的 public 方法；
  `connect()` 阻塞（命令行应用），`connectAsync()` 返回 `CompletableFuture`（GUI 应用）。
- `listTools()` 返回工具名列表（仅名字）。
- 自定义确认：`.confirmHandler((message, arguments, timeoutSeconds) -> boolean)`，
  默认是 Swing 对话框（超时自动取消）/控制台。

**运行示例**（在 `sdk/java`）：

```bash
mvn -q compile exec:java -Dexec.mainClass=com.agentquay.example.MusicApp
```

### 5.4 C# / .NET

**NuGet**

```bash
dotnet add package AgentQuay.Sdk     # .NET 8+；零第三方 NuGet 依赖
```

Bridge 二进制以 `contentFiles` 随包分发，自动拷贝到应用输出目录，无需手动处理。

**定义工具**：`[AgentTool("name", Description=..., RequiresConfirmation=...,
TimeoutSeconds=30, ConfirmTimeoutSeconds=120)]` + `[AgentParam(Description=...,
Required=true)]`。注意属性拼写是 **`ConfirmTimeoutSeconds`**。

```csharp
using AgentQuay;

public sealed class MusicController
{
    private readonly List<Song> _songs = new() { /* ... */ };

    [AgentTool("search", Description = "搜索音乐库中的歌曲")]
    public List<Song> Search([AgentParam(Description = "搜索关键词")] string keyword) { ... }

    [AgentTool("delete", Description = "删除歌曲", RequiresConfirmation = true)]
    public Dictionary<string, object?> Delete([AgentParam(Description = "歌曲ID")] string songId) { ... }

    [AgentTool("async_echo", Description = "异步回显")]
    public async Task<Dictionary<string, string>> AsyncEcho([AgentParam(Description = "消息")] string message)
    {
        await Task.Yield();
        return new() { ["received"] = message };
    }
}
```

**客户端与连接**（静态工厂，构造方法是私有的）：

```csharp
var client = await AgentQuayClient.ConnectAsync(
    appId: "music-app",
    appName: "Music Player",
    host: "localhost",
    port: 0,
    autoSpawnBridge: true);

client.RegisterTools(new MusicController());   // 或 RegisterTools<MusicController>() / RegisterTools(Type)
Console.WriteLine("已注册 tools: " + string.Join(", ", client.ListTools()));
await client.StartAsync();                     // 阻塞直至 DisposeAsync / 被替换 / 取消
await client.DisposeAsync();
```

- `RegisterTools` 支持泛型、`Type`、实例三种重载，返回 `client` 可链式调用；
  只扫描 public 实例/静态方法；`Task`/`Task<T>`/`ValueTask` 自动 await 解包。
- 默认确认：反射加载的 WinForms 对话框（无 GUI 时控制台输入）；自定义确认传
  `confirmationHandler: (message, args, timeoutSeconds) => bool`，签名
  `ConfirmationHandler(string, IReadOnlyDictionary<string, object?>?, int)`。
- 扫描实例不可实例化时会抛 `InvalidOperationException`；已 `DisposeAsync` 的客户端
  不可重启。

**运行示例**（在 `sdk/dotnet`）：`dotnet run --project examples/MusicApp`。

### 5.5 C++（Qt）

**依赖与构建**

- Qt **6.5+**（`Core`/`Network`/`WebSockets`；`Widgets` 可选，用于 GUI 确认框）、
  C++17、CMake ≥ 3.16。v1 以源码分发（`find_package(agentquay)` 或直接引用）。
- Qt 6.5 起 WebSockets 是独立模块，用 aqt 安装时需带 `-m qtwebsockets`。
- MSVC 需 `/utf-8`（CMake 已自动添加）。

**定义工具**：两个宏，`methodName` 传**裸标识符**（会被字符串化并用于拼接注册名），
`description` 传字符串字面量：

```cpp
#include <agentquay/agent_tool.h>
#include <QObject>

class MusicController : public QObject {
    Q_OBJECT
public:
#ifndef Q_MOC_RUN
    AGENT_TOOL(search, "搜索音乐库中的歌曲")
#endif
    Q_INVOKABLE QVariantList search(const QString& keyword);

#ifndef Q_MOC_RUN
    AGENT_TOOL_OPTS(deleteSong, "删除歌曲（危险操作，需要用户确认）",
                    agentquay::AgentToolOptions{true})   // requiresConfirmation = true
#endif
    Q_INVOKABLE QVariantMap deleteSong(const QString& songId);
};
```

- `AGENT_TOOL(name, description)` 使用默认选项；`AGENT_TOOL_OPTS(name, description,
  agentquay::AgentToolOptions{...})` 第三个参数是 C++17 聚合（按成员顺序，
  `{requiresConfirmation, timeoutSeconds, confirmTimeoutSeconds}`）。
- 宏必须紧贴对应的 `Q_INVOKABLE` 方法；旧版 moc 需用 `#ifndef Q_MOC_RUN` 包起来。

**客户端与连接**：

```cpp
#include <agentquay/AgentQuayClient.h>

int main(int argc, char* argv[]) {
    QCoreApplication app(argc, argv);

    agentquay::AgentQuayClient client(QStringLiteral("music-app"),
                                      QStringLiteral("Music Player"));
    client.setPort(0);                // 0 = 从 ~/.agentquay/port 自动读取
    client.setAutoSpawnBridge(true);  // 未检测到服务时自动拉起内嵌 Bridge

    client.registerTools<MusicController>();   // 或 registerTools(QObject* 实例)
    qInfo() << "已注册 tools:" << client.listTools();

    client.connect();                 // 异步连接，与 Qt 事件循环共存
    return app.exec();                // 事件循环驱动 WebSocket 与调用分发
}
```

- 配置都通过 setter（`setHost`/`setPort`/`setAutoSpawnBridge`/`setConfirmHandler`/
  `setLaunchInfo`/...），必须在 `connect()` 前设置。
- `connect()` 非阻塞；阻塞等到注册完成可用 `waitForConnected(int timeoutMs)`。
- Tool 方法在 `QThreadPool` 工作线程执行，**必须是线程安全的**；确认回调在
  主线程同步执行。方法抛 `std::runtime_error` 会以业务错误 `EXECUTION_ERROR`
  回传给 Agent。
- 非 Qt fallback：`client.addTool(name, handler, description, requiresConfirmation,
  timeoutSeconds, confirmTimeoutSeconds, inputSchema)`，无需 `Q_OBJECT`/`Q_INVOKABLE`。

**构建示例**（在 `sdk/cpp`）：

```bash
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j
```

Windows/VS2022 环境可一键：`powershell -ExecutionPolicy Bypass -File sdk/cpp/_build.ps1`。

### 5.6 Rust

**Cargo.toml**

```toml
[dependencies]
agentquay = { path = "sdk/rust/crates/agentquay" }   # crates.io 发布后: agentquay = "0.1"
tokio = { version = "1", features = ["rt-multi-thread", "macros", "time"] }
serde = { version = "1", features = ["derive"] }
schemars = { version = "1", features = ["derive"] }
```

features：`default = ["embedded-bridge"]`（内嵌 Bridge 二进制）；`keyring`（系统
钥匙串持久化 token，默认关因为 MinGW 下编译受限）；`native-confirm`（rfd OS 原生
确认框，默认控制台）。用户自定义的参数/返回结构体需要手动 `derive` `serde` 和
`schemars::JsonSchema`（宏生成的代码直接引用 `::serde::`/`::schemars::` 路径）。

**定义工具**：`#[agent_tool]` 标注 `impl` 块 + 块内方法逐个标注（推荐）：

```rust
use agentquay::{agent_tool, AgentQuayClient, ToolError};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, agentquay::JsonSchema)]
struct Song { id: String, title: String, artist: String }

struct MusicController { library: Vec<Song> }

impl MusicController {
    fn new() -> Self { /* ... */ }
}

#[agent_tool]
impl MusicController {
    /// 搜索音乐库中的歌曲        // description 缺省取 doc 首行
    #[agent_tool(name = "search")]
    fn search(&self, keyword: String) -> Vec<Song> { /* ... */ }

    #[agent_tool(name = "play", description = "播放指定歌曲")]
    fn play(&self, song_id: String) -> Result<String, String> { /* ... */ }

    #[agent_tool(name = "delete", description = "删除歌曲", requires_confirmation = true)]
    fn delete(&self, song_id: String) -> Result<(), ToolError> {
        Err(ToolError::business("SONG_NOT_FOUND", format!("歌曲不存在: {song_id}"), None))
    }

    #[agent_tool(name = "stats", description = "统计音乐库")]
    async fn stats(&self) -> Result<agentquay::serde_json::Value, ToolError> { /* ... */ }
}
```

规则（宏编译期强制）：方法接收者只能是 `&self`；可选参数必须是 `Option<T>`，
可用 `#[param(required = false)]` / `#[param(description = "...")]` 标注；返回值
可以为任意 `Serialize` 类型，`Result<T, ToolError>` 透传 `code`/`details`，其它
`Result<T, E>` 包装为 `EXECUTION_ERROR`。也支持自由函数模式（自动生成
`<fn>_tool` 载体结构体）。参数 Schema 由 **schemars** 从参数类型自动生成（draft07），
零手写。

**客户端与连接**：

```rust
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = AgentQuayClient::builder()
        .app_id("music-app")
        .app_name("Music Player")
        .build()?;
    // 按值注册（非引用）；IntoProviders 支持元组一次注册多个：
    // register_tools((MusicController::new(), other_provider))?
    client.register_tools(MusicController::new())?;
    println!("已登记 tools: {:?}", client.list_tools());
    client.connect().await?;   // 阻塞保持连接；close() 返回 Ok，被替换返回 Err(Replaced)
    Ok(())
}
```

- 工具必须**在 `connect()` 之前**注册（协议上一次连接只注册一次）。
- 还有 `register_fn(name, description, closure)` / `register_fn_async(...)` 用于
  手动注册闭包。

**运行示例**（在 `sdk/rust`）：`cargo run -p music_app`。

---

## 6. 进阶主题

### 6.1 危险操作确认

给工具加 `requires_confirmation=True`（Rust 里裸写 `requires_confirmation` 也等于
`true`）。Agent 调用时，Bridge **不直接执行**，而是：

1. 先向应用发 `confirm` 消息（含 message、arguments、timeoutSeconds，默认 120s）。
2. 应用弹 OS 原生确认框（各语言默认实现见下表），计时独立于执行超时。
3. Agent 侧等待期间每 30s 收到一次 MCP 进度通知（`notifications/progress`）。
4. 用户确认 → Bridge 才转发 `invoke` 执行；用户取消或确认超时 → Agent 收到
   `-32005`。

| 语言 | 默认确认实现 | 自定义方式 |
|------|--------------|-----------|
| Python | OS 原生弹窗 | `on_confirm` 异步回调 `(message, arguments) -> bool` |
| TypeScript | 终端 `[y/N]`（非 TTY 默认拒绝） | `onConfirm` 回调，GUI 应用必须提供 |
| Java | Swing 对话框（超时自动取消）/ 控制台 | `.confirmHandler((msg, args, timeout) -> bool)` |
| C# | WinForms 对话框（反射加载）/ 控制台 | `confirmationHandler` 委托 |
| C++ | QMessageBox（有 Widgets）/ 控制台 | `setConfirmHandler` |
| Rust | 控制台 y/n（`native-confirm` feature 走 rfd 对话框） | `confirm_handler` |

### 6.2 认证与 token 钉扎

- 首次注册时 Bridge 返回 `aq_`+64hex token，SDK 持久化到系统钥匙串（Windows
  Credential Manager / macOS Keychain / libsecret）或回退文件 `~/.agentquay/tokens.json`。
- 之后每次重连自动携带 token；token 错/缺失注册会被拒绝（`AUTH_FAILED` →
  `AuthFailedError`），防止伪造实例顶替。
- 同 appId 的新实例上线时，旧连接收到 `disconnect reason=replaced`，SDK 停止重连
  并抛出 `ReplacedError`（这是 MCP 侧 `-32007`）。
- 轮换 token：

```bash
agentquay rotate-token <appId>
```

轮换后应用重启重连时带着新 token 才能通过（SDK 的持久化 token 也会在下次
注册 ack 时被替换）。

### 6.3 超时与孤儿结果

- **执行超时**：`timeout_seconds`（默认 30s），在 Bridge 侧独立计时。
- SDK 侧的执行超时 = Bridge 超时 + 5s 余量，确保 **Bridge 先超时**，SDK 迟到的
  结果进入 Bridge 的**孤儿结果环形缓冲**（100 条），不视为错误。
- Agent 收到 `-32004` 表示超时（应用可能仍在执行）。

### 6.4 启动命令上报与离线自动拉起

SDK 注册时随 `launch` 字段上报应用启动命令（`LaunchInfo`：execPath / args /
cwd / singleInstance / launchTimeoutSeconds）。这样：

- Bridge 离线也能在 `tools/list` 里看到已登记的 `{appId}_{tool}`（描述前缀
  `[未运行] [应用名]`）；
- Agent 直接调用离线应用的工具时，Bridge 自动拉起进程并等待 SDK 注册（默认
  15s），再走正常校验/确认/调用流程——"打开微信 → 微信启动并执行"自然成立。

各语言默认会自动探测启动命令（Python `sys.executable`、Java `ProcessHandle`、
C# `Environment.ProcessPath`、C++ `applicationFilePath()`、Rust `current_exe()`
等），`auto_report_launch`/`autoReportLaunch` 默认 `true`，也可显式传 `launch_info`
覆盖。`agentquay apps` 系列命令可管理这个注册表（见 §8）。

### 6.5 错误处理（错误码）

| 码 | 含义 |
|----|------|
| -32001 | 应用未连接 |
| -32002 | Tool 不存在（离线应用可能更新过工具表，重试 `tools/list`） |
| -32003 | 参数无效（Schema 校验失败，**不转发**给应用） |
| -32004 | 执行超时（应用可能仍在执行） |
| -32005 | 用户取消（确认被拒或确认超时） |
| -32007 | 应用被替换（新实例注册） |

> 实现说明：mcp-go 将工具错误一律包装为 `-32603`，因此 Bridge 按 MCP SEP-1303
> 返回 `isError`，错误码以结构化 JSON 承载在 content 中（如
> `{"code":-32004,"message":"执行超时（…）"}`）；Tool 不存在时协议层返回 `-32602`。

各 SDK 的异常体系（语言命名略有差异，语义一致）：

- 基础：`AgentQuayError`（基类）
- `BridgeUnavailableError` / `BridgeSpawnError`：Bridge 不可用 / auto-spawn 失败
- `BridgeConnectionError`（或 `ConnectionException`）：连接断开
- `RegistrationError`（含 `code`）：注册被拒；`AuthFailedError`：token 认证失败
- `ReplacedError`：同 appId 实例替换
- `ProtocolError`：协议异常
- `ConfirmationError`：确认流程异常
- `ToolCallError`（含 `code`/`details`）：工具调用侧错误

Rust 侧还提供 `ToolError::business(code, message, details)` 用于从业务代码透传
自定义错误码给 Agent。

### 6.7 工具调用钩子（on_tool_call）

SDK 提供工具调用钩子，允许在业务方法执行前拦截调用，方便 UI 层响应。

```python
def on_tool_called(tool_name: str, arguments: dict):
    if tool_name == "navigate_page":
        category = arguments.get("category", "")
        direction = arguments.get("direction", "next")
        # 切到主线程更新 UI
        loop.call_soon(ui.navigate, category, direction)

client = AgentQuayClient("admin-system", "后台管理系统")
client.on_tool_call = on_tool_called  # 注册钩子
```

各语言 SDK 均支持此钩子：

| 语言 | 钩子名称 | 签名 |
|------|---------|------|
| Python | `on_tool_call` | `(tool_name: str, arguments: dict) -> None` |
| TypeScript | `onToolCall` | `(toolName: string, arguments: Record<string, unknown>) => void` |
| Java | `toolCallHandler` | `ToolCallHandler.onToolCall(String, Map<String, Object>)` |
| C# | `toolCallHandler` | `ToolCallHandler(string, IReadOnlyDictionary<string, object?>?)` |
| C++ | `setToolCallHandler` | `std::function<void(const QString&, const QVariantMap&)>` |
| Rust | `tool_call_handler` | `dyn ToolCallHandler(on_tool_call(&str, Option<&Value>))` |

典型用例：翻页导航。Agent 说"下一页"时，通过参数表达上下文：

```python
class AdminController:
    @agent_tool("navigate_page", description="在指定类目翻页")
    def navigate_page(self, category: str, direction: str = "next") -> dict:
        # 业务逻辑：更新状态、加载数据
        pass
```

```python
# UI 层监听并响应
def on_tool_called(name: str, args: dict):
    if name == "navigate_page":
        loop.call_soon(ui.navigate, args["category"], args.get("direction", "next"))

client.on_tool_call = on_tool_called
```

> 钩子执行异常不影响业务方法：SDK 会捕获并记录警告，继续执行原流程。

### 6.9 页面智能路由（页面级懒激活）

> 设计文档：《页面智能路由方案》。适用于**多页桌面应用**：页面未打开时，该页面的
> 工具也可以被 Agent 发现并调用——SDK 自动创建页面 → 导航 → 等待就绪 → 执行方法。

核心行为（`pageKey` 只是 SDK 内部的分组标签，不进协议、Agent 无感知）：

| 场景 | SDK 行为 |
|------|---------|
| 页面已打开 | 直接调用该实例（UI 线程，异步让出不阻塞） |
| 页面未打开但有工厂 | 单飞去创建 → 导航 → 等就绪 → 调用（默认 15s 激活超时，失败可重建） |
| 页面未打开且无工厂 | 返回 `PAGE_NOT_FOUND`（工具仍在 `tools/list` 中） |
| 调用期间页面被关闭 | 弱引用失效 → 有工厂则重建，无则明确错误 |

```python
# Python：惰性注册 + 激活钩子
client.register_tools(SearchPage, page_key="SearchPage")        # 类型级惰性注册
client.register_tools_factory(lambda: di.get(PlayerPage),       # 显式工厂（DI）
                              "PlayerPage")                     #   需标注返回类型
client.set_page_activator("SearchPage",
    navigate=lambda page: main_window.navigate_to(page),        # 导航（可协程）
    await_ready=lambda page: wait_loaded(page))                 # 就绪等待（必须异步）
```

```csharp
// C#（WPF）：UI 线程调度由扩展包 AgentQuay.Sdk.Wpf 提供
client.RegisterTools<SearchPage>(pageKey: "SearchPage");
client.SetUIThreadDispatcher(new WpfDispatcher());
client.SetPageActivator("SearchPage",
    navigate: page => MainWindow.NavigateTo((SearchPage)page),
    awaitReady: async page => await PageLoadedAsync((FrameworkElement)page));
```

TypeScript / Java 同构：`registerTools(cls, { pageKey })` /
`registerToolsFactory(factory, pageKey)`、`registerTools(Class, pageKey)` /
`registerToolsFactory(Class, factory, pageKey)`；激活钩子 `setPageActivator` /
`setPageActivator(pageKey, navigate, awaitReady)`；显式注销 `unregister_page` /
`unregisterPage`（不调也行，弱引用 GC 失效后工厂路径自动重建）。

**注意**：同一 appId 内工具名跨页面全局唯一；`tools/list` 描述带
`[AppName|PageKey]` 前缀；危险工具请求确认时，确认文案会注明"将在应用中打开页面"。

### 6.10 内置工具 app_*
|------|------|
| `app_list` | 查询应用注册表（在线/离线/可启动） |
| `app_launch` | 按名启动应用 |
| `app_search` | 在本机搜索可启动应用（开始菜单/.desktop） |
| `app_adopt` | 显式登记一个应用的安装位置 |

内置工具用 `app_` 前缀（appId 规范禁止 `_`，天然无冲突；冲突时内置工具优先）。

---

## 7. 把 AI Agent 接进来（MCP）

**标准方式（Streamable HTTP）**：把 MCP 端点配成

```
http://127.0.0.1:19846/mcp
```

适合所有支持 MCP Streamable HTTP 的 Agent（opencode / codex / zcode /
Claude Desktop / Cursor 等）。

**调试方式（stdio）**：

```bash
agentquay mcp --stdio
```

**生成 Agent 配置文件**：

```bash
agentquay generate-agent-config --output claude_desktop_config.json
```

生成内容相当于：

```json
{
  "mcpServers": {
    "agentquay": {
      "command": "agentquay",
      "args": ["mcp", "--stdio"]
    }
  }
}
```

---

## 8. Bridge 管理与运维

```bash
agentquay start [--daemon]         # 启动（前台；--daemon 后台运行）
agentquay stop                     # 停止
agentquay status                   # 状态：端口、协议版本、已注册应用、孤儿结果数
agentquay list-apps                # 查看在线应用
agentquay apps list                # 查看已登记应用（含离线与启动能力）
agentquay apps add --appId <id> --name <名称> --path <可执行文件> [--args "…"] [--auto-launch on|confirm|off]
agentquay apps remove <appId>      # 移除应用登记
agentquay apps search <关键字>      # 本机搜索可启动应用
agentquay apps launch <appId|名称> [--no-wait]   # 启动并默认等待 SDK 注册
agentquay apps set-auto-launch <appId> <on|confirm|off>
agentquay logs [--tail N]          # 日志（默认最近 100 行）
agentquay rotate-token <appId>     # 轮换 token
agentquay mcp --stdio              # stdio MCP（调试）
agentquay version                  # 版本与协议版本
agentquay generate-agent-config    # 生成 Agent 配置文件
```

配置目录：`~/.agentquay/`（`config.json` / `auth.json` / `apps/` / `port` /
`agentquay.log`）。SDK 内嵌模式（`serve --embedded`）日志写入应用缓存目录
（`AGENTQUAY_LOG_DIR`），不触碰全局配置。

---

## 9. 测试与验证

```bash
# Go 编译 + 静态检查
cd bridge && go vet ./... && go build ./...

# Python SDK 单测（18 项）+ 端到端联调（25 项，真实 Bridge + SDK + MCP 客户端）
cd sdk/python && python -m unittest discover -s tests && python tests/e2e_test.py

# TypeScript SDK 单测（17 项）+ 端到端联调（25 项）+ 冒烟
cd sdk/typescript && npm test && npm run test:e2e && npm run smoke

# Java SDK 单测（11 项）+ 端到端联调（7 项，真实 Go Bridge）
cd sdk/java && mvn test

# C# SDK 单测（7 项）+ 端到端联调（1 项全流程，真实 Go Bridge）
cd sdk/dotnet && dotnet test tests/AgentQuay.Sdk.Tests

# C++ SDK 单测（12 项）+ 端到端联调（9 项，真实 Go Bridge；需 Qt 6.5+ 与 Ninja）
powershell -ExecutionPolicy Bypass -File sdk/cpp/_build.ps1

# Rust SDK 单测（17 项）+ 宏集成测试（3 项）+ 端到端联调（1 项全流程，真实 Go Bridge）
cd sdk/rust && cargo test --workspace && cargo test -p agentquay --test e2e_test
```

端到端覆盖：注册/token、`tools/list`、正常调用、`-32003` 参数校验、业务错误透传、
确认流程（确认/取消）、`-32004` 超时 + 孤儿缓冲、同 appId 替换、SSE
`list_changed`、断线重连携带 token、`rotate-token` 旧 token 拒绝。

---

## 10. 约定、限制与常见问题

**命名与校验**

- `appId`：`[a-z0-9-]{1,48}`，**只允许小写字母/数字/连字符，禁止 `_` 和 `.`**
  （`_` 保留用于 MCP 工具名拼接：`{appId}_{toolName}`）。非法会抛
  `IllegalArgumentException` / `ValueError`。
- tool 名：`[a-zA-Z0-9_-]{1,78}`（允许 `_`），同客户端内不能重复。
- MCP 侧看到的是合成名 `{appId}_{toolName}`，如 `music-app_search`。

**各语言易踩的坑**

| 语言 | 注意事项 |
|------|----------|
| Python | 同步方法在线程池执行，注意线程安全；复杂嵌套模型可配 Pydantic 增强 Schema |
| TypeScript | tsc 需 `experimentalDecorators`；`connect()` 是长连接语义，不是即连即返；没有参数装饰器，Schema 用 `inputSchema`/`zodSchemas` |
| Java | 必须 `-parameters` 编译，否则参数名退化为 `argN`；`listTools()` 只返回名字；只有 `CompletableFuture` 会被自动 unwrap |
| C# | 属性名是 `ConfirmTimeoutSeconds`；只扫描 public 方法；实例化失败会抛 `InvalidOperationException`；`DisposeAsync` 后不可重启 |
| C++ | 宏第一个参数是裸标识符（非字符串）；`requiresConfirmation` 要 `AGENT_TOOL_OPTS`；`connect()` 是异步的，靠 `app.exec()` 事件循环驱动；tool 在 worker 线程执行须线程安全 |
| Rust | `register_tools` 按值传参保有 `ToolProvider`（`new()`/`clone()` 的实例）；只有 `&self` 方法；可选参数必须是 `Option<T>`；自定义结构体需手动 derive `serde`/`schemars`；工具须在 `connect()` 前注册 |

**常见问题**

- **SDK 没装 Bridge 也能跑？** 能。`auto_spawn_bridge=True`（默认）时 SDK 自动
  以 `serve --embedded` 拉起内嵌二进制；如果拉取失败会明确报错并提示两条出路：
  安装系统服务（`agentquay start --daemon`）或手动 `agentquay start`。
- **端口被占用？** Bridge 支持 autoPort（19846–19856 自动漂移），真实端口写入
  `~/.agentquay/port`。SDK 用 `port=0` 自动读取，重连失败时也会重读端口文件跟随
  漂移。
- **连接反复断开？** 30s 无消息视为心跳超时，SDK 自动指数退避重连并携带 token；
  若日志报 `AUTH_FAILED`，用 `agentquay rotate-token <appId>` 轮换后重启应用。
- **怎样让 Agent 在应用未启动时也能调用？** 应用运行过一次（SDK 上报了
  `launchInfo`）或执行过 `agentquay apps add` 后即登记。之后 Bridge 在工具离线时
  自动拉起应用再执行；离线工具的调用流程无需感知启动过程。自动拉起策略可用
  `agentquay apps set-auto-launch <appId> on|confirm|off` 调整。

**许可**：全项目（各语言 SDK 与 Bridge）统一为 Apache-2.0，宽松许可，便于嵌入第三方
应用（见根目录 `LICENSE` 与 `NOTICE`）。
