// Package coding implements the /coding automation: turn requirements.md
// into a tracked task checklist (.progress) and drive the agent through it
// one task at a time until everything is implemented, verified, and checked
// off. The actual writing/compiling/testing is done by the agent itself
// (it already has read_file/write_file/edit_file/run_command); this package
// only owns the outer loop, progress tracking, and detecting when
// requirements.md has changed so the plan can be reconciled incrementally
// instead of starting over.
package coding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// hashFile records the requirements.md content hash as of the last time
	// .progress was generated or reconciled, so a later /coding run can
	// tell whether requirements.md changed since and needs reconciling.
	hashFile = ".progress.hash"
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

// Run drives the /coding automation in workDir.
func Run(ctx context.Context, ag *agent.Agent, w *ui.Writer, workDir string) error {
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
		w.Plainln("[coding] no " + progressFile + " found, generating task list from " + requirementsFile)
		prompt := fmt.Sprintf(
			"Read %s and break its requirements down into a checklist of small, independently verifiable implementation tasks, "+
				"ordered so each task's dependencies come before it. Write the checklist to %s as GitHub-style markdown checkboxes, "+
				"one task per line: \"- [ ] <task>\" - each description concrete enough that it's unambiguous when it's done. "+
				"Include an initial task for any missing project scaffolding/toolchain setup, and a final task that verifies the "+
				"whole thing end-to-end. Do not implement anything yet - only produce the task list.",
			requirementsFile, progressFile,
		)
		if err := ag.RunTurn(ctx, prompt); err != nil {
			return fmt.Errorf("generating %s: %w", progressFile, err)
		}
		if _, err := os.Stat(progPath); err != nil {
			return fmt.Errorf("model did not create %s", progressFile)
		}
		if err := writeStoredHash(hashPath, currentHash); err != nil {
			return fmt.Errorf("recording requirements hash: %w", err)
		}
	} else if readStoredHash(hashPath) != currentHash {
		w.Plainln("[coding] " + requirementsFile + " changed since " + progressFile + " was last updated; reconciling")
		prompt := fmt.Sprintf(
			"%s has changed since %s was last updated. Compare the current content of %s against %s and the existing code, "+
				"then update %s: add new unchecked tasks (\"- [ ] ...\") for anything new or changed that still needs work, and if "+
				"a previously completed task (\"- [x]\") no longer matches the current requirement, uncheck it back to \"- [ ]\" and "+
				"adjust its description to reflect what's actually needed now. Leave unrelated existing tasks and their checked "+
				"state as-is. Do not implement anything yet - only update the task list.",
			requirementsFile, progressFile, requirementsFile, progressFile, progressFile,
		)
		if err := ag.RunTurn(ctx, prompt); err != nil {
			return fmt.Errorf("reconciling %s: %w", progressFile, err)
		}
		if err := writeStoredHash(hashPath, currentHash); err != nil {
			return fmt.Errorf("recording requirements hash: %w", err)
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
				"Implement it, then actually run the real build and test commands for this project via run_command in this same "+
				"turn, and fix any failures until they genuinely pass - do not skip this or assume it would pass. Only once you "+
				"have seen it pass in this turn, mark it done in %s by changing its checkbox from \"- [ ]\" to \"- [x]\" using "+
				"edit_file. If the task has nothing to build or test (e.g. documentation only), say so explicitly instead of "+
				"marking it done without verification. If you discover the task needs to be split into smaller steps, edit %s "+
				"to reflect that instead of marking it done.",
			requirementsFile, next.text, progressFile, progressFile,
		)
		if err := ag.RunTurn(ctx, prompt); err != nil {
			return fmt.Errorf("working on %q: %w", next.text, err)
		}
	}
}
