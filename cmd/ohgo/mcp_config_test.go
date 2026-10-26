package main

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/config"
)

// Disabling every configured server removes its capabilities without changing
// native execution, while /mcp still shows the disabled entry.
func TestMCPDisabledServerRemovesCapabilities(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 1)
	disabled := false
	opts.settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{{
		Name:    "off",
		Command: "/bin/echo",
		Enabled: &disabled,
	}}}
	toolSet := buildTools(opts, ai.NewRegistry())
	for _, name := range []string{"mcp_list", "mcp_read", "mcp_call"} {
		if hasTool(toolSet, name) {
			t.Errorf("disabled MCP server still registered %q", name)
		}
	}
	rows := mcpServerRows(opts)
	if len(rows) != 1 || rows[0].Name != "off" || rows[0].Enabled {
		t.Fatalf("disabled server rows = %+v", rows)
	}
}

// Transport and trust metadata reach the inspector without being mistaken for
// authority.
func TestMCPConfigRowsCarryTransportAndTrust(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 1)
	opts.settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{{
		Name:      "remote",
		Transport: "streamable-http",
		URL:       "https://mcp.example.test/rpc",
		Trust:     "external",
	}}}
	rows := mcpServerRows(opts)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Transport != "streamable-http" || rows[0].Trust != "external" || !rows[0].Enabled {
		t.Fatalf("row = %+v", rows[0])
	}
}

// An unknown transport is a configuration error, never a silent stdio
// fallback.
func TestMCPUnknownTransportFailsClosed(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 1)
	opts.settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{{
		Name:      "bad",
		Transport: "htp",
		Command:   "/bin/echo",
	}}}
	toolSet := buildTools(opts, ai.NewRegistry())
	list, ok := findTool(toolSet, "mcp_list")
	if !ok {
		t.Fatal("mcp_list disappeared instead of reporting the configuration error")
	}
	result := list.Execute(agent.Context{Ctx: context.Background(), Budget: phase8Budget()}, map[string]any{})
	if !result.IsError {
		t.Fatalf("unknown transport was accepted: %+v", result)
	}
}

// findTool returns the tool with a name from a built set.
func findTool(toolSet []agent.Tool, name string) (agent.Tool, bool) {
	for _, tool := range toolSet {
		if tool.Name() == name {
			return tool, true
		}
	}
	return nil, false
}
