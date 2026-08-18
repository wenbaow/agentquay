// Package apps 实现应用启动注册表（设计文档 §5.8、附录 C）：
// ~/.agentquay/apps/<appId>.json 持久化每个应用的启动命令、来源与最近状态，
// 使 Bridge 能在应用离线时仍然知道其安装位置并自动拉起。
//
// 信息获取三层（§5.8）：
//  1. SDK 注册时自报 launch 信息（首选，随注册刷新，天然跟随应用更新/搬家）；
//  2. 用户/CLI 显式登记（apps add）；
//  3. OS 级发现（开始菜单 / .desktop / /Applications），成功拉起后经 AutoAdopt 毕业为已登记。
package apps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agentquay/bridge/internal/protocol"
	"agentquay/bridge/internal/types"
)

// 登记来源。
const (
	SourceSDK       = "sdk"       // SDK 首次注册自报
	SourceUser      = "user"      // 用户/CLI 显式登记
	SourceDiscovery = "discovery" // OS 级发现后自动登记
)

// 自动拉起模式。
const (
	AutoLaunchOn      = "on"      // 离线时自动拉起（默认，SDK 注册应用）
	AutoLaunchConfirm = "confirm" // 需确认（预留：配合客户端权限层）
	AutoLaunchOff     = "off"     // 禁止自动拉起
)

