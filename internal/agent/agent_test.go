package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// sequenceProvider replays one canned response per call to Chat, so the
// tool-calling loop can be tested without a real model or network.
type sequenceProvider struct {
	calls     int
	responses [][]llm.ChatEvent
	sentReqs  []llm.ChatRequest
}

func (p *sequenceProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.sentReqs = append(p.sentReqs, req)
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
	a := New(p, "test-model", reg, false, 0, 0, w, nil)
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

	if a.LastUsage.TotalTokens != 16 {
		t.Errorf("expected LastUsage.TotalTokens to be the aggregated 16, got %d", a.LastUsage.TotalTokens)
	}
	if a.LastUsage.PromptTokens != 13 || a.LastUsage.CompletionTokens != 3 {
		t.Errorf("expected LastUsage to hold the aggregated prompt/completion tokens, got %+v", a.LastUsage)
	}
	if a.LastTurnElapsed <= 0 {
		t.Errorf("expected LastTurnElapsed to be a positive duration, got %v", a.LastTurnElapsed)
	}
}

// TestRunTurnPromptModeOmitsNativeToolsAndDescribesThemInPrompt guards the
// core design decision of ToolCallModePrompt: the native Tools spec must
// never be sent (sending it while also prompting for text-based calls
// tends to trigger a backend's own function-calling grammar constraints -
// the very failure mode this mode exists to route around), and the system
// message must describe each tool instead so the model has the same
// information it would have gotten via the API.
func TestRunTurnPromptModeOmitsNativeToolsAndDescribesThemInPrompt(t *testing.T) {
	responses := [][]llm.ChatEvent{
		{{Kind: llm.EventContent, Delta: "no tool needed"}, {Kind: llm.EventDone}},
	}
	a, _, _ := newTestAgent(t, responses)
	a.ToolCallMode = ToolCallModePrompt

	if err := a.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	p := a.Provider.(*sequenceProvider)
	if len(p.sentReqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(p.sentReqs))
	}
	if p.sentReqs[0].Tools != nil {
		t.Errorf("expected no native Tools spec sent in prompt mode, got: %+v", p.sentReqs[0].Tools)
	}
	sysMsg := p.sentReqs[0].Messages[0].Content
	if !strings.Contains(sysMsg, "TOOL CALLING (PROMPT MODE)") {
		t.Errorf("expected the system message to include the prompt-mode instructions, got: %s", sysMsg)
	}
	if !strings.Contains(sysMsg, "echo: echoes text") {
		t.Errorf("expected the system message to describe the registered echo tool, got: %s", sysMsg)
	}
}

// TestRunTurnPromptModeExecutesToolCallFromFencedBlock reproduces the
// actual point of this mode: a model with no native tool-calling event at
// all (EventContent only) can still trigger a real tool call by emitting a
// ```tool fenced JSON block as plain text.
func TestRunTurnPromptModeExecutesToolCallFromFencedBlock(t *testing.T) {
	responses := [][]llm.ChatEvent{
		{
			{Kind: llm.EventContent, Delta: "```tool\n" + `{"name": "echo", "arguments": {"text": "hi"}}` + "\n```"},
			{Kind: llm.EventDone},
		},
		{
			{Kind: llm.EventContent, Delta: "all done"},
			{Kind: llm.EventDone},
		},
	}
	a, et, _ := newTestAgent(t, responses)
	a.ToolCallMode = ToolCallModePrompt

	if err := a.RunTurn(context.Background(), "please echo hi"); err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if !et.called {
		t.Fatal("expected the echo tool to have been called from the fenced block")
	}

	var sawToolResult, sawNativeToolCallsField bool
	for _, m := range a.History {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Tool result (echo): echoed: hi") {
			sawToolResult = true
		}
		if len(m.ToolCalls) > 0 {
			sawNativeToolCallsField = true
		}
	}
	if !sawToolResult {
		t.Errorf("expected a plain user-role tool-result message, got history: %+v", a.History)
	}
	if sawNativeToolCallsField {
		t.Error("expected no message to carry a native ToolCalls field in prompt mode")
	}
}

// TestRunTurnPromptModeReportsMalformedBlockAsError confirms a ```tool
// block that fails to parse produces clear, actionable feedback (not a
// generic "unknown tool" from Tools.Call, and not a silently ignored turn).
func TestRunTurnPromptModeReportsMalformedBlockAsError(t *testing.T) {
	responses := [][]llm.ChatEvent{
		{
			{Kind: llm.EventContent, Delta: "```tool\nnot valid json\n```"},
			{Kind: llm.EventDone},
		},
		{
			{Kind: llm.EventContent, Delta: "sorry, let me retry"},
			{Kind: llm.EventDone},
		},
	}
	a, et, _ := newTestAgent(t, responses)
	a.ToolCallMode = ToolCallModePrompt

	if err := a.RunTurn(context.Background(), "please echo hi"); err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if et.called {
		t.Error("expected the echo tool NOT to have been called for a malformed block")
	}

	var sawParseError bool
	for _, m := range a.History {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "error: invalid JSON") {
			sawParseError = true
		}
	}
	if !sawParseError {
		t.Errorf("expected a clear parse-error tool result in history, got: %+v", a.History)
	}
}

