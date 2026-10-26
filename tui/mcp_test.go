package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

// /mcp list renders the configured servers, their provenance, and their cache
// state without connecting to anything.
func TestMCPCommandListsServersWithoutConnecting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow {
			return []MCPServerRow{
				{Name: "lynx", Transport: "stdio", Trust: "local", Enabled: true, Connected: true, Cached: true, Capabilities: 12},
				{Name: "postgres", Transport: "streamable-http", Trust: "external", Enabled: false, Capabilities: 18},
			}
		},
	})
	Command{Name: "mcp"}.runOn(p)
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice
		}
	}
	for _, want := range []string{
		"MCP SERVERS",
		"lynx", "local", "stdio", "12 capabilities",
		"postgres", "external", "streamable-http", "disabled",
		"Trust is provenance, not authority",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/mcp output = %q, want %q", text, want)
		}
	}
}

// /mcp inspect with more than one server asks for a target instead of guessing.
func TestMCPInspectRequiresATargetWhenAmbiguous(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow {
			return []MCPServerRow{{Name: "a", Enabled: true}, {Name: "b", Enabled: true}}
		},
		Inspect: func(context.Context, string, bool) MCPInspection { return MCPInspection{} },
	})
	Command{Name: "mcp", Args: []string{"inspect"}}.runOn(p)
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice
		}
	}
	if !strings.Contains(text, "usage: /mcp inspect <server>") {
		t.Fatalf("/mcp inspect = %q, want a usage hint", text)
	}
}

// The inspection view makes authority classification visible without
// transport internals for every row.
func TestMCPInspectionRendersAuthority(t *testing.T) {
	out := RenderMCPInspection(MCPInspection{
		Server:       "github",
		Transport:    "stdio",
		Trust:        "external",
		Cached:       true,
		DiscoveredAt: time.Unix(0, 0).UTC(),
		Capabilities: []MCPCapabilityRow{
			{ID: "github:search_code", Name: "search_code", Kind: "tool", Classification: "read", Authority: "READ"},
			{ID: "github:create_issue", Name: "create_issue", Kind: "tool", Classification: "mutate", Authority: "EXTERNAL_SIDE_EFFECT"},
			{ID: "github:run_query", Name: "run_query", Kind: "tool", Classification: "execute", Authority: "EXECUTE"},
		},
	})
	for _, want := range []string{
		"github", "transport: stdio", "trust: external (provenance, not authority)",
		"source: capability cache", "search_code", "READ",
		"create_issue", "EXTERNAL_SIDE_EFFECT", "run_query", "EXECUTE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspection = %q, want %q", out, want)
		}
	}
}

// A session without MCP says so; it never opens a second registry.
func TestMCPCommandWithoutHooksReportsUnavailable(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "mcp"}.runOn(p)
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice
		}
	}
	if !strings.Contains(text, "MCP is not configured") {
		t.Fatalf("/mcp = %q, want an unavailable notice", text)
	}
}

// The registry exposes the command, so help and the palette cannot drift from
// dispatch.
func TestMCPCommandIsRegistered(t *testing.T) {
	spec, ok := LookupCommand("mcp")
	if !ok {
		t.Fatal("/mcp is not in the command registry")
	}
	if !strings.Contains(spec.Usage, "inspect") || !strings.Contains(spec.Usage, "refresh") {
		t.Fatalf("mcp usage = %q", spec.Usage)
	}
}
