package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jonnyq/internal/agent"
	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// lastUserContent returns the content of the last user-role message in a
// sent ChatRequest, i.e. the actual prompt text that was composed for that
// turn.
func lastUserContent(req llm.ChatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i].Content
		}
	}
	return ""
}

// step is one canned reply to a single Chat call.
type step struct {
	events []llm.ChatEvent
}

// sequenceProvider replays one step per Chat call and fails the test if
// asked for more calls than were scripted, so tests can assert exactly how
// many turns a function performed (e.g. that reconciliation did or didn't
// happen, or that only one task was worked on).
type sequenceProvider struct {
	t        *testing.T
	calls    int
	steps    []step
	sentReqs []llm.ChatRequest
}

func (p *sequenceProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.sentReqs = append(p.sentReqs, req)
	if p.calls >= len(p.steps) {
		return nil, fmt.Errorf("unexpected Chat call #%d (only %d scripted)", p.calls+1, len(p.steps))
	}
	s := p.steps[p.calls]
	p.calls++
	ch := make(chan llm.ChatEvent, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (p *sequenceProvider) ListModels(ctx context.Context) ([]string, error) { return nil, nil }

func doneEvent() llm.ChatEvent { return llm.ChatEvent{Kind: llm.EventDone} }

// writeFileStep returns the tool-call events for a simulated write_file
// invocation that overwrites .progress with the given content.
func writeFileStep(content string) step {
	args := fmt.Sprintf(`{"path":".progress","content":%q}`, content)
	return step{events: []llm.ChatEvent{
		{Kind: llm.EventToolCalls, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write_file", Arguments: args}}},
		doneEvent(),
	}}
}

func contentStep(text string) step {
	return step{events: []llm.ChatEvent{
		{Kind: llm.EventContent, Delta: text},
		doneEvent(),
	}}
}

func newTestAgent(t *testing.T, dir string, provider llm.Provider) *agent.Agent {
	t.Helper()
	w, err := ui.New(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	reg := tools.NewRegistry()
	reg.Register(&tools.WriteFileTool{WorkDir: dir})
	reg.Register(&tools.ReadFileTool{WorkDir: dir})
	reg.Register(&tools.EditFileTool{WorkDir: dir})

	return agent.New(provider, "test-model", reg, false, 0, 0, w, nil, filepath.Join(dir, ".context"))
}

// ---- Plan (/plan) ----

func TestPlanGeneratesProgressWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("build a thing"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [ ] task one\n"), contentStep("ok"),
	}}
	ag := newTestAgent(t, dir, p)

	if err := Plan(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("expected exactly 2 Chat calls (toolcall+final), got %d", p.calls)
	}

	progData, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(progData) != "- [ ] task one\n" {
		t.Errorf("unexpected .progress content: %q", progData)
	}
	if got := lastUserContent(p.sentReqs[0]); !strings.Contains(got, "in English") {
		t.Errorf("expected the generation prompt to instruct English task descriptions, got: %s", got)
	}
	if got := lastUserContent(p.sentReqs[0]); !strings.Contains(got, "existing code") {
		t.Errorf("expected the generation prompt to check consistency with the existing codebase, got: %s", got)
	}

	hash, err := os.ReadFile(filepath.Join(dir, hashFile))
	if err != nil {
		t.Fatalf("expected %s to be written: %v", hashFile, err)
	}
	if len(hash) != 64 { // hex sha256
		t.Errorf("unexpected hash file content: %q", hash)
	}
}

func TestPlanSkipsWhenRequirementsUnchanged(t *testing.T) {
	dir := t.TempDir()
	reqData := []byte("build a thing")
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), reqData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [x] task one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(reqData)); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t} // no steps scripted: any Chat call fails the test
	ag := newTestAgent(t, dir, p)

	if err := Plan(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("expected no Chat calls when requirements.md hasn't changed, got %d", p.calls)
	}
}

