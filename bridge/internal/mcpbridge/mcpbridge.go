// Package mcpbridge 将 mcp-go 的 MCP Server 与注册中心/消息总线接线（设计文档 §3.2、§5.3）：
// 动态工具表（tools/list）、工具调用路由（tools/call）、list_changed 广播、确认流程与进度通知。
//
// 应用启动注册表（§5.8）：
//   - tools/list 除在线应用的工具外，还包含"已登记但离线"应用的工具（描述带 [未运行] 标注），
//     使 Agent 知道该应用存在；调用时 Bridge 自动拉起再转发，实现"打开微信 → 微信启动并执行"。
//   - 内置 app_* 工具（app 前缀，appId 正则禁 _ 故永不与应用冲突）：注册表查询 / 按名启动 / 搜索。
package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/discovery"
	"agentquay/bridge/internal/hub"
	"agentquay/bridge/internal/launcher"
	"agentquay/bridge/internal/protocol"
	"agentquay/bridge/internal/registry"
	"agentquay/bridge/internal/types"
)

// MCP 错误码（设计文档附录 B）。
const (
	MCPErrorGeneric       = -32000 // 通用错误
	MCPErrorAppOffline    = -32001 // 应用未连接（可能已尝试自动拉起但失败）
	MCPErrorToolNotFound  = -32002 // Tool 不存在
	MCPErrorInvalidArgs   = -32003 // 参数无效（Schema 校验失败，不转发给应用）
	MCPErrorTimeout       = -32004 // 执行超时（应用可能仍在执行）
	MCPErrorUserCancelled = -32005 // 用户取消（确认被拒或确认超时）
	MCPErrorAuthFailed    = -32006 // 认证失败
	MCPErrorReplaced      = -32007 // 应用被替换（新实例注册）
)

// 请求阶段。
const (
	PhaseConfirm = "confirm"
	PhaseExecute = "execute"
)

// 内置 Bridge 工具（appId 前缀，appId 正则 [a-z0-9-]{1,48} 禁止 _，故 "app" + "_" 永不与应用冲突）。
const (
	bridgeAppID       = "app"
	offlineAnnotation = "[未运行] "
	toolAppList       = "app_list"
	toolAppLaunch     = "app_launch"
	toolAppSearch     = "app_search"
	toolAppAdopt      = "app_adopt"
)

// appLauncher 启动服务的接口视图（便于测试替换）。
type appLauncher interface {
	Enabled() bool
	CanAutoLaunch(appID string) bool
	Launch(ctx context.Context, appID string) (*launcher.Result, error)        // 仅拉起
	LaunchAndWait(ctx context.Context, appID string) (*launcher.Result, error) // 拉起并等待注册
}

// appDiscoverer OS 发现的接口视图（便于测试替换）。
type appDiscoverer interface {
	Enabled() bool
	Search(ctx context.Context, query string, limit int) []discovery.Candidate
}

// Bridge 组装 MCP Server 与内部组件。
type Bridge struct {
	cfg      *config.Config
	reg      *registry.Registry
	hub      *hub.Hub
	store    *apps.Store
	launcher appLauncher
	disco    appDiscoverer
	logger   *slog.Logger
	version  string
	mcpsrv   *server.MCPServer
	handler  http.Handler
}

// New 创建 Bridge。
func New(cfg *config.Config, reg *registry.Registry, h *hub.Hub, store *apps.Store,
	lc appLauncher, ds appDiscoverer, version string, logger *slog.Logger) *Bridge {
	mcpsrv := server.NewMCPServer("agentquay", version,
		server.WithToolCapabilities(true), // 声明支持 tools/list_changed（§3.2）
	)
	b := &Bridge{
		cfg:      cfg,
		reg:      reg,
		hub:      h,
		store:    store,
		launcher: lc,
		disco:    ds,
		logger:   logger,
		version:  version,
		mcpsrv:   mcpsrv,
	}
	b.handler = server.NewStreamableHTTPServer(mcpsrv)
	return b
}

// Handler 返回 Streamable HTTP 的 MCP 处理器（挂载于 /mcp）。
func (b *Bridge) Handler() http.Handler { return b.handler }

