// Package launcher 实现应用拉起服务（设计文档 §5.8）：
// 从启动注册表解析启动命令 → 分离式进程拉起 → 等待 SDK 注册，
// 使 Agent 对离线应用的工具调用能"先启动、后执行"。
package launcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/daemon"
	"agentquay/bridge/internal/registry"
)

// 错误类型（供上层映射 MCP 错误码与提示信息）。
var (
	ErrDisabled      = errors.New("自动拉起已禁用（config.launch.enabled = false）")
	ErrNotInstalled  = errors.New("应用未登记，无法自动拉起")
	ErrAutoLaunchOff = errors.New("该应用已关闭自动拉起（autoLaunch = off）")
	ErrNotLaunchable = errors.New("应用没有可用的启动命令（请用 apps add 登记，或先手动启动一次让 SDK 上报）")
	ErrLaunchFailed  = errors.New("启动应用进程失败")
)

// Result 一次启动尝试的结果。
type Result struct {
	AppID     string        `json:"appId"`
	PID       int           `json:"pid"`
	Launched  bool          `json:"launched"`  // 本次是否实际执行了进程拉起
	Connected bool          `json:"connected"` // 等待期内应用是否完成 SDK 注册（在线）
	StartedAt time.Time     `json:"-"`
	Elapsed   time.Duration `json:"elapsed"` // 从拉起（或加入等待）到注册/超时的耗时
}

// Service 启动服务：注册表 + 在线注册中心 + 并发去重。
type Service struct {
	cfg    *config.Config
	store  *apps.Store
	reg    *registry.Registry
	logger *slog.Logger

	mu       sync.Mutex
	inflight map[string]time.Time // 正在启动中的应用 → 拉起时间（去重，防并发双开）
}

// New 创建启动服务。
func New(cfg *config.Config, store *apps.Store, reg *registry.Registry, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, store: store, reg: reg, logger: logger, inflight: make(map[string]time.Time)}
}

// Enabled 全局开关。
func (s *Service) Enabled() bool { return s.cfg.Launch.Enabled }

// CanAutoLaunch 判断离线应用是否可直接自动拉起（已登记 + 启动命令可用 + 未关闭自动拉起）。
func (s *Service) CanAutoLaunch(appID string) bool {
	if !s.Enabled() {
		return false
	}
	app := s.store.Get(appID)
	if app == nil {
		return false
	}
	if app.AutoLaunch == apps.AutoLaunchOff {
		return false
	}
	return app.Launchable()
}

// timeout 返回某应用的启动等待超时。
func (s *Service) timeout(app *apps.InstalledApp) time.Duration {
	return app.LaunchTimeout(s.cfg.Launch.DefaultTimeoutSeconds)
}

// LaunchAndWait 启动应用并等待 SDK 注册（自动拉起主入口）。
// 已在线 → 直接返回 connected；已在启动中 → 只等待不重复拉起；启动失败 → 返回错误。
func (s *Service) LaunchAndWait(ctx context.Context, appID string) (*Result, error) {
	res, err := s.Launch(ctx, appID)
	if err != nil {
		return nil, err
	}
	if res.Connected {
		return res, nil
	}
	// 等待注册
	app := s.store.Get(appID)
	timeout := s.timeout(app)
	connected := s.waitRegistered(ctx, appID, timeout)
	res.Connected = connected
	res.Elapsed = time.Since(res.StartedAt)
	if connected {
		s.clearInflight(appID) // 上线即释放，允许下次正常拉起
	}
	return res, nil
}

