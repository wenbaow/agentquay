package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/auth"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/discovery"
	"agentquay/bridge/internal/hub"
	"agentquay/bridge/internal/launcher"
	"agentquay/bridge/internal/registry"
	"agentquay/bridge/internal/types"
)

type discardSink struct{}

func (discardSink) Write(p []byte) (int, error) { return len(p), nil }

// stubLauncher / stubDiscoverer：测试替身（与 launcher_test 同构）。
type stubLauncher struct{ enabled bool }

func (s *stubLauncher) Enabled() bool                   { return s.enabled }
func (s *stubLauncher) CanAutoLaunch(appID string) bool { return false }
func (s *stubLauncher) Launch(_ context.Context, _ string) (*launcher.Result, error) {
	return &launcher.Result{AppID: "x"}, nil
}
func (s *stubLauncher) LaunchAndWait(_ context.Context, _ string) (*launcher.Result, error) {
	return &launcher.Result{AppID: "x"}, nil
}

type stubDiscoverer struct{}

func (s *stubDiscoverer) Enabled() bool { return false }
func (s *stubDiscoverer) Search(_ context.Context, _ string, _ int) []discovery.Candidate {
	return nil
}

func newTestBridge(t *testing.T) (*Bridge, *registry.Registry, *apps.Store) {
	t.Helper()
	cfg := config.Default()
	reg := registry.New(10)
	store, err := apps.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(cfg, reg, mustAuthStore(t), store, slog.New(slog.NewTextHandler(discardSink{}, nil)))
	logger := slog.New(slog.NewTextHandler(discardSink{}, nil))
	b := New(cfg, reg, h, store, &stubLauncher{}, &stubDiscoverer{}, "test", logger)
	h.SetOnAppsChanged(b.RebuildTools)
	return b, reg, store
}

// mustAuthStore 加载临时 auth 存储（hub.New 的第三个参数）。
func mustAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	store, err := auth.Load(t.TempDir() + "/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// describeTool 前缀规则（页面智能路由 §6.3）。
func TestDescribeTool(t *testing.T) {
	cases := []struct {
		name string
		tool types.ToolMetadata
		want string
	}{
		{"无 pageKey", types.ToolMetadata{Description: "搜索音乐"}, "[Music Player] 搜索音乐"},
		{"有 pageKey", types.ToolMetadata{Description: "搜索音乐", PageKey: "SearchPage"}, "[Music Player|SearchPage] 搜索音乐"},
	}
	for _, c := range cases {
		if got := describeTool("Music Player", c.tool); got != c.want {
			t.Errorf("%s: describeTool = %q, want %q", c.name, got, c.want)
		}
	}
}

// tools/list 描述前缀端到端：在线应用（[AppName|PageKey]）与离线应用（[未运行] 标注）。
func TestRebuildToolsDescriptionPrefix(t *testing.T) {
	b, reg, store := newTestBridge(t)

	reg.Register(&registry.Application{
		AppID:   "music-app",
		AppName: "Music Player",
		Tools: []types.ToolMetadata{
			{Name: "search", Description: "搜索音乐", PageKey: "SearchPage"},
			{Name: "quit", Description: "退出应用"},
		},
	})
	b.RebuildTools()

	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	// Streamable HTTP：initialize → tools/list（此后 RebuildTools 广播 list_changed 时
	// 响应会混入 SSE 通知，decodeMCPResponse 负责拆流）
	client := &http.Client{}
	sessionID := ""
	post := func(payload any) map[string]any {
		data, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(data))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		// Streamable HTTP 要求 Accept 头；后续请求携带会话头
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			sessionID = id
		}
		return decodeMCPResponse(resp)
	}

	resp0 := post(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	})
	if resp0["result"] == nil {
		t.Fatalf("initialize 失败: %v", resp0)
	}

	desc := listToolsOf(t, post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}))
	if got := desc["music-app_search"]; got != "[Music Player|SearchPage] 搜索音乐" {
		t.Errorf("页面工具描述前缀错误: %q", got)
	}
	if got := desc["music-app_quit"]; got != "[Music Player] 退出应用" {
		t.Errorf("普通工具描述前缀错误: %q", got)
	}

	// 离线应用（启动注册表）→ [未运行] 前缀；pageKey 同样体现在描述中
	if err := store.Upsert(&apps.InstalledApp{
		AppID:      "music-app",
		AppName:    "Music Player",
		Source:     apps.SourceSDK,
		AutoLaunch: apps.AutoLaunchOn,
		Tools: []types.ToolMetadata{
			{Name: "search", Description: "搜索音乐", PageKey: "SearchPage"},
			{Name: "quit", Description: "退出应用"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	reg.Unregister("music-app")
	b.RebuildTools()

	desc = listToolsOf(t, post(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"}))
	if got := desc["music-app_search"]; got != "[未运行] [Music Player|SearchPage] 搜索音乐" {
		t.Errorf("离线页面工具描述前缀错误: %q", got)
	}
}

// decodeMCPResponse 解析 Streamable HTTP 响应：
// 普通 JSON 直接解码；text/event-stream（挂起通知与结果混流）时提取每条 data: 的 JSON，
// 返回带请求 id 的那一条（通知无 id，跳过）。
func decodeMCPResponse(resp *http.Response) map[string]any {
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) == 0 {
		return map[string]any{}
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return out
	}
	// SSE：event: message\ndata: {...}\n\n
	var withID map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &msg); err != nil {
			continue
		}
		if _, hasID := msg["id"]; hasID {
			withID = msg
		}
	}
	if withID != nil {
		return withID
	}
	return map[string]any{}
}

// listToolsOf 从 tools/list 响应中取工具描述表（失败时 t.Fatal）。
func listToolsOf(t *testing.T, out map[string]any) map[string]string {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list 失败: %v", out)
	}
	toolsList, _ := result["tools"].([]any)
	desc := make(map[string]string, len(toolsList))
	for _, t0 := range toolsList {
		tool, _ := t0.(map[string]any)
		desc[tool["name"].(string)] = tool["description"].(string)
	}
	return desc
}