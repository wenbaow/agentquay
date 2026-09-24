// agentquay — AgentQuay Bridge 命令行工具（设计文档 §5.5）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"agentquay/bridge/internal/admin"
	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/auth"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/daemon"
	"agentquay/bridge/internal/discovery"
	"agentquay/bridge/internal/hub"
	"agentquay/bridge/internal/launcher"
	"agentquay/bridge/internal/logging"
	"agentquay/bridge/internal/mcpbridge"
	"agentquay/bridge/internal/peer"
	"agentquay/bridge/internal/registry"
)

// 版本信息。
const (
	Version         = "0.3.0"
	ProtocolVersion = "1.0"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "start":
		err = cmdStart(args[1:])
	case "serve":
		err = cmdServe(args[1:]) // 内部子命令：服务主循环（--daemon 与 SDK 内嵌的子进程入口）
	case "stop":
		err = cmdStop()
	case "status":
		err = cmdStatus()
	case "list-apps":
		err = cmdListApps()
	case "apps":
		err = cmdApps(args[1:])
	case "logs":
		err = cmdLogs(args[1:])
	case "rotate-token":
		err = cmdRotateToken(args[1:])
	case "mcp":
		err = cmdMCP(args[1:])
	case "version":
		err = cmdVersion()
	case "generate-agent-config":
		err = cmdGenerateAgentConfig(args[1:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`AgentQuay Bridge — 让 AI Agent 通过 MCP 发现并调用桌面应用方法

用法:
  agentquay start [--daemon]        启动 Bridge（前台；--daemon 后台运行）
  agentquay stop                    停止 Bridge
  agentquay status                  查看状态（端口、协议版本、已注册应用数）
  agentquay list-apps               查看在线应用
  agentquay apps list               查看已登记应用（含离线与启动能力）
  agentquay apps add --appId ...     登记应用的安装位置（离线应用自动拉起用）
  agentquay apps remove <appId>     移除应用登记
  agentquay apps search <关键字>     在本机搜索可启动的应用（开始菜单/.desktop）
  agentquay apps launch <appId|名称> 启动应用（等待注册则实时报告状态）
  agentquay logs [--tail N]         查看日志（默认最近 100 行）
  agentquay rotate-token <appId>    轮换某应用的 token
  agentquay mcp --stdio             以 stdio 模式临时提供 MCP 服务（调试用）
  agentquay version                 显示版本与协议版本
  agentquay generate-agent-config [--output file]
                                    生成 Agent 配置文件（claude_desktop_config.json）
  agentquay help                    显示本帮助

配置目录: ~/.agentquay/ (config.json / auth.json / apps/ / port / agentquay.log)
`)
}

// ---------------------------------------------------------------------------
// start / serve

func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	daemonMode := fs.Bool("daemon", false, "后台守护进程方式运行")
	_ = fs.Parse(args)

	if *daemonMode {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pid, err := daemon.SpawnDetached(cfg.LogFilePath())
		if err != nil {
			return err
		}
		fmt.Printf("AgentQuay Bridge 已在后台启动 (pid %d)，日志: %s\n", pid, cfg.LogFilePath())
		return nil
	}
	return runServer(false, false)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	embedded := fs.Bool("embedded", false, "SDK 内嵌模式（日志写入应用缓存目录）")
	_ = fs.Parse(args)
	return runServer(*embedded, false)
}

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	stdio := fs.Bool("stdio", false, "以 stdio 模式提供 MCP 服务（调试用）")
	_ = fs.Parse(args)
	if !*stdio {
		return errors.New("当前仅支持 --stdio 模式（调试用）；生产环境请使用默认 Streamable HTTP 端点 /mcp")
	}
	return runServer(false, true)
}

