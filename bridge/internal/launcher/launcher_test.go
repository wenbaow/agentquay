package launcher

import (
	"context"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"agentquay/bridge/internal/apps"
	"agentquay/bridge/internal/config"
	"agentquay/bridge/internal/registry"
)

func testSetup(t *testing.T) (*config.Config, *apps.Store, *registry.Registry, *Service) {
	t.Helper()
	cfg := config.Default()
	store, err := apps.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(10)
	logger := slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	svc := New(cfg, store, reg, logger)
	return cfg, store, reg, svc
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func (ioDiscard) Close() error { return nil }

func TestCanAutoLaunch(t *testing.T) {
	cfg, store, _, svc := testSetup(t)
	store.Upsert(&apps.InstalledApp{AppID: "a", AppName: "A",
		Launch: apps.LaunchCommand{ExecPath: osExecutablePath(t)}, Source: apps.SourceSDK, AutoLaunch: apps.AutoLaunchOn})
	store.Upsert(&apps.InstalledApp{AppID: "b", AppName: "B",
		Launch: apps.LaunchCommand{ExecPath: osExecutablePath(t)}, Source: apps.SourceSDK, AutoLaunch: apps.AutoLaunchOff})
	store.Upsert(&apps.InstalledApp{AppID: "c", AppName: "C",
		Source: apps.SourceSDK, AutoLaunch: apps.AutoLaunchOn}) // 无启动命令

	if !svc.CanAutoLaunch("a") {
		t.Error("a 应可自动拉起")
	}
	if svc.CanAutoLaunch("b") {
		t.Error("b 关闭了 autoLaunch 不应可拉起")
	}
	if svc.CanAutoLaunch("c") {
		t.Error("c 无启动命令不应可拉起")
	}
	if svc.CanAutoLaunch("不存在") {
		t.Error("未登记应用不应可拉起")
	}

	cfg.Launch.Enabled = false
	if svc.CanAutoLaunch("a") {
		t.Error("全局禁用后不应可拉起")
	}
}

func TestLaunchAndWait_OnlineFastPath(t *testing.T) {
	_, store, reg, svc := testSetup(t)
	store.Upsert(&apps.InstalledApp{AppID: "fast", AppName: "Fast",
		Launch: apps.LaunchCommand{ExecPath: osExecutablePath(t)}, Source: apps.SourceSDK, AutoLaunch: apps.AutoLaunchOn})
	// 人为置为在线 → 直接返回 connected，不启动
	reg.Register(&registry.Application{AppID: "fast", AppName: "Fast"})
	res, err := svc.LaunchAndWait(context.Background(), "fast")
	if err != nil {
		t.Fatalf("LaunchAndWait: %v", err)
	}
	if !res.Connected || res.Launched {
		t.Errorf("在线应用应直接 connected 且不启动: %+v", res)
	}
}

func TestLaunchAndWait_RealSpawn(t *testing.T) {
	_, store, _, svc := testSetup(t)
	lc := osLaunchCommand(t)
	store.Upsert(&apps.InstalledApp{AppID: "sleepy", AppName: "Sleepy",
		Launch: lc, Source: apps.SourceSDK, AutoLaunch: apps.AutoLaunchOn})

	// 启动一个立即退出的进程（不会注册 SDK），等待超时后应返回未连接
	svc.cfg.Launch.DefaultTimeoutSeconds = 1 // 缩短等待

	start := time.Now()
	res, err := svc.LaunchAndWait(context.Background(), "sleepy")
	if err != nil {
		t.Fatalf("LaunchAndWait: %v", err)
	}
	if res.Launched != true || res.Connected != false {
		t.Errorf("应已启动但未注册: %+v", res)
	}
	if time.Since(start) < 800*time.Millisecond {
		t.Errorf("等待超时过短: %v", time.Since(start))
	}
}

func TestLaunch_Dedup(t *testing.T) {
	_, store, reg, svc := testSetup(t)
	store.Upsert(&apps.InstalledApp{AppID: "d", AppName: "D",
		Launch: osLaunchCommand(t), Source: apps.SourceSDK, AutoLaunch: apps.AutoLaunchOn})

	ctx := context.Background()
	res1, err1 := svc.Launch(ctx, "d")
	if err1 != nil {
		t.Fatalf("first launch: %v", err1)
	}
	if !res1.Launched {
		t.Error("首次调用应实际启动")
	}
	// 第二次调用（进程仍在启动中，未上线）：不应重复拉起
	res2, err2 := svc.Launch(ctx, "d")
	if err2 != nil {
		t.Fatalf("second launch: %v", err2)
	}
	if res2.Launched {
		t.Error("In-flight 期间不应重复拉起")
	}
	// 上线后：直接 connected
	reg.Register(&registry.Application{AppID: "d", AppName: "D"})
	res3, _ := svc.Launch(ctx, "d")
	if !res3.Connected {
		t.Error("上线后应 direct connected")
	}
}

// osLaunchCommand 返回一个"启动即退出"的进程命令（避免测试留下窗口/进程）。
func osLaunchCommand(t *testing.T) apps.LaunchCommand {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		return apps.LaunchCommand{ExecPath: `C:\Windows\System32\cmd.exe`, Args: []string{"/c", "exit"}}
	default:
		return apps.LaunchCommand{ExecPath: "/bin/true"}
	}
}

// osExecutablePath 返回一个必然存在的可执行文件路径（仅判断存在性，不实际启动）。
func osExecutablePath(t *testing.T) string {
	t.Helper()
	return osLaunchCommand(t).ExecPath
}
