package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// /mcp list renders the configured servers, their provenance, and their cache
// state without connecting to anything.
func TestMCPCommandListsServersWithoutConnecting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	inspected := 0
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow {
			return []MCPServerRow{
				{Name: "lynx", Transport: "stdio", Trust: "local", Enabled: true, Connected: true, Cached: true, Capabilities: 12},
				{Name: "postgres", Transport: "streamable-http", Trust: "external", Enabled: false, Capabilities: 18},
			}
		},
		Inspect: func(context.Context, string, bool) MCPInspection {
			inspected++
			return MCPInspection{}
		},
	})
	Command{Name: "mcp", Args: []string{"list"}}.runOn(p)
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
			t.Errorf("/mcp list output = %q, want %q", text, want)
		}
	}
	if inspected != 0 {
		t.Errorf("listing inspected %d servers, want no discovery", inspected)
	}
}

// /mcp opens the manager over cheap cached rows and never connects while
// doing so. This is the lazy lifecycle the whole command depends on.
func TestMCPCommandOpensManagerWithoutConnecting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	inspected, applied := 0, 0
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow {
			return []MCPServerRow{
				{Name: "lynx", Trust: "local", Enabled: true, Cached: true, Capabilities: 12},
				{Name: "github", Trust: "external", Enabled: false},
			}
		},
		Inspect: func(context.Context, string, bool) MCPInspection {
			inspected++
			return MCPInspection{}
		},
		Apply: func(context.Context, []MCPChange) []MCPApplyResult {
			applied++
			return nil
		},
	})
	Command{Name: "mcp"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerMCP {
		t.Fatalf("picker = %+v, want the MCP manager", p.state.Picker)
	}
	if inspected != 0 || applied != 0 {
		t.Fatalf("opening /mcp inspected=%d applied=%d, want no server work", inspected, applied)
	}
	if got := p.state.Picker.Items; len(got) != 2 || got[0].Title != "lynx" || got[0].State != "enabled" {
		t.Fatalf("items = %+v", got)
	}
}

// Up/down changes the selected server without changing its pending state.
func TestMCPManagerNavigationSelectsTheServer(t *testing.T) {
	p := openMCPManager(t, []MCPServerRow{
		{Name: "lynx", Enabled: true},
		{Name: "github", Enabled: false},
	})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	chosen, ok := p.state.Picker.Chosen()
	if !ok || chosen.Value != "github" {
		t.Fatalf("chosen = %+v, want github", chosen)
	}
	if chosen.Pending {
		t.Fatal("navigation changed pending state")
	}
}

