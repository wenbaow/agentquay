// Package hub 实现 /ws WebSocket 服务（设计文档 §3.1）：
// 应用注册（token 钉扎）、连接替换、心跳检测、调用/确认消息路由、孤儿结果处理。
package hub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/auth"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/protocol"
	"agentquay/bridge/internal/registry"
	"agentquay/bridge/internal/types"
)

// 支持的协议版本（major 相同才接受）。
const SupportedProtocolVersion = "1.0"

// Hub 管理所有应用 WebSocket 连接。
type Hub struct {
	cfg    *config.Config
	reg    *registry.Registry
	auth   *auth.Store
	apps   *apps.Store
	logger *slog.Logger

	upgrader websocket.Upgrader

	// onAppsChanged 在应用注册/注销后被调用（由上层接线：重建 MCP 工具表 + 广播 list_changed）。
	onAppsChanged func()

	mu      sync.Mutex
	conns   int
	stopped bool
}

// New 创建 Hub。
func New(cfg *config.Config, reg *registry.Registry, store *auth.Store, appStore *apps.Store, logger *slog.Logger) *Hub {
	return &Hub{
		cfg:    cfg,
		reg:    reg,
		auth:   store,
		apps:   appStore,
		logger: logger,
		upgrader: websocket.Upgrader{
			// 仅监听 127.0.0.1（默认），不校验 Origin。
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// SetOnAppsChanged 注册应用变更回调（rebuild tools + 广播 list_changed）。
func (h *Hub) SetOnAppsChanged(fn func()) { h.onAppsChanged = fn }

func (h *Hub) notifyAppsChanged() {
	if h.onAppsChanged != nil {
		h.onAppsChanged()
	}
}

// AppCount 返回当前在线应用数（供 embedded 生命周期监视器查询）。
func (h *Hub) AppCount() int { return h.reg.AppCount() }

// MigrateToService 让位给更强的实例（service 模式）：通知全部应用迁移后关闭连接。
// 使用 reason=migrate 而非 replaced，避免 SDK 把它当作"被替换"而停止重连。
func (h *Hub) MigrateToService() {
	n := 0
	for _, app := range h.reg.Apps() {
		if err := app.Send(protocol.MsgDisconnect, &protocol.DisconnectPayload{Reason: protocol.DisconnectMigrate}); err == nil {
			n++
		}
		_ = app.Close()
	}
	if n > 0 {
		h.notifyAppsChanged()
	}
	h.logger.Info("检测到系统服务实例，已通知应用迁移并让位", "appsNotified", n)
}

// Handler 返回 /ws 的 HTTP 处理器。
func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(h.serveWS)
}

func (h *Hub) heartbeatInterval() time.Duration {
	sec := h.cfg.HeartbeatIntervalSeconds
	if sec <= 0 {
		sec = 30
	}
	return time.Duration(sec) * time.Second
}

func (h *Hub) maxConnections() int {
	if h.cfg.MaxConnections <= 0 {
		return 100
	}
	return h.cfg.MaxConnections
}

// SendInvoke 向应用转发工具调用。
func (h *Hub) SendInvoke(app *registry.Application, p *protocol.InvokePayload) error {
	return app.Send(protocol.MsgInvoke, p)
}

// SendConfirm 向应用发送确认请求。
func (h *Hub) SendConfirm(app *registry.Application, p *protocol.ConfirmPayload) error {
	return app.Send(protocol.MsgConfirm, p)
}

// serveWS 升级连接并进入消息循环。
func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.stopped || h.conns >= h.maxConnections() {
		h.mu.Unlock()
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	h.conns++
	h.mu.Unlock()

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.mu.Lock()
		h.conns--
		h.mu.Unlock()
		h.logger.Warn("WebSocket 升级失败", "error", err)
		return
	}
	conn.SetReadLimit(1 << 20) // 1MB
	h.serveConn(conn)
}

// serveConn 单连接消息循环。
func (h *Hub) serveConn(conn *websocket.Conn) {
	defer func() {
		h.mu.Lock()
		h.conns--
		h.mu.Unlock()
		conn.Close()
	}()

	var app *registry.Application
	defer func() {
		// 仅当该连接仍是注册表中的当前连接时才注销（被替换的旧连接不重复注销）。
		if app != nil && h.reg.Get(app.AppID) == app {
			h.unregister(app.AppID, "连接已断开")
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * h.heartbeatInterval()))
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				h.logger.Debug("连接读取结束", "error", err)
			}
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			h.logger.Warn("无法解析 WS 消息", "error", err)
			continue
		}

		switch env.Type {
		case protocol.MsgRegister:
			if app != nil {
				continue // 已注册，忽略重复注册
			}
			app = h.processRegister(conn, env.Payload)
			if app == nil {
				return // 注册失败，连接已关闭
			}
		case protocol.MsgPing:
			pong := &protocol.PingPayload{Timestamp: time.Now().Unix()}
			if app != nil {
				// 已注册：所有发送走 app.Send（持写锁），避免与心跳 goroutine 并发写
				_ = app.Send(protocol.MsgPong, pong)
			} else {
				_ = h.send(conn, protocol.MsgPong, pong)
			}
		case protocol.MsgPong:
			if app != nil {
				app.LastPing = time.Now()
			}
		case protocol.MsgResult:
			if app != nil {
				h.onResult(app, env.Payload)
			}
		case protocol.MsgConfirmResult:
			if app != nil {
				h.onConfirmResult(app, env.Payload)
			}
		case protocol.MsgDisconnect:
			return // 优雅断开
		default:
			h.logger.Warn("未知消息类型", "type", env.Type)
		}
	}
}

