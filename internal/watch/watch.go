// Package watch implements jonnyq's pair-programming trigger feature: it
// polls the working directory for changed files (stdlib-only - no fsnotify
// dependency) and, when a file changes, scans it for lines containing a
// configured "magic word" (e.g. "AI!"), emitting a Trigger for each newly
// seen one so the REPL can hand it to the agent as an instruction.
package watch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Trigger is one magic-word line found in a changed file.
type Trigger struct {
	File string // path relative to the watched working directory
	Line int    // 1-based line number
	Text string // the full line, trimmed
}

// skipDirNames are directory basenames never descended into, on top of any
// dotfile/dot-directory (which also covers jonnyq's own .git, .context,
// .progress* state, and .jonnyq/ cache).
var skipDirNames = map[string]bool{
	"node_modules": true,
	"vendor":       true,
}

type fileState struct {
	modTime time.Time
	seen    map[string]bool // trigger line text already emitted for this file
}

// Watcher polls workDir for changed files and reports new magic-word lines.
// Zero value is not usable; construct with New.
type Watcher struct {
	workDir  string
	interval time.Duration

	mu        sync.Mutex
	magicWord string
	started   bool

	stopOnce     sync.Once
	stop         chan struct{}
	done         chan struct{}
	state        map[string]*fileState
	excludeFiles map[string]bool
}

// New constructs a Watcher for workDir. excludeFiles are paths (relative to
// workDir) never scanned for the magic word - jonnyq's own transcript log
// belongs here, since it echoes fired triggers back into the working
// directory and would otherwise re-trigger itself in a runaway loop.
func New(workDir, magicWord string, interval time.Duration, excludeFiles ...string) *Watcher {
	ex := map[string]bool{}
	for _, f := range excludeFiles {
		if f == "" {
			continue
		}
		ex[filepath.Clean(f)] = true
	}
	return &Watcher{
		workDir:      workDir,
		interval:     interval,
		magicWord:    magicWord,
		state:        map[string]*fileState{},
		excludeFiles: ex,
	}
}

// SetMagicWord updates the word to scan for; safe to call while running.
func (w *Watcher) SetMagicWord(word string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.magicWord = word
}

func (w *Watcher) MagicWord() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.magicWord
}

// Start begins polling in a background goroutine, sending a Trigger for
// each newly seen magic-word line to triggers. A send that would block
// (the channel is full) is dropped rather than stalling the poll loop; the
// line stays marked seen so it won't spam retries once the consumer
// catches up - the user can re-save the file if a trigger was genuinely
// missed.
func (w *Watcher) Start(triggers chan<- Trigger) {
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	w.mu.Lock()
	w.started = true
	w.mu.Unlock()
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				w.poll(triggers)
			}
		}
	}()
}

// Stop signals the poll loop to exit and waits for it to do so. Safe to
// call even if Start was never called, and safe to call more than once.
func (w *Watcher) Stop() {
	w.mu.Lock()
	started := w.started
	w.mu.Unlock()
	if !started {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stop)
		<-w.done
	})
}

func (w *Watcher) poll(triggers chan<- Trigger) {
	word := w.MagicWord()
	if word == "" {
		return
	}
	seenThisPoll := map[string]bool{}

	_ = filepath.WalkDir(w.workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // ignore unreadable entries, keep walking
		}
		name := d.Name()
		if d.IsDir() {
			if path != w.workDir && (strings.HasPrefix(name, ".") || skipDirNames[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}

		rel, relErr := filepath.Rel(w.workDir, path)
		if relErr != nil {
			rel = path
		}
		if w.excludeFiles[filepath.Clean(rel)] {
			return nil
		}
		seenThisPoll[rel] = true

		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, tracked := w.state[rel]
		if tracked && !info.ModTime().After(st.modTime) {
			return nil // unchanged since last poll
		}
		if !tracked {
			st = &fileState{seen: map[string]bool{}}
			w.state[rel] = st
		}
		st.modTime = info.ModTime()

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, word) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if st.seen[trimmed] {
				continue
			}
			st.seen[trimmed] = true
			t := Trigger{File: rel, Line: i + 1, Text: trimmed}
			select {
			case triggers <- t:
			default:
			}
		}
		return nil
	})

	// Drop tracking for files that disappeared, so a long session doesn't
	// leak memory over one file at a time being renamed/deleted.
	for rel := range w.state {
		if !seenThisPoll[rel] {
			delete(w.state, rel)
		}
	}
}
