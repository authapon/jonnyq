// Command jonnyq is an interactive coding agent CLI.
package main

import (
	"context"
	"fmt"
	"os"

	"jonnyq/internal/agent"
	"jonnyq/internal/config"
	"jonnyq/internal/llm"
	"jonnyq/internal/repl"
	"jonnyq/internal/tools"
	"jonnyq/internal/ui"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	w, err := ui.New(cfg.OutputFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open output file:", err)
		os.Exit(1)
	}
	defer w.Close()

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot determine working directory:", err)
		os.Exit(1)
	}

	provider, err := llm.New(cfg.ProviderName, cfg.ProviderURL, cfg.Key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provider error:", err)
		os.Exit(1)
	}

	reg := tools.NewRegistry()
	reg.Register(&tools.ReadFileTool{WorkDir: workDir})
	reg.Register(&tools.WriteFileTool{WorkDir: workDir})
	reg.Register(&tools.EditFileTool{WorkDir: workDir})
	reg.Register(&tools.CreateFolderTool{WorkDir: workDir})
	reg.Register(tools.NewSearxngTool(cfg.SearxngURL, cfg.SearxngMaxResults, cfg.SearxngConcurrency, cfg.SearxngTimeoutSec))
	reg.Register(tools.NewFetchTool(cfg.FetchConcurrency, cfg.FetchTimeoutSec))
	reg.Register(&tools.PDFTool{WorkDir: workDir, MaxPages: cfg.PDFMaxPages, DPI: cfg.PDFDPI})
	reg.Register(&tools.PicTool{WorkDir: workDir})
	reg.Register(&tools.RunCommandTool{WorkDir: workDir})
	reg.Register(&tools.SkillTool{Paths: cfg.SkillPaths})

	contextFile := ".context"
	ag := agent.New(provider, cfg.Model, reg, cfg.Thinking, cfg.ContextSize, w, cfg.SkillPaths, contextFile)

	r := repl.New(cfg, ag, w)
	if err := r.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
