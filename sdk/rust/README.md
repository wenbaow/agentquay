# AgentQuay Rust SDK（`agentquay`，设计文档 §4.8）

让 AI Agent 通过标准 MCP 协议发现并调用桌面应用方法的 Rust SDK。职责：
`#[agent_tool]` 标记 Tool、注册、心跳、调用分发、确认流程、指数退避重连、
token 持久化、auto-spawn 内嵌 Bridge。

```rust
use agentquay::{agent_tool, AgentQuayClient};
use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize, agentquay::JsonSchema)]
struct Song { id: String, title: String, artist: String }

struct MusicController { library: Vec<Song> }

#[agent_tool]
impl MusicController {
    /// 搜索音乐库中的歌曲（description 缺省回退 doc 首行）
    #[agent_tool(name = "search")]
    fn search(&self, keyword: String) -> Vec<Song> { /* 业务逻辑 */ }

    /// 播放指定歌曲
    #[agent_tool(name = "play", description = "播放指定歌曲")]
    fn play(&self, song_id: String) -> Result<String, String> { /* ... */ }

    /// 删除歌曲（危险操作：Agent 调用时 Bridge 先向应用弹确认）
    #[agent_tool(name = "delete", description = "删除歌曲", requires_confirmation = true)]
    fn delete(&self, song_id: String) -> Result<(), agentquay::ToolError> { /* ... */ }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = AgentQuayClient::builder()
        .app_id("music-app")
        .app_name("Music Player")
        .build()?;
    client.register_tools(MusicController { library: vec![] })?; // connect() 之前注册
    client.connect().await?; // 保持连接，监听 Agent 调用
    Ok(())
}
```

## 特性

| 能力 | 说明 |
|------|------|
| `#[agent_tool]` 过程宏 | impl 块模式 + 自由函数模式（`fn search` → `search_tool` 注册载体） |
| JSON Schema 自动生成 | `schemars` 从参数类型生成（draft-07，`definitions` 兼容 Bridge 校验器） |
| 同步 / async 方法 | 同步方法 `spawn_blocking` 执行，不阻塞异步运行时；panic 兜底为 `EXECUTION_ERROR` |
| 参数绑定 | 按参数名从 JSON arguments 反序列化；`Option<T>` 参数可选；`#[param(description = "...")]` 进 schema |
| 错误映射 | `Result<T, ToolError>` 透传 code/details；`Result<T, E>` 包装为 `EXECUTION_ERROR` |
| 确认流程 | `requires_confirmation` 时回调确认处理器（默认控制台 y/n；`native-confirm` 特性开原生弹窗） |
| token 钉扎 | 优先系统钥匙串（特性 `keyring`），回退 `~/.agentquay/tokens.json`（与 Java/Python 同格式） |
| auto-spawn | 未检测到 Bridge 时解压内嵌二进制（`bridge_bin/`，按 target 条件 `include_bytes!`）以 `serve --embedded` 拉起 |
| 心跳重连 | 30s 心跳（2 周期判定死亡）；指数退避重连 + 端口漂移自动重读 |
| 免宏注册 | `register_fn` / `register_fn_async` 注册闭包 Tool |

## 快速开始

### 1. 加入依赖

```toml
[dependencies]
agentquay = { path = "sdk/rust/crates/agentquay" }   # 或 crates.io 分发后: agentquay = "0.1"
tokio = { version = "1", features = ["rt-multi-thread", "macros", "time"] }
# 用户类型的 derive 需要（derive 宏生成 ::serde:: / ::schemars:: 路径，与任何基于它们的库一致）
serde = { version = "1", features = ["derive"] }
schemars = { version = "1", features = ["derive"] }
```

### 2. 标记 Tool

```rust
#[agent_tool]
impl MusicController {
    #[agent_tool(name = "search", description = "搜索音乐库", requires_confirmation = false)]
    fn search(&self, keyword: String) -> Vec<Song> { ... }

    // 属性参数（均为可选）：
    //   name                    缺省回退方法名
    //   description             缺省回退 doc 注释首行
    //   requires_confirmation   危险操作（默认 false）
    //   timeout_seconds         执行超时（默认 30）
    //   confirm_timeout_seconds 确认超时（默认 120，独立于执行超时）
}
```

参数声明规则：

- 参数类型需要 `Deserialize`；返回值类型需要 `Serialize`（可选参数必须 `Option<T>`）
- `#[param(description = "...")]` 给参数加描述；`#[param(required = false)]` 仅对 `Option<T>` 合法
- 方法接收者仅支持 `&self`（同步或 `async fn`）

**自由函数模式**（设计文档 §4.8 的形态）：

```rust
/// 计算两点距离
#[agent_tool(name = "distance", description = "两点距离")]
fn distance(x1: f64, y1: f64, x2: f64, y2: f64) -> f64 { ... }

client.register_tools(distance_tool)?;              // fn distance → distance_tool
client.register_tools((search_tool, play_tool))?;   // 元组批量注册
```

### 3. 连接

