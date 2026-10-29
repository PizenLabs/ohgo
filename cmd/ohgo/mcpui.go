package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/execution/mcp"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// mcpHooks builds the /mcp manager over the same manager the tool set uses.
//
// A disabled server is listed but never inspected, so disabling MCP removes its
// capabilities without changing native execution. The hook never creates a
// second registry: the manager is the one buildTools already constructed.
func mcpHooks(opts options) tui.MCPHooks {
	return tui.MCPHooks{
		Servers: func() []tui.MCPServerRow {
			return mcpServerRows(opts)
		},
		Inspect: func(ctx context.Context, server string, refresh bool) tui.MCPInspection {
			return inspectMCP(ctx, opts, server, refresh)
		},
		Apply: func(ctx context.Context, changes []tui.MCPChange) []tui.MCPApplyResult {
			return applyMCPChanges(ctx, opts, changes)
		},
		AddHint: mcpAddHint,
	}
}

// mcpConfiguredSettings returns the current MCP block. The runtime snapshot
// wins because /mcp may have changed it in this session; the startup settings
// are the fallback for a test or a session that never built a manager.
func mcpConfiguredSettings(opts options) config.MCPSettings {
	if opts.mcpRuntime != nil {
		if settings, ok := opts.mcpRuntime.settingsSnapshot(); ok {
			return settings
		}
	}
	if opts.settings.MCP != nil {
		return *opts.settings.MCP
	}
	return config.MCPSettings{}
}

// mcpServerRows renders the cheap configuration view. It reads cached
// capability counts only and never connects.
func mcpServerRows(opts options) []tui.MCPServerRow {
	settings := mcpConfiguredSettings(opts)
	statuses := map[string]mcp.ServerStatus{}
	manager := (*mcp.Manager)(nil)
	if opts.mcpRuntime != nil {
		manager = opts.mcpRuntime.getManager()
	}
	if manager != nil {
		for _, status := range manager.Status() {
			statuses[status.Name] = status
		}
	}
	rows := make([]tui.MCPServerRow, 0, len(settings.Servers))
	for _, server := range settings.Servers {
		name := strings.TrimSpace(server.Name)
		row := tui.MCPServerRow{
			Name:      mcpLine(name, 100),
			Enabled:   server.IsEnabled(),
			Transport: mcpLine(string(mcp.Transport(server.Transport)), 40),
			Trust:     string(mcp.ParseTrust(server.Trust)),
		}
		if row.Transport == "" {
			row.Transport = string(mcp.TransportStdio)
		}
		if status, ok := statuses[name]; ok {
			// Runtime status is the actual state. Configuration is the
			// desired state; a failed apply must not be rendered as success.
			row.Enabled = status.Enabled
			row.Connected = status.Connected
			row.Cached = status.Cached
			row.Capabilities = status.Capabilities
			row.DiscoveredAt = status.DiscoveredAt
			row.CacheExpiresAt = status.CacheExpiresAt
		}
		if opts.mcpRuntime != nil {
			row.Error = opts.mcpRuntime.failure(name)
		}
		rows = append(rows, row)
	}
	return rows
}

