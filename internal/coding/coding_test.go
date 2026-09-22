package coding

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jonnyq/internal/agent"
	"jonnyq/internal/llm"
	"jonnyq/internal/notify"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// captureNotify starts an httptest.Server that records the last ntfy
// request's Title header and body, for asserting what Plan/RunOneTask
// notified about.
type captureNotify struct {
	srv   *httptest.Server
	title string
	body  string
	calls int
}

func newCaptureNotify(t *testing.T) *captureNotify {
	t.Helper()
	c := &captureNotify{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls++
		c.title = r.Header.Get("Title")
		body, _ := io.ReadAll(r.Body)
		c.body = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureNotify) notifier() *notify.Notifier {
	return notify.New(c.srv.URL)
}

// failIfCalledNotify starts an httptest.Server that fails the test if it
// ever receives a request, for asserting a code path does NOT notify.
func failIfCalledNotify(t *testing.T) *notify.Notifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected ntfy notification received")
	}))
	t.Cleanup(srv.Close)
	return notify.New(srv.URL)
}

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

	return agent.New(provider, "test-model", reg, false, 0, 0, w, nil)
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

	if err := Plan(context.Background(), ag, ag.UI, dir, nil); err != nil {
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

	if err := Plan(context.Background(), ag, ag.UI, dir, nil); err != nil {
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

	if err := Plan(context.Background(), ag, ag.UI, dir, nil); err != nil {
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
	if err := Plan(context.Background(), ag, ag.UI, dir, nil); err == nil {
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

	if err := Plan(context.Background(), ag, ag.UI, dir, nil); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if ag.CodingMode {
		t.Error("expected CodingMode to be reset to false after Plan returns")
	}
}

func TestPlanNotifiesOnGenerate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("build a thing"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{writeFileStep("- [ ] task one\n- [x] task two\n"), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := Plan(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if capture.calls != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", capture.calls)
	}
	if !strings.Contains(capture.title, "/plan") {
		t.Errorf("expected the notification title to mention /plan, got: %q", capture.title)
	}
	if !strings.Contains(capture.body, "1/2 tasks done") {
		t.Errorf("expected the notification body to summarize progress, got: %q", capture.body)
	}
}

func TestPlanNotifiesOnReconcile(t *testing.T) {
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
	capture := newCaptureNotify(t)

	if err := Plan(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if capture.calls != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", capture.calls)
	}
	if !strings.Contains(capture.body, "2 tasks total (was 1)") {
		t.Errorf("expected the notification body to mention the task-count change, got: %q", capture.body)
	}
}

func TestPlanDoesNotNotifyWhenAlreadyUpToDate(t *testing.T) {
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
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})

	if err := Plan(context.Background(), ag, ag.UI, dir, failIfCalledNotify(t)); err != nil {
		t.Fatalf("Plan failed: %v", err)
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

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, nil); err != nil {
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
	err := RunOneTask(context.Background(), ag, ag.UI, dir, nil)
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

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, nil); err != nil {
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
// TestRunOneTaskPersistsRetryDiagnosticAcrossInvocations reproduces a
// real-world failure across separate /coding invocations (no in-memory loop
// to carry state, unlike /autocoding): the model claims the task is done in
// prose every time without ever calling edit_file/write_file on .progress.
// Since the file's raw content never changes across invocations, this must
// persist and surface the "never touched the file" diagnostic, not the
// generic "checkbox didn't end up set" one.
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
		err = RunOneTask(context.Background(), ag, ag.UI, dir, nil)
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
	if !strings.Contains(last, "already attempted") || !strings.Contains(last, "did NOT call edit_file or write_file") {
		t.Errorf("expected the 'never touched the file' diagnostic note on a later invocation, got: %s", last)
	}
	if strings.Contains(last, "STILL shown as unchecked") {
		t.Errorf("did not expect the generic 'failed edit' note when no edit was ever attempted, got: %s", last)
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

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, nil); err != nil {
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

func TestRunOneTaskNotifiesOnCompletion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] task one\n- [ ] task two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [x] task one\n- [ ] task two\n"), contentStep("done"),
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatalf("RunOneTask failed: %v", err)
	}
	if capture.calls != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", capture.calls)
	}
	if !strings.Contains(capture.title, "/coding") {
		t.Errorf("expected the notification title to mention /coding, got: %q", capture.title)
	}
	if !strings.Contains(capture.body, "task one") || !strings.Contains(capture.body, "1/2 tasks done") {
		t.Errorf("expected the notification body to name the completed task and overall progress, got: %q", capture.body)
	}
}

func TestRunOneTaskNotifiesWhenAllDone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [x] task one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})
	capture := newCaptureNotify(t)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatalf("RunOneTask failed: %v", err)
	}
	if capture.calls != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", capture.calls)
	}
	if !strings.Contains(capture.body, "1/1 tasks done") {
		t.Errorf("expected the notification body to report full completion, got: %q", capture.body)
	}
}

