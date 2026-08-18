//go:build linux

package discovery

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	"agentquay/bridge/internal/apps"
)

// scanPlatform Linux：解析 /usr/share/applications 等目录下的 .desktop 文件。
// 跳过 Hidden/NoDisplay，Exec 去除字段码（%f/%F/%u/%U/%i/%c/%k）后取第一个作为可执行文件。
func scanPlatform(ctx context.Context) []Candidate {
	dirs := []string{
		"/usr/share/applications",
		"/usr/local/share/applications",
		"/var/lib/flatpak/exports/share/applications",
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".local/share/applications"),
			filepath.Join(home, ".local/share/flatpak/exports/share/applications"),
		)
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
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			if c, ok := parseDesktop(filepath.Join(dir, e.Name())); ok {
				cands = append(cands, c)
			}
		}
	}
	return cands
}

// parseDesktop 解析单个 .desktop 文件（仅 [Desktop Entry] 节）。
func parseDesktop(path string) (Candidate, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Candidate{}, false
	}
	defer f.Close()

	name, execLine, hidden, noDisplay := "", "", false, false
	inEntry := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "Type":
			if val != "Application" {
				return Candidate{}, false
			}
		case "Name":
			if name == "" {
				name = val
			}
		case "Exec":
			execLine = val
		case "Hidden":
			hidden = val == "true"
		case "NoDisplay":
			noDisplay = val == "true"
		case "Terminal":
			if val == "true" {
				return Candidate{}, false // 终端应用不适合拉起
			}
		}
	}
	if name == "" || execLine == "" || hidden || noDisplay {
		return Candidate{}, false
	}

	exe, args := parseExec(execLine)
	appID := AppIDFrom(name, exe)
	if appID == "" {
		return Candidate{}, false
	}
	return Candidate{
		AppID:   appID,
		AppName: name,
		Launch:  apps.LaunchCommand{ExecPath: exe, Args: args},
	}, true
}

// parseExec 解析 Exec= 行：去除字段码与引号，返回可执行文件与剩余参数。
func parseExec(line string) (string, []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	var exe string
	var args []string
	for i, f := range fields {
		f = strings.Trim(f, `"'`)
		f = stripFieldCode(f)
		if f == "" {
			continue
		}
		if i == 0 {
			exe = f
		} else {
			args = append(args, f)
		}
	}
	return exe, args
}

// stripFieldCode 移除 .desktop Exec 字段码（%f %F %u %U %i %c %k 等）。
func stripFieldCode(s string) string {
	if strings.HasPrefix(s, "%") && len(s) >= 2 && !strings.HasPrefix(s, "%%") {
		return ""
	}
	return s
}
