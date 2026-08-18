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
