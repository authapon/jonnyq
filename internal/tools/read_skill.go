package tools

import (
	"context"
	"fmt"
	"os"

	"jonnyq/internal/llm"
	"jonnyq/internal/skill"
)

// SkillTool loads the full content of one discovered skill by name.
type SkillTool struct{ Paths []string }

func (t *SkillTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read_skill",
		Description: "Read the full content of a skill by name (see the skill list provided each turn).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Skill name, as listed."},
			},
			"required": []string{"name"},
		},
	}
}

func (t *SkillTool) Call(ctx context.Context, args map[string]any) (string, error) {
	name, ok := stringArg(args, "name")
	if !ok || name == "" {
		return "", fmt.Errorf("name is required")
	}
	for _, s := range skill.Discover(t.Paths) {
		if s.Name == name {
			data, err := os.ReadFile(s.Path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
	}
	return "", fmt.Errorf("skill %q not found", name)
}
