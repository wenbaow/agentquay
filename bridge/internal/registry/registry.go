// Package registry 维护应用注册表、挂起请求与孤儿结果环形缓冲（设计文档 §5.2、§3.1）。
package registry

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"agentquay/bridge/internal/protocol"
	"agentquay/bridge/internal/types"
)

// 命名规范（§3.1）：
//   - appId 只允许 [a-z0-9-]{1,48}，禁止 _ 和 .（_ 保留给 tool 名解析分隔）
//   - tool 名只允许 [a-zA-Z0-9_-]{1,78}（保证合成名 {appId}_{toolName} ≤ 128）
var (
	AppIDPattern    = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)
	ToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,78}$`)
)

// JoinToolName 合成 MCP 工具名：{appId}_{toolName}。
func JoinToolName(appID, toolName string) string {
	return appID + "_" + toolName
}

// SplitToolName 按第一个 "_" 分割合成名（appId 禁止 _ 使解析无歧义）。
func SplitToolName(name string) (appID, toolName string, ok bool) {
	idx := strings.IndexByte(name, '_')
	if idx <= 0 || idx == len(name)-1 {
		return "", "", false
	}
	return name[:idx], name[idx+1:], true
}

// PendingRequest 一次 tools/call 的挂起状态，区分确认阶段与执行阶段。
type PendingRequest struct {
	RequestID string
	AppID     string
	ToolName  string
	CreatedAt time.Time

	// Phase 取值 "confirm" | "execute"（§5.2），经 SetPhase/Phase 访问。
	ResponseCh chan *types.ResultMessage // 执行结果
	ConfirmCh  chan bool                 // 用户确认结果

	mu      sync.Mutex
	phase   string
	timeout time.Duration
}

func NewPendingRequest(requestID, appID, toolName string) *PendingRequest {
	return &PendingRequest{
		RequestID:  requestID,
		AppID:      appID,
		ToolName:   toolName,
		CreatedAt:  time.Now(),
		phase:      "execute",
		ResponseCh: make(chan *types.ResultMessage, 1),
		ConfirmCh:  make(chan bool, 1),
	}
}

// SetPhase 切换阶段（confirm → execute）。
func (p *PendingRequest) SetPhase(phase string) {
	p.mu.Lock()
	p.phase = phase
	p.mu.Unlock()
}

// Phase 返回当前阶段。
func (p *PendingRequest) Phase() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase
}

// SetTimeout 更新阶段超时（确认阶段 120s → 执行阶段 30s）。
func (p *PendingRequest) SetTimeout(d time.Duration) {
	p.mu.Lock()
	p.timeout = d
	p.mu.Unlock()
}

// Timeout 返回当前超时。
func (p *PendingRequest) Timeout() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.timeout
}

// Application 一个已注册的桌面应用（§5.2）。
type Application struct {
	AppID           string
	AppName         string
	Version         string
	ProtocolVersion string
	Tools           []types.ToolMetadata
	Conn            *websocket.Conn
	ConnectedAt     time.Time
	LastPing        time.Time

	writeMu sync.Mutex
}

// Send 向应用发送一条 WS 消息（内部持有单写者锁，多 goroutine 安全）。
func (a *Application) Send(msgType string, payload any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return protocol.Send(a.Conn, msgType, payload)
}

// Close 关闭连接。
func (a *Application) Close() error { return a.Conn.Close() }

// Tool 按名查找应用的 Tool 元数据。
func (a *Application) Tool(toolName string) *types.ToolMetadata {
	for i := range a.Tools {
		if a.Tools[i].Name == toolName {
			return &a.Tools[i]
		}
	}
	return nil
}

// OrphanResult 迟到的执行结果（执行超时后应用仍返回）。
type OrphanResult struct {
	Time      time.Time            `json:"time"`
	AppID     string               `json:"appId"`
	Tool      string               `json:"tool"`
	RequestID string               `json:"requestId"`
	Result    *types.ResultMessage `json:"result"`
}

// OrphanBuffer 孤儿结果环形缓冲（默认 100 条）。
type OrphanBuffer struct {
	mu   sync.Mutex
	max  int
	buf  []OrphanResult
	next int
	full bool
}

func NewOrphanBuffer(max int) *OrphanBuffer {
	return &OrphanBuffer{max: max, buf: make([]OrphanResult, 0, max)}
}

func (b *OrphanBuffer) Add(o OrphanResult) {
	if b.max <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) < b.max {
		b.buf = append(b.buf, o)
		return
	}
	b.buf[b.next] = o
	b.next = (b.next + 1) % b.max
	b.full = true
}

func (b *OrphanBuffer) List() []OrphanResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]OrphanResult, 0, len(b.buf))
	if b.full {
		out = append(out, b.buf[b.next:]...)
		out = append(out, b.buf[:b.next]...)
	} else {
		out = append(out, b.buf...)
	}
	return out
}

// Registry 应用注册表 + 挂起请求表 + 孤儿缓冲。
type Registry struct {
	mu      sync.RWMutex
	apps    map[string]*Application
	pending map[string]*PendingRequest
	orphans *OrphanBuffer
}

func New(orphanBufferSize int) *Registry {
	return &Registry{
		apps:    make(map[string]*Application),
		pending: make(map[string]*PendingRequest),
		orphans: NewOrphanBuffer(orphanBufferSize),
	}
}

// Register 注册应用，返回被替换的旧连接（同 appId 已在线时）。
func (r *Registry) Register(app *Application) (replaced *Application) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.apps[app.AppID]
	r.apps[app.AppID] = app
	return old
}

// Unregister 注销应用，返回被移除的应用（不存在时返回 nil）。
func (r *Registry) Unregister(appID string) *Application {
	r.mu.Lock()
	defer r.mu.Unlock()
	app := r.apps[appID]
	delete(r.apps, appID)
	return app
}

// Get 按 appId 取应用。
func (r *Registry) Get(appID string) *Application {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.apps[appID]
}

// Apps 返回全部在线应用的快照。
func (r *Registry) Apps() []*Application {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Application, 0, len(r.apps))
	for _, app := range r.apps {
		out = append(out, app)
	}
	return out
}

// AppCount 返回在线应用数（供 embedded 生命周期监视器查询，跨并发安全）。
func (r *Registry) AppCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.apps)
}

// Tool 查询某应用的 Tool 元数据。
func (r *Registry) Tool(appID, toolName string) *types.ToolMetadata {
	app := r.Get(appID)
	if app == nil {
		return nil
	}
	return app.Tool(toolName)
}

// AddPending 登记一个挂起请求。
func (r *Registry) AddPending(p *PendingRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[p.RequestID] = p
}

// CompletePending 移除并返回挂起请求（幂等：不存在返回 nil）。
func (r *Registry) CompletePending(requestID string) *PendingRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pending[requestID]
	delete(r.pending, requestID)
	return p
}

// GetPending 按 requestId 查挂起请求（不移除）。
func (r *Registry) GetPending(requestID string) *PendingRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pending[requestID]
}

// FailAllForApp 使某应用的全部挂起请求失败（应用断连 / 被替换）。
func (r *Registry) FailAllForApp(appID string, code, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.pending {
		if p.AppID != appID {
			continue
		}
		delete(r.pending, id)
		select {
		case p.ResponseCh <- &types.ResultMessage{
			RequestID: p.RequestID,
			Success:   false,
			Error:     &types.ErrorInfo{Code: code, Message: message},
		}:
		default:
		}
	}
}

// AddOrphan 记录一条孤儿结果。
func (r *Registry) AddOrphan(o OrphanResult) {
	r.orphans.Add(o)
}

// Orphans 返回孤儿结果快照。
func (r *Registry) Orphans() []OrphanResult {
	return r.orphans.List()
}
