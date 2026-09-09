package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
	if usage.HasTiming {
		t.Errorf("openai usage should not report native timing")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 3 || usage.TotalTokens != 13 {
		t.Errorf("unexpected usage: %+v", usage)
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
