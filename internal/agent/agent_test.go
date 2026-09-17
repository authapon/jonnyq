package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// sequenceProvider replays one canned response per call to Chat, so the
// tool-calling loop can be tested without a real model or network.
type sequenceProvider struct {
	calls     int
	responses [][]llm.ChatEvent
}

func (p *sequenceProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	idx := p.calls
	p.calls++
	events := p.responses[idx]
	ch := make(chan llm.ChatEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (p *sequenceProvider) ListModels(ctx context.Context) ([]string, error) { return nil, nil }

// echoTool is a minimal tools.Tool used to verify the agent executes
// requested tool calls and feeds their results back to the provider.
type echoTool struct{ called bool }

func (t *echoTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "echo", Description: "echoes text", Parameters: map[string]any{"type": "object"}}
}

func (t *echoTool) Call(ctx context.Context, args map[string]any) (string, error) {
	t.called = true
	return "echoed: " + args["text"].(string), nil
}

func newTestAgent(t *testing.T, responses [][]llm.ChatEvent) (*Agent, *echoTool, string) {
	t.Helper()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "output.txt")
	w, err := ui.New(outFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	reg := tools.NewRegistry()
	et := &echoTool{}
	reg.Register(et)

	p := &sequenceProvider{responses: responses}
	ctxFile := filepath.Join(dir, ".context")
	a := New(p, "test-model", reg, false, 0, 0, w, nil, ctxFile)
	return a, et, outFile
}

func TestRunTurnExecutesToolCallThenFinalAnswer(t *testing.T) {
	responses := [][]llm.ChatEvent{
		{
			{Kind: llm.EventToolCalls, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: `{"text":"hi"}`}}},
			{Kind: llm.EventDone, Usage: llm.Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6, HasTiming: true}},
		},
		{
			{Kind: llm.EventContent, Delta: "all done"},
			{Kind: llm.EventDone, Usage: llm.Usage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10, HasTiming: true}},
		},
	}
	a, et, outFile := newTestAgent(t, responses)

	if err := a.RunTurn(context.Background(), "please echo hi"); err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if !et.called {
		t.Errorf("expected echo tool to have been called")
	}

	var sawToolResult, sawFinal bool
	for _, m := range a.History {
		if m.Role == llm.RoleTool && m.Content == "echoed: hi" {
			sawToolResult = true
		}
		if m.Role == llm.RoleAssistant && m.Content == "all done" {
			sawFinal = true
		}
	}
	if !sawToolResult {
		t.Errorf("history missing tool result: %+v", a.History)
	}
	if !sawFinal {
		t.Errorf("history missing final assistant answer: %+v", a.History)
	}

	logData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	// Section headers carry a "(YYYY-MM-DD HH:MM:SS)" timestamp appended by
	// ui.Writer, so match around it instead of an exact string.
	timestamp := `\(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\)`
	if !regexp.MustCompile(`Tool call ` + timestamp + `\necho\(\{"text":"hi"\}\)`).MatchString(log) {
		t.Errorf("expected tool call section header + call to be logged, got: %s", log)
	}
	if !regexp.MustCompile(`Answer ` + timestamp + `\nall done`).MatchString(log) {
		t.Errorf("expected answer section header + text to be logged, got: %s", log)
	}
	if !regexp.MustCompile(`\nTool call `+timestamp).MatchString(log) || !regexp.MustCompile(`\n\nAnswer `+timestamp).MatchString(log) {
		t.Errorf("expected blank-line-separated section headers in log, got: %s", log)
	}
	if !strings.Contains(log, "token_in=13") || !strings.Contains(log, "token_out=3") || !strings.Contains(log, "total_token=16") {
		t.Errorf("expected aggregated usage across both provider calls, got: %s", log)
	}

	ctxData, err := os.ReadFile(a.ContextFile)
	if err != nil {
		t.Fatalf("expected .context file to be written: %v", err)
	}
	if !strings.Contains(string(ctxData), "please echo hi") {
		t.Errorf(".context missing prompt: %s", ctxData)
	}
}

