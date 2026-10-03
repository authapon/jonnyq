// Package agent implements the tool-calling loop: send the conversation and
// available tools to the provider, stream thinking/content back to the user,
// execute any requested tool calls, and repeat until the model produces a
// final answer. It also owns periodic context compaction and the
// end-of-turn metrics footer.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"jonnyq/internal/llm"
	"jonnyq/internal/skill"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// ToolCallMode selects how the model is asked to invoke tools.
type ToolCallMode string

const (
	// ToolCallModeNative uses the provider's native function/tool-calling
	// protocol (the default): the Tools spec is sent with every request and
	// calls arrive as structured EventToolCalls.
	ToolCallModeNative ToolCallMode = "native"
	// ToolCallModePrompt asks the model, via the system prompt, to emit a
	// fenced ```tool JSON block in plain text instead of using native
	// function-calling - for models/backends whose native tool-calling is
	// unreliable (e.g. it reliably omits required arguments). No Tools spec
	// is sent to the provider in this mode: sending one while also
	// prompting for text-based calls tends to trigger a backend's own
	// (sometimes broken) function-calling grammar constraints, which is
	// exactly the failure mode this exists to route around.
	ToolCallModePrompt ToolCallMode = "prompt"
)

// approxCharsPerToken is a rough, tokenizer-free heuristic (no real
// tokenizer is wired in) used only to decide when History has grown large
// enough to compact - it doesn't need to be exact, just good enough to keep
// the prompt well clear of ContextSize before growth actually slows things
// down.
const approxCharsPerToken = 4

// compactHeadroomFraction is how much of ContextSize (converted to an
// estimated character budget) History is allowed to reach before
// compaction kicks in, leaving room for the system message, tool specs,
// and the model's own reply.
const compactHeadroomFraction = 0.6

// compactTargetFraction bounds how big the summary compact() produces is
// allowed to be, as a fraction of the same character budget. Without a
// cap, a verbose summary can land close to (or even above) the trigger
// threshold above, so the very next tool round re-triggers compaction
// again - repeatedly re-summarizing without ever actually shrinking
// History over a long session. Left deliberately far below
// compactHeadroomFraction so History stays small for a while after
// compacting; the model can always re-read a file or re-run a command if
// it needs detail the summary dropped.
const compactTargetFraction = 0.25

// fallbackContextSize is the character-budget basis used when ContextSize
// is left unset (<= 0), so proactive compaction still has something to
// measure against.
const fallbackContextSize = 8192

// DefaultMaxToolCallsPerTurn is the fallback used when MaxToolCallsPerTurn
// is left unset (<= 0). It's a runaway-loop safety valve: it caps total
// tool calls across one user turn even though the spec places no limit on
// the number of prompt rounds overall.
const DefaultMaxToolCallsPerTurn = 50

type Agent struct {
	Provider            llm.Provider
	Model               string
	Thinking            bool
	ContextSize         int
	MaxToolCallsPerTurn int
	Tools               *tools.Registry
	SkillPaths          []string
	UI                  *ui.Writer

	// OutputFile is the transcript log path (e.g. "output.txt") the
	// terminal UI mirrors its own output to - purely a runtime log, never
	// project content. When set, buildSystemMessage tells the model to
	// disregard it entirely, so /watchfile-style directory scans or a
	// curious read_file call don't waste a turn on jonnyq's own log.
	OutputFile string

	// ToolCallMode selects native (default) vs. prompt-based tool calling.
	// See ToolCallModeNative/ToolCallModePrompt.
	ToolCallMode ToolCallMode

	// CodingMode, when true, appends codingModePrompt to the system message.
	// /coding sets this for the duration of its run so the model is held to
	// a much stricter verify-before-done standard than ordinary chat.
	CodingMode bool

	History []llm.Message

	// LastUsage and LastTurnElapsed hold the most recently completed
	// RunTurn call's aggregated LLM usage and wall-clock duration, exposed
	// so callers (e.g. /plan and /coding's ntfy notifications) can report
	// the same figures shown in the terminal's own metrics line without
	// re-deriving them. Stale (left over from an earlier turn) if RunTurn
	// hasn't been called since the caller last checked - callers that need
	// to tell "no turn happened" apart from "a turn happened with zero
	// usage" must track that separately.
	LastUsage       llm.Usage
	LastTurnElapsed time.Duration

	// Context tracking for ContextStatus. realPromptTokens is the provider's
	// reported prompt size for the most recent request (0 when unknown or
	// invalidated by compaction/reset); realBaseChars is how many characters
	// (system message + history) that request contained, so text added
	// since can be estimated on top. sysChars is the current system
	// message's length.
	realPromptTokens int
	realBaseChars    int
	sysChars         int

	// turnStart and lastReq feed the elapsed-time / latest-request part of
	// section headers (see ui.Writer.TurnStatsFn). lastReq is cleared when a
	// turn starts and set whenever an LLM request completes.
	turnStart time.Time
	lastReq   *ui.RequestStats
}

