package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"jonnyq/internal/llm"
)

// SearxngTool searches the web through a searxng instance's JSON API.
// Disabled entirely (per spec) when BaseURL is empty.
type SearxngTool struct {
	BaseURL    string
	MaxResults int
	TimeoutSec int
	HTTP       *http.Client
	sem        chan struct{} // bounds concurrent searxng requests process-wide
}

func NewSearxngTool(baseURL string, maxResults, concurrency, timeoutSec int) *SearxngTool {
	if concurrency < 1 {
		concurrency = 1
	}
	return &SearxngTool{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		MaxResults: maxResults,
		TimeoutSec: timeoutSec,
		HTTP:       &http.Client{},
		sem:        make(chan struct{}, concurrency),
	}
}

func (t *SearxngTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "web_search",
		Description: "Search the web for a query via a searxng instance and return the top results (title, url, snippet).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search keywords."},
			},
			"required": []string{"query"},
		},
	}
}

type searxngResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func (t *SearxngTool) Call(ctx context.Context, args map[string]any) (string, error) {
	if t.BaseURL == "" {
		return "", fmt.Errorf("web_search is disabled: no searxng url configured")
	}
	query, ok := stringArg(args, "query")
	if !ok || strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}

	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.TimeoutSec)*time.Second)
	defer cancel()

	u := t.BaseURL + "/search?" + url.Values{"q": {query}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("searxng: unexpected status %s: %s", resp.Status, string(b))
	}

	var parsed searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}

	max := t.MaxResults
	if max <= 0 || max > len(parsed.Results) {
		max = len(parsed.Results)
	}
	if max == 0 {
		return "no results found", nil
	}
	var sb strings.Builder
	for i := 0; i < max; i++ {
		r := parsed.Results[i]
		fmt.Fprintf(&sb, "%d. %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return sb.String(), nil
}
