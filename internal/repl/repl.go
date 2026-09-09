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

	"jonnyq/internal/agent"
	"jonnyq/internal/coding"
	"jonnyq/internal/config"
	"jonnyq/internal/llm"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
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
  /coding                           run automated coding from requirements.md, tracked in .progress (Ctrl-C cancels)
  /exit                             exit jonnyq

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

	mu     sync.Mutex
	cancel context.CancelFunc
}

func New(cfg *config.Config, ag *agent.Agent, w *ui.Writer, runCommand *tools.RunCommandTool) *REPL {
	return &REPL{Cfg: cfg, Agent: ag, UI: w, RunCommand: runCommand}
}

// Run reads prompts from stdin until /exit or EOF (Ctrl-D). Ctrl-C cancels
// whichever turn is currently in flight without exiting the REPL.
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

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		if r.Cfg.Model == "" {
			r.UI.Plainln("model is not set; use /model <name> (only slash commands are accepted until then)")
		}
		r.UI.Plain("> ")
		raw, ok := readInput(scanner)
		if !ok {
			r.UI.Plainln("")
			return nil
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		isSlash := strings.HasPrefix(line, "/")
		if !isSlash && r.Cfg.Model == "" {
			r.UI.Plainln("no model set; type /model <name> first")
			continue
		}

		// Both slash commands (e.g. /coding, /list_model) and plain prompts
		// run under a cancellable context so Ctrl-C can interrupt either.
		turnCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.cancel = cancel
		r.mu.Unlock()

		var err error
		exit := false
		if isSlash {
			exit, err = r.handleSlash(turnCtx, line)
		} else {
			err = r.Agent.RunTurn(turnCtx, line)
		}
		wasCancelled := turnCtx.Err() != nil

		r.mu.Lock()
		r.cancel = nil
		r.mu.Unlock()
		cancel()

		if exit {
			return nil
		}
		if err != nil {
			if wasCancelled {
				r.UI.Meta("[cancelled]")
			} else {
				r.UI.Plainln("error: " + err.Error())
			}
		}
	}
}

// handleSlash processes one slash command. It returns (exit, error); exit is
// true if the REPL should exit. Commands that already reported their own
// error print it directly and return a nil error so the caller doesn't
// double-report it.
func (r *REPL) handleSlash(ctx context.Context, line string) (bool, error) {
	fields := strings.Fields(line)
	cmd := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(line, cmd))

	switch cmd {
	case "/?", "/help":
		r.UI.Plainln(helpText)

	case "/exit":
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

	case "/coding":
		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; use /model <name> first")
			return false, nil
		}
		return false, coding.Run(ctx, r.Agent, r.UI, ".")

	default:
		r.UI.Plainln("unknown command " + cmd + "; try /help")
	}
	return false, nil
}

func (r *REPL) rebuildProvider() error {
	p, err := llm.New(r.Cfg.ProviderName, r.Cfg.ProviderURL, r.Cfg.Key)
	if err != nil {
		return err
	}
	r.Agent.Provider = p
	return nil
}
