package coding

// /coding_phase: like /autocoding, but only until the current phase of
// .progress is finished, so a person can verify each phase (see
// /plan_for_human) before the next one starts.

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jonnyq/internal/agent"
	"jonnyq/internal/llm"
	"jonnyq/internal/notify"
	"jonnyq/internal/ui"
)

// maxListedPhaseTasks caps how many completed task names a phase
// notification lists.
const maxListedPhaseTasks = 20

// phaseLabelRe pulls the number out of a heading like "Phase 3: Orders".
var phaseLabelRe = regexp.MustCompile(`(?i)^phase\s+(\d+)\b`)

// describePhases lists the phases that have tasks, for error messages.
func describePhases(d progressDoc) string {
	var lines []string
	for _, p := range d.phases {
		if len(p.tasks) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s (%d/%d tasks done)", p.name, p.doneCount(), len(p.tasks)))
	}
	return strings.Join(lines, "\n")
}

// selectPhase picks the phase /coding_phase should work on. With no arg it
// is the first phase that still has an unchecked task (-1 when every task is
// done). With a number N it is the phase headed "Phase N", or, when no
// heading carries that label, the Nth phase that has tasks.
func selectPhase(d progressDoc, arg string) (int, error) {
	if !hasAnyTasks(d) {
		return 0, fmt.Errorf("%s has no tasks; run /plan or /plan_for_human first", progressFile)
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return d.firstIncompletePhase(), nil
	}
	numStr := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(arg), "phase"))
	n, err := strconv.Atoi(numStr)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("usage: /coding_phase [phase number] - %q is not a phase number", arg)
	}
	for i, p := range d.phases {
		if m := phaseLabelRe.FindStringSubmatch(p.name); m != nil && len(p.tasks) > 0 {
			if label, _ := strconv.Atoi(m[1]); label == n {
				return i, nil
			}
		}
	}
	pos := 0
	for i, p := range d.phases {
		if len(p.tasks) == 0 {
			continue
		}
		pos++
		if pos == n {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s has no phase %d. Available phases:\n%s", progressFile, n, describePhases(d))
}

func hasAnyTasks(d progressDoc) bool {
	for _, p := range d.phases {
		if len(p.tasks) > 0 {
			return true
		}
	}
	return false
}

// findPhase returns the index of the phase named name, or -1.
func findPhase(d progressDoc, name string) int {
	for i, p := range d.phases {
		if p.name == name {
			return i
		}
	}
	return -1
}

// CodingPhase is /coding_phase: it works through the tasks of one phase of
// .progress in one run, then stops. arg selects the phase by number (see
// selectPhase); empty means the next phase that still has unfinished tasks.
// Like /coding, .progress must already exist (from /plan or
// /plan_for_human). n, when enabled, is notified when the phase finishes -
// with that phase's human check and how to run the app when .progress has
// them, or the whole guide once every task is done - and on failure.
func CodingPhase(ctx context.Context, ag *agent.Agent, w *ui.Writer, workDir, arg string, n *notify.Notifier) (err error) {
	start := time.Now()
	defer func() { err = notifyFailure(ctx, n, w, "jonnyq: /coding_phase failed", start, err) }()

	progPath := filepath.Join(workDir, progressFile)
	doc, derr := parseProgressDoc(progPath)
	if derr != nil {
		return fmt.Errorf("%s not found; run /plan or /plan_for_human first to generate it from %s", progressFile, requirementsFile)
	}
	idx, err := selectPhase(doc, arg)
	if err != nil {
		return err
	}

	if idx == -1 { // nothing left anywhere
		w.Plainln("[coding_phase] all tasks in " + progressFile + " are checked off")
		tasks, _ := parseProgress(progPath)
		summary := fmt.Sprintf("All tasks already complete: %s", progressSummaryLine(tasks))
		if g := doc.guideText(); g != "" {
			summary += "\n\n" + truncateBytes(g, notifyGuideLimit)
		}
		notifyOrWarn(ctx, n, w, "jonnyq: /coding_phase complete", withTiming(summary, start, time.Now(), llm.Usage{}))
		return nil
	}

	target := doc.phases[idx]
	if target.complete() {
		w.Plainln(fmt.Sprintf("[coding_phase] %s is already complete", target.name))
		return nil
	}
	if earlier := doc.firstIncompletePhase(); earlier < idx {
		w.Plainln(fmt.Sprintf("[coding_phase] note: %s still has unfinished tasks; working on the phase you chose", doc.phases[earlier].name))
	}
	if target.name == "(no phase)" || len(doc.phases) == 1 {
		w.Plainln("[coding_phase] note: " + progressFile + " has a single section, so this covers every task (same as /autocoding)")
	}
	w.Plainln(fmt.Sprintf("[coding_phase] %s (%d of %d tasks remaining)", target.name, len(target.tasks)-target.doneCount(), len(target.tasks)))

	ag.CodingMode = true
	defer func() { ag.CodingMode = false }()

	name := target.name
	ranTurn, err := runTaskLoop(ctx, ag, w, workDir, "coding_phase", func() ([]task, task, bool, error) {
		d, err := parseProgressDoc(progPath)
		if err != nil {
			return nil, task{}, false, fmt.Errorf("reading %s: %w", progressFile, err)
		}
		i := findPhase(d, name)
		if i < 0 {
			return nil, task{}, false, fmt.Errorf("phase %q is no longer in %s (it was renamed or removed while working); check %s manually", name, progressFile, progressFile)
		}
		next, ok := firstUnfinished(d.phases[i].tasks)
		return d.phases[i].tasks, next, ok, nil
	})
	if err != nil {
		return err
	}

	// Phase finished: report it, with how to verify it.
	docAfter, _ := parseProgressDoc(progPath)
	tasksAfter, _ := parseProgress(progPath)
	i := findPhase(docAfter, name)
	if i < 0 {
		i = idx
	}
	w.Plainln(fmt.Sprintf("[coding_phase] %s is complete", name))

	var sb strings.Builder
	fmt.Fprintf(&sb, "Phase complete: %s\n%s", name, progressSummaryLine(tasksAfter))
	if i < len(docAfter.phases) {
		sb.WriteString("\n\nTasks in this phase:")
		for k, t := range docAfter.phases[i].tasks {
			if k == maxListedPhaseTasks {
				fmt.Fprintf(&sb, "\n- ... and %d more", len(docAfter.phases[i].tasks)-k)
				break
			}
			fmt.Fprintf(&sb, "\n- %s", t.text)
		}
	}
	title := "jonnyq: /coding_phase complete"
	if extra, kind := completionGuide(progPath, i, tasksAfter); extra != "" {
		sb.WriteString("\n\n" + truncateBytes(extra, notifyGuideLimit))
		title = "jonnyq: /coding_phase " + kind + " complete"
	} else if !docAfter.hasGuide() {
		sb.WriteString("\n\nNo verification notes in " + progressFile + "; run /plan_for_human to have them written.")
	}
	u := llm.Usage{}
	if ranTurn {
		u = ag.LastUsage // the last task's turn only; the duration covers the whole phase
	}
	notifyOrWarn(ctx, n, w, title, withTiming(sb.String(), start, time.Now(), u))
	return nil
}
