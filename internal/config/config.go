// Package config loads jonnyq settings from CLI flags and JONNYQ_* environment
// variables, with flags taking precedence over environment over defaults.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const envPrefix = "JONNYQ_"

const (
	DefaultProvider             = "ollama:http://localhost:11434"
	DefaultContextSize          = 16348
	DefaultOutputFile           = "output.txt"
	DefaultSearxngMaxResults    = 10
	DefaultSearxngConcurrency   = 4
	DefaultSearxngTimeoutSec    = 30
	DefaultFetchConcurrency     = 5
	DefaultFetchTimeoutSec      = 30
	DefaultPDFMaxPages          = 50
	DefaultPDFDPI               = 150
	DefaultThinking             = true
	DefaultRunCommandTimeoutSec = 300
	DefaultMaxToolCallsPerTurn  = 50
)

// Config holds all runtime settings for jonnyq.
type Config struct {
	ProviderName string // "ollama" or "openai"
	ProviderURL  string
	Key          string
	Model        string
	ContextSize  int
	OutputFile   string

	SearxngURL         string
	SearxngMaxResults  int
	SearxngConcurrency int
	SearxngTimeoutSec  int

	FetchConcurrency int
	FetchTimeoutSec  int

	PDFMaxPages int
	PDFDPI      int

	RunCommandTimeoutSec int
	MaxToolCallsPerTurn  int

	SkillPaths []string

	Thinking bool
}

func envString(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(name string, def bool) bool {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// SetProvider parses a "name:url" string (e.g. "ollama:http://localhost:11434")
// into ProviderName/ProviderURL.
func (c *Config) SetProvider(s string) error {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid provider %q, expected format name:url", s)
	}
	name := strings.ToLower(parts[0])
	if name != "ollama" && name != "openai" {
		return fmt.Errorf("unsupported provider %q, expected ollama or openai", name)
	}
	c.ProviderName = name
	c.ProviderURL = parts[1]
	return nil
}

// Load builds a Config from environment variables and then CLI args, with
// CLI args taking precedence.
func Load(args []string) (*Config, error) {
	c := &Config{}
	providerStr := envString("PROVIDER", DefaultProvider)
	skillPathStr := envString("SKILL_PATH", "")

	fs := flag.NewFlagSet("jonnyq", flag.ContinueOnError)
	provider := fs.String("provider", providerStr, "AI provider as name:url, e.g. ollama:http://localhost:11434")
	key := fs.String("key", envString("KEY", ""), "provider API key")
	model := fs.String("model", envString("MODEL", ""), "model name to use")
	contextSize := fs.Int("context", envInt("CONTEXT_SIZE", DefaultContextSize), "context size")
	outputFile := fs.String("output", envString("OUTPUT_FILE", DefaultOutputFile), "transcript log output file")
	searxngURL := fs.String("searxng-url", envString("SEARXNG_URL", ""), "searxng base URL for web_search")
	searxngMaxResults := fs.Int("searxng-max-results", envInt("SEARXNG_MAX_RESULTS", DefaultSearxngMaxResults), "max web_search results")
	searxngConcurrency := fs.Int("searxng-concurrency", envInt("SEARXNG_CONCURRENCY", DefaultSearxngConcurrency), "web_search concurrency")
	searxngTimeoutSec := fs.Int("searxng-timeout-sec", envInt("SEARXNG_TIMEOUT_SEC", DefaultSearxngTimeoutSec), "web_search timeout seconds")
	fetchConcurrency := fs.Int("fetch-concurrency", envInt("FETCH_CONCURRENCY", DefaultFetchConcurrency), "web_fetch concurrency")
	fetchTimeoutSec := fs.Int("fetch-timeout-sec", envInt("FETCH_TIMEOUT_SEC", DefaultFetchTimeoutSec), "web_fetch timeout seconds")
	pdfMaxPages := fs.Int("pdf-max-pages", envInt("PDF_MAX_PAGES", DefaultPDFMaxPages), "max PDF pages to read")
	pdfDPI := fs.Int("pdf-dpi", envInt("PDF_DPI", DefaultPDFDPI), "PDF render DPI")
	runCommandTimeoutSec := fs.Int("run-command-timeout-sec", envInt("RUN_COMMAND_TIMEOUT_SEC", DefaultRunCommandTimeoutSec), "run_command timeout seconds")
	maxToolCallsPerTurn := fs.Int("max-tool-calls-per-turn", envInt("MAX_TOOL_CALLS_PER_TURN", DefaultMaxToolCallsPerTurn), "max tool calls allowed within a single turn")
	skillPath := fs.String("skill-path", skillPathStr, "semicolon-separated skill directories")
	thinking := fs.Bool("thinking", envBool("THINKING", DefaultThinking), "enable model thinking")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if err := c.SetProvider(*provider); err != nil {
		return nil, err
	}
	c.Key = *key
	c.Model = *model
	c.ContextSize = *contextSize
	c.OutputFile = *outputFile
	c.SearxngURL = *searxngURL
	c.SearxngMaxResults = *searxngMaxResults
	c.SearxngConcurrency = *searxngConcurrency
	c.SearxngTimeoutSec = *searxngTimeoutSec
	c.FetchConcurrency = *fetchConcurrency
	c.FetchTimeoutSec = *fetchTimeoutSec
	c.PDFMaxPages = *pdfMaxPages
	c.PDFDPI = *pdfDPI
	c.RunCommandTimeoutSec = *runCommandTimeoutSec
	c.MaxToolCallsPerTurn = *maxToolCallsPerTurn
	c.Thinking = *thinking
	if *skillPath != "" {
		for _, p := range strings.Split(*skillPath, ";") {
			p = strings.TrimSpace(p)
			if p != "" {
				c.SkillPaths = append(c.SkillPaths, p)
			}
		}
	}

	return c, nil
}
