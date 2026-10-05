package coding

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"jonnyq/internal/notify"
)

// checkOff returns content with the first occurrence of each task's
// checkbox ticked.
func checkOff(content string, tasks ...string) string {
	for _, t := range tasks {
		content = strings.Replace(content, "- [ ] "+t, "- [x] "+t, 1)
	}
	return content
}

func TestCodingPhaseStopsAtPhaseBoundaryAndNotifiesGuide(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, guidedProgress)
	step1 := checkOff(guidedProgress, "build login form")
	step2 := checkOff(step1, "wire login API")
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep(step1), contentStep("ok"),
		writeFileStep(step2), contentStep("ok"),
	}} // a third task turn would fail the test: Phase 2 must not start
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "", capture.notifier()); err != nil {
		t.Fatalf("CodingPhase failed: %v", err)
	}
	if p.calls != 4 {
		t.Errorf("expected exactly the two Phase 1 task turns (4 Chat calls), got %d", p.calls)
	}
	tasks, _ := parseProgress(filepath.Join(dir, progressFile))
	if countDone(tasks) != 2 {
		t.Errorf("expected only Phase 1's two tasks done, got %d", countDone(tasks))
	}
	if capture.calls != 1 || !strings.Contains(capture.title, "/coding_phase phase complete") {
		t.Fatalf("expected one phase-complete notification, got %d (%q)", capture.calls, capture.title)
	}
	for _, want := range []string{
		"Phase complete: Phase 1: Login", "2/3 tasks done", "- build login form", "- wire login API",
		"PHASE COMPLETE: Phase 1: Login", "Dashboard appears", "HOW TO RUN", "npm start", "Duration:",
	} {
		if !strings.Contains(capture.body, want) {
			t.Errorf("body missing %q:\n%s", want, capture.body)
		}
	}
	if strings.Contains(capture.body, "3 sample orders") {
		t.Errorf("the next phase's check must not be included:\n%s", capture.body)
	}
}

func TestCodingPhaseDefaultsToNextIncompletePhase(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, checkOff(guidedProgress, "build login form", "wire login API"))
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep(checkOff(guidedProgress, "build login form", "wire login API", "build orders list")), contentStep("ok"),
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "", capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if prompt := lastUserContent(p.sentReqs[0]); !strings.Contains(prompt, "build orders list") {
		t.Errorf("expected the Phase 2 task to be worked on, got: %s", prompt)
	}
	// Last phase: every task is done, so the whole guide goes out.
	if !strings.Contains(capture.title, "/coding_phase all tasks complete") || !strings.Contains(capture.body, "HOW TO VERIFY") {
		t.Errorf("expected the all-tasks guide, got %q:\n%s", capture.title, capture.body)
	}
}

func TestCodingPhaseWithPhaseNumber(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, guidedProgress)
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep(checkOff(guidedProgress, "build orders list")), contentStep("ok"),
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	// Phase 1 is still unfinished, but the user asked for phase 2.
	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "2", capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if prompt := lastUserContent(p.sentReqs[0]); !strings.Contains(prompt, "build orders list") {
		t.Errorf("expected phase 2 to be worked on, got: %s", prompt)
	}
	if !strings.Contains(capture.body, "PHASE COMPLETE: Phase 2: Orders") || !strings.Contains(capture.body, "3 sample orders") {
		t.Errorf("expected phase 2's check in the notification:\n%s", capture.body)
	}
}

func TestCodingPhaseNumberAcceptsPhasePrefixAndFallsBackToPosition(t *testing.T) {
	doc := progressDoc{phases: []phaseInfo{
		{name: "Setup", tasks: []task{{text: "a"}}},
		{name: "Notes only"},
		{name: "Orders", tasks: []task{{text: "b"}}},
	}}
	// No heading is labeled "Phase N": N counts phases that have tasks.
	if i, err := selectPhase(doc, "2"); err != nil || i != 2 {
		t.Errorf("positional fallback: got %d, %v", i, err)
	}
	labeled := progressDoc{phases: []phaseInfo{
		{name: "Phase 0: Bootstrap", tasks: []task{{text: "a"}}},
		{name: "Phase 1: Login", tasks: []task{{text: "b"}}},
	}}
	// "Phase 1" matches the label, not the position.
	if i, err := selectPhase(labeled, "Phase 1"); err != nil || i != 1 {
		t.Errorf("label match: got %d, %v", i, err)
	}
}

