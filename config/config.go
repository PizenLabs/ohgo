// Package config loads and saves ohgo settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound means no configuration file exists at the expected location.
var ErrNotFound = errors.New("config: not found")

// Settings is the user-facing configuration.
//
// Every field is optional: a zero Settings is a valid configuration that uses
// the built-in defaults.
type Settings struct {
	// Model is the default model as "provider/id", e.g. "anthropic/claude-sonnet-4-5".
	Model string `json:"model,omitempty"`
	// MaxTokens caps each completion.
	MaxTokens int `json:"maxTokens,omitempty"`
	// Reasoning enables extended thinking by default.
	Reasoning bool `json:"reasoning,omitempty"`
	// ReasoningEffort is the default effort level.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// Approvals maps a tool name to its approval policy.
	Approvals map[string]string `json:"approvals,omitempty"`
	// AutoApprove lists the tools that never prompt.
	AutoApprove []string `json:"autoApprove,omitempty"`
	// SystemPrompt is appended to the built-in system prompt.
	SystemPrompt string `json:"systemPrompt,omitempty"`
	// Theme selects the UI palette: "dark" (default) or "light". Any other
	// value behaves as dark, so a typo degrades to the default rather than
	// an error.
	Theme string `json:"theme,omitempty"`
	// Compaction configures context reduction. A nil pointer means "use the
	// built-in defaults", which is distinct from "disabled".
	Compaction *Compaction `json:"compaction,omitempty"`
}

// Compaction settings as configured by the user.
//
// A nil field means "use the built-in default", which is how a sparse JSON
// document can override one knob without restating the others.
type Compaction struct {
	// Enabled turns compaction on. The built-in default is on.
	Enabled *bool `json:"enabled,omitempty"`
	// Strategy selects how context is reduced.
	Strategy string `json:"strategy,omitempty"`
	// KeepRecent is the number of trailing messages preserved verbatim.
	KeepRecent *int `json:"keepRecent,omitempty"`
	// ThresholdPercent triggers compaction at this fraction of the window.
	ThresholdPercent *float64 `json:"thresholdPercent,omitempty"`
}

// Default returns the built-in settings.
func Default() Settings {
	return Settings{
		MaxTokens:       8192,
		Reasoning:       false,
		ReasoningEffort: "medium",
		Approvals:       map[string]string{},
		Compaction:      defaultCompaction(),
	}
}

// defaultCompaction returns the built-in compaction settings.
func defaultCompaction() *Compaction {
	enabled := true
	keepRecent := 8
	threshold := 80.0
	return &Compaction{
		Enabled:          &enabled,
		KeepRecent:       &keepRecent,
		ThresholdPercent: &threshold,
	}
}

// Load reads the configuration file for root.
//
// A missing file is not an error: Default is returned with ErrNotFound so
// callers can distinguish "no config" from "malformed config".
func Load(root string) (Settings, error) {
	path, err := Path(root)
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), ErrNotFound
		}
		return Default(), fmt.Errorf("config: read %s: %w", path, err)
	}

	settings := Default()
	if err := json.Unmarshal(data, &settings); err != nil {
		return settings, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if settings.Approvals == nil {
		settings.Approvals = map[string]string{}
	}
	return settings, nil
}

// Path returns the configuration file path for root.
func Path(root string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".ohgo", "settings.json"), nil
}

// Dir returns the directory that holds ohgo state.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".ohgo"), nil
}

// Save writes settings to the configuration file for root.
func Save(settings Settings) error {
	path, err := Path("")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// ApprovalFor reports the approval policy for a tool.
//
// The explicit AutoApprove list wins over Approvals, and AutoApprove takes
// precedence over a deny so a user can force a tool on without editing the
// per-tool policy map.
func (s Settings) ApprovalFor(tool string) (string, bool) {
	for _, name := range s.AutoApprove {
		if strings.EqualFold(strings.TrimSpace(name), tool) {
			return "allow", true
		}
	}
	policy, ok := s.Approvals[tool]
	return policy, ok
}

// ModelID splits the configured model into provider and id.
//
// It returns ok=false when the value is not in "provider/id" form, so the
// caller can fall back to the default model rather than guessing a provider.
func (s Settings) ModelID() (provider, id string, ok bool) {
	value := strings.TrimSpace(s.Model)
	if value == "" {
		return "", "", false
	}
	provider, id, ok = strings.Cut(value, "/")
	if !ok || provider == "" || id == "" {
		return "", "", false
	}
	return provider, id, true
}
