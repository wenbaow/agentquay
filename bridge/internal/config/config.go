// Package config 管理 Bridge 配置（~/.agentquay/config.json，设计文档 §5.4）。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// 默认端口与 autoPort 探测范围（§5.4）。
const (
	DefaultPort      = 19846
	AutoPortSpan     = 10 // autoPort 时尝试 19846–19856
	MaxPortFileBytes = 64
)

// LaunchConfig 应用启动注册表配置（§5.8）。
type LaunchConfig struct {
	Enabled                  bool `json:"enabled"`                  // 全局开关：是否允许自动拉起离线应用
	DefaultTimeoutSeconds    int  `json:"defaultTimeoutSeconds"`    // 启动后等待注册的超时（默认 15s）
	MaxConcurrentLaunches    int  `json:"maxConcurrentLaunches"`    // 同时启动中的应用上限（防失控）
	DiscoveryEnabled         bool `json:"discoveryEnabled"`         // OS 级发现（开始菜单/.desktop//Applications）
	DiscoveryScanOnStart     bool `json:"discoveryScanOnStart"`     // 启动时后台预扫描一次并缓存（默认 true）
	DiscoveryCacheTTLSeconds int  `json:"discoveryCacheTtlSeconds"` // 发现缓存有效期（默认 300s，≤0 用默认 5 分钟）
	DiscoveryRequireConfirm  bool `json:"discoveryRequireConfirm"`  // 严格模式：未登记应用需先确认才允许拉起
	AutoAdoptDiscovery       bool `json:"autoAdoptDiscovery"`       // 由名称拉起未登记应用时自动登记（用户已表达意图）
}

// Config 主配置（§5.4）。
type Config struct {
	Port                       int          `json:"port"`
	MCPPath                    string       `json:"mcpPath"`
	WSPath                     string       `json:"wsPath"`
	Host                       string       `json:"host"`
	AutoPort                   bool         `json:"autoPort"`
	PortFile                   string       `json:"portFile"`
	LogLevel                   string       `json:"logLevel"`
	LogFile                    string       `json:"logFile"`
	DefaultTimeoutSeconds      int          `json:"defaultTimeoutSeconds"`
	ConfirmTimeoutSeconds      int          `json:"confirmTimeoutSeconds"`
	HeartbeatIntervalSeconds   int          `json:"heartbeatIntervalSeconds"`
	MaxConnections             int          `json:"maxConnections"`
	AllowRemoteConnections     bool         `json:"allowRemoteConnections"`
	OrphanResultBuffer         int          `json:"orphanResultBuffer"`
	EmbeddedIdleTimeoutSeconds int          `json:"embeddedIdleTimeoutSeconds"` // 嵌入模式空闲自回收宽限（0 = 永不）
	Launch                     LaunchConfig `json:"launch"`
}

// Default 返回默认配置（§5.4）。
func Default() *Config {
	return &Config{
		Port:                       DefaultPort,
		MCPPath:                    "/mcp",
		WSPath:                     "/ws",
		Host:                       "127.0.0.1",
		AutoPort:                   true,
		PortFile:                   "~/.agentquay/port",
		LogLevel:                   "info",
		LogFile:                    "~/.agentquay/agentquay.log",
		DefaultTimeoutSeconds:      30,
		ConfirmTimeoutSeconds:      120,
		HeartbeatIntervalSeconds:   30,
		MaxConnections:             100,
		AllowRemoteConnections:     false,
		OrphanResultBuffer:         100,
		EmbeddedIdleTimeoutSeconds: 60,
		Launch: LaunchConfig{
			Enabled:                  true,
			DefaultTimeoutSeconds:    15,
			MaxConcurrentLaunches:    3,
			DiscoveryEnabled:         true,
			DiscoveryScanOnStart:     true,
			DiscoveryCacheTTLSeconds: 300,
			DiscoveryRequireConfirm:  false,
			AutoAdoptDiscovery:       true,
		},
	}
}

// Dir 返回配置与状态目录（~/.agentquay）。
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agentquay"
	}
	return filepath.Join(home, ".agentquay")
}

// expandHome 展开 "~/" 前缀路径。
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// ConfigFile 配置路径。
func ConfigFile() string { return filepath.Join(Dir(), "config.json") }

// PortFile 端口文件路径（SDK 读取）。
func PortFile() string { return filepath.Join(Dir(), "port") }

// PidFile pid 文件路径。
func PidFile() string { return filepath.Join(Dir(), "agentquay.pid") }

// AuthFile token 钉扎表路径。
func AuthFile() string { return filepath.Join(Dir(), "auth.json") }

// AppsDir 应用注册表目录（每应用一个 JSON，附录 C；§5.8）。
func AppsDir() string { return filepath.Join(Dir(), "apps") }

// Load 加载配置：以默认值为底，合并 config.json（缺失字段保持默认），文件不存在时写出默认配置。
func Load() (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(ConfigFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if werr := saveDefault(); werr != nil {
				return nil, fmt.Errorf("写入默认配置失败: %w", werr)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	// 环境变量覆盖（SDK 内嵌 / 调试场景）
	if lvl := os.Getenv("AGENTQUAY_LOG_LEVEL"); lvl != "" {
		cfg.LogLevel = lvl
	}
	return cfg, nil
}

func saveDefault() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigFile(), data, 0o600)
}

// LogFilePath 返回解析后的日志文件路径。
func (c *Config) LogFilePath() string { return expandHome(c.LogFile) }

// PortFilePath 返回解析后的端口文件路径。
func (c *Config) PortFilePath() string { return expandHome(c.PortFile) }

// ListenHost 返回监听地址（默认仅回环，allowRemoteConnections 时全接口）。
func (c *Config) ListenHost() string {
	if c.AllowRemoteConnections {
		return "0.0.0.0"
	}
	return c.Host
}

// ResolvePort 解析监听端口：固定端口尝试直接监听；autoPort 时从固定端口起探测空闲端口。
// 返回实际可用端口。fixed=true 时若指定端口被占用则直接报错（不自动漂移）。
func ResolvePort(host string, port int, autoPort bool) (int, error) {
	if !autoPort {
		if err := probe(host, port); err != nil {
			return 0, fmt.Errorf("端口 %d 不可用（可设置 autoPort: true 自动选择）: %w", port, err)
		}
		return port, nil
	}
	for p := port; p < port+AutoPortSpan; p++ {
		if err := probe(host, p); err == nil {
			return p, nil
		}
	}
	return 0, fmt.Errorf("端口 %d–%d 均被占用", port, port+AutoPortSpan-1)
}

func probe(host string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}

// ReadPortFile 读取 SDK 发现用的端口文件。
func ReadPortFile() (int, error) {
	data, err := os.ReadFile(expandHome(PortFile()))
	if err != nil {
		return 0, err
	}
	var port int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &port); err != nil || port <= 0 {
		return 0, fmt.Errorf("端口文件内容无效: %q", strings.TrimSpace(string(data)))
	}
	return port, nil
}

// WritePortFile 写入实际监听端口（SDK 侧 port: 0 时自动读取）。
func WritePortFile(port int) error {
	if err := os.MkdirAll(filepath.Dir(expandHome(PortFile())), 0o755); err != nil {
		return err
	}
	return os.WriteFile(expandHome(PortFile()), []byte(fmt.Sprint(port)), 0o644)
}
