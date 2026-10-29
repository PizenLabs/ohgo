package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/spf13/cobra"
)

// doctorCmd checks the local setup and reports what would stop a run.
//
// Every check names its own fix: a diagnostic that says "broken" without saying
// what to do about it is a second error message, not a diagnosis.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the local setup",
		Long: `Check the working directory, configuration, model catalog, credentials,
session store, and tool set, and report what would stop a run.

A missing API key for the default provider is the common failure: the fix is
printed with the check, e.g. "export ANTHROPIC_API_KEY=...".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
}

// doctorReport is one check result, kept so tests assert on structure rather
// than on formatted text.
type doctorReport struct {
	// Name is the check, e.g. "config".
	Name string
	// OK reports whether the check passed.
	OK bool
	// Detail is the human-readable outcome or fix.
	Detail string
}

func runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	reports := collectDoctorReports(cmd)

	failed := false
	for _, rep := range reports {
		status := "ok"
		if !rep.OK {
			status = "FAIL"
			failed = true
		}
		_, _ = fmt.Fprintf(out, "%-10s %-12s %s\n", status, rep.Name, rep.Detail)
	}
	if failed {
		return fmt.Errorf("ohgo doctor: %d of %d checks failed", countFailed(reports), len(reports))
	}
	return nil
}

func countFailed(reports []doctorReport) int {
	n := 0
	for _, rep := range reports {
		if !rep.OK {
			n++
		}
	}
	return n
}

// collectDoctorReports runs every check. It is split from runDoctor so tests
// assert on the reports without parsing formatted output.
func collectDoctorReports(cmd *cobra.Command) []doctorReport {
	return collectReports(flagValue(cmd, "model"), ai.NewRegistry())
}

// collectReports runs every check from explicit inputs so the harness — and
// therefore /doctor in both interfaces — uses the same implementation as the
// CLI. One diagnostic path, not two that can disagree.
func collectReports(modelFlag string, registry *ai.Registry) []doctorReport {
	var reports []doctorReport
	add := func(name string, ok bool, detail string) {
		reports = append(reports, doctorReport{Name: name, OK: ok, Detail: detail})
	}

	cwd, err := os.Getwd()
	if err != nil {
		add("workdir", false, fmt.Sprintf("cannot locate working directory: %v", err))
		cwd = "."
	} else {
		add("workdir", true, cwd)
	}

	settings, err := config.Load(cwd)
	if err != nil && err != config.ErrNotFound {
		add("config", false, fmt.Sprintf("malformed config, defaults in effect: %v", err))
		settings = config.Default()
	} else {
		add("config", true, "settings loaded")
	}

	providers := catalog.ProviderNames()
	models := catalog.List()
	if len(providers) == 0 || len(models) == 0 {
		add("catalog", false, "embedded catalog is empty; reinstall ohgo")
	} else {
		add("catalog", true, fmt.Sprintf("%d models across %d providers", len(models), len(providers)))
	}

	model, err := resolveModel(settings, modelFlag, os.Getenv("OHGO_MODEL"), catalog.Resolve)
	if err != nil {
		add("model", false, err.Error())
	} else {
		add("model", true, fmt.Sprintf("%s/%s (%s ctx)", model.Provider, model.ID, formatInt(model.ContextWindow)))
	}

	if err == nil {
		if _, keyErr := registry.ResolveAPIKey(model.Provider); keyErr != nil {
			add("credentials", false, fmt.Sprintf("%s/%s: %v", model.Provider, model.ID, keyErr))
		} else {
			add("credentials", true, fmt.Sprintf("credential present for %s", model.Provider))
		}
	} else {
		add("credentials", false, "skipped: no model resolved")
	}

	// The endpoint probe distinguishes configured from connected: a present
	// credential against a dead endpoint is a different problem with a
	// different fix. Bounded, so a dead network fails the check in seconds.
	if err == nil {
		key, _ := registry.ResolveAPIKey(model.Provider)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		probe := ai.ProbeProvider(ctx, nil, model.API, model.BaseURL, key)
		cancel()
		add("endpoint", probe.Reachable && probe.Authenticated, probe.Detail)
	} else {
		add("endpoint", false, "skipped: no model resolved")
	}

	path, err := session.DefaultPath()
	if err != nil {
		add("sessions", false, fmt.Sprintf("cannot locate session store: %v", err))
	} else if store, err := session.Open(path); err != nil {
		add("sessions", false, fmt.Sprintf("cannot open %s: %v", path, err))
	} else {
		list, listErr := store.List(1)
		_ = store.Close()
		if listErr != nil {
			add("sessions", false, fmt.Sprintf("store unreadable: %v", listErr))
		} else {
			add("sessions", true, fmt.Sprintf("%s (%d+ stored sessions visible)", path, len(list)))
		}
	}

	opts := options{root: cwd, maxDepth: 1, settings: settings}
	toolSet := buildTools(opts, registry)
	if len(toolSet) == 0 {
		add("tools", false, "no tools built; check --no-tools is not set")
	} else {
		names := make([]string, 0, len(toolSet))
		var externalErrors []string
		external := 0
		for _, tool := range toolSet {
			names = append(names, tool.Name())
			switch typed := tool.(type) {
			case *tools.SearchTool:
				external++
				if typed.ConfigErr != "" {
					externalErrors = append(externalErrors, "search: "+typed.ConfigErr)
				}
			case *tools.MCPListTool:
				external++
				if typed.ConfigErr != "" {
					externalErrors = append(externalErrors, "mcp: "+typed.ConfigErr)
				}
			case *tools.MCPReadTool:
				external++
			case *tools.MCPCallTool:
				external++
			}
		}
		add("tools", true, fmt.Sprintf("%d tools (%s)", len(toolSet), strings.Join(names, ", ")))
		if len(externalErrors) > 0 {
			add("external", false, strings.Join(externalErrors, "; ")+"; fix ~/.ohgo/settings.json")
		} else if external > 0 {
			add("external", true, fmt.Sprintf("%d external capability tools configured", external))
		}
	}

	return reports
}