// runServer 启动完整服务：/mcp（Streamable HTTP）+ /ws（WebSocket）+ /admin（管理端点）。
// stdio=true 时额外以 stdin/stdout 提供 MCP 服务。
func runServer(embedded, stdio bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// SDK 内嵌模式：日志写入应用缓存目录（AGENTQUAY_LOG_DIR），不触碰全局日志
	logFile := cfg.LogFilePath()
	if embedded {
		if dir := os.Getenv("AGENTQUAY_LOG_DIR"); dir != "" {
			logFile = filepath.Join(dir, "agentquay.log")
		}
	}
	logger, closer, err := logging.Init(cfg.LogLevel, logFile)
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer closer.Close()

	// 组件接线
	store, err := auth.Load(config.AuthFile())
	if err != nil {
		return fmt.Errorf("加载认证表失败: %w", err)
	}
	appStore, err := apps.Load(config.AppsDir())
	if err != nil {
		return fmt.Errorf("加载应用注册表失败: %w", err)
	}
	reg := registry.New(cfg.OrphanResultBuffer)
	lc := launcher.New(cfg, appStore, reg, logger)
	ds := discovery.New(cfg.Launch.DiscoveryEnabled, logger)
	if ttl := cfg.Launch.DiscoveryCacheTTLSeconds; ttl > 0 {
		ds.SetCacheTTL(time.Duration(ttl) * time.Second)
	}
	h := hub.New(cfg, reg, store, appStore, logger)
	mb := mcpbridge.New(cfg, reg, h, appStore, lc, ds, Version, logger)
	h.SetOnAppsChanged(mb.RebuildTools)

	// 端口解析（autoPort 时 19846–19856 自动漂移）
	host := cfg.ListenHost()
	port, err := config.ResolvePort(host, cfg.Port, cfg.AutoPort)
	if err != nil {
		return err
	}
	if err := config.WritePortFile(port); err != nil {
		logger.Warn("写入端口文件失败", "error", err)
	}
	_ = daemon.WritePid(config.PidFile())

	// 实例模式："embedded"（SDK 拉起）或 "service"（系统服务/命令行启动）。
	// stdio 调试走 runServer(false, true)，不算 service，也不参与生命周期监视。
	mode := "service"
	if embedded {
		mode = "embedded"
	}

	// HTTP 路由
	mux := http.NewServeMux()
	mux.Handle(cfg.MCPPath, mb.Handler())
	mux.Handle(cfg.WSPath, h.Handler())
	adm := admin.New(os.Getpid(), port, Version, ProtocolVersion, mode, reg, store, appStore, lc, ds, cfg, logger)
	adm.SetOnAppStoreChanged(mb.RebuildTools)
	mux.Handle("/admin/", adm.Handler())

	// 启动时重建工具表：seed 已登记离线应用的工具 + 内置 app_* 工具（§5.8）
	mb.RebuildTools()

	// 启动后后台预扫描 OS 级应用并缓存（§5.8.5），不阻塞服务就绪
	if cfg.Launch.DiscoveryScanOnStart && cfg.Launch.DiscoveryEnabled {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if err := ds.Refresh(ctx); err != nil {
				logger.Warn("启动 OS 应用预扫描失败", "error", err)
				return
			}
			logger.Info("OS 应用预扫描完成（已缓存）", "count", ds.Len())
		}()
	}

	// HTTP 服务交由 serverManager 管理，支持 service 模式在权威端口空闲后归位 rebind。
	sm := newServerManager(host, port, mux)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h.StartHeartbeat(ctx.Done())

	if stdio {
		go func() {
			// stdio 调试模式（§5.5）
			logger.Info("以 stdio 模式提供 MCP 服务，Ctrl+C 退出")
			if serr := mb.ServeStdio(); serr != nil {
				logger.Warn("stdio 服务结束", "error", serr)
			}
		}()
	}

	// 生命周期监视：
	//  - embedded：空闲自回收 + 探测到 service 实例时让位迁移（退出时不删端口文件，它属于系统服务）；
	//  - service ：权威端口（默认 19846）空闲后 rebind 归位，保证 Agent 的静态 MCP 端点稳定。
	// stdio 调试模式不参与。
	var preservePortFile atomic.Bool
	switch {
	case embedded:
		go watchEmbeddedLifecycle(ctx, stop, cfg, host, sm, h, &preservePortFile, logger)
	case !stdio:
		go watchReclaimDefaultPort(ctx, cfg, sm, adm, host, logger)
	}

	sm.start()
	logger.Info("AgentQuay Bridge 已启动",
		"version", Version, "protocol", ProtocolVersion,
		"addr", joinHostPort(host, sm.port()), "mode", mode,
		"mcp", cfg.MCPPath, "ws", cfg.WSPath, "embedded", embedded)

	select {
	case <-ctx.Done():
		logger.Info("收到退出信号，正在关闭...", "preservePortFile", preservePortFile.Load())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		h.Shutdown()
		_ = sm.shutdown(shutdownCtx)
		_ = os.Remove(config.PidFile())
		if !preservePortFile.Load() {
			_ = os.Remove(config.PortFile())
		}
		return nil
	case err := <-sm.errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// serverManager：单实例 HTTP 监听管理（支持 service 模式归位 rebind）

// serverManager 管理当前的 HTTP server 引用，提供 port 查询与 rebind（换端口重建监听）。
// rebind 用于系统服务从漂移端口归位到权威端口；初始 server 的 Serve 由 start 启动。
type serverManager struct {
	mu      sync.Mutex
	host    string
	mux     http.Handler
	srv     *http.Server
	errCh   chan error
	running bool
}

func newServerManager(host string, port int, mux http.Handler) *serverManager {
	return &serverManager{
		host:  host,
		mux:   mux,
		srv:   &http.Server{Addr: joinHostPort(host, port), Handler: mux},
		errCh: make(chan error, 1),
	}
}

// start 开始监听（对初始 server，幂等）。
func (m *serverManager) start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}
	m.running = true
	go m.serve(m.srv)
}