func TestCodingPhaseRejectsBadArguments(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, guidedProgress)
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})

	err := CodingPhase(context.Background(), ag, ag.UI, dir, "9", nil)
	if err == nil || !strings.Contains(err.Error(), "no phase 9") || !strings.Contains(err.Error(), "Phase 2: Orders") {
		t.Errorf("expected an out-of-range error listing the phases, got: %v", err)
	}
	err = CodingPhase(context.Background(), ag, ag.UI, dir, "login", nil)
	if err == nil || !strings.Contains(err.Error(), "usage: /coding_phase") {
		t.Errorf("expected a usage error, got: %v", err)
	}
}

func TestCodingPhaseErrorsWithoutProgress(t *testing.T) {
	dir := t.TempDir()
	ag := newTestAgent(t, dir, &sequenceProvider{t: t})
	capture := newCaptureNotify(t)

	err := CodingPhase(context.Background(), ag, ag.UI, dir, "", capture.notifier())
	if err == nil || !strings.Contains(err.Error(), "/plan or /plan_for_human") {
		t.Errorf("expected a hint to run /plan or /plan_for_human, got: %v", err)
	}
	if capture.calls != 1 || !strings.Contains(capture.title, "/coding_phase failed") || !notify.IsNotified(err) {
		t.Errorf("expected one failure notification, got %d (%q)", capture.calls, capture.title)
	}
}

func TestCodingPhaseAlreadyCompletePhaseDoesNothing(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, checkOff(guidedProgress, "build login form", "wire login API"))
	p := &sequenceProvider{t: t} // any Chat call fails the test
	ag := newTestAgent(t, dir, p)

	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "1", failIfCalledNotify(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCodingPhaseAllDoneResendsGuideWithoutModelCall(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, checkOff(guidedProgress, "build login form", "wire login API", "build orders list"))
	p := &sequenceProvider{t: t}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "", capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || capture.calls != 1 || !strings.Contains(capture.body, "All tasks already complete") || !strings.Contains(capture.body, "HOW TO VERIFY") {
		t.Errorf("unexpected: calls=%d notifications=%d\n%s", p.calls, capture.calls, capture.body)
	}
}

func TestCodingPhaseWithoutGuideSuggestsPlanForHuman(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, "# T\n\n## Phase 1: A\n\n- [ ] one\n\n## Phase 2: B\n\n- [ ] two\n")
	p := &sequenceProvider{t: t, steps: []step{
		writeFileStep("# T\n\n## Phase 1: A\n\n- [x] one\n\n## Phase 2: B\n\n- [ ] two\n"), contentStep("ok"),
	}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "", capture.notifier()); err != nil {
		t.Fatal(err)
	}
	if capture.title != "jonnyq: /coding_phase complete" || !strings.Contains(capture.body, "Phase complete: Phase 1: A") || !strings.Contains(capture.body, "/plan_for_human") {
		t.Errorf("expected a plain phase notification with a /plan_for_human hint, got %q:\n%s", capture.title, capture.body)
	}
}

func TestCodingPhaseAbortsWhenPhaseDisappears(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, guidedProgress)
	renamed := strings.Replace(guidedProgress, "## Phase 1: Login", "## Phase 1: Auth", 1)
	p := &sequenceProvider{t: t, steps: []step{writeFileStep(renamed), contentStep("ok")}}
	ag := newTestAgent(t, dir, p)
	capture := newCaptureNotify(t)

	err := CodingPhase(context.Background(), ag, ag.UI, dir, "", capture.notifier())
	if err == nil || !strings.Contains(err.Error(), "no longer in") {
		t.Fatalf("expected an error about the vanished phase, got: %v", err)
	}
	if capture.calls != 1 || !strings.Contains(capture.title, "failed") {
		t.Errorf("expected a failure notification, got %d (%q)", capture.calls, capture.title)
	}
}

func TestCodingPhaseResetsCodingMode(t *testing.T) {
	dir := t.TempDir()
	writeProgress(t, dir, "## Phase 1: A\n\n- [ ] one\n")
	ag := newTestAgent(t, dir, &sequenceProvider{t: t, steps: []step{writeFileStep("## Phase 1: A\n\n- [x] one\n"), contentStep("ok")}})
	if err := CodingPhase(context.Background(), ag, ag.UI, dir, "", nil); err != nil {
		t.Fatal(err)
	}
	if ag.CodingMode {
		t.Error("CodingMode must be switched off when /coding_phase returns")
	}
}
