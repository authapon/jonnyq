// Package ui renders colored terminal output while mirroring a plain-text
// (ANSI-stripped) transcript to the configured output file.
package ui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// Context usage thresholds (percent of the context window) for coloring.
// ContextWarnPercent mirrors the point at which the agent compacts history.
const (
	ContextWarnPercent     = 60
	ContextCriticalPercent = 85
)

// ContextStatus is how much of the model's context window is in use.
// Estimated is true when Used was partly or wholly approximated from text
// length instead of a token count reported by the provider.
type ContextStatus struct {
	Used      int
	Total     int
	Estimated bool
}

// Left is the remaining tokens, never negative.
func (c ContextStatus) Left() int {
	if c.Used >= c.Total {
		return 0
	}
	return c.Total - c.Used
}

// Percent is Used as a whole percentage of Total (0 when Total is unset).
func (c ContextStatus) Percent() int {
	if c.Total <= 0 {
		return 0
	}
	return c.Used * 100 / c.Total
}

// Color is gray normally, yellow once usage reaches ContextWarnPercent
// (compaction is near) and red from ContextCriticalPercent.
func (c ContextStatus) Color() string {
	switch p := c.Percent(); {
	case p >= ContextCriticalPercent:
		return ColorRed
	case p >= ContextWarnPercent:
		return ColorYellow
	}
	return ColorGray
}

// String renders e.g. "ctx 6,300/16,348 (38%) left 10,048", with "~" before
// the figures that are estimates.
func (c ContextStatus) String() string {
	tilde := ""
	if c.Estimated {
		tilde = "~"
	}
	return fmt.Sprintf("ctx %s%s/%s (%s%d%%) left %s%s",
		tilde, groupDigits(c.Used), groupDigits(c.Total), tilde, c.Percent(), tilde, groupDigits(c.Left()))
}

// RequestStats summarizes one finished LLM request for section headers.
// Zero token/tok-per-sec values mean "unknown" and render as n/a.
type RequestStats struct {
	PromptTokens        int
	CompletionTokens    int
	CompletionEstimated bool
	TokPerSec           float64
	TokPerSecEstimated  bool
	Duration            time.Duration
}

// String renders e.g. "1,420 in/380 out, 22.4 tok/s, 17.0s", with "~" before
// estimated figures.
func (r RequestStats) String() string {
	in, out, tps := "n/a", "n/a", "n/a"
	if r.PromptTokens > 0 {
		in = groupDigits(r.PromptTokens)
	}
	if r.CompletionTokens > 0 {
		out = groupDigits(r.CompletionTokens)
		if r.CompletionEstimated {
			out = "~" + out
		}
	}
	if r.TokPerSec > 0 {
		tps = fmt.Sprintf("%.1f", r.TokPerSec)
		if r.TokPerSecEstimated {
			tps = "~" + tps
		}
	}
	return fmt.Sprintf("%s in/%s out, %s tok/s, %s", in, out, tps, formatElapsed(r.Duration))
}

// TurnStats is what section headers show beyond context usage: time since
// the turn started and, once one has finished, the latest LLM request.
type TurnStats struct {
	Elapsed time.Duration
	Req     *RequestStats // nil until a request has finished in this turn
}

// formatElapsed renders d as tenths of a second under a minute, else rounded
// to whole seconds (e.g. "17.0s", "1m5s").
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// groupDigits formats n with thousands separators.
func groupDigits(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

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

	// ContextFn, when set, supplies the current context usage that section
	// headers show after their timestamp. Nil leaves headers unchanged.
	ContextFn func() ContextStatus

	// TurnStatsFn, when set, supplies the elapsed-time and latest-request
	// figures appended after the context readout. It reports false when no
	// turn is running. The request shown is labeled "req" on a Tool call
	// header (printed right after that request finished, so it is the one
	// that produced the call) and "last req" on Thinking/Answer headers
	// (printed while the current request is still starting, so it is the
	// previous one).
	TurnStatsFn func() (TurnStats, bool)
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
// header label (with the current date/time in parentheses, marking when
// this section started) the first time it's called for a new kind of
// section; repeated calls for the same still-current section are a no-op so
// streamed chunks don't each get their own header. Streamed body text
// rarely ends in a newline, so this first finishes the current line (if
// needed) before inserting the actual blank line - a single "\n" alone
// would just end the current line rather than produce visible separation.
func (w *Writer) startSection(s section, bright, label string) {
	if w.current == s {
		return
	}
	w.current = s
	if !w.atLineStart {
		w.Plain("\n")
	}
	w.Plain("\n")
	stamped := fmt.Sprintf("%s (%s)", label, time.Now().Format("2006-01-02 15:04:05"))
	ctxText, ctxColor := "", ColorGray
	if w.ContextFn != nil {
		cs := w.ContextFn()
		ctxText, ctxColor = " | "+cs.String(), cs.Color()
	}
	if w.TurnStatsFn != nil {
		if ts, ok := w.TurnStatsFn(); ok {
			ctxText += " | +" + formatElapsed(ts.Elapsed)
			if ts.Req != nil {
				label := "last req"
				if s == sectionTool {
					label = "req"
				}
				ctxText += " | " + label + " " + ts.Req.String()
			}
		}
	}
	if w.colorOut {
		fmt.Fprint(w.out, ColorBold, bright, stamped, ColorReset)
		if ctxText != "" {
			fmt.Fprint(w.out, ctxColor, ctxText, ColorReset)
		}
		fmt.Fprint(w.out, "\n")
	} else {
		fmt.Fprint(w.out, stamped, ctxText, "\n")
	}
	fmt.Fprint(w.log, stamped, ctxText, "\n")
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

// MetaColored is Meta in a caller-chosen color (e.g. a context warning).
func (w *Writer) MetaColored(color, text string) { w.Println(color, text) }

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
