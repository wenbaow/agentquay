/**
 * 页面智能路由单测（方案 §9 Phase 1 验收，对齐 C# PageRouterTests / Python test_page_router.py）：
 * 惰性注册元数据立即可见、单飞去重、弱引用重建、激活超时、无工厂报错、
 * 激活钩子执行一次、显式注销、注册消息 pageKey。
 *
 * 通过 handleInvoke（私有方法，运行时可访问）注入伪 ws 捕获 result 消息。
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  AgentQuayClient,
  AgentTool,
  PageNotFoundError,
} from "../src/index";

/** 页面创建计数（模块级，测试间重置）。 */
let CREATED = 0;

class SearchPage {
  seq: number;

  constructor() {
    this.seq = CREATED++;
  }

  @AgentTool("search", { description: "搜索音乐" })
  async search(keyword: string): Promise<{ page: number; keyword: string }> {
    return { page: this.seq, keyword };
  }

  @AgentTool("ping", { description: "同步探针" })
  ping(): number {
    return this.seq;
  }
}

function newClient(): AgentQuayClient {
  return new AgentQuayClient({
    appId: "music-app",
    appName: "Music Player",
    autoSpawnBridge: false,
  });
}

interface FakeWs {
  sent: Array<{ type: string; payload: Record<string, unknown> }>;
  send(msg: string): void;
}

function fakeWs(): FakeWs {
  const ws = {
    sent: [],
    send(msg: string) {
      ws.sent.push(JSON.parse(msg));
    },
  };
  return ws;
}

/** 调用 search 工具并返回 result 消息的 payload。 */
async function invokeSearch(
  client: AgentQuayClient,
  ws: FakeWs,
  keyword = "七里香",
): Promise<Record<string, unknown>> {
  await (client as unknown as {
    handleInvoke(ws: FakeWs, payload: Record<string, unknown>): Promise<void>;
  }).handleInvoke(ws, {
    requestId: `req-${ws.sent.length}`,
    tool: "search",
    timeoutSeconds: 30,
    arguments: { keyword },
  });
  const last = ws.sent[ws.sent.length - 1];
  assert.equal(last.type, "result");
  return last.payload as Record<string, unknown>;
}

/** 并发窗口：awaitReady 挂起在闸门上（模拟 Loaded 事件未到），释放后激活继续。 */
async function withGate<T>(
  fn: (release: () => void) => Promise<T>,
): Promise<T> {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const running = fn(release);
  await new Promise((r) => setTimeout(r, 100)); // 等待全部进入合并等待
  release();
  return running;
}

test("惰性注册：元数据立即可见，实例未创建", () => {
  CREATED = 0;
  const client = newClient();
  let factoryCalls = 0;
  client.registerTools(SearchPage, {
    pageKey: "SearchPage",
    factory: () => {
      factoryCalls++;
      return new SearchPage();
    },
  });
  assert.deepEqual(client.listTools().sort(), ["ping", "search"]);
  assert.equal(factoryCalls, 0); // 未创建实例
});

test("首次调用创建实例，随后复用（弱引用存活）", async () => {
  CREATED = 0;
  const client = newClient();
  let factoryCalls = 0;
  client.registerTools(SearchPage, {
    pageKey: "SearchPage",
    factory: () => {
      factoryCalls++;
      return new SearchPage();
    },
  });
  const ws = fakeWs();

  const r1 = await invokeSearch(client, ws);
  assert.equal(r1.success, true);
  assert.equal(factoryCalls, 1);
  const p1 = (r1.data as { page: number }).page;

  const r2 = await invokeSearch(client, ws);
  assert.equal(r2.success, true);
  assert.equal(factoryCalls, 1);
  assert.equal((r2.data as { page: number }).page, p1);
});

test("并发调用单飞：只创建一个实例", async () => {
  CREATED = 0;
  const client = newClient();
  let factoryCalls = 0;
  client.registerTools(SearchPage, {
    pageKey: "SearchPage",
    factory: () => {
      factoryCalls++;
      return new SearchPage();
    },
  });

  const ws = fakeWs();
  await withGate(async (release) => {
    // 首个调用激活后挂在 awaitReady 闸门上，其余并发调用合并等待同一任务
    client.setPageActivator("SearchPage", { awaitReady: () => {
      const gate = new Promise<void>((resolve) => {
        const orig: () => void = resolve;
        releaseCb = orig;
      });
      return gate;
    } });
    let releaseCb: (() => void) | null = null;
    const calls = Array.from({ length: 10 }, () => invokeSearch(client, ws));
    await new Promise((r) => setTimeout(r, 80));
    releaseCb?.();
    await Promise.all(calls);
    release();
  });

  assert.equal(factoryCalls, 1);
  const pages = ws.sent
    .filter((m) => m.type === "result" && m.payload.success)
    .map((m) => (m.payload.data as { page: number }).page);
  assert.equal(new Set(pages).size, 1);
});

