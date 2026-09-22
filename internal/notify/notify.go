// Package notify sends push notifications via ntfy.sh (or a self-hosted
// ntfy server) by POSTing to a topic URL, e.g. "https://ntfy.sh/my-topic".
// See https://docs.ntfy.sh/publish/ for the wire format.
package notify

import (
	"bytes"
	"context"
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
	if !n.Enabled() {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewBufferString(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
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
