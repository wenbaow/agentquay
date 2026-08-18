/**
 * AgentQuay TypeScript SDK。
 *
 * 让 AI Agent 通过标准 MCP 协议发现并调用你的桌面应用方法。
 *
 * 快速开始：
 * ```ts
 * import { AgentQuayClient, AgentTool } from "@agentquay/sdk";
 *
 * class MusicController {
 *   @AgentTool("search", { description: "搜索音乐库" })
 *   search(keyword: string) {
 *     return [{ id: "1", title: "七里香", artist: "周杰伦" }];
 *   }
 * }
 *
 * const client = new AgentQuayClient({ appId: "music-app", appName: "Music Player" });
 * client.registerTools(MusicController);
 * await client.connect();
 * ```
 */
export { AgentQuayClient } from "./client";
export type {
  AgentQuayClientOptions,
  ConfirmationHandler,
  Logger,
  RegisterToolsOptions,
  ToolBinding,
} from "./client";
export { AgentTool } from "./decorator";
export type { AgentToolOptions, ToolSpec } from "./decorator";
export { TOOL_ATTR, TOOL_NAME_PATTERN, getAgentTools } from "./decorator";
export { paramSchema, parseParams, resolveInputSchema, zodToJsonSchema } from "./schema";
export type { JsonSchema, ParamInfo } from "./schema";
export {
  AgentQuayError,
  AuthFailedError,
  BridgeConnectionError,
  BridgeSpawnError,
  BridgeUnavailableError,
  ConfirmationError,
  InvokeTimeoutError,
  ProtocolError,
  RegistrationError,
  ReplacedError,
  ToolCallError,
} from "./errors";
export type {
  ConfirmPayload,
  Envelope,
  ErrorInfo,
  InvokePayload,
  LaunchInfo,
  RegisterPayload,
  ResultMessage,
  ToolMetadata,
} from "./protocol";
export { MSG_REGISTER, MSG_RESULT } from "./protocol";

export const __version__ = "0.1.0";
