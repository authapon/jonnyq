package llm

import (
	"context"
	"fmt"
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
