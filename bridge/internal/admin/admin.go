// Package admin 提供本地管理端点（仅监听 127.0.0.1，供 CLI 查询状态/轮换 token/管理应用注册表）：
//
//	GET  /admin/health         存活探测（SDK auto-spawn 探测用）
//	GET  /admin/status         状态快照（status / list-apps）
//	POST /admin/rotate-token   轮换 appId 的 token（内存与 auth.json 同步）
//	GET  /admin/apps           已登记应用列表（含在线状态，§5.8）
//	POST /admin/apps           登记应用（apps add）
//	DELETE /admin/apps?appId=  移除登记（apps remove）
//	POST /admin/apps/launch    拉起应用（apps launch，走启动注册表）
//	GET  /admin/apps/search?q= OS 级搜索（apps search）
package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/auth"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/discovery"
	"agentquay/bridge/internal/launcher"
	"agentquay/bridge/internal/registry"
)

// Server 管理端点。
type Server struct {
	pid             int
	port            atomic.Int32 // 当前监听端口（service 模式归位 rebind 后可能变化）
	version         string
	protocolVersion string
	mode            string // "embedded" | "service"
	reg             *registry.Registry
	auth            *auth.Store
	appStore        *apps.Store
	launcher        *launcher.Service
	disco           *discovery.Scanner
	cfg             *config.Config
	logger          *slog.Logger

	// onAppStoreChanged 注册表变更时回调（重建 MCP 工具表）。
	onAppStoreChanged func()
}

// New 创建管理端点服务器。
func New(pid, port int, version, protocolVersion, mode string, reg *registry.Registry, store *auth.Store,
	appStore *apps.Store, lc *launcher.Service, ds *discovery.Scanner, cfg *config.Config,
	logger *slog.Logger) *Server {
	s := &Server{
		pid:             pid,
		version:         version,
		protocolVersion: protocolVersion,
		mode:            mode,
		reg:             reg,
		auth:            store,
		appStore:        appStore,
		launcher:        lc,
		disco:           ds,
		cfg:             cfg,
		logger:          logger,
	}
	s.port.Store(int32(port))
	return s
}

// SetCurrentPort 更新当前监听端口（service 模式归位 rebind 后由 main 调用）。
func (s *Server) SetCurrentPort(port int) { s.port.Store(int32(port)) }

// SetOnAppStoreChanged 注册表变更回调（main 接线到 mcpbridge.RebuildTools）。
func (s *Server) SetOnAppStoreChanged(fn func()) { s.onAppStoreChanged = fn }

func (s *Server) notifyAppStoreChanged() {
	if s.onAppStoreChanged != nil {
		s.onAppStoreChanged()
	}
}

// AppStatus 单应用状态。
type AppStatus struct {
	AppID       string    `json:"appId"`
	AppName     string    `json:"appName"`
	Version     string    `json:"version"`
	ToolCount   int       `json:"toolCount"`
	ConnectedAt time.Time `json:"connectedAt"`
}

// Status 整体状态快照。
type Status struct {
	Mode            string      `json:"mode"` // "embedded" | "service"
	Pid             int         `json:"pid"`
	Port            int         `json:"port"`
	Version         string      `json:"version"`
	ProtocolVersion string      `json:"protocolVersion"`
	Apps            []AppStatus `json:"apps"`
	OrphanResults   int         `json:"orphanResults"`
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/admin/status", s.handleStatus)
	mux.HandleFunc("/admin/rotate-token", s.handleRotateToken)
	mux.HandleFunc("/admin/apps", s.handleApps)
	mux.HandleFunc("/admin/apps/launch", s.handleAppLaunch)
	mux.HandleFunc("/admin/apps/search", s.handleAppSearch)
	mux.HandleFunc("/admin/apps/auto-launch", s.handleAppAutoLaunch)
	return mux
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := Status{
		Mode:            s.mode,
		Pid:             s.pid,
		Port:            int(s.port.Load()),
		Version:         s.version,
		ProtocolVersion: s.protocolVersion,
		Apps:            make([]AppStatus, 0),
		OrphanResults:   len(s.reg.Orphans()),
	}
	for _, app := range s.reg.Apps() {
		st.Apps = append(st.Apps, AppStatus{
			AppID:       app.AppID,
			AppName:     app.AppName,
			Version:     app.Version,
			ToolCount:   len(app.Tools),
			ConnectedAt: app.ConnectedAt,
		})
	}
	writeJSON(w, http.StatusOK, st)
}

type rotateTokenRequest struct {
	AppID string `json:"appId"`
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req rotateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AppID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "appId 不能为空"})
		return
	}
	token, err := s.auth.Rotate(req.AppID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "app 未注册过: " + req.AppID})
		return
	}
	s.logger.Info("已轮换 token", "appId", req.AppID)
	writeJSON(w, http.StatusOK, map[string]string{"appId": req.AppID, "token": token})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// 应用注册表管理（§5.8）