// ServeStdio 以 stdio 模式提供 MCP 服务（调试用，agentquay mcp --stdio）。
func (b *Bridge) ServeStdio() error { return server.ServeStdio(b.mcpsrv) }

// RebuildTools 从注册表与启动注册表重建 MCP 工具表并向所有已连接 Agent 广播 list_changed（§3.2、§5.8）。
// 触发时机：应用注册/注销（hub 回调）、启动时（seed 离线工具）、注册表增删改（app_* 工具）。
func (b *Bridge) RebuildTools() {
	tools := make([]server.ServerTool, 0)
	online := make(map[string]bool)

	// 1. 在线应用：工具直接暴露
	for _, app := range b.reg.Apps() {
		online[app.AppID] = true
		for _, t := range app.Tools {
			if b.isBridgeTool(app.AppID, t.Name) {
				b.logger.Warn("应用工具与内置工具重名，忽略", "appId", app.AppID, "tool", t.Name)
				continue
			}
			tools = append(tools, server.ServerTool{
				Tool: mcp.Tool{
					Name:           registry.JoinToolName(app.AppID, t.Name),
					Description:    describeTool(app.AppName, t),
					RawInputSchema: mustJSON(t.InputSchema),
				},
				Handler: b.dispatch,
			})
		}
	}

	// 2. 已登记但离线的应用：工具进表（标注 [未运行]），调用时自动拉起
	for _, app := range b.store.All() {
		if online[app.AppID] {
			continue
		}
		for _, t := range app.Tools {
			if b.isBridgeTool(app.AppID, t.Name) {
				continue
			}
			tools = append(tools, server.ServerTool{
				Tool: mcp.Tool{
					Name:           registry.JoinToolName(app.AppID, t.Name),
					Description:    fmt.Sprintf("%s%s", offlineAnnotation, describeTool(app.AppName, t)),
					RawInputSchema: mustJSON(t.InputSchema),
				},
				Handler: b.dispatch,
			})
		}
	}

	// 3. 内置 Bridge 工具（始终可用）
	tools = append(tools,
		server.ServerTool{Tool: b.builtinTool(toolAppList,
			"列出本机已登记应用及其运行状态（内置工具，无需应用在线）。Agent 可用它了解哪些应用可被自动拉起。",
			map[string]any{}), Handler: b.handleAppList},
		server.ServerTool{Tool: b.builtinTool(toolAppLaunch,
			"启动（拉起）一个应用。支持已登记 appId 或应用名称（未登记时自动在系统中搜索并登记）。",
			map[string]any{
				"appId":         map[string]any{"type": "string", "description": "已登记应用的 appId（与 name 二选一）"},
				"name":          map[string]any{"type": "string", "description": "应用名称（模糊匹配已登记应用或系统中的应用）"},
				"waitConnected": map[string]any{"type": "boolean", "description": "等待 SDK 注册后再返回（默认 true，仅对已登记应用有效）"},
			}), Handler: b.handleAppLaunch},
		server.ServerTool{Tool: b.builtinTool(toolAppSearch,
			"在本机搜索未登记的应用（开始菜单 / 应用目录 / .desktop）。用于发现可被 app_launch 启动的候选。",
			map[string]any{
				"query": map[string]any{"type": "string", "description": "搜索关键词（应用名或可执行文件名）"},
			}), Handler: b.handleAppSearch},
		server.ServerTool{Tool: b.builtinTool(toolAppAdopt,
			"登记一个应用（写入启动注册表）。常用于把 app_search 找到的应用显式登记为可自动拉起。",
			map[string]any{
				"appId":    map[string]any{"type": "string", "description": "appId（[a-z0-9-]{1,48}）"},
				"appName":  map[string]any{"type": "string", "description": "应用显示名"},
				"execPath": map[string]any{"type": "string", "description": "可执行文件绝对路径"},
				"args":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "启动参数（可选）"},
			}), Handler: b.handleAppAdopt},
	)

	b.mcpsrv.SetTools(tools...)
	b.mcpsrv.SendNotificationToAllClients("notifications/tools/list_changed", nil)
	b.logger.Debug("MCP 工具表已重建", "count", len(tools))
}

