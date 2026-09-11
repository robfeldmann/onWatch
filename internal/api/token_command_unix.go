//go:build !windows

package api

import (
	"os/exec"
	"syscall"
)

// tokenCommandIsolate runs the shell in its own process group and, when the
// context ends, kills that group so the command's children go with it.
func tokenCommandIsolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
