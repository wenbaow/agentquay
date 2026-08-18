package apps

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentquay/bridge/internal/protocol"
	"agentquay/bridge/internal/types"
)

func newTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s, dir
}

func TestSaveForRegistration_CreatesAndUpdates(t *testing.T) {
	s, dir := newTempStore(t)

	// 首次注册：创建记录，来源 sdk，autoLaunch on，持久化 launch 信息
	payload := protocol.RegisterPayload{
		AppID:   "note-app",
		AppName: "Notes",
		Version: "1.0.0",
		Tools: []types.ToolMetadata{
			{Name: "add", Description: "add note"},
		},
		Launch: &protocol.LaunchInfo{
			ExecPath:             `C:\apps\notes.exe`,
			Args:                 []string{"--quiet"},
			LaunchTimeoutSeconds: 20,
		},
	}
	if err := s.SaveForRegistration(payload); err != nil {
		t.Fatalf("SaveForRegistration: %v", err)
	}

	got := s.Get("note-app")
	if got == nil {
		t.Fatal("记录未写入")
	}
	if got.Source != SourceSDK || got.AutoLaunch != AutoLaunchOn {
		t.Errorf("source=%s autoLaunch=%s，期望 sdk/on", got.Source, got.AutoLaunch)
	}
	if got.Launch.ExecPath != `C:\apps\notes.exe` || len(got.Launch.Args) != 1 {
		t.Errorf("launch 未持久化: %+v", got.Launch)
	}
	if got.LaunchTimeoutSeconds != 20 {
		t.Errorf("launchTimeoutSeconds=%d", got.LaunchTimeoutSeconds)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "add" {
		t.Errorf("tools 未持久化: %+v", got.Tools)
	}

	// 第二次注册（应用升级搬路径）：应更新路径并把旧路径收进历史
	payload.Version = "2.0.0"
	payload.Launch.ExecPath = `D:\apps\notes.exe`
	if err := s.SaveForRegistration(payload); err != nil {
		t.Fatalf("第二次注册: %v", err)
	}
	got = s.Get("note-app")
	if got.Version != "2.0.0" {
		t.Errorf("version 未更新: %s", got.Version)
	}
	if got.Launch.ExecPath != `D:\apps\notes.exe` {
		t.Errorf("新路径未生效: %+v", got.Launch)
	}
	if len(got.Locations) != 1 || got.Locations[0] != `C:\apps\notes.exe` {
		t.Errorf("旧路径未入历史: %+v", got.Locations)
	}
	if got.Launchable() {
		t.Error("Launchable 应为 false（测试路径均不存在于本机）")
	}

	// 无 launch 字段的旧 SDK：保留既有启动命令
	payload.Launch = nil
	if err := s.SaveForRegistration(payload); err != nil {
		t.Fatalf("无 launch 注册: %v", err)
	}
	got = s.Get("note-app")
	if got.Launch.ExecPath != `D:\apps\notes.exe` {
		t.Errorf("无 launch 时不应覆盖已有启动命令: %+v", got.Launch)
	}

	// 文件落盘存在且可重载
	if _, err := os.Stat(filepath.Join(dir, "note-app.json")); err != nil {
		t.Errorf("记录文件未落盘: %v", err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("重载: %v", err)
	}
	if r := reloaded.Get("note-app"); r == nil || r.Version != "2.0.0" {
		t.Error("重载后数据不一致")
	}
}

func TestResolveLaunch_PathFallback(t *testing.T) {
	s, _ := newTempStore(t)
	good := t.TempDir()
	f, err := os.CreateTemp(good, "app*.exe")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	goodPath := f.Name()

	// 主路径失效 → 回退历史路径
	app := &InstalledApp{
		AppID:      "portable-app",
		AppName:    "Portable",
		Launch:     LaunchCommand{ExecPath: `Z:\nonexistent\app.exe`},
		Locations:  []string{goodPath},
		Source:     SourceUser,
		AutoLaunch: AutoLaunchOn,
	}
	if err := s.Upsert(app); err != nil {
		t.Fatal(err)
	}
	got := s.Get("portable-app")
	if !got.Launchable() {
		t.Error("有可用历史路径时 Launchable 应为 true")
	}
	if got.ResolveLaunch().ExecPath != goodPath {
		t.Errorf("未回退到历史路径: %+v", got.ResolveLaunch())
	}
}

func TestFindByName(t *testing.T) {
	s, _ := newTempStore(t)
	must := func(id, name string) {
		t.Helper()
		if err := s.Upsert(&InstalledApp{AppID: id, AppName: name, Source: SourceSDK, AutoLaunch: AutoLaunchOn}); err != nil {
			t.Fatal(err)
		}
	}
	must("wechat", "微信")
	must("wecom", "企业微信")

	if got := s.FindByName("wechat"); got == nil || got.AppID != "wechat" {
		t.Error("appId 精确匹配失败")
	}
	if got := s.FindByName("微信"); got == nil || got.AppID != "wechat" {
		t.Error("appName 精确匹配失败")
	}
	if got := s.FindByName("微信"); got == nil {
		t.Error("中文名匹配失败")
	}
	if got := s.FindByName("不存在"); got != nil {
		t.Errorf("不应匹配到: %+v", got)
	}
}

func TestStoreRemoveSetAutoLaunch(t *testing.T) {
	s, dir := newTempStore(t)
	if err := s.Upsert(&InstalledApp{AppID: "a", AppName: "A", Source: SourceUser, AutoLaunch: AutoLaunchOn}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAutoLaunch("a", AutoLaunchOff); err != nil {
		t.Fatal(err)
	}
	if s.Get("a").AutoLaunch != AutoLaunchOff {
		t.Error("autoLaunch 未更新")
	}
	if err := s.Remove("缺失"); err == nil {
		t.Error("移除不存在应用应报错")
	}
	if err := s.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.json")); !os.IsNotExist(err) {
		t.Error("移除后文件应删除")
	}
}

func TestLaunchTimeout(t *testing.T) {
	s, _ := newTempStore(t)
	var app *InstalledApp

	// 无定制 + 全局默认
	if d := app.LaunchTimeout(15); d != 15*time.Second {
		t.Errorf("默认超时错误: %v", d)
	}
	// 无效全局默认
	if d := app.LaunchTimeout(0); d != 15*time.Second {
		t.Errorf("兜底超时错误: %v", d)
	}
	// 应用定制优先
	if err := s.Upsert(&InstalledApp{AppID: "a", AppName: "A", LaunchTimeoutSeconds: 40,
		Source: SourceSDK, AutoLaunch: AutoLaunchOn}); err != nil {
		t.Fatal(err)
	}
	if d := s.Get("a").LaunchTimeout(15); d != 40*time.Second {
		t.Errorf("定制超时错误: %v", d)
	}
}
