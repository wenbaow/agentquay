/**
 * schema.ts 单测：参数源码解析 → JSON Schema、zod 转换（对齐 Python test_schema.py）。
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import { z } from "zod";
import { paramSchema, parseParams, resolveInputSchema, zodToJsonSchema } from "../src/index";

test("parseParams：必填/默认值", () => {
  function f(keyword: string, limit = 10): void {}
  const params = parseParams(f);
  assert.deepEqual(params, [
    { name: "keyword", hasDefault: false },
    { name: "limit", hasDefault: true },
  ]);
});

test("parseParams：async 方法 / 箭头函数 / 单参箭头", async () => {
  async function g(a: number, b: string): Promise<void> {}
  assert.deepEqual(parseParams(g), [
    { name: "a", hasDefault: false },
    { name: "b", hasDefault: false },
  ]);
  const arrow = (x: number, y = 1): number => x + y;
  assert.deepEqual(parseParams(arrow), [
    { name: "x", hasDefault: false },
    { name: "y", hasDefault: true },
  ]);
  const single = (v: string): string => v;
  assert.deepEqual(parseParams(single), [{ name: "v", hasDefault: false }]);
});

test("parseParams：rest 跳过、解构返回 null", () => {
  function f(a: string, ...rest: number[]): void {
    void rest;
  }
  assert.deepEqual(parseParams(f), [{ name: "a", hasDefault: false }]);
  function g({ a }: { a: number }): void {
    void a;
  }
  assert.equal(parseParams(g), null); // 解构 → 无法按名绑定
});

test("paramSchema：required 与 properties", () => {
  function f(keyword: string, limit = 10): void {}
  const schema = paramSchema(f);
  assert.equal(schema.type, "object");
  assert.deepEqual(schema.required, ["keyword"]);
  assert.deepEqual(schema.properties, { keyword: {}, limit: {} });
});

test("paramSchema：默认值参数不在 required 中", () => {
  function f(tag: string | null = null): void {}
  const schema = paramSchema(f);
  assert.equal(schema.required, undefined);
  assert.deepEqual(schema.properties, { tag: {} });
});

test("paramSchema：无参数方法", () => {
  function f(): void {}
  const schema = paramSchema(f);
  assert.equal(schema.required, undefined);
  assert.deepEqual(schema.properties, {});
});

test("zodToJsonSchema：z.object → JSON Schema", async () => {
  const schema = z.object({ keyword: z.string(), limit: z.number().optional() });
  const json = await zodToJsonSchema(schema);
  assert.equal(json.type, "object");
  assert.deepEqual(json.required, ["keyword"]);
  assert.deepEqual((json.properties as Record<string, { type?: string }>).keyword, {
    type: "string",
  });
  assert.deepEqual((json.properties as Record<string, { type?: string }>).limit, {
    type: "number",
  });
});

test("resolveInputSchema：zod 优先，JSON Schema 原样，缺省自动生成", async () => {
  const method = (keyword: string): void => {
    void keyword;
  };
  // zod → 转换
  const fromZod = await resolveInputSchema(z.object({ keyword: z.string() }), method);
  assert.equal((fromZod.properties as Record<string, unknown>).keyword.type, "string");
  // 原生 JSON Schema → 原样
  const raw = { type: "object", properties: { keyword: { type: "string" } } };
  assert.equal(await resolveInputSchema(raw, method), raw);
  // 缺省 → 参数源码解析
  const auto = await resolveInputSchema(undefined, method);
  assert.deepEqual(auto.required, ["keyword"]);
});
