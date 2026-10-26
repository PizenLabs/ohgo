package main

import (
	"context"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/knowledge"
	"github.com/PizenLabs/ohgo/mcp"
	"github.com/PizenLabs/ohgo/tui"
)

// mcpHooks builds the /mcp inspector over the same manager the tool set uses.
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
	}
}

// mcpServerRows renders the cheap configuration view. It reads cached
// capability counts only and never connects.
func mcpServerRows(opts options) []tui.MCPServerRow {
	var settings config.MCPSettings
	if opts.settings.MCP != nil {
		settings = *opts.settings.MCP
	}
	statuses := map[string]mcp.ServerStatus{}
	if opts.mcpRuntime != nil && opts.mcpRuntime.manager != nil {
		for _, status := range opts.mcpRuntime.manager.Status() {
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
			row.Connected = status.Connected
			row.Cached = status.Cached
			row.Capabilities = status.Capabilities
			row.DiscoveredAt = status.DiscoveredAt
			row.CacheExpiresAt = status.CacheExpiresAt
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
	if opts.settings.MCP != nil {
		for _, configured := range opts.settings.MCP.Servers {
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
	}
	if opts.mcpRuntime == nil || opts.mcpRuntime.manager == nil {
		view.Error = "no enabled MCP manager is attached to this session"
		return view
	}
	manager := opts.mcpRuntime.manager
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
		return view
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
			Classification: string(capability.Classification),
			Authority:      string(capability.Authority),
		})
	}
	return view
}

// mcpLine collapses untrusted server or configuration text to one bounded line
// before it reaches a terminal.
func mcpLine(text string, max int) string {
	text = knowledge.SanitizeText(text)
	text, _ = knowledge.BoundText(text, max)
	return strings.Join(strings.Fields(text), " ")
}

// boundedMCPError keeps a UI failure short and free of payloads.
func boundedMCPError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(knowledge.SanitizeText(err.Error()))
	text, _ = knowledge.BoundText(strings.Join(strings.Fields(text), " "), 240)
	return text
}

// replMCP renders /mcp for the plain REPL. It uses the same manager and the
// same renderers as the full-screen inspector.
func replMCP(h *harness, args []string) string {
	opts := options{}
	if h != nil {
		opts.mcpRuntime = h.mcp
		if h.settings != nil {
			opts.settings = *h.settings
		}
	}
	sub := "list"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "list":
		return tui.RenderMCPServers(mcpServerRows(opts))
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
	default:
		return "usage: /mcp [list|inspect <server>|refresh [server]]"
	}
}
