package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathWithinWorkDir(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolvePath(dir, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "sub", "file.txt")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolvePathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		"../outside.txt",
		"../../etc/passwd",
		"sub/../../outside.txt",
	}
	for _, c := range cases {
		if _, err := ResolvePath(dir, c); err == nil {
			t.Errorf("ResolvePath(%q) should have failed but succeeded", c)
		}
	}
}

func TestResolvePathAbsoluteOutsideRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolvePath(dir, "/etc/passwd"); err == nil {
		t.Errorf("absolute path outside workdir should be rejected")
	}
}

func TestResolvePathSymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}
	if _, err := ResolvePath(dir, "escape/secret.txt"); err == nil {
		t.Errorf("symlink escaping workdir should be rejected")
	}
}

func TestReadWriteEditCreateStayWithinWorkDir(t *testing.T) {
	dir := t.TempDir()
	wf := &WriteFileTool{WorkDir: dir}
	if _, err := wf.Call(context.Background(), map[string]any{"path": "a/b.txt", "content": "hello"}); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	rf := &ReadFileTool{WorkDir: dir}
	out, err := rf.Call(context.Background(), map[string]any{"path": "a/b.txt"})
	if err != nil || out != "hello" {
		t.Fatalf("read_file got (%q, %v)", out, err)
	}

	ef := &EditFileTool{WorkDir: dir}
	if _, err := ef.Call(context.Background(), map[string]any{"path": "a/b.txt", "old_string": "hello", "new_string": "world"}); err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	out, _ = rf.Call(context.Background(), map[string]any{"path": "a/b.txt"})
	if out != "world" {
		t.Fatalf("edit_file did not apply, got %q", out)
	}

	cf := &CreateFolderTool{WorkDir: dir}
	if _, err := cf.Call(context.Background(), map[string]any{"path": "newdir"}); err != nil {
		t.Fatalf("create_folder: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "newdir")); err != nil || !info.IsDir() {
		t.Fatalf("newdir was not created")
	}

	for _, tt := range []struct {
		name string
		call func() (string, error)
	}{
		{"write_file", func() (string, error) {
			return wf.Call(context.Background(), map[string]any{"path": "../escape.txt", "content": "x"})
		}},
		{"read_file", func() (string, error) { return rf.Call(context.Background(), map[string]any{"path": "../escape.txt"}) }},
		{"create_folder", func() (string, error) { return cf.Call(context.Background(), map[string]any{"path": "../escape"}) }},
	} {
		if _, err := tt.call(); err == nil {
			t.Errorf("%s: expected error escaping working directory, got none", tt.name)
		}
	}
}
