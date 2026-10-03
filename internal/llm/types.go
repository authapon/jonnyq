// Package llm defines a provider-agnostic chat interface used by the agent
// loop, plus concrete implementations for Ollama (native API) and
// OpenAI-compatible chat completion endpoints.
package llm

import (
	"context"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a single function call requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object, as text
}

// Message is one turn in the conversation sent to/from the provider.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // set on RoleTool messages: which call this answers
	Name       string // set on RoleTool messages: the tool name
}

// ToolSpec describes a callable tool using JSON-schema parameters, following
// the OpenAI function-calling convention (also accepted by Ollama's /api/chat).
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Usage carries token counts and, when available, timing breakdowns.
// HasTiming is false when no timing exists at all, so callers can render
// "n/a" instead of a fabricated number. Timing is either reported by the
// provider (Ollama) or measured client-side while streaming (OpenAI-compatible
// endpoints, which only report token counts); Estimated marks the latter so it
// can be shown as approximate.
type Usage struct {
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	LoadDuration       time.Duration
	PromptEvalDuration time.Duration
	EvalDuration       time.Duration
	HasTiming          bool
	// Estimated is true when the timings were measured by the client rather
	// than reported by the provider: PromptEvalDuration is then the time to
	// the first streamed token (load + prompt eval + network, not separable),
	// LoadDuration is unavailable, and EvalDuration is first-to-last token
	// time as seen by the client.
	Estimated bool
	// CompletionEstimated is true when CompletionTokens was approximated from
	// the streamed text because the provider sent no usage block.
	CompletionEstimated bool
}

// ChatRequest is one call to Chat.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolSpec
	Thinking bool
	// ContextSize, when > 0, is passed as the model's context window size
	// (Ollama's num_ctx). OpenAI-compatible providers have no equivalent
	// per-request knob and ignore it.
	ContextSize int
}

type EventKind int

const (
	EventThinking EventKind = iota
	EventContent
	EventToolCalls
	EventDone
	EventError
)

// ChatEvent is one streamed unit of a Chat response.
type ChatEvent struct {
	Kind      EventKind
	Delta     string
	ToolCalls []ToolCall
	Usage     Usage
	Err       error
}

// Provider is implemented by each backend (ollama, openai).
type Provider interface {
	// Chat streams the model's reply. The returned channel is closed after
	// an EventDone or EventError event.
	Chat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	ListModels(ctx context.Context) ([]string, error)
}
