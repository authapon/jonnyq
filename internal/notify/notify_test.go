package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
