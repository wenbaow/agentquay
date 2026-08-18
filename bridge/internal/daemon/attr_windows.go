//go:build windows

package daemon

import "syscall"

// detachedAttr Windows：DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP，脱离控制台。
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}
