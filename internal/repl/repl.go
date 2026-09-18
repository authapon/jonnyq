// Package repl implements the interactive prompt loop and slash commands.
package repl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"jonnyq/internal/agent"
	"jonnyq/internal/coding"
	"jonnyq/internal/config"
	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
	"jonnyq/internal/watch"
)

const helpText = `Slash commands:
  /?  /help                         show this help
  /provider <name>:<url>            set the AI provider, e.g. ollama:http://localhost:11434
  /key <key>                        set the provider API key
  /model <model>                    set the model name
  /list_model                       list models available from the current provider
  /context <size>                   set the context size
  /think <true|false>               enable or disable model thinking
  /run_command_timeout <seconds>    set the run_command timeout in seconds
  /prompt <file>                    read a prompt from file and send it, as if typed
  /plan                             generate/reconcile .progress from requirements.md, checked against the codebase (no coding)
  /coding                           work on the next unfinished task in .progress, then stop (Ctrl-C cancels)
  /autocoding                       plan if needed, then work through every task in .progress in one run (Ctrl-C cancels)
  /watchfile [<word>|off]           watch the working directory for a magic word (default "AI!") and act on it when found
  /exit  /bye                       exit jonnyq

Multi-line prompts: end a line with a trailing backslash to continue it on
the next line, or type """ alone on a line to start a block that runs
until a matching closing """ line.
`

// REPL owns the prompt loop, wiring slash commands to the live config and
// agent.
type REPL struct {
	Cfg        *config.Config
	Agent      *agent.Agent
	UI         *ui.Writer
	RunCommand *tools.RunCommandTool

	mu      sync.Mutex
	cancel  context.CancelFunc
	watcher *watch.Watcher
}

func New(cfg *config.Config, ag *agent.Agent, w *ui.Writer, runCommand *tools.RunCommandTool) *REPL {
	return &REPL{Cfg: cfg, Agent: ag, UI: w, RunCommand: runCommand}
}

// Run reads prompts from stdin until /exit or EOF (Ctrl-D). Ctrl-C cancels
// whichever turn is currently in flight without exiting the REPL. While
// /watchfile is active, a file change carrying the magic word is handled as
// an extra turn interleaved with typed prompts - both flow through the same
// single-threaded loop below, so there's no concurrent access to Agent/UI.
func (r *REPL) Run(ctx context.Context) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			r.mu.Lock()
			if r.cancel != nil {
				r.cancel()
			}
			r.mu.Unlock()
		}
	}()

	defer func() {
		if r.watcher != nil {
			r.watcher.Stop()
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	stdinLines := make(chan string)
	go func() {
		defer close(stdinLines)
		for {
			raw, ok := readInput(scanner)
			if !ok {
				return
			}
			stdinLines <- raw
		}
	}()

	watchTriggers := make(chan watch.Trigger, 32)

	for {
		if r.Cfg.Model == "" {
			r.UI.Plainln("model is not set; use /model <name> (only slash commands are accepted until then)")
		}
		r.UI.Plain(r.promptString())

		var line string
		select {
		case raw, ok := <-stdinLines:
			if !ok {
				r.UI.Plainln("")
				return nil
			}
			line = strings.TrimSpace(raw)

		case trig := <-watchTriggers:
			r.UI.Plainln("")
			r.UI.Plainln(fmt.Sprintf("[watchfile] %s:%d: %s", trig.File, trig.Line, trig.Text))
			if r.Cfg.Model == "" {
				r.UI.Plainln("no model set; skipping watchfile trigger")
				continue
			}
			prompt := fmt.Sprintf(
				"A magic-word trigger (%q) was found in %s at line %d:\n\n    %s\n\n"+
					"Read the surrounding code in %s for context, then carry out the instruction on that line. "+
					"Once you're done, edit %s to remove or update that line (e.g. delete the trigger marker) so it doesn't fire again.",
				r.watcherMagicWord(), trig.File, trig.Line, trig.Text, trig.File, trig.File,
			)
			r.runCancellable(ctx, func(c context.Context) (bool, error) { return false, r.Agent.RunTurn(c, prompt) })
			continue
		}

		if line == "" {
			continue
		}

		isSlash := strings.HasPrefix(line, "/")
		if !isSlash && r.Cfg.Model == "" {
			r.UI.Plainln("no model set; type /model <name> first")
			continue
		}

		if isSlash {
			exit := r.runCancellable(ctx, func(c context.Context) (bool, error) { return r.handleSlash(c, line, watchTriggers) })
			if exit {
				return nil
			}
			continue
		}

		r.runCancellable(ctx, func(c context.Context) (bool, error) { return false, r.Agent.RunTurn(c, line) })
	}
}

// runCancellable runs fn under a fresh cancellable child of ctx (wired to
// Ctrl-C via r.cancel), reports "[cancelled]" or the returned error
// afterward, and returns fn's exit flag (only meaningful for slash
// commands; plain turns and watch triggers always pass false).
func (r *REPL) runCancellable(ctx context.Context, fn func(context.Context) (exit bool, err error)) bool {
	turnCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	exit, err := fn(turnCtx)
	wasCancelled := turnCtx.Err() != nil

	r.mu.Lock()
	r.cancel = nil
	r.mu.Unlock()
	cancel()

	if err != nil {
		if wasCancelled {
			r.UI.Meta("[cancelled]")
		} else {
			r.UI.Plainln("error: " + err.Error())
		}
	}
	return exit
}

func (r *REPL) watcherMagicWord() string {
	if r.watcher != nil {
		return r.watcher.MagicWord()
	}
	return r.Cfg.WatchMagicWord
}

// handleSlash processes one slash command. It returns (exit, error); exit is
// true if the REPL should exit. Commands that already reported their own
// error print it directly and return a nil error so the caller doesn't
// double-report it.
func (r *REPL) handleSlash(ctx context.Context, line string, watchTriggers chan<- watch.Trigger) (bool, error) {
	fields := strings.Fields(line)
	cmd := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(line, cmd))

	switch cmd {
	case "/?", "/help":
		r.UI.Plainln(helpText)

	case "/exit", "/bye":
		return true, nil

	case "/provider":
		if rest == "" {
			r.UI.Plainln("usage: /provider <name>:<url>")
			return false, nil
		}
		if err := r.Cfg.SetProvider(rest); err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false, nil
		}
		if err := r.rebuildProvider(); err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false, nil
		}
		r.UI.Plainln(fmt.Sprintf("provider set to %s:%s", r.Cfg.ProviderName, r.Cfg.ProviderURL))

	case "/key":
		r.Cfg.Key = rest
		if err := r.rebuildProvider(); err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false, nil
		}
		r.UI.Plainln("key set")

	case "/model":
		if rest == "" {
			r.UI.Plainln("usage: /model <model>")
			return false, nil
		}
		r.Cfg.Model = rest
		r.Agent.Model = rest
		r.UI.Plainln("model set to " + rest)

	case "/list_model":
		names, err := r.Agent.Provider.ListModels(ctx)
		if err != nil {
			return false, err
		}
		if len(names) == 0 {
			r.UI.Plainln("no models found")
			return false, nil
		}
		r.UI.Plainln(strings.Join(names, "\n"))

	case "/context":
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			r.UI.Plainln("usage: /context <positive integer>")
			return false, nil
		}
		r.Cfg.ContextSize = n
		r.Agent.ContextSize = n
		r.UI.Plainln(fmt.Sprintf("context size set to %d", n))

	case "/think":
		b, err := strconv.ParseBool(rest)
		if err != nil {
			r.UI.Plainln("usage: /think <true|false>")
			return false, nil
		}
		r.Cfg.Thinking = b
		r.Agent.Thinking = b
		r.UI.Plainln(fmt.Sprintf("thinking set to %v", b))

	case "/run_command_timeout":
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			r.UI.Plainln("usage: /run_command_timeout <positive integer seconds>")
			return false, nil
		}
		r.Cfg.RunCommandTimeoutSec = n
		if r.RunCommand != nil {
			r.RunCommand.TimeoutSec = n
		}
		r.UI.Plainln(fmt.Sprintf("run_command timeout set to %ds", n))

	case "/prompt":
		if rest == "" {
			r.UI.Plainln("usage: /prompt <file>")
			return false, nil
		}
		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; use /model <name> first")
			return false, nil
		}
		data, err := os.ReadFile(rest)
		if err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false, nil
		}
		return false, r.Agent.RunTurn(ctx, string(data))

	case "/plan":
		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; use /model <name> first")
			return false, nil
		}
		return false, coding.Plan(ctx, r.Agent, r.UI, ".")

	case "/coding":
		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; use /model <name> first")
			return false, nil
		}
		return false, coding.RunOneTask(ctx, r.Agent, r.UI, ".")

	case "/autocoding":
		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; use /model <name> first")
			return false, nil
		}
		return false, coding.AutoRun(ctx, r.Agent, r.UI, ".")

	case "/watchfile":
		r.toggleWatch(rest, watchTriggers)

	default:
		r.UI.Plainln("unknown command " + cmd + "; try /help")
	}
	return false, nil
}

