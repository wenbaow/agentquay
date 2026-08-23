/**
 * AgentQuayClient — 连接 Bridge 的应用侧客户端（设计文档 §4.3）。
 *
 * 职责：注册 Tool、心跳、调用分发、确认流程、指数退避重连、token 持久化、
 * auto-spawn 内嵌 Bridge。
 *
 * ```ts
 * const client = new AgentQuayClient({
 *   appId: "music-app",
 *   appName: "Music Player",
 *   port: 0,               // 从 ~/.agentquay/port 自动读取
 *   autoSpawnBridge: true, // 未检测到服务时自动拉起内嵌 Bridge
 * });
 * client.registerTools(MusicController, { zodSchemas: { search: z.object({ keyword: z.string() }) } });
 * await client.connect();  // 保持连接，监听 Agent 调用
 * ```
 */
import WebSocket from "ws";
import type { ChildProcess } from "node:child_process";
import { existsSync } from "node:fs";
import { askConfirmation } from "./confirm";
import { getAgentTools, TOOL_NAME_PATTERN, type ToolSpec } from "./decorator";
import {
  AuthFailedError,
  BridgeConnectionError,
  BridgeUnavailableError,
  ConfirmationError,
  InvokeTimeoutError,
  PageActivationError,
  PageActivationTimeoutError,
  PageNotFoundError,
  ProtocolError,
  RegistrationError,
  ReplacedError,
} from "./errors";
import {
  encode,
  parse,
  type Envelope,
  type InvokePayload,
  type LaunchInfo,
  type RegisterPayload,
  type ToolMetadata,
} from "./protocol";
import { parseParams, resolveInputSchema, type JsonSchema, type ParamInfo } from "./schema";
import { ensureBridge, readPortFile } from "./spawn";
import { TokenStore } from "./tokens";

// 协议消息类型（与 Bridge 一致）
const MSG_REGISTER = "register";
const MSG_REGISTER_ACK = "register_ack";
const MSG_REGISTER_ERROR = "register_error";
const MSG_INVOKE = "invoke";
const MSG_RESULT = "result";
const MSG_CONFIRM = "confirm";
const MSG_CONFIRM_RESULT = "confirm_result";
const MSG_PING = "ping";
const MSG_PONG = "pong";
const MSG_DISCONNECT = "disconnect";
const MSG_NOTIFICATION = "notification";

// 断开原因
const DISCONNECT_NORMAL = "normal";
const DISCONNECT_REPLACED = "replaced";
const DISCONNECT_MIGRATE = "migrate"; // 让位：应用重连到端口文件指向的更强实例（service 模式）

/** 已绑定实例方法的 Tool。 */
export interface ToolBinding {
  name: string;
  description: string;
  inputSchema: unknown; // zod schema 或 JSON Schema；connect 时解析为 resolvedSchema
  resolvedSchema?: JsonSchema;
  requiresConfirmation: boolean;
  timeoutSeconds: number;
  confirmTimeoutSeconds: number;
  /** 已绑定实例的方法（立即注册的调用入口）。 */
  method: (...args: unknown[]) => unknown;
  /** 未绑定的原始方法（参数源码解析 / schema 生成用，避免 bound 函数 toString 失效；
   *  惰性注册时经 apply(target) 调用）。 */
  rawMethod: (...args: unknown[]) => unknown;
  paramNames: ParamInfo[] | null;
  isAsync: boolean;
  /** 页面分组标签（页面智能路由：仅 SDK 内部路由用，不进协议）。 */
  pageKey?: string;
  /** 立即绑定实例（已开页面；惰性注册为 null）。 */
  instance?: object | null;
  /** 惰性工厂（页面未打开时首次调用创建）。 */
  factory?: () => object;
  /** 惰性激活后的弱引用实例。 */
  live?: WeakRef<object> | null;
}

/** 页面激活钩子（页面智能路由 §3.2）。 */
export interface PageActivator {
  /** 自定义导航（接收创建好的实例；UI 主线程执行）。 */
  navigate?: (page: object) => void;
  /** 自定义就绪等待（如 loaded 事件，必须异步等待，禁止阻塞）。 */
  awaitReady?: (page: object) => Promise<void> | void;
}

/** 确认回调：返回 true=确认 / false=取消（支持同步与异步）。 */
export type ConfirmationHandler = (
  message: string,
  arguments_: Record<string, unknown> | null,
  timeoutSeconds: number,
) => boolean | Promise<boolean>;

/** 日志接口（默认只输出 warn/error）。 */
export interface Logger {
  debug(...args: unknown[]): void;
  info(...args: unknown[]): void;
  warn(...args: unknown[]): void;
  error(...args: unknown[]): void;
}

