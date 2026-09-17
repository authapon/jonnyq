package agent

import "strings"

// repeatDetector flags degenerate output where a model repeats the same
// line/paragraph over and over without making progress - a real failure
// mode observed with some local models (via llama.cpp-based backends) that
// get stuck reasoning in circles in plain text instead of calling a tool or
// giving a final answer. Left unchecked, that burns tokens/time
// indefinitely (or until the provider's own generation limit kicks in).
//
// Detection is line/paragraph-based: it tracks the last few non-trivial
// lines and flags a loop once one of them recurs often enough within that
// window. This catches both a line repeated back-to-back and small cycles
// (e.g. two alternating sentences), which is what's been observed in
// practice; it will not catch repetition confined to a single line/
// paragraph with no newlines at all.
type repeatDetector struct {
	buf    strings.Builder
	recent []string
}

const (
	// repeatMinChunkLen ignores short lines (blank lines, "---", bullet
	// markers) that would otherwise trivially "repeat" without indicating a
	// real loop.
	repeatMinChunkLen = 20
	// repeatWindow is how many recent non-trivial lines are remembered.
	repeatWindow = 8
	// repeatThreshold is how many times one of those lines must recur
	// before it's treated as a loop.
	repeatThreshold = 3
)

// Add feeds one streamed delta into the detector and reports whether it
// just completed a line that pushes some recent line's count to the
// threshold.
func (d *repeatDetector) Add(delta string) bool {
	d.buf.WriteString(delta)
	for {
		s := d.buf.String()
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		chunk := strings.TrimSpace(s[:i])
		d.buf.Reset()
		d.buf.WriteString(s[i+1:])
		if len(chunk) < repeatMinChunkLen {
			continue
		}

		d.recent = append(d.recent, chunk)
		if len(d.recent) > repeatWindow {
			d.recent = d.recent[len(d.recent)-repeatWindow:]
		}

		count := 0
		for _, c := range d.recent {
			if c == chunk {
				count++
			}
		}
		if count >= repeatThreshold {
			return true
		}
	}
	return false
}
