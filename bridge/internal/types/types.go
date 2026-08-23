// Package types 定义 Bridge 核心共享数据类型（对应设计文档 §5.2）。
package types

// ToolMetadata 一个 Tool 的元数据（由应用注册时上报）。
type ToolMetadata struct {
	Name                  string `json:"name"`
	Description           string `json:"description"`
	InputSchema           any    `json:"inputSchema"`
	RequiresConfirmation  bool   `json:"requiresConfirmation"`
	TimeoutSeconds        int    `json:"timeoutSeconds"`
	ConfirmTimeoutSeconds int    `json:"confirmTimeoutSeconds"` // 默认 120
	// PageKey 工具归属的页面分组标签（页面智能路由，V1 可选字段，旧 SDK 不传即空）。
	// 仅供 SDK 内部路由与 tools/list 描述展示使用，不进 invoke 协议，Agent 无感知。
	PageKey string `json:"pageKey,omitempty"`
}

// ErrorInfo 业务错误信息（应用返回，Bridge 透传）。
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details"`
}

// ResultMessage 一次 Tool 调用的执行结果。
// 同时用作：应用 → Bridge 的 WS result 消息 payload 与 Bridge 内部挂起请求的响应通道消息。
type ResultMessage struct {
	RequestID string     `json:"requestId"`
	Success   bool       `json:"success"`
	Data      any        `json:"data"`
	Error     *ErrorInfo `json:"error"`
}

// 内部错误码（Bridge 侧产生，不入业务 code 空间）。
const (
	ErrCodeConnLost    = "CONNECTION_LOST" // 应用断连（映射 MCP -32001）
	ErrCodeReplaced    = "APP_REPLACED"    // 应用被新实例替换（映射 MCP -32007）
	ErrCodeRequestGone = "REQUEST_GONE"    // 请求已被移除（迟到结果）
)