// isBridgeTool 判断是否为内置工具名（避免与 appId="app" 的真实应用冲突）。
func (b *Bridge) isBridgeTool(appID, toolName string) bool {
	if appID != bridgeAppID {
		return false
	}
	switch toolName {
	case "list", "launch", "search", "adopt":
		return true
	}
	return false
}

func (b *Bridge) builtinTool(name, description string, props map[string]any) mcp.Tool {
	return mcp.Tool{
		Name:           name,
		Description:    description,
		RawInputSchema: mustJSON(map[string]any{"type": "object", "properties": props}),
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return data
}

// describeTool 组装 tools/list 中的工具描述。
// 无 pageKey：  [AppName] 描述
// 有 pageKey：  [AppName|PageKey] 描述 —— Agent 由此可知工具归属页面，工具名与调用方式不变。
func describeTool(appName string, t types.ToolMetadata) string {
	if t.PageKey == "" {
		return fmt.Sprintf("[%s] %s", appName, t.Description)
	}
	return fmt.Sprintf("[%s|%s] %s", appName, t.PageKey, t.Description)
}

// dispatch 是所有应用工具的共用处理器：解析合成名 → 校验 → 自动拉起（离线时）→ 确认流程 → 转发 → 回传。
func (b *Bridge) dispatch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// 1. 解析 {appId}_{toolName}
	appID, toolName, ok := registry.SplitToolName(req.Params.Name)
	if !ok {
		return mcpError(MCPErrorToolNotFound,
			fmt.Sprintf("工具名 %q 格式非法（应为 {appId}_{toolName}）", req.Params.Name))
	}

	// 2. 校验 app 在线（-32001）；离线时尝试按启动注册表自动拉起（§5.8）
	app := b.reg.Get(appID)
	if app == nil {
		if reason := b.tryAutoLaunch(ctx, appID); reason != "" {
			return mcpError(MCPErrorAppOffline, "应用未连接: "+appID+"（"+reason+"）")
		}
		app = b.reg.Get(appID)
		if app == nil {
			return mcpError(MCPErrorAppOffline,
				"应用未连接: "+appID+"（已拉起但未能在等待期内完成 SDK 注册，请稍后重试或查看应用是否正常启动）")
		}
	}

	// 3. 校验 tool 存在（-32002；离线期间应用可能已更新工具表）
	tool := b.reg.Tool(appID, toolName)
	if tool == nil {
		return mcpError(MCPErrorToolNotFound, "Tool 不存在: "+req.Params.Name+"（应用离线期间可能更新了工具列表，请重试 tools/list）")
	}

	args := req.GetArguments()

	// 4. JSON Schema 校验 arguments（-32003，失败不转发给应用）
	if err := b.validateArgs(tool.InputSchema, args); err != nil {
		return mcpError(MCPErrorInvalidArgs, "参数不符合 inputSchema: "+err.Error())
	}

	requestID := newRequestID()
	pending := registry.NewPendingRequest(requestID, appID, toolName)
	b.reg.AddPending(pending)
	defer b.reg.CompletePending(requestID)

	execTimeout := b.timeoutOf(tool.TimeoutSeconds, b.cfg.DefaultTimeoutSeconds)
	confirmTimeout := b.timeoutOf(tool.ConfirmTimeoutSeconds, b.cfg.ConfirmTimeoutSeconds)

	// 5. RequiresConfirmation → 确认流程（§3.3、§6.3）
	if tool.RequiresConfirmation {
		b.logger.Debug("进入确认流程", "tool", registry.JoinToolName(appID, toolName), "requestId", requestID)
		pending.SetPhase(PhaseConfirm)
		// 页面工具（pageKey 非空）注明确认后将在应用中打开/创建对应页面，用户可拒绝而不触发任何页面动作
		confirmMessage := "确认执行 " + toolName + "？"
		if tool.PageKey != "" {
			confirmMessage = fmt.Sprintf("确认执行 %s？（将在应用中打开页面 %s）", toolName, tool.PageKey)
		}
		if err := b.hub.SendConfirm(app, &protocol.ConfirmPayload{
			RequestID:      requestID,
			Message:        confirmMessage,
			Arguments:      args,
			TimeoutSeconds: int(confirmTimeout.Seconds()),
		}); err != nil {
			return mcpError(MCPErrorAppOffline, "发送确认请求失败: "+err.Error())
		}

		// 等待期间每 30s 向 Agent 发 progress 通知（§3.3）
		progressCtx, cancelProgress := context.WithCancel(ctx)
		go b.sendProgress(progressCtx, req)

		select {
		case confirmed := <-pending.ConfirmCh:
			if !confirmed {
				cancelProgress()
				return mcpError(MCPErrorUserCancelled, "用户取消了操作")
			}
		case res := <-pending.ResponseCh:
			// 应用在确认期间断连/被替换：FailAllForApp 已把失败结果写入 ResponseCh。
			// 立即返回（-32001/-32007），而不是等满确认超时（否则 Agent 会白等 120s）。
			cancelProgress()
			return b.toToolResult(res)
		case <-time.After(confirmTimeout):
			cancelProgress()
			return mcpError(MCPErrorUserCancelled, "确认超时（用户未响应），已取消")
		case <-ctx.Done():
			cancelProgress()
			return mcpError(MCPErrorGeneric, "请求已取消")
		}
		cancelProgress()
	}

	// 6. 执行阶段：转发 invoke，独立计时（§3.3）
	pending.SetPhase(PhaseExecute)
	pending.SetTimeout(execTimeout)
	b.logger.Debug("转发调用", "tool", registry.JoinToolName(appID, toolName),
		"requestId", requestID, "execTimeout", execTimeout.String())
	if err := b.hub.SendInvoke(app, &protocol.InvokePayload{
		RequestID:      requestID,
		Tool:           toolName,
		Arguments:      args,
		TimeoutSeconds: int(execTimeout.Seconds()),
	}); err != nil {
		return mcpError(MCPErrorAppOffline, "发送调用请求失败: "+err.Error())
	}

	select {
	case res := <-pending.ResponseCh:
		b.logger.Debug("调用结果返回", "requestId", requestID, "success", res.Success)
		return b.toToolResult(res)
	case <-time.After(execTimeout):
		// 应用侧继续执行，迟到的 result 进入孤儿处理
		b.logger.Warn("执行超时", "tool", registry.JoinToolName(appID, toolName),
			"requestId", requestID, "timeout", execTimeout.String())
		return mcpError(MCPErrorTimeout, fmt.Sprintf("执行超时（%s 可能仍在执行）", toolName))
	case <-ctx.Done():
		return mcpError(MCPErrorGeneric, "请求已取消")
	}
}