/** AgentQuayClient 构造选项。 */
export interface AgentQuayClientOptions {
  /** 应用 ID（[a-z0-9-]{1,48}，禁止 _ 和 .；如 music-app）。 */
  appId: string;
  /** 应用显示名。 */
  appName: string;
  /** Bridge 地址（默认 127.0.0.1）。 */
  host?: string;
  /** Bridge 端口（0 = 从 ~/.agentquay/port 自动读取）。 */
  port?: number;
  /** 未检测到服务时自动拉起内嵌 Bridge（默认 true）。 */
  autoSpawnBridge?: boolean;
  /** 应用版本号（随注册上报，默认 1.0.0）。 */
  version?: string;
  /** 协议版本（默认 "1.0"）。 */
  protocolVersion?: string;
  /** 心跳间隔秒数（默认 30）。 */
  heartbeatInterval?: number;
  /** 重连退避上限秒数（默认 30）。 */
  maxRetryInterval?: number;
  /** 启动命令（推荐显式指定；缺省自动探测 §5.8）。 */
  launch?: LaunchInfo;
  /** 是否在注册时上报 launch 信息（默认 true）。 */
  autoReportLaunch?: boolean;
  /** 自定义确认回调 async (message, arguments) => boolean，缺省使用终端确认。 */
  onConfirm?: ConfirmationHandler;
  /** 工具调用钩子：在业务方法执行前触发（允许 UI 层拦截并响应）。签名：(toolName, args) => void。 */
  onToolCall?: (toolName: string, args: Record<string, unknown>) => void;
  /** Bridge 广播通知回调。 */
  onNotification?: (message: string) => void;
  /** 自定义日志器（默认只输出 warn/error 到 console）。 */
  logger?: Logger;
}

/** registerTools 选项。 */
export interface RegisterToolsOptions {
  /** 可选增强：tool 名 → zod schema（运行时转换为 inputSchema 供 Bridge 校验）。 */
  zodSchemas?: Record<string, unknown>;
  /** 页面分组标签（页面智能路由）：传入即惰性注册——页面未打开工具也可见，
   *  首次调用才创建实例；pageKey 只是 SDK 内部的分组标签，不进协议、Agent 无感知。 */
  pageKey?: string;
  /** 惰性注册的显式工厂（DI 场景；缺省 new ctor()，需无参构造）。
   *  仅与 pageKey 同时使用时生效。 */
  factory?: () => object;
}

// appId 规范（与 Bridge 一致）：[a-z0-9-]{1,48}
const APP_ID_PATTERN = /^[a-z0-9-]{1,48}$/;

const noop = (): void => {};
const defaultLogger: Logger = {
  debug: noop,
  info: noop,
  warn: (...a) => console.warn("[agentquay]", ...a),
  error: (...a) => console.error("[agentquay]", ...a),
};

/** 自动探测当前进程的启动命令（§5.8，尽力而为，可被 launch 选项覆盖）。
 *  Node 脚本 → [node, 主脚本]；Electron 打包应用 → 应用可执行文件本身。 */
function detectDefaultLaunchInfo(): LaunchInfo {
  const execPath = process.execPath ?? "";
  const args: string[] = [];
  try {
    const script = process.argv[1];
    if (script && existsSync(script)) {
      args.push(script);
    }
  } catch {
    // 无文件系统（如浏览器环境）则仅上报解释器路径
  }
  return { execPath, args, cwd: "", singleInstance: false, launchTimeoutSeconds: 30 };
}

export class AgentQuayClient {
  readonly appId: string;
  readonly appName: string;
  readonly host: string;
  port: number;
  readonly autoSpawnBridge: boolean;
  readonly version: string;
  readonly protocolVersion: string;
  readonly heartbeatInterval: number;
  readonly maxRetryInterval: number;
  /** 上报给 Bridge 的启动命令（§5.8，离线自动拉起用）。 */
  private readonly launchInfo: LaunchInfo | null;
  /** 自定义确认回调（可在运行期切换，与 Python SDK 一致）。 */
  onConfirm?: ConfirmationHandler;
  /** 工具调用钩子（可在运行期切换）。 */
  onToolCall?: (toolName: string, args: Record<string, unknown>) => void;
  readonly onNotification?: (message: string) => void;
  private readonly log: Logger;

  private readonly tools = new Map<string, ToolBinding>();
  /** 页面路由（页面智能路由 §4.2）：pageKey → 创建中的任务（单飞去重）。 */
  private readonly inflight = new Map<string, { promise: Promise<object>; settled: boolean }>();
  /** pageKey → 激活钩子（导航 / 就绪等待）。 */
  private readonly activators = new Map<string, PageActivator>();
  /** 页面激活超时秒数（创建/导航/等待整体计时，默认 15s）。 */
  pageActivationTimeoutSeconds = 15;
  private readonly tokenStore: TokenStore;
  private token: string | null;