func TestPlanReconcilesWhenRequirementsChanged(t *testing.T) {
	dir := t.TempDir()
	oldReq := []byte("build a thing")
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), oldReq, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [x] old task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(oldReq)); err != nil {
		t.Fatal(err)
	}

	newReq := []byte("build a thing, and also a new feature")
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), newReq, 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [x] old task\n- [ ] new task\n"), contentStep("ok"),
	}}
	ag := newTestAgent(t, dir, p)

	if err := Plan(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("expected exactly 2 Chat calls (toolcall+final), got %d", p.calls)
	}

	progData, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(progData) != "- [x] old task\n- [ ] new task\n" {
		t.Errorf("unexpected .progress content: %q", progData)
	}
	if got := lastUserContent(p.sentReqs[0]); !strings.Contains(got, "in English") {
		t.Errorf("expected the reconcile prompt to instruct English task descriptions, got: %s", got)
	}

	gotHash, err := os.ReadFile(filepath.Join(dir, hashFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotHash) != fileHash(newReq) {
		t.Error("hash file was not updated to the new requirements.md content")
	}
}

func TestPlanErrorsWithoutRequirementsFile(t *testing.T) {
	dir := t.TempDir()
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})
	if err := Plan(context.Background(), ag, ag.UI, dir); err == nil {
		t.Error("expected an error when requirements.md is missing")
	}
}

func TestPlanResetsCodingMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{writeFileStep("- [ ] only task\n"), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)

	if err := Plan(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if ag.CodingMode {
		t.Error("expected CodingMode to be reset to false after Plan returns")
	}
}

// ---- RunOneTask (/coding) ----

func TestRunOneTaskDoesExactlyOneTaskThenStops(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] task one\n- [ ] task two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [x] task one\n- [ ] task two\n"), contentStep("done"),
	}}
	ag := newTestAgent(t, dir, p)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("RunOneTask failed: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("expected exactly 2 Chat calls (one task's toolcall+final), got %d", p.calls)
	}

	progData, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(progData) != "- [x] task one\n- [ ] task two\n" {
		t.Errorf("expected only the first task to be touched, got: %q", progData)
	}
}

func TestRunOneTaskErrorsWithoutProgressFile(t *testing.T) {
	dir := t.TempDir()
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})
	err := RunOneTask(context.Background(), ag, ag.UI, dir)
	if err == nil {
		t.Fatal("expected an error when .progress is missing")
	}
	if !strings.Contains(err.Error(), "/plan") {
		t.Errorf("expected the error to point at /plan, got: %v", err)
	}
}

func TestRunOneTaskAllDone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [x] task one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t}
	ag := newTestAgent(t, dir, p)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("RunOneTask failed: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("expected no Chat calls when everything is already done, got %d", p.calls)
	}
}

// TestRunOneTaskPersistsRetryDiagnosticAcrossInvocations reproduces the
// original stuck-task bug (see agent.CodingMode / buildTaskPrompt), but for
// /coding's one-invocation-per-call model: since there's no in-memory loop
// across separate RunOneTask calls, the "N attempts so far" state has to be
// persisted to disk (.progress.retry) to still surface the diagnostic note
// and eventually abort.
func TestRunOneTaskPersistsRetryDiagnosticAcrossInvocations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] stuck task\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The model just asserts it's done every time without ever touching
	// .progress.
	steps := make([]step, maxStallRounds)
	for i := range steps {
		steps[i] = contentStep("I already completed and verified this task.")
	}
	p := &sequenceProvider{t: t, steps: steps}
	ag := newTestAgent(t, dir, p)

	var err error
	for i := 0; i < maxStallRounds; i++ {
		err = RunOneTask(context.Background(), ag, ag.UI, dir)
		if i < maxStallRounds-1 && err != nil {
			t.Fatalf("invocation %d: unexpected error: %v", i+1, err)
		}
	}
	if err == nil {
		t.Fatal("expected an error on the final invocation once the stall limit is reached")
	}
	if !strings.Contains(err.Error(), "made no progress") {
		t.Errorf("expected a stall error, got: %v", err)
	}
	if p.calls != maxStallRounds {
		t.Errorf("expected exactly %d invocations to have called Chat, got %d", maxStallRounds, p.calls)
	}

	first := lastUserContent(p.sentReqs[0])
	if strings.Contains(first, "already attempted") {
		t.Errorf("did not expect the diagnostic note on the first invocation, got: %s", first)
	}
	last := lastUserContent(p.sentReqs[len(p.sentReqs)-1])
	if !strings.Contains(last, "already attempted") || !strings.Contains(last, "STILL shown as unchecked") {
		t.Errorf("expected the diagnostic note on a later invocation, got: %s", last)
	}

	if _, err := os.Stat(filepath.Join(dir, retryFile)); !os.IsNotExist(err) {
		t.Error("expected the retry state file to be cleaned up once the stall limit aborts")
	}
}

