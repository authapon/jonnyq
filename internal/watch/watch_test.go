package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const pollInterval = 20 * time.Millisecond

func waitForTrigger(t *testing.T, ch <-chan Trigger, timeout time.Duration) Trigger {
	t.Helper()
	select {
	case tr := <-ch:
		return tr
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a trigger")
		return Trigger{}
	}
}

func expectNoTrigger(t *testing.T, ch <-chan Trigger, wait time.Duration) {
	t.Helper()
	select {
	case tr := <-ch:
		t.Fatalf("expected no trigger, got: %+v", tr)
	case <-time.After(wait):
	}
}

func TestWatcherDetectsMagicWordInNewFile(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval)
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\n// AI! add error handling here\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tr := waitForTrigger(t, triggers, 2*time.Second)
	if tr.File != "main.go" {
		t.Errorf("expected file main.go, got %q", tr.File)
	}
	if tr.Line != 3 {
		t.Errorf("expected line 3, got %d", tr.Line)
	}
	if tr.Text != "// AI! add error handling here" {
		t.Errorf("unexpected trigger text: %q", tr.Text)
	}
}

func TestWatcherDoesNotRetriggerUnchangedLine(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval)
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("AI! do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForTrigger(t, triggers, 2*time.Second)

	// Touch the file (new mtime) without changing the trigger line's
	// content - re-saving in an editor without edits is common and must
	// not re-fire the same instruction.
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	expectNoTrigger(t, triggers, 300*time.Millisecond)
}

func TestWatcherFiresAgainOnNewMagicWordLine(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval)
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("AI! first instruction\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := waitForTrigger(t, triggers, 2*time.Second)
	if first.Text != "AI! first instruction" {
		t.Errorf("unexpected first trigger: %q", first.Text)
	}

	if err := os.WriteFile(path, []byte("AI! first instruction\nAI! second instruction\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := waitForTrigger(t, triggers, 2*time.Second)
	if second.Text != "AI! second instruction" {
		t.Errorf("unexpected second trigger: %q", second.Text)
	}
}

func TestWatcherIgnoresDotfilesAndDotDirs(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval)
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("AI! secret instruction\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "COMMIT_EDITMSG"), []byte("AI! git internals\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectNoTrigger(t, triggers, 300*time.Millisecond)
}

func TestWatcherIgnoresNodeModulesAndVendor(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval)
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	for _, sub := range []string{"node_modules", "vendor"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "lib.go"), []byte("AI! ignore me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expectNoTrigger(t, triggers, 300*time.Millisecond)
}

func TestSetMagicWordAppliesLive(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval)
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("TODO! change the word\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectNoTrigger(t, triggers, 200*time.Millisecond)

	w.SetMagicWord("TODO!")
	// Touch the file so the watcher re-scans it under the new word.
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	tr := waitForTrigger(t, triggers, 8*time.Second)
	if tr.Text != "TODO! change the word" {
		t.Errorf("unexpected trigger: %q", tr.Text)
	}
}

func TestStopIsIdempotentAndSafeWithoutStart(t *testing.T) {
	w := New(t.TempDir(), "AI!", pollInterval)
	w.Stop() // never started
	w.Stop() // idempotent

	w2 := New(t.TempDir(), "AI!", pollInterval)
	w2.Start(make(chan Trigger, 1))
	w2.Stop()
	w2.Stop() // idempotent after a real Start/Stop
}

// TestExcludedFileNeverTriggers guards against a runaway feedback loop: a
// consumer that echoes fired triggers back into a log file living inside
// workDir (as the REPL does with its transcript) must not have that log
// file re-scanned, or the echoed magic word would fire again forever.
func TestExcludedFileNeverTriggers(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "AI!", pollInterval, "output.txt")
	triggers := make(chan Trigger, 8)
	w.Start(triggers)
	defer w.Stop()

	path := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(path, []byte("[watchfile] hello.go:3: // AI! add a comment here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectNoTrigger(t, triggers, 300*time.Millisecond)

	// Appending more echoed trigger lines (as a real session would, one per
	// fired trigger) must still never fire.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[watchfile] output.txt:1: [watchfile] hello.go:3: // AI! add a comment here\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	expectNoTrigger(t, triggers, 300*time.Millisecond)
}
