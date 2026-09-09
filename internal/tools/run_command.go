package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"jonnyq/internal/llm"
)

// DefaultCommandTimeoutSec bounds a single run_command call so a hung
// process can't block the REPL forever; Ctrl-C (which cancels ctx) still
// wins earlier. It's the fallback used when TimeoutSec is left unset.
const DefaultCommandTimeoutSec = 300

// RunCommandTool executes a shell command. No confirmation prompt is shown
// (by design, per user request) but commands matching a fixed denylist of
// known-destructive patterns are refused outright. TimeoutSec may be
// changed at runtime (e.g. by the /run_command_timeout slash command); it
// is read fresh on every call.
type RunCommandTool struct {
	WorkDir    string
	TimeoutSec int
}

func (t *RunCommandTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "run_command",
		Description: "Run a shell command in the working directory and return its combined stdout/stderr output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command to run."},
			},
			"required": []string{"command"},
		},
	}
}

func (t *RunCommandTool) Call(ctx context.Context, args map[string]any) (string, error) {
	command, ok := stringArg(args, "command")
	if !ok || command == "" {
		return "", fmt.Errorf("command is required")
	}
	if reason, blocked := checkDenylist(command); blocked {
		return "", fmt.Errorf("refused to run: command %s", reason)
	}

	timeoutSec := t.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = DefaultCommandTimeoutSec
	}
	timeout := time.Duration(timeoutSec) * time.Second

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", command)
	cmd.Dir = t.WorkDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()

	result := out.String()
	if cctx.Err() != nil && ctx.Err() == nil {
		return result, fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("%s\n[exit code: %d]", result, exitErr.ExitCode()), nil
		}
		return result, err
	}
	return result, nil
}
