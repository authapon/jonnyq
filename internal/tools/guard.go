package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolvePath resolves p (absolute or relative to workDir) and guarantees the
// result stays inside workDir. It walks the path component by component,
// resolving symlinks as it goes, so a symlink placed inside workDir cannot be
// used to escape it (a plain filepath.Clean check alone would not catch that).
func ResolvePath(workDir, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is required")
	}

	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	workReal, err := filepath.EvalSymlinks(workAbs)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	var target string
	if filepath.IsAbs(p) {
		target = filepath.Clean(p)
	} else {
		target = filepath.Clean(filepath.Join(workReal, p))
	}

	rel, err := filepath.Rel(workReal, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", p)
	}

	cur := workReal
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		real, err := filepath.EvalSymlinks(cur)
		if err != nil {
			// Component doesn't exist yet (e.g. a file being created) -
			// nothing more to resolve below it.
			break
		}
		cur = real
		innerRel, err := filepath.Rel(workReal, cur)
		if err != nil || innerRel == ".." || strings.HasPrefix(innerRel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q escapes the working directory via a symlink", p)
		}
	}

	return target, nil
}
