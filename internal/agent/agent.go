// Package agent implements the tool-calling loop: send the conversation and
// available tools to the provider, stream thinking/content back to the user,
// execute any requested tool calls, and repeat until the model produces a
// final answer. It also owns .context transcript persistence, periodic
// context compaction, and the end-of-turn metrics footer.
package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"jonnyq/internal/llm"
	"jonnyq/internal/skill"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

// compactEvery is how many completed user turns accumulate before the
// conversation history is summarized and replaced, per spec.
const compactEvery = 20

// maxToolCallsPerTurn is a runaway-loop safety valve: it caps total tool
// calls across one user turn even though the spec places no limit on the
// number of prompt rounds overall.
const maxToolCallsPerTurn = 50

type Agent struct {
	Provider    llm.Provider
	Model       string
	Thinking    bool
	ContextSize int
	Tools       *tools.Registry
	SkillPaths  []string
	UI          *ui.Writer
	ContextFile string

	History            []llm.Message
	roundsSinceCompact int
}

func New(provider llm.Provider, model string, reg *tools.Registry, thinking bool, contextSize int, w *ui.Writer, skillPaths []string, contextFile string) *Agent {
	return &Agent{
		Provider:    provider,
		Model:       model,
		Thinking:    thinking,
		ContextSize: contextSize,
		Tools:       reg,
		SkillPaths:  skillPaths,
		UI:          w,
		ContextFile: contextFile,
	}
}

func (a *Agent) buildSystemMessage() llm.Message {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	var sb strings.Builder
	sb.WriteString("You are jonnyq, a coding agent with tool access. Current date and time: ")
	sb.WriteString(now)
	sb.WriteString("\n")
	skills := skill.Discover(a.SkillPaths)
	if len(skills) > 0 {
		sb.WriteString("Available skills (use read_skill with a name below to load its full content):\n")
		for _, s := range skills {
			fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
		}
	}
	return llm.Message{Role: llm.RoleSystem, Content: sb.String()}
}

func sumUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:       a.PromptTokens + b.PromptTokens,
		CompletionTokens:   a.CompletionTokens + b.CompletionTokens,
		TotalTokens:        a.TotalTokens + b.TotalTokens,
		LoadDuration:       a.LoadDuration + b.LoadDuration,
		PromptEvalDuration: a.PromptEvalDuration + b.PromptEvalDuration,
		EvalDuration:       a.EvalDuration + b.EvalDuration,
		HasTiming:          a.HasTiming || b.HasTiming,
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
	a.History = append(a.History, llm.Message{Role: llm.RoleUser, Content: userInput})

	var totalUsage llm.Usage
	remainingToolBudget := maxToolCallsPerTurn
	var finalAnswer strings.Builder

	for {
		messages := append([]llm.Message{systemMsg}, a.History...)
		events, err := a.Provider.Chat(ctx, llm.ChatRequest{
			Model:       a.Model,
			Messages:    messages,
			Tools:       a.Tools.Specs(),
			Thinking:    a.Thinking,
			ContextSize: a.ContextSize,
		})
		if err != nil {
			return err
		}

		var pendingCalls []llm.ToolCall
		var assistantContent strings.Builder
		var roundUsage llm.Usage
		gotDone := false

		for ev := range events {
			switch ev.Kind {
			case llm.EventThinking:
				a.UI.Thinking(ev.Delta)
			case llm.EventContent:
				a.UI.Answer(ev.Delta)
				assistantContent.WriteString(ev.Delta)
				finalAnswer.WriteString(ev.Delta)
			case llm.EventToolCalls:
				pendingCalls = append(pendingCalls, ev.ToolCalls...)
			case llm.EventError:
				return ev.Err
			case llm.EventDone:
				roundUsage = ev.Usage
				gotDone = true
			}
		}
		if !gotDone {
			return fmt.Errorf("provider stream ended without a completion event")
		}
		totalUsage = sumUsage(totalUsage, roundUsage)

		if len(pendingCalls) == 0 {
			a.UI.Plainln("")
			a.History = append(a.History, llm.Message{Role: llm.RoleAssistant, Content: assistantContent.String()})
			break
		}

		if len(pendingCalls) > remainingToolBudget {
			return fmt.Errorf("tool call budget exceeded for this turn (limit %d)", maxToolCallsPerTurn)
		}
		remainingToolBudget -= len(pendingCalls)

		a.History = append(a.History, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   assistantContent.String(),
			ToolCalls: pendingCalls,
		})

		for _, tc := range pendingCalls {
			a.UI.ToolCall(fmt.Sprintf("%s(%s)", tc.Name, tc.Arguments))
			result, err := a.Tools.Call(ctx, tc.Name, tc.Arguments)
			if err != nil {
				result = "error: " + err.Error()
				a.UI.ToolCall(fmt.Sprintf("%s: error: %v", tc.Name, err))
			}
			a.History = append(a.History, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Name,
			})
		}
		// Loop again so the model can see the tool results.
	}

	elapsed := time.Since(start)
	a.printMetrics(totalUsage, elapsed)
	a.appendContextFile(userInput, finalAnswer.String())

	a.roundsSinceCompact++
	if a.roundsSinceCompact >= compactEvery {
		a.compact(ctx)
	}
	return nil
}

func (a *Agent) printMetrics(u llm.Usage, wallClock time.Duration) {
	fmtDur := func(d time.Duration) string {
		if !u.HasTiming {
			return "n/a"
		}
		return d.Round(time.Millisecond).String()
	}
	tokIn, tokOut, tokTotal := "n/a", "n/a", "n/a"
	if u.TotalTokens > 0 {
		tokIn = strconv.Itoa(u.PromptTokens)
		tokOut = strconv.Itoa(u.CompletionTokens)
		tokTotal = strconv.Itoa(u.TotalTokens)
	}
	tps := "n/a"
	if u.HasTiming && u.EvalDuration > 0 && u.CompletionTokens > 0 {
		tps = fmt.Sprintf("%.2f", float64(u.CompletionTokens)/u.EvalDuration.Seconds())
	}
	msg := fmt.Sprintf(
		"[preload=%s prompt_eval=%s thinking=%s token_in=%s token_out=%s total_token=%s tok/s=%s wall=%s]",
		fmtDur(u.LoadDuration), fmtDur(u.PromptEvalDuration), fmtDur(u.EvalDuration),
		tokIn, tokOut, tokTotal, tps, wallClock.Round(time.Millisecond),
	)
	a.UI.Meta(msg)
}

func (a *Agent) appendContextFile(prompt, answer string) {
	f, err := os.OpenFile(a.ContextFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "=== %s ===\n> %s\n%s\n\n", time.Now().Format(time.RFC3339), prompt, answer)
}

// compact summarizes History into a single message and resets it, per the
// spec's "compact every 20 rounds" rule. It resets the round counter even on
// failure so a persistently failing summarization doesn't retry every turn.
func (a *Agent) compact(ctx context.Context) {
	defer func() { a.roundsSinceCompact = 0 }()

	var transcript strings.Builder
	for _, m := range a.History {
		fmt.Fprintf(&transcript, "%s: %s\n", m.Role, m.Content)
	}

	req := llm.ChatRequest{
		Model: a.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Summarize the conversation below concisely, preserving important facts, decisions, and open tasks. Reply with only the summary."},
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
	a.History = []llm.Message{{Role: llm.RoleSystem, Content: "Summary of earlier conversation:\n" + summary.String()}}
	a.UI.Meta("[context compacted]")
}
