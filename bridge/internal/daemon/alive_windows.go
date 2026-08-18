//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
)

// IsAlive Windows：os.FindProcess 无法判断存活，用 tasklist 探测。
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