// serve 监听循环，把真正的致命错误上报给 errCh；ErrServerClosed 被过滤（关闭由我们主动触发）。
func (m *serverManager) serve(srv *http.Server) {
	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		m.errCh <- err
	}
}

// port 返回当前监听端口。
func (m *serverManager) port() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, p, err := net.SplitHostPort(m.srv.Addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// rebind 关闭当前监听并在新端口重建（归位用）。绑定失败返回错误并保持原监听不变。
func (m *serverManager) rebind(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ln, err := net.Listen("tcp", joinHostPort(m.host, port))
	if err != nil {
		return err
	}
	old := m.srv
	m.srv = &http.Server{Addr: ln.Addr().String(), Handler: m.mux}
	m.running = true
	go func() {
		err := m.srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.errCh <- err
		}
	}()
	// 关闭旧监听；已建立的 WS/MCP 会话被强制断开，客户端按端口文件重连收敛（归位代价 = 一次重连）。
	return old.Close()
}

// shutdown 优雅停止当前监听。
func (m *serverManager) shutdown(ctx context.Context) error {
	m.mu.Lock()
	srv := m.srv
	m.mu.Unlock()
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// watchReclaimDefaultPort（service 模式）：漂移端口空闲出权威端口后 rebind 归位。
func watchReclaimDefaultPort(ctx context.Context, cfg *config.Config, sm *serverManager, adm *admin.Server, host string, logger *slog.Logger) {
	if !cfg.AutoPort || cfg.Port <= 0 {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if sm.port() == cfg.Port {
				continue // 已在权威端口
			}
			if tcpAlive(host, cfg.Port) {
				continue // 权威端口仍被占用（如内嵌桥尚未让位）
			}
			logger.Info("权威端口已空闲，归位到默认端口", "from", sm.port(), "to", cfg.Port)
			if err := sm.rebind(cfg.Port); err != nil {
				logger.Warn("归位失败（可能被抢占），保持当前端口", "error", err, "port", sm.port())
				continue
			}
			adm.SetCurrentPort(cfg.Port)
			if err := config.WritePortFile(cfg.Port); err != nil {
				logger.Warn("写入端口文件失败", "error", err)
			}
			logger.Info("已归位到权威端口", "port", cfg.Port)
		}
	}
}

// watchEmbeddedLifecycle（embedded 模式）：
//  1. 让位：探测到 service 实例 → 通知应用迁移（reason=migrate）→ 退出且保留端口文件；
//  2. 空闲自回收：无应用注册且超过宽限期 → 自行退出（宿主退出不再负责杀子进程）。
func watchEmbeddedLifecycle(ctx context.Context, stop context.CancelFunc, cfg *config.Config, host string,
	sm *serverManager, h *hub.Hub, preservePortFile *atomic.Bool, logger *slog.Logger) {

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	grace := time.Duration(cfg.EmbeddedIdleTimeoutSeconds) * time.Second
	lastAppsAt := time.Now() // 启动即计时：从未有应用注册也自退
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1) 让位：存在 mode=service 的更强实例
			if p, mode, ok := peer.ProbeSuperiorPeer(sm.port(), host); ok && mode == "service" {
				logger.Info("检测到系统服务实例，引导应用迁移后让位", "superiorPort", p)
				h.MigrateToService()
				time.Sleep(3 * time.Second)  // 给客户端收下 migrate 指令并重连的窗口
				preservePortFile.Store(true) // 端口文件属于系统服务，退出时不删除
				logger.Info("让位退出")
				stop()
				return
			}

			// 2) 空闲自回收
			if h.AppCount() > 0 {
				lastAppsAt = time.Now()
				continue
			}
			if grace <= 0 {
				continue // embeddedIdleTimeoutSeconds=0：禁用空闲自回收
			}
			if time.Since(lastAppsAt) >= grace {
				logger.Info("嵌入模式空闲超时（无应用注册），自行退出", "grace", grace)
				stop()
				return
			}
		}
	}
}

