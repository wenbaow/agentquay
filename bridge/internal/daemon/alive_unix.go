//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// IsAlive Unix：signal 0 探测进程存活。
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
