// Package protocol 定义 Bridge 与桌面应用之间的 WebSocket 消息格式（设计文档 §3.1、附录 A）。
package protocol

import (
	"encoding/json"

	"github.com/gorilla/websocket"

	"agentquay/bridge/internal/types"
)

// 消息类型（附录 A）。
const (
	MsgRegister      = "register"       // 应用 → Bridge：注册应用及 Tool 列表
	MsgRegisterAck   = "register_ack"   // Bridge → 应用：注册成功（首次注册含分配的 token）
	MsgRegisterError = "register_error" // Bridge → 应用：注册失败
	MsgInvoke        = "invoke"         // Bridge → 应用：调用指定 Tool
	MsgResult        = "result"         // 应用 → Bridge：Tool 执行结果
	MsgConfirm       = "confirm"        // Bridge → 应用：请求用户确认（危险操作）
	MsgConfirmResult = "confirm_result" // 应用 → Bridge：用户确认/取消结果
	MsgPing          = "ping"           // 双向心跳
	MsgPong          = "pong"           // 双向心跳响应
	MsgDisconnect    = "disconnect"     // 双向优雅断开通知
	MsgNotification  = "notification"   // Bridge → 应用：广播通知
)

// 断开原因。
const (
	DisconnectNormal   = "normal"   // 正常关闭
	DisconnectReplaced = "replaced" // 被同 appId 的新实例替换
	DisconnectShutdown = "shutdown" // Bridge 即将停止
	DisconnectMigrate  = "migrate"  // 让位：应用请重连到端口文件指向的更强实例（service 模式）
)

// Envelope 是所有 WebSocket 消息的通用外壳。
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RegisterErrorCode 注册失败原因。
type RegisterErrorCode string

const (
	CodeBadRequest         RegisterErrorCode = "BAD_REQUEST"         // 注册消息格式错误（扩展）
	CodeAuthFailed         RegisterErrorCode = "AUTH_FAILED"         // token 不匹配
	CodeUnsupportedVersion RegisterErrorCode = "UNSUPPORTED_VERSION" // 协议版本不兼容
	CodeInvalidAppID       RegisterErrorCode = "INVALID_APP_ID"      // appId 不符合命名规范
	CodeInvalidTool        RegisterErrorCode = "INVALID_TOOL"        // tool 名不符合规范或重复（扩展）
)

// LaunchInfo 应用 → Bridge：可选的启动信息（§5.8，V1 可选字段，旧 SDK 不传即无法自动拉起）。
// 由应用进程自报（它才知道"该启动哪个进程"），Bridge 持久化到 ~/.agentquay/apps/<appId>.json。
type LaunchInfo struct {
	ExecPath             string   `json:"execPath"`                       // 可执行文件绝对路径（必填）
	Args                 []string `json:"args,omitempty"`                 // 启动参数（argv 数组，禁止 shell 字符串）
	Cwd                  string   `json:"cwd,omitempty"`                  // 工作目录（可空）
	SingleInstance       bool     `json:"singleInstance,omitempty"`       // 是否单实例（已在运行时不再重复拉起）
	LaunchTimeoutSeconds int      `json:"launchTimeoutSeconds,omitempty"` // 覆盖全局启动等待超时
}

// RegisterPayload 应用 → Bridge：注册信息（§3.1）。
type RegisterPayload struct {
	AppID           string               `json:"appId"`
	AppName         string               `json:"appName"`
	Version         string               `json:"version"`
	ProtocolVersion string               `json:"protocolVersion"`
	AuthToken       string               `json:"authToken"`
	Tools           []types.ToolMetadata `json:"tools"`
	Launch          *LaunchInfo          `json:"launch,omitempty"` // 可选：启动信息（自动拉起用）
}

// RegisterAckPayload Bridge → 应用：注册成功确认。
type RegisterAckPayload struct {
	AppID string `json:"appId"`
	Token string `json:"token"` // 首次注册时分配的 token；重连时返回当前有效 token
}

// RegisterErrorPayload Bridge → 应用：注册失败。
type RegisterErrorPayload struct {
	Code              RegisterErrorCode `json:"code"`
	Message           string            `json:"message"`
	SupportedVersions []string          `json:"supportedVersions,omitempty"`
}

// InvokePayload Bridge → 应用：调用指定 Tool。
type InvokePayload struct {
	RequestID      string         `json:"requestId"`
	Tool           string         `json:"tool"`
	Arguments      map[string]any `json:"arguments"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
}

// ConfirmPayload Bridge → 应用：请求用户确认（危险操作）。
type ConfirmPayload struct {
	RequestID      string         `json:"requestId"`
	Message        string         `json:"message"`
	Arguments      map[string]any `json:"arguments"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
}

// ConfirmResultPayload 应用 → Bridge：用户确认结果。
type ConfirmResultPayload struct {
	RequestID string `json:"requestId"`
	Confirmed bool   `json:"confirmed"`
}

// ResultPayload 应用 → Bridge：Tool 执行结果（复用 ResultMessage）。
type ResultPayload = types.ResultMessage

// PingPayload 双向心跳（§3.1）。
type PingPayload struct {
	Timestamp int64 `json:"timestamp"`
}

// DisconnectPayload 优雅断开通知。
type DisconnectPayload struct {
	Reason string `json:"reason"`
}

// NotificationPayload Bridge → 应用：广播通知。
type NotificationPayload struct {
	Message string `json:"message"`
}

// Encode 将消息编码为 JSON 字节。
func Encode(msgType string, payload any) ([]byte, error) {
	env := Envelope{Type: msgType}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		env.Payload = raw
	}
	return json.Marshal(env)
}

// Send 序列化并发送一条消息。同一连接的多 goroutine 并发发送需由调用方串行化
// （Application.Send 内部已持有写锁，勿再包装）。
func Send(conn *websocket.Conn, msgType string, payload any) error {
	data, err := Encode(msgType, payload)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}