// tcpAlive TCP 探测端口是否可连接。
func tcpAlive(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", joinHostPort(host, port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func joinHostPort(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}

// ---------------------------------------------------------------------------
// stop / status / list-apps / logs / rotate-token

func cmdStop() error {
	// 优先从管理端点获取真实 pid（pid 文件可能陈旧：进程被强杀后未清理）
	pid := 0
	port, perr := config.ReadPortFile()
	if perr == nil && healthy(port) {
		if st, err := fetchStatus(port); err == nil {
			pid = st.Pid
		}
	}
	if pid == 0 {
		pid, perr = daemon.ReadPid(config.PidFile())
		if perr != nil {
			return errors.New("Bridge 未在运行（无 pid 文件）")
		}
	}
	if err := daemon.Kill(pid); err != nil {
		if perr == nil && !healthy(port) {
			// 陈旧 pid 文件：进程已不存在
			fmt.Println("AgentQuay Bridge 未在运行")
			_ = os.Remove(config.PidFile())
			return nil
		}
		return fmt.Errorf("终止进程失败: %w", err)
	}
	// 等待健康检查失败（最多 5s）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if perr != nil || !healthy(port) {
			fmt.Println("AgentQuay Bridge 已停止")
			_ = os.Remove(config.PidFile())
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("进程已终止但健康检查仍通过，请检查端口占用")
}

func cmdStatus() error {
	port, err := config.ReadPortFile()
	if err != nil {
		fmt.Println("AgentQuay Bridge 未运行（无端口文件）。可用 `agentquay start` 或 `agentquay start --daemon` 启动。")
		return nil
	}
	st, err := fetchStatus(port)
	if err != nil {
		fmt.Printf("AgentQuay Bridge 未运行（端口 %d 无响应）。可用 `agentquay start --daemon` 启动。\n", port)
		return nil
	}
	fmt.Printf("AgentQuay Bridge\n")
	fmt.Printf("  版本:            %s (协议 %s)\n", st.Version, st.ProtocolVersion)
	fmt.Printf("  模式:            %s\n", st.Mode)
	fmt.Printf("  进程:            pid %d\n", st.Pid)
	fmt.Printf("  端口:            %d (MCP: http://127.0.0.1:%d/mcp, WS: ws://127.0.0.1:%d/ws)\n", st.Port, st.Port, st.Port)
	fmt.Printf("  已注册应用:      %d\n", len(st.Apps))
	fmt.Printf("  孤儿结果数:      %d\n", st.OrphanResults)
	if len(st.Apps) > 0 {
		for _, a := range st.Apps {
			fmt.Printf("    - %-16s %-20s tools=%d connected=%s\n", a.AppID, a.AppName, a.ToolCount, a.ConnectedAt.Format("15:04:05"))
		}
	}
	return nil
}

func cmdListApps() error {
	port, err := config.ReadPortFile()
	if err != nil {
		fmt.Println("AgentQuay Bridge 未运行（无端口文件）。")
		return nil
	}
	st, err := fetchStatus(port)
	if err != nil {
		fmt.Printf("AgentQuay Bridge 未运行（端口 %d 无响应）。\n", port)
		return nil
	}
	if len(st.Apps) == 0 {
		fmt.Println("暂无已注册应用")
		return nil
	}
	fmt.Printf("%-24s %-24s %-10s %-6s %s\n", "APP ID", "APP NAME", "VERSION", "TOOLS", "CONNECTED AT")
	for _, a := range st.Apps {
		fmt.Printf("%-24s %-24s %-10s %-6d %s\n", a.AppID, a.AppName, a.Version, a.ToolCount, a.ConnectedAt.Format("2006-01-02 15:04:05"))
	}
	return nil
}

// ---------------------------------------------------------------------------
// apps 子命令（应用启动注册表，§5.8）

// cmdApps 管理应用启动注册表：list / add / remove / search / launch / set-auto-launch。
func cmdApps(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: agentquay apps <list|add|remove|search|launch|set-auto-launch> ...")
	}
	var err error
	switch args[0] {
	case "list":
		err = appsList()
	case "add":
		err = appsAdd(args[1:])
	case "remove":
		err = appsRemove(args[1:])
	case "search":
		err = appsSearch(args[1:])
	case "launch":
		err = appsLaunch(args[1:])
	case "set-auto-launch":
		err = appsSetAutoLaunch(args[1:])
	default:
		return fmt.Errorf("未知 apps 子命令: %s", args[0])
	}
	return err
}