// Space stages a toggle. It must not call the apply hook or open a connection.
func TestMCPManagerSpaceOnlyStagesTheToggle(t *testing.T) {
	calls := 0
	p := openMCPManagerWithApply(t, []MCPServerRow{
		{Name: "lynx", Enabled: true},
		{Name: "github", Enabled: false},
	}, func(context.Context, []MCPChange) []MCPApplyResult {
		calls++
		return nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	item, _ := p.state.Picker.Chosen()
	if !item.Active || item.State != "enabled" || !item.Pending {
		t.Fatalf("row after space = %+v, want staged enabled", item)
	}
	if calls != 0 {
		t.Fatalf("space applied %d changes, want none", calls)
	}
}

// Enter applies exactly the rows the user changed and leaves the rest alone.
func TestMCPManagerEnterAppliesOnlyChangedServers(t *testing.T) {
	var got []MCPChange
	p := openMCPManagerWithApply(t, []MCPServerRow{
		{Name: "lynx", Enabled: true},
		{Name: "github", Enabled: false},
		{Name: "postgres", Enabled: false},
	}, func(_ context.Context, changes []MCPChange) []MCPApplyResult {
		got = append(got, changes...)
		return []MCPApplyResult{{Name: changes[0].Name, Enabled: changes[0].Enabled, Connected: true, Capabilities: 31}}
	})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not start an apply")
	}
	// The apply runs in a Bubble Tea command; drain the notice it publishes so
	// a full event buffer can never make this test flaky.
	_ = cmd()
	notice := drainEventNotices(p)
	if len(got) != 1 || got[0].Name != "github" || !got[0].Enabled {
		t.Fatalf("changes = %+v, want only github enabled", got)
	}
	if !strings.Contains(notice, "● github") || !strings.Contains(notice, "31 capabilities") {
		t.Fatalf("notice = %q, want a truthful enable result", notice)
	}
	if p.state.Picker != nil {
		t.Fatal("the manager stayed open after applying")
	}
}

// Esc discards staged changes and never invokes the apply hook.
func TestMCPManagerEscDiscardsPendingChanges(t *testing.T) {
	calls := 0
	p := openMCPManagerWithApply(t, []MCPServerRow{
		{Name: "lynx", Enabled: true},
		{Name: "github", Enabled: false},
	}, func(context.Context, []MCPChange) []MCPApplyResult {
		calls++
		return nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Picker != nil {
		t.Fatal("Esc did not close the manager")
	}
	if calls != 0 {
		t.Fatalf("Esc applied %d changes, want none", calls)
	}
	if p.mcpBefore != nil {
		t.Fatal("Esc left the initial-state snapshot behind")
	}
}

// The empty state is an actionable picker, not a dead paragraph.
func TestMCPManagerEmptyStateIsActionable(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow { return nil },
		AddHint: func() string { return "Add servers in settings.json" },
	})
	Command{Name: "mcp"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerMCP {
		t.Fatal("empty /mcp did not open a picker")
	}
	if out := Render(p.state); !strings.Contains(out, "Enter select") {
		t.Fatalf("empty manager footer = %q, want the select contract", out)
	}
	item, ok := p.state.Picker.Chosen()
	if !ok || item.Value != MCPAddValue || item.Title != MCPAddTitle {
		t.Fatalf("empty row = %+v, want the add action", item)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	notice := mcpLastNotice(p)
	if !strings.Contains(notice, "settings.json") {
		t.Fatalf("add action notice = %q, want the configuration path", notice)
	}
}

// A failed apply shows the error and never claims the requested state worked.
func TestMCPManagerFailureIsTruthful(t *testing.T) {
	p := openMCPManagerWithApply(t, []MCPServerRow{
		{Name: "github", Enabled: false},
	}, func(context.Context, []MCPChange) []MCPApplyResult {
		return []MCPApplyResult{{Name: "github", Enabled: true, Error: "connection failed"}}
	})
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd()
	notice := drainEventNotices(p)
	if !strings.Contains(notice, "! github") || !strings.Contains(notice, "connection failed") {
		t.Fatalf("notice = %q, want a truthful failure", notice)
	}
	if strings.Contains(notice, "● github") || strings.Contains(notice, "capabilities") {
		t.Fatalf("notice = %q, a failed connection must not render as enabled", notice)
	}
}

// The manager renders state, trust, and a known capability count, and teaches
// the Space/Enter/Esc contract in its footer.
func TestMCPManagerRendersStateTrustAndCount(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(PickerOpened{
		Kind:  PickerMCP,
		Title: "MCP SERVERS",
		Items: []PickerItem{
			{Title: "lynx", Value: "lynx", State: "enabled", Trust: "local", Count: 12, Cached: true, Active: true},
			{Title: "postgres", Value: "postgres", State: "error", Trust: "external", Count: 8, Cached: true, Active: true},
		},
	})
	out := Render(s)
	for _, want := range []string{"● lynx", "local", "12", "! postgres", "external", "8", "Space toggle", "Enter apply", "Esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("manager render = %q, want %q", out, want)
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

// The registry exposes the command and its subcommands, so help and the
// palette cannot drift from dispatch.
func TestMCPCommandIsRegistered(t *testing.T) {
	spec, ok := LookupCommand("mcp")
	if !ok {
		t.Fatal("/mcp is not in the command registry")
	}
	if spec.Usage != "mcp" {
		t.Fatalf("mcp usage = %q, want the manager command", spec.Usage)
	}
	var help strings.Builder
	for _, sub := range spec.Subcommands {
		help.WriteString(sub.Usage)
		help.WriteString(" ")
		help.WriteString(sub.Description)
		help.WriteString("\n")
	}
	for _, want := range []string{"inspect <server>", "refresh [server]", "enable <server>", "disable <server>"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("mcp subcommands = %q, want %q", help.String(), want)
		}
	}
	if text := HelpText(); !strings.Contains(text, "/mcp inspect <server>") {
		t.Errorf("runtime help = %q, want the inspect form", text)
	}
}

// Space remains a search character in every other picker: the MCP toggle must
// not leak into model, session, or job selection.
func TestSpaceStillFiltersNonMCPPickers(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: []PickerItem{{Title: "deep seek"}}})
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("deep")},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune("seek")},
	} {
		p.Update(key)
	}
	if got := p.state.Picker.Filter; got != "deep seek" {
		t.Fatalf("model picker filter = %q, want the typed query", got)
	}
}

// openMCPManager opens /mcp with cheap rows and no apply hook.
func openMCPManager(t *testing.T, rows []MCPServerRow) *Program {
	t.Helper()
	p := NewProgram(nil, "test", 0)
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow { return rows },
	})
	Command{Name: "mcp"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerMCP {
		t.Fatalf("picker = %+v, want the MCP manager", p.state.Picker)
	}
	return p
}

// openMCPManagerWithApply opens /mcp and records apply calls.
func openMCPManagerWithApply(t *testing.T, rows []MCPServerRow, apply func(context.Context, []MCPChange) []MCPApplyResult) *Program {
	t.Helper()
	p := NewProgram(nil, "test", 0)
	p.SetMCPHooks(MCPHooks{
		Servers: func() []MCPServerRow { return rows },
		Apply:   apply,
	})
	Command{Name: "mcp"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerMCP {
		t.Fatalf("picker = %+v, want the MCP manager", p.state.Picker)
	}
	return p
}

// mcpLastNotice returns the most recent notice text in the transcript.
func mcpLastNotice(p *Program) string {
	for i := len(p.state.Entries) - 1; i >= 0; i-- {
		if p.state.Entries[i].Kind == EntryNotice {
			return p.state.Entries[i].Notice
		}
	}
	return ""
}

// drainEventNotices collects notices published by an asynchronous command.
func drainEventNotices(p *Program) string {
	var out []string
	for {
		select {
		case event := <-p.events:
			if notice, ok := event.(Notice); ok {
				out = append(out, notice.Text)
			}
		default:
			return strings.Join(out, "\n")
		}
	}
}
