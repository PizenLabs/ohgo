package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// connectViewFixture is a populated roster for the Provider Manager render
// tests: one configured provider, one missing credential, and one that needs
// none.
func connectViewFixture() ConnectView {
	return ConnectView{
		Providers: []ProviderRow{
			{ID: "anthropic", Name: "Anthropic", Driver: "Anthropic Messages", KeyDetail: "environment (ANTHROPIC_API_KEY)", Count: 5, Configured: true},
			{ID: "groq", Name: "Groq", Driver: "OpenAI Chat", KeyDetail: "missing: set GROQ_API_KEY", Count: 3},
			{ID: "ollama", Name: "Ollama", Driver: "Ollama Chat", KeyDetail: "none required", Configured: true},
		},
		Cursor:      0,
		VisibleRows: 5,
	}
}

// connectModalCard returns the plain dialog for a size, with ANSI stripped.
func connectModalCard(view ConnectView, width, height int) string {
	return stripANSI(RenderConnectModal(view, DarkPalette(), width, height))
}

// connectModalLines is the plain dialog split into rows.
func connectModalLines(view ConnectView, width, height int) []string {
	return strings.Split(connectModalCard(view, width, height), "\n")
}

// connectProgram builds a program whose Provider Manager roster and guided
// setup are installed from a fixture.
func connectProgram(t *testing.T, rows []ProviderRow) *Program {
	t.Helper()
	p := NewProgram(nil, "test", 0)
	p.SetConnectHooks(ConnectHooks{
		Providers: func() []ProviderRow { return rows },
	})
	return p
}

// The dialog carries the border-embedded title, the roster table, the
// credential markers, the configuration pane, and the action bar.
func TestRenderConnectModalShowsRosterAndConfig(t *testing.T) {
	lines := connectModalLines(connectViewFixture(), 92, 0)
	out := strings.Join(lines, "\n")

	for _, want := range []string{
		"PROVIDER MANAGER",
		"PROVIDERS", "CONFIGURATION",
		"STATUS", "PROVIDER", "TYPE", "DETAILS",
		"[✓]", "[×]",
		"Anthropic", "Groq", "Ollama",
		"Anthropic Messages", "OpenAI Chat", "Ollama Chat",
		"Driver", "Status", "Credential", "Setup",
		"environment (ANTHROPIC_API_KEY)",
		"ready — credential: environment (ANTHROPIC_API_KEY)",
		"Esc", "q", "close modal",
		"─",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dialog missing %q:\n%s", want, out)
		}
	}

	// The title rides the top border, not a body row.
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") || !strings.Contains(lines[0], "PROVIDER MANAGER") {
		t.Errorf("title is not embedded in the top border: %q", lines[0])
	}

	// The highlighted row carries the cursor independently of the credential
	// state.
	selectedRow := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "│> ") {
			selectedRow = line
			break
		}
	}
	if !strings.Contains(selectedRow, "Anthropic") {
		t.Errorf("cursor is not on the selected provider: %q", selectedRow)
	}
}

// The frame is rectangular at every width: each row is exactly the dialog
// width, so the borders land in one column and optional columns are shed
// rather than overlapping.
func TestRenderConnectModalFrameAlignment(t *testing.T) {
	for _, width := range []int{34, 50, 92} {
		lines := connectModalLines(connectViewFixture(), width, 0)
		want := CellWidth(lines[0])
		for i, line := range lines {
			if got := CellWidth(line); got != want {
				t.Errorf("width %d row %d is %d cells, want %d: %q", width, i, got, want, line)
			}
		}
	}
}

// A narrow interior sheds the optional TYPE and DETAILS columns rather than
// crushing the provider column into overlapping text.
func TestRenderConnectModalShedsOptionalColumns(t *testing.T) {
	narrow := connectModalCard(connectViewFixture(), 34, 0)
	if strings.Contains(narrow, "DETAILS") || strings.Contains(narrow, "TYPE") {
		t.Errorf("narrow dialog kept optional columns:\n%s", narrow)
	}
	if !strings.Contains(narrow, "PROVIDER") || !strings.Contains(narrow, "Anthropic") {
		t.Errorf("narrow dialog dropped the provider column:\n%s", narrow)
	}
}

// A short viewport keeps the frame and footer intact rather than overflowing.
func TestRenderConnectModalKeepsFooterWhenShort(t *testing.T) {
	out := connectModalCard(connectViewFixture(), 92, 8)
	if !strings.Contains(out, "close modal") {
		t.Errorf("footer lost on a short viewport:\n%s", out)
	}
	if strings.Contains(out, "CONFIGURATION") {
		t.Errorf("configuration should yield before the footer on a short viewport:\n%s", out)
	}
}

// The configuration pane follows the highlight, so the instruction always
// describes the provider the cursor is on.
func TestConnectDetailsFollowHighlight(t *testing.T) {
	view := connectViewFixture()
	view.Cursor = 1 // groq, missing credential
	out := connectModalCard(view, 92, 0)
	if !strings.Contains(out, "Missing GROQ_API_KEY or config providers.groq.api_key") {
		t.Errorf("missing-credential instruction absent:\n%s", out)
	}
	if !strings.Contains(out, "[×] missing credential") {
		t.Errorf("status pane did not name the missing credential:\n%s", out)
	}
}