test("unregisterPage 后重建", async () => {
  CREATED = 0;
  const client = newClient();
  let factoryCalls = 0;
  client.registerTools(SearchPage, {
    pageKey: "SearchPage",
    factory: () => {
      factoryCalls++;
      return new SearchPage();
    },
  });
  const ws = fakeWs();
  await invokeSearch(client, ws);
  assert.equal(factoryCalls, 1);

  client.unregisterPage("SearchPage");
  await invokeSearch(client, ws);
  assert.equal(factoryCalls, 2); // 弱引用已清 → 重建
});

test("激活超时 → PAGE_ACTIVATION_TIMEOUT", async () => {
  CREATED = 0;
  const client = newClient();
  client.pageActivationTimeoutSeconds = 0.2;
  client.registerTools(SearchPage, { pageKey: "SearchPage" });
  client.setPageActivator("SearchPage", {
    awaitReady: () => new Promise<void>(() => {}), // 永不就绪
  });
  const ws = fakeWs();
  const payload = await invokeSearch(client, ws);
  assert.equal(payload.success, false);
  assert.equal((payload.error as { code: string }).code, "PAGE_ACTIVATION_TIMEOUT");
});

test("激活失败 → PAGE_ACTIVATION_FAILED，随后可重建", async () => {
  CREATED = 0;
  const client = newClient();
  let flaky = true;
  client.registerTools(SearchPage, {
    pageKey: "SearchPage",
    factory: () => {
      if (flaky) {
        flaky = false;
        throw new Error("DI 容器不可用");
      }
      return new SearchPage();
    },
  });
  const ws = fakeWs();

  const r1 = await invokeSearch(client, ws);
  assert.equal(r1.success, false);
  assert.equal((r1.error as { code: string }).code, "PAGE_ACTIVATION_FAILED");

  const r2 = await invokeSearch(client, ws);
  assert.equal(r2.success, true); // 失败后回 NotLoaded，可重建
  assert.equal((r2.data as { page: number }).page, 0);
});

test("无实例无工厂 → PAGE_NOT_FOUND（工具仍在表内）", async () => {
  CREATED = 0;
  const client = newClient();
  client.registerTools(SearchPage, { pageKey: "SearchPage" });
  const internal = client as unknown as {
    tools: Map<
      string,
      { factory?: () => object; live: unknown; instance: unknown }
    >;
    call(
      binding: { factory?: () => object },
      args: Record<string, unknown>,
      timeout: number,
    ): Promise<unknown>;
  };
  const binding = internal.tools.get("search")!;
  binding.factory = undefined; // 模拟"未配工厂"（防御性路径）
  binding.live = null;
  binding.instance = null;

  await assert.rejects(
    () => internal.call(binding, { keyword: "x" }, 30),
    PageNotFoundError,
  );
});

test("激活钩子：并发激活只执行一次（导航/就绪各一）", async () => {
  CREATED = 0;
  const client = newClient();
  let navigated = 0;
  let ready = 0;
  client.registerTools(SearchPage, { pageKey: "SearchPage" });

  const ws = fakeWs();
  await withGate(async (release) => {
    client.setPageActivator("SearchPage", {
      navigate: () => {
        navigated++;
      },
      awaitReady: () => {
        ready++;
        const gate = new Promise<void>((resolve) => {
          releaseCb = resolve;
        });
        return gate;
      },
    });
    let releaseCb: (() => void) | null = null;
    const calls = Array.from({ length: 10 }, () => invokeSearch(client, ws));
    await new Promise((r) => setTimeout(r, 80));
    releaseCb?.();
    await Promise.all(calls);
    release();
  });

  assert.equal(navigated, 1);
  assert.equal(ready, 1);
});

test("注册消息携带 pageKey", () => {
  const client = newClient();
  client.registerTools(SearchPage, { pageKey: "SearchPage" });
  const msg = (
    client as unknown as { buildRegisterMessage(): string }
  ).buildRegisterMessage();
  const env = JSON.parse(msg) as {
    payload: { tools: Array<{ name: string; pageKey?: string }> };
  };
  const tools = env.payload.tools;
  assert.equal(tools.length, 2);
  for (const t of tools) {
    assert.equal(t.pageKey, "SearchPage");
  }
});