  private ws: WebSocket | null = null;
  private spawnedProc: ChildProcess | null = null;
  private stopRequested = false;
  private closed = false;
  private sleepTimer: NodeJS.Timeout | null = null;
  /** 退避等待的可唤醒句柄：close() 时提前 resolve，避免 connect() 永久挂起。 */
  private sleepResolve: (() => void) | null = null;
  private schemasResolved = false;

  constructor(options: AgentQuayClientOptions) {
    if (!APP_ID_PATTERN.test(options.appId)) {
      throw new Error(`appId ${options.appId} 不符合规范 [a-z0-9-]{1,48}（禁止 _ 和 .）`);
    }
    if (!options.appName) {
      throw new Error("appName 不能为空");
    }
    this.appId = options.appId;
    this.appName = options.appName;
    this.host = options.host ?? "127.0.0.1";
    this.port = options.port ?? 0;
    this.autoSpawnBridge = options.autoSpawnBridge ?? true;
    this.version = options.version ?? "1.0.0";
    this.protocolVersion = options.protocolVersion ?? "1.0";
    this.heartbeatInterval = options.heartbeatInterval ?? 30;
    this.maxRetryInterval = options.maxRetryInterval ?? 30;
    this.onConfirm = options.onConfirm;
    this.onToolCall = options.onToolCall;
    this.onNotification = options.onNotification;
    this.log = options.logger ?? defaultLogger;
    this.launchInfo = options.launch
      ? options.launch
      : options.autoReportLaunch !== false
        ? detectDefaultLaunchInfo()
        : null;
    this.tokenStore = new TokenStore(this.appId);
    this.token = null;
  }

  // ------------------------------------------------------------------
  // 工具注册
  // ------------------------------------------------------------------

  /**
   * 扫描被 @AgentTool 标记的方法并登记。
   *
   * - 传控制器**实例**：立即绑定（原行为）
   * - 传控制器**类**：立即实例化（无参构造）；全部为静态方法时无需实例
   * - 传控制器类 + `opts.pageKey`：**惰性注册**（页面智能路由）——页面未打开工具也可见，
   *   首次调用才创建实例；可用 `opts.factory` 提供显式工厂（DI 场景）
   *
   * @param target 控制器实例或类
   * @param opts zodSchemas: 可选增强，tool 名 → zod schema；pageKey/factory: 惰性注册
   * @returns this（支持链式调用）
   */
  registerTools(target: object | Function, opts: RegisterToolsOptions = {}): this {
    let ctor: Function;
    let instance: object | null;
    if (typeof target === "function") {
      ctor = target;
      instance = null;
    } else {
      ctor = (target as { constructor: Function }).constructor;
      instance = target;
    }
    const specs = getAgentTools(ctor);
    if (!specs || specs.length === 0) {
      return this;
    }
    const lazy = !!opts.pageKey;
    // 类输入 + 立即注册：尝试实例化（无参构造）；全部为静态方法时无需实例
    if (!lazy && instance === null) {
      try {
        instance = new (ctor as new () => object)();
      } catch (e) {
        const ctorRecord = ctor as unknown as Record<string, unknown>;
        if (specs.some((s) => typeof ctorRecord[s.method] !== "function")) {
          throw new Error(`控制器含实例方法但无法实例化（需要可访问的无参构造）: ${e}`);
        }
      }
    }
    for (const spec of specs) {
      this.registerSpec(spec, instance, ctor, opts, lazy);
    }
    return this;
  }

  /**
   * 惰性注册（显式工厂，DI 场景，页面智能路由）：首次调用才执行工厂创建实例。
   * 与 C# `RegisterTools(Func<object>, pageKey)` / Python `register_tools_factory` 同构。
   */
  registerToolsFactory(
    ctor: Function,
    factory: () => object,
    pageKey: string,
    opts: RegisterToolsOptions = {},
  ): this {
    if (typeof factory !== "function") {
      throw new TypeError("factory 必须是函数");
    }
    if (!pageKey) {
      throw new Error("pageKey 不能为空");
    }
    return this.registerTools(ctor, { ...opts, pageKey, factory });
  }

  /**
   * 注册 pageKey 的激活钩子（页面智能路由 §3.2）：首次惰性创建后执行一次。
   * navigate 在 UI 主线程执行；awaitReady 必须异步等待（如 loaded 事件），禁止阻塞。
   */
  setPageActivator(pageKey: string, activator: PageActivator): this {
    if (!pageKey) {
      throw new Error("pageKey 不能为空");
    }
    this.activators.set(pageKey, activator);
    return this;
  }