// The setup hint turns a missing credential into the one actionable line,
// matching the house wording.
func TestConnectSetupHintForMissingCredential(t *testing.T) {
	got := connectSetupHint(ProviderRow{ID: "anthropic", KeyDetail: "missing: set ANTHROPIC_API_KEY"})
	want := "Missing ANTHROPIC_API_KEY or config providers.anthropic.api_key"
	if got != want {
		t.Errorf("connectSetupHint = %q, want %q", got, want)
	}
}

// /connect opens the Provider Manager, not the flat picker, and the composer
// hides behind it.
func TestConnectCommandOpensProviderManager(t *testing.T) {
	p := connectProgram(t, connectViewFixture().Providers)
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	p.state.Apply(InputChanged{Text: "typing"})

	Command{Name: "connect"}.runOn(p)

	if p.state.ConnectModal == nil {
		t.Fatal("/connect did not open the Provider Manager")
	}
	if p.state.Picker != nil {
		t.Error("the Provider Manager must not fall back to the flat picker")
	}
	out := stripANSI(Render(p.state))
	if strings.Contains(out, "typing") {
		t.Errorf("composer text rendered under the modal:\n%s", out)
	}
	if !strings.Contains(out, "PROVIDER MANAGER") {
		t.Errorf("modal frame missing:\n%s", out)
	}
}

// Esc and q dismiss the manager without acting.
func TestConnectManagerCloses(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, runes("q")} {
		p := connectProgram(t, connectViewFixture().Providers)
		Command{Name: "connect"}.runOn(p)
		if p.state.ConnectModal == nil {
			t.Fatal("no manager opened")
		}
		keyPress(p, key)
		if p.state.ConnectModal != nil {
			t.Errorf("key %q did not close the manager", key.String())
		}
	}
}

// Enter opens the API-key modal for the highlighted provider and closes the
// manager, so choosing a provider advances the flow rather than trapping the
// user in the browse surface.
func TestConnectManagerEnterOpensKeyModal(t *testing.T) {
	p := connectProgram(t, connectViewFixture().Providers)
	Command{Name: "connect"}.runOn(p)
	if p.state.ConnectModal == nil {
		t.Fatal("no manager opened")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.ConnectModal != nil {
		t.Error("the manager stayed open after choosing a provider")
	}
	if p.state.ConnectKey == nil {
		t.Fatal("no API-key modal after choosing a provider")
	}
	if p.state.ConnectKey.Provider.ID != "anthropic" {
		t.Errorf("key modal provider = %q, want the highlighted provider", p.state.ConnectKey.Provider.ID)
	}
	if p.flow == nil || p.flow.provider != "anthropic" {
		t.Errorf("flow = %+v, want the highlighted provider", p.flow)
	}
}

// Navigation wraps at both ends and keeps the highlight inside the roster.
func TestConnectManagerNavigationWraps(t *testing.T) {
	s := New()
	s.Apply(ConnectModalOpened{View: connectViewFixture()})
	s.ConnectModal.VisibleRows = 2
	s.ConnectModal.EnsureVisible()

	s.Apply(ConnectModalMoved{Delta: -1}) // 0 -> wraps to 2
	if s.ConnectModal.Cursor != 2 {
		t.Fatalf("cursor = %d after wrap up, want 2", s.ConnectModal.Cursor)
	}
	s.Apply(ConnectModalMoved{Delta: 1}) // 2 -> wraps to 0
	if s.ConnectModal.Cursor != 0 {
		t.Fatalf("cursor = %d after wrap down, want 0", s.ConnectModal.Cursor)
	}
	s.Apply(ConnectModalBottom{})
	if s.ConnectModal.Cursor != 2 {
		t.Errorf("bottom = cursor %d, want 2", s.ConnectModal.Cursor)
	}
	if end := s.ConnectModal.Offset + 2; end < len(s.ConnectModal.Providers) {
		t.Errorf("offset %d leaves the last row hidden", s.ConnectModal.Offset)
	}
	s.Apply(ConnectModalTop{})
	if s.ConnectModal.Cursor != 0 || s.ConnectModal.Offset != 0 {
		t.Errorf("top = cursor %d offset %d, want 0/0", s.ConnectModal.Cursor, s.ConnectModal.Offset)
	}
}

// Paging moves by a viewport without wrapping past an end.
func TestConnectManagerPagingStopsAtEnds(t *testing.T) {
	s := New()
	s.Apply(ConnectModalOpened{View: connectViewFixture()})
	s.Apply(ConnectModalPaged{Delta: 1})
	if s.ConnectModal.Cursor != 2 {
		t.Errorf("page down = cursor %d, want the last row", s.ConnectModal.Cursor)
	}
	s.Apply(ConnectModalPaged{Delta: 1})
	if s.ConnectModal.Cursor != 2 {
		t.Errorf("page down past the end = cursor %d, want it parked", s.ConnectModal.Cursor)
	}
}

// A missing producer reports instead of opening an empty manager.
func TestConnectManagerWithoutProvidersReports(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "connect"}.runOn(p)
	if p.state.ConnectModal != nil {
		t.Error("a missing provider hook opened a manager")
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "guided setup is unavailable") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the unavailable notice", p.state.Entries)
	}
}
