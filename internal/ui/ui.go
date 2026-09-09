// Package ui renders colored terminal output while mirroring a plain-text
// (ANSI-stripped) transcript to the configured output file.
package ui

import (
	"fmt"
	"os"
	"regexp"
)

const (
	ColorReset  = "\x1b[0m"
	ColorWhite  = "\x1b[37m" // user prompt
	ColorGreen  = "\x1b[32m" // thinking
	ColorRed    = "\x1b[31m" // tool calls
	ColorYellow = "\x1b[33m" // model answer
	ColorGray   = "\x1b[90m" // trailing metrics
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Writer prints colored text to stdout (when stdout is a terminal) and
// always appends the plain-text equivalent to a log file.
type Writer struct {
	out      *os.File
	log      *os.File
	colorOut bool
}

func New(outputFile string) (*Writer, error) {
	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{
		out:      os.Stdout,
		log:      f,
		colorOut: isTerminal(os.Stdout),
	}, nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func (w *Writer) Close() error { return w.log.Close() }

// Print writes text to stdout wrapped in color (if stdout is a TTY) and the
// plain text to the log file.
func (w *Writer) Print(color, text string) {
	if w.colorOut {
		fmt.Fprint(w.out, color, text, ColorReset)
	} else {
		fmt.Fprint(w.out, text)
	}
	fmt.Fprint(w.log, stripANSI(text))
}

func (w *Writer) Println(color, text string) { w.Print(color, text+"\n") }

func (w *Writer) UserPrompt(text string) { w.Println(ColorWhite, text) }
func (w *Writer) Thinking(text string)   { w.Print(ColorGreen, text) }
func (w *Writer) ToolCall(text string)   { w.Println(ColorRed, text) }
func (w *Writer) Answer(text string)     { w.Print(ColorYellow, text) }
func (w *Writer) Meta(text string)       { w.Println(ColorGray, text) }

// Plain writes uncolored text to stdout and the log (for prompts, errors,
// help text, etc. that shouldn't carry one of the five semantic colors).
func (w *Writer) Plain(text string) {
	fmt.Fprint(w.out, text)
	fmt.Fprint(w.log, stripANSI(text))
}

func (w *Writer) Plainln(text string) { w.Plain(text + "\n") }