  /** 页面关闭时显式注销：清除弱引用与激活钩子，工厂路径下次调用自动重建。
   *  不调也行——弱引用 GC 后自动失效（JS 的 WeakRef 由引擎回收）。 */
  unregisterPage(pageKey: string): this {
    for (const binding of this.tools.values()) {
      if (binding.pageKey === pageKey) {
        binding.live = null;
      }
    }
    this.activators.delete(pageKey);
    return this;
  }

  private registerSpec(
    spec: ToolSpec,
    instance: object | null,
    ctor: Function,
    opts: RegisterToolsOptions,
    lazy: boolean,
  ): void {
    if (this.tools.has(spec.name)) {
      // 同一 appId 内工具名跨页面全局唯一（页面智能路由 §2.2）
      throw new Error(`tool 名重复（跨页面也须全局唯一）: ${spec.name}`);
    }
    if (!TOOL_NAME_PATTERN.test(spec.name)) {
      throw new Error(`tool 名 ${spec.name} 不符合规范 [a-zA-Z0-9_-]{1,78}`);
    }
    // 实例方法挂 prototype、静态方法挂类本身：惰性注册（无实例）时两处都要找
    let fn: unknown;
    if (instance) {
      fn = (instance as unknown as Record<string, unknown>)[spec.method];
    } else {
      const proto = (ctor as { prototype?: unknown }).prototype as Record<string, unknown> | undefined;
      fn = proto?.[spec.method] ?? (ctor as unknown as Record<string, unknown>)[spec.method];
    }
    if (typeof fn !== "function") {
      throw new Error(`${spec.method} 被标记为 AgentTool 但不可调用`);
    }
    const method = lazy
      ? (fn as (...args: unknown[]) => unknown)
      : (fn as (...args: unknown[]) => unknown).bind(instance);
    this.tools.set(spec.name, {
      name: spec.name,
      description: spec.description,
      inputSchema: opts.zodSchemas?.[spec.name] ?? spec.inputSchema,
      requiresConfirmation: spec.requiresConfirmation,
      timeoutSeconds: spec.timeoutSeconds,
      confirmTimeoutSeconds: spec.confirmTimeoutSeconds,
      method,
      rawMethod: fn as (...args: unknown[]) => unknown,
      paramNames: parseParams(fn),
      isAsync: fn.constructor?.name === "AsyncFunction",
      pageKey: lazy ? opts.pageKey : undefined,
      instance: lazy ? null : instance,
      factory: lazy ? (opts.factory ?? (() => new (ctor as new () => object)())) : undefined,
      live: null,
    });
    this.log.debug(`已登记 tool: ${spec.name}`);
  }

  /** 返回已登记的 tool 名列表。 */
  listTools(): string[] {
    return [...this.tools.keys()];
  }

  // ------------------------------------------------------------------
  // 连接生命周期
  // ------------------------------------------------------------------

  /**
   * 连接 Bridge 并保持（心跳 + 自动重连）。
   *
   * 返回的 Promise 在 close() 时 resolve；连接被同 appId 新实例替换时
   * reject 为 ReplacedError；注册被拒绝（AUTH_FAILED 等）时 reject 对应错误。
   */
  async connect(): Promise<void> {
    if (this.closed) {
      throw new Error("客户端已关闭，无法再次 connect");
    }
    this.stopRequested = false;
    // 加载已持久化的 token（token 钉扎：重连/重启必须携带，首次注册时为空）
    if (this.token === null) {
      this.token = await this.tokenStore.get();
    }
    // 首次连接：确保 Bridge 可用（含 auto-spawn），解析实际端口
    const { port, proc } = await ensureBridge(this.host, this.port, this.autoSpawnBridge);
    this.port = port;
    if (proc) {
      this.spawnedProc = proc;
    }
    await this.resolveSchemas();
    this.log.info(`连接 Bridge: ws://${this.host}:${this.port}/ws`);
    await this.runForever();
  }

  /** 优雅关闭：停止重连、断开连接、清理拉起的 Bridge。 */
  async close(): Promise<void> {
    this.closed = true;
    this.stopRequested = true;
    // 唤醒正在退避等待的 runForever：否则 close() 期间 sleep() 的定时器被下面的
    // clearTimeout 清掉、resolve 永不触发，connect() 会永久挂起
    const wake = this.sleepResolve;
    this.sleepResolve = null;
    if (wake) {
      wake();
    }
    const ws = this.ws;
    if (ws) {
      try {
        ws.close();
      } catch {
        // 已关闭
      }
    }
    this.cleanupSpawned();
  }

  // ------------------------------------------------------------------
  // 内部实现
  // ------------------------------------------------------------------

  /** 将 zod schema / JSON Schema 解析为 inputSchema（仅首次 connect 时执行一次）。 */
  private async resolveSchemas(): Promise<void> {
    if (this.schemasResolved) {
      return;
    }
    this.schemasResolved = true;
    for (const binding of this.tools.values()) {
      binding.resolvedSchema = await resolveInputSchema(binding.inputSchema, binding.rawMethod);
    }
  }

