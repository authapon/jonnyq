package repl

import (
	"context"
	"path/filepath"
	"testing"

	"jonnyq/internal/agent"
	"jonnyq/internal/config"
	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

type noopProvider struct{}

func (noopProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent)
	close(ch)
	return ch, nil
}

func (noopProvider) ListModels(ctx context.Context) ([]string, error) { return nil, nil }

func newTestREPL(t *testing.T) *REPL {
	t.Helper()
	dir := t.TempDir()
	w, err := ui.New(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	cfg := &config.Config{Model: "test-model"}
	ag := agent.New(noopProvider{}, cfg.Model, tools.NewRegistry(), false, 0, 0, w, nil, filepath.Join(dir, ".context"))
	rc := &tools.RunCommandTool{WorkDir: dir}
	return New(cfg, ag, w, rc)
}

func TestExitAndByeBothExit(t *testing.T) {
	for _, cmd := range []string{"/exit", "/bye"} {
		r := newTestREPL(t)
		exit, err := r.handleSlash(context.Background(), cmd)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", cmd, err)
		}
		if !exit {
			t.Errorf("%s: expected exit=true", cmd)
		}
	}
}