// InstalledAppView 对外暴露的已登记应用视图。
type InstalledAppView struct {
	AppID        string    `json:"appId"`
	AppName      string    `json:"appName"`
	Version      string    `json:"version"`
	Online       bool      `json:"online"`
	ToolCount    int       `json:"toolCount"`
	Source       string    `json:"source"`
	AutoLaunch   string    `json:"autoLaunch"`
	Launchable   bool      `json:"launchable"`
	ExecPath     string    `json:"execPath,omitempty"`
	LastOnlineAt time.Time `json:"lastOnlineAt,omitempty"`
	LastLaunchAt time.Time `json:"lastLaunchAt,omitempty"`
	LaunchCount  int       `json:"launchCount,omitempty"`
}

// handleApps GET 列表 / POST 登记 / DELETE 移除。
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		online := make(map[string]bool)
		for _, app := range s.reg.Apps() {
			online[app.AppID] = true
		}
		out := make([]InstalledAppView, 0)
		for _, app := range s.appStore.All() {
			out = append(out, viewFrom(app, online[app.AppID]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"apps": out})

	case http.MethodPost:
		var req struct {
			AppID      string   `json:"appId"`
			AppName    string   `json:"appName"`
			ExecPath   string   `json:"execPath"`
			Args       []string `json:"args"`
			Cwd        string   `json:"cwd"`
			AutoLaunch string   `json:"autoLaunch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
			return
		}
		if !registry.AppIDPattern.MatchString(req.AppID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "appId 只允许 [a-z0-9-]{1,48}，禁止 _ 和 ."})
			return
		}
		if req.ExecPath == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "execPath 不能为空"})
			return
		}
		if req.AutoLaunch == "" {
			req.AutoLaunch = apps.AutoLaunchOn
		}
		if err := s.appStore.Upsert(&apps.InstalledApp{
			AppID:      req.AppID,
			AppName:    req.AppName,
			Launch:     apps.LaunchCommand{ExecPath: req.ExecPath, Args: req.Args, Cwd: req.Cwd},
			Source:     apps.SourceUser,
			AutoLaunch: req.AutoLaunch,
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.logger.Info("用户登记应用", "appId", req.AppID, "appName", req.AppName, "execPath", req.ExecPath)
		s.notifyAppStoreChanged()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "appId": req.AppID})

	case http.MethodDelete:
		appID := r.URL.Query().Get("appId")
		if err := s.appStore.Remove(appID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "应用未登记: " + appID})
			return
		}
		s.logger.Info("移除应用登记", "appId", appID)
		s.notifyAppStoreChanged()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

type launchRequest struct {
	AppID         string `json:"appId"`
	WaitConnected bool   `json:"waitConnected"`
}

// handleAppLaunch 拉起已登记应用；waitConnected=true 时等待 SDK 注册后返回（默认等待）。
func (s *Server) handleAppLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req launchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AppID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "appId 不能为空"})
		return
	}
	app := s.appStore.Get(req.AppID)
	if app == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "应用未登记: " + req.AppID})
		return
	}
	var res *launcher.Result
	var err error
	if req.WaitConnected {
		res, err = s.launcher.LaunchAndWait(r.Context(), req.AppID)
	} else {
		res, err = s.launcher.Launch(r.Context(), req.AppID)
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleAppSearch OS 级搜索（异步扫描）。
func (s *Server) handleAppSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	cands := s.disco.Search(r.Context(), query, 10)
	out := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		out = append(out, map[string]any{
			"appId":      c.AppID,
			"appName":    c.AppName,
			"execPath":   c.Launch.ExecPath,
			"confidence": c.Confidence,
			"installed":  s.appStore.Get(c.AppID) != nil,
			"online":     s.reg.Get(c.AppID) != nil,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

// handleAppAutoLaunch 设置某应用的 autoLaunch 模式。
func (s *Server) handleAppAutoLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		AppID      string `json:"appId"`
		AutoLaunch string `json:"autoLaunch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AppID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "appId 与 autoLaunch 不能为空"})
		return
	}
	if err := s.appStore.SetAutoLaunch(req.AppID, req.AutoLaunch); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.logger.Info("设置 autoLaunch", "appId", req.AppID, "mode", req.AutoLaunch)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func viewFrom(app *apps.InstalledApp, online bool) InstalledAppView {
	return InstalledAppView{
		AppID:        app.AppID,
		AppName:      app.AppName,
		Version:      app.Version,
		Online:       online,
		ToolCount:    len(app.Tools),
		Source:       app.Source,
		AutoLaunch:   app.AutoLaunch,
		Launchable:   app.Launchable(),
		ExecPath:     app.Launch.ExecPath,
		LastOnlineAt: app.LastOnlineAt,
		LastLaunchAt: app.LastLaunchAt,
		LaunchCount:  app.LaunchCount,
	}
}