// decisiveThinkingPrompt is included in every system message, regardless of
// mode: it asks the model to reason tightly and commit to a decision
// instead of meandering or re-deliberating, without trading away accuracy.
// It's a cheap, general-purpose complement to the harder repetition-loop
// cutoff in repeat.go - that catches runaway output after the fact, this
// tries to reduce how often it happens in the first place.
const decisiveThinkingPrompt = "Think and act decisively: settle on an approach and commit to it rather than " +
	"re-deliberating the same options, restating your plan, or second-guessing a decision you already made without " +
	"new information. Keep reasoning tight and to the point - avoid long, meandering chains of thought. Speed must " +
	"never come at the cost of correctness: verify anything uncertain with a tool call before asserting it as fact, " +
	"and take the extra step when accuracy requires it.\n"

// codingModePrompt is appended to the system message while CodingMode is
// set. It exists because /coding runs unattended across many turns with no
// human checking each step, so "looks right" is not an acceptable bar - the
// model must have just proven it with a real, passing command.
const codingModePrompt = `
=== AUTONOMOUS CODING MODE ===
You are working through a task checklist with no human reviewing each step before it's committed. Follow these rules exactly, without exception:

1. A task is done only when it compiles/builds with no errors AND all relevant automated tests pass AND it actually satisfies the requirement. Writing the code is not enough.
2. Before changing a task's checkbox from "- [ ]" to "- [x]" in .progress, you MUST have actually run the real build and test commands for this project via run_command in THIS SAME turn, and seen their real passing output. Never mark a task done based on an earlier turn's results, based on reasoning about what "should" work, or without running anything at all.
3. If a verification command fails, fix the underlying problem and re-run it until it genuinely passes before marking the task done. Never mark a task done "with known issues" or "should be fine."
4. If you don't already know how to build or test this project, find out first (check for a Makefile, package.json, go.mod, Cargo.toml, README, CI config, etc.) rather than guessing or skipping verification.
5. If a task genuinely has nothing to build or test (e.g. a documentation-only change), say so explicitly in your reply instead of silently marking it done with no verification.
6. Never state or imply a task is verified, working, or tested unless you have real tool output from this turn proving it.
7. If you notice yourself repeating the same plan or reasoning without taking a new concrete action, stop immediately and call a tool (e.g. run_command, read_file) to make real progress, instead of continuing to reason in text.
`

func New(provider llm.Provider, model string, reg *tools.Registry, thinking bool, contextSize, maxToolCallsPerTurn int, w *ui.Writer, skillPaths []string) *Agent {
	ag := &Agent{
		Provider:            provider,
		Model:               model,
		Thinking:            thinking,
		ContextSize:         contextSize,
		MaxToolCallsPerTurn: maxToolCallsPerTurn,
		Tools:               reg,
		SkillPaths:          skillPaths,
		UI:                  w,
		ToolCallMode:        ToolCallModeNative,
	}
	// Section headers (Thinking / Tool call / Answer) show live context use.
	if w != nil {
		w.ContextFn = ag.ContextStatus
		w.TurnStatsFn = ag.turnStats
	}
	return ag
}

// contextTotal is the context window size used for "left" figures: the
// configured ContextSize, or the fallback when unset.
func (a *Agent) contextTotal() int {
	if a.ContextSize > 0 {
		return a.ContextSize
	}
	return fallbackContextSize
}

// ContextStatus reports how much of the context window the conversation
// currently occupies. When the provider reported the last request's prompt
// size, that real figure is the base and only text added since (the reply,
// tool results) is estimated at approxCharsPerToken on top; otherwise the
// whole system message + history is estimated. Estimated is set whenever any
// approximation is involved.
func (a *Agent) ContextStatus() ui.ContextStatus {
	chars := a.sysChars + a.historyChars()
	cs := ui.ContextStatus{Total: a.contextTotal(), Estimated: true}
	if a.realPromptTokens > 0 {
		delta := chars - a.realBaseChars
		if delta < 0 {
			delta = 0
		}
		cs.Used = a.realPromptTokens + delta/approxCharsPerToken
		cs.Estimated = delta > 0
		return cs
	}
	cs.Used = chars / approxCharsPerToken
	return cs
}

