package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCommandTimeout(t *testing.T) {
	rc := &RunCommandTool{WorkDir: t.TempDir(), TimeoutSec: 1}
	start := time.Now()
	out, err := rc.Call(context.Background(), map[string]any{"command": "sleep 3"})
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got out=%q err=%v", out, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("expected command to be killed around 1s, took %s", elapsed)
	}
}

func TestRunCommandDefaultTimeoutFallback(t *testing.T) {
	rc := &RunCommandTool{WorkDir: t.TempDir()} // TimeoutSec left unset (0)
	if out, err := rc.Call(context.Background(), map[string]any{"command": "echo ok"}); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("expected quick success with default timeout, got (%q, %v)", out, err)
	}
}

// TestRunCommandTimeoutKillsBackgroundedGrandchild reproduces the bug where
// a shell command backgrounds a subprocess (`cmd &`): killing only the
// direct bash child leaves that grandchild running, and since it inherited
// the stdout/stderr pipe, exec's Wait() blocks until the grandchild exits
// on its own - ignoring the configured timeout entirely. The fix kills the
// whole process group on timeout.
func TestRunCommandTimeoutKillsBackgroundedGrandchild(t *testing.T) {
	rc := &RunCommandTool{WorkDir: t.TempDir(), TimeoutSec: 1}
	start := time.Now()
	out, err := rc.Call(context.Background(), map[string]any{"command": "sleep 10 & sleep 10"})
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got out=%q err=%v", out, err)
	}
	if elapsed > 4*time.Second {
		t.Errorf("expected both the foreground and backgrounded sleep to be killed around 1s, took %s", elapsed)
	}
}

func TestRunCommandContextCancelDoesNotReportAsTimeout(t *testing.T) {
	rc := &RunCommandTool{WorkDir: t.TempDir(), TimeoutSec: 300}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	_, err := rc.Call(ctx, map[string]any{"command": "sleep 1"})
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("cancellation should not be reported as a timeout, got: %v", err)
	}
}
