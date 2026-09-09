package repl

import (
	"bufio"
	"strings"
	"testing"
)

func scanFrom(s string) *bufio.Scanner {
	return bufio.NewScanner(strings.NewReader(s))
}

func TestReadInputSingleLine(t *testing.T) {
	sc := scanFrom("hello world\n")
	got, ok := readInput(sc)
	if !ok || got != "hello world" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
}

func TestReadInputBackslashContinuation(t *testing.T) {
	sc := scanFrom("line one \\\nline two \\\nline three\nnext prompt\n")
	got, ok := readInput(sc)
	if !ok {
		t.Fatal("expected ok=true")
	}
	want := "line one \nline two \nline three"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Scanner should be positioned at the next line for the next call.
	got2, ok2 := readInput(sc)
	if !ok2 || got2 != "next prompt" {
		t.Fatalf("second read got (%q, %v)", got2, ok2)
	}
}

func TestReadInputTripleQuoteBlock(t *testing.T) {
	sc := scanFrom("\"\"\"\nfirst line\nsecond line\n\"\"\"\nafter\n")
	got, ok := readInput(sc)
	if !ok {
		t.Fatal("expected ok=true")
	}
	want := "first line\nsecond line"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	got2, ok2 := readInput(sc)
	if !ok2 || got2 != "after" {
		t.Fatalf("second read got (%q, %v)", got2, ok2)
	}
}

func TestReadInputUnterminatedBlockIsEOF(t *testing.T) {
	sc := scanFrom("\"\"\"\nfirst line\n")
	if _, ok := readInput(sc); ok {
		t.Fatal("expected ok=false for an unterminated block hitting EOF")
	}
}

func TestReadInputEOF(t *testing.T) {
	sc := scanFrom("")
	if _, ok := readInput(sc); ok {
		t.Fatal("expected ok=false on empty input")
	}
}