// turnStats reports the running turn's elapsed time and latest finished
// request, for section headers. False outside a turn.
func (a *Agent) turnStats() (ui.TurnStats, bool) {
	if a.turnStart.IsZero() {
		return ui.TurnStats{}, false
	}
	return ui.TurnStats{Elapsed: time.Since(a.turnStart), Req: a.lastReq}, true
}

// requestStats condenses a finished request's usage and wall time into the
// figures section headers show. tok/s follows FormatUsage's rule.
func requestStats(u llm.Usage, wall time.Duration) *ui.RequestStats {
	rs := &ui.RequestStats{
		PromptTokens:        u.PromptTokens,
		CompletionTokens:    u.CompletionTokens,
		CompletionEstimated: u.CompletionEstimated,
		Duration:            wall,
	}
	if u.HasTiming && u.EvalDuration > 0 && u.CompletionTokens > 0 {
		rs.TokPerSec = float64(u.CompletionTokens) / u.EvalDuration.Seconds()
		rs.TokPerSecEstimated = u.Estimated || u.CompletionEstimated
	}
	return rs
}

// finalContextStatus is the context size after a turn: the last request's
// real prompt tokens plus the reply's real completion tokens when the
// provider reported them, else ContextStatus's estimate.
func (a *Agent) finalContextStatus(last llm.Usage) ui.ContextStatus {
	if last.PromptTokens > 0 {
		return ui.ContextStatus{
			Used:      last.PromptTokens + last.CompletionTokens,
			Total:     a.contextTotal(),
			Estimated: last.CompletionEstimated,
		}
	}
	return a.ContextStatus()
}

// ResetHistory discards all conversation history, so the next RunTurn call
// starts from a clean slate (just the system message and its own prompt).
// Used by /autocoding to keep each task's prompt minimal instead of
// carrying earlier tasks' (or the planning step's) conversation forward -
// the model re-discovers whatever it needs for the new task via
// read_file/run_command rather than relying on memory of past turns.
func (a *Agent) ResetHistory() {
	a.History = nil
	a.realPromptTokens = 0
}

func (a *Agent) buildSystemMessage() llm.Message {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	var sb strings.Builder
	sb.WriteString("You are jonnyq, a coding agent with tool access. Current date and time: ")
	sb.WriteString(now)
	sb.WriteString("\n")
	sb.WriteString(decisiveThinkingPrompt)
	if a.OutputFile != "" {
		fmt.Fprintf(&sb,
			"Ignore %s if you come across it in the working directory - it is this program's own runtime transcript "+
				"log (a mirror of what's printed to the terminal), not part of the project. You never need to read it, "+
				"inspect it, or take its contents into account for any task.\n",
			a.OutputFile,
		)
	}
	if a.ToolCallMode == ToolCallModePrompt {
		sb.WriteString(buildPromptToolCallSection(a.Tools.Specs()))
	}
	skills := skill.Discover(a.SkillPaths)
	if len(skills) > 0 {
		sb.WriteString("Available skills (use read_skill with a name below to load its full content):\n")
		for _, s := range skills {
			fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
		}
	}
	if a.CodingMode {
		sb.WriteString(codingModePrompt)
	}
	return llm.Message{Role: llm.RoleSystem, Content: sb.String()}
}

func sumUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:        a.PromptTokens + b.PromptTokens,
		CompletionTokens:    a.CompletionTokens + b.CompletionTokens,
		TotalTokens:         a.TotalTokens + b.TotalTokens,
		LoadDuration:        a.LoadDuration + b.LoadDuration,
		PromptEvalDuration:  a.PromptEvalDuration + b.PromptEvalDuration,
		EvalDuration:        a.EvalDuration + b.EvalDuration,
		HasTiming:           a.HasTiming || b.HasTiming,
		Estimated:           a.Estimated || b.Estimated,
		CompletionEstimated: a.CompletionEstimated || b.CompletionEstimated,
	}
}