  private async runForever(): Promise<void> {
    let backoff = 1;
    while (!this.stopRequested) {
      try {
        await this.connectOnce();
        backoff = 1;
      } catch (e) {
        if (e instanceof ReplacedError) {
          this.log.warn("连接被同 appId 的新实例替换，停止重连");
          throw e;
        }
        if (e instanceof RegistrationError || e instanceof BridgeUnavailableError) {
          throw e; // 注册被拒 / Bridge 不可用：不重试，向调用方抛错
        }
        if (this.stopRequested) {
          break;
        }
        // Bridge 可能已重启并发生端口漂移（autoPort）：重读权威端口文件
        if (this.port !== 0) {
          const read = readPortFile();
          if (read && read !== this.port) {
            this.log.info(`Bridge 端口变化 ${this.port} → ${read}`);
            this.port = read;
          }
        }
        this.log.warn(`连接失败: ${String(e)}（${backoff}s 后重连）`);
        await this.sleep(backoff * 1000);
        backoff = Math.min(backoff * 2, this.maxRetryInterval);
      }
    }
  }

  /** 建立一次连接：注册 → 消息循环（连接结束/出错时 resolve/reject）。 */
  private connectOnce(): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      let ws: WebSocket;
      try {
        ws = new WebSocket(`ws://${this.host}:${this.port}/ws`, { handshakeTimeout: 10_000 });
      } catch (e) {
        reject(new BridgeConnectionError(`WebSocket 创建失败: ${e}`));
        return;
      }
      this.ws = ws;
      let settled = false;
      const fail = (err: Error) => {
        if (!settled) {
          settled = true;
          cleanup();
          reject(err);
        }
      };
      const done = () => {
        if (!settled) {
          settled = true;
          cleanup();
          resolve();
        }
      };
      const cleanup = () => {
        if (watchdog) {
          clearTimeout(watchdog);
          watchdog = null;
        }
        try {
          ws.close();
        } catch {
          // 已关闭
        }
        this.ws = null;
      };

      let watchdog: NodeJS.Timeout | null = null;

