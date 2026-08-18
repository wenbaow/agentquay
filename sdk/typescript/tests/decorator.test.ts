/**
 * decorator.ts 单测：装饰器元数据挂载与注册扫描（对齐 Python test_decorators.py）。
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import { AgentQuayClient, AgentTool, getAgentTools, TOOL_ATTR } from "../src/index";

class Music {
  @AgentTool("search", { description: "搜索音乐" })
  search(keyword: string): string[] {
    return [];
  }

  @AgentTool({ description: "默认名回退方法名" })
  play(songId: string): void {
    void songId;
  }

  @AgentTool("delete", { description: "删除", requiresConfirmation: true, timeoutSeconds: 15 })
  delete(songId: string): void {
    void songId;
  }

  plain(): void {
    // 未装饰，不应被扫描
  }
}

test("spec 元数据挂载到类构造器", () => {
  const specs = getAgentTools(Music);
  assert.ok(specs);
  assert.equal(specs!.length, 3);
  const search = specs!.find((s) => s.name === "search");
  assert.ok(search);
  assert.equal(search!.description, "搜索音乐");
  assert.equal(search!.requiresConfirmation, false);
  assert.equal(search!.method, "search");
});

test("name 缺省回退方法名", () => {
  const specs = getAgentTools(Music)!;
  const play = specs.find((s) => s.name === "play");
  assert.ok(play);
  assert.equal(play!.description, "默认名回退方法名");
});

test("确认/超时标记", () => {
  const specs = getAgentTools(Music)!;
  const del = specs.find((s) => s.name === "delete");
  assert.ok(del);
  assert.equal(del!.requiresConfirmation, true);
  assert.equal(del!.timeoutSeconds, 15);
  assert.equal(del!.confirmTimeoutSeconds, 120);
});

test("registerTools 扫描（实例输入）", () => {
  const client = new AgentQuayClient({ appId: "music-app", appName: "Music Player", autoSpawnBridge: false });
  client.registerTools(new Music());
  assert.deepEqual([...client.listTools()].sort(), ["delete", "play", "search"]);
});

test("registerTools 扫描（类输入，自动实例化）", () => {
  const client = new AgentQuayClient({ appId: "music-app", appName: "Music Player", autoSpawnBridge: false });
  client.registerTools(Music);
  assert.deepEqual([...client.listTools()].sort(), ["delete", "play", "search"]);
});

test("非法 tool 名在装饰期被拒绝", () => {
  assert.throws(() => {
    class Bad {
      @AgentTool("bad name")
      f(): void {}
    }
    void Bad;
  }, /不符合规范/);
});

test("重复 tool 名在注册期被拒绝", () => {
  class Dup {
    @AgentTool("x")
    a(): void {}

    @AgentTool("x")
    b(): void {}
  }
  const client = new AgentQuayClient({ appId: "music-app", appName: "Music Player", autoSpawnBridge: false });
  assert.throws(() => client.registerTools(Dup), /tool 名重复/);
});

test("appId 校验", () => {
  assert.throws(() => new AgentQuayClient({ appId: "music_app", appName: "x" }), /不符合规范/); // 下划线非法
  assert.throws(() => new AgentQuayClient({ appId: "Music.App", appName: "x" }), /不符合规范/); // 大写/点非法
  assert.throws(() => new AgentQuayClient({ appId: "a".repeat(49), appName: "x" }), /不符合规范/); // 超长
  new AgentQuayClient({ appId: "music-app", appName: "x" }); // 合法
});

test("TOOL_ATTR 属性名与设计文档一致", () => {
  assert.equal(TOOL_ATTR, "__agentTools");
});
