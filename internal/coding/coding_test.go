package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"jonnyq/internal/agent"
	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// step is one canned reply to a single Chat call.
type step struct {
	events []llm.ChatEvent
}

// sequenceProvider replays one step per Chat call and fails the test if
// asked for more calls than were scripted, so tests can assert exactly how
// many turns Run performed (e.g. that reconciliation did or didn't happen).
type sequenceProvider struct {
	t     *testing.T
	calls int
	steps []step
}

func (p *sequenceProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
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

// writeFileCall returns the tool-call events for a simulated write_file
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

func TestRunGeneratesProgressWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("build a thing"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		// Each RunTurn that makes a tool call needs a second, content-only
		// step afterward: the tool loop always continues until the model
		// replies with no further tool calls.
		writeFileStep("- [ ] task one\n"), contentStep("ok"), // generation turn writes the checklist
		writeFileStep("- [x] task one\n"), contentStep("done"), // task turn marks it done
	}}
	ag := newTestAgent(t, dir, p)

	if err := Run(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Run failed: %v", err)
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

	hash, err := os.ReadFile(filepath.Join(dir, hashFile))
	if err != nil {
		t.Fatalf("expected %s to be written: %v", hashFile, err)
	}
	if len(hash) != 64 { // hex sha256
		t.Errorf("unexpected hash file content: %q", hash)
	}
}

func TestRunSkipsReconcileWhenRequirementsUnchanged(t *testing.T) {
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

	if err := Run(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("expected no Chat calls when nothing changed and all tasks are done, got %d", p.calls)
	}
}

func TestRunReconcilesWhenRequirementsChanged(t *testing.T) {
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

	// Now the requirement changes.
	newReq := []byte("build a thing, and also a new feature")
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), newReq, 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [x] old task\n- [ ] new task\n"), contentStep("ok"), // reconcile turn
		writeFileStep("- [x] old task\n- [x] new task\n"), contentStep("done"), // task turn finishes it
	}}
	ag := newTestAgent(t, dir, p)

	if err := Run(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if p.calls != 4 {
		t.Errorf("expected exactly 4 Chat calls (reconcile: toolcall+final, one task: toolcall+final), got %d", p.calls)
	}

	progData, err := os.ReadFile(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(progData) != "- [x] old task\n- [x] new task\n" {
		t.Errorf("unexpected .progress content: %q", progData)
	}

	gotHash, err := os.ReadFile(filepath.Join(dir, hashFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotHash) != fileHash(newReq) {
		t.Errorf("hash file was not updated to the new requirements.md content")
	}
}

func TestRunErrorsWithoutRequirementsFile(t *testing.T) {
	dir := t.TempDir()
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})
	if err := Run(context.Background(), ag, ag.UI, dir); err == nil {
		t.Error("expected an error when requirements.md is missing")
	}
}

func TestCodingModeIsResetAfterRun(t *testing.T) {
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

	if err := Run(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if ag.CodingMode {
		t.Error("expected CodingMode to be reset to false after Run returns")
	}
}