// appsPort 运行中的 Bridge 端口（未运行返回 0）。
func appsPort() int {
	port, err := config.ReadPortFile()
	if err != nil || !healthy(port) {
		return 0
	}
	return port
}

func appsList() error {
	if port := appsPort(); port > 0 {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/admin/apps", port))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out struct {
			Apps []admin.InstalledAppView `json:"apps"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		if len(out.Apps) == 0 {
			fmt.Println("暂无已登记应用（可运行 `agentquay apps add` 或 `apps search`）")
			return nil
		}
		fmt.Printf("%-24s %-24s %-8s %-6s %-8s %-8s %-12s %s\n",
			"APP ID", "APP NAME", "VERSION", "状态", "SOURCE", "AUTO", "EXEC LIKE", "LAST ONLINE")
		for _, a := range out.Apps {
			status := "离线"
			if a.Online {
				status = "在线"
			}
			ts := "-"
			if !a.LastOnlineAt.IsZero() {
				ts = a.LastOnlineAt.Format("01-02 15:04")
			}
			fmt.Printf("%-24s %-24s %-8s %-6s %-8s %-8s %-12s %s\n",
				a.AppID, a.AppName, a.Version, status, a.Source, a.AutoLaunch, baseName(a.ExecPath), ts)
		}
		return nil
	}
	// 未运行：直接读本地注册表
	store, err := apps.Load(config.AppsDir())
	if err != nil {
		return err
	}
	if len(store.All()) == 0 {
		fmt.Println("暂无已登记应用（Bridge 未运行，可运行 `agentquay apps add` 或 `apps search`）")
		return nil
	}
	fmt.Printf("%-24s %-24s %-8s %-8s %-12s %s\n", "APP ID", "APP NAME", "VERSION", "SOURCE", "EXEC LIKE", "LAST ONLINE")
	for _, a := range store.All() {
		ts := "-"
		if !a.LastOnlineAt.IsZero() {
			ts = a.LastOnlineAt.Format("01-02 15:04")
		}
		fmt.Printf("%-24s %-24s %-8s %-8s %-12s %s\n",
			a.AppID, a.AppName, a.Version, a.Source, baseName(a.Launch.ExecPath), ts)
	}
	return nil
}

func appsAdd(args []string) error {
	fs := flag.NewFlagSet("apps add", flag.ContinueOnError)
	appID := fs.String("appId", "", "appId（[a-z0-9-]{1,48}）")
	name := fs.String("name", "", "应用显示名")
	path := fs.String("path", "", "可执行文件绝对路径")
	argsStr := fs.String("args", "", "启动参数（空格分隔，可选）")
	cwd := fs.String("cwd", "", "工作目录（可选）")
	autoLaunch := fs.String("auto-launch", "on", "自动拉起策略（on/confirm/off）")
	_ = fs.Parse(args)
	if *appID == "" || *path == "" {
		return errors.New("用法: agentquay apps add --appId <id> --name <名称> --path <可执行文件>\n  [--args \"参数\"] [--cwd 目录] [--auto-launch on|confirm|off]")
	}
	if !registry.AppIDPattern.MatchString(*appID) {
		return errors.New("appId 只允许 [a-z0-9-]{1,48}，禁止 _ 和 .")
	}
	body, _ := json.Marshal(map[string]any{
		"appId":      *appID,
		"appName":    *name,
		"execPath":   *path,
		"args":       strings.Fields(*argsStr),
		"cwd":        *cwd,
		"autoLaunch": *autoLaunch,
	})
	if port := appsPort(); port > 0 {
		resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/admin/apps", port),
			"application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("登记失败: %v", out["error"])
		}
		fmt.Printf("已登记应用 %s (%s)\n", *appID, out["appId"])
		return nil
	}
	store, err := apps.Load(config.AppsDir())
	if err != nil {
		return err
	}
	if err := store.Upsert(&apps.InstalledApp{
		AppID:      *appID,
		AppName:    *name,
		Launch:     apps.LaunchCommand{ExecPath: *path, Args: strings.Fields(*argsStr), Cwd: *cwd},
		Source:     apps.SourceUser,
		AutoLaunch: *autoLaunch,
	}); err != nil {
		return err
	}
	fmt.Printf("Bridge 未运行，已直接写入本地注册表。已登记应用 %s (%s)\n", *appID, *name)
	return nil
}

func appsRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("用法: agentquay apps remove <appId>")
	}
	appID := args[0]
	if port := appsPort(); port > 0 {
		req, err := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("http://127.0.0.1:%d/admin/apps?appId=%s", port, appID), nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			return fmt.Errorf("移除失败: %v", out["error"])
		}
		fmt.Printf("已移除应用登记: %s\n", appID)
		return nil
	}
	store, err := apps.Load(config.AppsDir())
	if err != nil {
		return err
	}
	if err := store.Remove(appID); err != nil {
		return err
	}
	fmt.Printf("Bridge 未运行，已直接更新本地注册表。已移除应用登记: %s\n", appID)
	return nil
}

func appsSearch(args []string) error {
	if len(args) != 1 {
		return errors.New("用法: agentquay apps search <关键字>")
	}
	query := args[0]
	if port := appsPort(); port > 0 {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/admin/apps/search?q=%s", port, url.QueryEscape(query)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out struct {
			Apps []map[string]any `json:"apps"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		if len(out.Apps) == 0 {
			fmt.Printf("未找到匹配 %q 的应用\n", query)
			return nil
		}
		fmt.Printf("%-24s %-24s %-8s %-10s %s\n", "APP ID", "APP NAME", "匹配度", "状态", "可执行文件")
		for _, c := range out.Apps {
			status := "未登记"
			if c["installed"].(bool) {
				status = "已登记"
				if c["online"].(bool) {
					status = "在线"
				}
			}
			fmt.Printf("%-24s %-24s %-8d %-10s %s\n",
				c["appId"], c["appName"], int(c["confidence"].(float64)), status, c["execPath"])
		}
		return nil
	}
	// 未运行：本地扫描
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ds := discovery.New(cfg.Launch.DiscoveryEnabled, defaultLogger())
	cands := ds.Search(context.Background(), query, 10)
	if len(cands) == 0 {
		fmt.Printf("未找到匹配 %q 的应用\n", query)
		return nil
	}
	fmt.Printf("%-24s %-24s %-8s %s\n", "APP ID", "APP NAME", "匹配度", "可执行文件")
	for _, c := range cands {
		fmt.Printf("%-24s %-24s %-8d %s\n", c.AppID, c.AppName, c.Confidence, c.Launch.ExecPath)
	}
	return nil
}