// RunTurn drives one full user prompt through the tool-calling loop to a
// final answer.
func (a *Agent) RunTurn(ctx context.Context, userInput string) error {
	if a.Model == "" {
		return fmt.Errorf("no model set; use /model <name> first")
	}
	start := time.Now()
	a.UI.NewTurn()
	systemMsg := a.buildSystemMessage()
	a.sysChars = len(systemMsg.Content)
	a.turnStart = start
	a.lastReq = nil
	defer func() { a.turnStart = time.Time{} }()
	a.History = append(a.History, llm.Message{Role: llm.RoleUser, Content: userInput})

	maxToolCalls := a.MaxToolCallsPerTurn
	if maxToolCalls <= 0 {
		maxToolCalls = DefaultMaxToolCallsPerTurn
	}

	var totalUsage llm.Usage
	remainingToolBudget := maxToolCalls
	var lastRoundUsage llm.Usage // the final request's usage, for the context readout

	for {
		messages := append([]llm.Message{systemMsg}, a.History...)
		roundBaseChars := a.sysChars + a.historyChars()
		roundStart := time.Now()

		// Each Chat call gets its own cancellable context so a detected
		// repetition loop can abort just that in-flight request without
		// touching the turn's overall ctx (which callers still use for
		// Ctrl-C).
		chatCtx, cancelChat := context.WithCancel(ctx)
		// In prompt mode, deliberately omit Tools: sending a native tool
		// spec while also prompting the model to emit text-based calls
		// tends to trigger a backend's own function-calling grammar
		// constraints - the very thing this mode exists to route around.
		var toolSpecs []llm.ToolSpec
		if a.ToolCallMode != ToolCallModePrompt {
			toolSpecs = a.Tools.Specs()
		}
		events, err := a.Provider.Chat(chatCtx, llm.ChatRequest{
			Model:       a.Model,
			Messages:    messages,
			Tools:       toolSpecs,
			Thinking:    a.Thinking,
			ContextSize: a.ContextSize,
		})
		if err != nil {
			cancelChat()
			return err
		}

		var pendingCalls []llm.ToolCall
		var assistantContent strings.Builder
		var roundUsage llm.Usage
		gotDone := false
		loopDetected := false
		var thinkingRepeat, contentRepeat repeatDetector

		for ev := range events {
			switch ev.Kind {
			case llm.EventThinking:
				if loopDetected {
					continue
				}
				a.UI.Thinking(ev.Delta)
				if thinkingRepeat.Add(ev.Delta) {
					loopDetected = true
					cancelChat()
				}
			case llm.EventContent:
				if loopDetected {
					continue
				}
				a.UI.Answer(ev.Delta)
				assistantContent.WriteString(ev.Delta)
				if contentRepeat.Add(ev.Delta) {
					loopDetected = true
					cancelChat()
				}
			case llm.EventToolCalls:
				if !loopDetected {
					pendingCalls = append(pendingCalls, ev.ToolCalls...)
				}
			case llm.EventError:
				if loopDetected {
					// Expected: our own cancellation surfacing as a stream
					// error. Treat it the same as a clean end below.
					continue
				}
				cancelChat()
				return ev.Err
			case llm.EventDone:
				roundUsage = ev.Usage
				gotDone = true
				a.lastReq = requestStats(ev.Usage, time.Since(roundStart))
				if ev.Usage.PromptTokens > 0 {
					a.realPromptTokens = ev.Usage.PromptTokens
					a.realBaseChars = roundBaseChars
				}
			}
		}
		cancelChat()

		if loopDetected {
			a.UI.Meta("[repetitive output detected; response cut short]")
			a.History = append(a.History, llm.Message{
				Role:    llm.RoleAssistant,
				Content: "[cut short: this response started repeating the same reasoning without making progress]",
			})
			totalUsage = sumUsage(totalUsage, roundUsage)
			lastRoundUsage = roundUsage
			break
		}
		if !gotDone {
			return fmt.Errorf("provider stream ended without a completion event")
		}
		totalUsage = sumUsage(totalUsage, roundUsage)
		lastRoundUsage = roundUsage

		// In prompt mode the provider never emits EventToolCalls (no Tools
		// spec was sent), so a requested call instead shows up as a
		// ```tool fenced block within the plain content just streamed.
		if a.ToolCallMode == ToolCallModePrompt && len(pendingCalls) == 0 {
			pendingCalls = parsePromptToolCalls(assistantContent.String())
		}

		if len(pendingCalls) == 0 {
			a.UI.Plainln("")
			a.History = append(a.History, llm.Message{Role: llm.RoleAssistant, Content: assistantContent.String()})
			break
		}

		if len(pendingCalls) > remainingToolBudget {
			return fmt.Errorf("tool call budget exceeded for this turn (limit %d)", maxToolCalls)
		}
		remainingToolBudget -= len(pendingCalls)

		assistantMsg := llm.Message{Role: llm.RoleAssistant, Content: assistantContent.String()}
		if a.ToolCallMode != ToolCallModePrompt {
			// Only attach native ToolCalls when we actually asked for
			// native tool-calling - in prompt mode the raw ```tool block is
			// already part of Content, and echoing it back to the provider
			// wrapped in a native tool_calls field (when no Tools spec was
			// ever sent) risks confusing or being rejected by a backend not
			// expecting the function-calling wire format at all.
			assistantMsg.ToolCalls = pendingCalls
		}
		a.History = append(a.History, assistantMsg)

		for _, tc := range pendingCalls {
			var result string
			var err error
			if tc.Name == parseErrorToolName {
				// A malformed ```tool block from parsePromptToolCalls -
				// report the actual parse problem rather than routing
				// through Tools.Call (which would just say "unknown tool"),
				// and skip the sentinel name in anything shown to the user
				// or fed back to the model.
				a.UI.ToolCall("malformed ```tool block: " + tc.Arguments)
				err = errors.New(tc.Arguments)
			} else {
				a.UI.ToolCall(fmt.Sprintf("%s(%s)", tc.Name, tc.Arguments))
				result, err = a.Tools.Call(ctx, tc.Name, tc.Arguments)
			}
			if err != nil {
				result = "error: " + err.Error()
				if tc.Name != parseErrorToolName {
					a.UI.ToolCall(fmt.Sprintf("%s: error: %v", tc.Name, err))
				}
			}
			if a.ToolCallMode == ToolCallModePrompt {
				// A plain user-role message, not RoleTool: the "tool"
				// role/tool_call_id pairing is an OpenAI-style
				// function-calling convention that a model relying on
				// prompt mode (because its native tool-calling is
				// unreliable) may not understand or a backend may not
				// accept at all when no Tools spec was ever sent.
				content := fmt.Sprintf("Tool result (%s): %s", tc.Name, result)
				if tc.Name == parseErrorToolName {
					content = result
				}
				a.History = append(a.History, llm.Message{Role: llm.RoleUser, Content: content})
			} else {
				a.History = append(a.History, llm.Message{
					Role:       llm.RoleTool,
					Content:    result,
					ToolCallID: tc.ID,
					Name:       tc.Name,
				})
			}
		}

		// Check after every tool round, not just at the end of the turn: a
		// single turn (e.g. /autocoding grinding through many tasks) can
		// pile up a lot of tool call/result content long before it returns,
		// and waiting for the turn to finish would let prompts stay bloated
		// for the whole run.
		if a.shouldCompact() {
			a.compact(ctx)
		}
		// Loop again so the model can see the tool results.
	}

	elapsed := time.Since(start)
	a.printMetrics(totalUsage, elapsed, a.finalContextStatus(lastRoundUsage))
	a.LastUsage = totalUsage
	a.LastTurnElapsed = elapsed

	if a.shouldCompact() {
		a.compact(ctx)
	}
	return nil
}