// inspectMCP discovers one server for the UI. Discovery is read-only: it
// returns descriptions and never grants or exercises a capability.
func inspectMCP(ctx context.Context, opts options, server string, refresh bool) tui.MCPInspection {
	server = strings.TrimSpace(server)
	view := tui.MCPInspection{Server: mcpLine(server, 100)}
	settings := mcpConfiguredSettings(opts)
	for _, configured := range settings.Servers {
		if strings.TrimSpace(configured.Name) != server {
			continue
		}
		view.Transport = mcpLine(string(mcp.Transport(configured.Transport)), 40)
		if view.Transport == "" {
			view.Transport = string(mcp.TransportStdio)
		}
		view.Trust = string(mcp.ParseTrust(configured.Trust))
		break
	}
	manager := (*mcp.Manager)(nil)
	if opts.mcpRuntime != nil {
		manager = opts.mcpRuntime.getManager()
	}
	if manager == nil {
		view.Error = "no enabled MCP manager is attached to this session"
		return view
	}
	if !manager.Configured(server) {
		view.Error = "server is not configured or is disabled"
		return view
	}
	var discovery mcp.Discovery
	var err error
	if refresh {
		discovery, err = manager.Refresh(ctx, server, mcp.Meter{})
	} else {
		discovery, err = manager.Discover(ctx, server, mcp.Meter{})
	}
	if err != nil {
		view.Error = boundedMCPError(err)
		if opts.mcpRuntime != nil {
			// The manager row is a management surface: it says the connection
			// failed without echoing configuration. The inspection view keeps
			// the bounded detail for deliberate diagnosis.
			opts.mcpRuntime.setFailure(server, "connection failed")
		}
		return view
	}
	if opts.mcpRuntime != nil {
		opts.mcpRuntime.clearFailure(server)
	}
	view.Cached = discovery.CacheHit
	view.DiscoveredAt = discovery.FetchedAt
	view.Truncated = discovery.Truncated
	view.Reason = discovery.Reason
	for _, status := range manager.Status() {
		if status.Name != server {
			continue
		}
		view.Transport = string(status.Transport)
		view.Trust = string(status.Trust)
		break
	}
	for _, capability := range discovery.Capabilities {
		view.Capabilities = append(view.Capabilities, tui.MCPCapabilityRow{
			ID:             mcpLine(capability.ID(), 300),
			Name:           mcpLine(capability.Name, 200),
			Kind:           string(capability.Kind),
			Authority:      string(capability.Authority),
			Classification: string(capability.Classification),
		})
	}
	return view
}

