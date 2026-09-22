// Package coding implements the /plan, /coding, and /autocoding automation:
// turn requirements.md into a tracked task checklist (.progress), and drive
// the agent through it - either one task at a time or all the way through.
// The actual writing/compiling/testing is done by the agent itself (it
// already has read_file/write_file/edit_file/run_command); this package
// only owns planning, progress tracking, and detecting when requirements.md
// has changed so the plan can be reconciled incrementally instead of
// starting over.
package coding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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

const (
	requirementsFile = "requirements.md"
	progressFile     = ".progress"
	// hashFile records the requirements.md content hash as of the last time
	// .progress was generated or reconciled, so a later /plan can tell
	// whether requirements.md changed since and needs reconciling.
	hashFile = ".progress.hash"
	// retryFile persists how many consecutive /coding invocations have
	// picked the same still-unfinished task, since (unlike /autocoding)
	// /coding runs one task per invocation with no in-memory loop to track
	// it across calls.
	retryFile = ".progress.retry"
	// maxStallRounds aborts once the same task stays unchecked after this
	// many consecutive attempts, so a genuinely stuck task surfaces to the
	// user instead of being retried forever unnoticed.
	maxStallRounds = 5
	// maxNoProgressRounds is AutoRun's broader, independent safety valve on
	// top of maxStallRounds: it counts consecutive rounds where NOT ONE
	// task anywhere in .progress newly went from unchecked to checked,
	// regardless of whether the "next" unfinished task's text happens to
	// differ round to round (e.g. the model rewords a task, reorders the
	// list, or thrashes between several half-finished tasks). The
	// same-task stall counter alone cannot catch that pattern, since it
	// only tracks repeats of one exact task text - without this, a run
	// where the "next" task's text keeps changing has no bound and can
	// loop for hours. Larger than maxStallRounds since legitimately
	// working through several small tasks before the first one lands is
	// normal.
	maxNoProgressRounds = 15
)

var checklistRe = regexp.MustCompile(`^- \[([ xX])\]\s*(.+)$`)

// progressLanguageRule is included in every prompt that writes to
// .progress. Some models write noticeably worse task descriptions in
// languages other than English even when requirements.md itself is in
// another language, so the checklist stays in English regardless of the
// requirement's language; only proper nouns/labels quoted from the
// requirement should keep their original language.
const progressLanguageRule = "Write task descriptions in %s in English, even if %s is written in another language. " +
	"Only keep names, labels, or short quoted terms from %s in their original language where useful for reference."

type task struct {
	done bool
	text string
}

func parseProgress(path string) ([]task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tasks []task
	for _, line := range strings.Split(string(data), "\n") {
		if m := checklistRe.FindStringSubmatch(line); m != nil {
			tasks = append(tasks, task{done: strings.ToLower(m[1]) == "x", text: m[2]})
		}
	}
	return tasks, nil
}

func firstUnfinished(tasks []task) (task, bool) {
	for _, t := range tasks {
		if !t.done {
			return t, true
		}
	}
	return task{}, false
}

func countDone(tasks []task) int {
	n := 0
	for _, t := range tasks {
		if t.done {
			n++
		}
	}
	return n
}

func progressSummaryLine(tasks []task) string {
	return fmt.Sprintf("%d/%d tasks done", countDone(tasks), len(tasks))
}

// notifyOrWarn sends a completion notification via n, printing a non-fatal
// warning through w if it fails - a broken notification endpoint must
// never fail the underlying /plan or /coding command. A no-op (no
// warning) when n is disabled.
func notifyOrWarn(ctx context.Context, n *notify.Notifier, w *ui.Writer, title, message string) {
	if !n.Enabled() {
		return
	}
	if err := n.Send(ctx, title, message); err != nil {
		w.Plainln("warning: ntfy notification failed: " + err.Error())
	}
}

// notifyTimestampLayout matches the timezone-qualified format already used
// for the "current date and time" line in the system message, so a
// notification's timestamps read consistently with what the model itself
// was told.
const notifyTimestampLayout = "2006-01-02 15:04:05 MST"

