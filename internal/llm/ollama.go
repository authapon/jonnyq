package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaProvider talks to Ollama's native /api/chat and /api/tags endpoints.
// Native streaming is newline-delimited JSON, and Ollama reports real
// load/prompt-eval/eval timings, so metrics displayed to the user are exact
// rather than client-side estimates.
type OllamaProvider struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

func NewOllamaProvider(baseURL, key string) *OllamaProvider {
	return &OllamaProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Key:     key,
		HTTP:    &http.Client{},
	}
}

type owFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type owToolCall struct {
	Function owFunctionCall `json:"function"`
}

type owTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type owMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	ToolCalls []owToolCall `json:"tool_calls,omitempty"`
}

type owOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
}

type owChatRequest struct {
	Model    string      `json:"model"`
	Messages []owMessage `json:"messages"`
	Tools    []owTool    `json:"tools,omitempty"`
	Stream   bool        `json:"stream"`
	Think    *bool       `json:"think,omitempty"`
	Options  *owOptions  `json:"options,omitempty"`
}

type owChatResponse struct {
	Message            owMessage `json:"message"`
	Done               bool      `json:"done"`
	TotalDuration      int64     `json:"total_duration"`
	LoadDuration       int64     `json:"load_duration"`
	PromptEvalCount    int       `json:"prompt_eval_count"`
	PromptEvalDuration int64     `json:"prompt_eval_duration"`
	EvalCount          int       `json:"eval_count"`
	EvalDuration       int64     `json:"eval_duration"`
	Error              string    `json:"error"`
}

func (p *OllamaProvider) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	return req, nil
}

func toOllamaMessages(msgs []Message) []owMessage {
	out := make([]owMessage, 0, len(msgs))
	for _, m := range msgs {
		om := owMessage{Role: string(m.Role), Content: m.Content}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			om.ToolCalls = append(om.ToolCalls, owToolCall{Function: owFunctionCall{
				Name:      tc.Name,
				Arguments: json.RawMessage(args),
			}})
		}
		out = append(out, om)
	}
	return out
}

func toOllamaTools(tools []ToolSpec) []owTool {
	out := make([]owTool, 0, len(tools))
	for _, t := range tools {
		var ot owTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Parameters
		out = append(out, ot)
	}
	return out
}

func (p *OllamaProvider) Chat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	think := req.Thinking
	body := owChatRequest{
		Model:    req.Model,
		Messages: toOllamaMessages(req.Messages),
		Tools:    toOllamaTools(req.Tools),
		Stream:   true,
		Think:    &think,
	}
	if req.ContextSize > 0 {
		body.Options = &owOptions{NumCtx: req.ContextSize}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := p.newRequest(ctx, http.MethodPost, "/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: unexpected status %s: %s", resp.Status, string(b))
	}

	out := make(chan ChatEvent)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		dec := json.NewDecoder(resp.Body)
		for {
			var chunk owChatResponse
			if err := dec.Decode(&chunk); err != nil {
				if err == io.EOF {
					return
				}
				out <- ChatEvent{Kind: EventError, Err: err}
				return
			}
			if chunk.Error != "" {
				out <- ChatEvent{Kind: EventError, Err: fmt.Errorf("ollama: %s", chunk.Error)}
				return
			}
			if chunk.Message.Thinking != "" {
				out <- ChatEvent{Kind: EventThinking, Delta: chunk.Message.Thinking}
			}
			if chunk.Message.Content != "" {
				out <- ChatEvent{Kind: EventContent, Delta: chunk.Message.Content}
			}
			if len(chunk.Message.ToolCalls) > 0 {
				var calls []ToolCall
				for i, tc := range chunk.Message.ToolCalls {
					args := "{}"
					if len(tc.Function.Arguments) > 0 {
						args = string(tc.Function.Arguments)
					}
					calls = append(calls, ToolCall{
						ID:        fmt.Sprintf("call_%d", i),
						Name:      tc.Function.Name,
						Arguments: args,
					})
				}
				out <- ChatEvent{Kind: EventToolCalls, ToolCalls: calls}
			}
			if chunk.Done {
				out <- ChatEvent{Kind: EventDone, Usage: Usage{
					PromptTokens:       chunk.PromptEvalCount,
					CompletionTokens:   chunk.EvalCount,
					TotalTokens:        chunk.PromptEvalCount + chunk.EvalCount,
					LoadDuration:       time.Duration(chunk.LoadDuration),
					PromptEvalDuration: time.Duration(chunk.PromptEvalDuration),
					EvalDuration:       time.Duration(chunk.EvalDuration),
					HasTiming:          true,
				}}
				return
			}
		}
	}()
	return out, nil
}

type owTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := p.newRequest(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: unexpected status %s: %s", resp.Status, string(b))
	}
	var tags owTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names, nil
}
