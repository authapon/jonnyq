// Package coding implements the /coding automation: turn requirements.md
// into a tracked task checklist (.progress) and drive the agent through it
// one task at a time until everything is implemented, verified, and checked
// off. The actual writing/compiling/testing is done by the agent itself
// (it already has read_file/write_file/edit_file/run_command); this package
// only owns the outer loop and progress tracking.
package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"jonnyq/internal/agent"
	"jonnyq/internal/ui"
)

const (
	requirementsFile = "requirements.md"
	progressFile     = ".progress"
	// maxStallRounds aborts the loop if the same task stays unchecked after
	// this many consecutive attempts, so a genuinely stuck task surfaces to
	// the user instead of looping forever unnoticed.
	maxStallRounds = 5
)

var checklistRe = regexp.MustCompile(`^- \[([ xX])\]\s*(.+)$`)

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

// Run drives the /coding automation in workDir.
func Run(ctx context.Context, ag *agent.Agent, w *ui.Writer, workDir string) error {
	reqPath := filepath.Join(workDir, requirementsFile)
	if _, err := os.Stat(reqPath); err != nil {
		return fmt.Errorf("%s not found in the working directory", requirementsFile)
	}
	progPath := filepath.Join(workDir, progressFile)

	if _, err := os.Stat(progPath); err != nil {
		w.Plainln("[coding] no " + progressFile + " found, generating task list from " + requirementsFile)
		prompt := fmt.Sprintf(
			"Read %s and break its requirements down into a checklist of small, independently verifiable implementation tasks. "+
				"Write the checklist to %s as GitHub-style markdown checkboxes, one task per line: \"- [ ] <task>\". "+
				"Do not implement anything yet - only produce the task list.",
			requirementsFile, progressFile,
		)
		if err := ag.RunTurn(ctx, prompt); err != nil {
			return fmt.Errorf("generating %s: %w", progressFile, err)
		}
		if _, err := os.Stat(progPath); err != nil {
			return fmt.Errorf("model did not create %s", progressFile)
		}
	}

	lastText := ""
	stall := 0
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
			w.Plainln("[coding] all tasks in " + progressFile + " are checked off")
			return nil
		}

		if next.text == lastText {
			stall++
			if stall >= maxStallRounds {
				return fmt.Errorf("task %q made no progress after %d attempts; check %s and requirements.md manually", next.text, maxStallRounds, progressFile)
			}
		} else {
			lastText = next.text
			stall = 0
		}

		w.Plainln(fmt.Sprintf("[coding] working on: %s", next.text))
		prompt := fmt.Sprintf(
			"Work on this task from %s: %q\n\n"+
				"Implement it, then compile/build and run relevant tests to verify it actually works, fixing any bugs you find. "+
				"Only once it is verified working, mark it done in %s by changing its checkbox from \"- [ ]\" to \"- [x]\" using edit_file. "+
				"If you discover the task needs to be split into smaller steps, edit %s to reflect that instead of marking it done.",
			requirementsFile, next.text, progressFile, progressFile,
		)
		if err := ag.RunTurn(ctx, prompt); err != nil {
			return fmt.Errorf("working on %q: %w", next.text, err)
		}
	}
}