// LaunchCommand 启动命令（argv 直传，杜绝 shell 字符串注入）。
type LaunchCommand struct {
	ExecPath string   `json:"execPath"`
	Args     []string `json:"args,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
}

// InstalledApp 一条已登记应用的注册表记录。
type InstalledApp struct {
	AppID                string               `json:"appId"`
	AppName              string               `json:"appName"`
	Version              string               `json:"version,omitempty"`
	Launch               LaunchCommand        `json:"launchCommand,omitempty"`
	LaunchTimeoutSeconds int                  `json:"launchTimeoutSeconds,omitempty"` // SDK 定制的启动等待超时
	Source               string               `json:"source"`                         // sdk | user | discovery
	AutoLaunch           string               `json:"autoLaunch"`                     // on | confirm | off
	Tools                []types.ToolMetadata `json:"tools,omitempty"`                // 上次注册时的工具表（离线展示用）
	LastSeenAt           time.Time            `json:"lastSeenAt,omitempty"`
	LastOnlineAt         time.Time            `json:"lastOnlineAt,omitempty"`
	LastLaunchAt         time.Time            `json:"lastLaunchAt,omitempty"`
	LaunchCount          int                  `json:"launchCount,omitempty"`
	Locations            []string             `json:"locations,omitempty"` // 历史路径，主路径失效时回退
}

// Launchable 判断该应用当前是否有可用的启动命令。
func (a *InstalledApp) Launchable() bool {
	if a.Launch.ExecPath == "" {
		return false
	}
	if _, err := os.Stat(a.Launch.ExecPath); err == nil {
		return true
	}
	// 主路径失效：回退历史位置
	for _, loc := range a.Locations {
		if _, err := os.Stat(loc); err == nil {
			return true
		}
	}
	return false
}

// ResolveLaunch 返回可用启动命令（主路径失效时自动回退历史位置）。
func (a *InstalledApp) ResolveLaunch() LaunchCommand {
	lc := a.Launch
	if _, err := os.Stat(lc.ExecPath); err == nil {
		return lc
	}
	for _, loc := range a.Locations {
		if _, err := os.Stat(loc); err == nil {
			lc.ExecPath = loc
			return lc
		}
	}
	return a.Launch
}

// LaunchTimeout 返回该应用的启动等待超时（无定制时回退全局默认）。
func (a *InstalledApp) LaunchTimeout(defaultSeconds int) time.Duration {
	if a != nil && a.LaunchTimeoutSeconds > 0 {
		return time.Duration(a.LaunchTimeoutSeconds) * time.Second
	}
	if defaultSeconds > 0 {
		return time.Duration(defaultSeconds) * time.Second
	}
	return 15 * time.Second
}

// ErrNotInstalled 应用未登记。
var ErrNotInstalled = errors.New("应用未登记")

// Store 应用注册表（内存镜像 + 目录持久化）。
type Store struct {
	mu   sync.RWMutex
	dir  string
	apps map[string]*InstalledApp
}

// Load 加载目录下所有 <appId>.json（损坏文件跳过并记录错误）。
func Load(dir string) (*Store, error) {
	s := &Store{dir: dir, apps: make(map[string]*InstalledApp)}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var app InstalledApp
		if err := json.Unmarshal(data, &app); err != nil || app.AppID == "" {
			continue // 损坏记录不阻塞启动，下次注册/登记时重建
		}
		s.apps[app.AppID] = &app
	}
	return s, nil
}

// Get 按 appId 查询。
func (s *Store) Get(appID string) *InstalledApp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app := s.apps[appID]
	if app == nil {
		return nil
	}
	cp := *app // 防外部修改
	return &cp
}

// FindByName 按名称查找已登记应用：精确匹配 appId → 精确匹配 appName → 包含匹配（取第一个）。
func (s *Store) FindByName(name string) *InstalledApp {
	name = strings.ToLower(strings.TrimSpace(name))
	s.mu.RLock()
	defer s.mu.RUnlock()
	var fuzzy *InstalledApp
	for _, app := range s.apps {
		if strings.ToLower(app.AppID) == name {
			return app
		}
		ln := strings.ToLower(app.AppName)
		if ln == name {
			return app
		}
		if fuzzy == nil && strings.Contains(ln, name) {
			fuzzy = app
		}
	}
	return fuzzy
}

// All 返回全部记录的拷贝（按 appId 排序）。
func (s *Store) All() []*InstalledApp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*InstalledApp, 0, len(s.apps))
	for _, app := range s.apps {
		cp := *app
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out
}

// Upsert 插入或更新并落盘（source=user 的登记默认 autoLaunch=on）。
func (s *Store) Upsert(app *InstalledApp) error {
	if app.AppID == "" {
		return fmt.Errorf("appId 不能为空")
	}
	if app.Source == "" {
		app.Source = SourceUser
	}
	if app.AutoLaunch == "" {
		app.AutoLaunch = AutoLaunchOn
	}
	app.LastSeenAt = time.Now()
	s.mu.Lock()
	s.apps[app.AppID] = app
	err := s.saveLocked(app)
	s.mu.Unlock()
	return err
}

// Remove 移除并删除文件（不存在返回 ErrNotInstalled）。
func (s *Store) Remove(appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		return ErrNotInstalled
	}
	delete(s.apps, appID)
	if err := os.Remove(s.pathOf(appID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SaveForRegistration 应用注册时刷新记录（§5.8）：合并启动命令、工具表与时间戳。
// launch 为 SDK 自报的可选启动信息；nil 时保留既有记录（兼容未升级的旧 SDK）。
func (s *Store) SaveForRegistration(p protocol.RegisterPayload) error {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	app, ok := s.apps[p.AppID]
	if !ok {
		app = &InstalledApp{
			AppID:      p.AppID,
			AppName:    p.AppName,
			Version:    p.Version,
			Source:     SourceSDK,
			AutoLaunch: AutoLaunchOn,
		}
		s.apps[p.AppID] = app
	}
	app.AppName = p.AppName
	app.Version = p.Version
	app.Tools = p.Tools
	app.LastSeenAt = now
	app.LastOnlineAt = now

	if p.Launch != nil {
		lc := LaunchCommand{ExecPath: p.Launch.ExecPath, Args: p.Launch.Args, Cwd: p.Launch.Cwd}
		app.LaunchTimeoutSeconds = p.Launch.LaunchTimeoutSeconds
		// 记录路径变化：主路径更新时把旧路径收入历史，供回退
		if lc.ExecPath != "" && lc.ExecPath != app.Launch.ExecPath {
			if app.Launch.ExecPath != "" {
				app.Locations = appendUnique(app.Locations, app.Launch.ExecPath)
			}
			app.Launch = lc
		} else if lc.ExecPath != "" {
			app.Launch = lc
		}
	}
	return s.saveLocked(app)
}

// LogLaunch 记录一次成功执行系统拉起（计数与时间戳）。
func (s *Store) LogLaunch(appID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.apps[appID]
	if app == nil {
		return
	}
	app.LaunchCount++
	app.LastLaunchAt = time.Now()
	_ = s.saveLocked(app)
}

// SetAutoLaunch 设置自动拉起模式。
func (s *Store) SetAutoLaunch(appID, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.apps[appID]
	if app == nil {
		return ErrNotInstalled
	}
	switch mode {
	case AutoLaunchOn, AutoLaunchConfirm, AutoLaunchOff:
		app.AutoLaunch = mode
	default:
		return fmt.Errorf("autoLaunch 取值必须为 on/confirm/off")
	}
	return s.saveLocked(app)
}

func (s *Store) pathOf(appID string) string { return filepath.Join(s.dir, appID+".json") }

func (s *Store) saveLocked(app *InstalledApp) error {
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.pathOf(app.AppID), data, 0o600)
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
