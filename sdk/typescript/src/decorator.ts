/**
 * AgentTool 装饰器：标记方法为 Agent Tool（设计文档 §4.3）。
 *
 * 装饰器把 Tool 元数据挂载到类构造器 `__agentTools`（遗留装饰器，设计文档约定）
 * 或方法函数 `__agentquay_tool__`（标准装饰器），`AgentQuayClient.registerTools()`
 * 时通过 getAgentTools 统一收集。
 *
 * 兼容两种装饰器语义：
 * - 遗留装饰器（tsc + experimentalDecorators，设计文档推荐）：(target, propertyKey, descriptor)
 * - 标准装饰器（TS 5 / esbuild / tsx）：(value, context)，
 *   元数据急切挂到方法函数本身（getAgentTools 扫描原型链兜底）
 */
import type { JsonSchema } from "./schema";

/** 类构造器上的 Tool 元数据属性名（设计文档 §4.3）。 */
export const TOOL_ATTR = "__agentTools";

/** 方法函数上的 Tool 元数据属性名（标准装饰器路径）。 */
export const METHOD_TOOL_ATTR = "__agentquay_tool__";

/** tool 名规范（与 Bridge 一致）：[a-zA-Z0-9_-]{1,78} */
export const TOOL_NAME_PATTERN = /^[a-zA-Z0-9_-]{1,78}$/;

/** AgentTool 装饰器选项。 */
export interface AgentToolOptions {
  /** Tool 名（缺省回退到方法名）。 */
  name?: string;
  /** 描述。 */
  description?: string;
  /** 危险操作需要用户确认（Bridge 触发确认流程）。 */
  requiresConfirmation?: boolean;
  /** 执行超时（默认 30s，Bridge 侧独立计时）。 */
  timeoutSeconds?: number;
  /** 确认超时（默认 120s，独立于执行超时）。 */
  confirmTimeoutSeconds?: number;
  /** 直接提供 inputSchema（JSON Schema 对象）；缺省时按方法参数自动生成。 */
  inputSchema?: JsonSchema;
}

/** 单个 Tool 的元数据（装饰期挂载）。 */
export interface ToolSpec {
  name: string;
  description: string;
  requiresConfirmation: boolean;
  timeoutSeconds: number;
  confirmTimeoutSeconds: number;
  /** 被装饰的方法名（类属性键）。 */
  method: string;
  inputSchema?: JsonSchema;
}

/**
 * 标记方法为 Agent Tool。
 *
 * 用法：
 * ```ts
 * class MusicController {
 *   @AgentTool("search", { description: "搜索音乐库" })
 *   search(keyword: string) { ... }
 *
 *   @AgentTool({ description: "默认名回退方法名" })
 *   play(songId: string) { ... }
 * }
 * ```
 */
export function AgentTool(
  nameOrOptions?: string | AgentToolOptions,
  options?: AgentToolOptions,
): (targetOrValue: unknown, contextOrKey: unknown, descriptor?: PropertyDescriptor) => void {
  const opts: AgentToolOptions =
    typeof nameOrOptions === "string" ? { ...(options ?? {}), name: nameOrOptions } : (nameOrOptions ?? {});
  return (targetOrValue: unknown, contextOrKey: unknown, _descriptor?: PropertyDescriptor): void => {
    // 标准装饰器语义：(value, context) — context 是对象，无类构造器引用，
    // 元数据挂到方法函数本身，由 getAgentTools 扫描原型链收集
    if (typeof contextOrKey === "object" && contextOrKey !== null) {
      const methodName = (contextOrKey as { name: string | symbol }).name;
      const spec = makeSpec(opts, methodName);
      attachMethodSpec(targetOrValue, spec);
      return; // 不替换方法
    }
    // 遗留装饰器语义：(target, propertyKey, descriptor)
    const spec = makeSpec(opts, contextOrKey as string | symbol);
    attachSpec((targetOrValue as { constructor: unknown }).constructor, spec);
  };
}

/** 构造并校验 ToolSpec（name 缺省回退方法名）。 */
function makeSpec(opts: AgentToolOptions, methodName: string | symbol): ToolSpec {
  const name = opts.name ?? String(methodName);
  if (!TOOL_NAME_PATTERN.test(name)) {
    throw new Error(`tool 名 ${name} 不符合规范 [a-zA-Z0-9_-]{1,78}`);
  }
  return {
    name,
    description: opts.description ?? "",
    requiresConfirmation: opts.requiresConfirmation ?? false,
    timeoutSeconds: opts.timeoutSeconds ?? 30,
    confirmTimeoutSeconds: opts.confirmTimeoutSeconds ?? 120,
    method: String(methodName),
    inputSchema: opts.inputSchema,
  };
}

/** 遗留装饰器路径：挂到类构造器。 */
function attachSpec(ctor: unknown, spec: ToolSpec): void {
  if (typeof ctor !== "function") {
    return;
  }
  const holder = ctor as unknown as Record<string, unknown>;
  const list = (holder[TOOL_ATTR] as ToolSpec[] | undefined) ?? [];
  list.push(spec);
  holder[TOOL_ATTR] = list;
}

/** 标准装饰器路径：挂到方法函数本身。 */
function attachMethodSpec(fn: unknown, spec: ToolSpec): void {
  if (typeof fn === "function") {
    (fn as unknown as Record<string, unknown>)[METHOD_TOOL_ATTR] = spec;
  }
}

/**
 * 取类构造器上的 ToolSpec 列表（无则返回 null）。
 *
 * 优先读 `ctor.__agentTools`（遗留装饰器）；标准装饰器路径下扫描
 * 构造器自身（静态方法）与原型链（实例方法）上的方法级元数据。
 */
export function getAgentTools(ctor: unknown): ToolSpec[] | null {
  if (typeof ctor !== "function") {
    return null;
  }
  const holder = ctor as unknown as Record<string, unknown>;
  const list = holder[TOOL_ATTR];
  if (Array.isArray(list)) {
    return list as ToolSpec[];
  }
  // 标准装饰器路径：扫描方法级元数据
  const specs: ToolSpec[] = [];
  const push = (s: unknown): void => {
    if (s && typeof s === "object") {
      const spec = s as ToolSpec;
      // 按 name+method 去重（原型链扫描可能跨层级重复），重复 name 由 registerTools 报错
      if (!specs.some((x) => x.name === spec.name && x.method === spec.method)) {
        specs.push(spec);
      }
    }
  };
  for (const key of Object.getOwnPropertyNames(ctor)) {
    const fn = (holder as Record<string, unknown>)[key];
    if (typeof fn === "function") {
      push((fn as unknown as Record<string, unknown>)[METHOD_TOOL_ATTR]);
    }
  }
  let proto: unknown = (ctor as { prototype?: unknown }).prototype;
  while (proto !== null && proto !== undefined && proto !== Object.prototype) {
    const ph = proto as Record<string, unknown>;
    for (const key of Object.getOwnPropertyNames(ph)) {
      const fn = ph[key];
      if (typeof fn === "function") {
        push((fn as unknown as Record<string, unknown>)[METHOD_TOOL_ATTR]);
      }
    }
    proto = Object.getPrototypeOf(proto);
  }
  return specs.length > 0 ? specs : null;
}
