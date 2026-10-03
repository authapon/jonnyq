package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// approxCharsPerToken is used only to estimate completion tokens when the
// endpoint sends no usage block.
const approxCharsPerToken = 4

// streamTimer measures generation timing client-side: the time from the
// request to the first streamed token, and from the first to the last
// streamed token, plus how much text arrived (for a token estimate).
type streamTimer struct {
	start, first, last time.Time
	chars              int
}

// mark records a streamed delta carrying n characters of output.
func (t *streamTimer) mark(n int) {
	if n <= 0 {
		return
	}
	now := time.Now()
	if t.first.IsZero() {
		t.first = now
	}
	t.last = now
	t.chars += n
}

// apply fills u's timing (and, if the endpoint reported no completion
// tokens, an estimate of them) from what was measured. u is left untouched
// if nothing was streamed.
func (t *streamTimer) apply(u Usage) Usage {
	if t.first.IsZero() {
		return u
	}
	u.HasTiming = true
	u.Estimated = true
	u.PromptEvalDuration = t.first.Sub(t.start)
	u.EvalDuration = t.last.Sub(t.first)
	if u.CompletionTokens == 0 {
		u.CompletionTokens = (t.chars + approxCharsPerToken - 1) / approxCharsPerToken
		u.CompletionEstimated = true
	}
	return u
}

// OpenAIProvider talks to any OpenAI-compatible /v1/chat/completions endpoint
// over SSE. Unlike Ollama, these endpoints only report token counts, not
// load/prompt-eval/eval timings, so Chat measures timing client-side while
// streaming and flags it as Usage.Estimated.
type OpenAIProvider struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

func NewOpenAIProvider(baseURL, key string) *OpenAIProvider {
	return &OpenAIProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Key:     key,
		HTTP:    &http.Client{},
	}
}

type oaMessage struct {
	Role string `json:"role"`
	// Content intentionally has no omitempty: some OpenAI-compatible
	// servers (e.g. LM Studio's llama.cpp backend) reject a message whose
	// "content" key is missing entirely, which is what encoding/json would
	// do for a tool result that happened to be an empty string (e.g. a
	// shell command that succeeded with no output). Sending "content":""
	// is always valid; omitting the key is not, for some servers.
	Content    string       `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaChatRequest struct {
	Model         string           `json:"model"`
	Messages      []oaMessage      `json:"messages"`
	Tools         []oaTool         `json:"tools,omitempty"`
	Stream        bool             `json:"stream"`
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
}

type oaToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []oaToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func toOpenAIMessages(msgs []Message) []oaMessage {
	out := make([]oaMessage, 0, len(msgs))
	for _, m := range msgs {
		om := oaMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			var oc oaToolCall
			oc.ID = tc.ID
			oc.Type = "function"
			oc.Function.Name = tc.Name
			oc.Function.Arguments = args
			om.ToolCalls = append(om.ToolCalls, oc)
		}
		out = append(out, om)
	}
	return out
}

func toOpenAITools(tools []ToolSpec) []oaTool {
	out := make([]oaTool, 0, len(tools))
	for _, t := range tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Parameters
		out = append(out, ot)
	}
	return out
}

func (p *OpenAIProvider) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
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

type toolCallBuilder struct {
	id   string
	name string
	args strings.Builder
}

func (p *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	body := oaChatRequest{
		Model:         req.Model,
		Messages:      toOpenAIMessages(req.Messages),
		Tools:         toOpenAITools(req.Tools),
		Stream:        true,
		StreamOptions: &oaStreamOptions{IncludeUsage: true},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := p.newRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	timer := &streamTimer{start: time.Now()}
	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai: unexpected status %s: %s", resp.Status, string(b))
	}

	out := make(chan ChatEvent)
	go func() {
		defer resp.Body.Close()
		defer close(out)

		builders := map[int]*toolCallBuilder{}
		var usage Usage
		flushToolCalls := func() {
			if len(builders) == 0 {
				return
			}
			idxs := make([]int, 0, len(builders))
			for i := range builders {
				idxs = append(idxs, i)
			}
			sort.Ints(idxs)
			calls := make([]ToolCall, 0, len(idxs))
			for _, i := range idxs {
				b := builders[i]
				calls = append(calls, ToolCall{ID: b.id, Name: b.name, Arguments: b.args.String()})
			}
			out <- ChatEvent{Kind: EventToolCalls, ToolCalls: calls}
			builders = map[int]*toolCallBuilder{}
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				flushToolCalls()
				out <- ChatEvent{Kind: EventDone, Usage: timer.apply(usage)}
				return
			}
			var chunk oaChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				out <- ChatEvent{Kind: EventError, Err: err}
				return
			}
			if chunk.Usage != nil {
				usage.PromptTokens = chunk.Usage.PromptTokens
				usage.CompletionTokens = chunk.Usage.CompletionTokens
				usage.TotalTokens = chunk.Usage.TotalTokens
			}
			for _, choice := range chunk.Choices {
				n := len(choice.Delta.ReasoningContent) + len(choice.Delta.Content)
				for _, tc := range choice.Delta.ToolCalls {
					n += len(tc.Function.Name) + len(tc.Function.Arguments)
				}
				timer.mark(n)
				if choice.Delta.ReasoningContent != "" {
					out <- ChatEvent{Kind: EventThinking, Delta: choice.Delta.ReasoningContent}
				}
				if choice.Delta.Content != "" {
					out <- ChatEvent{Kind: EventContent, Delta: choice.Delta.Content}
				}
				for _, tc := range choice.Delta.ToolCalls {
					b, ok := builders[tc.Index]
					if !ok {
						b = &toolCallBuilder{}
						builders[tc.Index] = b
					}
					if tc.ID != "" {
						b.id = tc.ID
					}
					if tc.Function.Name != "" {
						b.name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						b.args.WriteString(tc.Function.Arguments)
					}
				}
				if choice.FinishReason != "" {
					flushToolCalls()
				}
			}
		}
		if err := scanner.Err(); err != nil {
			out <- ChatEvent{Kind: EventError, Err: err}
			return
		}
		flushToolCalls()
		out <- ChatEvent{Kind: EventDone, Usage: timer.apply(usage)}
	}()
	return out, nil
}

type oaModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (p *OpenAIProvider) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := p.newRequest(ctx, http.MethodGet, "/models", nil)
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
		return nil, fmt.Errorf("openai: unexpected status %s: %s", resp.Status, string(b))
	}
	var models oaModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(models.Data))
	for _, m := range models.Data {
		names = append(names, m.ID)
	}
	return names, nil
}
