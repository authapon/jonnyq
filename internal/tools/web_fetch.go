package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"jonnyq/internal/llm"
)

const maxFetchBytes = 3 * 1024 * 1024 // cap response body so one fetch can't blow the context

// FetchTool downloads a URL and, for HTML pages, extracts readable text.
// HTML-to-text is done with stdlib regexp/html (no HTML-parsing dependency)
// which is good enough to strip markup and decode entities for typical pages.
type FetchTool struct {
	TimeoutSec int
	HTTP       *http.Client
	sem        chan struct{}
}

func NewFetchTool(concurrency, timeoutSec int) *FetchTool {
	if concurrency < 1 {
		concurrency = 1
	}
	return &FetchTool{
		TimeoutSec: timeoutSec,
		HTTP:       &http.Client{},
		sem:        make(chan struct{}, concurrency),
	}
}

func (t *FetchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "web_fetch",
		Description: "Fetch a URL and return its readable text content (HTML is stripped of markup).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "The URL to fetch."},
			},
			"required": []string{"url"},
		},
	}
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript|template)[^>]*>.*?</\s*(script|style|noscript|template)\s*>`)
	blockTagRe    = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|header|footer)[^>]*>`)
	anyTagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	multiSpaceRe  = regexp.MustCompile(`[ \t]+`)
	multiBlankRe  = regexp.MustCompile(`\n{3,}`)
)

func htmlToText(body string) string {
	body = scriptStyleRe.ReplaceAllString(body, "\n")
	body = blockTagRe.ReplaceAllString(body, "\n")
	body = anyTagRe.ReplaceAllString(body, "")
	body = html.UnescapeString(body)
	body = multiSpaceRe.ReplaceAllString(body, " ")

	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	text := strings.Join(out, "\n")
	return multiBlankRe.ReplaceAllString(text, "\n\n")
}

func (t *FetchTool) Call(ctx context.Context, args map[string]any) (string, error) {
	rawURL, ok := stringArg(args, "url")
	if !ok || strings.TrimSpace(rawURL) == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("url must start with http:// or https://")
	}

	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.TimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "jonnyq/1.0")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_fetch: unexpected status %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", err
	}

	contentType := resp.Header.Get("Content-Type")
	body := string(data)
	if strings.Contains(contentType, "html") {
		body = htmlToText(body)
	}
	if len(body) > maxFetchBytes {
		body = body[:maxFetchBytes] + "\n\n[truncated]"
	}
	return body, nil
}