// historyChars estimates History's size in characters, as a stand-in for
// tokens (no real tokenizer is wired in).
func (a *Agent) historyChars() int {
	total := 0
	for _, m := range a.History {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Name) + len(tc.Arguments)
		}
	}
	return total
}

// charBudget estimates how many characters of History correspond to the
// full ContextSize (falling back to fallbackContextSize when unset), as a
// stand-in for tokens. compactHeadroomFraction/compactTargetFraction scale
// this into the trigger and target sizes below.
func (a *Agent) charBudget() int {
	budget := a.ContextSize
	if budget <= 0 {
		budget = fallbackContextSize
	}
	return int(float64(budget) * approxCharsPerToken)
}

// shouldCompact reports whether History has grown large enough, relative to
// ContextSize, that it should be summarized and replaced before the next
// prompt is sent.
func (a *Agent) shouldCompact() bool {
	return float64(a.historyChars()) > float64(a.charBudget())*compactHeadroomFraction
}

func (a *Agent) printMetrics(u llm.Usage, wallClock time.Duration, cs ui.ContextStatus) {
	msg := fmt.Sprintf("[%s wall=%s | %s]", FormatUsage(u), wallClock.Round(time.Millisecond), cs)
	a.UI.MetaColored(cs.Color(), msg)
}

