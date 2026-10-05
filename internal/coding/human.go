package coding

// Support for /plan_for_human: plans written so a person can verify the
// result by actually using the application (ideally through its frontend),
// with the verification steps stored in .progress itself as blockquote
// notes and pushed to the user via ntfy.
//
// The notes live in .progress as lines starting with "> ":
//
//	# Project Title
//
//	> How to run: <commands, URLs, seed data, test accounts>
//
//	## Phase 1: Login
//
//	> Human check: <steps a person can follow> -> <expected result>
//
//	- [ ] task ...
//
// They are not checkboxes, so parseProgress (task tracking) ignores them,
// and they survive reconciliation as part of their phase. The notification
// text is extracted from the file directly - no extra model call.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	howToRunPrefix   = "how to run:"
	humanCheckPrefix = "human check:"

	// notifyGuideLimit caps the verification-guide part of a notification.
	// ntfy limits message bodies (about 4 KB on ntfy.sh), and the summary and
	// timing footer need room too.
	notifyGuideLimit = 3000
)

var (
	phaseHeadingRe = regexp.MustCompile(`^##\s+(.+?)\s*$`)
	blockquoteRe   = regexp.MustCompile(`^>\s?(.*)$`)
)

// phaseInfo is one "## ..." section of .progress.
type phaseInfo struct {
	name  string
	tasks []task
	check string // joined "> Human check:" notes; empty when none
}

func (p phaseInfo) doneCount() int { return countDone(p.tasks) }

// complete reports whether the phase has tasks and all are checked.
func (p phaseInfo) complete() bool {
	return len(p.tasks) > 0 && p.doneCount() == len(p.tasks)
}

// progressDoc is .progress parsed into phases plus the human-verification
// notes.
type progressDoc struct {
	howToRun string
	phases   []phaseInfo
}

// hasGuide reports whether the file carries a usable verification guide: a
// "How to run" note and at least one phase "Human check".
func (d progressDoc) hasGuide() bool {
	if d.howToRun == "" {
		return false
	}
	for _, p := range d.phases {
		if p.check != "" {
			return true
		}
	}
	return false
}

// firstIncompletePhase returns the index of the first phase that still has
// an unchecked task, or -1.
func (d progressDoc) firstIncompletePhase() int {
	for i, p := range d.phases {
		if len(p.tasks) > 0 && !p.complete() {
			return i
		}
	}
	return -1
}

func parseProgressDoc(path string) (progressDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return progressDoc{}, err
	}
	var doc progressDoc
	cur := -1
	var block []string

	flush := func() {
		if len(block) == 0 {
			return
		}
		first := strings.TrimSpace(block[0])
		lower := strings.ToLower(first)
		body := func(prefix string) string {
			rest := []string{strings.TrimSpace(first[len(prefix):])}
			for _, l := range block[1:] {
				rest = append(rest, strings.TrimSpace(l))
			}
			return strings.TrimSpace(strings.Join(rest, "\n"))
		}
		switch {
		case strings.HasPrefix(lower, howToRunPrefix):
			doc.howToRun = body(howToRunPrefix)
		case strings.HasPrefix(lower, humanCheckPrefix) && cur >= 0:
			text := body(humanCheckPrefix)
			if doc.phases[cur].check != "" {
				text = doc.phases[cur].check + "\n" + text
			}
			doc.phases[cur].check = text
		}
		block = nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := blockquoteRe.FindStringSubmatch(line); m != nil {
			block = append(block, m[1])
			continue
		}
		flush()
		if m := phaseHeadingRe.FindStringSubmatch(line); m != nil {
			doc.phases = append(doc.phases, phaseInfo{name: m[1]})
			cur = len(doc.phases) - 1
			continue
		}
		if m := checklistRe.FindStringSubmatch(line); m != nil {
			if cur < 0 { // checkbox before any phase heading
				doc.phases = append(doc.phases, phaseInfo{name: "(no phase)"})
				cur = 0
			}
			doc.phases[cur].tasks = append(doc.phases[cur].tasks, task{done: strings.ToLower(m[1]) == "x", text: m[2]})
		}
	}
	flush()
	return doc, nil
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// guideText renders the full guide: how to run, then every phase's check
// with its progress. Empty when there is no guide.
func (d progressDoc) guideText() string {
	if !d.hasGuide() {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("HOW TO RUN\n")
	sb.WriteString(d.howToRun)
	sb.WriteString("\n\nHOW TO VERIFY (per phase)")
	for _, p := range d.phases {
		if p.check == "" {
			continue
		}
		status := fmt.Sprintf("%d/%d tasks done", p.doneCount(), len(p.tasks))
		if p.complete() {
			status = "done"
		}
		fmt.Fprintf(&sb, "\n\n%s [%s]\n%s", p.name, status, indent(p.check, "  "))
	}
	return sb.String()
}

// phaseGuideText renders the guide for one just-finished phase.
func (d progressDoc) phaseGuideText(idx int) string {
	p := d.phases[idx]
	var sb strings.Builder
	fmt.Fprintf(&sb, "PHASE COMPLETE: %s\nPlease verify it yourself:\n%s", p.name, indent(p.check, "  "))
	if d.howToRun != "" {
		sb.WriteString("\n\nHOW TO RUN\n")
		sb.WriteString(d.howToRun)
	}
	return sb.String()
}

// truncateBytes cuts s to at most max bytes on a rune boundary, appending a
// pointer to .progress when it had to cut.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const note = "\n... (truncated; see " + progressFile + " for the full guide)"
	cut := max - len(note)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + note
}

