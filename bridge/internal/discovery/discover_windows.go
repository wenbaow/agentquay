//go:build windows

package discovery

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"

	"agentquay/bridge/internal/apps"
)

// scanPlatform Windows：开始菜单 .lnk（COM 解析目标）+ App Paths 注册表。
// PowerShell 结果写入 UTF-8 临时文件（避免控制台/管道编码丢失中文），Go 读取文件解析。
// 文件内容：TSV（tab 分隔）kind \t name \t exe \t args \t cwd，每行一条。
func scanPlatform(ctx context.Context) []Candidate {
	tmp, err := os.CreateTemp("", "agentquay-scan-*.txt")
	if err != nil {
		return nil
	}
	tmpPath := tmp.Name()
	tmp.Close()
	_ = os.Remove(tmpPath) // 由 PowerShell 重新创建

	// 注意：PowerShell 脚本内不使用反引号转义（Go 原始字符串冲突），
	// tab 用 [char]9 生成，行用数组 join 拼接，' 与 " 分离避免嵌套问题。
	script := `
$tab = [char]9
$out = New-Object System.Collections.Generic.List[string]
$dirs = @(
  "$env:ProgramData\Microsoft\Windows\Start Menu\Programs",
  "$env:APPDATA\Microsoft\Windows\Start Menu\Programs"
)
$shell = New-Object -ComObject WScript.Shell
foreach ($d in $dirs) {
  if (-not (Test-Path $d)) { continue }
  Get-ChildItem -Path $d -Recurse -Include *.lnk,*.url -File -ErrorAction SilentlyContinue | ForEach-Object {
    try {
      $s = $shell.CreateShortcut($_.FullName)
      $t = $s.TargetPath
      if ($t -and (Test-Path $t) -and ($t -match '\.exe$')) {
        $out.Add((@('lnk', $_.BaseName, $t, $s.Arguments, $s.WorkingDirectory) -join $tab))
      }
    } catch {}
  }
}
$keys = @(
  "HKLM:\Software\Microsoft\Windows\CurrentVersion\App Paths",
  "HKCU:\Software\Microsoft\Windows\CurrentVersion\App Paths"
)
foreach ($k in $keys) {
  if (Test-Path $k) {
    Get-ChildItem $k -ErrorAction SilentlyContinue | ForEach-Object {
      $p = (Get-ItemProperty $_.PSPath -ErrorAction SilentlyContinue).'(default)'
      if ($p -and (Test-Path $p) -and ($p -match '\.exe$')) {
        $n = [System.IO.Path]::GetFileNameWithoutExtension($_.PSChildName)
        $out.Add((@('app', $n, $p, '', '') -join $tab))
      }
    }
  }
}
[System.IO.File]::WriteAllLines($env:AGENTQUAY_SCAN_OUT, $out, [System.Text.Encoding]::UTF8)
`
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "AGENTQUAY_SCAN_OUT="+tmpPath)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpPath)
		return nil
	}

	cands := make([]Candidate, 0, 128)
	seen := make(map[string]bool, 128)
	f, err := os.Open(tmpPath)
	_ = os.Remove(tmpPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		kind, name, exe := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if name == "" || exe == "" {
			continue
		}
		appID := AppIDFrom(name, exe)
		if appID == "" || seen[appID] {
			continue
		}
		seen[appID] = true
		var args, cwd string
		if len(parts) >= 4 {
			args = strings.TrimSpace(parts[3])
		}
		if len(parts) >= 5 {
			cwd = strings.TrimSpace(parts[4])
		}
		lc := apps.LaunchCommand{ExecPath: exe, Cwd: cwd}
		if kind == "lnk" && args != "" {
			lc.Args = parseArgs(args)
		}
		cands = append(cands, Candidate{
			AppID:   appID,
			AppName: name,
			Launch:  lc,
		})
	}
	return cands
}

// parseArgs 简单切分快捷方式参数（按空格，忽略空；不处理引号嵌套复杂场景）。
func parseArgs(s string) []string {
	return strings.Fields(s)
}
