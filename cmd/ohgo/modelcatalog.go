package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/tui"
	tea "github.com/charmbracelet/bubbletea"
)

// catalogSnapshot builds the instant /model picker roster from the merged
// catalog snapshot: bundled rows plus cached live rosters, no network.
//
// Detail shows context size and verified pricing only when known; local
// (ollama) rows are marked; confirmed capability flags (reasoning, images)
// appear when set. Unpriced rows carry no price claim: the picker matches a
// "free" query against their names without ever rendering them as verified
// free. Ids stay native "provider/id" so selection resolves through the
// existing switcher without aliasing or fabrication.
func catalogSnapshot() tui.CatalogSnapshot {
	entries, stale := catalog.SnapshotAll()
	options := make([]tui.ModelOption, 0, len(entries))
	for _, entry := range entries {
		model := entry.Model
		id := model.Provider + "/" + model.ID
		title := model.Name
		if title == "" {
			title = id
		}
		var bits []string
		if model.ContextWindow > 0 {
			bits = append(bits, formatTokens(model.ContextWindow)+" ctx")
		}
		switch {
		case entry.PriceKnown && entry.Free:
			bits = append(bits, "free")
		case entry.PriceKnown && (model.Cost.Input > 0 || model.Cost.Output > 0):
			bits = append(bits, fmt.Sprintf("$%.2f/$%.2f per 1M", model.Cost.Input, model.Cost.Output))
		}
		if model.Provider == "ollama" || model.Provider == "ollama-cloud" {
			bits = append(bits, "local")
		} else {
			bits = append(bits, "remote")
		}
		var caps []string
		if model.Reasoning {
			caps = append(caps, "reasoning")
		}
		for _, m := range model.InputModalities {
			if m == "image" {
				caps = append(caps, "images")
				break
			}
		}
		if len(caps) > 0 {
			bits = append(bits, strings.Join(caps, "+"))
		}
		options = append(options, tui.ModelOption{
			ID:              id,
			Provider:        model.Provider,
			Title:           title,
			Detail:          strings.Join(bits, " · "),
			Name:            model.Name,
			Context:         model.ContextWindow,
			PriceKnown:      entry.PriceKnown,
			Free:            entry.Free,
			Origin:          entry.Origin,
			Capabilities:    caps,
			MaxOutput:       model.MaxOutputTokens,
			CostInput:       model.Cost.Input,
			CostOutput:      model.Cost.Output,
			CostCacheRead:   model.Cost.CacheRead,
			CostCacheWrite:  model.Cost.CacheWrite,
			InputModalities: append([]string(nil), model.InputModalities...),
			ThinkingEfforts: thinkingEfforts(entry.Thinking),
			Reasoning:       model.Reasoning,
			ReasoningKnown:  entry.ReasoningKnown,
		})
	}
	origin := "live"
	if stale {
		origin = "cached"
	}
	if len(options) == 0 {
		origin = "loading"
	}
	return tui.CatalogSnapshot{Options: options, Origin: origin, Stale: stale}
}

// refreshModelCatalog fetches every dynamic provider's live roster off the
// update goroutine and reports back through the Bubble Tea command path.
//
// Public metadata needs no credential: OpenRouter's /models answers
// unauthenticated, so a missing key browses with "" rather than failing.
// Authenticated providers use their configured key when one resolves.
// Per-provider failures keep that provider's cached rows and surface one
// summary error; victories merge through the same snapshot the picker
// opened with, so newly added models appear without a binary rebuild.
func refreshModelCatalog(parent context.Context, held *harness, seq int) tea.Cmd {
	return func() tea.Msg {
		// The caller's context is the program lifetime: quitting the TUI
		// cancels the fetch instead of leaving provider requests running
		// behind a closed UI.
		ctx, cancel := context.WithTimeout(parent, 90*time.Second)
		defer cancel()
		var failures []string
		for _, provider := range catalog.DynamicProviders() {
			baseURL, _ := catalog.ProviderBaseURL(provider)
			if override, ok := held.registry.BaseURLOverride(provider); ok {
				baseURL = override
			}
			// A missing credential browses public metadata anonymously;
			// only inference requires authentication.
			key, _ := held.registry.ResolveAPIKey(provider)
			pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
			_, err := catalog.DiscoverModelsFresh(pctx, nil, provider, baseURL, key)
			pcancel()
			if err != nil {
				failures = append(failures, provider+": "+shortCatalogError(err))
			}
		}
		snapshot := catalogSnapshot()
		snapshot.Origin = "live"
		snapshot.Stale = false
		errMsg := ""
		if len(failures) > 0 {
			errMsg = strings.Join(failures, "; ")
			// Partial failure: refreshed providers are live, the rest keep
			// cached rows. Claim "live" only when everything refreshed.
			if len(failures) == len(catalog.DynamicProviders()) {
				snapshot.Origin = "cached"
				snapshot.Stale = true
			}
		}
		return tui.NewModelsRefreshedMsg(seq, snapshot, errMsg)
	}
}

