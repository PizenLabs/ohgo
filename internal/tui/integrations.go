package tui

import "strings"

// Integration status is the sidebar's projection of the runtime's external
// capabilities. It is display state only: the manager, registry, and language
// server processes remain the owners, and the view never connects or probes.
//
// MCP rows are cheap configuration plus cached runtime status, so publishing
// them on startup and after an explicit /mcp change costs no network work.
// LSP rows are supplied by the host through LSPHooks; a host without one simply
// has no language-server rows to show.

// IntegrationKind names the capability family.
type IntegrationKind string

const (
	// IntegrationMCP is a configured Model Context Protocol server.
	IntegrationMCP IntegrationKind = "mcp"
	// IntegrationLSP is a language server attached to the session.
	IntegrationLSP IntegrationKind = "lsp"
	// IntegrationKindMCP and IntegrationKindLSP are descriptive aliases for
	// callers that name the kind explicitly.
	IntegrationKindMCP = IntegrationMCP
	IntegrationKindLSP = IntegrationLSP
)

// IntegrationStatus is one row in the sidebar's Active Integrations section.
//
// The zero value is inactive and hidden. Active is true only for a live,
// usable connection; State still reports the truthful configured state so an
// enabled server that has not connected yet is not rendered as connected.
type IntegrationStatus struct {
	Kind  IntegrationKind
	Name  string
	State string
	// Status mirrors State for callers that use the lifecycle vocabulary
	// directly. State remains canonical when both are set.
	Status string
	Detail string
	Active bool
	Error  string
}

// Integration is a shorter alias for callers that build a status directly.
type Integration = IntegrationStatus

// IntegrationsUpdated publishes the complete integration snapshot. It is a
// projection, never a second registry: the Program reads the existing MCP
// manager hooks and any host-supplied LSP rows.
type IntegrationsUpdated struct {
	Items []IntegrationStatus
}

func (IntegrationsUpdated) eventKind() string { return "integrations_updated" }

// LSPServerRow is one language server reported by the host.
//
// Status is the server's own lifecycle word (starting, ready, stopped, ...).
// Active is reserved for a ready, attached server; the view renders a starting
// server truthfully but does not claim it is usable.
type LSPServerRow struct {
	Name     string
	Language string
	Status   string
	Detail   string
	Active   bool
	Error    string
}

// LSPHooks connects the sidebar to the host's language-server status.
//
// Servers must be cheap: the view calls it when it refreshes integrations, not
// on every frame. A zero value simply contributes no LSP rows.
type LSPHooks struct {
	Servers func() []LSPServerRow
}

// ProjectMCPIntegrations converts cheap MCP manager rows into sidebar rows.
// Disabled servers are omitted: the section is for active integrations, not a
// second copy of the /mcp manager.
func ProjectMCPIntegrations(rows []MCPServerRow) []IntegrationStatus {
	items := make([]IntegrationStatus, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled && row.Error == "" {
			continue
		}
		state := "enabled"
		if row.Connected {
			state = "connected"
		}
		if row.Error != "" {
			state = "error"
		}
		detail := mcpTrust(row.Trust)
		if row.Cached {
			detail += " · " + mcpCapabilityLabel(row.Capabilities)
		}
		if row.Transport != "" {
			detail += " · " + sanitizeInline(row.Transport)
		}
		items = append(items, IntegrationStatus{
			Kind:   IntegrationMCP,
			Name:   sanitizeInline(row.Name),
			State:  state,
			Status: state,
			Detail: detail,
			Active: row.Connected,
			Error:  sanitizeInline(row.Error),
		})
	}
	return items
}

// ProjectLSPIntegrations converts host language-server rows into sidebar rows.
// Stopped servers are omitted for the same reason disabled MCP rows are.
func ProjectLSPIntegrations(rows []LSPServerRow) []IntegrationStatus {
	items := make([]IntegrationStatus, 0, len(rows))
	for _, row := range rows {
		status := strings.ToLower(strings.TrimSpace(row.Status))
		if status == "stopped" && row.Error == "" {
			continue
		}
		detail := sanitizeInline(row.Language)
		if extra := sanitizeInline(row.Detail); extra != "" {
			if detail != "" {
				detail += " · "
			}
			detail += extra
		}
		items = append(items, IntegrationStatus{
			Kind:   IntegrationLSP,
			Name:   sanitizeInline(row.Name),
			State:  sanitizeInline(row.Status),
			Status: sanitizeInline(row.Status),
			Detail: detail,
			Active: row.Active,
			Error:  sanitizeInline(row.Error),
		})
	}
	return items
}

// SetLSPHooks installs the optional language-server roster and republishes the
// integration snapshot immediately. It is a setup seam, like SetMCPHooks.
func (p *Program) SetLSPHooks(hooks LSPHooks) {
	p.lsp = hooks
	p.syncIntegrations()
}

// PublishIntegrations replaces the sidebar's integration snapshot. It is safe
// to call from another goroutine, like PublishNotice.
func (p *Program) PublishIntegrations(items []IntegrationStatus) {
	p.publish(IntegrationsUpdated{Items: append([]IntegrationStatus(nil), items...)})
}

// syncIntegrations applies the current MCP/LSP snapshot directly. It is used
// during setup, before the event loop starts.
func (p *Program) syncIntegrations() {
	p.state.Apply(IntegrationsUpdated{Items: p.integrationSnapshot()})
}

// publishIntegrations sends the current snapshot through the normal event
// channel when called from an async command.
func (p *Program) publishIntegrations() {
	p.publish(IntegrationsUpdated{Items: p.integrationSnapshot()})
}

// integrationSnapshot reads the existing hooks. Each hook is cheap by
// contract; the Program owns no separate integration registry.
func (p *Program) integrationSnapshot() []IntegrationStatus {
	var items []IntegrationStatus
	if p.mcp.Servers != nil {
		items = append(items, ProjectMCPIntegrations(p.mcp.Servers())...)
	}
	if p.lsp.Servers != nil {
		items = append(items, ProjectLSPIntegrations(p.lsp.Servers())...)
	}
	return items
}
