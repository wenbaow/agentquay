/**
 * 端到端联调测试：真实 Bridge + TS SDK + MCP 客户端全链路（对齐 Python e2e_test.py）。
 *
 * 覆盖场景：
 *  1. Bridge 启动（serve 前台）与 health 探测
 *  2. SDK 注册（token 钉扎）→ tools/list 可见（合成名 + 应用名前缀描述）
 *  3. tools/call 正常调用、参数校验 -32003、未知 tool、业务错误透传
 *  4. 确认流程：确认成功 / 用户取消 -32005
 *  5. 执行超时 -32004 + 迟到结果进孤儿缓冲
 *  6. 同 appId 替换 -32007（ReplacedError）
 *  7. 断线重连自动携带 token（不触发 AUTH_FAILED）
 *  8. rotate-token：旧 token 被拒 AUTH_FAILED，新 token 恢复
 *  9. （尽力而为）SSE 上的 notifications/tools/list_changed
 *
 * 用法: npx tsx --test tests/e2e.test.ts（需先构建 bridge/dist）
 */
import { spawn, type ChildProcess } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import assert from "node:assert/strict";
import { test } from "node:test";
import { z } from "zod";
import {
  AgentQuayClient,
  AgentTool,
  AuthFailedError,
  ReplacedError,
} from "../src/index";
import { readPortFile } from "../src/spawn";
import { TokenStore } from "../src/tokens";

const REPO_ROOT = path.resolve(__dirname, "..", "..", "..");
const APP_ID = "ts-e2e-app";

function findBridgeBinary(): string {
  const env = process.env.AGENTQUAY_BRIDGE_BIN;
  if (env && fs.existsSync(env)) {
    return env;
  }
  const distDir = path.join(REPO_ROOT, "bridge", "dist");
  const names = fs.existsSync(distDir)
    ? fs.readdirSync(distDir).filter((f) => f.startsWith("agentquay"))
    : [];
  const win = names.find((f) => f.endsWith(".exe"));
  const name = win ?? names[0];
  if (!name) {
    throw new Error("未找到 Bridge 二进制，请先构建: cd bridge && go build -o dist/ ./cmd/agentquay");
  }
  return path.join(distDir, name);
}

const BRIDGE_BIN = findBridgeBinary();

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** 给 Promise 加超时（超时返回 "timeout" 哨兵）。 */
async function raceTimeout<T>(promise: Promise<T>, ms: number): Promise<T | "timeout"> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<"timeout">((resolve) => {
        timer = setTimeout(() => resolve("timeout"), ms);
      }),
    ]);
  } finally {
    if (timer) {
      clearTimeout(timer);
    }
  }
}

/** 收敛 Promise 的拒绝（后台任务可能 reject，如 ReplacedError）。 */
function track(p: Promise<unknown>): Promise<unknown> {
  p.catch(() => {
    // 已由调用方处理或忽略
  });
  return p;
}

/** 等待并捕获结果（替代裸 await，避免 pending 任务挂起测试）。 */
async function settle<T>(promise: Promise<T>): Promise<{ ok: boolean; value?: T; error?: unknown }> {
  try {
    return { ok: true, value: await promise };
  } catch (e) {
    return { ok: false, error: e };
  }
}

// ---------------------------------------------------------------------------
// 最小 MCP Streamable HTTP 客户端（fetch 实现，处理 SSE 响应）
// ---------------------------------------------------------------------------

class MCPRawClient {
  sessionId: string | null = null;
  private id = 0;

  constructor(private readonly port: number) {}

  async post(method: string, params?: Record<string, unknown>): Promise<unknown> {
    this.id += 1;
    const body = JSON.stringify({
      jsonrpc: "2.0",
      id: this.id,
      method,
      ...(params ? { params } : {}),
    });
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
    };
    if (this.sessionId) {
      headers["Mcp-Session-Id"] = this.sessionId;
    }
    const resp = await fetch(`http://127.0.0.1:${this.port}/mcp`, {
      method: "POST",
      headers,
      body,
    });
    const sid = resp.headers.get("Mcp-Session-Id");
    if (sid) {
      this.sessionId = sid;
    }
    const text = await resp.text();
    const ctype = resp.headers.get("Content-Type") ?? "";
    // Streamable HTTP 规范：会话有挂起通知（如 list_changed）时服务端以 SSE 流响应
    if (ctype.includes("text/event-stream")) {
      return parseSSELast(text);
    }
    return JSON.parse(text) as unknown;
  }
}

/** 从 SSE 流提取最后一个 data 负载（即目标 JSON-RPC 响应）。 */
function parseSSELast(text: string): unknown {
  let last: string | null = null;
  for (const event of text.split("\n\n")) {
    let payload: string | null = null;
    for (const line of event.split("\n")) {
      if (line.startsWith("data:")) {
        payload = line.slice(5).trim();
      }
    }
    if (payload !== null) {
      last = payload;
    }
  }
  if (last === null) {
    throw new Error(`SSE 流中未找到 data 负载: ${text.slice(0, 200)}`);
  }
  return JSON.parse(last) as unknown;
}

