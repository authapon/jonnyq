package coding

import (
	"context"
	"jonnyq/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const guidedProgress = `# Demo App

> How to run: ` + "`npm start`" + ` then open http://localhost:3000
> Demo login: demo / demo

## Phase 1: Login

> Human check: Open /login, sign in as demo/demo -> the Dashboard appears

- [ ] build login form
- [ ] wire login API

## Phase 2: Orders

> Human check: Open /orders -> 3 sample orders are listed

- [ ] build orders list
`

func writeProgress(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, progressFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeReq(t *testing.T, dir, content string) []byte {
	t.Helper()
	data := []byte(content)
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseProgressDocReadsGuideAndPhases(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, guidedProgress)
	doc, err := parseProgressDoc(filepath.Join(dir, progressFile))
	if err != nil {
		t.Fatal(err)
	}
	if !doc.hasGuide() {
		t.Fatal("expected a guide to be detected")
	}
	if !strings.Contains(doc.howToRun, "npm start") || !strings.Contains(doc.howToRun, "Demo login: demo / demo") {
		t.Errorf("multi-line How to run not captured: %q", doc.howToRun)
	}
	if len(doc.phases) != 2 || len(doc.phases[0].tasks) != 2 || len(doc.phases[1].tasks) != 1 {
		t.Fatalf("unexpected phases: %+v", doc.phases)
	}
	if !strings.Contains(doc.phases[0].check, "Dashboard appears") || !strings.Contains(doc.phases[1].check, "3 sample orders") {
		t.Errorf("phase checks not attached to their phases: %+v", doc.phases)
	}
	// The notes are not tasks: task tracking must be unaffected.
	tasks, _ := parseProgress(filepath.Join(dir, progressFile))
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
}

func TestParseProgressDocWithoutGuide(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, "# T\n\n## Phase 1: A\n\n- [ ] x\n")
	doc, _ := parseProgressDoc(filepath.Join(dir, progressFile))
	if doc.hasGuide() || doc.guideText() != "" {
		t.Errorf("expected no guide, got %+v", doc)
	}
}

func TestGuideTextShowsPhaseProgress(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, strings.Replace(guidedProgress, "- [ ] build orders list", "- [x] build orders list", 1))
	doc, _ := parseProgressDoc(filepath.Join(dir, progressFile))
	g := doc.guideText()
	for _, want := range []string{"HOW TO RUN", "npm start", "HOW TO VERIFY", "Phase 1: Login [0/2 tasks done]", "Phase 2: Orders [done]", "Open /orders"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide missing %q:\n%s", want, g)
		}
	}
}

func TestTruncateBytesIsRuneSafeAndFlagsCut(t *testing.T) {
	long := strings.Repeat("สวัสดี", 400)
	out := truncateBytes(long, 500)
	if len(out) > 500 || !utf8.ValidString(out) || !strings.Contains(out, "truncated") {
		t.Errorf("bad truncation: len=%d valid=%v", len(out), utf8.ValidString(out))
	}
	if truncateBytes("short", 500) != "short" {
		t.Error("short text must pass through unchanged")
	}
}

// ---- PlanForHuman ----

func TestPlanForHumanGeneratesAndNotifiesGuide(t *testing.T) {
	dir := t.TempDir()
	writeReq(t, dir, "build a thing")
	p := &sequenceProvider{t: t, steps: []step{writeFileStep(guidedProgress), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatalf("PlanForHuman failed: %v", err)
	}
	if p.calls != 2 { // write_file round + final answer round: one turn, no retry
		t.Errorf("expected a single model turn, got %d Chat calls", p.calls)
	}
	if prompt := firstUserContent(p.sentReqs[0]); !strings.Contains(prompt, "HUMAN REVIEWER") || !strings.Contains(prompt, "> Human check:") {
		t.Errorf("expected the human-verifiable planning rules in the prompt, got: %s", prompt)
	}
	if capture.calls != 1 || !strings.Contains(capture.title, "/plan_for_human complete") {
		t.Fatalf("expected one /plan_for_human notification, got %d (%q)", capture.calls, capture.title)
	}
	for _, want := range []string{"0/3 tasks done", "HOW TO RUN", "npm start", "HOW TO VERIFY", "Phase 1: Login", "Dashboard appears", "Duration:"} {
		if !strings.Contains(capture.body, want) {
			t.Errorf("notification body missing %q:\n%s", want, capture.body)
		}
	}
	if readStoredHash(filepath.Join(dir, hashFile)) == "" {
		t.Error("expected the requirements hash to be recorded")
	}
}

func TestPlanForHumanRetriesOnceWhenGuideMissing(t *testing.T) {
	dir := t.TempDir()
	writeReq(t, dir, "build a thing")
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("## Phase 1: A\n\n- [ ] x\n"), contentStep("ok"), // no notes
		writeFileStep(guidedProgress), contentStep("added"), // retry adds them
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 4 {
		t.Fatalf("expected generate + one retry turn (4 Chat calls), got %d", p.calls)
	}
	if retry := lastUserContent(p.sentReqs[2]); !strings.Contains(retry, "Do not change, remove, reorder, check or uncheck") {
		t.Errorf("expected the add-notes prompt on the retry, got: %s", retry)
	}
	if strings.Contains(capture.body, "WARNING") || !strings.Contains(capture.body, "HOW TO VERIFY") {
		t.Errorf("expected the guide and no warning after a successful retry:\n%s", capture.body)
	}
}

func TestPlanForHumanWarnsWhenGuideStillMissing(t *testing.T) {
	dir := t.TempDir()
	writeReq(t, dir, "build a thing")
	bare := "## Phase 1: A\n\n- [ ] x\n"
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep(bare), contentStep("ok"),
		contentStep("I did nothing"), // retry still produces no notes
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatalf("a missing guide must warn, not fail: %v", err)
	}
	if capture.calls != 1 || !strings.Contains(capture.body, "WARNING") || !strings.Contains(capture.body, "no verification guide") {
		t.Errorf("expected a warning in the notification, got %d:\n%s", capture.calls, capture.body)
	}
}

func TestPlanForHumanResendsGuideWithoutModelCall(t *testing.T) {
	dir := t.TempDir()
	req := writeReq(t, dir, "build a thing")
	writeProgress(t, dir, guidedProgress)
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(req)); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t} // any Chat call fails the test
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Errorf("expected no model call, got %d", p.calls)
	}
	if capture.calls != 1 || !strings.Contains(capture.body, "Re-sending the verification guide") || !strings.Contains(capture.body, "HOW TO VERIFY") {
		t.Errorf("expected the guide to be re-sent, got %d:\n%s", capture.calls, capture.body)
	}
	if !strings.Contains(capture.body, "token_in=n/a") {
		t.Errorf("no turn ran, so stats must be n/a:\n%s", capture.body)
	}
}

func TestPlanForHumanAddsNotesToExistingProgressWithoutTouchingTasks(t *testing.T) {
	dir := t.TempDir()
	req := writeReq(t, dir, "build a thing")
	writeProgress(t, dir, "## Phase 1: A\n\n- [x] done already\n- [ ] todo\n")
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(req)); err != nil {
		t.Fatal(err)
	}
	withNotes := "# T\n\n> How to run: go run .\n\n## Phase 1: A\n\n> Human check: open / -> see hello\n\n- [x] done already\n- [ ] todo\n"
	p := &sequenceProvider{t: t, steps: []step{writeFileStep(withNotes), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capture.body, "Added human verification notes") || !strings.Contains(capture.body, "1/2 tasks done") {
		t.Errorf("unexpected notification:\n%s", capture.body)
	}
	if prompt := firstUserContent(p.sentReqs[0]); !strings.Contains(prompt, "Do not change, remove, reorder, check or uncheck") {
		t.Errorf("expected the add-notes prompt, got: %s", prompt)
	}
}

func TestPlanForHumanReconcilesWithHumanRules(t *testing.T) {
	dir := t.TempDir()
	writeReq(t, dir, "changed requirements")
	writeProgress(t, dir, guidedProgress)
	if err := writeStoredHash(filepath.Join(dir, hashFile), "stale-hash"); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{writeFileStep(guidedProgress), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	prompt := firstUserContent(p.sentReqs[0])
	if !strings.Contains(prompt, "has changed since") || !strings.Contains(prompt, "HUMAN REVIEWER") || !strings.Contains(prompt, "keep the existing") {
		t.Errorf("expected the reconcile prompt with human rules, got: %s", prompt)
	}
	if !strings.Contains(capture.body, "Reconciled") || !strings.Contains(capture.body, "HOW TO VERIFY") {
		t.Errorf("unexpected notification:\n%s", capture.body)
	}
}

func TestPlanDoesNotUseHumanRules(t *testing.T) {
	dir := t.TempDir()
	writeReq(t, dir, "build a thing")
	p := &sequenceProvider{t: t, steps: []step{writeFileStep("- [ ] a\n"), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)

	if err := Plan(context.Background(), ag, ag.UI, dir, nil); err != nil {
		t.Fatal(err)
	}
	if prompt := firstUserContent(p.sentReqs[0]); strings.Contains(prompt, "HUMAN REVIEWER") {
		t.Errorf("plain /plan must not use the human-review rules")
	}
}

func TestPlanForHumanNotifiesOnError(t *testing.T) {
	dir := t.TempDir() // no requirements.md
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})
	capture := newCaptureNotify(t)

	if err := PlanForHuman(context.Background(), ag, ag.UI, dir, capture.notifier()); err == nil {
		t.Fatal("expected an error")
	}
	if capture.calls != 1 || !strings.Contains(capture.title, "/plan_for_human failed") {
		t.Errorf("unexpected failure notification: %d %q", capture.calls, capture.title)
	}
}

// ---- /coding and /autocoding notifications with a guide ----

func TestRunOneTaskNotifiesPhaseGuideWhenPhaseCompletes(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, strings.Replace(guidedProgress, "- [ ] build login form", "- [x] build login form", 1))
	done := strings.Replace(guidedProgress, "- [ ] build login form", "- [x] build login form", 1)
	done = strings.Replace(done, "- [ ] wire login API", "- [x] wire login API", 1)
	p := &sequenceProvider{t: t, steps: []step{writeFileStep(done), contentStep("done")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capture.title, "/coding phase complete") {
		t.Errorf("expected a phase-complete title, got %q", capture.title)
	}
	for _, want := range []string{"Completed: \"wire login API\"", "PHASE COMPLETE: Phase 1: Login", "Dashboard appears", "HOW TO RUN", "npm start"} {
		if !strings.Contains(capture.body, want) {
			t.Errorf("body missing %q:\n%s", want, capture.body)
		}
	}
	if strings.Contains(capture.body, "3 sample orders") {
		t.Errorf("the next phase's check must not be sent yet:\n%s", capture.body)
	}
	if prompt := firstUserContent(p.sentReqs[0]); !strings.Contains(prompt, "notes for a human reviewer") {
		t.Errorf("expected the keep-the-guide-accurate note in the task prompt, got: %s", prompt)
	}
}

func TestRunOneTaskMidPhaseCompletionHasNoGuide(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, guidedProgress)
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep(strings.Replace(guidedProgress, "- [ ] build login form", "- [x] build login form", 1)), contentStep("done"),
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if capture.title != "jonnyq: /coding complete" || strings.Contains(capture.body, "PHASE COMPLETE") || strings.Contains(capture.body, "HOW TO") {
		t.Errorf("expected a plain completion notification:\n%s: %s", capture.title, capture.body)
	}
}

func TestRunOneTaskNotifiesFullGuideWhenAllDone(t *testing.T) {
	dir := t.TempDir()
	almost := strings.Replace(guidedProgress, "- [ ] build login form", "- [x] build login form", 1)
	almost = strings.Replace(almost, "- [ ] wire login API", "- [x] wire login API", 1)
	writeProgress(t, dir, almost)
	all := strings.Replace(almost, "- [ ] build orders list", "- [x] build orders list", 1)
	p := &sequenceProvider{t: t, steps: []step{writeFileStep(all), contentStep("done")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := RunOneTask(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capture.title, "/coding all tasks complete") {
		t.Errorf("expected an all-tasks title, got %q", capture.title)
	}
	for _, want := range []string{"3/3 tasks done", "HOW TO VERIFY", "Phase 1: Login [done]", "Phase 2: Orders [done]"} {
		if !strings.Contains(capture.body, want) {
			t.Errorf("body missing %q:\n%s", want, capture.body)
		}
	}
}

func TestAutoRunNotifiesOnceWhenAllDoneWithGuide(t *testing.T) {
	dir := t.TempDir()
	req := writeReq(t, dir, "x")
	writeProgress(t, dir, "# T\n\n> How to run: go run .\n\n## Phase 1: A\n\n> Human check: open / -> hello\n\n- [ ] only task\n")
	if err := writeStoredHash(filepath.Join(dir, hashFile), fileHash(req)); err != nil {
		t.Fatal(err)
	}
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("# T\n\n> How to run: go run .\n\n## Phase 1: A\n\n> Human check: open / -> hello\n\n- [x] only task\n"), contentStep("done"),
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := AutoRun(context.Background(), ag, ag.UI, dir, capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if capture.calls != 1 || !strings.Contains(capture.title, "/autocoding complete") {
		t.Fatalf("expected exactly one final notification, got %d (%q)", capture.calls, capture.title)
	}
	for _, want := range []string{"All tasks complete: 1/1 tasks done", "HOW TO RUN", "go run .", "open / -> hello"} {
		if !strings.Contains(capture.body, want) {
			t.Errorf("body missing %q:\n%s", want, capture.body)
		}
	}
}

// firstUserContent returns the content of the first user-role message of a
// sent request (the prompt a turn started with).
func firstUserContent(req llm.ChatRequest) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			return m.Content
		}
	}
	return ""
}