// Prompt fragments shared by the /plan_for_human prompts.
const (
	howToRunRule = "Right after the \"# <Project Title>\" line, add one blockquote starting with \"> How to run:\" that tells " +
		"a human exactly how to start the system and reach it: the commands, URL(s) and port(s), required environment or " +
		"configuration, seed/demo data, and any test accounts or credentials. Continue it on further lines that each begin " +
		"with \"> \"."

	humanCheckRule = "Directly under every \"## Phase N: ...\" heading (before its tasks), add a blockquote starting with " +
		"\"> Human check:\" giving concrete steps a person can follow in the browser and the exact result they should see, " +
		"e.g. \"> Human check: Open http://localhost:3000/login, sign in as demo/demo -> the Dashboard shows 3 sample " +
		"orders\". Only when the project has no frontend, use the nearest human-observable surface instead (Swagger UI, " +
		"curl commands with expected output, or CLI commands with expected output). A phase that cannot be observed " +
		"directly must say how to confirm it indirectly, or be merged into a phase that can."

	reviewTaskRule = "Make the very last task of the final phase a checkbox task to review every \"> How to run:\" and " +
		"\"> Human check:\" note against the actual implementation and correct anything that no longer matches (URLs, ports, " +
		"commands, button or menu names, credentials, expected results)."

	blockquoteRule = "Each note line must start with \"> \". Notes are not tasks: never put checkboxes in them, and keep them " +
		"in English."
)

// humanPlanRules is appended to the generate/reconcile prompts for
// /plan_for_human.
const humanPlanRules = "THIS PLAN IS FOR A HUMAN REVIEWER who will verify the work by actually using the application, " +
	"preferably through its frontend (web UI), without reading code. Therefore: (1) Organize phases as user-visible " +
	"vertical slices (e.g. \"login works end to end\") rather than technical layers, so that every phase ends with " +
	"something a person can see and try. (2) " + howToRunRule + " (3) " + humanCheckRule + " (4) " + reviewTaskRule +
	" " + blockquoteRule

func buildGenerateForHumanPrompt() string {
	return buildGeneratePrompt() + "\n\n" + humanPlanRules
}

func buildReconcileForHumanPrompt() string {
	return buildReconcilePrompt() + "\n\n" + humanPlanRules +
		" When reconciling, keep the existing \"> How to run:\" and \"> Human check:\" notes that are still accurate, " +
		"update the ones affected by the changed requirements, and add notes for any new phase. Do not change any " +
		"checkbox state because of this."
}

// buildAddChecksPrompt asks the model to add the verification notes to an
// existing .progress without touching its tasks.
func buildAddChecksPrompt() string {
	return fmt.Sprintf(
		"Read %s, %s and the existing code in the working directory, then edit %s to add notes that let a HUMAN verify "+
			"the work by actually using the application (preferably through its frontend, without reading code). "+
			"Do not change, remove, reorder, check or uncheck any existing task line - every \"- [ ]\" / \"- [x]\" line "+
			"must stay exactly as it is. Only add the following: (1) %s (2) %s (3) %s (add that one new unchecked task at "+
			"the end of the last phase if it is not already there). %s Do not implement anything - only edit %s.",
		progressFile, requirementsFile, progressFile, howToRunRule, humanCheckRule, reviewTaskRule, blockquoteRule, progressFile,
	)
}

// humanGuideNote is appended to a task prompt when .progress carries a
// verification guide, so the guide stays accurate as the code changes.
func humanGuideNote(d progressDoc) string {
	if !d.hasGuide() {
		return ""
	}
	return fmt.Sprintf(
		"\n\nNote: %s contains notes for a human reviewer (\"> How to run:\" and \"> Human check:\" lines). If your work "+
			"changes how a person would run or verify anything (URL, port, command, UI labels, credentials, expected "+
			"results), update the affected note lines in %s too. Never turn those notes into checkboxes.",
		progressFile, progressFile,
	)
}
