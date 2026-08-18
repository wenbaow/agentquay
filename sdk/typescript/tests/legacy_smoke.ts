/**
 * legacy 装饰器冒烟测试：tsc + experimentalDecorators 编译，验证设计文档推荐的
 * 遗留装饰器语义（元数据挂在 ctor.__agentTools）在发布包（dist/）上可用。
 *
 * 用法（需先 npm run build）:
 *   npx tsc tests/legacy_smoke.ts --outDir dist-test --module commonjs --target ES2022 \
 *     --experimentalDecorators --esModuleInterop --moduleResolution node \
 *     --skipLibCheck --strict --types node && node dist-test/legacy_smoke.js
 */
import * as path from "node:path";
import type { AgentQuayClient as AgentQuayClientType } from "../dist/index";

// eslint-disable-next-line @typescript-eslint/no-var-requires
const { AgentQuayClient, AgentTool, getAgentTools } = require(path.resolve(
  __dirname, "..", "dist", "index.js",
)) as {
  AgentQuayClient: typeof AgentQuayClientType;
  AgentTool: (nameOrOptions?: unknown, options?: unknown) => MethodDecorator;
  getAgentTools: (ctor: unknown) => unknown;
};

class Music {
  @AgentTool("search", { description: "搜索音乐" })
  search(keyword: string): string[] {
    void keyword;
    return [];
  }

  @AgentTool({ description: "默认名回退方法名", requiresConfirmation: true })
  play(songId: string): void {
    void songId;
  }

  plain(): void {
    // 未装饰，不应被扫描
  }
}

function assert(cond: boolean, msg: string): void {
  if (!cond) {
    console.error(`❌ ${msg}`);
    process.exit(1);
  }
  console.log(`✅ ${msg}`);
}

function listTools(client: AgentQuayClientType): string[] {
  return [...client.listTools()].sort();
}

// 1. legacy 语义：元数据挂在类构造器 __agentTools
const specs = getAgentTools(Music) as { name: string; method: string; requiresConfirmation: boolean }[] | null;
assert(specs !== null, "getAgentTools(Music) 返回元数据");
assert(specs!.length === 2, "共 2 个 tool");
assert(specs!.some((s) => s.name === "search" && s.method === "search"), "search: 显式 name");
assert(specs!.some((s) => s.name === "play" && s.requiresConfirmation), "play: name 回退方法名 + 确认标记");

// 2. registerTools 扫描（类输入，自动实例化）
const client = new AgentQuayClient({ appId: "music-app", appName: "Music Player", autoSpawnBridge: false });
client.registerTools(Music);
assert(listTools(client).join(",") === "play,search", "listTools = [play, search]");

// 3. 实例输入
const client2 = new AgentQuayClient({ appId: "music-app", appName: "Music Player", autoSpawnBridge: false });
client2.registerTools(new Music());
assert(listTools(client2).join(",") === "play,search", "实例输入扫描正常");

console.log("=== legacy 冒烟完成 ===");