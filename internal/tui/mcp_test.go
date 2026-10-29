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
		"lynx", "local", "12 capabilities", "connected",
		"postgres", "external", "disabled",
		"Trust is provenance, not authority",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/mcp list output = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "stdio") || strings.Contains(text, "streamable-http") {
		t.Errorf("/mcp list leaked transport detail into the manager rows: %q", text)
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
		AddHint: func() string {
			return "Add servers in ~/.ohgo/settings.json under \"mcp\": {\"servers\": [...]}"
		},
	})
	Command{Name: "mcp"}.runOn(p)
	if p.state.Picker == nil || p.state.Picker.Kind != PickerMCP {
		t.Fatal("empty /mcp did not open a picker")
	}
	out := Render(p.state)
	for _, want := range []string{
		"MCP · no servers configured",
		"> + Add server",
		"↑↓ navigate · Enter select · Esc close",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("empty manager = %q, want %q", out, want)
		}
	}
	item, ok := p.state.Picker.Chosen()
	if !ok || item.Value != MCPAddValue || item.Title != MCPAddTitle {
		t.Fatalf("empty row = %+v, want the add action", item)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	notice := mcpLastNotice(p)
	if !strings.Contains(notice, "settings.json") || !strings.Contains(notice, `"mcp"`) || !strings.Contains(notice, "servers") {
		t.Fatalf("add action notice = %q, want the configuration path and mcp.servers shape", notice)
	}
}

// Even without an AddHint hook, the empty state points at the canonical
// settings file and the mcp.servers shape. It never opens a second editor.
func TestMCPManagerEmptyStateFallbackHintIsActionable(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.SetMCPHooks(MCPHooks{Servers: func() []MCPServerRow { return nil }})
	Command{Name: "mcp"}.runOn(p)
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	notice := mcpLastNotice(p)
	for _, want := range []string{"~/.ohgo/settings.json", `"mcp"`, `"servers"`} {
		if !strings.Contains(notice, want) {
			t.Fatalf("fallback add hint = %q, want %q", notice, want)
		}
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

// The manager reuses the shared modal frame: thin box drawing, a cyan
// border-embedded title formatted "MCP · <status>", and no bespoke rounded
// box. It also keeps transport, command, URL, header, and secret text out of
// the row entirely.
func TestMCPManagerCardMatchesTheModalFamily(t *testing.T) {
	p := openMCPManager(t, []MCPServerRow{
		{Name: "lynx", Trust: "local", Enabled: true, Cached: true, Capabilities: 12},
	})
	out := Render(p.state)
	for _, want := range []string{"┌", "┐", "└", "┘", "MCP · 1 server", "● lynx", "local", "12 capabilities"} {
		if !strings.Contains(out, want) {
			t.Errorf("manager card = %q, want %q", out, want)
		}
	}
	for _, forbidden := range []string{"╭", "╮", "╰", "╯", "stdio", "streamable-http", "http://", "https://"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("manager card leaked %q:\n%s", forbidden, out)
		}
	}
}

// Printable keys do not build an invisible search query in /mcp. Esc therefore
// closes on the first press instead of merely clearing hidden text.
func TestMCPManagerIgnoresPrintableKeys(t *testing.T) {
	p := openMCPManager(t, []MCPServerRow{
		{Name: "lynx", Enabled: true},
		{Name: "github", Enabled: false},
	})
	for _, r := range "abc def" {
		p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if p.state.Picker == nil {
		t.Fatal("typing closed the manager")
	}
	if p.state.Picker.Filter != "" {
		t.Fatalf("manager filter = %q, want no hidden query", p.state.Picker.Filter)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Picker != nil {
		t.Fatal("Esc cleared an invisible query instead of closing")
	}
}

// Toggling back to the state captured at open clears the pending marker, so
// Enter never reports a change the user already reverted.
func TestMCPManagerToggleBackClearsPending(t *testing.T) {
	p := openMCPManager(t, []MCPServerRow{{Name: "lynx", Enabled: true}})
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	if item, _ := p.state.Picker.Chosen(); !item.Pending || item.Active {
		t.Fatalf("after first space = %+v, want staged disable", item)
	}
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	item, _ := p.state.Picker.Chosen()
	if item.Pending || !item.Active || item.State != "enabled" {
		t.Fatalf("after second space = %+v, want the original enabled state", item)
	}
	if got := p.state.Picker.PendingCount(); got != 0 {
		t.Fatalf("pending count = %d, want none", got)
	}
}

// A failed connection renders with "!" and never claims a live connection,
// even while the human stages the opposite desired state.
func TestMCPManagerFailureStaysTruthfulWhileStaged(t *testing.T) {
	p := openMCPManager(t, []MCPServerRow{{
		Name:         "github",
		Trust:        "external",
		Enabled:      true,
		Connected:    true,
		Cached:       true,
		Capabilities: 4,
		Error:        "connection failed",
	}})
	out := Render(p.state)
	for _, want := range []string{"! github", "external", "connection failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("failed row = %q, want %q", out, want)
		}
	}
	for _, forbidden := range []string{"● github", "connected"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("failed row rendered %q as healthy:\n%s", forbidden, out)
		}
	}
	p.Update(tea.KeyMsg{Type: tea.KeySpace})
	out = Render(p.state)
	if !strings.Contains(out, "! github") || !strings.Contains(out, "connection failed") || !strings.Contains(out, "pending") {
		t.Fatalf("staged failed row = %q, want failure plus pending", out)
	}
}

// The full-screen Command & Help Center names each MCP subcommand's full form,
// not five indistinguishable "/mcp" rows.
func TestHelpCenterDisplaysMCPSubcommands(t *testing.T) {
	view := NewHelpCenter(Commands)
	items := commandItems(Commands)
	parentSeen := false
	for _, item := range items {
		if item.Name != "mcp" || item.Usage != "mcp" {
			continue
		}
		parentSeen = true
		row := renderHelpRow(helpRow{Item: item}, view, DarkPalette(), 80, 8)
		if !strings.Contains(row, "/mcp") || strings.Contains(row, "/mcp mcp") {
			t.Errorf("parent help row = %q, want a single /mcp invocation", row)
		}
	}
	if !parentSeen {
		t.Error("help center is missing the /mcp parent command")
	}
	want := []string{"list", "inspect <server>", "refresh [server]", "enable <server>", "disable <server>"}
	for _, usage := range want {
		found := false
		for _, item := range items {
			if item.Name != "mcp" || item.Usage != usage {
				continue
			}
			found = true
			row := renderHelpRow(helpRow{Item: item}, view, DarkPalette(), 80, 8)
			if !strings.Contains(row, "/mcp "+usage) {
				t.Errorf("help row = %q, want /mcp %s", row, usage)
			}
		}
		if !found {
			t.Errorf("help center is missing /mcp %s", usage)
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
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
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
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
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
