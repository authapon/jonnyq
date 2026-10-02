// Package notify sends push notifications via ntfy.sh (or a self-hosted
// ntfy server) by POSTing to a topic URL, e.g. "https://ntfy.sh/my-topic".
// See https://docs.ntfy.sh/publish/ for the wire format.
package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Notifier posts messages to a configured ntfy topic URL. The zero value
// (and a nil *Notifier) is a valid, disabled notifier: Send is then a
// no-op, so callers don't need to check Enabled before calling it.
type Notifier struct {
	url    string
	client *http.Client
}

// New builds a Notifier for the given ntfy topic URL. An empty/whitespace
// url disables it.
func New(url string) *Notifier {
	return &Notifier{
		url:    strings.TrimSpace(url),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether a topic URL is configured. Safe to call on a nil
// receiver.
func (n *Notifier) Enabled() bool {
	return n != nil && n.url != ""
}

// Send posts title+message to the configured ntfy topic. A no-op returning
// nil when disabled (including a nil receiver), so callers can call it
// unconditionally.
func (n *Notifier) Send(ctx context.Context, title, message string) error {
	return n.post(ctx, title, message, nil)
}

// SendError posts a failure notification: a high-priority message (with a
// warning tag so ntfy clients render it distinctively) containing the raw
// error plus a plain-language explanation of the likely cause from
// Explain. extra is optional context appended after the explanation (e.g.
// timing). A no-op returning nil when disabled.
func (n *Notifier) SendError(ctx context.Context, title string, err error, extra string) error {
	msg := FormatError(err)
	if extra != "" {
		msg += "\n\n" + extra
	}
	return n.SendAlert(ctx, title, msg)
}

// SendAlert posts message as a high-priority warning notification (see
// SendError). A no-op returning nil when disabled.
func (n *Notifier) SendAlert(ctx context.Context, title, message string) error {
	return n.post(ctx, title, message, map[string]string{
		"Priority": "high",
		"Tags":     "warning",
	})
}

func (n *Notifier) post(ctx context.Context, title, message string, headers map[string]string) error {
	if !n.Enabled() {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewBufferString(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: server returned %s", resp.Status)
	}
	return nil
}

// notifiedError marks an error whose failure notification has already been
// sent, so an outer layer (the REPL) doesn't notify about it a second time.
type notifiedError struct{ err error }

func (e *notifiedError) Error() string { return e.err.Error() }
func (e *notifiedError) Unwrap() error { return e.err }

// MarkNotified wraps err to record that a notification was already sent
// for it. A nil err stays nil.
func MarkNotified(err error) error {
	if err == nil || IsNotified(err) {
		return err
	}
	return &notifiedError{err: err}
}

// IsNotified reports whether err (or anything it wraps) was marked by
// MarkNotified.
func IsNotified(err error) bool {
	var ne *notifiedError
	return errors.As(err, &ne)
}
