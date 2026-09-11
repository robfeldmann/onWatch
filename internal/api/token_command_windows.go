//go:build windows

package api

import "os/exec"

// tokenCommandIsolate is a no-op on Windows: exec.CommandContext kills the
// shell and WaitDelay releases the pipe held by any surviving child.
func tokenCommandIsolate(cmd *exec.Cmd) {}