func TestRunOneTaskNotifiesOnStall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] stuck task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRetryState(filepath.Join(dir, retryFile), retryState{text: "stuck task", count: maxStallRounds - 1}); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{contentStep("I already did this.")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	err := RunOneTask(context.Background(), ag, ag.UI, dir, capture.notifier())
	if err == nil {
		t.Fatal("expected an error once the stall limit is reached")
	}
	if capture.calls != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", capture.calls)
	}
	if !strings.Contains(capture.title, "stalled") {
		t.Errorf("expected the notification title to flag a stall, got: %q", capture.title)
	}
	if !strings.Contains(capture.body, "stuck task") {
		t.Errorf("expected the notification body to name the stuck task, got: %q", capture.body)
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

// TestAutoRunRetriesStuckTaskNeverTouchedWithDiagnosticNote reproduces a
// real-world failure: the model claims the task is done in prose (and even
// prints fake "✓ passing" lines via run_command echo) without ever calling
// edit_file/write_file on .progress at all. Since .progress's raw content
// never changes across attempts, this must hit the "untouched" branch of
// the diagnostic note, not the generic "checkbox didn't end up set" one.
func TestAutoRunRetriesStuckTaskNeverTouchedWithDiagnosticNote(t *testing.T) {
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
	if !strings.Contains(last, "already attempted") || !strings.Contains(last, "did NOT call edit_file or write_file") {
		t.Errorf("expected the 'never touched the file' diagnostic note, got: %s", last)
	}
	if strings.Contains(last, "STILL shown as unchecked") {
		t.Errorf("did not expect the generic 'failed edit' note when no edit was ever attempted, got: %s", last)
	}
	first := lastUserContent(p.sentReqs[0])
	if strings.Contains(first, "already attempted") {
		t.Errorf("did not expect the diagnostic note on the first attempt, got: %s", first)
	}
}

// TestAutoRunRetriesStuckTaskTouchedWithDiagnosticNote covers the other
// failure mode: the model DOES call write_file on .progress each attempt,
// but the task stays unchecked (e.g. it keeps rewriting the same content
// from a stale copy). This must hit the generic "checkbox didn't end up
// set" branch, not the "never touched the file" one.
func TestAutoRunRetriesStuckTaskTouchedWithDiagnosticNote(t *testing.T) {
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

	// Each attempt writes genuinely different bytes (as a real stale-copy
	// clobber would) so this is distinguishable, at the file-content level
	// used by progressUnchanged, from an attempt that never touches the
	// file at all - writing back identical content wouldn't be.
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [ ] stuck task\n# attempt 1\n"), contentStep("done"),
		writeFileStep("- [ ] stuck task\n# attempt 2\n"), contentStep("done"),
		writeFileStep("- [ ] stuck task\n# attempt 3\n"), contentStep("done"),
		writeFileStep("- [ ] stuck task\n# attempt 4\n"), contentStep("done"),
		writeFileStep("- [ ] stuck task\n# attempt 5\n"), contentStep("done"),
	}}
	ag := newTestAgent(t, dir, p)

	err := AutoRun(context.Background(), ag, ag.UI, dir)
	if err == nil {
		t.Fatal("expected an error once the stall limit is reached")
	}
	if !strings.Contains(err.Error(), "made no progress") {
		t.Errorf("expected a stall error, got: %v", err)
	}

	last := lastUserContent(p.sentReqs[len(p.sentReqs)-1])
	if !strings.Contains(last, "already attempted") || !strings.Contains(last, "STILL shown as unchecked") {
		t.Errorf("expected the generic 'checkbox not set' diagnostic note, got: %s", last)
	}
	if strings.Contains(last, "did NOT call edit_file or write_file") {
		t.Errorf("did not expect the 'never touched the file' note when write_file was called every attempt, got: %s", last)
	}
}