type RpcResponse = { result?: any; error?: any };

function toolErrorText(resp: unknown): string {
  return (resp as RpcResponse)?.result?.content?.[0]?.text ?? "";
}

function toolErrorCode(resp: unknown): number | null {
  const text = toolErrorText(resp);
  try {
    const code = (JSON.parse(text) as { code?: number }).code;
    return code ?? null;
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------
// admin 端点
// ---------------------------------------------------------------------------

async function adminStatus(port: number): Promise<{ apps: { appId: string }[]; orphanResults: number }> {
  const resp = await fetch(`http://127.0.0.1:${port}/admin/status`);
  return (await resp.json()) as { apps: { appId: string }[]; orphanResults: number };
}

async function adminRotate(port: number, appId: string): Promise<string> {
  const resp = await fetch(`http://127.0.0.1:${port}/admin/rotate-token`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ appId }),
  });
  return ((await resp.json()) as { token: string }).token;
}

async function waitAppOnline(port: number, appId: string, timeoutMs = 8000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const st = await adminStatus(port);
      if (st.apps.some((a) => a.appId === appId)) {
        return true;
      }
    } catch {
      // 未就绪
    }
    await sleep(200);
  }
  return false;
}

async function waitAppOffline(port: number, appId: string, timeoutMs = 8000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const st = await adminStatus(port);
      if (!st.apps.some((a) => a.appId === appId)) {
        return true;
      }
    } catch {
      return true;
    }
    await sleep(200);
  }
  return false;
}

/** 尽力而为：SSE 流上等待 list_changed 通知（非致命检查）。 */
function waitListChanged(port: number, sessionId: string, timeoutMs = 8000): Promise<boolean> {
  return new Promise((resolve) => {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      controller.abort();
      resolve(false);
    }, timeoutMs + 2000);
    void (async () => {
      try {
        const resp = await fetch(`http://127.0.0.1:${port}/mcp`, {
          headers: { Accept: "text/event-stream", "Mcp-Session-Id": sessionId },
          signal: controller.signal,
        });
        const reader = resp.body?.getReader();
        if (!reader) {
          resolve(false);
          return;
        }
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { done, value } = await reader.read();
          if (done) {
            break;
          }
          buffer += decoder.decode(value, { stream: true });
          let idx: number;
          while ((idx = buffer.indexOf("\n\n")) >= 0) {
            const event = buffer.slice(0, idx);
            buffer = buffer.slice(idx + 2);
            if (event.includes("list_changed")) {
              clearTimeout(timer);
              controller.abort();
              resolve(true);
              return;
            }
          }
        }
      } catch {
        // abort 或连接错误
      }
      clearTimeout(timer);
      resolve(false);
    })();
  });
}

// ---------------------------------------------------------------------------
// 被测应用（SDK 侧）
// ---------------------------------------------------------------------------

class E2EController {
  @AgentTool("echo", { description: "回显消息" })
  echo(message: string): { received: string } {
    return { received: message };
  }

  @AgentTool("add", { description: "整数加法" })
  add(a: number, b: number): number {
    return a + b;
  }

  @AgentTool("confirm_op", { description: "需要用户确认的操作", requiresConfirmation: true })
  confirmOp(value: string): { confirmed_value: string } {
    return { confirmed_value: value };
  }

  @AgentTool("fail", { description: "总是失败（业务错误透传）" })
  fail(): never {
    throw new Error("模拟业务失败");
  }

  @AgentTool("slow", { description: "慢操作（测试执行超时）" })
  async slow(seconds = 32): Promise<{ elapsed: number }> {
    await sleep(seconds * 1000); // async 不阻塞事件循环（心跳/重连不受影响）
    return { elapsed: seconds };
  }
}

const ZOD_SCHEMAS: Record<string, unknown> = {
  echo: z.object({ message: z.string() }),
  add: z.object({ a: z.number(), b: z.number() }),
  confirm_op: z.object({ value: z.string() }),
  fail: z.object({}),
  slow: z.object({ seconds: z.number().optional() }),
};

let confirmAnswer = true;

/** 带诊断日志的客户端（info 输出连接/注册/重连事件）。 */
function makeApp(tag = "app"): AgentQuayClient {
  const client = new AgentQuayClient({
    appId: APP_ID,
    appName: "TS E2E App",
    port: 0,
    autoSpawnBridge: false, // 测试脚本自己管理 Bridge
    heartbeatInterval: 30,
    maxRetryInterval: 2,
    onConfirm: async (_message, _args) => confirmAnswer,
    logger: {
      debug: () => {},
      info: (...a) => console.log(`  [${tag}]`, ...a),
      warn: (...a) => console.warn(`  [${tag}]`, ...a),
      error: (...a) => console.error(`  [${tag}]`, ...a),
    },
  });
  client.registerTools(E2EController, { zodSchemas: ZOD_SCHEMAS });
  return client;
}

