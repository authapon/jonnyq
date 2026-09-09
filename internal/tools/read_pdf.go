package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"jonnyq/internal/llm"
)

// PDFTool reads PDF files by shelling out to poppler-utils (pdftotext,
// pdftoppm) rather than linking a PDF library into the binary, per user
// request. Both binaries must be installed and on PATH.
type PDFTool struct {
	WorkDir  string
	MaxPages int
	DPI      int
}

func (t *PDFTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read_pdf",
		Description: "Extract text from a PDF file inside the working directory (up to the configured max pages). If the PDF has no extractable text (e.g. a scanned document), its pages are rendered to PNG images instead and their paths are returned for use with read_pic.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "PDF file path, relative to the working directory."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *PDFTool) Call(ctx context.Context, args map[string]any) (string, error) {
	p, ok := stringArg(args, "path")
	if !ok {
		return "", fmt.Errorf("path is required")
	}
	full, err := ResolvePath(t.WorkDir, p)
	if err != nil {
		return "", err
	}

	maxPages := t.MaxPages
	if maxPages <= 0 {
		maxPages = 50
	}

	if _, err := exec.LookPath("pdftotext"); err != nil {
		return "", fmt.Errorf("read_pdf requires poppler-utils (pdftotext not found on PATH): %w", err)
	}

	textOut, err := runCapture(ctx, "pdftotext", "-layout", "-f", "1", "-l", strconv.Itoa(maxPages), full, "-")
	if err != nil {
		return "", fmt.Errorf("pdftotext failed: %w", err)
	}
	if strings.TrimSpace(textOut) != "" {
		return textOut, nil
	}

	// No extractable text - likely a scanned PDF. Render pages to images.
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return "", fmt.Errorf("pdf has no extractable text and pdftoppm is not on PATH to render page images: %w", err)
	}
	dpi := t.DPI
	if dpi <= 0 {
		dpi = 150
	}
	outDir := filepath.Join(t.WorkDir, ".jonnyq", "pdf_pages", strings.TrimSuffix(filepath.Base(full), filepath.Ext(full)))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	prefix := filepath.Join(outDir, "page")
	if _, err := runCapture(ctx, "pdftoppm", "-r", strconv.Itoa(dpi), "-png", "-f", "1", "-l", strconv.Itoa(maxPages), full, prefix); err != nil {
		return "", fmt.Errorf("pdftoppm failed: %w", err)
	}
	rel, _ := filepath.Rel(t.WorkDir, outDir)
	return fmt.Sprintf("no extractable text; rendered pages at %d DPI to %s/page-*.png - use read_pic on those files", dpi, rel), nil
}

func runCapture(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, stderr.String())
		}
		return "", err
	}
	return out.String(), nil
}
