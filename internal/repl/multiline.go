package repl

import (
	"bufio"
	"strings"
)

// readInput reads one logical prompt from scanner, supporting two ways to
// enter multi-line input at the "> " prompt:
//   - a line ending in a trailing backslash continues on the next line
//     (the backslash is stripped, lines are joined with \n)
//   - a line containing only """ starts a block that runs verbatim until a
//     matching closing """ line
//
// ok is false when the input stream is exhausted (Ctrl-D), matching
// bufio.Scanner.Scan's convention.
func readInput(scanner *bufio.Scanner) (string, bool) {
	if !scanner.Scan() {
		return "", false
	}
	first := scanner.Text()
	if strings.TrimSpace(first) == `"""` {
		var lines []string
		for {
			if !scanner.Scan() {
				return "", false
			}
			line := scanner.Text()
			if strings.TrimSpace(line) == `"""` {
				return strings.Join(lines, "\n"), true
			}
			lines = append(lines, line)
		}
	}

	line, cont := stripTrailingContinuation(first)
	for cont {
		if !scanner.Scan() {
			break
		}
		var next string
		next, cont = stripTrailingContinuation(scanner.Text())
		line += "\n" + next
	}
	return line, true
}

func stripTrailingContinuation(s string) (string, bool) {
	s = strings.TrimRight(s, "\r")
	if strings.HasSuffix(s, `\`) {
		return strings.TrimSuffix(s, `\`), true
	}
	return s, false
}