// ---------------------------------------------------------------------------
// 主测试流程
// ---------------------------------------------------------------------------

test("AgentQuay TS SDK 端到端联调", { timeout: 300_000 }, async (t) => {
  let pass = 0;
  let fail = 0;
  const check = (name: string, cond: boolean, detail = ""): void => {
    if (cond) {
      pass += 1;
      console.log(`  ✅ ${name}`);
    } else {
      fail += 1;
      console.log(`  ❌ ${name} ${detail}`);
    }
  };

  // 1. 启动 Bridge（前台 serve，由本脚本管理生命周期）
  //    先删除端口文件，保证端口状态确定
  const portFile = path.join(os.homedir(), ".agentquay", "port");
  fs.rmSync(portFile, { force: true });
  const bridge: ChildProcess = spawn(BRIDGE_BIN, ["serve"], {
    stdio: "ignore",
    env: { ...process.env, AGENTQUAY_LOG_LEVEL: "debug" },
  });

  const clients: AgentQuayClient[] = [];
  try {
    // 等待端口文件出现（新 Bridge 写入）且健康
    let port = 0;
    for (let i = 0; i < 60; i++) {
      await sleep(200);
      const p = readPortFile();
      if (!p) {
        continue;
      }
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
    if (!port) {
      t.assert.equal(fail, 0, "Bridge 启动失败");
      return;
    }

    // 2. MCP 握手 + 会话
    const rpc = new MCPRawClient(port);
    const init = (await rpc.post("initialize", {
      protocolVersion: "2025-06-18",
      capabilities: {},
      clientInfo: { name: "ts-e2e", version: "1.0" },
    })) as Record<string, unknown>;
    check("MCP initialize 握手", "result" in init, JSON.stringify(init).slice(0, 200));
    check("会话已建立", rpc.sessionId !== null);

    // 3. SDK 应用注册
    const app = makeApp("app1");
    clients.push(app);
    const appTask = track(app.connect()); // 后台保持连接，监听调用
    appTask.catch((e) => console.log(`  [诊断] app1.connect() 失败: ${String(e)}`));
    check("SDK 应用注册上线", await waitAppOnline(port, APP_ID));

    // 4. tools/list
    const toolsResp = (await rpc.post("tools/list", {})) as RpcResponse;
    const tools = toolsResp.result.tools as {
      name: string;
      description: string;
      inputSchema: { required?: string[] };
    }[];
    const names = new Set(tools.map((t) => t.name));
    check("tools/list 返回合成名 ts-e2e-app_echo", names.has("ts-e2e-app_echo"));
    const echoTool = tools.find((t) => t.name === "ts-e2e-app_echo");
    check(
      "描述带应用名前缀 [TS E2E App]",
      echoTool?.description.startsWith("[TS E2E App]") ?? false,
    );
    check(
      "inputSchema 含 required",
      JSON.stringify(echoTool?.inputSchema.required) === '["message"]',
    );

    // 5. 正常调用
    const echoResp = await rpc.post("tools/call", {
      name: "ts-e2e-app_echo",
      arguments: { message: "你好，Agent" },
    });
    const r = echoResp as RpcResponse;
    const echoOk = r.result !== undefined && !r.result.isError;
    check("tools/call 正常调用", echoOk, JSON.stringify(echoResp).slice(0, 200));
    if (echoOk) {
      const text = JSON.parse(toolErrorText(echoResp)) as { received: string };
      check("结果内容正确", text.received === "你好，Agent", JSON.stringify(text));
    }

    // 6. 参数校验（-32003）
    const missingArg = await rpc.post("tools/call", {
      name: "ts-e2e-app_echo",
      arguments: {},
    });
    check("缺必填参数 → -32003", toolErrorCode(missingArg) === -32003, toolErrorText(missingArg).slice(0, 120));
    const wrongType = await rpc.post("tools/call", {
      name: "ts-e2e-app_echo",
      arguments: { message: 123 },
    });
    check("类型错误 → -32003", toolErrorCode(wrongType) === -32003, toolErrorText(wrongType).slice(0, 120));

    // 7. 未知 tool / 未知应用
    const noTool = await rpc.post("tools/call", { name: "ts-e2e-app_no_such_tool", arguments: {} });
    check(
      "未知 tool → 错误响应",
      Boolean((noTool as RpcResponse).error) || toolErrorCode(noTool) !== null,
      JSON.stringify(noTool).slice(0, 150),
    );
    const noApp = await rpc.post("tools/call", { name: "no-such-app_echo", arguments: { message: "x" } });
    check(
      "未知应用 → 错误响应",
      Boolean((noApp as RpcResponse).error) || toolErrorCode(noApp) !== null,
      JSON.stringify(noApp).slice(0, 150),
    );

    // 8. 业务错误透传
    const failResp = await rpc.post("tools/call", { name: "ts-e2e-app_fail", arguments: {} });
    check("业务错误透传 isError", Boolean((failResp as RpcResponse).result?.isError), JSON.stringify(failResp).slice(0, 200));

    // 9. 确认流程：确认成功
    const confirmOk = await rpc.post("tools/call", {
      name: "ts-e2e-app_confirm_op",
      arguments: { value: "v1" },
    });
    check(
      "确认流程（确认）→ 成功",
      Boolean((confirmOk as RpcResponse).result && !(confirmOk as RpcResponse).result?.isError),
      JSON.stringify(confirmOk).slice(0, 200),
    );

    // 10. 确认流程：用户取消 → -32005（临时切换回调，避免替换注册）
    const oldCb = app.onConfirm;
    app.onConfirm = async () => false;
    const confirmNo = await rpc.post("tools/call", {
      name: "ts-e2e-app_confirm_op",
      arguments: { value: "v2" },
    });
    app.onConfirm = oldCb;
    check("确认流程（取消）→ -32005", toolErrorCode(confirmNo) === -32005, toolErrorText(confirmNo).slice(0, 120));

    // 11. 执行超时 → -32004 + 孤儿结果
    const t0 = Date.now();
    const slowResp = await rpc.post("tools/call", {
      name: "ts-e2e-app_slow",
      arguments: { seconds: 32 },
    });
    const elapsed = (Date.now() - t0) / 1000;
    check(
      "执行超时 → -32004",
      toolErrorCode(slowResp) === -32004 && elapsed > 28 && elapsed < 40,
      `code=${toolErrorCode(slowResp)} elapsed=${elapsed.toFixed(1)}s`,
    );
    // 等待迟到结果进入孤儿缓冲（32s sleep 结束）
    let orphanSeen = false;
    for (let i = 0; i < 80; i++) {
      await sleep(500);
      const st = await adminStatus(port);
      if (st.orphanResults >= 1) {
        orphanSeen = true;
        break;
      }
    }
    check("迟到结果进孤儿缓冲", orphanSeen);

    // 12. 同 appId 替换 → ReplacedError（-32007 语义）
    const ssePromise = waitListChanged(port, rpc.sessionId ?? "", 8000);
    const app2 = makeApp("app2");
    clients.push(app2);
    const app2Task = track(app2.connect());
    app2Task.catch((e) => console.log(`  [诊断] app2.connect() 失败: ${String(e)}`));
    const replacedResult = await settle(raceTimeout(appTask, 8000));
    const replaced = !replacedResult.ok && replacedResult.error instanceof ReplacedError;
    console.log(`  [诊断] app1 替换结果: ${replacedResult.ok ? String(replacedResult.value) : String(replacedResult.error)}`);
    check("同 appId 新实例替换旧连接", replaced);
    check("新实例接管注册", await waitAppOnline(port, APP_ID));
    check("SSE list_changed 通知", await ssePromise, "（尽力而为检查）");

    // 13. 断线重连 + token（关闭新实例，旧 token 的客户端重连成功）
    await app2.close();
    check("新实例下线", await waitAppOffline(port, APP_ID));
    const app3 = makeApp();
    clients.push(app3);
    track(app3.connect());
    check("重连自动携带 token 成功", await waitAppOnline(port, APP_ID));

    // 14. rotate-token：旧 token 被拒，新 token 恢复
    const newToken = await adminRotate(port, APP_ID);
    await app3.close();
    await sleep(500);
    const appOld = makeApp();
    clients.push(appOld);
    const oldResult = await settle(raceTimeout(appOld.connect(), 8000));
    check(
      "轮换后旧 token 被拒 (AUTH_FAILED)",
      !oldResult.ok && oldResult.error instanceof AuthFailedError,
      String(oldResult.error ?? oldResult.value),
    );

    await new TokenStore(APP_ID).set(newToken);
    const appNew = makeApp();
    clients.push(appNew);
    track(appNew.connect());
    check("新 token 恢复注册", await waitAppOnline(port, APP_ID));

    // 清理：关闭客户端
    await sleep(500);
    void app2Task;
    for (const c of clients) {
      await c.close();
    }
  } finally {
    bridge.kill();
    try {
      await Promise.race([new Promise((r) => bridge.once("exit", r)), sleep(5000)]);
    } catch {
      bridge.kill("SIGKILL");
    }
  }

  console.log(`\n=== 结果: ${pass} 通过, ${fail} 失败 ===`);
  t.assert.equal(fail, 0, `${fail} 项失败`);
});