// tryAutoLaunch 离线应用按注册表自动拉起；返回非空字符串表示拉起失败及原因。
func (b *Bridge) tryAutoLaunch(ctx context.Context, appID string) string {
	if !b.launcher.Enabled() {
		return "自动拉起已禁用（config.launch.enabled = false）"
	}
	app := b.store.Get(appID)
	if app == nil {
		return "应用未登记，无法自动拉起（可用 app_search/app_launch 按名称启动，或用 agentquay apps add 登记）"
	}
	if app.AutoLaunch == apps.AutoLaunchOff {
		return "已登记但关闭了自动拉起（autoLaunch = off，可用 agentquay apps set-auto-launch 开启）"
	}
	if !app.Launchable() {
		return "缺少有效的启动命令（安装位置已失效，请用 agentquay apps add 重新登记）"
	}
	if _, err := b.launcher.LaunchAndWait(ctx, appID); err != nil {
		return "已尝试自动启动但失败: " + err.Error()
	}
	b.RebuildTools()
	return ""
}

// ---------------------------------------------------------------------------
// 内置工具

// handleAppList 列出已登记应用与运行状态。
func (b *Bridge) handleAppList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	online := make(map[string]bool)
	for _, app := range b.reg.Apps() {
		online[app.AppID] = true
	}
	out := make([]map[string]any, 0)
	for _, app := range b.store.All() {
		out = append(out, map[string]any{
			"appId":        app.AppID,
			"appName":      app.AppName,
			"version":      app.Version,
			"online":       online[app.AppID],
			"toolCount":    len(app.Tools),
			"source":       app.Source, // sdk | user | discovery
			"autoLaunch":   app.AutoLaunch,
			"launchable":   app.Launchable(),
			"lastLaunchAt": app.LastLaunchAt,
		})
	}
	return textResult(out)
}