// FormatUsage renders u's timing/token figures the same way the terminal's
// end-of-turn metrics line does (preload/prompt_eval/generate durations,
// token in/out/total, tokens/sec) - exported for reuse anywhere the same
// stats need reporting outside the UI, e.g. /plan and /coding's ntfy
// notifications.
//
// "generate" is the whole generation time (reasoning plus the answer), not
// just the thinking phase. Figures the client measured itself rather than
// the provider reporting (u.Estimated) are prefixed with "~", and preload
// is "n/a" because load time can't be separated from prompt eval there
// (prompt_eval is then the time to the first token).
func FormatUsage(u llm.Usage) string {
	fmtDur := func(d time.Duration) string {
		if !u.HasTiming {
			return "n/a"
		}
		s := d.Round(time.Millisecond).String()
		if u.Estimated {
			s = "~" + s
		}
		return s
	}
	preload := fmtDur(u.LoadDuration)
	if u.Estimated {
		preload = "n/a"
	}
	tokIn, tokOut, tokTotal := "n/a", "n/a", "n/a"
	if u.TotalTokens > 0 {
		tokIn = strconv.Itoa(u.PromptTokens)
		tokOut = strconv.Itoa(u.CompletionTokens)
		tokTotal = strconv.Itoa(u.TotalTokens)
	} else if u.CompletionEstimated && u.CompletionTokens > 0 {
		tokOut = "~" + strconv.Itoa(u.CompletionTokens)
	}
	tps := "n/a"
	if u.HasTiming && u.EvalDuration > 0 && u.CompletionTokens > 0 {
		tps = fmt.Sprintf("%.2f", float64(u.CompletionTokens)/u.EvalDuration.Seconds())
		if u.Estimated || u.CompletionEstimated {
			tps = "~" + tps
		}
	}
	return fmt.Sprintf(
		"preload=%s prompt_eval=%s generate=%s token_in=%s token_out=%s total_token=%s tok/s=%s",
		preload, fmtDur(u.PromptEvalDuration), fmtDur(u.EvalDuration),
		tokIn, tokOut, tokTotal, tps,
	)
}

// compact summarizes History into a single message, bounded to roughly
// compactTargetFraction of the character budget, and resets History to just
// that message. Called whenever shouldCompact reports History has grown too
// large relative to ContextSize, rather than on a fixed turn count, so it
// also fires mid-turn during a long run instead of only between turns.
func (a *Agent) compact(ctx context.Context) {
	var transcript strings.Builder
	for _, m := range a.History {
		fmt.Fprintf(&transcript, "%s: %s\n", m.Role, m.Content)
	}

	target := int(float64(a.charBudget()) * compactTargetFraction)
	req := llm.ChatRequest{
		Model: a.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: fmt.Sprintf(
				"Summarize the conversation below concisely, preserving important facts, decisions, and open "+
					"tasks. Keep the summary under about %d characters - omit exact file contents, long command "+
					"output, or other verbatim detail; the agent can re-read a file or re-run a command later if it "+
					"needs that detail again. Reply with only the summary.", target,
			)},
			{Role: llm.RoleUser, Content: transcript.String()},
		},
	}
	events, err := a.Provider.Chat(ctx, req)
	if err != nil {
		a.UI.Meta(fmt.Sprintf("[context compaction failed: %v]", err))
		return
	}
	var summary strings.Builder
	for ev := range events {
		switch ev.Kind {
		case llm.EventContent:
			summary.WriteString(ev.Delta)
		case llm.EventError:
			a.UI.Meta(fmt.Sprintf("[context compaction failed: %v]", ev.Err))
			return
		}
	}
	// Enforce the target regardless of how well the model followed the
	// length instruction above - a verbose summary must not be allowed to
	// leave History close to the trigger threshold again immediately.
	content := truncateChars("Summary of earlier conversation:\n"+summary.String(), target)
	a.History = []llm.Message{{Role: llm.RoleSystem, Content: content}}
	a.realPromptTokens = 0 // the old prompt size no longer describes History
	a.UI.Meta("[context compacted]")
}

// truncateChars cuts s to at most maxBytes bytes, backing off to the
// nearest earlier UTF-8 rune boundary so multi-byte characters (e.g. Thai
// text) are never split into invalid UTF-8.
func truncateChars(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