      ws.on("open", () => {
        this.register(ws)
          .then(() => {
            this.log.info(`注册成功: ${this.appId} (tools=${this.tools.size})`);
            // 消息循环：事件驱动 + 心跳 watchdog
            let silentCycles = 0;
            const armWatchdog = () => {
              if (watchdog) {
                clearTimeout(watchdog);
              }
              watchdog = setTimeout(() => {
                silentCycles += 1;
                try {
                  ws.send(encode(MSG_PING, { timestamp: nowSeconds() }));
                } catch (e) {
                  fail(new BridgeConnectionError(`心跳发送失败: ${e}`));
                  return;
                }
                if (silentCycles >= 2) {
                  fail(new BridgeConnectionError("心跳超时（无响应）"));
                  return;
                }
                armWatchdog();
              }, this.heartbeatInterval * 2 * 1000);
            };
            const onMessage = (data: unknown) => {
              silentCycles = 0;
              armWatchdog();
              let env: Envelope;
              try {
                env = parse((data as Buffer).toString());
              } catch (e) {
                this.log.warn(`无法解析 Bridge 消息: ${String(e)}`);
                return;
              }
              this.dispatchMessage(ws, env, fail);
            };
            ws.on("message", onMessage);
            ws.on("close", () => {
              if (watchdog) {
                clearTimeout(watchdog);
                watchdog = null;
              }
              done();
            });
            ws.on("error", (err) => {
              fail(new BridgeConnectionError(`连接错误: ${err.message ?? String(err)}`));
            });
            armWatchdog();
          })
          .catch(fail);
      });
      ws.on("error", (err) => {
        fail(new BridgeConnectionError(`WebSocket 连接失败: ${err.message ?? String(err)}`));
      });
    });
  }

  /**
   * 分发一条消息。disconnect（replaced/shutdown）需要同步 settle 连接，
   * 因此在此同步抛错，由 onMessage 捕获传给 fail；invoke/confirm 等异步处理
   * 不阻塞消息循环（fire-and-forget）。
   */
  private dispatchMessage(
    ws: WebSocket,
    env: Envelope,
    fail: (err: Error) => void,
  ): void {
    switch (env.type) {
      case MSG_DISCONNECT: {
        const reason = ((env.payload as { reason?: string } | undefined)?.reason) ?? DISCONNECT_NORMAL;
        if (reason === DISCONNECT_REPLACED) {
          fail(new ReplacedError("本连接已被同 appId 的新实例替换"));
        } else if (reason === DISCONNECT_MIGRATE) {
          // Bridge 让位给更强的系统服务实例：按普通断线重连，重连会重读端口文件并落到新实例
          fail(new BridgeConnectionError("Bridge 让位迁移"));
        } else {
          fail(new BridgeConnectionError(`Bridge 断开: ${reason}`));
        }
        return;
      }
      case MSG_INVOKE:
        void this.handleInvoke(ws, env.payload as InvokePayload).catch((e) => {
          this.log.error(`invoke 处理异常: ${String(e)}`);
        });
        return;
      case MSG_CONFIRM:
        void this.handleConfirm(ws, env.payload as { requestId: string; message?: string; arguments?: Record<string, unknown> | null; timeoutSeconds?: number }).catch((e) => {
          this.log.warn(`confirm 处理异常: ${String(e)}`);
        });
        return;
      case MSG_PING:
        try {
          ws.send(encode(MSG_PONG, { timestamp: nowSeconds() }));
        } catch (e) {
          fail(new BridgeConnectionError(`pong 发送失败: ${e}`));
        }
        return;
      case MSG_PONG:
        return; // 静默，watchdog 已复位
      case MSG_NOTIFICATION:
        this.onNotification?.((env.payload as { message?: string })?.message ?? "");
        this.log.info(`Bridge 通知: ${(env.payload as { message?: string })?.message ?? ""}`);
        return;
      default:
        this.log.warn(`未知消息类型: ${env.type}`);
    }
  }

  /** 注册（token 钉扎）：等待 register_ack / register_error（10s 超时）。 */
  private register(ws: WebSocket): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      let settled = false;
      const timer = setTimeout(() => {
        if (!settled) {
          settled = true;
          ws.off("message", onMessage);
          reject(new BridgeConnectionError("注册超时（10s 未收到 register_ack）"));
        }
      }, 10_000);
      const onMessage = (data: unknown) => {
        if (settled) {
          return;
        }
        let env: Envelope;
        try {
          env = parse((data as Buffer).toString());
        } catch (e) {
          settled = true;
          clearTimeout(timer);
          ws.off("message", onMessage);
          reject(e instanceof ProtocolError ? e : new ProtocolError(String(e)));
          return;
        }
        if (env.type === MSG_REGISTER_ACK) {
          const token = (env.payload as { token?: string } | undefined)?.token;
          if (token) {
            this.token = token;
            void this.tokenStore.set(token); // 持久化，重连自动携带
          }
          settled = true;
          clearTimeout(timer);
          ws.off("message", onMessage);
          resolve();
          return;
        }
        if (env.type === MSG_REGISTER_ERROR) {
          const payload = (env.payload as { code?: string; message?: string } | undefined) ?? {};
          const code = payload.code ?? "UNKNOWN";
          const message = payload.message ?? "";
          settled = true;
          clearTimeout(timer);
          ws.off("message", onMessage);
          if (code === "AUTH_FAILED") {
            reject(new AuthFailedError(`认证失败: ${message}`));
          } else {
            reject(new RegistrationError(`注册被拒绝 [${code}]: ${message}`, code));
          }
          return;
        }
        settled = true;
        clearTimeout(timer);
        ws.off("message", onMessage);
        reject(new ProtocolError(`注册等待期间收到意外消息: ${env.type}`));
      };
      ws.on("message", onMessage);
      try {
        ws.send(this.buildRegisterMessage());
      } catch (e) {
        settled = true;
        clearTimeout(timer);
        ws.off("message", onMessage);
        reject(new BridgeConnectionError(`发送注册消息失败: ${e}`));
      }
    });
  }

  private buildRegisterMessage(): string {
    const toolsMeta: ToolMetadata[] = [...this.tools.values()].map((t) => {
      const meta: ToolMetadata = {
        name: t.name,
        description: t.description,
        inputSchema: t.resolvedSchema ?? {},
        requiresConfirmation: t.requiresConfirmation,
        timeoutSeconds: t.timeoutSeconds,
        confirmTimeoutSeconds: t.confirmTimeoutSeconds,
      };
      if (t.pageKey) {
        meta.pageKey = t.pageKey; // 可选分组标签（页面智能路由，旧 SDK 不传即空）
      }
      return meta;
    });
    const payload: RegisterPayload = {
      appId: this.appId,
      appName: this.appName,
      version: this.version,
      protocolVersion: this.protocolVersion,
      authToken: this.token ?? "",
      tools: toolsMeta,
      ...(this.launchInfo ? { launch: this.launchInfo } : {}),
    };
    return encode(MSG_REGISTER, payload);
  }

  // ------------------------------------------------------------------
  // 调用分发
  // ------------------------------------------------------------------

  private async handleInvoke(ws: WebSocket, payload: InvokePayload): Promise<void> {
    const requestId = payload.requestId ?? "";
    const toolName = payload.tool ?? "";
    const timeoutSeconds = payload.timeoutSeconds ?? 30;
    const arguments_ = payload.arguments ?? {};
    const binding = this.tools.get(toolName);
    if (!binding) {
      await this.sendResult(ws, requestId, false, null, {
        code: "TOOL_NOT_FOUND",
        message: `tool 不存在: ${toolName}`,
      });
      return;
    }
    this.log.debug(`执行 tool: ${toolName} args=${JSON.stringify(arguments_)}`);
    // 触发工具调用钩子（在业务方法执行前）
    this.onToolCall?.(toolName, arguments_);
try {
      const result = await this.call(binding, arguments_, timeoutSeconds);
      await this.sendResult(ws, requestId, true, result, null);
    } catch (e) {
      if (e instanceof PageActivationTimeoutError) {
        await this.sendResult(ws, requestId, false, null, {
          code: "PAGE_ACTIVATION_TIMEOUT",
          message: rootMessage(e),
        });
      } else if (e instanceof PageNotFoundError) {
        await this.sendResult(ws, requestId, false, null, {
          code: "PAGE_NOT_FOUND",
          message: rootMessage(e),
        });
      } else if (e instanceof PageActivationError) {
        await this.sendResult(ws, requestId, false, null, {
          code: "PAGE_ACTIVATION_FAILED",
          message: rootMessage(e),
        });
      } else if (e instanceof InvokeTimeoutError) {
        await this.sendResult(ws, requestId, false, null, {
          code: "EXECUTION_TIMEOUT",
          message: `执行超时（>${timeoutSeconds}s）`,
        });
      } else {
        this.log.error(`tool 执行异常: ${toolName}`, e);
        await this.sendResult(ws, requestId, false, null, {
          code: "EXECUTION_ERROR",
          message: rootMessage(e),
        });
      }
    }
  }

  /**
   * 调用分发核心（页面智能路由 §4.2）：实例存活直接调；无实例有工厂则单飞创建
   * （带激活超时）；无实例无工厂抛 PageNotFoundError（工具仍在表内，Agent 收到明确错误）。
   */
  private async call(
    binding: ToolBinding,
    arguments_: Record<string, unknown>,
    timeoutSeconds: number,
  ): Promise<unknown> {
    let target: object | null = binding.instance ?? binding.live?.deref() ?? null;
    if (target === null && binding.factory) {
      target = await this.getOrCreate(binding);
    }
    if (target === null) {
      throw new PageNotFoundError(
        `页面 '${binding.pageKey ?? ""}' 未打开且无工厂，无法调用`,
      );
    }
    // 惰性注册：rawMethod.bind(实例) 完成绑定（实例方法/静态方法均正确）
    const method = binding.instance
      ? binding.method
      : (binding.rawMethod as (...args: unknown[]) => unknown).bind(target);
    const bound = bindArguments(binding, arguments_);
    const result = method(...bound);
    // 超时上限 = Bridge 侧执行超时 + 5s 余量（保证 Bridge 先超时，迟到结果进孤儿缓冲）
    const timeoutMs = (timeoutSeconds + 5) * 1000;
    return withTimeout(Promise.resolve(result), timeoutMs, () => {
      return new InvokeTimeoutError(`执行超时（>${timeoutSeconds + 5}s）`);
    });
  }

  /** 单飞：并发调用合并等待同一个创建任务，不会建出两个页面；
   *  任务完成（成功或失败）后回 NotLoaded，下次调用重建。 */
  private getOrCreate(binding: ToolBinding): Promise<object> {
    const key = binding.pageKey ?? binding.name;
    let entry = this.inflight.get(key);
    if (!entry || entry.settled) {
      const promise = this.activate(binding, key);
      entry = { promise, settled: false };
      promise.then(
        () => (entry!.settled = true),
        () => (entry!.settled = true),
      );
      this.inflight.set(key, entry);
    }
    // 超时只中断本调用方（不取消创建任务——与"页面已导航但调用超时"的孤儿机制一致）；
    // 超时按身份让出槽位，下次调用可重建
    return withTimeout(entry.promise, this.pageActivationTimeoutSeconds * 1000, () => {
      if (this.inflight.get(key) === entry) {
        this.inflight.delete(key);
      }
      return new PageActivationTimeoutError(
        `页面激活超时（>${this.pageActivationTimeoutSeconds}s，pageKey=${key}）`,
      );
    });
  }

  /** 激活：工厂创建 → 可选导航 → 等待就绪。成功后先设置弱引用再返回。
   *  工厂/导航/就绪任一抛异常 → PageActivationError（PAGE_ACTIVATION_FAILED）。 */
  private activate(binding: ToolBinding, _key: string): Promise<object> {
    return (async () => {
      let instance: object;
      try {
        instance = binding.factory!();
        if (!instance) {
          throw new PageActivationError(
            `页面工厂返回 null（pageKey=${binding.pageKey ?? ""}）`,
          );
        }
        const act = this.activators.get(binding.pageKey ?? "");
        if (act?.navigate) {
          act.navigate(instance);
        }
        if (act?.awaitReady) {
          await act.awaitReady(instance); // 必须异步等待（如 loaded 事件），禁止阻塞
        }
      } catch (e) {
        if (e instanceof PageActivationError) {
          throw e;
        }
        throw new PageActivationError(
          `页面激活失败（pageKey=${binding.pageKey ?? ""}）: ${rootMessage(e)}`,
        );
      }
      binding.live = new WeakRef(instance);
      return instance;
    })();
  }

  // ------------------------------------------------------------------
  // 确认流程
  // ------------------------------------------------------------------

  private async handleConfirm(
    ws: WebSocket,
    payload: {
      requestId: string;
      message?: string;
      arguments?: Record<string, unknown> | null;
      timeoutSeconds?: number;
    },
  ): Promise<void> {
    const requestId = payload.requestId ?? "";
    const message = payload.message ?? "确认执行操作？";
    const arguments_ = payload.arguments ?? null;
    const timeoutSeconds = payload.timeoutSeconds ?? 120;
    let confirmed = false;
    try {
      if (this.onConfirm) {
        confirmed = Boolean(await this.onConfirm(message, arguments_, timeoutSeconds));
      } else {
        confirmed = await askConfirmation(message, arguments_, timeoutSeconds);
      }
    } catch (e) {
      this.log.warn(`确认流程异常，默认拒绝: ${String(e)}`);
      confirmed = false;
    }
    try {
      ws.send(encode(MSG_CONFIRM_RESULT, { requestId, confirmed }));
    } catch (e) {
      throw new ConfirmationError(`发送确认结果失败: ${e}`);
    }
  }

  private async sendResult(
    ws: WebSocket,
    requestId: string,
    success: boolean,
    data: unknown,
    error: { code: string; message: string } | null,
  ): Promise<void> {
    try {
      // 与 Python / Java SDK 一致：data / error 始终携带（无值时为 null）
      ws.send(
        encode(MSG_RESULT, {
          requestId,
          success,
          data: data ?? null,
          error: error ?? null,
        }),
      );
    } catch (e) {
      this.log.debug(`发送 result 失败（连接可能已断开）: ${requestId}`, e);
    }
  }

  // ------------------------------------------------------------------
  // 清理
  // ------------------------------------------------------------------

  private cleanupSpawned(): void {
    // 内嵌 Bridge 生命周期由自己管理（空闲自回收），宿主退出不再主动 kill
    // ——见 embedded-bridge-lifecycle.md 步骤 1。
    if (this.spawnedProc) {
      this.log.info(`内嵌 Bridge 交由空闲自回收退出 (pid ${this.spawnedProc.pid})`);
      this.spawnedProc = null;
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      if (this.stopRequested) {
        resolve();
        return;
      }
      const timer = setTimeout(() => {
        this.sleepTimer = null;
        this.sleepResolve = null;
        resolve();
      }, ms);
      this.sleepTimer = timer;
      this.sleepResolve = () => {
        if (this.sleepTimer) {
          clearTimeout(this.sleepTimer);
          this.sleepTimer = null;
        }
        this.sleepResolve = null;
        resolve();
      };
    });
  }
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