// handleAppSearch 在本机搜索未登记应用。
func (b *Bridge) handleAppSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := argString(req, "query")
	out := make([]map[string]any, 0)
	for _, c := range b.disco.Search(ctx, query, 10) {
		installed := b.store.Get(c.AppID) != nil
		out = append(out, map[string]any{
			"appId":      c.AppID,
			"appName":    c.AppName,
			"execPath":   c.Launch.ExecPath,
			"confidence": c.Confidence,
			"installed":  installed,
			"online":     b.reg.Get(c.AppID) != nil,
		})
	}
	return textResult(out)
}

// handleAppLaunch 启动应用：已登记 → 直接拉起；未登记 → 按名称搜索候选并自动登记后拉起。
func (b *Bridge) handleAppLaunch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	appID := argString(req, "appId")
	name := argString(req, "name")
	waitConnected := argBool(req, "waitConnected", true)
	if appID == "" && name == "" {
		return mcpError(MCPErrorInvalidArgs, "app_launch 需要 appId 或 name 参数")
	}

	var installed *apps.InstalledApp
	if appID != "" {
		installed = b.store.Get(appID)
		if installed == nil {
			return mcpError(MCPErrorAppOffline,
				fmt.Sprintf("应用 %q 未登记，无法启动（可用 app_search 搜索后用名称启动，或用 agentquay apps add 登记）", appID))
		}
	} else {
		installed = b.store.FindByName(name)
	}

	if installed != nil {
		appID = installed.AppID
		if !b.launcher.Enabled() {
			return mcpError(MCPErrorAppOffline, "自动拉起已禁用（config.launch.enabled = false）")
		}
		if !installed.Launchable() {
			return mcpError(MCPErrorAppOffline,
				fmt.Sprintf("应用 %s 缺少有效的启动命令（安装位置已失效，请用 agentquay apps add 重新登记）", appID))
		}
		// 已登记但从未注册过 SDK（纯发现应用）→ 只拉起不等待；否则等待 SDK 注册后返回可控状态
		var res *launcher.Result
		var err error
		if waitConnected && sdkKnown(installed) {
			res, err = b.launcher.LaunchAndWait(ctx, appID)
		} else {
			res, err = b.launcher.Launch(ctx, appID)
		}
		if err != nil {
			return mcpError(MCPErrorAppOffline, "启动失败: "+err.Error())
		}
		b.RebuildTools()
		return textResult(b.launchStatus(b.store.Get(appID), res))
	}

	// 未登记：按名称在系统里搜索
	if name == "" {
		return mcpError(MCPErrorAppOffline,
			fmt.Sprintf("应用 %q 未登记（可用 app_search 搜索后用名称启动）", appID))
	}
	cands := b.disco.Search(ctx, name, 5)
	best := pickBest(cands)
	if best == nil {
		return mcpError(MCPErrorAppOffline,
			fmt.Sprintf("系统中未找到名为 %q 的应用（可用 app_search 查看候选）", name))
	}
	if b.cfg.Launch.DiscoveryRequireConfirm {
		return mcpError(MCPErrorAppOffline,
			fmt.Sprintf("应用 %q 未登记且处于严格模式（launch.discoveryRequireConfirm=true），请先用 agentquay apps add --appId %s --name %q --path %q 登记",
				name, best.AppID, best.AppName, best.Launch.ExecPath))
	}
	// 自动登记（AutoAdoptDiscovery 默认开启，用户对 app_launch 的调用即表达了启动意图）
	adopted := &apps.InstalledApp{
		AppID:      best.AppID,
		AppName:    best.AppName,
		Launch:     best.Launch,
		Source:     apps.SourceDiscovery,
		AutoLaunch: apps.AutoLaunchOn,
	}
	if err := b.store.Upsert(adopted); err != nil {
		return mcpError(MCPErrorGeneric, "登记应用失败: "+err.Error())
	}
	b.logger.Info("自动登记发现的应用", "appId", best.AppID, "appName", best.AppName, "execPath", best.Launch.ExecPath)

	res, err := b.launcher.LaunchAndWait(ctx, best.AppID)
	if err != nil {
		return mcpError(MCPErrorAppOffline, "启动失败: "+err.Error())
	}
	b.RebuildTools()
	return textResult(b.launchStatus(b.store.Get(best.AppID), res))
}

