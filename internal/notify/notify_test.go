package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendPostsTitleAndBody(t *testing.T) {
	var gotMethod, gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotTitle = r.Header.Get("Title")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(srv.URL)
	if err := n.Send(context.Background(), "jonnyq: /coding", "task done"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotTitle != "jonnyq: /coding" {
		t.Errorf("expected Title header %q, got %q", "jonnyq: /coding", gotTitle)
	}
	if gotBody != "task done" {
		t.Errorf("expected body %q, got %q", "task done", gotBody)
	}
}

func TestSendNoopWhenDisabled(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	n := New("") // disabled
	if n.Enabled() {
		t.Error("expected Enabled() to be false for an empty URL")
	}
	if err := n.Send(context.Background(), "title", "message"); err != nil {
		t.Errorf("expected no error from a disabled Notifier, got: %v", err)
	}
	if called {
		t.Error("expected no HTTP request when disabled")
	}
}

func TestNilNotifierIsSafeNoop(t *testing.T) {
	var n *Notifier
	if n.Enabled() {
		t.Error("expected Enabled() to be false for a nil Notifier")
	}
	if err := n.Send(context.Background(), "title", "message"); err != nil {
		t.Errorf("expected no error from a nil Notifier, got: %v", err)
	}
}

func TestSendReturnsErrorOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := New(srv.URL)
	if err := n.Send(context.Background(), "title", "message"); err == nil {
		t.Error("expected an error on a 500 response")
	}
}

func TestEnabledTrimsWhitespace(t *testing.T) {
	n := New("   ")
	if n.Enabled() {
		t.Error("expected a whitespace-only URL to be treated as disabled")
	}
}

func TestSendErrorIsHighPriorityAndExplainsReason(t *testing.T) {
	var gotPriority, gotTags, gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPriority = r.Header.Get("Priority")
		gotTags = r.Header.Get("Tags")
		gotTitle = r.Header.Get("Title")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
	}))
	defer srv.Close()

	err := fmt.Errorf("working on %q: %w", "x", errors.New("Post http://localhost:11434: dial tcp: connect: connection refused"))
	if sendErr := New(srv.URL).SendError(context.Background(), "jonnyq: error", err, "Duration: 1s"); sendErr != nil {
		t.Fatalf("SendError failed: %v", sendErr)
	}
	if gotPriority != "high" || gotTags != "warning" || gotTitle != "jonnyq: error" {
		t.Errorf("unexpected headers: priority=%q tags=%q title=%q", gotPriority, gotTags, gotTitle)
	}
	for _, want := range []string{"Error: working on", "connection refused", "Reason:", "not running", "Duration: 1s"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("expected body to contain %q, got: %q", want, gotBody)
		}
	}
}

func TestSendErrorNoopWhenDisabled(t *testing.T) {
	var n *Notifier
	if err := n.SendError(context.Background(), "t", errors.New("boom"), ""); err != nil {
		t.Errorf("expected no error from a nil Notifier, got: %v", err)
	}
}

func TestExplain(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("ollama: unexpected status 404 Not Found: model not found"), "model name"},
		{errors.New("openai: unexpected status 401 Unauthorized: bad key"), "API key"},
		{errors.New("openai: unexpected status 429 Too Many Requests"), "rate limiting"},
		{errors.New("openai: unexpected status 503 Service Unavailable"), "server-side"},
		{errors.New("provider stream ended without a completion event"), "dropped"},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), "too long"},
		{errors.New(`task "a" made no progress after 5 attempts`), "never checked it off"},
		{errors.New("requirements.md not found in the working directory"), "requirements.md"},
		{errors.New("tool call budget exceeded for this turn (limit 50)"), "loop"},
		{errors.New("something weird"), "Unexpected error"},
	}
	for _, c := range cases {
		if got := Explain(c.err); !strings.Contains(got, c.want) {
			t.Errorf("Explain(%q) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
	if Explain(nil) != "" {
		t.Error("expected empty explanation for nil error")
	}
}

func TestMarkNotified(t *testing.T) {
	base := errors.New("boom")
	if IsNotified(base) {
		t.Error("plain error should not be notified")
	}
	marked := MarkNotified(base)
	if !IsNotified(fmt.Errorf("wrapped: %w", marked)) || !errors.Is(marked, base) || marked.Error() != "boom" {
		t.Error("marked error should be detectable, unwrap to the original and keep its text")
	}
	if MarkNotified(nil) != nil {
		t.Error("MarkNotified(nil) should stay nil")
	}
}
