package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	w, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, path
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSectionHeaderShownOncePerRun(t *testing.T) {
	w, path := newTestWriter(t)
	w.Thinking("chunk one ")
	w.Thinking("chunk two")
	log := readLog(t, path)

	if got := strings.Count(log, "Thinking"); got != 1 {
		t.Errorf("expected exactly one 'Thinking' header across a contiguous run, got %d in: %q", got, log)
	}
	if !strings.Contains(log, "Thinking\nchunk one chunk two") {
		t.Errorf("expected header followed by joined chunks, got: %q", log)
	}
}

func TestSectionHeaderRepeatsOnKindChange(t *testing.T) {
	w, path := newTestWriter(t)
	w.Thinking("thinking...")
	w.Answer("the answer")
	log := readLog(t, path)

	if !strings.Contains(log, "Thinking") || !strings.Contains(log, "Answer") {
		t.Fatalf("expected both headers present, got: %q", log)
	}
	// A blank line must separate the two sections.
	if !strings.Contains(log, "\n\nAnswer\n") {
		t.Errorf("expected a blank line before the Answer header, got: %q", log)
	}
}

func TestNewTurnResetsSectionEvenForSameKind(t *testing.T) {
	w, path := newTestWriter(t)
	w.Answer("first turn's answer")
	w.NewTurn()
	w.Answer("second turn's answer")
	log := readLog(t, path)

	if got := strings.Count(log, "Answer"); got != 2 {
		t.Errorf("expected a fresh 'Answer' header after NewTurn even though the kind repeats, got %d headers in: %q", got, log)
	}
}

func TestToolCallGetsHeaderAndOwnLine(t *testing.T) {
	w, path := newTestWriter(t)
	w.ToolCall(`read_file({"path":"a.txt"})`)
	log := readLog(t, path)

	if !strings.Contains(log, "Tool call\nread_file({\"path\":\"a.txt\"})\n") {
		t.Errorf("expected tool call header followed by the call on its own line, got: %q", log)
	}
}
