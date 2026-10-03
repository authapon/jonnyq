package ui

import (
	"os"
	"path/filepath"
	"regexp"
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

// timestamp matches the "(YYYY-MM-DD HH:MM:SS)" suffix startSection appends
// to every header, so tests don't have to predict the exact second printed.
const timestamp = `\(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\)`

func TestSectionHeaderShownOncePerRun(t *testing.T) {
	w, path := newTestWriter(t)
	w.Thinking("chunk one ")
	w.Thinking("chunk two")
	log := readLog(t, path)

	if got := strings.Count(log, "Thinking "); got != 1 {
		t.Errorf("expected exactly one 'Thinking' header across a contiguous run, got %d in: %q", got, log)
	}
	re := regexp.MustCompile(`Thinking ` + timestamp + `\nchunk one chunk two`)
	if !re.MatchString(log) {
		t.Errorf("expected header followed by joined chunks, got: %q", log)
	}
}

func TestSectionHeaderRepeatsOnKindChange(t *testing.T) {
	w, path := newTestWriter(t)
	w.Thinking("thinking...")
	w.Answer("the answer")
	log := readLog(t, path)

	if !strings.Contains(log, "Thinking ") || !strings.Contains(log, "Answer ") {
		t.Fatalf("expected both headers present, got: %q", log)
	}
	// A blank line must separate the two sections.
	re := regexp.MustCompile(`\n\nAnswer ` + timestamp + `\n`)
	if !re.MatchString(log) {
		t.Errorf("expected a blank line before the Answer header, got: %q", log)
	}
}

func TestNewTurnResetsSectionEvenForSameKind(t *testing.T) {
	w, path := newTestWriter(t)
	w.Answer("first turn's answer")
	w.NewTurn()
	w.Answer("second turn's answer")
	log := readLog(t, path)

	if got := strings.Count(log, "Answer "); got != 2 {
		t.Errorf("expected a fresh 'Answer' header after NewTurn even though the kind repeats, got %d headers in: %q", got, log)
	}
}

func TestToolCallGetsHeaderAndOwnLine(t *testing.T) {
	w, path := newTestWriter(t)
	w.ToolCall(`read_file({"path":"a.txt"})`)
	log := readLog(t, path)

	re := regexp.MustCompile(`Tool call ` + timestamp + "\nread_file\\(\\{\"path\":\"a.txt\"\\}\\)\n")
	if !re.MatchString(log) {
		t.Errorf("expected tool call header followed by the call on its own line, got: %q", log)
	}
}

func TestSectionHeaderIncludesTimestamp(t *testing.T) {
	w, path := newTestWriter(t)
	w.Answer("hi")
	log := readLog(t, path)

	re := regexp.MustCompile(`Answer ` + timestamp)
	if !re.MatchString(log) {
		t.Errorf("expected the section header to include a (YYYY-MM-DD HH:MM:SS) timestamp, got: %q", log)
	}
}

func TestSectionHeaderShowsContextWhenConfigured(t *testing.T) {
	w, path := newTestWriter(t)
	w.ContextFn = func() ContextStatus { return ContextStatus{Used: 6300, Total: 16348} }
	w.Thinking("hmm")
	w.ToolCall("x()")
	log := readLog(t, path)

	re := regexp.MustCompile(`Thinking ` + timestamp + ` \| ctx 6,300/16,348 \(38%\) left 10,048\n`)
	if !re.MatchString(log) {
		t.Errorf("expected ctx readout on the Thinking header, got: %q", log)
	}
	if !strings.Contains(log, "Tool call ") || strings.Count(log, "| ctx ") != 2 {
		t.Errorf("expected every section header to carry the readout, got: %q", log)
	}
}

func TestSectionHeaderUnchangedWithoutContextFn(t *testing.T) {
	w, path := newTestWriter(t)
	w.Answer("hi")
	if strings.Contains(readLog(t, path), "ctx") {
		t.Errorf("expected no ctx readout without ContextFn")
	}
}

func TestContextStatusFormattingAndColors(t *testing.T) {
	cs := ContextStatus{Used: 1234567, Total: 2000000}
	if got := cs.String(); got != "ctx 1,234,567/2,000,000 (61%) left 765,433" {
		t.Errorf("unexpected format: %q", got)
	}
	if got := (ContextStatus{Used: 10, Total: 100, Estimated: true}).String(); got != "ctx ~10/100 (~10%) left ~90" {
		t.Errorf("unexpected estimated format: %q", got)
	}
	over := ContextStatus{Used: 150, Total: 100}
	if over.Left() != 0 {
		t.Errorf("Left must not go negative, got %d", over.Left())
	}
	cases := []struct {
		used int
		want string
	}{{10, ColorGray}, {59, ColorGray}, {60, ColorYellow}, {84, ColorYellow}, {85, ColorRed}, {150, ColorRed}}
	for _, c := range cases {
		if got := (ContextStatus{Used: c.used, Total: 100}).Color(); got != c.want {
			t.Errorf("used=%d: color %q, want %q", c.used, got, c.want)
		}
	}
}