// send 向未注册连接发送消息（注册前阶段，AppID 未定，无写锁——单循环内串行，安全）。
func (h *Hub) send(conn *websocket.Conn, msgType string, payload any) error {
	return protocol.Send(conn, msgType, payload)
}

// processRegister 处理注册（§3.1 注册冲突与替换规则）。失败时发送 register_error 并关闭连接，返回 nil。
func (h *Hub) processRegister(conn *websocket.Conn, raw json.RawMessage) *registry.Application {
	var p protocol.RegisterPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		h.failRegister(conn, protocol.CodeBadRequest, "注册消息格式错误: "+err.Error())
		return nil
	}

	// 1. 协议版本校验（major 相同才接受）
	if major(p.ProtocolVersion) != major(SupportedProtocolVersion) {
		h.failRegister(conn, protocol.CodeUnsupportedVersion,
			fmt.Sprintf("协议版本 %s 不受支持", p.ProtocolVersion),
			[]string{SupportedProtocolVersion})
		return nil
	}

	// 2. appId 格式校验
	if !registry.AppIDPattern.MatchString(p.AppID) {
		h.failRegister(conn, protocol.CodeInvalidAppID,
			"appId 只允许 [a-z0-9-]{1,48}，禁止 _ 和 .")
		return nil
	}

	// 3. tools 校验（名称规范 + 去重）。去重按 appId 内全局进行：带上 pageKey 的
	//    页面工具名同样必须唯一（页面智能路由：工具名跨页面全局唯一，不随页面开合变化）。
	seen := make(map[string]bool, len(p.Tools))
	for _, t := range p.Tools {
		if !registry.ToolNamePattern.MatchString(t.Name) {
			h.failRegister(conn, protocol.CodeInvalidTool,
				fmt.Sprintf("tool 名 %q 不符合规范 [a-zA-Z0-9_-]{1,78}", t.Name))
			return nil
		}
		if seen[t.Name] {
			h.failRegister(conn, protocol.CodeInvalidTool,
				fmt.Sprintf("tool 名重复（同一 appId 内跨页面也须全局唯一）: %s", t.Name))
			return nil
		}
		seen[t.Name] = true
	}

	// 4. token 钉扎认证（§6.2）
	token, ok := h.auth.Token(p.AppID)
	if !ok {
		// 首次注册：分配并落盘
		var err error
		token, err = h.auth.Allocate(p.AppID)
		if err != nil {
			h.logger.Error("分配 token 失败", "appId", p.AppID, "error", err)
			h.failRegister(conn, protocol.CodeAuthFailed, "服务端分配 token 失败")
			return nil
		}
		h.logger.Info("首次注册，已分配 token", "appId", p.AppID)
	} else if p.AuthToken != token {
		h.failRegister(conn, protocol.CodeAuthFailed, "认证失败：authToken 不匹配")
		return nil
	}

	// 5. 同名替换：通知旧连接、失败其挂起请求、关闭旧连接
	if old := h.reg.Get(p.AppID); old != nil {
		_ = old.Send(protocol.MsgDisconnect, &protocol.DisconnectPayload{Reason: protocol.DisconnectReplaced})
		h.reg.FailAllForApp(p.AppID, types.ErrCodeReplaced, "应用已被新实例替换")
		h.reg.Unregister(p.AppID)
		_ = old.Close()
		h.logger.Info("同 appId 新实例注册，替换旧连接", "appId", p.AppID)
	}

	// 6. 注册
	app := &registry.Application{
		AppID:           p.AppID,
		AppName:         p.AppName,
		Version:         p.Version,
		ProtocolVersion: p.ProtocolVersion,
		Tools:           p.Tools,
		Conn:            conn,
		ConnectedAt:     time.Now(),
		LastPing:        time.Now(),
	}
	h.reg.Register(app)

	// 7. 持久化启动注册表（§5.8）：SDK 自报的 launch 信息落盘，供离线自动拉起。
	//    旧 SDK 不上报 launch 时仅刷新名称/版本/工具（保留既有启动命令）。
	if err := h.apps.SaveForRegistration(p); err != nil {
		h.logger.Warn("写入启动注册表失败", "appId", p.AppID, "error", err)
	}

	if err := h.send(conn, protocol.MsgRegisterAck, &protocol.RegisterAckPayload{
		AppID: p.AppID,
		Token: token,
	}); err != nil {
		h.logger.Warn("发送 register_ack 失败", "appId", p.AppID, "error", err)
		return nil
	}

	h.logger.Info("应用注册成功",
		"appId", p.AppID, "appName", p.AppName, "version", p.Version, "tools", len(p.Tools))
	h.notifyAppsChanged()
	return app
}

