//go:build !windows

package daemon

import "syscall"

// detachedAttr Unix：setsid 脱离会话与终端。
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
