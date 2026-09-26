package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"jonnyq/internal/llm"
)

// parseErrorToolName marks a ToolCall as a parse failure rather than a real
// tool invocation. RunTurn special-cases it in the execution loop to report
// the actual parse problem, since routing it through Tools.Call would just
// report "unknown tool" and lose the reason.
const parseErrorToolName = "__prompt_tool_parse_error__"

// promptToolCallBlockRe matches a ```tool fenced code block containing a
// JSON tool-call request, used by ToolCallModePrompt for models/backends
// whose native function-calling is unreliable.
var promptToolCallBlockRe = regexp.MustCompile("(?s)```tool\\s*(.*?)```")

// promptToolCallEnvelope is the JSON shape a model must use inside a
// ```tool block in ToolCallModePrompt.
type promptToolCallEnvelope struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// parsePromptToolCalls scans content for ```tool fenced blocks and returns
// one llm.ToolCall per block found, in order (nil if none). A block whose
// JSON doesn't parse into the expected envelope, or is missing "name",
// still produces a ToolCall so the model gets clear, actionable feedback
// next round instead of the turn silently doing nothing - see
// parseErrorToolName.
func parsePromptToolCalls(content string) []llm.ToolCall {
	matches := promptToolCallBlockRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	calls := make([]llm.ToolCall, 0, len(matches))
	for i, m := range matches {
		id := fmt.Sprintf("prompt-%d", i+1)
		raw := strings.TrimSpace(m[1])

		var env promptToolCallEnvelope
		switch err := json.Unmarshal([]byte(raw), &env); {
		case err != nil:
			calls = append(calls, malformedPromptToolCall(id, fmt.Sprintf(
				"invalid JSON in ```tool block: %v. Expected exactly: "+`{"name": "<tool>", "arguments": {...}}`, err,
			)))
		case env.Name == "":
			calls = append(calls, malformedPromptToolCall(id, fmt.Sprintf(
				"```tool block is missing the required \"name\" field. Expected exactly: "+
					`{"name": "<tool>", "arguments": {...}}`+", got: %s", raw,
			)))
		default:
			args := "{}"
			if len(env.Arguments) > 0 {
				args = string(env.Arguments)
			}
			calls = append(calls, llm.ToolCall{ID: id, Name: env.Name, Arguments: args})
		}
	}
	return calls
}

func malformedPromptToolCall(id, reason string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: parseErrorToolName, Arguments: reason}
}

// promptToolCallInstructions is appended to the system message in
// ToolCallModePrompt, ahead of the tool list, explaining the fenced-block
// convention parsePromptToolCalls expects.
const promptToolCallInstructions = "\n=== TOOL CALLING (PROMPT MODE) ===\n" +
	"This model or backend's native function-calling is unreliable, so tool calls are made as plain text instead of " +
	"through a native tool-calling API. To call a tool, respond with ONLY a single fenced block in exactly this " +
	"form, and nothing else in that response:\n\n" +
	"```tool\n" +
	`{"name": "<tool name>", "arguments": {<arguments as a JSON object, matching that tool's parameters below>}}` + "\n" +
	"```\n\n" +
	"Rules:\n" +
	"1. Emit at most one such block per response, and nothing else - no explanation before or after it.\n" +
	"2. Wait for the tool's result (given back to you as a plain message) before calling another tool.\n" +
	"3. Once you have everything you need, answer normally in plain text with no ```tool block at all.\n" +
	"4. If a call comes back with an error, re-read that tool's parameters below and correct the JSON - do not repeat " +
	"the exact same malformed call.\n\n" +
	"Available tools:\n"

// buildPromptToolCallSection renders specs into the tool-list portion of
// promptToolCallInstructions, one tool per line with its JSON parameter
// schema, so the model has the same information native tool-calling would
// have given it via the API's Tools field.
func buildPromptToolCallSection(specs []llm.ToolSpec) string {
	if len(specs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(promptToolCallInstructions)
	for _, s := range specs {
		params, err := json.Marshal(s.Parameters)
		if err != nil {
			params = []byte("{}")
		}
		fmt.Fprintf(&sb, "- %s: %s\n  parameters: %s\n", s.Name, s.Description, params)
	}
	return sb.String()
}
