package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jonnyq/internal/llm"
)

const maxReadBytes = 2 * 1024 * 1024 // 2MB cap so one read_file can't blow the context

// ReadFileTool reads a file's contents. Restricted to WorkDir.
type ReadFileTool struct{ WorkDir string }

func (t *ReadFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read_file",
		Description: "Read the contents of a text file inside the working directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path, relative to the working directory."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ReadFileTool) Call(ctx context.Context, args map[string]any) (string, error) {
	p, ok := stringArg(args, "path")
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	full, err := ResolvePath(t.WorkDir, p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if len(data) > maxReadBytes {
		return fmt.Sprintf("%s\n\n[truncated: file is %d bytes, showing first %d bytes]", string(data[:maxReadBytes]), len(data), maxReadBytes), nil
	}
	return string(data), nil
}

// WriteFileTool writes (overwriting) a file's contents. Restricted to WorkDir.
type WriteFileTool struct{ WorkDir string }

func (t *WriteFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "write_file",
		Description: "Create or overwrite a file inside the working directory with the given content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path, relative to the working directory."},
				"content": map[string]any{"type": "string", "description": "Full content to write to the file."},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *WriteFileTool) Call(ctx context.Context, args map[string]any) (string, error) {
	p, ok := stringArg(args, "path")
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	content, _ := stringArg(args, "content")
	full, err := ResolvePath(t.WorkDir, p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), p), nil
}

// EditFileTool replaces an exact substring in a file. Restricted to WorkDir.
type EditFileTool struct{ WorkDir string }

func (t *EditFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "edit_file",
		Description: "Replace an exact, unique occurrence of old_string with new_string in a file inside the working directory. Fails if old_string is not found, or is found more than once unless replace_all is true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "File path, relative to the working directory."},
				"old_string":  map[string]any{"type": "string", "description": "Exact text to replace."},
				"new_string":  map[string]any{"type": "string", "description": "Replacement text."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match. Defaults to false."},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func (t *EditFileTool) Call(ctx context.Context, args map[string]any) (string, error) {
	p, ok := stringArg(args, "path")
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	oldStr, ok := stringArg(args, "old_string")
	if !ok || oldStr == "" {
		return "", fmt.Errorf("old_string is required")
	}
	newStr, _ := stringArg(args, "new_string")
	replaceAll := boolArg(args, "replace_all", false)

	full, err := ResolvePath(t.WorkDir, p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", p)
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_string found %d times in %s; provide more context or set replace_all", count, p)
	}
	n := 1
	if replaceAll {
		n = -1
	}
	updated := strings.Replace(content, oldStr, newStr, n)
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", count, p), nil
}

// CreateFolderTool creates a directory (and parents). Restricted to WorkDir.
type CreateFolderTool struct{ WorkDir string }

func (t *CreateFolderTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "create_folder",
		Description: "Create a directory, including any missing parent directories, inside the working directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path, relative to the working directory."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *CreateFolderTool) Call(ctx context.Context, args map[string]any) (string, error) {
	p, ok := stringArg(args, "path")
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	full, err := ResolvePath(t.WorkDir, p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		return "", err
	}
	return fmt.Sprintf("created folder %s", p), nil
}