// launchStatus 组装 app_launch 的返回结构。
func (b *Bridge) launchStatus(inst *apps.InstalledApp, res *launcher.Result) map[string]any {
	online := b.reg.Get(res.AppID) != nil
	var tools []string
	if app := b.reg.Get(res.AppID); app != nil {
		for _, t := range app.Tools {
			tools = append(tools, t.Name)
		}
	}
	status := "launched"
	if online || res.Connected {
		status = "connected"
	}
	return map[string]any{
		"appId":        res.AppID,
		"appName":      instName(inst, res.AppID),
		"status":       status, // connected=已完成 SDK 注册可控 | launched=仅启动，不可控
		"launched":     res.Launched,
		"controllable": online && len(tools) > 0,
		"tools":        tools,
		"pid":          res.PID,
		"elapsedMs":    res.Elapsed.Milliseconds(),
	}
}

func instName(inst *apps.InstalledApp, appID string) string {
	if inst != nil && inst.AppName != "" {
		return inst.AppName
	}
	return appID
}

// sdkKnown 判断该登记记录是否已知具备 SDK（自报来源、带工具或曾上线过）。
// 纯 OS 发现且从未注册过 SDK 的应用视为不可控，等待注册无意义。
func sdkKnown(inst *apps.InstalledApp) bool {
	if inst == nil {
		return false
	}
	return inst.Source == apps.SourceSDK || len(inst.Tools) > 0 || !inst.LastOnlineAt.IsZero()
}

// handleAppAdopt 把应用显式登记进启动注册表（app_search 找到的候选常用此固化）。
func (b *Bridge) handleAppAdopt(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	appID := argString(req, "appId")
	name := argString(req, "appName")
	execPath := argString(req, "execPath")
	if !registry.AppIDPattern.MatchString(appID) {
		return mcpError(MCPErrorInvalidArgs, "appId 只允许 [a-z0-9-]{1,48}，禁止 _ 和 .")
	}
	if name == "" || execPath == "" {
		return mcpError(MCPErrorInvalidArgs, "app_adopt 需要 appName 与 execPath")
	}
	adopted := &apps.InstalledApp{
		AppID:      appID,
		AppName:    name,
		Launch:     apps.LaunchCommand{ExecPath: execPath, Args: argStrings(req, "args")},
		Source:     apps.SourceDiscovery,
		AutoLaunch: apps.AutoLaunchOn,
	}
	if err := b.store.Upsert(adopted); err != nil {
		return mcpError(MCPErrorGeneric, "登记失败: "+err.Error())
	}
	b.logger.Info("已登记应用", "appId", appID, "appName", name, "execPath", execPath)
	b.RebuildTools()
	return textResult(map[string]any{"appId": appID, "appName": name, "registered": true})
}

// pickBest 取置信度最高的候选（不足 60 分视为无匹配）。
func pickBest(cands []discovery.Candidate) *discovery.Candidate {
	if len(cands) == 0 {
		return nil
	}
	best := cands[0]
	for _, c := range cands {
		if c.Confidence > best.Confidence {
			best = c
		}
	}
	if best.Confidence < 60 {
		return nil
	}
	return &best
}

func argString(req mcp.CallToolRequest, key string) string {
	if v, ok := req.GetArguments()[key].(string); ok {
		return v
	}
	return ""
}

