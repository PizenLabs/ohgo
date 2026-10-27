package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/gryhook"
	"github.com/spf13/cobra"
)

// gryCmd configures the opt-in GRY evidence adapter.
//
// The adapter is read-only. This command writes selection and budget fields to
// the machine-scoped settings file; it never changes approval, grounding,
// budget, or mutation policy. Enabling the adapter is therefore not a grant of
// authority.
func gryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gry",
		Short: "Configure the opt-in GRY evidence adapter",
		Long: `Configure the GRY lifecycle evidence adapter.

GRY supplies read-only repository evidence at before_plan, before_edit,
after_edit and before_finish. OHGO keeps every authorization, approval,
budget and mutation decision. The adapter is disabled until you enable it.

The OHGO_GRY_* environment variables override the stored values for one
process, and OHGO_GRY_HOOK=0 disables an enabled entry without editing it.`,
	}
	cmd.AddCommand(gryStatusCmd(), gryEnableCmd(), gryDisableCmd())
	return cmd
}

func gryStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the effective GRY adapter configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := loadGrySettings()
			if err != nil {
				return err
			}
			file := gryFileConfig(settings)
			effective := gryhook.FromEnvAndSettings("", file)
			binaryFound := false
			if path, err := exec.LookPath(effective.Binary); err == nil && path != "" {
				binaryFound = true
			}
			report := map[string]any{
				"enabled":            effective.Enabled,
				"binary":             effective.Binary,
				"binary_found":       binaryFound,
				"project":            effective.Project,
				"project_valid":      gryhook.ValidProject(effective.Project),
				"cache_dir":          effective.CacheDir,
				"timeout_ms":         effective.Timeout.Milliseconds(),
				"max_tokens":         effective.MaxTokens,
				"debug":              effective.Debug,
				"events":             gryhook.Stages,
				"tui_wired":          false,
				"settings_path":      settingsPath(),
				"authority_boundary": "GRY evidence never grants permission or executes a mutation",
			}
			if !effective.Enabled {
				report["reason"] = gryDisabledReason(effective)
			}
			raw, _ := json.MarshalIndent(report, "", "  ")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return nil
		},
	}
	return cmd
}

func gryEnableCmd() *cobra.Command {
	var (
		project   string
		binary    string
		cacheDir  string
		timeoutMs int
		maxTokens int
		debug     bool
	)
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable the GRY evidence adapter for headless runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			project = strings.TrimSpace(project)
			if project == "" {
				return errors.New("gry enable: --project is required (the GRY project name, not a directory)")
			}
			if !gryhook.ValidProject(project) {
				return fmt.Errorf("gry enable: invalid project %q", project)
			}
			if timeoutMs != 0 && (timeoutMs < 100 || timeoutMs > 30000) {
				return errors.New("gry enable: --timeout-ms must be between 100 and 30000")
			}
			if maxTokens != 0 && (maxTokens < 128 || maxTokens > 1000000) {
				return errors.New("gry enable: --max-tokens must be between 128 and 1000000")
			}
			resolved, err := resolveGryBinary(binary)
			if err != nil {
				return err
			}
			settings, err := loadGrySettings()
			if err != nil {
				return err
			}
			previous := settings.Gry
			settings.Gry = &config.GrySettings{
				Enabled:   true,
				Binary:    resolved,
				Project:   project,
				CacheDir:  strings.TrimSpace(cacheDir),
				TimeoutMs: timeoutMs,
				MaxTokens: maxTokens,
				Debug:     debug,
			}
			if previous != nil {
				if settings.Gry.CacheDir == "" {
					settings.Gry.CacheDir = previous.CacheDir
				}
				if settings.Gry.TimeoutMs == 0 {
					settings.Gry.TimeoutMs = previous.TimeoutMs
				}
				if settings.Gry.MaxTokens == 0 {
					settings.Gry.MaxTokens = previous.MaxTokens
				}
			}
			if err := updateSettingsRaw(func(root map[string]any) error {
				root["gry"] = settings.Gry
				return nil
			}); err != nil {
				return fmt.Errorf("gry enable: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "enabled gry hooks: project=%s binary=%s\n", project, resolved)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "run `ohgo gry status` to verify, or OHGO_GRY_HOOK=0 to disable for one process")
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GRY project name (required)")
	cmd.Flags().StringVar(&binary, "binary", "", "gry binary path or PATH name (default: resolve gry)")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "GRY cache directory used for indexing")
	cmd.Flags().IntVar(&timeoutMs, "timeout-ms", 0, "per-hook timeout in milliseconds (100..30000)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "evidence token budget (128..1000000)")
	cmd.Flags().BoolVar(&debug, "debug", false, "print one bounded status line per observation")
	return cmd
}

func gryDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove the stored GRY adapter configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := loadGrySettings()
			if err != nil {
				return err
			}
			if settings.Gry == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "gry hooks were not configured")
				return nil
			}
			settings.Gry = nil
			if err := updateSettingsRaw(func(root map[string]any) error {
				delete(root, "gry")
				return nil
			}); err != nil {
				return fmt.Errorf("gry disable: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "disabled gry hooks (other settings preserved)")
			return nil
		},
	}
}

// updateSettingsRaw edits the settings document as a JSON object so keys this
// build does not know about survive an integration write. The typed Settings
// struct is still used to validate and resolve the values, but it is never the
// serialization source for this command.
func updateSettingsRaw(mutate func(map[string]any) error) error {
	path := settingsPath()
	if path == "" {
		return errors.New("cannot resolve the settings path")
	}
	root := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("refusing to edit malformed %s: %w", path, err)
		}
	}
	if err := mutate(root); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func loadGrySettings() (config.Settings, error) {
	settings, err := config.Load("")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return settings, fmt.Errorf("gry: %w", err)
	}
	return settings, nil
}

func gryFileConfig(settings config.Settings) gryhook.FileConfig {
	if settings.Gry == nil {
		return gryhook.FileConfig{}
	}
	return gryhook.FileConfig{
		Enabled:   settings.Gry.Enabled,
		Binary:    settings.Gry.Binary,
		Project:   settings.Gry.Project,
		CacheDir:  settings.Gry.CacheDir,
		TimeoutMs: settings.Gry.TimeoutMs,
		MaxTokens: settings.Gry.MaxTokens,
		Debug:     settings.Gry.Debug,
	}
}

func resolveGryBinary(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		path, err := exec.LookPath("gry")
		if err != nil {
			return "", errors.New("gry enable: gry is not on PATH; install it or pass --binary")
		}
		return path, nil
	}
	if strings.ContainsRune(value, os.PathSeparator) {
		info, err := os.Stat(value)
		if err != nil {
			return "", fmt.Errorf("gry enable: binary: %w", err)
		}
		if info.IsDir() {
			return "", errors.New("gry enable: binary is a directory")
		}
		return value, nil
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("gry enable: find %q: %w", value, err)
	}
	return path, nil
}

func gryDisabledReason(cfg gryhook.Config) string {
	if cfg.Binary == "" {
		return "binary is not configured"
	}
	if cfg.Project == "" {
		return "project is not configured; ohgo never guesses indexing identity"
	}
	if os.Getenv("OHGO_GRY_HOOK") != "" {
		return "OHGO_GRY_HOOK disables the adapter for this process"
	}
	return "adapter is disabled"
}
