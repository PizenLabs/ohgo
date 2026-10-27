package gryhook

import (
	"testing"
	"time"
)

func TestFromEnvAndSettingsMergesWithEnvironmentPrecedence(t *testing.T) {
	t.Setenv("OHGO_GRY_HOOK", "")
	t.Setenv("OHGO_GRY_BIN", "")
	t.Setenv("OHGO_GRY_PROJECT", "")
	t.Setenv("OHGO_GRY_TIMEOUT_MS", "")
	t.Setenv("OHGO_GRY_MAX_TOKENS", "")
	t.Setenv("OHGO_GRY_DEBUG", "")

	file := FileConfig{
		Enabled:   true,
		Binary:    "/opt/gry",
		Project:   "repo",
		CacheDir:  "/tmp/cache",
		TimeoutMs: 900,
		MaxTokens: 2048,
	}
	cfg := FromEnvAndSettings("/work", file)
	if !cfg.Enabled || cfg.Binary != "/opt/gry" || cfg.Project != "repo" {
		t.Fatalf("file config not applied: %+v", cfg)
	}
	if cfg.Timeout != 900*time.Millisecond || cfg.MaxTokens != 2048 {
		t.Fatalf("budgets not applied: %+v", cfg)
	}

	t.Setenv("OHGO_GRY_PROJECT", "other")
	t.Setenv("OHGO_GRY_TIMEOUT_MS", "1200")
	t.Setenv("OHGO_GRY_MAX_TOKENS", "4096")
	cfg = FromEnvAndSettings("/work", file)
	if cfg.Project != "other" || cfg.Timeout != 1200*time.Millisecond || cfg.MaxTokens != 4096 {
		t.Fatalf("environment did not override: %+v", cfg)
	}

	// An explicit 0 disables an enabled file entry for one process.
	t.Setenv("OHGO_GRY_HOOK", "0")
	if cfg := FromEnvAndSettings("/work", file); cfg.Enabled {
		t.Fatal("OHGO_GRY_HOOK=0 must disable the adapter")
	}
}

func TestFromEnvAndSettingsFailsClosedWithoutIdentity(t *testing.T) {
	t.Setenv("OHGO_GRY_HOOK", "")
	t.Setenv("OHGO_GRY_BIN", "")
	t.Setenv("OHGO_GRY_PROJECT", "")
	if cfg := FromEnvAndSettings("/work", FileConfig{Enabled: true, Binary: "/opt/gry"}); cfg.Enabled {
		t.Fatal("a missing project must disable the adapter")
	}
	if cfg := FromEnvAndSettings("/work", FileConfig{Enabled: true, Project: "repo"}); cfg.Enabled {
		t.Fatal("a missing binary must disable the adapter")
	}
}

func TestInvalidBudgetsFallBackToDefaults(t *testing.T) {
	t.Setenv("OHGO_GRY_HOOK", "")
	t.Setenv("OHGO_GRY_BIN", "")
	t.Setenv("OHGO_GRY_PROJECT", "")
	t.Setenv("OHGO_GRY_TIMEOUT_MS", "")
	t.Setenv("OHGO_GRY_MAX_TOKENS", "")
	cfg := FromEnvAndSettings("/work", FileConfig{
		Enabled: true, Binary: "/opt/gry", Project: "repo", TimeoutMs: 1, MaxTokens: 1,
	})
	if cfg.Timeout != 1500*time.Millisecond || cfg.MaxTokens != 6000 {
		t.Fatalf("out-of-range file budgets must not be used: %+v", cfg)
	}
}

func TestValidProjectMatchesTheAdapterRule(t *testing.T) {
	for _, name := range []string{"repo", "repo.v2", "a_b-c"} {
		if !ValidProject(name) {
			t.Errorf("ValidProject(%q) = false", name)
		}
	}
	for _, name := range []string{"", "../repo", "repo name", "-repo"} {
		if ValidProject(name) {
			t.Errorf("ValidProject(%q) = true", name)
		}
	}
}
