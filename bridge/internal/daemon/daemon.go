// Package daemon 提供后台化运行（--daemon）与 pid 文件管理。
// 跨平台实现：重新拉起自身子进程（Windows 用 DETACHED_PROCESS，Unix 用 setsid）。
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// SpawnDetached 以脱离终端的方式重启自身（agentquay serve），输出重定向到 logFile。
// 返回子进程 pid。
func SpawnDetached(logFile string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = detachedAttr()
	// 子进程标准输出/错误重定向到日志文件
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	} else {
		devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		cmd.Stdout = devnull
		cmd.Stderr = devnull
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动后台进程失败: %w", err)
	}
	return cmd.Process.Pid, nil
}

// StartDetached 以脱离终端的方式启动任意应用可执行文件（启动注册表拉起用，§5.8）。
// argv 直传（exec 内部自行处理引号），绝不经过 shell，杜绝注入；输出丢弃。
func StartDetached(exe string, args []string, cwd string) (int, error) {
	if _, err := os.Stat(exe); err != nil {
		return 0, fmt.Errorf("可执行文件不存在: %s", exe)
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detachedAttr()
	if cwd != "" {
		cmd.Dir = cwd
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return 0, err
	}
	defer devnull.Close()
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动 %s 失败: %w", exe, err)
	}
	return cmd.Process.Pid, nil
}

// WritePid 写入 pid 文件。
func WritePid(path string) error {
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// ReadPid 读取 pid 文件。
func ReadPid(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, errors.New("pid 文件内容无效")
	}
	return pid, nil
}

// Kill 终止进程。
func Kill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