func appsLaunch(args []string) error {
	fs := flag.NewFlagSet("apps launch", flag.ContinueOnError)
	noWait := fs.Bool("no-wait", false, "不等待 SDK 注册，立即返回")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("用法: agentquay apps launch <appId|名称> [--no-wait]")
	}
	port := appsPort()
	if port == 0 {
		return errors.New("Bridge 未运行，无法启动应用（请先 `agentquay start --daemon`）")
	}
	target := fs.Arg(0)
	// 解析 appId：名称/标识/模糊匹配（优先已登记，其次系统搜索）
	appID := resolveAppID(target)
	if appID == "" {
		return fmt.Errorf("未找到可启动的应用 %q（可先 `agentquay apps search %s` 确认名称，或 `apps add` 登记）", target, target)
	}
	body, _ := json.Marshal(map[string]any{"appId": appID, "waitConnected": !*noWait})
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/admin/apps/launch", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("启动失败: %v", out["error"])
	}
	status := "已启动"
	if out["connected"].(bool) {
		status = "已启动并完成注册（可控）"
	} else if out["launched"] == true {
		status = "已启动，但未在等待期内完成 SDK 注册（不可控或启动较慢）"
	}
	fmt.Printf("%s: %s\n", out["appId"], status)
	if pid, ok := out["pid"].(float64); ok && pid > 0 {
		fmt.Printf("  进程 pid: %d\n", int(pid))
	}
	if elapsed, ok := out["elapsedMs"].(float64); ok {
		fmt.Printf("  等待注册耗时: %dms\n", int(elapsed))
	}
	return nil
}

