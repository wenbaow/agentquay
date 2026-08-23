/**
 * Bridge ↔ 应用的 WebSocket 协议（设计文档 §3.1、附录 A）。
 * 消息外壳：{ "type": "...", "payload": {...} }。
 */
import { ProtocolError } from "./errors";

// 消息类型（附录 A）
export const MSG_REGISTER = "register";
export const MSG_REGISTER_ACK = "register_ack";
export const MSG_REGISTER_ERROR = "register_error";
export const MSG_INVOKE = "invoke";
export const MSG_RESULT = "result";
export const MSG_CONFIRM = "confirm";
export const MSG_CONFIRM_RESULT = "confirm_result";
export const MSG_PING = "ping";
export const MSG_PONG = "pong";
export const MSG_DISCONNECT = "disconnect";
export const MSG_NOTIFICATION = "notification";

// 断开原因
export const DISCONNECT_NORMAL = "normal";
export const DISCONNECT_REPLACED = "replaced";
export const DISCONNECT_SHUTDOWN = "shutdown";

/** 注册失败原因（register_error payload.code）。 */
export type RegisterErrorCode =
  | "BAD_REQUEST"
  | "AUTH_FAILED"
  | "UNSUPPORTED_VERSION"
  | "INVALID_APP_ID"
  | "INVALID_TOOL";

/** 通用消息外壳。 */
export interface Envelope {
  type: string;
  payload?: unknown;
}

/** 注册的应用 Tool 元数据（Bridge 侧 ToolMetadata 的镜像）。 */
export interface ToolMetadata {
  name: string;
  description: string;
  inputSchema: unknown;
  requiresConfirmation: boolean;
  timeoutSeconds: number;
  confirmTimeoutSeconds: number;
  /** 页面分组标签（页面智能路由，V1 可选字段，旧 SDK 不传即空）。 */
  pageKey?: string;
}

/** 业务错误信息（应用返回，Bridge 透传）。 */
export interface ErrorInfo {
  code: string;
  message: string;
  details?: unknown;
}

/** 一次 Tool 调用的执行结果。 */
export interface ResultMessage {
  requestId: string;
  success: boolean;
  data?: unknown;
  error?: ErrorInfo | null;
}

/** 注册消息 payload（应用 → Bridge）。 */
export interface RegisterPayload {
  appId: string;
  appName: string;
  version: string;
  protocolVersion: string;
  authToken: string;
  tools: ToolMetadata[];
  /** 可选的启动命令（§5.8），供 Bridge 离线自动拉起。 */
  launch?: LaunchInfo;
}

/** 启动命令信息（随 register 上报，§5.8）。 */
export interface LaunchInfo {
  /** 可执行文件绝对路径。 */
  execPath: string;
  /** 启动参数（argv 数组，禁止 shell 字符串）。 */
  args?: string[];
  /** 工作目录（可空）。 */
  cwd?: string;
  /** 是否单实例（已在运行时不再重复拉起）。 */
  singleInstance?: boolean;
  /** 覆盖全局启动等待超时秒数。 */
  launchTimeoutSeconds?: number;
}

/** 调用消息 payload（Bridge → 应用）。 */
export interface InvokePayload {
  requestId: string;
  tool: string;
  arguments: Record<string, unknown>;
  timeoutSeconds: number;
}

/** 确认请求 payload（Bridge → 应用）。 */
export interface ConfirmPayload {
  requestId: string;
  message: string;
  arguments: Record<string, unknown> | null;
  timeoutSeconds: number;
}

/** 编码一条消息（payload 省略时仅含 type）。 */
export function encode(type: string, payload?: unknown): string {
  if (payload === undefined) {
    return JSON.stringify({ type });
  }
  return JSON.stringify({ type, payload });
}

/** 解析外壳消息，返回 { type, payload }。 */
export function parse(raw: string): Envelope {
  let env: unknown;
  try {
    env = JSON.parse(raw);
  } catch (e) {
    throw new ProtocolError(`无法解析 Bridge 消息: ${e}`);
  }
  if (typeof env !== "object" || env === null || typeof (env as Envelope).type !== "string") {
    throw new ProtocolError(`消息缺少 type 字段: ${raw}`);
  }
  return env as Envelope;
}