func TestRunOneTaskClearsRetryStateWhenTaskCompletes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] task one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate one prior failed attempt already on disk.
	if err := writeRetryState(filepath.Join(dir, retryFile), retryState{text: "task one", count: 2}); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [x] task one\n"), contentStep("done"),
	}}
	ag := newTestAgent(t, dir, p)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("RunOneTask failed: %v", err)
	}

	// The prompt for this attempt should carry over the prior count...
	got := lastUserContent(p.sentReqs[0])
	if !strings.Contains(got, "already attempted 2 time(s)") {
		t.Errorf("expected the persisted retry count to be used, got: %s", got)
	}
	// ...but once the task completes, the retry state must be cleared so a
	// future, unrelated task doesn't inherit it.
	if _, err := os.Stat(filepath.Join(dir, retryFile)); !os.IsNotExist(err) {
		t.Error("expected retry state to be cleared once the task is done")
	}
}

// ---- AutoRun (/autocoding) ----

func TestAutoRunGeneratesAndCompletesAllTasks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("build a thing"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [ ] task one\n"), contentStep("ok"), // Plan's generation turn
		writeFileStep("- [x] task one\n"), contentStep("done"), // task loop finishes it
	}}
	ag := newTestAgent(t, dir, p)

	if err := AutoRun(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("AutoRun failed: %v", err)
	}
	if p.calls != 4 {
		t.Errorf("expected exactly 4 Chat calls (generate: toolcall+final, one task: toolcall+final), got %d", p.calls)
	}

	progData, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(progData) != "- [x] task one\n" {
		t.Errorf("unexpected .progress content: %q", progData)
	}
}

func TestAutoRunRetriesStuckTaskWithDiagnosticNote(t *testing.T) {
	dir := t.TempDir()
	reqData := []byte("x")
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), reqData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] stuck task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(reqData)); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		contentStep("I already completed and verified this task."),
		contentStep("I already completed and verified this task."),
		contentStep("I already completed and verified this task."),
		contentStep("I already completed and verified this task."),
		contentStep("I already completed and verified this task."),
	}}
	ag := newTestAgent(t, dir, p)

	err := AutoRun(context.Background(), ag, ag.UI, dir)
	if err == nil {
		t.Fatal("expected an error once the stall limit is reached")
	}
	if !strings.Contains(err.Error(), "made no progress") {
		t.Errorf("expected a stall error, got: %v", err)
	}
	if p.calls != maxStallRounds {
		t.Errorf("expected exactly %d attempts before aborting, got %d", maxStallRounds, p.calls)
	}

	last := lastUserContent(p.sentReqs[len(p.sentReqs)-1])
	if !strings.Contains(last, "already attempted") || !strings.Contains(last, "STILL shown as unchecked") {
		t.Errorf("expected the retry prompt to include the diagnostic note, got: %s", last)
	}
	first := lastUserContent(p.sentReqs[0])
	if strings.Contains(first, "already attempted") {
		t.Errorf("did not expect the diagnostic note on the first attempt, got: %s", first)
	}
}

func TestAutoRunResetsCodingMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{
		// The generated checklist is already fully checked off, so no
		// separate task-loop turn is needed.
		writeFileStep("- [x] only task\n"), contentStep("ok"),
	}}
	ag := newTestAgent(t, dir, p)

	if err := AutoRun(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("AutoRun failed: %v", err)
	}
	if ag.CodingMode {
		t.Error("expected CodingMode to be reset to false after AutoRun returns")
	}
}
