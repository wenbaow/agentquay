# @agentquay/sdk — AgentQuay TypeScript SDK

让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。加个装饰器，其余全部由框架处理
（设计文档 §4.3）。

## 安装

```bash
npm install @agentquay/sdk
```

- 需要 **Node.js 18+**（WebSocket 用 `ws` 包）
- 使用装饰器需在 `tsconfig.json` 开启（设计文档推荐，tsc 编译路径）：

  ```json
  {
    "compilerOptions": {
      "experimentalDecorators": true
    }
  }
  ```

  装饰器同时兼容 TS 5 标准装饰器语义（esbuild / tsx / Babel 路径），两种模式均可使用。
- 可选依赖（不安装时优雅降级）：
  - `keytar` — token 持久化到系统凭证库（Windows Credential Manager / macOS Keychain /
    libsecret）；不安装则回退 `~/.agentquay/tokens.json`
  - `zod` + `zod-to-json-schema` — 用 zod schema 声明参数校验；不安装则按函数参数
    自动生成基础 schema

## 用法

```ts
import { AgentQuayClient, AgentTool } from "@agentquay/sdk";

class MusicController {
  @AgentTool("search", { description: "搜索音乐库" })
  search(keyword: string): Song[] {
    // 业务逻辑
  }

  @AgentTool("play", { description: "播放歌曲" })
  async play(songId: string): Promise<void> {
    // 异步方法原生支持
  }

  @AgentTool("delete", { description: "删除歌曲", requiresConfirmation: true })
  deleteSong(songId: string): void {
    // 危险操作：Bridge 会先向应用弹确认框
  }
}

const client = new AgentQuayClient({
  appId: "music-app",       // [a-z0-9-]{1,48}，禁止 _ 和 .
  appName: "Music Player",
  host: "localhost",
  port: 0,                  // 0 = 从 ~/.agentquay/port 自动读取
  autoSpawnBridge: true,    // 未检测到服务时自动拉起内嵌 Bridge
});
client.registerTools(MusicController);   // 传类（自动实例化）或实例
await client.connect();                  // 保持连接，监听 Agent 调用
// ... 应用继续运行，close() 时结束
await client.close();
```

## JSON Schema（inputSchema）三种来源

| 来源 | 说明 |
|------|------|
| 自动生成（默认） | 从函数源码解析参数名 → `{ type: "object", properties, required }`；无运行时类型，属性为 `{}`（任意值） |
| zod schema（推荐） | `registerTools(MusicController, { zodSchemas: { search: z.object({ keyword: z.string() }) } })`，运行时转换为 JSON Schema 供 Bridge 校验（-32003） |
| 显式 inputSchema | `@AgentTool("search", { inputSchema: {...} })` 直接提供 JSON Schema |

## 确认流程

`requiresConfirmation: true` 的 Tool 被调用时，Bridge 会向应用发 `confirm` 消息。
默认处理为终端交互确认（`[y/N]`，超时视为取消；无交互终端默认拒绝——安全优先）。
GUI 应用（Electron 等）通过 `onConfirm` 提供原生对话框：

```ts
const client = new AgentQuayClient({
  appId: "music-app",
  appName: "Music Player",
  onConfirm: async (message, arguments, timeoutSeconds) => {
    const ok = await dialog.showMessageBox({ type: "question", message });
    return ok.response === 0; // true=确认 / false=取消
  },
});
```

## 特性

| 能力 | 说明 |
|------|------|
| 装饰器扫描 | `@AgentTool`（name 缺省回退方法名）+ `registerTools(Class)` / `registerTools(instance)`；兼容遗留与标准两种装饰器语义 |
| 异步方法 | async / Promise 返回值自动 await；同步方法照常调用 |
| 参数绑定 | 按参数名绑定（源码解析）；单参数对象风格 `(args: {...})` 自动识别；无法解析时整体传参 |
| 确认流程 | 默认终端交互；`onConfirm` 自定义（支持同步/异步） |
| token 持久化 | keytar（可选）→ `~/.agentquay/tokens.json` 回退；重连自动携带 |
| 心跳 + 重连 | 30s 应用层 ping/pong；断线指数退避重连（1s → 30s），自动携带 token；被替换（同 appId 新实例）时 `connect()` 抛 `ReplacedError` |
| auto-spawn | 本地无 Bridge 时自动拉起内嵌二进制（`serve --embedded`，日志写 `~/.agentquay/logs/`），进程退出自动终止 |

## 错误

`connect()` 可抛出的错误（均继承 `AgentQuayError`）：

| 错误 | 场景 |
|------|------|
| `ReplacedError` | 连接被同 appId 的新实例替换（-32007 语义），停止重连 |
| `AuthFailedError` | token 与 Bridge 钉扎的不匹配（AUTH_FAILED） |
| `RegistrationError` | 注册被拒绝（协议不兼容 / appId / tool 名非法） |
| `BridgeUnavailableError` | 本地无 Bridge 且无法自动拉起 |
| `BridgeConnectionError` | 连接断开/失败（自动重连） |

## 构建与测试

```bash
npm install
npm run build          # tsc 编译到 dist/（含类型声明）
npm test               # 单测 17 项（装饰器 + schema）
npm run test:e2e       # 端到端 25 项（需先构建 bridge/dist）
npm run smoke          # legacy 装饰器 + dist 产物冒烟（真实 Bridge）
npm run example        # 示例应用 music-app
```

端到端覆盖（`tests/e2e.test.ts`，真实 Go Bridge）：注册/token、`tools/list` 合成名与描述前缀、
`-32003` 参数校验（zod schema）、未知 tool/应用、业务错误透传、确认流程（确认/取消 `-32005`）、
执行超时 `-32004` + 孤儿缓冲、同 appId 替换（`ReplacedError`）、断线重连携带 token、
`rotate-token` 旧 token 被拒。

> 测试通过环境变量 `AGENTQUAY_BRIDGE_BIN` 或自动查找 `bridge/dist/` 定位 Bridge 二进制；
> 不指定时自动使用随包分发的内嵌二进制（`bridge_bin/`，三平台）。