package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func writeSSE(w http.ResponseWriter, payload string) {
	fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestOpenAIChatStreamsContentAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"Hel"}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`)
		writeSSE(w, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`)
		writeSSE(w, "[DONE]")
	}))
	defer srv.Close()

	p := NewOpenAIProvider(srv.URL, "")
	events, err := p.Chat(context.Background(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	var usage Usage
	for ev := range events {
		switch ev.Kind {
		case EventContent:
			content += ev.Delta
		case EventDone:
			usage = ev.Usage
		case EventError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if content != "Hello" {
		t.Errorf("expected 'Hello', got %q", content)
	}
	if !usage.HasTiming || !usage.Estimated {
		t.Errorf("openai usage should carry client-measured (estimated) timing, got %+v", usage)
	}
	if usage.CompletionEstimated {
		t.Errorf("completion tokens came from the usage block and must not be flagged estimated")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 3 || usage.TotalTokens != 13 {
		t.Errorf("unexpected usage: %+v", usage)
	}
}

// TestOpenAIRequestAlwaysIncludesContentKey reproduces a real failure: LM
// Studio's llama.cpp-based backend rejects a message whose "content" key
// is missing entirely, which is what encoding/json would produce for a
// tool result that happened to be an empty string (e.g. a shell command
// that succeeded with no output) if the field were tagged omitempty.
func TestOpenAIRequestAlwaysIncludesContentKey(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		writeSSE(w, "[DONE]")
	}))
	defer srv.Close()

	p := NewOpenAIProvider(srv.URL, "")
	events, err := p.Chat(context.Background(), ChatRequest{Model: "test", Messages: []Message{
		{Role: RoleUser, Content: "run go mod tidy"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "run_command", Arguments: `{"command":"go mod tidy"}`}}},
		{Role: RoleTool, Content: "", ToolCallID: "call_1", Name: "run_command"}, // empty output, e.g. a silent success
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}

	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("invalid request JSON: %v\nbody: %s", err, body)
	}
	last := decoded.Messages[len(decoded.Messages)-1]
	if last["role"] != "tool" {
		t.Fatalf("expected the last message to be the tool result, got: %+v", last)
	}
	content, present := last["content"]
	if !present {
		t.Errorf("expected the tool message to include a \"content\" key even when empty, got: %+v", last)
	}
	if content != "" {
		t.Errorf("expected content to be an empty string, got: %v", content)
	}
}

func TestOpenAIChatAccumulatesToolCallArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":""}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]},"finish_reason":"tool_calls"}]}`)
		writeSSE(w, "[DONE]")
	}))
	defer srv.Close()

	p := NewOpenAIProvider(srv.URL, "")
	events, err := p.Chat(context.Background(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "read a.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	var calls []ToolCall
	for ev := range events {
		if ev.Kind == EventToolCalls {
			calls = append(calls, ev.ToolCalls...)
		}
	}
	if len(calls) != 1 || calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"a.txt"}` {
		t.Errorf("unexpected accumulated tool call: %+v", calls)
	}
}

func TestOpenAIMeasuresTimingClientSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond) // "prompt processing" before the first token
		writeSSE(w, `{"choices":[{"delta":{"content":"Hello"}}]}`)
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond) // "generation"
		writeSSE(w, `{"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`)
		writeSSE(w, "[DONE]")
	}))
	defer srv.Close()

	events, err := NewOpenAIProvider(srv.URL, "").Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var usage Usage
	for ev := range events {
		if ev.Kind == EventDone {
			usage = ev.Usage
		}
	}
	if !usage.HasTiming || !usage.Estimated {
		t.Fatalf("expected estimated timing, got %+v", usage)
	}
	if usage.PromptEvalDuration < 50*time.Millisecond {
		t.Errorf("expected time-to-first-token >= ~60ms, got %s", usage.PromptEvalDuration)
	}
	if usage.EvalDuration < 70*time.Millisecond {
		t.Errorf("expected generation time >= ~80ms, got %s", usage.EvalDuration)
	}
	if usage.LoadDuration != 0 {
		t.Errorf("load time cannot be measured client-side, got %s", usage.LoadDuration)
	}
	// No usage block was sent: tokens are estimated from the 11 streamed chars.
	if !usage.CompletionEstimated || usage.CompletionTokens != 3 {
		t.Errorf("expected 3 estimated completion tokens, got %+v", usage)
	}
}

func TestOpenAINoTimingWhenNothingStreamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, "[DONE]")
	}))
	defer srv.Close()

	events, err := NewOpenAIProvider(srv.URL, "").Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range events {
		if ev.Kind == EventDone && (ev.Usage.HasTiming || ev.Usage.Estimated) {
			t.Errorf("expected no timing when no token streamed, got %+v", ev.Usage)
		}
	}
}
