package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/mcp"
	"github.com/PizenLabs/ohgo/tui"
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

// Applying one enable persists only that server and records the real
// connection outcome: a saved configuration is not a connection claim.
func TestMCPApplyPersistsOnlyTheChangedServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	opts := testOptions(t, root, 1)
	opts.mcpRuntime = &mcpRuntime{}
	enabled := true
	opts.settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{
		{Name: "keep", Command: "/bin/echo", Enabled: &enabled},
		{Name: "off", Command: "definitely-not-an-mcp-command"},
	}}
	buildTools(opts, ai.NewRegistry())

	results := applyMCPChanges(context.Background(), opts, []tui.MCPChange{{Name: "off", Enabled: true}})
	if len(results) != 1 || results[0].Error == "" {
		t.Fatalf("results = %+v, want a truthful connection failure", results)
	}
	if results[0].Connected {
		t.Fatalf("results = %+v, a failed discovery claimed a connection", results)
	}

	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("load persisted settings: %v", err)
	}
	if reloaded.MCP == nil || len(reloaded.MCP.Servers) != 2 {
		t.Fatalf("persisted MCP = %+v", reloaded.MCP)
	}
	byName := map[string]config.MCPServer{}
	for _, server := range reloaded.MCP.Servers {
		byName[server.Name] = server
	}
	if !byName["off"].IsEnabled() {
		t.Error("the changed server was not persisted as enabled")
	}
	if !byName["keep"].IsEnabled() {
		t.Error("the untouched server lost its enabled state")
	}

	statuses := opts.mcpRuntime.getManager().Status()
	for _, status := range statuses {
		if status.Name == "keep" && status.Connected {
			t.Error("applying one server connected the untouched server")
		}
	}
	rows := mcpServerRows(opts)
	for _, row := range rows {
		if row.Name != "off" {
			continue
		}
		if row.Error == "" || row.Connected {
			t.Errorf("off row = %+v, want a truthful failure state", row)
		}
	}
}

// The plain REPL keeps a usable textual management path: disable persists and
// the next list reflects it without a restart.
func TestReplMCPDisableIsUsableWithoutFullScreenUI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	settings := config.Default()
	enabled := true
	settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{{
		Name: "off", Command: "/bin/echo", Enabled: &enabled,
	}}}
	runtime := &mcpRuntime{}
	manager, err := mcp.NewManager([]mcp.ServerConfig{{Name: "off", Command: "/bin/echo"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	runtime.set(manager)
	runtime.configure(*settings.MCP)
	h := &harness{settings: &settings, root: root, mcp: runtime}

	out := replMCP(h, []string{"disable", "off"})
	if !strings.Contains(out, "○ off") || !strings.Contains(out, "disabled") {
		t.Fatalf("disable output = %q, want a disabled row", out)
	}
	list := replMCP(h, nil)
	if !strings.Contains(list, "○ off") || !strings.Contains(list, "disabled") {
		t.Fatalf("list after disable = %q, want the persisted disabled state", list)
	}
	reloaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("load persisted settings: %v", err)
	}
	if reloaded.MCP == nil || reloaded.MCP.Servers[0].IsEnabled() {
		t.Fatalf("persisted = %+v, want off disabled", reloaded.MCP)
	}
}

// An empty plain-REPL list names the canonical configuration path instead of
// leaving the user with a dead end.
func TestReplMCPEmptyStateNamesTheConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := replMCP(&harness{settings: &config.Settings{}}, nil)
	if !strings.Contains(out, "No MCP servers are configured") || !strings.Contains(out, ".ohgo") {
		t.Fatalf("empty output = %q, want an actionable path", out)
	}
}

// The empty state's add action names the canonical path and the mcp.servers
// shape. It is a pointer to the one settings file, never a second store.
func TestMCPAddHintNamesCanonicalPathAndSchema(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hint := mcpAddHint()
	for _, want := range []string{".ohgo/settings.json", `"mcp"`, `"servers"`, `"name"`, `"command"`, "trust"} {
		if !strings.Contains(hint, want) {
			t.Errorf("add hint = %q, want %q", hint, want)
		}
	}
}

// A failed manager transition must not reach disk: the settings file only
// records states the runtime actually accepted.
func TestMCPApplyDoesNotPersistWhenManagerTransitionFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	opts := testOptions(t, root, 1)
	enabled := true
	opts.settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{{
		Name: "off", Command: "/bin/echo", Enabled: &enabled,
	}}}
	runtime := &mcpRuntime{}
	manager, err := mcp.NewManager([]mcp.ServerConfig{{Name: "off", Command: "/bin/echo"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	runtime.set(manager)
	runtime.configure(*opts.settings.MCP)
	opts.mcpRuntime = runtime
	if err := manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	results := applyMCPChanges(context.Background(), opts, []tui.MCPChange{{Name: "off", Enabled: false}})
	if len(results) != 1 || results[0].Error == "" {
		t.Fatalf("results = %+v, want a failed transition", results)
	}
	if _, err := config.Load(root); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("settings were written after a failed transition: %v", err)
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
