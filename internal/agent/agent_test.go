package agent

import (
	"context"
	"os"
	"path/filepath"
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
	a := New(p, "test-model", reg, false, 0, w, nil, ctxFile)
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
	if !strings.Contains(log, "[tool] echo(") {
		t.Errorf("expected tool call to be logged, got: %s", log)
	}
	if !strings.Contains(log, "all done") {
		t.Errorf("expected final answer to be logged, got: %s", log)
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

func TestRunTurnNoModelSet(t *testing.T) {
	a, _, _ := newTestAgent(t, nil)
	a.Model = ""
	if err := a.RunTurn(context.Background(), "hi"); err == nil {
		t.Error("expected error when no model is set")
	}
}
