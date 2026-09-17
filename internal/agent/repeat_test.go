package agent

import "testing"

func feedAll(d *repeatDetector, deltas []string) (loopAt int) {
	for i, delta := range deltas {
		if d.Add(delta) {
			return i
		}
	}
	return -1
}

func TestRepeatDetectorCatchesAlternatingSentenceLoop(t *testing.T) {
	// Reproduces the reported /autocoding failure: the model alternates
	// between two near-identical sentences indefinitely instead of taking
	// any action.
	a := "Wait, I'll try to use `golang:1.23-bullseye` in the Dockerfile to see if it resolves any dependency issues, and I'll also ensure `go.mod` is correctly set.\n\n"
	b := "Actually, I'll just try to fix `go.mod` one more time very carefully.\n\n"
	var deltas []string
	for i := 0; i < 6; i++ {
		deltas = append(deltas, a, b)
	}

	d := &repeatDetector{}
	at := feedAll(d, deltas)
	if at < 0 {
		t.Fatal("expected the alternating loop to be detected, but it never triggered")
	}
	// It should trigger well before all 12 chunks are fed - the whole point
	// is to cut the loop short, not let it run to completion.
	if at > 6 {
		t.Errorf("expected the loop to be caught within a handful of repeats, triggered at delta %d", at)
	}
}

func TestRepeatDetectorAllowsNormalVariedText(t *testing.T) {
	d := &repeatDetector{}
	deltas := []string{
		"First I'll read the Dockerfile to see what base image it uses.\n\n",
		"The go.mod file currently requires go 1.24, but the image only has 1.23.\n\n",
		"I'll lower the go directive to 1.23 and rerun the build to confirm it works.\n\n",
		"The build now succeeds and the container starts correctly on port 3033.\n\n",
	}
	if at := feedAll(d, deltas); at >= 0 {
		t.Errorf("expected no loop to be detected in varied, non-repeating text, triggered at delta %d", at)
	}
}

func TestRepeatDetectorIgnoresShortLines(t *testing.T) {
	d := &repeatDetector{}
	// Short lines (blank separators, bullet markers) recur constantly in
	// normal output and must not be mistaken for a loop.
	deltas := []string{
		"- a\n", "- b\n", "- c\n", "- d\n", "- e\n", "- f\n", "\n", "\n", "\n",
	}
	if at := feedAll(d, deltas); at >= 0 {
		t.Errorf("expected short/blank lines to be ignored, triggered at delta %d", at)
	}
}
