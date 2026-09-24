# AgentQuay

![License](https://img.shields.io/github/license/wenbaow/agentquay)
![Release](https://img.shields.io/github/v/release/wenbaow/agentquay)
![Stars](https://img.shields.io/github/stars/wenbaow/agentquay)
![Forks](https://img.shields.io/github/forks/wenbaow/agentquay)
![Issues](https://img.shields.io/github/issues/wenbaow/agentquay)
![Last commit](https://img.shields.io/github/last-commit/wenbaow/agentquay)
![Top language](https://img.shields.io/github/languages/top/wenbaow/agentquay)
![SDKs](https://img.shields.io/badge/SDK-Python%20%7C%20TypeScript%20%7C%20Java%20%7C%20.NET%20%7C%20C%2B%2B%20%7C%20Rust-brightgreen.svg)
![Platform](https://img.shields.io/badge/Platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey.svg)
![PyPI](https://img.shields.io/pypi/v/agentquay-sdk)
![npm](https://img.shields.io/npm/v/@agentquay/sdk)
![crates.io](https://img.shields.io/crates/v/agentquay)
![NuGet](https://img.shields.io/nuget/v/AgentQuay.Sdk)
![Maven Central](https://img.shields.io/maven-central/v/io.github.wenbaow/agentquay-sdk)

**让 AI Agent 通过标准 MCP 协议发现并调用桌面应用方法的跨平台框架。**

开发者只需在应用方法上加注解（`@agent_tool`），一个常驻后台服务（Bridge）负责
注册中心、token 钉扎认证、消息路由与应用自动拉起；AI Agent（opencode / codex /
zcode / Claude Desktop 等）通过标准 MCP 协议发现并调用这些方法——**零安装、零配置**：
首次连接时 SDK 自动拉起内嵌 Bridge，无需手动安装任何东西。

> 一句话总结：**一个 Go 实现的跨平台持久服务做 MCP Server + 消息总线，各语言轻量 SDK
> 让桌面应用通过注解暴露方法，AI Agent 通过标准 MCP 协议间接控制任何桌面应用。**
> 开发者只管写业务方法加注解，其余全部由框架处理。

- **SDK 使用教程**：见 [`sdk-tutorial.md`](sdk-tutorial.md)（六种语言完整用法）

---

## Overview

AgentQuay lets AI agents discover and call methods inside desktop apps through the standard
[MCP](https://modelcontextprotocol.io) protocol — with **zero installation and zero
configuration**. You only annotate the methods you want to expose (`@agent_tool`), and a
persistent cross-platform daemon — the **AgentQuay Bridge** (written in Go) — handles
registration, token-pinned auth, message routing, and auto-launching apps. AI agents
(opencode, codex, zcode, Claude Desktop, …) then find and call those methods via MCP, just
like any other MCP server. On first connect the SDK auto-spawns the embedded Bridge —
nothing to install, nothing to configure.

**What's in this repository:**

| Path | What it is |
|------|------------|
| `bridge/` | The Bridge daemon (Go): MCP server + registration/routing bus, single endpoint `http://127.0.0.1:19846/mcp` |
| `sdk/python` · `sdk/typescript` · `sdk/java` · `sdk/dotnet` · `sdk/cpp` · `sdk/rust` | Lightweight SDKs that expose annotated app methods to agents |
| `sdk-tutorial.md` | Tutorial covering all six languages |
| `README.md` | Full architecture & wire-protocol design (Chinese) |

**Quick start (Python):**

```python
import asyncio
from agentquay import AgentQuayClient, agent_tool

class MusicController:
    @agent_tool("search", description="Search the music library")
    def search(self, keyword: str) -> list[dict]:
        return [{"id": "1", "title": "Seven Miles", "artist": "Jay Chou"}]

async def main():
    client = AgentQuayClient(app_id="music-app", app_name="Music Player")
    client.register_tools(MusicController())
    await client.connect()          # auto-spawns the embedded Bridge

asyncio.run(main())
```

Then point any MCP Streamable HTTP agent at `http://127.0.0.1:19846/mcp` — the tool
`music-app_search` becomes available. A Chinese quick start and the full tutorial are below.

**License:** [Apache-2.0](LICENSE) · see also [`NOTICE`](NOTICE)

---

## 目录

- [1. 整体架构](#1-整体架构)
- [2. 通信协议设计（核心契约）](#2-通信协议设计核心契约)
- [3. 内嵌 Bridge 生命周期方案](#3-内嵌-bridge-生命周期方案)
- [4. Bridge Service 详细设计（Go）](#4-bridge-service-详细设计go)
- [5. 应用启动注册表（离线应用自动拉起）](#5-应用启动注册表离线应用自动拉起)
- [6. 安全设计](#6-安全设计)
- [7. 快速开始](#7-快速开始)
- [8. 仓库结构](#8-仓库结构)
- [9. 验证](#9-验证)
- [10. 开发计划与现状](#10-开发计划与现状)
- [11. 开源授权](#11-开源授权)

---

## 1. 整体架构

### 1.1 架构图

```
┌─────────────────────────────────────────────────────────┐
│              AgentQuay Bridge（Go 守护进程）               │
│      单端口 19846：/mcp（Streamable HTTP）+ /ws（WS）      │
│     MCP Server │ 注册中心(token钉扎) │ 消息路由/心跳        │
│     ┌─────────────────────────────────────────────┐      │
│     │ 应用启动注册表 §5（离线自动拉起 / OS 级发现）    │      │
│     └─────────────────────────────────────────────┘      │
└────────┬────────────────────────────────┬────────────────┘
         │ MCP (Streamable HTTP)          │ WebSocket
         │ http://127.0.0.1:19846/mcp     │ ws://127.0.0.1:19846/ws
┌────────┴────────┐              ┌────────┴─────────┐
│  AI Agent 工具   │              │  桌面应用进程       │
│  opencode/codex │              │  MusicApp/...     │
│  zcode/...      │              │  (Python SDK)     │
└─────────────────┘              └───────────────────┘
```

### 1.2 角色

| 角色 | 说明 |
|------|------|
| **AgentQuay Bridge** | 核心守护进程（Go）。同时是 MCP Server 与注册/路由中心：注册中心（token 钉扎）、消息路由、心跳检测、确认流程、应用启动注册表。单端口 `19846`（`/mcp` + `/ws` + `/admin`）。 |
| **SDK（各语言）** | 嵌入应用侧的轻量库。扫描注解生成工具元数据、连接 Bridge、注册工具、分发调用、确认弹窗、心跳重连、auto-spawn 内嵌 Bridge。 |
| **AI Agent** | 任何支持 MCP Streamable HTTP 的客户端。发现工具、调用工具，无需为每个应用单独配置。 |

### 1.3 命名约定

| 名称 | 含义 |
|------|------|
| **AgentQuay** | 框架整体名称（Agent 停靠的码头） |
| **AgentQuay Bridge** | 核心守护进程（Bridge Service），Go 实现 |
| `agentquay` | 守护进程命令行工具名 |
| `~/.agentquay/` | 守护进程配置与状态目录 |
| `AgentTool` / `AgentParam` | SDK 注解名（C# Attribute / Java Annotation / TS 装饰器 / Swift Macro 统一） |
| `AgentQuayClient` | SDK 客户端类名（各语言统一，连接 Bridge 的应用侧客户端） |

**标识规则**（协议契约的一部分）：

- `appId`：`[a-z0-9-]{1,48}`，**只允许小写字母/数字/连字符，禁止 `_` 和 `.`**（`_` 保留给 MCP 工具名拼接）。
- tool 名：`[a-zA-Z0-9_-]{1,78}`，缺省回退到方法名；同名客户端内不能重复。
- MCP 侧看到的是合成名 `{appId}_{toolName}`，如 `music-app_search`。

### 1.4 一次完整工作流

```
1. Agent 在 help 中拿到 MCP 端点 http://127.0.0.1:19846/mcp（或 SDK 服务安装后即存在）。
2. 桌面应用启动：
   ├─ SDK 检测端口 → 无服务且 autoSpawnBridge=true → 自动拉起内嵌 Bridge（零安装）
   ├─ WebSocket 连接 → 发送 register（protocolVersion + appId + tools + 版本 + authToken
   │                    + 可选 launch 启动命令）
   ├─ 首次注册 Bridge 分配持久化 token（register_ack 返回）；重连必须携带
   └─ 注册成功 → 各语言 SDK list_changed 广播给 Agent
3. Agent 调 tools/list 看到 {appId}_{tool}（描述带 [应用名] 前缀）
4. Agent 调 tools/call：
   └─ Bridge 校验（appId 在线/离线拉起 → tool 存在 → Schema 校验 → 危险操作确认）
      → 转发 invoke → 应用执行 → result 返回 → Bridge 透传给 Agent
5. 应用离线时，已登记的离线应用工具仍可见（[未运行] 前缀），调用自动拉起再执行（§5）。
```

---

## 2. 通信协议设计（核心契约）

### 2.1 Bridge ↔ 桌面应用（WebSocket `/ws`）

**消息类型列表**：

| 消息 | 方向 | 说明 |
|------|------|------|
| `register` | 应用 → Bridge | 注册应用及工具列表（含 protocolVersion、authToken；可选 `launch` 启动命令） |
| `register_ack` | Bridge → 应用 | 注册成功确认（首次注册含分配的 token） |
| `register_error` | Bridge → 应用 | 注册失败（AUTH_FAILED / UNSUPPORTED_VERSION / INVALID_APP_ID） |
| `invoke` | Bridge → 应用 | 调用指定 Tool（含执行 timeoutSeconds） |
| `result` | 应用 → Bridge | Tool 执行结果 |
| `confirm` | Bridge → 应用 | 请求用户确认（危险操作，含独立确认 timeoutSeconds） |
| `confirm_result` | 应用 → Bridge | 用户确认/取消结果 |
| `ping` / `pong` | 双向 | 心跳检测（每 30s） |
| `disconnect` | 双向 | 优雅断开通知（reason: `normal` / `replaced` / `shutdown` / `migrate`） |
| `notification` | Bridge → 应用 | 广播通知 |

**注册与 token 钉扎**（§6.2）：首次注册 Bridge 分配 `aq_`+64hex token 并落盘
（`~/.agentquay/auth.json`），SDK 持久化到系统凭证库（Windows Credential Manager /
macOS Keychain / libsecret，回退文件）。之后重连必须携带 token，否则 `AUTH_FAILED`
拒绝注册——防止伪造实例顶替同名 appId。

**注册冲突与替换**：同 appId 新实例注册（持有正确 token）→ 通知旧连接
`disconnect(replaced)` → 失败其挂起请求 → 关闭旧连接 → 新实例接管。SDK 收到
`replaced` 后停止重连并抛 `ReplacedError`（MCP 侧 `-32007`）。

**心跳与重连**：双向 30s ping/pong；每 30s 无消息判定一次超时，连续两次判定连接死亡。
SDK 指数退避重连（1s → 30s），重连自动重读 `~/.agentquay/port`（处理端口漂移）并携带 token。

**孤儿结果**：执行超时后应用迟到的 `result` 进入 Bridge 内存环形缓冲（100 条，
`orphanResultBuffer` 可配），不视为错误，只记日志。

### 2.2 Agent ↔ Bridge（MCP `/mcp`）

遵循 MCP 标准协议（JSON-RPC 2.0），传输 **Streamable HTTP**，默认
`POST http://127.0.0.1:19846/mcp`；调试期可用 `agentquay mcp --stdio`。

**工具命名**：合成 `{appId}_{toolName}`，按第一个 `_` 解析（appId 禁止 `_` 使解析无歧义）。

**`tools/list` 工具表组成**（三种来源叠加）：

| 类型 | 命名 | 是否始终可见 | 说明 |
|------|------|--------------|------|
| 在线应用工具 | `{appId}_{tool}` | 仅应用在线时 | 描述前缀 `[AppName]` |
| 已登记离线应用工具 | `{appId}_{tool}` | 离线也可见 | 描述前缀 **`[未运行] [AppName]`**；调用时 Bridge 自动拉起再执行（§5） |
| 内置工具 | `app_list` / `app_launch` / `app_search` / `app_adopt` | 始终可见 | 应用注册表查询 / 按名启动 / 系统搜索 / 显式登记（§5.8.6） |

应用注册/注销时，Bridge 向所有已连接 Agent 广播标准通知
`notifications/tools/list_changed`，Agent 收到后重拉 `tools/list`。

**`tools/call` 校验顺序**：

1. 解析 `{appId}_{toolName}`；appId 不在线 → 已登记且可拉起 → 自动启动并等待注册（§5.8）；
   无法拉起 → `-32001`（附原因）
2. tool 不存在 → `-32002`（离线应用可能更新过工具表，提示重试 tools/list）
3. `inputSchema` 校验 arguments → 失败 `-32003`，**不转发给应用**
4. `RequiresConfirmation` → 确认流程（§2.3），等待期间每 30s 发
   `notifications/progress`（带 progressToken）
5. 转发 `invoke`，等待结果

**参数校验**：SDK 各语言自动生成 inputSchema（Python 类型注解 / Jackson /
System.Text.Json / schemars / Qt QMetaType / Zod）。Agent 传参不符合 Schema 时由
Bridge 拒绝（`-32003`），不占用应用端。

### 2.3 确认流程（危险操作）

```
Agent → Bridge: tools/call { name: "music-app_delete", ... }（带 progressToken）
  → Bridge 检查 RequiresConfirmation = true
  → Bridge → 应用: confirm { requestId, message, arguments, timeoutSeconds: 120 }
  → 应用弹 OS 原生确认框（各语言默认：iOS 原生/Swing/WinForms/QMessageBox/控制台），
    确认计时开始（120s，独立于执行超时）
  → 等待期间 Bridge 每 30s 向 Agent 发 notifications/progress
  → 用户点击确认/取消 → 应用 → Bridge: confirm_result { confirmed: true/false }
  → confirmed=false 或确认超时 → Agent 收到 -32005（用户取消）
  → confirmed=true → Bridge 发 invoke，执行计时开始（默认 30s）
  → 应用返回 result → Bridge 转发 Agent
```

### 2.4 错误码

| 码 | 含义 |
|----|------|
| -32000 | 通用错误 |
| -32001 | 应用未连接（已登记且可自动拉起时会先启动并等待注册；此处表示启动失败/未登记/已关闭自动拉起，错误信息附具体原因） |
| -32002 | Tool 不存在 |
| -32003 | 参数无效（Schema 校验失败，**不转发**给应用） |
| -32004 | 执行超时（应用可能仍在执行） |
| -32005 | 用户取消（确认被拒或确认超时） |
| -32006 | 认证失败 |
| -32007 | 应用被替换（新实例注册） |

> **实现说明**：mcp-go v0.58 将工具处理器返回的 error 一律包装为 JSON-RPC `-32603`，
> 无法透传自定义错误码。因此 Bridge 按 MCP SEP-1303 标准返回 `isError` 工具执行错误，
> 错误码以结构化 JSON 承载在 content 中（如 `{"code":-32004,"message":"执行超时（…）"}`）。
> 工具不存在时由 mcp-go 协议层返回 `-32602`（消息为 tool not found）。
> 各语言 SDK 的异常体系见 [`sdk-tutorial.md`](sdk-tutorial.md#65-错误处理错误码)。

---

## 3. 内嵌 Bridge 生命周期方案

> 内嵌 Bridge（SDK 自动拉起、`serve --embedded` 运行）*为什么存在* 以及 *生命周期怎么管*，
> 是 AgentQuay 的**零安装**设计核心。本节为经过实现与端到端验证的最终方案。

### 3.1 为什么内嵌（而不是"必须先装系统服务"）

| 问题 | 说明 |
|------|------|
| 接入门槛 | 开发者接 SDK 第一件事不该是"下载、装后台服务"。内嵌后首次 `connect()` 自动拉起，零感知 |
| 权限门槛 | Windows Service / macOS launchd / 受管设备往往没有安装权限；内嵌不需要 |
| 鸡生蛋 | Agent 要控制应用必须先有 Bridge 在跑；内嵌让"Bridge 怎么来的"完全自动化 |
| 版本匹配 | 内嵌天然保证 SDK 与 Bridge 同版本配套；手动装可能协议对不上 |
| 多语言一致 | 六种语言 SDK 的接入体验完全一致 |

为什么可行：Bridge 用 Go 编译成单个静态二进制（~10MB，压缩 ~4MB），每个语言包
都能低成本携带（wheel `package_data` / npm `bridge_bin` / NuGet `contentFiles` /
Java classpath 资源 / Rust 构建期准备 / C++ 资源），一套三平台产物。Rust 因
crates.io 单包 10MiB 上限不内嵌二进制：本地开发用仓库内 `bridge_bin/`，从
crates.io 安装时由 `build.rs` 按版本从 GitHub Releases 下载对应平台二进制（首次
构建需联网）。

内嵌模式与系统服务**是同一份代码、同一套协议**，只是启动参数不同
（`serve --embedded` vs `serve`）：SDK 探测到系统服务在跑就直接复用，不会重复拉起。

### 3.2 生命周期管理（三步方案，已实现）

**根因问题**：内嵌桥的命运若绑死在"恰好第一个拉起它的应用"上，宿主一关、共享桥就塌。
原则：**Bridge 的存活由"还有没有客户端在用"决定，绝不由"宿主进程在不在"决定；
出现更稳定的实例（service 模式）时，低级别实例主动让位、引导客户端迁移并退出。**

| 步骤 | 语义 | 效果 |
|------|------|------|
| 1. **生命周期解耦 + 空闲自回收** | 内嵌桥自己管理存活：最后一个应用注销后再空置 `embeddedIdleTimeoutSeconds`（默认 60s）便优雅退出；宿主退出不再杀子进程 | 场景：宿主 A 关闭 → 已连接的 B/C/D 不掉线；全关后桥自回收、释放端口，宿主被强杀也不残留 |
| 2. **实例分级让位 + 主动迁移 + 权威端口归位** | `service` 模式 > `embedded` 模式。内嵌桥探到 mode=service 的更强实例 → 向应用发 `disconnect(reason=migrate)` → 退出；系统服务随后把监听 rebind 回权威端口 19846 | 场景：系统服务最后启动 → 应用自动迁到系统服务、端口归位回 19846，Agent 的静态 MCP 端点（19846）始终有效 |
| 3. **拉起竞态消除（spawn 原子锁）** | `~/.agentquay/spawn.lock`（`{pid, startedAt}` JSON，TTL 15s 防崩溃残留）；抢不到锁的应用等待端口文件就绪后复用 | 多应用并发首启只拉起**一个** Bridge |

**协议扩展**（向后兼容）：`disconnect` 新增 `reason=migrate`；`/admin/status` 新增
`mode`（`embedded`/`service`）。老 SDK 把 `migrate` 当作普通断线重连仍能收敛。

### 3.3 实现状态（2026-08-18，全部实现并验证）

| 内容 | 状态 | 验证 |
|------|------|------|
| 空闲自回收（`embeddedIdleTimeoutSeconds` / `AppCount` / 监视器） | ✅ Bridge | CLI 冒烟 + 自动化 e2e 场景 1 |
| 让位迁移 + service 归位（`DisconnectMigrate` / `mode` / `peer` 探测 / `MigrateToService` / rebind） | ✅ Bridge | 自动化 e2e 场景 2 |
| 宿主退出不杀子进程 + `reason=migrate` 处理 | ✅ 六种语言 SDK | 各语言测试全过 |
| spawn 原子锁 | ✅ 六种语言 SDK | 自动化 e2e 场景 3（并发仅 1 个桥） |

自动化 e2e 见 `sdk/python/tests/lifecycle_e2e.py`（隔离临时家目录，驱动真实 Bridge + 真实 SDK）。

### 3.4 双模式部署

| 模式 | 适用场景 | 说明 |
|------|---------|------|
| **SDK 内嵌（默认）** | 开发、独立应用、无权限环境 | 零安装：首连自动拉起；日志写应用缓存目录（`AGENTQUAY_LOG_DIR`），不触碰全局配置 |
| **系统服务（可选）** | 生产、多应用长期共享 | 登录自启，更适合常驻 |

| 平台 | 系统服务方式 |
|------|------|
| macOS | LaunchAgent（`~/Library/LaunchAgents/com.agentquay.bridge.plist`） |
| Windows | Windows Service（`sc create AgentQuayBridge` 或安装包注册） |
| Linux | systemd user service（`~/.config/systemd/user/agentquay.service`） |

> **MCP 端点稳定性约束（重要）**：Agent 的 MCP 端点是静态固定 URL（默认
> `http://127.0.0.1:19846/mcp`），而桥的端口会随启动顺序漂移。因此：
> 1. **权威端口恒为默认端口 19846**——内嵌/服务的最终协调中心都落回 19846；
> 2. **`~/.agentquay/port` 是唯一权威端口来源**——SDK 重连永远重读它。
> 归位（service rebind 回 19846）的代价是一次额外重连；期间对 Agent 的要求是：
> 把连接错误当可重试信号、断线后重跑 `tools/list`、重试挂起调用。

---

## 4. Bridge Service 详细设计（Go）

### 4.1 技术选型

| 组件 | 方案 | 说明 |
|------|------|------|
| 语言 | **Go** | 单静态二进制、三平台交叉编译零依赖（分发是决定因素）；goroutine 适合长连接消息路由 |
| MCP Server | `mark3labs/mcp-go` | 用现成 MCP SDK 实现协议层，不手写 JSON-RPC |
| WebSocket | `gorilla/websocket` | — |
| JSON Schema 校验 | `santhosh-tekuri/jsonschema` | tools/call 参数校验 |
| 进程管理 | 双模式：内嵌（`--embedded`）+ 系统服务 | §3.4 |
| 配置 | `~/.agentquay/config.json` | — |
| 日志 | `slog` | — |

### 4.2 目录与核心结构

```
bridge/
├── cmd/agentquay/        # CLI 入口（start/serve/stop/status/apps/...）
└── internal/
    ├── config/           # 配置（端口、超时、launch 节）
    ├── auth/             # token 钉扎表（auth.json）
    ├── registry/         # 在线应用注册表 + 挂起请求 + 孤儿缓冲
    ├── hub/              # /ws WebSocket 服务（注册、心跳、路由、确认）
    ├── mcpbridge/        # /mcp Streamable HTTP 服务（tools/list、tools/call）
    ├── admin/            # /admin 管理端点（status、rotate-token、apps）
    ├── apps/             # 应用启动注册表（~/.agentquay/apps/*.json）
    ├── launcher/         # 离线应用拉起服务
    ├── discovery/        # OS 级应用发现（开始菜单/.desktop//Applications）
    ├── daemon/           # 后台守护进程管理
    └── peer/             # 实例探测（embedded 让位用，§3.2）
```

### 4.3 配置文件（`~/.agentquay/config.json`）

```json
{
  "port": 19846,
  "mcpPath": "/mcp",
  "wsPath": "/ws",
  "host": "127.0.0.1",
  "autoPort": true,
  "portFile": "~/.agentquay/port",
  "logLevel": "info",
  "logFile": "~/.agentquay/agentquay.log",
  "defaultTimeoutSeconds": 30,
  "confirmTimeoutSeconds": 120,
  "heartbeatIntervalSeconds": 30,
  "maxConnections": 100,
  "allowRemoteConnections": false,
  "orphanResultBuffer": 100,
  "embeddedIdleTimeoutSeconds": 60,
  "launch": {
    "enabled": true,
    "defaultTimeoutSeconds": 15,
    "maxConcurrentLaunches": 3,
    "discoveryEnabled": true,
    "discoveryScanOnStart": true,
    "discoveryCacheTtlSeconds": 300,
    "discoveryRequireConfirm": false,
    "autoAdoptDiscovery": true
  }
}
```

**`launch` 节**：`enabled`（全局自动拉起开关）/ `defaultTimeoutSeconds`（启动后等待注册超时，
默认 15s）/ `maxConcurrentLaunches`（并发拉起上限，默认 3）/ `discoveryEnabled`（OS 级发现）/
`discoveryScanOnStart`（启动预扫描）/ `discoveryCacheTtlSeconds`（发现缓存 5 分钟）/
`discoveryRequireConfirm`（严格模式，未登记应用需先确认才允许拉起）/ `autoAdoptDiscovery`
（按名拉起未登记应用时自动登记）。

**端口策略**：默认单端口 19846；`autoPort:true` 时被占则尝试 19846–19856，最终端口写入
`~/.agentquay/port`。SDK 侧 `port:0` 表示从 port 文件自动读取，开发者无需关心端口。

### 4.4 命令行接口

```bash
agentquay start [--daemon]        # 启动（前台 / --daemon 后台运行）
agentquay serve [--embedded]      # 服务主循环（内部；SDK 内嵌模式入口）
agentquay stop                    # 停止
agentquay status                  # 状态（版本/协议/模式/端口/已注册应用/孤儿结果数）
agentquay list-apps               # 查看在线应用
agentquay apps list               # 已登记应用（含离线与启动能力）
agentquay apps add --appId <id> --name <名称> --path <可执行文件> [--args "…"] [--auto-launch on|confirm|off]
agentquay apps remove <appId>     # 移除登记
agentquay apps search <关键字>     # 本机搜索可启动应用
agentquay apps launch <appId|名称> [--no-wait]   # 启动（默认等待 SDK 注册并报告状态）
agentquay apps set-auto-launch <appId> <on|confirm|off>
agentquay logs [--tail N]         # 日志（默认最近 100 行）
agentquay rotate-token <appId>    # 轮换某应用 token
agentquay mcp --stdio             # stdio MCP（调试）
agentquay version                 # 版本与协议版本
agentquay generate-agent-config [--output file]   # 生成 Agent 配置文件
agentquay help                    # 帮助
```

配置目录：`~/.agentquay/`（`config.json` / `auth.json` / `apps/` / `port` / `agentquay.log`）。

---

## 5. 应用启动注册表（离线应用自动拉起）

> 解决"Agent 打开一个**尚未运行**的应用"：在线应用的可控性由 §2 保证，这里解决
> **离线应用的安装位置从哪来、由谁启动、如何衔接工具调用**。

### 5.1 信息获取三层（持久化到 `~/.agentquay/apps/<appId>.json`）

| 层 | 来源 | 优先级 | 说明 |
|----|------|--------|------|
| ① SDK 自报（首选） | `register` 携带 `launch` 字段（V0.1 各 SDK 自动探测并上报） | 最高 | 应用只有自己知道"该启动哪个进程"；每次注册刷新，天然跟随更新/搬家 |
| ② 用户/CLI 显式登记 | `agentquay apps add` | 中 | 兜底，手工登记 |
| ③ OS 级发现 | Windows 开始菜单/注册表、macOS /Applications、Linux .desktop | 低 | 只提供"启动"能力；成功拉起后自动"毕业"为已登记 |

启动命令必须是 **argv 数组**，Bridge 经 exec 直传，绝不拼 shell 字符串（防注入）。

### 5.2 拉起流程与安全边界

- **离线工具调用**：`tools/list` 已含离线应用工具（`[未运行]` 标注）；Agent 直接调用即
  触发分离式拉起（Windows DETACHED_PROCESS / Unix setsid）→ 等待 SDK 注册（默认 15s）→
  在线后走常规校验/确认/调用。**并发去重**（同一应用拉起中只等不双开）、**并发上限**
  （默认 ≤3）、**路径回退**（历史位置兜底）。
- **内置工具**：`app_list` / `app_launch` / `app_search` / `app_adopt`（§5.4 的四个 app_*）。
- **安全边界**：注册表只认三种来源（SDK 自证 / CLI 登记 / OS 发现自动登记）；
  `app_launch` 和离线工具调用**只接受已登记 appId 或系统内发现候选，绝不接受 Agent 传来的任意路径**
  （防 Prompt Injection 驱动器任意程序）；严格模式 `launch.discoveryRequireConfirm=true`
  拒绝未登记应用拉起；全局开关 `launch.enabled=false` 完全关闭。
- **OS 发现**：缓存 TTL 5 分钟、单飞防重扫；模糊匹配（完全一致 100 / 前缀 90 / 包含 80）；
  macOS 经 `/usr/bin/open -n`、Linux 解析 `.desktop` 的 `Exec`。发现结果仅"启动"能力，
  是否可控取决于应用是否带 SDK 注册。

---

## 6. 安全设计

### 6.1 威胁模型

| 风险 | 严重 | 缓解 |
|------|------|------|
| 局域网其他机器连接 Bridge | 高 | 仅监听 `127.0.0.1`；`allowRemoteConnections` 默认 false |
| 恶意应用抢注同名 appId 劫持调用 | 中 | **token 钉扎**：首连者获 token，后续必须携带，否则拒绝注册 |
| Agent 调用危险操作 | 中 | `RequiresConfirmation=true` → Bridge 发 confirm → 应用弹 OS 原生对话框；确认/执行超时分离 + progress 通知 |
| 提示词诱导拉起任意应用 | 低-中 | 只接受已登记/系统内发现的候选，不接受任意路径；严格模式；MCP 客户端权限层可先询问用户 |
| 敏感数据经 Tool 返回泄露 | 低 | 预留结果过滤中间件（脱敏/审计） |
| 拒绝服务（大量连接） | 低 | 最大连接数限制（默认 100） |

### 6.2 token 钉扎认证

首次运行生成 `~/.agentquay/auth.json`（appId → token 映射）；应用首次连接分配
`aq_`+64hex token；SDK 持久化到系统凭证库；重连/同名替换都必须携带原 token；
泄漏可 `agentquay rotate-token <appId>` 轮换（VPN 后旧 token 立即失效，应用重启换新）。

---

## 7. 快速开始

各语言 SDK 已发布到官方包管理器，安装方式：

| 语言 | 命令 |
|------|------|
| Python | `pip install agentquay-sdk` |
| TypeScript | `npm install @agentquay/sdk` |
| Rust | `cargo add agentquay`（首次构建自动从 GitHub Releases 下载内嵌 Bridge 二进制） |
| C# / .NET | `dotnet add package AgentQuay.Sdk`（WPF 扩展另加 `AgentQuay.Sdk.Wpf`） |
| Java | Maven / Gradle 引入 `io.github.wenbaow:agentquay-sdk` |
| C++ | 源码分发：CMake 引入 `sdk/cpp`（Qt 6.5+） |

完整的分语言指南见 **[`sdk-tutorial.md`](sdk-tutorial.md)**。最短路径（Python 示例）：

```python
import asyncio
from agentquay import AgentQuayClient, agent_tool

class MusicController:
    @agent_tool("search", description="搜索音乐库")
    def search(self, keyword: str) -> list[dict]:
        return [{"id": "1", "title": "七里香", "artist": "周杰伦"}]

async def main():
    client = AgentQuayClient(app_id="music-app", app_name="Music Player")
    client.register_tools(MusicController())
    await client.connect()          # 自动拉起内嵌 Bridge，保持连接

asyncio.run(main())
```

跑起来后，任何支持 MCP Streamable HTTP 的 Agent 配置 `http://127.0.0.1:19846/mcp`
即可看到 `music-app_search` 并直接调用。若想装系统服务：

```bash
cd bridge && go build -o dist/agentquay ./cmd/agentquay   # Go 1.25.5+
./dist/agentquay start --daemon
```

## 8. 页面智能路由（页面级懒激活，V0.2）

**页面未打开，工具也要可见。** 页面路由方案把"工具元数据"与"页面实例"解耦：
注册时全量上报（含可选 `pageKey` 分组标签），实例绑定惰性化——Agent 调用时：

1. 页面已打开 → 直接调用该实例的方法（UI 线程，异步让出不阻塞）；
2. 页面未打开但有工厂 → **自动创建 → 导航 → 等待就绪 → 调用**（单飞去重，默认 15s 激活超时）；
3. 页面未打开且无工厂 → 返回明确错误（`PAGE_NOT_FOUND`，工具仍在 `tools/list` 中）。

`pageKey` 只是 SDK 内部的分组标签，**不进协议、Agent 无感知**；工具名跨页面全局唯一
（重复注册返回 `INVALID_TOOL`）；`tools/list` 描述带 `[AppName|PageKey]` 前缀便于
Agent 了解工具归属。示例（C#，WPF 应用）：

```csharp
var client = await AgentQuayClient.ConnectAsync(appId: "music-app", appName: "Music Player",
    autoSpawnBridge: true);

// 全局工具：原路径零改动
client.RegisterTools<AppCommands>();

// 页面工具：惰性注册（页面未打开工具也可见，首次调用才创建）
client.RegisterTools<SearchPage>(pageKey: "SearchPage");
client.RegisterTools(() => _services.GetRequiredService<PlayerPage>(), pageKey: "PlayerPage");

// 激活钩子（可选）：导航 + 等待就绪（WPF 扩展包 AgentQuay.Sdk.Wpf 提供调度器与就绪等待）
client.SetUIThreadDispatcher(new WpfDispatcher());
client.SetPageActivator("SearchPage",
    navigate: page => MainWindow.NavigateTo((SearchPage)page),
    awaitReady: async page => await PageLoadedAsync((FrameworkElement)page));

await client.StartAsync();
```

各语言 API 同构：Python `register_tools(cls, page_key=...)` / `register_tools_factory`、
TypeScript `registerTools(cls, { pageKey })` / `registerToolsFactory`、Java
`registerTools(Class, pageKey)` / `registerToolsFactory`，均提供 `setPageActivator`
激活钩子与 `unregisterPage` 显式注销（不调也行——弱引用 GC 失效后工厂路径自动重建）。
详见 [`sdk-tutorial.md`](sdk-tutorial.md) 与各 SDK 单测。

## 9. 仓库结构

```
├── README.md                # 本文件：整体架构方案、介绍、开源授权
├── sdk-tutorial.md          # SDK 使用教程（六种语言）
├── bridge/                  # AgentQuay Bridge（Go）
│   ├── cmd/agentquay/       # CLI 入口
│   └── internal/            # config / auth / registry / hub / mcpbridge / admin / daemon / apps / launcher / discovery / peer
└── sdk/
    ├── python/              # Python SDK（agentquay-sdk，PyPI）
    ├── typescript/          # TypeScript SDK（@agentquay/sdk，npm）
    ├── java/                # Java SDK（io.github.wenbaow:agentquay-sdk，Maven Central）
    ├── dotnet/              # C# SDK（AgentQuay.Sdk / AgentQuay.Sdk.Wpf，NuGet，.NET 8+）
    ├── cpp/                 # C++ SDK（Qt 6.5+，CMake 源码分发）
    └── rust/                # Rust SDK（agentquay / agentquay-macros，crates.io）
```

## 10. 验证

```bash
# Go 编译 + 静态检查
cd bridge && go vet ./... && go build ./...

# Python SDK 单测（30 项，含页面路由 12 项）+ 端到端联调（31 项，含页面路由 6 项，真实 Bridge + SDK + MCP 客户端）
# + 内嵌 Bridge 生命周期 e2e（3 场景：宿主退出/让位+归位/并发竞态）
cd sdk/python && python -m unittest discover -s tests \
  && python tests/e2e_test.py \
  && python tests/lifecycle_e2e.py

# TypeScript SDK 单测（26 项，含页面路由 9 项）+ 端到端联调（25 项）+ 冒烟
cd sdk/typescript && npm test && npm run test:e2e && npm run smoke

# Java SDK 单测（21 项，含页面路由 10 项）+ 端到端联调（7 项，真实 Go Bridge）
cd sdk/java && mvn test

# C# SDK 单测（16 项，含页面路由 9 项）+ 端到端联调（1 项全流程，真实 Go Bridge）
cd sdk/dotnet && dotnet test tests/AgentQuay.Sdk.Tests

# C++ SDK 单测（12 项）+ 端到端联调（9 项，真实 Go Bridge；需 Qt 6.5+ 与 Ninja）
powershell -ExecutionPolicy Bypass -File sdk/cpp/_build.ps1

# Rust SDK 单测（17 项）+ 宏集成测试（3 项）+ 端到端联调（1 项全流程，真实 Go Bridge）
cd sdk/rust && cargo test --workspace && cargo test -p agentquay --test e2e_test
```

端到端覆盖：注册/token、`tools/list`（在线/离线/内置三种工具表）、正常调用、
`-32003` 参数校验、业务错误透传、确认流程（确认/取消）、`-32004` 超时 + 孤儿缓冲、
同 appId 替换、SSE `list_changed`、断线重连携带 token、`rotate-token` 旧 token 拒绝。

## 11. 开发计划与现状

**当前进展（V0.1）**：Bridge + Python/TypeScript/Java/.NET/C++/Rust 六种语言 SDK
已完成并通过单元/端到端验证；应用启动注册表 + OS 发现、内嵌 Bridge 生命周期三步方案
均已实现。

| 计划项 | 状态 |
|--------|------|
| Bridge + 六语言 SDK + 端到端验证 | ✅ 已实现 |
| 应用启动注册表与 OS 级发现（V0.1 新增） | ✅ 已实现 |
| 内嵌 Bridge 生命周期（自回收/让位/归位/竞态锁） | ✅ 已实现 |
| 页面智能路由（页面级懒激活，V0.2 新增） | ✅ 已实现 |
| WPF 扩展包 AgentQuay.Sdk.Wpf（UI 线程调度 + 就绪等待） | ✅ 已实现 |
| Swift SDK（Swift Macro） | ⬜ 规划 |
| 系统服务安装器（launchd/systemd/Windows Service 一键安装） | ⬜ 规划 |
| 结果脱敏中间件 | ⬜ 规划 |
| 多用户 System scope | ⬜ 远期 |

**已确认的技术决策**：MCP 传输 Streamable HTTP（保留 `mcp --stdio`）；协议版本
major 不兼容/minor 兼容；七语言错误与消息格式统一；auto-spawn 内嵌二进制为默认；
token 存系统凭证库（fallback 文件）；C++ 走 Qt 元对象反射；Rust 按 target 条件内嵌二进制。

**仍未拍板**：Swift Macro 方案、多用户 System scope、Agent 兼容矩阵的完整覆盖、
Python Pydantic 可选增强。

## 12. 开源授权

**Copyright (c) 2026 王文豹（Wang Wenbao）**

全项目（含 Bridge 与各语言 SDK）统一采用 **Apache-2.0** 许可，条文见
[`LICENSE`](LICENSE)，著作权归属见 [`NOTICE`](NOTICE)。宽松许可，便于嵌入第三方应用，
不产生传染义务，并附带明确的专利授权条款。
