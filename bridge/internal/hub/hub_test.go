package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/auth"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/protocol"
	"agentquay/bridge/internal/registry"
	"agentquay/bridge/internal/types"
)

type discardSink struct{}

func (discardSink) Write(p []byte) (int, error) { return len(p), nil }

func newTestHub(t *testing.T) (*Hub, *registry.Registry, *apps.Store) {
	t.Helper()
	cfg := config.Default()
	store, err := auth.Load(t.TempDir() + "/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	appStore, err := apps.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(10)
	logger := slog.New(slog.NewTextHandler(discardSink{}, nil))
	return New(cfg, reg, store, appStore, logger), reg, appStore
}

func TestRegisterPersistsLaunchInfo(t *testing.T) {
	h, reg, appStore := newTestHub(t)

	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 应用注册：上报 launch 信息
	register := protocol.Envelope{
		Type: protocol.MsgRegister,
		Payload: mustRaw(t, protocol.RegisterPayload{
			AppID:           "note-app",
			AppName:         "Notes",
			Version:         "1.0.0",
			ProtocolVersion: "1.0",
			AuthToken:       "",
			Tools: []types.ToolMetadata{
				{Name: "add", Description: "add note"},
			},
			Launch: &protocol.LaunchInfo{
				ExecPath:             `C:\apps\notes.exe`,
				Args:                 []string{"--quiet"},
				LaunchTimeoutSeconds: 15,
			},
		}),
	}
	if err := conn.WriteJSON(register); err != nil {
		t.Fatalf("write register: %v", err)
	}

	// 等 register_ack
	deadline := time.Now().Add(5 * time.Second)
	var ack protocol.Envelope
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.ReadJSON(&ack); err == nil && ack.Type == protocol.MsgRegisterAck {
			break
		}
	}
	if ack.Type != protocol.MsgRegisterAck {
		t.Fatal("未收到 register_ack")
	}

	// 在线注册表中可见
	if app := reg.Get("note-app"); app == nil {
		t.Fatal("应用未进入在线注册表")
	}

	// launch 信息持久化到启动注册表
	installed := appStore.Get("note-app")
	if installed == nil {
		t.Fatal("启动注册表未写入")
	}
	if installed.Launch.ExecPath != `C:\apps\notes.exe` {
		t.Errorf("launch execPath 未持久化: %+v", installed.Launch)
	}
	if installed.Source != apps.SourceSDK || installed.AutoLaunch != apps.AutoLaunchOn {
		t.Errorf("source/autoLaunch 错误: %+v", installed)
	}
	if len(installed.Tools) != 1 || installed.Tools[0].Name != "add" {
		t.Errorf("tools 未持久化: %+v", installed.Tools)
	}

	// 断开连接 → 注销
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = conn.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(time.Second))
	for ctx.Err() == nil {
		if reg.Get("note-app") == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if app := reg.Get("note-app"); app != nil {
		t.Error("断开后未注销")
	}
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// registerAndWaitAck 建立连接并发送注册消息，等待 register_ack / register_error。
// 返回连接与收到的响应（调用方负责关闭连接）。
func registerAndWaitAck(t *testing.T, h *Hub, p protocol.RegisterPayload) (*websocket.Conn, protocol.Envelope) {
	t.Helper()
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteJSON(protocol.Envelope{
		Type:    protocol.MsgRegister,
		Payload: mustRaw(t, p),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var env protocol.Envelope
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.ReadJSON(&env); err == nil && (env.Type == protocol.MsgRegisterAck || env.Type == protocol.MsgRegisterError) {
			return conn, env
		}
	}
	t.Fatal("未收到 register_ack / register_error")
	return conn, env
}

// 页面智能路由：带 pageKey 的工具正常注册，PageKey 随元数据进入在线注册表与启动注册表。
func TestRegisterWithPageKey(t *testing.T) {
	h, reg, appStore := newTestHub(t)

	conn, ack := registerAndWaitAck(t, h, protocol.RegisterPayload{
		AppID:           "music-app",
		AppName:         "Music Player",
		Version:         "1.0.0",
		ProtocolVersion: "1.0",
		Tools: []types.ToolMetadata{
			{Name: "search", Description: "搜索音乐", PageKey: "SearchPage"},
			{Name: "quit", Description: "退出应用"},
		},
	})
	defer conn.Close()
	if ack.Type != protocol.MsgRegisterAck {
		t.Fatalf("注册被拒绝: %s", string(ack.Payload))
	}

	app := reg.Get("music-app")
	if app == nil {
		t.Fatal("应用未进入在线注册表")
	}
	search := app.Tool("search")
	if search == nil || search.PageKey != "SearchPage" {
		t.Fatalf("pageKey 未随元数据保留: %+v", search)
	}
	if quit := app.Tool("quit"); quit == nil || quit.PageKey != "" {
		t.Fatalf("无 pageKey 工具不传即为空: %+v", quit)
	}

	installed := appStore.Get("music-app")
	if installed == nil || len(installed.Tools) != 2 || installed.Tools[0].PageKey != "SearchPage" {
		t.Fatalf("pageKey 未持久化到启动注册表: %+v", installed)
	}
}

// 工具名跨页面也必须全局唯一：不同 pageKey 下同名 → INVALID_TOOL。
func TestRegisterDuplicateNameAcrossPagesRejected(t *testing.T) {
	h, _, _ := newTestHub(t)

	conn, env := registerAndWaitAck(t, h, protocol.RegisterPayload{
		AppID:           "music-app",
		AppName:         "Music Player",
		Version:         "1.0.0",
		ProtocolVersion: "1.0",
		Tools: []types.ToolMetadata{
			{Name: "search", Description: "页面A搜索", PageKey: "SearchPageA"},
			{Name: "search", Description: "页面B搜索", PageKey: "SearchPageB"},
		},
	})
	defer conn.Close()
	if env.Type != protocol.MsgRegisterError {
		t.Fatalf("跨页面重名应注册失败，却收到: %s", env.Type)
	}
	var payload protocol.RegisterErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != protocol.CodeInvalidTool {
		t.Fatalf("错误码应为 INVALID_TOOL，实际: %s", payload.Code)
	}
}