func appsSetAutoLaunch(args []string) error {
	if len(args) != 2 {
		return errors.New("用法: agentquay apps set-auto-launch <appId> <on|confirm|off>")
	}
	appID, mode := args[0], args[1]
	if port := appsPort(); port > 0 {
		body, _ := json.Marshal(map[string]string{"appId": appID, "autoLaunch": mode})
		resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/admin/apps/auto-launch", port),
			"application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("设置失败: %v", out["error"])
		}
		fmt.Printf("已设置 %s 的自动拉起策略: %s\n", appID, mode)
		return nil
	}
	store, err := apps.Load(config.AppsDir())
	if err != nil {
		return err
	}
	if err := store.SetAutoLaunch(appID, mode); err != nil {
		return err
	}
	fmt.Printf("Bridge 未运行，已直接更新本地注册表。已设置 %s 的自动拉起策略: %s\n", appID, mode)
	return nil
}

// resolveAppID 把用户输入解析为已登记的 appId（精确 → 名称精确 → 包含），找不到返回空。
func resolveAppID(target string) string {
	store, err := apps.Load(config.AppsDir())
	if err != nil {
		return ""
	}
	if store.Get(target) != nil {
		return target
	}
	if app := store.FindByName(target); app != nil {
		return app.AppID
	}
	return ""
}

func baseName(p string) string {
	if p == "" {
		return "-"
	}
	return filepath.Base(p)
}

func defaultLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	tail := fs.Int("tail", 100, "显示最后 N 行")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	path := cfg.LogFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("日志文件不存在: %s", path)
		}
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if *tail > 0 && len(lines) > *tail {
		lines = lines[len(lines)-*tail:]
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

func cmdRotateToken(args []string) error {
	if len(args) != 1 {
		return errors.New("用法: agentquay rotate-token <appId>")
	}
	appID := args[0]
	port, err := config.ReadPortFile()
	if err == nil && healthy(port) {
		// 运行中：走管理端点（内存与文件同步）
		body, _ := json.Marshal(map[string]string{"appId": appID})
		resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/admin/rotate-token", port),
			"application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out struct {
			AppID string `json:"appId"`
			Token string `json:"token"`
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("轮换失败: %s", out.Error)
		}
		fmt.Printf("已轮换 %s 的 token: %s\n", out.AppID, out.Token)
		return nil
	}
	// 未运行：直接修改 auth.json（当前运行实例需重启后生效）
	store, lerr := auth.Load(config.AuthFile())
	if lerr != nil {
		return lerr
	}
	token, rerr := store.Rotate(appID)
	if rerr != nil {
		return rerr
	}
	fmt.Printf("Bridge 未运行，已直接写入 auth.json。已轮换 %s 的 token: %s\n", appID, token)
	fmt.Println("注意：运行中的实例持有旧 token 的内存副本，重启后生效。")
	return nil
}

// ---------------------------------------------------------------------------
// version / generate-agent-config

func cmdVersion() error {
	fmt.Printf("agentquay %s (protocol %s, %s/%s)\n", Version, ProtocolVersion, runtime.GOOS, runtime.GOARCH)
	return nil
}

func cmdGenerateAgentConfig(args []string) error {
	fs := flag.NewFlagSet("generate-agent-config", flag.ContinueOnError)
	output := fs.String("output", "", "输出文件路径（缺省输出到标准输出）")
	_ = fs.Parse(args)

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"agentquay": map[string]any{
				"command": "agentquay",
				"args":    []string{"mcp", "--stdio"},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if *output == "" {
		fmt.Println(string(data))
		return nil
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("已生成 Agent 配置文件: %s\n", *output)
	return nil
}

// ---------------------------------------------------------------------------
// 管理端点客户端

// healthy 探测 Bridge 是否存活。
func healthy(port int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/admin/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func fetchStatus(port int) (*admin.Status, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/admin/status", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st admin.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}
