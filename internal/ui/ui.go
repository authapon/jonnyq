// Package ui renders colored terminal output while mirroring a plain-text
// (ANSI-stripped) transcript to the configured output file.
package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	ColorReset = "\x1b[0m"
	ColorBold  = "\x1b[1m"

	ColorWhite  = "\x1b[37m" // user prompt
	ColorGreen  = "\x1b[32m" // thinking body
	ColorRed    = "\x1b[31m" // tool call body
	ColorYellow = "\x1b[33m" // answer body
	ColorGray   = "\x1b[90m" // trailing metrics

	// Bright variants, used (bold+bright) for section headers so they stand
	// out from the body text printed in the base color above.
	ColorBrightGreen  = "\x1b[92m"
	ColorBrightRed    = "\x1b[91m"
	ColorBrightYellow = "\x1b[93m"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// section tracks which kind of streamed output is currently being printed,
// so a bold header is shown once per contiguous run of the same kind (with
// a blank line before it) instead of once per chunk of streamed text.
type section int

const (
	sectionNone section = iota
	sectionThinking
	sectionAnswer
	sectionTool
)

// Writer prints colored text to stdout (when stdout is a terminal) and
// always appends the plain-text equivalent to a log file.
type Writer struct {
	out      *os.File
	log      *os.File
	colorOut bool
	current  section
	// atLineStart is true once the last byte written was a newline (or
	// nothing has been written yet), so startSection knows whether it needs
	// to terminate the current line before inserting a blank separator one.
	atLineStart bool
}

func New(outputFile string) (*Writer, error) {
	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{
		out:         os.Stdout,
		log:         f,
		colorOut:    isTerminal(os.Stdout),
		atLineStart: true,
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
	if text == "" {
		return
	}
	if w.colorOut {
		fmt.Fprint(w.out, color, text, ColorReset)
	} else {
		fmt.Fprint(w.out, text)
	}
	fmt.Fprint(w.log, stripANSI(text))
	w.atLineStart = strings.HasSuffix(text, "\n")
}

func (w *Writer) Println(color, text string) { w.Print(color, text+"\n") }

// startSection prints a blank separator line and a bold, bright-colored
// header label the first time it's called for a new kind of section;
// repeated calls for the same still-current section are a no-op so streamed
// chunks don't each get their own header. Streamed body text rarely ends in
// a newline, so this first finishes the current line (if needed) before
// inserting the actual blank line - a single "\n" alone would just end the
// current line rather than produce visible separation.
func (w *Writer) startSection(s section, bright, label string) {
	if w.current == s {
		return
	}
	w.current = s
	if !w.atLineStart {
		w.Plain("\n")
	}
	w.Plain("\n")
	if w.colorOut {
		fmt.Fprint(w.out, ColorBold, bright, label, ColorReset, "\n")
	} else {
		fmt.Fprint(w.out, label, "\n")
	}
	fmt.Fprint(w.log, label, "\n")
	w.atLineStart = true
}

// NewTurn resets section tracking so the next output starts a fresh header
// even if it's the same kind that ended the previous turn.
func (w *Writer) NewTurn() { w.current = sectionNone }

func (w *Writer) UserPrompt(text string) { w.Println(ColorWhite, text) }

func (w *Writer) Thinking(text string) {
	w.startSection(sectionThinking, ColorBrightGreen, "Thinking")
	w.Print(ColorGreen, text)
}

func (w *Writer) Answer(text string) {
	w.startSection(sectionAnswer, ColorBrightYellow, "Answer")
	w.Print(ColorYellow, text)
}

func (w *Writer) ToolCall(text string) {
	w.startSection(sectionTool, ColorBrightRed, "Tool call")
	w.Println(ColorRed, text)
}

func (w *Writer) Meta(text string) { w.Println(ColorGray, text) }

// Plain writes uncolored text to stdout and the log (for prompts, errors,
// help text, etc. that shouldn't carry one of the semantic colors above).
func (w *Writer) Plain(text string) {
	if text == "" {
		return
	}
	fmt.Fprint(w.out, text)
	fmt.Fprint(w.log, stripANSI(text))
	w.atLineStart = strings.HasSuffix(text, "\n")
}

func (w *Writer) Plainln(text string) { w.Plain(text + "\n") }
