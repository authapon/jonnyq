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
  /coding                           run automated coding from requirements.md, tracked in .progress
  /exit                             exit jonnyq
`

// REPL owns the prompt loop, wiring slash commands to the live config and
// agent.
type REPL struct {
	Cfg   *config.Config
	Agent *agent.Agent
	UI    *ui.Writer

	mu     sync.Mutex
	cancel context.CancelFunc
}

func New(cfg *config.Config, ag *agent.Agent, w *ui.Writer) *REPL {
	return &REPL{Cfg: cfg, Agent: ag, UI: w}
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
		if !scanner.Scan() {
			r.UI.Plainln("")
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			if exit := r.handleSlash(ctx, line); exit {
				return nil
			}
			continue
		}

		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; type /model <name> first")
			continue
		}

		turnCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.cancel = cancel
		r.mu.Unlock()

		err := r.Agent.RunTurn(turnCtx, line)
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
	}
}

// handleSlash processes one slash command. It returns true if the REPL
// should exit.
func (r *REPL) handleSlash(ctx context.Context, line string) bool {
	fields := strings.Fields(line)
	cmd := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(line, cmd))

	switch cmd {
	case "/?", "/help":
		r.UI.Plainln(helpText)

	case "/exit":
		return true

	case "/provider":
		if rest == "" {
			r.UI.Plainln("usage: /provider <name>:<url>")
			return false
		}
		if err := r.Cfg.SetProvider(rest); err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false
		}
		if err := r.rebuildProvider(); err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false
		}
		r.UI.Plainln(fmt.Sprintf("provider set to %s:%s", r.Cfg.ProviderName, r.Cfg.ProviderURL))

	case "/key":
		r.Cfg.Key = rest
		if err := r.rebuildProvider(); err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false
		}
		r.UI.Plainln("key set")

	case "/model":
		if rest == "" {
			r.UI.Plainln("usage: /model <model>")
			return false
		}
		r.Cfg.Model = rest
		r.Agent.Model = rest
		r.UI.Plainln("model set to " + rest)

	case "/list_model":
		names, err := r.Agent.Provider.ListModels(ctx)
		if err != nil {
			r.UI.Plainln("error: " + err.Error())
			return false
		}
		if len(names) == 0 {
			r.UI.Plainln("no models found")
			return false
		}
		r.UI.Plainln(strings.Join(names, "\n"))

	case "/context":
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			r.UI.Plainln("usage: /context <positive integer>")
			return false
		}
		r.Cfg.ContextSize = n
		r.Agent.ContextSize = n
		r.UI.Plainln(fmt.Sprintf("context size set to %d", n))

	case "/think":
		b, err := strconv.ParseBool(rest)
		if err != nil {
			r.UI.Plainln("usage: /think <true|false>")
			return false
		}
		r.Cfg.Thinking = b
		r.Agent.Thinking = b
		r.UI.Plainln(fmt.Sprintf("thinking set to %v", b))

	case "/coding":
		if r.Cfg.Model == "" {
			r.UI.Plainln("no model set; use /model <name> first")
			return false
		}
		if err := coding.Run(ctx, r.Agent, r.UI, "."); err != nil {
			r.UI.Plainln("error: " + err.Error())
		}

	default:
		r.UI.Plainln("unknown command " + cmd + "; try /help")
	}
	return false
}

func (r *REPL) rebuildProvider() error {
	p, err := llm.New(r.Cfg.ProviderName, r.Cfg.ProviderURL, r.Cfg.Key)
	if err != nil {
		return err
	}
	r.Agent.Provider = p
	return nil
}