func (h *Hub) failRegister(conn *websocket.Conn, code protocol.RegisterErrorCode, message string, supported ...[]string) {
	payload := &protocol.RegisterErrorPayload{Code: code, Message: message}
	if len(supported) > 0 {
		payload.SupportedVersions = supported[0]
	}
	_ = h.send(conn, protocol.MsgRegisterError, payload)
	h.logger.Warn("注册失败", "appId", "", "code", code, "message", message)
	_ = conn.Close()
}

// unregister 注销应用：移除注册、失败挂起请求、广播 list_changed。
func (h *Hub) unregister(appID, cause string) {
	if app := h.reg.Unregister(appID); app != nil {
		h.reg.FailAllForApp(appID, types.ErrCodeConnLost, "应用连接丢失: "+cause)
		h.logger.Info("应用注销", "appId", appID, "cause", cause)
		h.notifyAppsChanged()
	}
}

// onResult 处理应用返回的执行结果（含孤儿结果处理，§3.1）。
func (h *Hub) onResult(app *registry.Application, raw json.RawMessage) {
	var p protocol.ResultPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		h.logger.Warn("无法解析 result 消息", "appId", app.AppID, "error", err)
		return
	}
	// 先查后删：孤儿诊断需要 tool 名（挂起请求已超时移除时无从得知）
	pending := h.reg.GetPending(p.RequestID)
	if pending == nil {
		h.recordOrphan(app.AppID, "", &p)
		return
	}
	h.reg.CompletePending(p.RequestID)
	select {
	case pending.ResponseCh <- &p:
	default:
		h.logger.Warn("挂起请求通道已满，丢弃结果", "requestId", p.RequestID)
	}
}

// recordOrphan 记录孤儿结果（执行超时后应用仍在执行，迟到结果已无对应挂起请求）。
func (h *Hub) recordOrphan(appID, tool string, p *protocol.ResultPayload) {
	toolName := tool
	if toolName == "" {
		toolName = "unknown"
	}
	h.reg.AddOrphan(registry.OrphanResult{
		Time:      time.Now(),
		AppID:     appID,
		Tool:      toolName,
		RequestID: p.RequestID,
		Result:    p,
	})
	h.logger.Warn("收到孤儿结果（对应请求已超时或不存在）",
		"appId", appID, "tool", toolName, "requestId", p.RequestID)
}

// onConfirmResult 处理用户确认结果。
func (h *Hub) onConfirmResult(app *registry.Application, raw json.RawMessage) {
	var p protocol.ConfirmResultPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		h.logger.Warn("无法解析 confirm_result 消息", "appId", app.AppID, "error", err)
		return
	}
	pending := h.reg.GetPending(p.RequestID)
	if pending == nil || pending.Phase() != "confirm" {
		h.logger.Warn("孤儿 confirm_result（确认请求已超时或不存在）",
			"appId", app.AppID, "requestId", p.RequestID)
		return
	}
	select {
	case pending.ConfirmCh <- p.Confirmed:
	default:
		h.logger.Warn("确认通道已满，丢弃 confirm_result", "requestId", p.RequestID)
	}
}

// StartHeartbeat 启动心跳发送 goroutine（每 30s 向所有应用发 ping，§3.1）。
// 停止时关闭 stop 通道。死连接由 serveConn 的读超时（2×心跳间隔）检测并清理。
func (h *Hub) StartHeartbeat(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(h.heartbeatInterval())
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for _, app := range h.reg.Apps() {
					_ = app.Send(protocol.MsgPing, &protocol.PingPayload{Timestamp: time.Now().Unix()})
				}
			}
		}
	}()
}

// Shutdown 优雅停止：通知所有应用并关闭连接，随后广播 list_changed。
func (h *Hub) Shutdown() {
	h.mu.Lock()
	h.stopped = true
	h.mu.Unlock()
	for _, app := range h.reg.Apps() {
		_ = app.Send(protocol.MsgDisconnect, &protocol.DisconnectPayload{Reason: protocol.DisconnectShutdown})
		_ = app.Close()
	}
	if len(h.reg.Apps()) > 0 {
		h.notifyAppsChanged()
	}
	h.logger.Info("Bridge 已停止，所有应用连接已关闭")
}

// major 取协议版本的主版本号（"1.2" → "1"）。
func major(version string) string {
	if i := strings.IndexByte(version, '.'); i >= 0 {
		return version[:i]
	}
	return version
}