```rust
let client = AgentQuayClient::builder()
    .app_id("music-app")        // [a-z0-9-]{1,48}，禁止 _ 和 .
    .app_name("Music Player")   // Agent 侧 tools/list 描述带 [应用名] 前缀
    // .host("127.0.0.1")       // Bridge 地址（默认）
    // .port(0)                 // 0 = 从 ~/.agentquay/port 自动读取
    // .auto_spawn_bridge(true) // 未检测到服务时自动拉起内嵌 Bridge（默认）
    // .heartbeat_interval(30)  // 心跳间隔秒数
    // .max_retry_interval(30)  // 重连退避上限秒数
    // .confirm_handler(handler)// 自定义确认回调（Arc<dyn ConfirmationHandler>）
    .build()?;
client.register_tools(MusicController::new())?;     // 必须在 connect() 之前
client.connect().await?;                            // 阻塞保持连接；替换/认证失败返回 Err
```

`connect()` 返回：

- `Ok(())` — 调用了 `close()`
- `Err(AgentQuayError::Replaced)` — 被同 appId 的新实例替换（不再重连）
- `Err(AgentQuayError::AuthFailed)` — token 钉扎失败（可能被轮换，不重试）

## Cargo 特性

| 特性 | 默认 | 说明 |
|------|------|------|
| `embedded-bridge` | ✅ | 按 target 内嵌 Bridge 二进制（windows-amd64 / linux-amd64 / darwin-arm64），关闭后回退 PATH 查找 |
| `keyring` | ❌ | token 持久化优先系统钥匙串；关闭时仅用 `~/.agentquay/tokens.json`。注意：keyring 的加密依赖在部分工具链（如 MinGW 无 dlltool）下无法编译 |
| `native-confirm` | ❌ | 确认弹窗用 OS 原生对话框（rfd）；默认控制台 y/n |

## 目录结构

```
sdk/rust/
├── Cargo.toml                    # workspace（agentquay + agentquay-macros + music_app）
├── crates/
│   ├── agentquay/                # SDK 主 crate
│   │   ├── bridge_bin/           # 内嵌 Bridge 二进制（三平台，随包分发）
│   │   └── src/
│   │       ├── client.rs         # AgentQuayClient（注册/心跳/调用分发/确认/重连）
│   │       ├── protocol.rs       # WS 消息格式（§3.1、附录 A）
│   │       ├── tool.rs           # ToolMetadata / ToolError / ToolProvider / IntoProviders
│   │       ├── schema.rs         # 过程宏的 schema 运行时辅助（__private）
│   │       ├── spawn.rs          # auto-spawn：端口文件/TCP 探测/内嵌二进制
│   │       ├── token.rs          # token 持久化（keyring + 文件回退）
│   │       ├── confirm.rs        # 确认回调（控制台默认 / rfd 可选）
│   │       ├── error.rs          # AgentQuayError
│   │       └── home.rs           # 主目录解析（优先环境变量，与 Go os.UserHomeDir 一致）
│   ├── agentquay-macros/         # #[agent_tool] 过程宏（syn/quote + schemars）
│   └── agentquay/tests/          # macro_test（3 项）+ e2e_test（真实 Bridge 全流程）
└── examples/music_app/           # MusicApp 示例
```

## 验证

```bash
cd sdk/rust

# 编译 + 单测（17 项）+ 宏集成测试（3 项）
cargo test -p agentquay

# 端到端联调（1 项全流程：真实 Bridge + SDK + raw MCP 客户端）
# 自动使用仓库 bridge/dist 下的二进制；可用 AGENTQUAY_BRIDGE_BIN 指定
cargo test -p agentquay --test e2e_test

# 整个 workspace（含示例）
cargo test --workspace

# 运行示例（auto-spawn 自动拉起内嵌 Bridge）
cargo run -p music_app
```

端到端覆盖：注册/token、tools/list、正常调用（同步/异步）、-32003 参数校验、
业务错误透传（ToolError code / EXECUTION_ERROR / panic 兜底）、确认流程（确认/取消 -32005）、
同 appId 替换（Replaced）、断线重连携带 token。

> **Windows GNU 工具链注意**：若使用 x86_64-pc-windows-gnu（MinGW）目标，链接需要
> `dlltool`（rustc 对 raw-dylib / 导入库生成会调用它）。安装 MSYS2 的
> `mingw-w64-ucrt-x86_64-binutils` 并加入 PATH 即可（MSVC 工具链无此问题）。

## 与设计文档的差异说明

1. **`register_tools(&MusicController)` → `register_tools(MusicController)`**：Rust 无法把
   零散自由函数与结构体关联（`&MusicController` 临时引用也无法 'static 存储），故
   impl 块模式按值注册（内部转为 `Arc<dyn ToolProvider>`）。
2. **自由函数模式**生成 `<fn名>_tool` 单元结构体作为注册载体（如 `fn search` →
   `search_tool`），可用元组批量注册。
3. **可选参数必须是 `Option<T>`**（schema 与反序列化严格一致，缺省参数在编译期报错）。
4. **用户类型需 `serde`/`schemars` 直接依赖**（derive 宏生成 `::serde::`/`::schemars::` 路径）。

## 许可

Apache-2.0
