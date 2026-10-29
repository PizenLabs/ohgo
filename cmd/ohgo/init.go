package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/spf13/cobra"
)

// initCmd sets up ohgo for first use.
//
// It writes ~/.ohgo/settings.json with a working model and theme, then tells
// the user exactly what is left (usually one environment variable). It is the
// harness's front door: a fresh install should reach a working session
// through init, doctor, and run, in that order.
func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up ohgo for first use",
		Long: `Write ~/.ohgo/settings.json with a working model and theme.

The model defaults to the catalog default when its credential is present,
else to the first model of the first provider that has one. Pass --model to
choose explicitly. An existing settings file is never overwritten without
--force: silently replacing someone's configuration would be the worse trade
against a failed first run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelFlag, _ := cmd.Flags().GetString("model")
			themeFlag, _ := cmd.Flags().GetString("theme")
			force, _ := cmd.Flags().GetBool("force")
			return runInit(cmd, initOptions{model: modelFlag, theme: themeFlag, force: force})
		},
	}
	cmd.Flags().String("model", "", "model as provider/id, e.g. groq/llama-3.1-8b-instant")
	cmd.Flags().String("theme", "", "palette: dark or light")
	cmd.Flags().Bool("force", false, "overwrite an existing settings file")
	return cmd
}

// initOptions are the init flags.
type initOptions struct {
	model string
	theme string
	force bool
}

// initPlan is the setup init will apply. It is computed separately from
// applying so tests assert on decisions without touching the filesystem.
type initPlan struct {
	// model is the resolved default model reference.
	model string
	// theme is the normalized palette.
	theme string
	// providers lists providers with credentials present, sorted.
	providers []string
	// keyHint names what is missing when the chosen model has no credential.
	keyHint string
}

// planInit decides the setup without writing anything.
func planInit(modelFlag, themeFlag string) (initPlan, error) {
	plan := initPlan{theme: "dark"}
	if strings.TrimSpace(themeFlag) != "" {
		plan.theme = normalizeThemeFlag(themeFlag)
	}

	if strings.TrimSpace(modelFlag) != "" {
		if _, err := catalog.Resolve(modelFlag); err != nil {
			return plan, fmt.Errorf("init: %w\nrun `ohgo models` to list them", err)
		}
		plan.model = strings.TrimSpace(modelFlag)
	} else {
		plan.model = defaultModelWithCredential()
	}
	plan.providers = providersWithCredentials()

	registry := ai.NewRegistry()
	provider, _, _ := strings.Cut(plan.model, "/")
	if _, err := registry.ResolveAPIKey(provider); err != nil {
		plan.keyHint = err.Error()
	}
	return plan, nil
}

// normalizeThemeFlag maps a flag to a palette name. Anything but light is
// dark: a typo degrades to the default rather than failing the setup.
func normalizeThemeFlag(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "light") {
		return "light"
	}
	return "dark"
}

// providersWithCredentials lists catalog providers whose credential resolves,
// sorted. The registry consults its map then the environment, so this sees
// both configured gateways and plain exports.
func providersWithCredentials() []string {
	registry := ai.NewRegistry()
	var out []string
	for _, provider := range catalog.ProviderNames() {
		if _, err := registry.ResolveAPIKey(provider); err == nil {
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out
}

// defaultModelWithCredential prefers the catalog default when its key is
// present, else the first model of the first credentialed provider. Both
// orderings are sorted, so the choice is deterministic rather than map
// order — a setup that picks a different model on each run would be
// untrustworthy.
func defaultModelWithCredential() string {
	def := catalog.DefaultRef
	registry := ai.NewRegistry()
	if provider, _, ok := strings.Cut(def, "/"); ok {
		if _, err := registry.ResolveAPIKey(provider); err == nil {
			return def
		}
	}
	for _, provider := range catalog.ProviderNames() {
		if _, err := registry.ResolveAPIKey(provider); err != nil {
			continue
		}
		if models, err := catalog.ListForProvider(provider); err == nil && len(models) > 0 {
			return provider + "/" + models[0].ID
		}
	}
	return def
}

// runInit plans the setup, writes it, and reports what is left.
func runInit(cmd *cobra.Command, opts initOptions) error {
	out := cmd.OutOrStdout()

	if _, err := os.Stat(settingsPath()); err == nil && !opts.force {
		return fmt.Errorf("init: settings already exist (use --force to overwrite)")
	}

	plan, err := planInit(opts.model, opts.theme)
	if err != nil {
		return err
	}

	settings := config.Default()
	settings.Model = plan.model
	settings.Theme = plan.theme
	if err := config.Save(settings); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	_, _ = fmt.Fprintf(out, "wrote %s\n", settingsPath())
	_, _ = fmt.Fprintf(out, "model: %s\ntheme: %s\n", plan.model, plan.theme)
	if len(plan.providers) == 0 {
		_, _ = fmt.Fprintln(out, "no provider credentials found: export one, e.g. export ANTHROPIC_API_KEY=...")
	} else {
		_, _ = fmt.Fprintf(out, "credentials: %s\n", strings.Join(plan.providers, ", "))
	}
	if plan.keyHint != "" {
		_, _ = fmt.Fprintf(out, "missing credential for %s: %s\n", plan.model, plan.keyHint)
	} else {
		_, _ = fmt.Fprintln(out, "run `ohgo doctor` to verify, then `ohgo` to start.")
	}
	return nil
}

// settingsPath locates the settings file init writes.
func settingsPath() string {
	path, err := config.Path("")
	if err != nil {
		return ""
	}
	return path
}