// TestAutoRunResetsHistoryBeforeEachTask confirms each task starts from a
// clean slate: none of the planning step's or an earlier task's own
// conversation (tool calls, replies) leaks into a later task's prompt. This
// keeps every task's prompt minimal instead of growing across the whole
// run - the model is expected to re-discover whatever it needs via
// read_file/run_command instead of relying on carried-over history.
func TestAutoRunResetsHistoryBeforeEachTask(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte("build two things"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("- [ ] first task\n- [ ] second task\n"), contentStep("plan reply"), // Plan's generation turn
		writeFileStep("- [x] first task\n- [ ] second task\n"), contentStep("task1 reply"), // first task
		writeFileStep("- [x] first task\n- [x] second task\n"), contentStep("task2 reply"), // second task
	}}
	ag := newTestAgent(t, dir, p)

	if err := AutoRun(context.Background(), ag, ag.UI, dir); err != nil {
		t.Fatalf("AutoRun failed: %v", err)
	}
	if p.calls != 6 {
		t.Fatalf("expected exactly 6 Chat calls, got %d", p.calls)
	}

	// Requests 5 and 6 (index 4, 5) are the second task's turn. Neither of
	// its messages should carry the planning step's or the first task's own
	// reply text forward.
	for _, idx := range []int{4, 5} {
		for _, m := range p.sentReqs[idx].Messages {
			if strings.Contains(m.Content, "plan reply") {
				t.Errorf("request #%d: expected the planning turn's reply not to leak into the second task's history, got: %+v", idx+1, p.sentReqs[idx].Messages)
			}
			if strings.Contains(m.Content, "task1 reply") {
				t.Errorf("request #%d: expected the first task's reply not to leak into the second task's history, got: %+v", idx+1, p.sentReqs[idx].Messages)
			}
		}
	}
	// The second task's own request should still be self-contained: a
	// system message plus this task's own prompt (and, for the final
	// round, its own tool call/result) - not zero, and not bloated with
	// carried-over turns.
	if got := len(p.sentReqs[5].Messages); got < 2 || got > 4 {
		t.Errorf("expected the second task's final request to have 2-4 messages (system + its own turn), got %d: %+v", got, p.sentReqs[5].Messages)
	}
}

// TestAutoRunAbortsOnNoOverallProgressEvenIfNextTaskTextKeepsChanging
// reproduces a real-world runaway: the model keeps rewriting .progress
// (e.g. reordering it) each attempt, so the "next" unfinished task's text
// alternates between two different tasks every round - defeating the
// same-task stall counter, which only tracks whether one exact task text
// repeats - while zero tasks ever actually get checked off. Without a
// broader safety valve this can run for hours; with it, AutoRun must abort
// within maxNoProgressRounds instead of exhausting every scripted response
// (which would itself surface as a different, misleading error).
func TestAutoRunAbortsOnNoOverallProgressEvenIfNextTaskTextKeepsChanging(t *testing.T) {
	dir := t.TempDir()
	reqData := []byte("x")
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), reqData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte("- [ ] task A\n- [ ] task B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(reqData)); err != nil {
		t.Fatal(err)
	}

	// Provide comfortably more scripted rounds than maxNoProgressRounds
	// should ever consume, so a fix that doesn't abort in time causes a
	// mismatched error (running out of scripted steps) rather than
	// silently looping forever in the test.
	var steps []step
	for i := 0; i < maxNoProgressRounds+5; i++ {
		if i%2 == 0 {
			steps = append(steps, writeFileStep("- [ ] task B\n- [ ] task A\n"), contentStep("reordered"))
		} else {
			steps = append(steps, writeFileStep("- [ ] task A\n- [ ] task B\n"), contentStep("reordered"))
		}
	}
	p := &sequenceProvider{t: t, steps: steps}
	ag := newTestAgent(t, dir, p)

	err := AutoRun(context.Background(), ag, ag.UI, dir)
	if err == nil {
		t.Fatal("expected an error once the no-overall-progress limit is reached")
	}
	if !strings.Contains(err.Error(), "no task") || !strings.Contains(err.Error(), "completed") {
		t.Errorf("expected the no-overall-progress error, got: %v", err)
	}
	// 2 Chat calls (tool round + final round) per AutoRun iteration.
	if maxRounds := 2 * (maxNoProgressRounds + 1); p.calls > maxRounds {
		t.Errorf("expected AutoRun to abort within %d rounds instead of looping past the safety valve, got %d Chat calls", maxNoProgressRounds, p.calls/2)
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
