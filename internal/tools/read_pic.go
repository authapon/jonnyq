package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

	"jonnyq/internal/llm"
)

const maxImageBytes = 10 * 1024 * 1024

// PicTool reads an image file and returns it base64-encoded as a data URI,
// restricted to WorkDir. Note: this returns the encoded image as a plain
// text tool result; wiring it into a provider's native multimodal message
// format (Ollama's message.images / OpenAI's image_url content parts) is a
// follow-up, since it requires extending Message beyond a plain string body.
type PicTool struct{ WorkDir string }

func (t *PicTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read_pic",
		Description: "Read an image file inside the working directory and return it as a base64 data URI.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Image file path, relative to the working directory."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *PicTool) Call(ctx context.Context, args map[string]any) (string, error) {
	p, ok := stringArg(args, "path")
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	full, err := ResolvePath(t.WorkDir, p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.Size() > maxImageBytes {
		return "", fmt.Errorf("image is %d bytes, exceeds max of %d bytes", info.Size(), maxImageBytes)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(data)
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mime, encoded), nil
}
