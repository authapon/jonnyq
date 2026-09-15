package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaChatStreamsContentAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"lo"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true,"total_duration":1000,"load_duration":100,"prompt_eval_count":5,"prompt_eval_duration":200,"eval_count":2,"eval_duration":300}`)
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "")
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
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", content)
	}
	if !usage.HasTiming || usage.PromptTokens != 5 || usage.CompletionTokens != 2 {
		t.Errorf("unexpected usage: %+v", usage)
	}
}

func TestOllamaChatToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.txt"}}}]},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true,"eval_count":1}`)
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "")
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
		t.Errorf("unexpected tool calls: %+v", calls)
	}
}

// TestOllamaRequestAlwaysIncludesContentKey mirrors the OpenAI-provider
// regression test: a tool result that happens to be an empty string (e.g.
// a shell command that succeeded with no output) must still send an
// explicit "content" key rather than have encoding/json drop it.
func TestOllamaRequestAlwaysIncludesContentKey(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true}`)
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "")
	events, err := p.Chat(context.Background(), ChatRequest{Model: "test", Messages: []Message{
		{Role: RoleUser, Content: "run go mod tidy"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "run_command", Arguments: `{"command":"go mod tidy"}`}}},
		{Role: RoleTool, Content: "", ToolCallID: "call_1", Name: "run_command"},
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

func TestOllamaListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"llama3:latest"},{"name":"qwen3:8b"}]}`)
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "")
	names, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "llama3:latest" {
		t.Errorf("unexpected models: %v", names)
	}
}