// Launch 仅拉起进程，不等待注册（用于无 SDK 的纯发现应用：启动即算完成）。
// 并发去重：同一应用已在拉起中（inflight 未过期且未上线）时不再重复拉起。
func (s *Service) Launch(ctx context.Context, appID string) (*Result, error) {
	app := s.store.Get(appID)
	if app == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotInstalled, appID)
	}

	res := &Result{AppID: appID, StartedAt: time.Now()}

	// 已在线：无需启动，释放可能残留的 inflight
	if s.reg.Get(appID) != nil {
		s.clearInflight(appID)
		res.Connected = true
		return res, nil
	}

	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if app.AutoLaunch == apps.AutoLaunchOff {
		return nil, fmt.Errorf("%w: %s", ErrAutoLaunchOff, appID)
	}

	// 并发去重 + 并发上限（同一时刻处于拉起中的应用有限）
	s.mu.Lock()
	s.pruneLocked()
	limit := s.maxConcurrentReached()
	if limit {
		s.mu.Unlock()
		return nil, fmt.Errorf("同时启动中的应用数已达上限（%d）", s.maxConcurrent())
	}
	if _, ok := s.inflight[appID]; ok {
		s.mu.Unlock()
		return res, nil // 已有调用在拉起中，本调用只等待
	}
	s.inflight[appID] = time.Now()
	s.mu.Unlock()

	launched, pid, err := s.spawn(app)
	if err != nil {
		s.clearInflight(appID) // 拉起失败：允许立即重试
		s.logger.Warn("拉起应用失败", "appId", appID, "error", err)
		return nil, err
	}
	res.Launched = launched
	res.PID = pid
	s.store.LogLaunch(appID)
	s.logger.Info("已拉起应用", "appId", appID, "pid", pid, "execPath", app.ResolveLaunch().ExecPath)
	return res, nil
}

// WaitConnected 仅等待某应用注册（不启动），用于已经上线的场景与测试。
func (s *Service) WaitConnected(ctx context.Context, appID string, timeout time.Duration) bool {
	return s.waitRegistered(ctx, appID, timeout)
}

// waitRegistered 轮询注册中心直到应用上线、超时或 ctx 取消。
func (s *Service) waitRegistered(ctx context.Context, appID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.reg.Get(appID) != nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if time.Now().After(deadline) {
				return false
			}
		}
	}
}

// spawn 分离式拉起应用进程（platform 相关：macOS .app 用 open；PATH 命令按 LookPath 解析）。
func (s *Service) spawn(app *apps.InstalledApp) (bool, int, error) {
	lc := app.ResolveLaunch()
	exe := lc.ExecPath
	if exe == "" {
		return false, 0, fmt.Errorf("%w: %s", ErrNotLaunchable, app.AppID)
	}
	if runtime.GOOS == "darwin" && strings.HasSuffix(exe, ".app") {
		// .app 是目录不是可执行文件，交给 open 启动
		pid, err := daemon.StartDetached("/usr/bin/open", []string{"-n", exe}, "")
		if err != nil {
			return false, 0, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
		}
		return true, pid, nil
	}
	// 无路径分隔符的命令（Linux .desktop 常见，如 "firefox"）：按 PATH 解析
	if !filepath.IsAbs(exe) && !strings.ContainsAny(exe, `/\\`) {
		if resolved, err := exec.LookPath(exe); err == nil {
			exe = resolved
		}
	}
	pid, err := daemon.StartDetached(exe, lc.Args, lc.Cwd)
	if err != nil {
		return false, 0, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}
	return true, pid, nil
}

// maxConcurrentReached 判断是否已达并发上限（先清理过期记录再计数）。
func (s *Service) maxConcurrentReached() bool {
	s.pruneLocked()
	return len(s.inflight) >= s.maxConcurrent()
}

func (s *Service) maxConcurrent() int {
	if s.cfg.Launch.MaxConcurrentLaunches <= 0 {
		return 3
	}
	return s.cfg.Launch.MaxConcurrentLaunches
}

// pruneLocked 清理已过期的 inflight 记录（应用未注册但早已超过等待窗口 + 10s）。
// 调用方需持有 s.mu。
func (s *Service) pruneLocked() {
	for appID, t := range s.inflight {
		if app := s.store.Get(appID); app != nil {
			if time.Since(t) > s.timeout(app)+10*time.Second {
				delete(s.inflight, appID)
				continue
			}
		}
		if s.reg.Get(appID) != nil {
			delete(s.inflight, appID)
		}
	}
}

// clearInflight 应用上线或拉起失败时移除 inflight 记录。
func (s *Service) clearInflight(appID string) {
	s.mu.Lock()
	delete(s.inflight, appID)
	s.mu.Unlock()
}