// shortCatalogError compresses a fetch failure to one picker-status line.
func shortCatalogError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "HTTP 429") {
		return "rate-limited"
	}
	if strings.Contains(msg, "rejected the credential") {
		return "credential rejected"
	}
	if strings.Contains(msg, "no live discovery") {
		return "no live catalog"
	}
	if strings.Contains(msg, "context deadline") || strings.Contains(msg, "timeout") {
		return "timed out"
	}
	if strings.Contains(msg, "decode") {
		return "bad response"
	}
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "network") || strings.Contains(msg, "connection refused") {
		return "offline"
	}
	return "unreachable"
}

// formatTokens renders a context window compactly for picker detail.
func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// connectHooks builds the guided-setup providers from the harness.
func connectHooks(held *harness) tui.ConnectHooks {
	return tui.ConnectHooks{
		Providers: func() []tui.ProviderRow {
			return held.ProviderRows()
		},
		Validate: func(ctx context.Context, provider, key string) ([]string, string, error) {
			return held.validateProvider(ctx, provider, key)
		},
		Save: func(provider, key, model string) error {
			return held.SaveConnect(provider, key, provider+"/"+model)
		},
	}
}

// mentionLister builds the workspace roster offered by the "@" trigger. It
// walks the project with the search tools' own ignore rules, so the inline
// palette cannot suggest a path the read tool would refuse, and caps the roster
// so a large tree cannot stall discovery. A trailing slash marks a folder.
func mentionLister(root string) func() []string {
	return func() []string {
		paths := tools.WorkspaceMentions(context.Background(), root, 0)
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			if p.Dir {
				out = append(out, p.Path+"/")
				continue
			}
			out = append(out, p.Path)
		}
		return out
	}
}

// mentionReader reads a workspace-relative file for "@" context injection,
// with the same root containment the read tool enforces: a reference outside
// the workspace, a directory, or an oversized file fails rather than attaching
// the wrong bytes to the model request.
func mentionReader(root string) func(path string) (string, error) {
	return func(path string) (string, error) {
		return tools.ReadWorkspaceFile(root, path)
	}
}

// validateProvider checks a credential and lists models: probe first (cheap,
// precise errors), discovery second. An empty key resolves the detected
// credential, so choosing "use detected" never pastes anything anywhere.
func (h *harness) validateProvider(ctx context.Context, provider, key string) ([]string, string, error) {
	if strings.TrimSpace(key) == "" {
		resolved, _, err := h.registry.ResolveAPIKeySource(provider)
		if err != nil {
			return nil, "", err
		}
		key = resolved
	}
	baseURL, _ := catalog.ProviderBaseURL(provider)
	if override, ok := h.registry.BaseURLOverride(provider); ok {
		baseURL = override
	}
	api := providerAPI(provider)
	probe := ai.ProbeProvider(ctx, nil, api, baseURL, key)
	if !probe.Reachable || !probe.Authenticated {
		return nil, "", fmt.Errorf("%s", probe.Detail)
	}
	models, err := catalog.DiscoverModels(ctx, nil, provider, baseURL, key)
	if err != nil {
		// Discovery failing after a good probe still validates the
		// credential: the connection works, only the roster fetch did not.
		// Fall back to bundled rows so setup can proceed.
		bundled, berr := catalog.ListForProvider(provider)
		if berr != nil || len(bundled) == 0 {
			return nil, "", err
		}
		var ids []string
		for _, model := range bundled {
			ids = append(ids, model.ID)
		}
		return ids, probe.Detail + " (bundled roster)", nil
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, probe.Detail, nil
}