// applyMCPChanges commits only the supplied changes.
//
// The manager transition runs first and the settings file is written only
// after it succeeds, so a saved "enabled" is not a claim that a connection
// worked. Enabling discovers once; disabling closes that one client. Servers
// missing from the change list are never touched.
func applyMCPChanges(ctx context.Context, opts options, changes []tui.MCPChange) []tui.MCPApplyResult {
	results := make([]tui.MCPApplyResult, 0, len(changes))
	if len(changes) == 0 {
		return results
	}

	current := mcpConfiguredSettings(opts)
	index := make(map[string]int, len(current.Servers))
	for i, server := range current.Servers {
		index[strings.TrimSpace(server.Name)] = i
	}

	persisted, err := settingsForMCPUpdate(opts, current)
	if err != nil {
		for _, change := range changes {
			results = append(results, tui.MCPApplyResult{Name: change.Name, Enabled: change.Enabled, Error: boundedMCPError(err)})
		}
		return results
	}

	manager := (*mcp.Manager)(nil)
	if opts.mcpRuntime != nil {
		manager = opts.mcpRuntime.getManager()
	}
	actualBefore := map[string]bool{}
	for _, server := range current.Servers {
		actualBefore[strings.TrimSpace(server.Name)] = server.IsEnabled()
	}
	if manager != nil {
		for _, status := range manager.Status() {
			actualBefore[status.Name] = status.Enabled
		}
	}

	// A session that started with every server disabled has no manager yet.
	// Build one from the desired enabled set; it still connects lazily, on the
	// explicit enable below, not on picker open.
	if manager == nil {
		desired := desiredServerSettings(current, changes)
		if configs := enabledServerConfigs(desired, opts.root); len(configs) > 0 {
			created, createErr := mcp.NewManager(configs)
			if createErr != nil {
				for _, change := range changes {
					results = append(results, tui.MCPApplyResult{Name: change.Name, Enabled: change.Enabled, Error: boundedMCPError(createErr)})
				}
				return results
			}
			opts.mcpRuntime.set(created)
			manager = created
		}
	}

	saved := false
	for _, change := range changes {
		name := strings.TrimSpace(change.Name)
		idx, ok := index[name]
		if !ok {
			results = append(results, tui.MCPApplyResult{Name: name, Enabled: change.Enabled, Error: "server is not configured"})
			continue
		}
		server := current.Servers[idx]
		cfg := mcpServerConfig(server, opts.root)
		result := tui.MCPApplyResult{Name: name, Enabled: change.Enabled}

		if manager != nil {
			if err := manager.SetServerEnabled(cfg, change.Enabled); err != nil {
				result.Error = boundedMCPError(err)
				results = append(results, result)
				continue
			}
		} else if change.Enabled {
			result.Error = "no MCP manager is available for this session"
			results = append(results, result)
			continue
		}

		if change.Enabled {
			discovery, discoverErr := manager.Discover(ctx, name, mcp.Meter{})
			if discoverErr != nil {
				// The row and the notice name the failure, not the command,
				// endpoint, or header that produced it. /mcp inspect remains
				// the deliberate diagnostic path.
				result.Error = "connection failed"
				if opts.mcpRuntime != nil {
					opts.mcpRuntime.setFailure(name, result.Error)
				}
			} else {
				result.Connected = true
				result.Capabilities = len(discovery.Capabilities)
				if opts.mcpRuntime != nil {
					opts.mcpRuntime.clearFailure(name)
				}
			}
		} else if opts.mcpRuntime != nil {
			opts.mcpRuntime.clearFailure(name)
		}

		enabled := change.Enabled
		persisted.MCP.Servers[idx].Enabled = &enabled
		saved = true
		results = append(results, result)
	}

	if !saved {
		return results
	}
	if err := config.Save(persisted); err != nil {
		// The manager moved but the durable state did not. Revert every
		// transition so the next session and the current session agree, and
		// report the failure rather than pretending the change stuck.
		if manager != nil {
			for _, change := range changes {
				idx, ok := index[strings.TrimSpace(change.Name)]
				if !ok {
					continue
				}
				was, known := actualBefore[change.Name]
				if !known {
					continue
				}
				_ = manager.SetServerEnabled(mcpServerConfig(current.Servers[idx], opts.root), was)
			}
		}
		for i := range results {
			if results[i].Error == "" {
				results[i].Error = "saving MCP configuration failed"
				results[i].Connected = false
			}
		}
		return results
	}

	if opts.mcpRuntime != nil {
		opts.mcpRuntime.configure(*persisted.MCP)
	}
	return results
}

// settingsForMCPUpdate loads the durable settings so an MCP change preserves
// every other setting, including ones edited after this session started. The
// current MCP block is authoritative for the rows being managed.
func settingsForMCPUpdate(opts options, current config.MCPSettings) (config.Settings, error) {
	persisted, err := config.Load(opts.root)
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			return config.Settings{}, err
		}
		persisted = opts.settings
	}
	persisted.MCP = &config.MCPSettings{Servers: append([]config.MCPServer(nil), current.Servers...)}
	return persisted, nil
}

// desiredServerSettings applies changes to a copy for initial manager setup.
func desiredServerSettings(settings config.MCPSettings, changes []tui.MCPChange) config.MCPSettings {
	out := cloneMCPSettings(settings)
	index := make(map[string]int, len(out.Servers))
	for i, server := range out.Servers {
		index[strings.TrimSpace(server.Name)] = i
	}
	for _, change := range changes {
		idx, ok := index[strings.TrimSpace(change.Name)]
		if !ok {
			continue
		}
		enabled := change.Enabled
		out.Servers[idx].Enabled = &enabled
	}
	return out
}

// enabledServerConfigs converts every enabled server for manager creation.
func enabledServerConfigs(settings config.MCPSettings, root string) []mcp.ServerConfig {
	configs := make([]mcp.ServerConfig, 0, len(settings.Servers))
	for _, server := range settings.Servers {
		if !server.IsEnabled() {
			continue
		}
		configs = append(configs, mcpServerConfig(server, root))
	}
	return configs
}