func argStrings(req mcp.CallToolRequest, key string) []string {
	if v, ok := req.GetArguments()[key].([]any); ok {
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func argBool(req mcp.CallToolRequest, key string, def bool) bool {
	if v, ok := req.GetArguments()[key].(bool); ok {
		return v
	}
	return def
}

// textResult 把结构化数据以 JSON 文本返回给 Agent。
func textResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte("{}")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: string(data)}},
	}, nil
}

// mcpError 构造 MCP 工具执行错误。
// 注意：mcp-go v0.58 把工具处理器返回的 error 一律包装为 JSON-RPC -32603（INTERNAL_ERROR），
// 无法透传自定义 JSON-RPC 错误码；因此按 MCP SEP-1303 标准返回 IsError 结果，
// 将设计文档附录 B 的错误码以结构化 JSON 承载在 content 中，Agent 侧可直接读取。
func mcpError(code int, message string) (*mcp.CallToolResult, error) {
	errObj := map[string]any{"code": code, "message": message}
	text, _ := json.Marshal(errObj)
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{mcp.TextContent{Type: "text", Text: string(text)}},
		StructuredContent: errObj,
	}, nil
}

// toToolResult 将内部结果转换为 MCP 结果：成功 → text 内容；内部错误 → 对应错误码；业务错误 → 透传。
func (b *Bridge) toToolResult(res *types.ResultMessage) (*mcp.CallToolResult, error) {
	if res.Success {
		text, err := json.Marshal(res.Data)
		if err != nil {
			text = []byte("null")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: "text", Text: string(text)}},
		}, nil
	}
	switch res.Error.Code {
	case types.ErrCodeConnLost:
		return mcpError(MCPErrorAppOffline, "应用连接丢失: "+res.Error.Message)
	case types.ErrCodeReplaced:
		return mcpError(MCPErrorReplaced, "应用已被新实例替换")
	case types.ErrCodeRequestGone:
		return mcpError(MCPErrorGeneric, "请求已失效")
	default:
		// 业务错误透传（§5.6）：以 IsError 结果返回 error 信息
		data, err := json.Marshal(res.Error)
		if err != nil {
			data = []byte("{}")
		}
		return &mcp.CallToolResult{
			IsError:           true,
			Content:           []mcp.Content{mcp.TextContent{Type: "text", Text: string(data)}},
			StructuredContent: res.Error,
		}, nil
	}
}

// sendProgress 确认等待期间每 30s 发送 MCP 标准进度通知（§3.2 长时间等待）。
func (b *Bridge) sendProgress(ctx context.Context, req mcp.CallToolRequest) {
	ticker := time.NewTicker(b.heartbeatInterval())
	defer ticker.Stop()
	progress := 1
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			params := map[string]any{
				"progress": progress,
				"total":    2,
				"message":  "等待用户在应用中确认",
			}
			if req.Params.Meta != nil && req.Params.Meta.ProgressToken != nil {
				params["progressToken"] = req.Params.Meta.ProgressToken
			}
			if err := b.mcpsrv.SendNotificationToClient(ctx, "notifications/progress", params); err != nil {
				return // 会话已断开
			}
			progress++
		}
	}
}

func (b *Bridge) heartbeatInterval() time.Duration {
	sec := b.cfg.HeartbeatIntervalSeconds
	if sec <= 0 {
		sec = 30
	}
	return time.Duration(sec) * time.Second
}

// timeoutOf 取 tool 级超时，缺省回退配置默认值。
func (b *Bridge) timeoutOf(seconds, fallback int) time.Duration {
	if seconds <= 0 {
		seconds = fallback
	}
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

// validateArgs 用 inputSchema 校验 arguments（santhosh-tekuri/jsonschema/v5）。
func (b *Bridge) validateArgs(schema any, args map[string]any) error {
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("inputSchema 序列化失败: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("input.json", bytes.NewReader(data)); err != nil {
		return err
	}
	sch, err := compiler.Compile("input.json")
	if err != nil {
		return fmt.Errorf("inputSchema 无效: %w", err)
	}
	return sch.Validate(args)
}
