// Package tools implements the concrete tools the model can call:
// read_file, write_file, edit_file, create_folder, web_search, web_fetch,
// read_pdf, read_pic, run_command, and read_skill.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"jonnyq/internal/llm"
)

// Tool is implemented by every callable tool.
type Tool interface {
	Spec() llm.ToolSpec
	Call(ctx context.Context, args map[string]any) (string, error)
}

// Registry holds the tools available to the model for one session.
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	name := t.Spec().Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Specs returns tool specs in registration order, for passing to the provider.
func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.tools[name].Spec())
	}
	return specs
}

// Call dispatches a tool call by name with raw JSON arguments.
func (r *Registry) Call(ctx context.Context, name, argsJSON string) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	args := map[string]any{}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid arguments for %s: %w", name, err)
		}
	}
	return t.Call(ctx, args)
}

func stringArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func boolArg(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}
