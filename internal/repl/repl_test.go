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
	"jonnyq/internal/watch"
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

	cfg := &config.Config{Model: "test-model", WatchMagicWord: "AI!", WatchPollIntervalSec: 1}
	ag := agent.New(noopProvider{}, cfg.Model, tools.NewRegistry(), false, 0, 0, w, nil, filepath.Join(dir, ".context"))
	rc := &tools.RunCommandTool{WorkDir: dir}
	r := New(cfg, ag, w, rc)
	t.Cleanup(func() {
		if r.watcher != nil {
			r.watcher.Stop()
		}
	})
	return r
}

func TestPromptStringShowsModelAndContextSize(t *testing.T) {
	r := newTestREPL(t)
	r.Cfg.Model = "ornith-1.5-35b-a3b"
	r.Cfg.ContextSize = 100000

	got := r.promptString()
	want := "ornith-1.5-35b-a3b (100000 token) > "
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPromptStringPlainWhenModelUnset(t *testing.T) {
	r := newTestREPL(t)
	r.Cfg.Model = ""

	if got := r.promptString(); got != "> " {
		t.Errorf("got %q, want %q", got, "> ")
	}
}

func TestToggleWatchStartAndStop(t *testing.T) {
	r := newTestREPL(t)
	triggers := make(chan watch.Trigger, 1)

	r.toggleWatch("", triggers)
	if r.watcher == nil {
		t.Fatal("expected watcher to be started")
	}
	if got := r.watcher.MagicWord(); got != r.Cfg.WatchMagicWord {
		t.Errorf("expected magic word %q, got %q", r.Cfg.WatchMagicWord, got)
	}

	// A bare command again while already watching toggles it off.
	r.toggleWatch("", triggers)
	if r.watcher != nil {
		t.Error("expected watcher to be stopped")
	}
}

func TestToggleWatchSetsWordAndStartsWithArgument(t *testing.T) {
	r := newTestREPL(t)
	triggers := make(chan watch.Trigger, 1)

	r.toggleWatch("TODO!", triggers)
	if r.watcher == nil {
		t.Fatal("expected watcher to be started")
	}
	if got := r.watcher.MagicWord(); got != "TODO!" {
		t.Errorf("expected magic word TODO!, got %q", got)
	}
	if r.Cfg.WatchMagicWord != "TODO!" {
		t.Errorf("expected config magic word to be updated, got %q", r.Cfg.WatchMagicWord)
	}
}

func TestToggleWatchUpdatesWordLiveWithoutRestarting(t *testing.T) {
	r := newTestREPL(t)
	triggers := make(chan watch.Trigger, 1)

	r.toggleWatch("AI!", triggers)
	first := r.watcher
	if first == nil {
		t.Fatal("expected watcher to be started")
	}

	r.toggleWatch("FIXME!", triggers)
	if r.watcher != first {
		t.Error("expected the same watcher instance to be reused, not restarted")
	}
	if got := r.watcher.MagicWord(); got != "FIXME!" {
		t.Errorf("expected magic word FIXME!, got %q", got)
	}
}

func TestToggleWatchOffStopsExplicitly(t *testing.T) {
	r := newTestREPL(t)
	triggers := make(chan watch.Trigger, 1)

	r.toggleWatch("AI!", triggers)
	if r.watcher == nil {
		t.Fatal("expected watcher to be started")
	}
	r.toggleWatch("off", triggers)
	if r.watcher != nil {
		t.Error("expected watcher to be stopped by 'off'")
	}
}

func TestToggleWatchOffWhenNotWatchingIsHarmless(t *testing.T) {
	r := newTestREPL(t)
	triggers := make(chan watch.Trigger, 1)
	r.toggleWatch("off", triggers) // must not panic
	if r.watcher != nil {
		t.Error("expected no watcher")
	}
}

func TestExitAndByeBothExit(t *testing.T) {
	for _, cmd := range []string{"/exit", "/bye"} {
		r := newTestREPL(t)
		triggers := make(chan watch.Trigger, 1)
		exit, err := r.handleSlash(context.Background(), cmd, triggers)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", cmd, err)
		}
		if !exit {
			t.Errorf("%s: expected exit=true", cmd)
		}
	}
}
