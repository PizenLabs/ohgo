package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// MCPCapabilityRow is one discovered MCP capability for /mcp inspect.
type MCPCapabilityRow struct {
	// ID is the stable server:capability identity.
	ID string
	// Name is the capability name without the server prefix.
	Name string
	// Kind is tool, resource, or prompt.
	Kind string
	// Classification is the configured safety class.
	Classification string
	// Authority is the normalized admission class.
	Authority string
}

// MCPServerRow is one configured MCP server for /mcp list.
type MCPServerRow struct {
	// Name is the configured server name.
	Name string
	// Transport is stdio or streamable-http.
	Transport string
	// Trust is local, external, or untrusted. It is provenance, not authority.
	Trust string
	// Enabled reports whether the server contributes capabilities.
	Enabled bool
	// Connected reports whether this session holds a live connection.
	Connected bool
	// Cached reports whether a capability snapshot exists.
	Cached bool
	// Capabilities is the cached capability count.
	Capabilities int
	// DiscoveredAt is when the snapshot was observed.
	DiscoveredAt time.Time
	// CacheExpiresAt is when the cached snapshot becomes stale. Zero means
	// the server has no cached snapshot.
	CacheExpiresAt time.Time
}

// MCPInspection is the result of one /mcp inspect or /mcp refresh.
type MCPInspection struct {
	// Server is the inspected server.
	Server string
	// Transport is the configured transport.
	Transport string
	// Trust is the configured provenance label.
	Trust string
	// Cached reports that the answer came from the capability cache.
	Cached bool
	// DiscoveredAt is when the snapshot was observed.
	DiscoveredAt time.Time
	// Capabilities are the advertised capabilities.
	Capabilities []MCPCapabilityRow
	// Truncated reports that a configured bound cut the capability list.
	Truncated bool
	// Reason names the bound that cut it.
	Reason string
	// Error is a bounded failure description. It never contains a secret.
	Error string
}

// MCPHooks connects /mcp to the session's one MCP manager.
//
// Both hooks are nil-safe: a session without MCP reports that MCP is not
// configured instead of starting a second registry.
type MCPHooks struct {
	// Servers lists configured servers without connecting to any of them.
	Servers func() []MCPServerRow
	// Inspect discovers one server. refresh bypasses the capability cache.
	Inspect func(ctx context.Context, server string, refresh bool) MCPInspection
}

// RenderMCPServers renders the /mcp list view.
//
// The view is deliberately table-like and monochrome-safe: a marker, a name
// column, trust, transport, and the capability count. It makes classification
// visible without turning the transcript into a settings screen.
func RenderMCPServers(rows []MCPServerRow) string {
	if len(rows) == 0 {
		return "MCP\n\nNo MCP servers are configured."
	}
	var b strings.Builder
	b.WriteString("MCP SERVERS\n\n")
	for _, row := range rows {
		marker := "o"
		state := "configured"
		if !row.Enabled {
			marker = "-"
			state = "disabled"
		} else if row.Connected {
			marker = "*"
			state = "connected"
		}
		trust := row.Trust
		if trust == "" {
			trust = "untrusted"
		}
		transport := row.Transport
		if transport == "" {
			transport = "stdio"
		}
		count := "no cached capabilities"
		if row.Cached {
			count = fmt.Sprintf("%d capabilities", row.Capabilities)
			if !row.CacheExpiresAt.IsZero() && time.Now().After(row.CacheExpiresAt) {
				count += " (stale)"
			}
		}
		fmt.Fprintf(&b, "%s %-12s %-10s %-14s %s\n",
			marker, row.Name, trust, transport, count)
		if state != "configured" {
			fmt.Fprintf(&b, "  %-12s %s\n", "", state)
		}
	}
	b.WriteString("\n* connected  o configured  - disabled\n")
	b.WriteString("Trust is provenance, not authority. /mcp inspect <server> discovers capabilities.")
	return strings.TrimRight(b.String(), "\n")
}

