package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const defaultTokenCommandTimeout = 30 * time.Second

// tokenCommandTimeout is a variable so tests can exercise timeout handling
// without waiting for the production timeout.
var tokenCommandTimeout = defaultTokenCommandTimeout

// RunTokenCommand runs command through the platform shell and returns its
// trimmed standard output. Commands have a 30-second timeout by default.
func RunTokenCommand(ctx context.Context, command string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	commandCtx, cancel := context.WithTimeout(ctx, tokenCommandTimeout)
	defer cancel()

	var shell string
	var args []string
	if runtime.GOOS == "windows" {
		shell = "cmd"
		args = []string{"/C", command}
	} else {
		shell = "/bin/sh"
		args = []string{"-c", command}
	}

	cmd := exec.CommandContext(commandCtx, shell, args...)
	// The shell's children (the real command) inherit the stdout pipe, so
	// killing only the shell on timeout would leave Output() waiting for them.
	// Kill the whole process group where the platform allows it, and stop
	// waiting on the pipe shortly after the kill regardless.
	tokenCommandIsolate(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("token command timed out after %s", tokenCommandTimeout)
		}
		if errors.Is(commandCtx.Err(), context.Canceled) {
			return "", fmt.Errorf("token command canceled: %w", commandCtx.Err())
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("token command failed: %w: %s", err, detail)
		}
		return "", fmt.Errorf("token command failed: %w", err)
	}

	token := strings.TrimSpace(string(stdout))
	if token == "" {
		return "", fmt.Errorf("token command returned empty output")
	}
	return token, nil
}

// TokenCommandRefresher adapts a token command to an agent token refresh hook.
// A failed command leaves the agent's previous token unchanged.
func TokenCommandRefresher(command string, logger *slog.Logger) func() string {
	if logger == nil {
		logger = slog.Default()
	}
	return func() string {
		token, err := RunTokenCommand(context.Background(), command)
		if err != nil {
			logger.Warn("token command failed", "error", err)
			return ""
		}
		return token
	}
}