// mcpAddHint names the canonical configuration file and the block to edit.
// It is a diagnostic path, not a second configuration system: /mcp never opens
// an inline editor or writes a side store.
func mcpAddHint() string {
	path := ""
	if resolved, err := config.Path(""); err == nil && resolved != "" {
		path = mcpTildePath(resolved)
	}
	return tui.MCPAddHintText(path)
}

// mcpTildePath shortens the home directory prefix so the hint names the
// canonical ~/.ohgo/settings.json path while keeping an unusual state
// directory fully qualified.
func mcpTildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// mcpLine collapses untrusted server or configuration text to one bounded line
// before it reaches a terminal.
func mcpLine(text string, max int) string {
	text = knowledge.SanitizeText(text)
	text, _ = knowledge.BoundText(text, max)
	return strings.Join(strings.Fields(text), " ")
}

// boundedMCPError keeps a UI failure short and free of payloads.
//
// A transport error may embed an endpoint URL, so URL-shaped tokens are
// dropped before the message reaches a row or the transcript.
func boundedMCPError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(knowledge.SanitizeText(err.Error()))
	fields := strings.Fields(text)
	kept := fields[:0]
	for _, field := range fields {
		lower := strings.ToLower(field)
		if strings.Contains(lower, "://") || strings.HasPrefix(lower, "http") {
			continue
		}
		kept = append(kept, field)
	}
	text = strings.Join(kept, " ")
	if text == "" {
		text = "connection failed"
	}
	text, _ = knowledge.BoundText(text, 240)
	return text
}

// replMCP renders /mcp for the plain REPL. It uses the same manager and the
// same renderers as the full-screen manager, with textual enable/disable as
// the non-TUI management path.
func replMCP(h *harness, args []string) string {
	opts := options{}
	if h != nil {
		opts.mcpRuntime = h.mcp
		if h.settings != nil {
			opts.settings = *h.settings
		}
		opts.root = h.root
	}
	sub := "list"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "list":
		rows := mcpServerRows(opts)
		out := tui.RenderMCPServers(rows)
		if len(rows) == 0 {
			out += "\n\n" + mcpAddHint()
		} else {
			out += "\n\n/mcp enable <server> · /mcp disable <server> · /mcp inspect <server>"
		}
		return out
	case "inspect", "refresh":
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		refresh := sub == "refresh"
		target := ""
		if len(args) > 1 {
			target = strings.TrimSpace(args[1])
		}
		if target == "" && !refresh {
			rows := mcpServerRows(opts)
			if len(rows) != 1 {
				return "usage: /mcp inspect <server>"
			}
			target = rows[0].Name
		}
		if refresh && target == "" {
			var views []string
			for _, row := range mcpServerRows(opts) {
				if row.Enabled {
					views = append(views, tui.RenderMCPInspection(inspectMCP(ctx, opts, row.Name, true)))
				}
			}
			if len(views) == 0 {
				return "no enabled MCP servers to refresh"
			}
			return strings.Join(views, "\n\n")
		}
		return tui.RenderMCPInspection(inspectMCP(ctx, opts, target, refresh))
	case "enable", "disable":
		target := ""
		if len(args) > 1 {
			target = strings.TrimSpace(args[1])
		}
		if target == "" {
			return "usage: /mcp " + sub + " <server>"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		results := applyMCPChanges(ctx, opts, []tui.MCPChange{{Name: target, Enabled: sub == "enable"}})
		// The harness settings pointer is the plain REPL's cheap view; refresh
		// it so the next /mcp list agrees with disk without restarting.
		if h != nil && h.settings != nil {
			if reloaded, err := config.Load(h.root); err == nil {
				*h.settings = reloaded
			}
		}
		return tui.RenderMCPApplyResults(results)
	default:
		return "usage: /mcp [list|inspect <server>|refresh [server]|enable <server>|disable <server>]"
	}
}