function nowSeconds(): number {
  return Math.floor(Date.now() / 1000);
}

function rootMessage(e: unknown): string {
  if (e instanceof Error) {
    return e.message;
  }
  return String(e);
}

/**
 * 按参数名将 JSON arguments 绑定到方法参数：
 * - 参数名可解析 → 按声明顺序传位置参数，未知键忽略
 * - 单参数且参数名不在 arguments 键中（如 (args: {...}) 风格）→ 整个对象作为唯一参数
 * - 无法解析参数名 → 整个对象作为唯一参数
 */
function bindArguments(binding: ToolBinding, arguments_: Record<string, unknown>): unknown[] {
  const names = binding.paramNames;
  if (names === null) {
    return [arguments_];
  }
  if (names.length === 0) {
    return [];
  }
  if (names.length === 1 && !(names[0].name in arguments_)) {
    return [arguments_];
  }
  return names.map((p) => arguments_[p.name]);
}

/** 给 Promise 加超时（原 Promise 继续执行，结果迟到进 Bridge 孤儿缓冲）。 */
function withTimeout<T>(
  promise: Promise<T>,
  ms: number,
  makeError: () => Error,
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(makeError()), ms);
    promise.then(
      (v) => {
        clearTimeout(timer);
        resolve(v);
      },
      (e) => {
        clearTimeout(timer);
        reject(e);
      },
    );
  });
}