// withTiming appends a start/finish/duration/stats footer to summary, using
// u (typically ag.LastUsage right after the RunTurn call that did the
// work) for the same preload/prompt_eval/thinking/token figures shown in
// the terminal's own metrics line - so a notification tells the full story
// without needing the terminal open. Pass a zero-value llm.Usage{} when no
// RunTurn call happened (e.g. "all tasks already complete"); FormatUsage
// renders that as all "n/a" rather than stale figures from an unrelated
// earlier turn.
func withTiming(summary string, start, end time.Time, u llm.Usage) string {
	return fmt.Sprintf(
		"%s\n\nStarted: %s\nFinished: %s\nDuration: %s\nStats: %s",
		summary,
		start.Format(notifyTimestampLayout),
		end.Format(notifyTimestampLayout),
		end.Sub(start).Round(time.Millisecond),
		agent.FormatUsage(u),
	)
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readStoredHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeStoredHash(path, hash string) error {
	return os.WriteFile(path, []byte(hash), 0o644)
}

// retryState is the persisted "same task, N attempts in a row" counter used
// by RunOneTask (/coding) across separate invocations. untouched records
// whether the most recent attempt left .progress's raw content completely
// unchanged (i.e. never called edit_file/write_file on it at all), as
// opposed to having edited it but still left this task unchecked - these
// are different failure modes and get different diagnostic notes.
type retryState struct {
	text      string
	count     int
	untouched bool
}

func loadRetryState(path string) retryState {
	data, err := os.ReadFile(path)
	if err != nil {
		return retryState{}
	}
	parts := strings.SplitN(strings.TrimRight(string(data), "\n"), "\t", 3)
	if len(parts) != 3 {
		return retryState{}
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return retryState{}
	}
	return retryState{count: n, untouched: parts[1] == "1", text: parts[2]}
}

func writeRetryState(path string, s retryState) error {
	untouched := "0"
	if s.untouched {
		untouched = "1"
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\t%s\t%s", s.count, untouched, s.text)), 0o644)
}

// progressUnchanged reports whether .progress's raw content is identical
// before and after an attempt, i.e. the model never called
// edit_file/write_file on it at all (a read error counts as "changed" so a
// missing/unreadable file never falsely reports "unchanged").
func progressUnchanged(path string, before []byte) bool {
	after, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(before, after)
}

func clearRetryState(path string) {
	_ = os.Remove(path)
}

func buildGeneratePrompt() string {
	return fmt.Sprintf(
		"Read %s and break its requirements down into a checklist of small, independently verifiable implementation tasks, "+
			"ordered so each task's dependencies come before it. Also look at the existing code in the working directory, if "+
			"any - don't propose tasks for things already implemented, and account for what's already there. Write the "+
			"checklist to %s as GitHub-style markdown checkboxes, one task per line: \"- [ ] <task>\" - each description "+
			"concrete enough that it's unambiguous when it's done. Include an initial task for any missing project "+
			"scaffolding/toolchain setup, and a final task that verifies the whole thing end-to-end. "+progressLanguageRule+" "+
			"Do not implement anything yet - only produce the task list.",
		requirementsFile, progressFile, progressFile, requirementsFile, requirementsFile,
	)
}

func buildReconcilePrompt() string {
	return fmt.Sprintf(
		"%s has changed since %s was last updated. Compare the current content of %s against %s and the existing code in "+
			"the working directory, then update %s: add new unchecked tasks (\"- [ ] ...\") for anything new or changed that "+
			"still needs work, and if a previously completed task (\"- [x]\") no longer matches the current requirement or "+
			"the actual code, uncheck it back to \"- [ ]\" and adjust its description to reflect what's actually needed now. "+
			"Leave unrelated existing tasks and their checked state as-is. "+progressLanguageRule+" "+
			"Do not implement anything yet - only update the task list.",
		requirementsFile, progressFile, requirementsFile, progressFile, progressFile, progressFile, requirementsFile, requirementsFile,
	)
}

func buildTaskPrompt(taskText string, stall int, prevUntouched bool) string {
	var retryNote string
	switch {
	case stall > 0 && prevUntouched:
		// The model's previous attempt never called edit_file/write_file on
		// .progress at all - it only ran shell commands and/or narrated the
		// change in its reply text, which changes nothing by itself. This
		// is a different failure mode from a failed edit (below), and
		// pointing at "check your old_string match" would be misleading
		// when no edit was even attempted, so call it out specifically.
		retryNote = fmt.Sprintf(
			"Note: this task was already attempted %d time(s) before. In your last attempt you did NOT call edit_file or "+
				"write_file on %s at all - describing the change in your reply, or printing success messages via "+
				"run_command (e.g. echo), does not change the file and does not count as finishing the task. This time, "+
				"after you've genuinely verified it, you MUST make a real edit_file (preferred) or write_file tool call "+
				"that changes this task's checkbox in %s from \"- [ ]\" to \"- [x]\".\n\n",
			stall, progressFile, progressFile,
		)
	case stall > 0:
		// The same task came back unchecked after a previous attempt that
		// did touch .progress. Left as a plain "work on this task" prompt,
		// models have been observed to just repeat "I already did this and
		// verified it" without noticing that .progress on disk still shows
		// it unchecked - typically because an earlier edit_file call
		// silently failed to match (its old_string didn't exactly match the
		// current line) or a write_file rewrite of the whole file was based
		// on a stale, pre-edit copy in the model's context. Point this out
		// explicitly so the model investigates instead of repeating the
		// same unverified claim.
		retryNote = fmt.Sprintf(
			"Note: this task was already attempted %d time(s) before and is STILL shown as unchecked (\"- [ ]\") in %s right "+
				"now. Do not assume you already finished it, even if it looks familiar - re-check from scratch: read_file %s "+
				"and your implementation files to see their real current state, figure out concretely why the checkbox didn't "+
				"end up set (a common cause: edit_file's old_string must match the line in %s exactly, or a prior write_file "+
				"rewrote %s from a stale in-memory copy and clobbered the change), and fix it for real this time.\n\n",
			stall, progressFile, progressFile, progressFile, progressFile,
		)
	}
	return fmt.Sprintf(
		"%sWork on this task from %s: %q\n\n"+
			"Implement it, then actually run the real build and test commands for this project via run_command in this same "+
			"turn, and fix any failures until they genuinely pass - do not skip this or assume it would pass. Before changing "+
			"%s, always read_file it first to see its exact current content - never edit it from memory, since your view of it "+
			"may be stale. Prefer edit_file for a single checkbox change over rewriting the whole file with write_file, which "+
			"risks clobbering other tasks' state if your copy of it is out of date. Only once you have seen the build/test "+
			"pass in this turn, actually call edit_file or write_file to change this task's checkbox in %s from \"- [ ]\" to "+
			"\"- [x]\" - stating in your reply that it's done, without making that tool call, does not count and leaves the "+
			"task unfinished. If the task has nothing to build or test (e.g. documentation only), say so explicitly instead "+
			"of marking it done without verification. If you discover the task needs to be split into smaller steps, edit %s "+
			"to reflect that instead of marking it done.",
		retryNote, requirementsFile, taskText, progressFile, progressFile, progressFile,
	)
}

// Plan is /plan: it generates .progress from requirements.md if missing, or
// reconciles it if requirements.md changed since .progress was last
// generated/reconciled (checked against a stored content hash), checking
// consistency with the existing codebase either way. It never implements
// anything. If .progress already exists and requirements.md hasn't changed,
// it does nothing and reports that the plan is already up to date. n is the
// ntfy notifier to post a completion summary to when real work happened
// (generating or reconciling) - pass nil/a disabled Notifier to skip
// notifying, as AutoRun does for its own internal Plan call.
func Plan(ctx context.Context, ag *agent.Agent, w *ui.Writer, workDir string, n *notify.Notifier) error {
	start := time.Now()
	reqPath := filepath.Join(workDir, requirementsFile)
	reqData, err := os.ReadFile(reqPath)
	if err != nil {
		return fmt.Errorf("%s not found in the working directory", requirementsFile)
	}
	currentHash := fileHash(reqData)
	progPath := filepath.Join(workDir, progressFile)
	hashPath := filepath.Join(workDir, hashFile)

	ag.CodingMode = true
	defer func() { ag.CodingMode = false }()

	if _, err := os.Stat(progPath); err != nil {
		w.Plainln("[plan] no " + progressFile + " found, generating task list from " + requirementsFile)
		if err := ag.RunTurn(ctx, buildGeneratePrompt()); err != nil {
			return fmt.Errorf("generating %s: %w", progressFile, err)
		}
		tasks, err := parseProgress(progPath)
		if err != nil {
			return fmt.Errorf("model did not create %s", progressFile)
		}
		if err := writeStoredHash(hashPath, currentHash); err != nil {
			return fmt.Errorf("recording requirements hash: %w", err)
		}
		summary := fmt.Sprintf("Generated %s from %s: %s", progressFile, requirementsFile, progressSummaryLine(tasks))
		notifyOrWarn(ctx, n, w, "jonnyq: /plan complete", withTiming(summary, start, time.Now(), ag.LastUsage))
		return nil
	}

	if readStoredHash(hashPath) == currentHash {
		w.Plainln("[plan] " + progressFile + " is already up to date with " + requirementsFile)
		return nil
	}

	tasksBefore, _ := parseProgress(progPath)
	w.Plainln("[plan] " + requirementsFile + " changed since " + progressFile + " was last updated; reconciling")
	if err := ag.RunTurn(ctx, buildReconcilePrompt()); err != nil {
		return fmt.Errorf("reconciling %s: %w", progressFile, err)
	}
	if err := writeStoredHash(hashPath, currentHash); err != nil {
		return fmt.Errorf("recording requirements hash: %w", err)
	}
	tasksAfter, _ := parseProgress(progPath)
	summary := fmt.Sprintf(
		"Reconciled %s against %s: %d tasks total (was %d), %s",
		progressFile, requirementsFile, len(tasksAfter), len(tasksBefore), progressSummaryLine(tasksAfter),
	)
	notifyOrWarn(ctx, n, w, "jonnyq: /plan complete", withTiming(summary, start, time.Now(), ag.LastUsage))
	return nil
}

// RunOneTask is /coding: it works on exactly one unfinished task from
// .progress and then returns, without looping through the rest. .progress
// must already exist (via /plan or /autocoding) - it is not generated here.
// n is the ntfy notifier to post a completion summary to (all tasks
// already done, this task completed, or this task stalled out) - pass
// nil/a disabled Notifier to skip notifying.
func RunOneTask(ctx context.Context, ag *agent.Agent, w *ui.Writer, workDir string, n *notify.Notifier) error {
	start := time.Now()
	progPath := filepath.Join(workDir, progressFile)
	if _, err := os.Stat(progPath); err != nil {
		return fmt.Errorf("%s not found; run /plan first to generate it from %s", progressFile, requirementsFile)
	}

	tasks, err := parseProgress(progPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", progressFile, err)
	}
	next, ok := firstUnfinished(tasks)
	if !ok {
		w.Plainln("[coding] all tasks in " + progressFile + " are checked off")
		summary := fmt.Sprintf("All tasks already complete: %s", progressSummaryLine(tasks))
		// No RunTurn call happened, so there's no fresh LastUsage to report
		// - a zero-value llm.Usage renders as "n/a" rather than stale
		// figures from an unrelated earlier turn.
		notifyOrWarn(ctx, n, w, "jonnyq: /coding complete", withTiming(summary, start, time.Now(), llm.Usage{}))
		return nil
	}

	retryPath := filepath.Join(workDir, retryFile)
	prev := loadRetryState(retryPath)
	stall := 0
	prevUntouched := false
	if prev.text == next.text {
		stall = prev.count
		prevUntouched = prev.untouched
	}

	ag.CodingMode = true
	defer func() { ag.CodingMode = false }()

	w.Plainln(fmt.Sprintf("[coding] working on: %s", next.text))
	before, _ := os.ReadFile(progPath)
	if err := ag.RunTurn(ctx, buildTaskPrompt(next.text, stall, prevUntouched)); err != nil {
		return fmt.Errorf("working on %q: %w", next.text, err)
	}

	tasksAfter, err := parseProgress(progPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", progressFile, err)
	}
	if nextAfter, stillUnfinished := firstUnfinished(tasksAfter); stillUnfinished && nextAfter.text == next.text {
		newCount := stall + 1
		if newCount >= maxStallRounds {
			clearRetryState(retryPath)
			summary := fmt.Sprintf(
				"Task %q made no progress after %d attempts; check %s and requirements.md manually. %s",
				next.text, maxStallRounds, progressFile, progressSummaryLine(tasksAfter),
			)
			notifyOrWarn(ctx, n, w, "jonnyq: /coding stalled", withTiming(summary, start, time.Now(), ag.LastUsage))
			return fmt.Errorf("task %q made no progress after %d attempts via /coding; check %s and requirements.md manually", next.text, maxStallRounds, progressFile)
		}
		untouched := progressUnchanged(progPath, before)
		if err := writeRetryState(retryPath, retryState{text: next.text, count: newCount, untouched: untouched}); err != nil {
			return fmt.Errorf("recording retry state: %w", err)
		}
		return nil
	}
	clearRetryState(retryPath)
	summary := fmt.Sprintf("Completed: %q\n%s", next.text, progressSummaryLine(tasksAfter))
	notifyOrWarn(ctx, n, w, "jonnyq: /coding complete", withTiming(summary, start, time.Now(), ag.LastUsage))
	return nil
}

// AutoRun is /autocoding: the original /coding behavior, kept under a new
// name. It plans (as Plan does) if needed, then works through every
// unfinished task in .progress in one run instead of stopping after each.
// It never sends ntfy notifications itself (only /plan and /coding do) -
// its internal Plan call passes a disabled notifier accordingly.
func AutoRun(ctx context.Context, ag *agent.Agent, w *ui.Writer, workDir string) error {
	if err := Plan(ctx, ag, w, workDir, nil); err != nil {
		return err
	}

	progPath := filepath.Join(workDir, progressFile)

	ag.CodingMode = true
	defer func() { ag.CodingMode = false }()

	lastText := ""
	stall := 0
	untouched := false
	lastDoneCount := -1
	noProgressRounds := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tasks, err := parseProgress(progPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", progressFile, err)
		}
		next, ok := firstUnfinished(tasks)
		if !ok {
			w.Plainln("[autocoding] all tasks in " + progressFile + " are checked off")
			return nil
		}

		// Independent of the same-task check below: if the total number of
		// completed tasks hasn't grown in a while, nothing is actually
		// getting finished, no matter how "next" is drifting round to
		// round. This bounds the run even in cases the same-task counter
		// can't see.
		doneNow := countDone(tasks)
		if lastDoneCount == -1 {
			lastDoneCount = doneNow
		}
		if doneNow > lastDoneCount {
			lastDoneCount = doneNow
			noProgressRounds = 0
		} else {
			noProgressRounds++
			if noProgressRounds >= maxNoProgressRounds {
				return fmt.Errorf("no task in %s has been completed in the last %d rounds (currently on %q); aborting - check %s and requirements.md manually", progressFile, maxNoProgressRounds, next.text, progressFile)
			}
		}

		if next.text == lastText {
			stall++
			if stall >= maxStallRounds {
				return fmt.Errorf("task %q made no progress after %d attempts; check %s and requirements.md manually", next.text, maxStallRounds, progressFile)
			}
		} else {
			lastText = next.text
			stall = 0
			untouched = false
		}

		w.Plainln(fmt.Sprintf("[autocoding] working on: %s", next.text))
		// Start each task with a clean slate: no memory of the planning
		// step or any earlier task. The model re-discovers whatever it
		// needs (via read_file/run_command) instead of relying on
		// conversation history that keeps growing across the whole run.
		ag.ResetHistory()
		before, _ := os.ReadFile(progPath)
		if err := ag.RunTurn(ctx, buildTaskPrompt(next.text, stall, untouched)); err != nil {
			return fmt.Errorf("working on %q: %w", next.text, err)
		}
		untouched = progressUnchanged(progPath, before)
	}
}
