package api

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunTokenCommandSuccess(t *testing.T) {
	token, err := RunTokenCommand(context.Background(), "echo tok")
	if err != nil {
		t.Fatalf("RunTokenCommand() error = %v", err)
	}
	if token != "tok" {
		t.Fatalf("RunTokenCommand() = %q, want %q", token, "tok")
	}
}

func TestRunTokenCommandTrimsTrailingNewlines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printf is a Unix shell command")
	}
	token, err := RunTokenCommand(context.Background(), `printf '  tok  \n\n'`)
	if err != nil {
		t.Fatalf("RunTokenCommand() error = %v", err)
	}
	if token != "tok" {
		t.Fatalf("RunTokenCommand() = %q, want %q", token, "tok")
	}
}

func TestRunTokenCommandNonZeroExit(t *testing.T) {
	command := "exit 7"
	if runtime.GOOS == "windows" {
		command = "exit /b 7"
	}
	_, err := RunTokenCommand(context.Background(), command)
	if err == nil {
		t.Fatal("RunTokenCommand() error = nil, want non-zero exit error")
	}
	if !strings.Contains(err.Error(), "token command failed") {
		t.Fatalf("RunTokenCommand() error = %q, want command failure", err)
	}
}

func TestRunTokenCommandEmptyOutput(t *testing.T) {
	_, err := RunTokenCommand(context.Background(), "")
	if err == nil {
		t.Fatal("RunTokenCommand() error = nil, want empty output error")
	}
	if !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("RunTokenCommand() error = %q, want empty output", err)
	}
}

func TestRunTokenCommandTimeout(t *testing.T) {
	originalTimeout := tokenCommandTimeout
	tokenCommandTimeout = 20 * time.Millisecond
	t.Cleanup(func() { tokenCommandTimeout = originalTimeout })

	command := "sleep 1"
	if runtime.GOOS == "windows" {
		command = "ping 127.0.0.1 -n 2 >NUL"
	}
	started := time.Now()
	_, err := RunTokenCommand(context.Background(), command)
	if err == nil {
		t.Fatal("RunTokenCommand() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("RunTokenCommand() error = %q, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %s, want less than one second", elapsed)
	}
}