func TestFormatUsageMatchesTerminalMetricsLine(t *testing.T) {
	u := llm.Usage{
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
		LoadDuration: 1200 * time.Millisecond, PromptEvalDuration: 300 * time.Millisecond, EvalDuration: 2 * time.Second,
		HasTiming: true,
	}
	got := FormatUsage(u)
	want := "preload=1.2s prompt_eval=300ms thinking=2s token_in=100 token_out=50 total_token=150 tok/s=25.00"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatUsageHandlesMissingTimingAndTokens(t *testing.T) {
	got := FormatUsage(llm.Usage{})
	want := "preload=n/a prompt_eval=n/a thinking=n/a token_in=n/a token_out=n/a total_token=n/a tok/s=n/a"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
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

// TestRunTurnCompactsMidTurnWhenHistoryGrowsLarge reproduces the actual
// cause of "prompt processing gets slow": a single long turn (e.g.
// /autocoding grinding through many tasks) can pile up a lot of tool
// call/result content well before it returns, so compaction must fire
// during the turn, not only afterward. With a tiny ContextSize, one large
// tool round should already push History over the compaction threshold.
func TestRunTurnCompactsMidTurnWhenHistoryGrowsLarge(t *testing.T) {
	bigArg := strings.Repeat("x", 200)
	responses := [][]llm.ChatEvent{
		{ // round 1: a tool call with a large argument
			{Kind: llm.EventToolCalls, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: `{"text":"` + bigArg + `"}`}}},
			{Kind: llm.EventDone},
		},
		{ // compact()'s own summarization call, triggered mid-turn
			{Kind: llm.EventContent, Delta: "compacted summary"},
			{Kind: llm.EventDone},
		},
		{ // round 2: model sees the compacted history and finishes
			{Kind: llm.EventContent, Delta: "final answer"},
			{Kind: llm.EventDone},
		},
	}
	a, _, outFile := newTestAgent(t, responses)
	// Small enough that the ~440-char tool round crosses the compaction
	// threshold, but large enough that the ~65-char post-compaction history
	// (summary + final answer) doesn't trigger a second compaction call.
	a.ContextSize = 100

	if err := a.RunTurn(context.Background(), "do the big thing"); err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}

	if len(a.History) != 2 {
		t.Fatalf("expected History to be [summary, final answer] after mid-turn compaction, got %d entries: %+v", len(a.History), a.History)
	}
	if a.History[0].Role != llm.RoleSystem || !strings.Contains(a.History[0].Content, "Summary of earlier conversation") {
		t.Errorf("expected first entry to be the compaction summary, got: %+v", a.History[0])
	}
	if a.History[1].Role != llm.RoleAssistant || a.History[1].Content != "final answer" {
		t.Errorf("expected final answer after the summary, got: %+v", a.History[1])
	}
	if strings.Contains(a.History[0].Content+a.History[1].Content, bigArg) {
		t.Errorf("expected the large tool argument to have been summarized away, got: %+v", a.History)
	}

	logData, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "[context compacted]") {
		t.Errorf("expected a compaction notice in the log, got: %s", logData)
	}
}

// TestCompactEnforcesTargetSizeEvenIfModelIgnoresLengthInstruction guards
// against a slow leak: compact() asks the model to keep its summary under
// ~25% of the character budget, but a local model can simply ignore that
// instruction. Without a hard cap, an oversized summary would sit close to
// (or above) the compaction trigger again immediately, so a long session
// would keep re-summarizing without ever actually shrinking History.
func TestCompactEnforcesTargetSizeEvenIfModelIgnoresLengthInstruction(t *testing.T) {
	verbose := strings.Repeat("lorem ipsum ", 100) // ~1200 chars, ignores any length request
	responses := [][]llm.ChatEvent{
		{
			{Kind: llm.EventContent, Delta: verbose},
			{Kind: llm.EventDone},
		},
	}
	a, _, _ := newTestAgent(t, responses)
	a.ContextSize = 100 // charBudget = 400, target = 100
	a.History = []llm.Message{{Role: llm.RoleUser, Content: "some prior turn content"}}

	a.compact(context.Background())

	if len(a.History) != 1 {
		t.Fatalf("expected History to be replaced with exactly one summary message, got %d: %+v", len(a.History), a.History)
	}
	target := int(float64(a.charBudget()) * compactTargetFraction)
	if got := len(a.History[0].Content); got > target {
		t.Errorf("expected compacted History capped at %d chars, got %d: %q", target, got, a.History[0].Content)
	}
	if strings.Contains(a.History[0].Content, verbose) {
		t.Errorf("expected the verbose summary to have been truncated, got the full text retained")
	}
}

func TestTruncateCharsKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("สวัสดี", 50) // Thai text: multi-byte runes throughout
	for max := 0; max < 40; max++ {
		out := truncateChars(s, max)
		if !utf8.ValidString(out) {
			t.Fatalf("truncateChars(_, %d) produced invalid UTF-8: %q", max, out)
		}
		if len(out) > max && max > 0 {
			t.Errorf("truncateChars(_, %d) returned %d bytes, want <= %d", max, len(out), max)
		}
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
