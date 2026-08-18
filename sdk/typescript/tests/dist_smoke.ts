/**
 * dist 产物冒烟测试：验证 npm 包形态（tsc 编译的 CJS dist/ + legacy 装饰器语义）
 * 连接真实 Bridge 全链路可用。
 *
 * 用法（需先 npm run build）:
 *   npx tsc tests/dist_smoke.ts --outDir dist-test --module commonjs --target ES2022 \
 *     --experimentalDecorators --esModuleInterop --moduleResolution node \
 *     --skipLibCheck --strict --types node && node dist-test/dist_smoke.js
 */
import { spawn, type ChildProcess } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
// 类型仅编译期引用（运行时不解析）；值通过 require 按 __dirname 计算绝对路径加载 dist 产物
import type { AgentQuayClient as AgentQuayClientType } from "../dist/index";

// eslint-disable-next-line @typescript-eslint/no-var-requires
const { AgentQuayClient, AgentTool } = require(path.resolve(
  __dirname, "..", "dist", "index.js",
)) as {
  AgentQuayClient: typeof AgentQuayClientType;
  AgentTool: (nameOrOptions?: unknown, options?: unknown) => MethodDecorator;
};

const APP_ID = "dist-smoke";
const BRIDGE_BIN =
  process.env.AGENTQUAY_BRIDGE_BIN ??
  path.resolve(__dirname, "..", "..", "..", "bridge", "dist", "agentquay-windows-amd64.exe");

class Ctrl {
  @AgentTool("echo", { description: "回显" })
  echo(message: string): { received: string } {
    return { received: message };
  }

  @AgentTool("confirm_op", { description: "确认操作", requiresConfirmation: true })
  confirmOp(): { ok: boolean } {
    return { ok: true };
  }
}

function check(name: string, cond: boolean, detail = ""): void {
  console.log(`  ${cond ? "✅" : "❌"} ${name}${detail ? " " + detail : ""}`);
  if (!cond) {
    process.exitCode = 1;
  }
}

async function main(): Promise<void> {
  const portFile = path.join(os.homedir(), ".agentquay", "port");
  fs.rmSync(portFile, { force: true });
  const bridge: ChildProcess = spawn(BRIDGE_BIN, ["serve"], {
    stdio: "ignore",
    env: { ...process.env, AGENTQUAY_LOG_LEVEL: "info" },
  });
  const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));
  try {
    // 1. 等待 Bridge 就绪
    let port = 0;
    for (let i = 0; i < 60; i++) {
      await sleep(200);
      let p = 0;
      try {
        p = Number(fs.readFileSync(portFile, "utf8").trim());
      } catch {
        continue;
      }
      if (p <= 0) continue;
      try {
        const resp = await fetch(`http://127.0.0.1:${p}/admin/health`);
        if (resp.ok) {
          port = p;
          break;
        }
      } catch {
        // 未就绪
      }
    }
    check("Bridge 启动并健康", port > 0, `port=${port}`);
    if (!port) return;

    // 2. dist 包注册（legacy 装饰器 + CJS + zod 缺省路径：参数源码解析 schema）
    const client = new AgentQuayClient({
      appId: APP_ID,
      appName: "Dist Smoke",
      port,
      autoSpawnBridge: false,
      onConfirm: async () => true,
    });
    client.registerTools(Ctrl);
    const task = client.connect().catch((e: unknown) => {
      console.log(`  [诊断] connect 失败: ${String(e)}`);
    });

    // 3. 等待上线（admin/status）
    let online = false;
    for (let i = 0; i < 40; i++) {
      await sleep(200);
      try {
        const st = (await (await fetch(`http://127.0.0.1:${port}/admin/status`)).json()) as {
          apps: { appId: string }[];
        };
        if (st.apps.some((a) => a.appId === APP_ID)) {
          online = true;
          break;
        }
      } catch {
        // 未就绪
      }
    }
    check("dist 包注册上线（legacy 装饰器路径）", online);

    // 4. MCP 握手 + tools/list 合成名 + inputSchema.required
    let sessionId = "";
    const initResp = await fetch(`http://127.0.0.1:${port}/mcp`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream" },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "initialize",
        params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "dist-smoke", version: "1.0" } },
      }),
    });
    sessionId = initResp.headers.get("Mcp-Session-Id") ?? "";

    const mcpPost = async (id: number, method: string, params: Record<string, unknown>): Promise<unknown> => {
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
        Accept: "application/json, text/event-stream",
      };
      if (sessionId) {
        headers["Mcp-Session-Id"] = sessionId;
      }
      const resp = await fetch(`http://127.0.0.1:${port}/mcp`, {
        method: "POST",
        headers,
        body: JSON.stringify({ jsonrpc: "2.0", id, method, params }),
      });
      const text = await resp.text();
      if ((resp.headers.get("Content-Type") ?? "").includes("text/event-stream")) {
        // 取最后一个 data 负载
        let last: string | null = null;
        for (const event of text.split("\n\n")) {
          for (const line of event.split("\n")) {
            if (line.startsWith("data:")) {
              last = line.slice(5).trim();
            }
          }
        }
        if (last === null) throw new Error(`SSE 无 data: ${text.slice(0, 200)}`);
        return JSON.parse(last) as unknown;
      }
      return JSON.parse(text) as unknown;
    };

    const list = (await mcpPost(2, "tools/list", {})) as {
      result?: { tools?: { name: string; inputSchema?: { required?: string[] } }[] };
    };
    const tools = list.result?.tools ?? [];
    const echo = tools.find((t) => t.name === "dist-smoke_echo");
    check("tools/list 合成名 dist-smoke_echo", echo !== undefined);
    check("inputSchema required=[message]（参数源码解析）", JSON.stringify(echo?.inputSchema?.required) === '["message"]');

    // 5. 正常调用 + 确认流程（确认）
    const call = (await mcpPost(3, "tools/call", {
      name: "dist-smoke_echo",
      arguments: { message: "dist" },
    })) as { result?: { content?: { text?: string }[]; isError?: boolean } };
    check("tools/call 正常调用", !call.result?.isError && call.result?.content?.[0]?.text !== undefined);

    const confirm = (await mcpPost(4, "tools/call", {
      name: "dist-smoke_confirm_op",
      arguments: {},
    })) as { result?: { isError?: boolean } };
    check("确认流程（确认）→ 成功", !confirm.result?.isError);

    await client.close();
    await task;
    console.log("=== dist 冒烟完成 ===");
  } finally {
    bridge.kill();
  }
}

void main();
