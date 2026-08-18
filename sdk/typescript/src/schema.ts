/**
 * JSON Schema 生成（设计文档 §4.3）。
 *
 * TS 运行时无类型信息，三种来源（优先级递减）：
 * 1. AgentTool 选项直接提供 `inputSchema`
 * 2. registerTools 的 `zodSchemas`（可选依赖 zod-to-json-schema 转换）
 * 3. 从函数源码解析参数名（尽力而为）：生成 properties + required，
 *    参数类型未知时属性为 `{}`（任意值，Bridge 校验不拦截）
 */
export type JsonSchema = Record<string, unknown>;

/** 解析出的参数信息。 */
export interface ParamInfo {
  name: string;
  hasDefault: boolean;
}

const parseCache = new WeakMap<Function, ParamInfo[] | null>();

/**
 * 从函数源码解析参数列表（尽力而为）。
 * - 解构参数 / 无法解析 → 返回 null（调用方回退为"整个 arguments 对象作为唯一参数"）
 * - 跳过 rest 参数；默认值仅记录 hasDefault（类型运行时不可知）
 */
export function parseParams(fn: Function): ParamInfo[] | null {
  const cached = parseCache.get(fn);
  if (cached !== undefined) {
    return cached;
  }
  let src: string;
  try {
    src = Function.prototype.toString.call(fn);
  } catch {
    parseCache.set(fn, null);
    return null;
  }
  src = src.replace(/\/\*[\s\S]*?\*\//g, ""); // 去块注释（参数默认值内少见）
  const open = src.indexOf("(");
  if (open < 0) {
    // 无括号：单参数箭头函数 `x => ...`
    const arrow = src.indexOf("=>");
    if (arrow < 0) {
      parseCache.set(fn, null);
      return null;
    }
    const name = src.slice(0, arrow).trim();
    if (!/^[A-Za-z_$][\w$]*$/.test(name)) {
      parseCache.set(fn, null);
      return null;
    }
    const info: ParamInfo[] = [{ name, hasDefault: false }];
    parseCache.set(fn, info);
    return info;
  }
  const close = findMatchingParen(src, open);
  if (close < 0) {
    parseCache.set(fn, null);
    return null;
  }
  const parts = splitTopLevel(src.slice(open + 1, close));
  const params: ParamInfo[] = [];
  for (const part of parts) {
    const trimmed = part.trim();
    if (!trimmed) {
      continue;
    }
    if (trimmed.startsWith("...")) {
      continue; // rest 参数跳过
    }
    if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
      parseCache.set(fn, null); // 解构参数：无法按名绑定
      return null;
    }
    const eq = findTopLevelChar(trimmed, "=");
    const name = (eq >= 0 ? trimmed.slice(0, eq) : trimmed).trim();
    if (!/^[A-Za-z_$][\w$]*$/.test(name)) {
      parseCache.set(fn, null);
      return null;
    }
    params.push({ name, hasDefault: eq >= 0 });
  }
  parseCache.set(fn, params);
  return params;
}

/** 生成 inputSchema：{ type: "object", properties, required }。 */
export function paramSchema(fn: Function): JsonSchema {
  const params = parseParams(fn);
  const properties: Record<string, JsonSchema> = {};
  const required: string[] = [];
  if (params) {
    for (const p of params) {
      properties[p.name] = {}; // 类型未知 → 任意值
      if (!p.hasDefault) {
        required.push(p.name);
      }
    }
  }
  const schema: JsonSchema = { type: "object", properties };
  if (required.length > 0) {
    schema.required = required;
  }
  return schema;
}

/**
 * 解析 Tool 的 inputSchema：zod schema（有 safeParse 方法）→ JSON Schema；
 * 其余按 JSON Schema 原样返回；未提供 → 从方法参数自动生成。
 */
export async function resolveInputSchema(
  inputSchema: unknown,
  method: Function,
): Promise<JsonSchema> {
  if (inputSchema === undefined) {
    return paramSchema(method);
  }
  if (isZodLike(inputSchema)) {
    return zodToJsonSchema(inputSchema);
  }
  return inputSchema as JsonSchema;
}

/** 判断是否 zod schema（zod 实例有 safeParse 方法；纯 JSON Schema 对象没有）。 */
export function isZodLike(schema: unknown): boolean {
  return (
    typeof schema === "object" &&
    schema !== null &&
    typeof (schema as { safeParse?: unknown }).safeParse === "function"
  );
}

/**
 * zod schema → JSON Schema（可选依赖 zod-to-json-schema）。
 * 未安装时抛错（zodSchemas 是可选增强，核心链路不依赖）。
 */
export async function zodToJsonSchema(schema: unknown): Promise<JsonSchema> {
  try {
    const mod = (await import("zod-to-json-schema")) as {
      zodToJsonSchema?: (s: unknown) => unknown;
      default?: { zodToJsonSchema?: (s: unknown) => unknown } | ((s: unknown) => unknown);
    };
    const convert =
      mod.zodToJsonSchema ??
      (typeof mod.default === "function" ? mod.default : mod.default?.zodToJsonSchema);
    if (typeof convert !== "function") {
      throw new Error("模块未导出 zodToJsonSchema");
    }
    return convert(schema) as JsonSchema;
  } catch (e) {
    throw new Error(`Zod schema 转换失败（需安装 zod + zod-to-json-schema）: ${e}`);
  }
}

// ---------------------------------------------------------------------------
// 源码解析辅助（带引号/嵌套感知，尽力而为）
// ---------------------------------------------------------------------------

/** 从 open 位置找匹配的右括号（感知字符串与转义）。找不到返回 -1。 */
function findMatchingParen(src: string, open: number): number {
  let depth = 0;
  let quote: string | null = null;
  for (let i = open; i < src.length; i++) {
    const ch = src[i];
    if (quote) {
      if (ch === "\\") {
        i++;
      } else if (ch === quote) {
        quote = null;
      }
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
    } else if (ch === "(") {
      depth++;
    } else if (ch === ")") {
      depth--;
      if (depth === 0) {
        return i;
      }
    }
  }
  return -1;
}

/** 按顶层逗号切分（括号/方括号/花括号与字符串内逗号不切分）。 */
function splitTopLevel(body: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let quote: string | null = null;
  let start = 0;
  for (let i = 0; i < body.length; i++) {
    const ch = body[i];
    if (quote) {
      if (ch === "\\") {
        i++;
      } else if (ch === quote) {
        quote = null;
      }
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
    } else if (ch === "(" || ch === "[" || ch === "{") {
      depth++;
    } else if (ch === ")" || ch === "]" || ch === "}") {
      depth--;
    } else if (ch === "," && depth === 0) {
      parts.push(body.slice(start, i));
      start = i + 1;
    }
  }
  parts.push(body.slice(start));
  return parts;
}

/** 在顶层找指定字符（跳过嵌套与字符串）。 */
function findTopLevelChar(s: string, target: string): number {
  let depth = 0;
  let quote: string | null = null;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (quote) {
      if (ch === "\\") {
        i++;
      } else if (ch === quote) {
        quote = null;
      }
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch;
    } else if (ch === "(" || ch === "[" || ch === "{") {
      depth++;
    } else if (ch === ")" || ch === "]" || ch === "}") {
      depth--;
    } else if (ch === target && depth === 0) {
      return i;
    }
  }
  return -1;
}