// RenderMCPInspection renders one server's capabilities with their authority
// class.
func RenderMCPInspection(view MCPInspection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", view.Server)
	b.WriteString(strings.Repeat("-", min(28, max(8, len(view.Server)))) + "\n")
	if view.Transport != "" {
		fmt.Fprintf(&b, "transport: %s\n", view.Transport)
	}
	if view.Trust != "" {
		fmt.Fprintf(&b, "trust: %s (provenance, not authority)\n", view.Trust)
	}
	if view.Cached {
		b.WriteString("source: capability cache\n")
	}
	if !view.DiscoveredAt.IsZero() {
		fmt.Fprintf(&b, "discovered: %s\n", view.DiscoveredAt.Format(time.RFC3339))
	}
	if view.Error != "" {
		b.WriteString("\nfailed: " + view.Error)
		return b.String()
	}
	if len(view.Capabilities) == 0 {
		b.WriteString("\nNo capabilities advertised.")
		return b.String()
	}
	if view.Truncated {
		fmt.Fprintf(&b, "\ntruncated: true (%s)\n", view.Reason)
	}
	b.WriteString("\n")
	for _, capability := range view.Capabilities {
		authority := capability.Authority
		if authority == "" {
			authority = strings.ToUpper(capability.Classification)
		}
		fmt.Fprintf(&b, "%-28s %-6s %s\n",
			truncateText(capability.Name, 28), capability.Kind, authority)
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncateText bounds a table cell without changing the full name shown in
// discovery.
func truncateText(value string, max int) string {
	if max <= 0 || len([]rune(value)) <= max {
		return value
	}
	runes := []rune(value)
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "~"
}

// mcpCommand routes /mcp list, inspect, and refresh.
//
// Listing is a cache read and never connects. Inspect and refresh run off the
// update goroutine so a slow server cannot freeze the UI; the result arrives as
// a notice, exactly like /doctor and /compact.
func (p *Program) mcpCommand(args []string) tea.Cmd {
	if p.mcp.Servers == nil && p.mcp.Inspect == nil {
		p.state.Apply(Notice{Text: "MCP is not configured in this session"})
		return nil
	}
	sub := "list"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "list":
		if p.mcp.Servers == nil {
			p.state.Apply(Notice{Text: "MCP is not configured in this session"})
			return nil
		}
		p.state.Apply(Notice{Text: RenderMCPServers(p.mcp.Servers())})
		return nil
	case "inspect", "refresh":
		if p.mcp.Inspect == nil {
			p.state.Apply(Notice{Text: "MCP inspection is unavailable in this session"})
			return nil
		}
		refresh := sub == "refresh"
		target := ""
		if len(args) > 1 {
			target = strings.TrimSpace(args[1])
		}
		if target == "" {
			var servers []MCPServerRow
			if p.mcp.Servers != nil {
				servers = p.mcp.Servers()
			}
			if !refresh {
				if len(servers) == 0 {
					p.state.Apply(Notice{Text: "no MCP servers are configured"})
					return nil
				}
				if len(servers) != 1 {
					p.state.Apply(Notice{Text: "usage: /mcp inspect <server>"})
					return nil
				}
				target = servers[0].Name
			} else {
				var names []string
				for _, server := range servers {
					if server.Enabled {
						names = append(names, server.Name)
					}
				}
				if len(names) == 0 {
					p.state.Apply(Notice{Text: "no enabled MCP servers to refresh"})
					return nil
				}
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
					defer cancel()
					var views []string
					for _, name := range names {
						views = append(views, RenderMCPInspection(p.mcp.Inspect(ctx, name, true)))
					}
					p.publish(Notice{Text: strings.Join(views, "\n\n")})
					return nil
				}
			}
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			p.publish(Notice{Text: RenderMCPInspection(p.mcp.Inspect(ctx, target, refresh))})
			return nil
		}
	default:
		p.state.Apply(Notice{Text: "usage: /mcp [list|inspect <server>|refresh [server]]"})
		return nil
	}
}
