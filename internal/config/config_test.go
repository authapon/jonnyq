package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderName != "ollama" || cfg.ProviderURL != "http://localhost:11434" {
		t.Errorf("unexpected default provider: %+v", cfg)
	}
	if cfg.ContextSize != DefaultContextSize {
		t.Errorf("expected default context size %d, got %d", DefaultContextSize, cfg.ContextSize)
	}
	if cfg.Thinking != true {
		t.Errorf("expected thinking default true")
	}
	if cfg.RunCommandTimeoutSec != DefaultRunCommandTimeoutSec {
		t.Errorf("expected default run_command timeout %d, got %d", DefaultRunCommandTimeoutSec, cfg.RunCommandTimeoutSec)
	}
	if cfg.MaxToolCallsPerTurn != DefaultMaxToolCallsPerTurn {
		t.Errorf("expected default max tool calls per turn %d, got %d", DefaultMaxToolCallsPerTurn, cfg.MaxToolCallsPerTurn)
	}
}

func TestMaxToolCallsPerTurnFlagAndEnv(t *testing.T) {
	cfg, err := Load([]string{"-max-tool-calls-per-turn", "10"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxToolCallsPerTurn != 10 {
		t.Errorf("expected flag to set 10, got %d", cfg.MaxToolCallsPerTurn)
	}

	t.Setenv("JONNYQ_MAX_TOOL_CALLS_PER_TURN", "25")
	cfg, err = Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxToolCallsPerTurn != 25 {
		t.Errorf("expected env to set 25, got %d", cfg.MaxToolCallsPerTurn)
	}
}

func TestLoadEnvOverridesDefault(t *testing.T) {
	t.Setenv("JONNYQ_MODEL", "llama3")
	t.Setenv("JONNYQ_CONTEXT_SIZE", "8192")
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "llama3" {
		t.Errorf("expected env model llama3, got %q", cfg.Model)
	}
	if cfg.ContextSize != 8192 {
		t.Errorf("expected env context size 8192, got %d", cfg.ContextSize)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	t.Setenv("JONNYQ_MODEL", "llama3")
	cfg, err := Load([]string{"-model", "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "qwen" {
		t.Errorf("expected flag to override env, got %q", cfg.Model)
	}
}

func TestSkillPathSplitting(t *testing.T) {
	cfg, err := Load([]string{"-skill-path", "/a/skills;/b/skills"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SkillPaths) != 2 || cfg.SkillPaths[0] != "/a/skills" || cfg.SkillPaths[1] != "/b/skills" {
		t.Errorf("unexpected skill paths: %+v", cfg.SkillPaths)
	}
}

func TestInvalidProviderRejected(t *testing.T) {
	if _, err := Load([]string{"-provider", "bogus"}); err == nil {
		t.Error("expected error for malformed provider")
	}
	if _, err := Load([]string{"-provider", "azure:http://x"}); err == nil {
		t.Error("expected error for unsupported provider name")
	}
}
