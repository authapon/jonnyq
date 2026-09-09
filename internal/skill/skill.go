// Package skill discovers skill files under the configured skill_path
// directories. A skill is either a subdirectory containing SKILL.md, or a
// standalone .md file; its description is the first non-empty, non-heading
// line of that markdown so it can be summarized to the model without
// requiring any frontmatter parser.
package skill

import (
	"os"
	"path/filepath"
	"strings"
)

type Skill struct {
	Name        string
	Description string
	Path        string
}

func Discover(paths []string) []Skill {
	var out []Skill
	for _, root := range paths {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				mdPath := filepath.Join(root, e.Name(), "SKILL.md")
				if data, err := os.ReadFile(mdPath); err == nil {
					out = append(out, Skill{Name: e.Name(), Description: firstLine(string(data)), Path: mdPath})
				}
				continue
			}
			if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
				full := filepath.Join(root, e.Name())
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
				out = append(out, Skill{Name: name, Description: firstLine(string(data)), Path: full})
			}
		}
	}
	return out
}

func firstLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			if len(line) > 200 {
				line = line[:200]
			}
			return line
		}
	}
	return ""
}