// TestRunTurnDetectsRepetitionLoopAndCutsShort reproduces a real /autocoding
// failure: the model alternated between two near-identical sentences
// indefinitely, in plain content text, without ever calling a tool. Before
// this fix, RunTurn had no way to notice and would just keep printing and
// accumulating the repeated text as if it were a normal answer.
func TestRunTurnDetectsRepetitionLoopAndCutsShort(t *testing.T) {
	sentenceA := "Wait, I'll try to use `golang:1.23-bullseye` in the Dockerfile to see if it resolves any dependency issues, and I'll also ensure `go.mod` is correctly set.\n\n"
	sentenceB := "Actually, I'll just try to fix `go.mod` one more time very carefully.\n\n"
	var events []llm.ChatEvent
	for i := 0; i < 6; i++ {
		events = append(events,
			llm.ChatEvent{Kind: llm.EventContent, Delta: sentenceA},
			llm.ChatEvent{Kind: llm.EventContent, Delta: sentenceB},
		)
	}
	events = append(events, llm.ChatEvent{Kind: llm.EventDone})

	a, _, outFile := newTestAgent(t, [][]llm.ChatEvent{events})

	if err := a.RunTurn(context.Background(), "fix the docker build"); err != nil {
		t.Fatalf("RunTurn should recover from a detected loop without erroring, got: %v", err)
	}

	if len(a.History) == 0 {
		t.Fatal("expected an assistant entry in history")
	}
	last := a.History[len(a.History)-1]
	if last.Role != llm.RoleAssistant {
		t.Fatalf("expected the last history entry to be from the assistant, got role %q", last.Role)
	}
	if strings.Contains(last.Content, "bullseye") {
		t.Errorf("expected the repeated garbage NOT to be stored verbatim in history, got: %s", last.Content)
	}
	if !strings.Contains(last.Content, "cut short") {
		t.Errorf("expected a clear cut-short marker in history, got: %s", last.Content)
	}

	logData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "repetitive output detected") {
		t.Errorf("expected a user-visible notice about the detected loop, got: %s", logData)
	}
}

func TestRunTurnNoModelSet(t *testing.T) {
	a, _, _ := newTestAgent(t, nil)
	a.Model = ""
	if err := a.RunTurn(context.Background(), "hi"); err == nil {
		t.Error("expected error when no model is set")
	}
}

func TestCodingModeAddsStrictPromptOnlyWhenSet(t *testing.T) {
	a, _, _ := newTestAgent(t, nil)

	plain := a.buildSystemMessage()
	if strings.Contains(plain.Content, "AUTONOMOUS CODING MODE") {
		t.Errorf("system message should not include coding-mode rules by default, got: %s", plain.Content)
	}

	a.CodingMode = true
	strict := a.buildSystemMessage()
	if !strings.Contains(strict.Content, "AUTONOMOUS CODING MODE") {
		t.Errorf("expected coding-mode rules when CodingMode is set, got: %s", strict.Content)
	}
	if !strings.Contains(strict.Content, "run_command in THIS SAME turn") {
		t.Errorf("expected the verify-before-done rule to be present, got: %s", strict.Content)
	}
}

func TestSystemMessageAlwaysIncludesDecisiveThinkingDirective(t *testing.T) {
	a, _, _ := newTestAgent(t, nil)

	plain := a.buildSystemMessage()
	if !strings.Contains(plain.Content, "Think and act decisively") {
		t.Errorf("expected the decisive-thinking directive in plain chat mode, got: %s", plain.Content)
	}

	a.CodingMode = true
	coding := a.buildSystemMessage()
	if !strings.Contains(coding.Content, "Think and act decisively") {
		t.Errorf("expected the decisive-thinking directive in coding mode too, got: %s", coding.Content)
	}
}
