//go:build darwin

package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agentquay/bridge/internal/apps"
)

// scanPlatform macOS：扫描 /Applications 与 ~/Applications 下的 .app 包，
// 用 PlistBuddy 读取 CFBundleName / CFBundleIdentifier 作为名称与标识。
func scanPlatform(ctx context.Context) []Candidate {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	var cands []Candidate
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			select {
			case <-ctx.Done():
				return cands
			default:
			}
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".app") {
				continue
			}
			bundle := filepath.Join(dir, e.Name())
			name := strings.TrimSuffix(e.Name(), ".app")
			bundleID := ""
			if id := plistValue(bundle, "CFBundleIdentifier"); id != "" {
				bundleID = id
			}
			if n := plistValue(bundle, "CFBundleName"); n != "" {
				name = n
			}
			appID := AppIDFrom(name, bundleID)
			if appID == "" {
				continue
			}
			cands = append(cands, Candidate{
				AppID:   appID,
				AppName: name,
				Launch:  apps.LaunchCommand{ExecPath: bundle}, // launcher 对 .app 走 open -n
			})
		}
	}
	return cands
}

// plistValue 读取 Info.plist 指定键（PlistBuddy 在 macOS 上随系统提供）。
func plistValue(bundle, key string) string {
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	if _, err := os.Stat(plist); err != nil {
		return ""
	}
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :"+key, plist).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
