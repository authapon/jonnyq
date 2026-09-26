package agent

import (
	"strings"
	"testing"

	"jonnyq/internal/llm"
)

func TestParsePromptToolCallsExtractsSingleCall(t *testing.T) {
	content := "I'll read the file first.\n\n```tool\n" +
		`{"name": "read_file", "arguments": {"path": "data.txt"}}` +
		"\n```\n"
	calls := parsePromptToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Name != "read_file" {
		t.Errorf("expected name read_file, got %q", calls[0].Name)
	}
	if calls[0].Arguments != `{"path": "data.txt"}` {
		t.Errorf("expected arguments to pass through raw, got %q", calls[0].Arguments)
	}
}

func TestParsePromptToolCallsReturnsNilWhenNoBlock(t *testing.T) {
	if calls := parsePromptToolCalls("just a plain final answer, no tool needed"); calls != nil {
		t.Errorf("expected nil, got %+v", calls)
	}
}

func TestParsePromptToolCallsHandlesMultipleBlocks(t *testing.T) {
	content := "```tool\n" + `{"name": "a", "arguments": {}}` + "\n```\n" +
		"```tool\n" + `{"name": "b", "arguments": {"x": 1}}` + "\n```\n"
	calls := parsePromptToolCalls(content)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(calls), calls)
	}
	if calls[0].Name != "a" || calls[1].Name != "b" {
		t.Errorf("expected calls in order [a, b], got %+v", calls)
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("expected distinct IDs, got both %q", calls[0].ID)
	}
}

func TestParsePromptToolCallsDefaultsMissingArguments(t *testing.T) {
	content := "```tool\n" + `{"name": "list_things"}` + "\n```\n"
	calls := parsePromptToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Arguments != "{}" {
		t.Errorf("expected arguments to default to {}, got %q", calls[0].Arguments)
	}
}

func TestParsePromptToolCallsFlagsInvalidJSON(t *testing.T) {
	content := "```tool\nnot valid json at all\n```\n"
	calls := parsePromptToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != parseErrorToolName {
		t.Errorf("expected the parse-error sentinel name, got %q", calls[0].Name)
	}
	if !strings.Contains(calls[0].Arguments, "invalid JSON") {
		t.Errorf("expected the reason to mention invalid JSON, got %q", calls[0].Arguments)
	}
}

func TestParsePromptToolCallsFlagsMissingName(t *testing.T) {
	content := "```tool\n" + `{"arguments": {"path": "x"}}` + "\n```\n"
	calls := parsePromptToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != parseErrorToolName {
		t.Errorf("expected the parse-error sentinel name, got %q", calls[0].Name)
	}
	if !strings.Contains(calls[0].Arguments, `missing the required "name"`) {
		t.Errorf("expected the reason to mention the missing name field, got %q", calls[0].Arguments)
	}
}

func TestBuildPromptToolCallSectionListsToolsWithSchema(t *testing.T) {
	specs := []llm.ToolSpec{
		{Name: "read_file", Description: "reads a file", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
		}},
	}
	got := buildPromptToolCallSection(specs)
	if !strings.Contains(got, "```tool") {
		t.Errorf("expected the fenced-block convention to be documented, got: %s", got)
	}
	if !strings.Contains(got, "read_file: reads a file") {
		t.Errorf("expected the tool name and description to be listed, got: %s", got)
	}
	if !strings.Contains(got, `"path"`) {
		t.Errorf("expected the tool's parameter schema to be embedded, got: %s", got)
	}
}

func TestBuildPromptToolCallSectionEmptyWhenNoTools(t *testing.T) {
	if got := buildPromptToolCallSection(nil); got != "" {
		t.Errorf("expected empty output for no tools, got: %q", got)
	}
}
