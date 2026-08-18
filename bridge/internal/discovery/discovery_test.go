package discovery

import (
	"context"
	"testing"
	"time"

	"agentquay/bridge/internal/apps"
)

func TestAppIDFrom(t *testing.T) {
	cases := []struct{ name, exec, want string }{
		{"微信", `D:\Program Files\Tencent\Weixin\Weixin.exe`, "weixin"},
		{"Google Chrome", `C:\Program Files\Google\Chrome\Application\chrome.exe`, "chrome"},
		{"记事本", `C:\Windows\System32\notepad.exe`, "notepad"},
		{"微信开发者工具", `E:\Program Files\微信web开发者工具\微信开发者工具.exe`, "app-"}, // 全中文→短哈希兜底，仅验证前缀
		{"Firefox", "/usr/lib/firefox/firefox", "firefox"},
	}
	for _, c := range cases {
		got := AppIDFrom(c.name, c.exec)
		if c.want == "app-" {
			if len(got) != 10 || got[:4] != "app-" { // app- + 6 hex
				t.Errorf("AppIDFrom(%q, %q) = %q，期望 app-<hex6> 形式", c.name, c.exec, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("AppIDFrom(%q, %q) = %q, want %q", c.name, c.exec, got, c.want)
		}
	}
	// 合规校验
	for _, c := range cases {
		got := AppIDFrom(c.name, c.exec)
		for _, r := range got {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				t.Errorf("AppID %q 含非法字符 %q", got, r)
			}
		}
		if len(got) > 48 {
			t.Errorf("AppID %q 超长", got)
		}
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"wechat 3.0":  "wechat-3-0",
		"WeChat..App": "wechat-app",
		"--leading--": "leading",
		"ABC":         "abc",
		"":            "",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScoreByName(t *testing.T) {
	cases := []struct {
		name, q string
		want    int
	}{
		{"微信", "微信", 100},
		{"微信", "微", 90},
		{"企业微信", "微信", 80},
		{"WeChat", "wechat", 100}, // 大小写不敏感
		{"Chrome", "firefox", 0},
	}
	for _, c := range cases {
		if got := scoreByName(c.name, c.q); got != c.want {
			t.Errorf("scoreByName(%q, %q) = %d, want %d", c.name, c.q, got, c.want)
		}
	}
}

func TestSearchFiltersAndRanks(t *testing.T) {
	s := New(true, nil)
	// 白盒测试：query 非空时零分结果被过滤，匹配应用按相关度靠前。
	cs := []Candidate{
		{AppID: "chrome", AppName: "Google Chrome", Launch: apps.LaunchCommand{ExecPath: "chrome.exe"}},
		{AppID: "firefox", AppName: "Mozilla Firefox", Launch: apps.LaunchCommand{ExecPath: "firefox.exe"}},
		{AppID: "wechat", AppName: "微信", Launch: apps.LaunchCommand{ExecPath: "Weixin.exe"}},
	}
	ranked := rankByQuery(cs, "chrome")
	if ranked[0].AppID != "chrome" || ranked[0].Confidence != 80 {
		t.Errorf("包含匹配应为 80 分: %+v", ranked[0])
	}
	if s := s.enabled; !s {
		t.Error("默认应启用扫描")
	}

	// appId 匹配（大小写不敏感，fallback 70 分）
	ranked2 := rankByQuery(cs, "WECHAT")
	if ranked2[2].AppID != "wechat" || ranked2[2].Confidence != 70 {
		t.Errorf("appId 匹配应为 70 分: %+v", ranked2[2])
	}

	// 过滤：Search 路径（非空 query）应只保留 Confidence>0
	filtered := make([]Candidate, 0)
	for _, c := range ranked {
		if c.Confidence > 0 {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) != 1 || filtered[0].AppID != "chrome" {
		t.Errorf("过滤后应只剩 chrome: %+v", filtered)
	}
}

func scanDisabledScanner() *Scanner {
	s := New(false, nil)
	return s
}

func TestScannerDisabled(t *testing.T) {
	s := scanDisabledScanner()
	if s.Enabled() {
		t.Error("禁用后 Enabled 应为 false")
	}
	if got := s.Search(context.Background(), "chrome", 5); got != nil {
		t.Error("禁用后 Search 应返回 nil")
	}
}

// TestScannerCache 验证缓存：Refresh 后缓存新鲜、重复查询走内存（不触发系统扫描）。
func TestScannerCache(t *testing.T) {
	s := New(true, nil)
	s.SetCacheTTL(10 * time.Minute)

	// 未扫描：缓存空、不新鲜
	if s.Len() != 0 {
		t.Errorf("初始缓存应为空: %d", s.Len())
	}

	// 手动刷新（真实系统扫描，Windows 上 1-2s）
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if s.Len() == 0 {
		t.Skip("本机未扫描到任何应用（无开始菜单项/desktop）")
	}

	// 缓存新鲜
	s.mu.Lock()
	fresh := s.freshLocked()
	s.mu.Unlock()
	if !fresh {
		t.Error("Refresh 后缓存应新鲜")
	}

	// 缓存命中：搜索应即时返回（不重新扫描）
	start := time.Now()
	got := s.Search(context.Background(), "chrome", 3)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("缓存命中应 <500ms，实际 %v", elapsed)
	}
	if got == nil {
		t.Error("搜索不应为 nil")
	}

	// TTL 过期后重扫恢复新鲜
	s.SetCacheTTL(1 * time.Millisecond)
	s.mu.Lock()
	s.cachedAt = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	s.Search(context.Background(), "chrome", 3) // 触发重新扫描
	s.mu.Lock()
	fresh = s.freshLocked()
	s.mu.Unlock()
	if !fresh {
		t.Error("重新扫描后应恢复新鲜")
	}
}