// toggleWatch implements /watchfile: bare (not watching) starts watching
// with the current magic word, bare (already watching) or "off"/"stop"
// stops, and a word argument sets the magic word (starting watching if
// needed, or updating it live if already watching).
func (r *REPL) toggleWatch(arg string, watchTriggers chan<- watch.Trigger) {
	arg = strings.TrimSpace(arg)
	lower := strings.ToLower(arg)

	if lower == "off" || lower == "stop" || (arg == "" && r.watcher != nil) {
		if r.watcher == nil {
			r.UI.Plainln("[watchfile] not watching")
			return
		}
		r.watcher.Stop()
		r.watcher = nil
		r.UI.Plainln("[watchfile] stopped")
		return
	}

	if arg != "" {
		r.Cfg.WatchMagicWord = arg
	}
	if r.Cfg.WatchMagicWord == "" {
		r.UI.Plainln("usage: /watchfile [<magic word>|off]")
		return
	}

	if r.watcher != nil {
		r.watcher.SetMagicWord(r.Cfg.WatchMagicWord)
		r.UI.Plainln(fmt.Sprintf("[watchfile] magic word updated to %q (still watching)", r.Cfg.WatchMagicWord))
		return
	}

	interval := time.Duration(r.Cfg.WatchPollIntervalSec) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	r.watcher = watch.New(".", r.Cfg.WatchMagicWord, interval, r.Cfg.OutputFile)
	r.watcher.Start(watchTriggers)
	r.UI.Plainln(fmt.Sprintf("[watchfile] watching the working directory for %q", r.Cfg.WatchMagicWord))
}

func (r *REPL) rebuildProvider() error {
	p, err := llm.New(r.Cfg.ProviderName, r.Cfg.ProviderURL, r.Cfg.Key)
	if err != nil {
		return err
	}
	r.Agent.Provider = p
	return nil
}

// promptString is the "> " prompt shown before each read, e.g.
// "ornith-1.5-35b-a3b (100000 token) > ". Before a model is set it falls
// back to a bare "> " since there's nothing to show yet (the "model is not
// set" notice above it already covers that case).
func (r *REPL) promptString() string {
	if r.Cfg.Model == "" {
		return "> "
	}
	return fmt.Sprintf("%s (%d token) > ", r.Cfg.Model, r.Cfg.ContextSize)
}
