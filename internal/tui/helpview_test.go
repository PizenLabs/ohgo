package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Both listing commands must open the same surface, and neither may print the
// old plain-text registry into the transcript.
func TestHelpAndCommandsOpenTheSameModal(t *testing.T) {
	for _, name := range []string{"help", "commands"} {
		p := NewProgram(nil, "test", 0)
		Command{Name: name}.runOn(p)
		if p.state.HelpModal == nil {
			t.Fatalf("/%s did not open the command & help center", name)
		}
		for _, entry := range p.state.Entries {
			if entry.Kind == EntryNotice {
				t.Errorf("/%s polluted the transcript with a notice: %q", name, entry.Notice)
			}
		}
	}
}

// The roster lists both categories with their headers and columns, and it never
// surfaces a hidden alias dispatch would reject as a discovery path.
func TestHelpCenterListsCategorizedRoster(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	out := ansi.Strip(RenderHelpCenter(view, DarkPalette(), 96, 60))
	for _, want := range []string{
		"COMMAND & HELP CENTER", "Search commands…",
		"COMMAND", "CATEGORY", "DESCRIPTION",
		CommandGroupWorkspace, CommandGroupControl,
		"/ask", "/fuzz", "/build", "/help", "/commands", "/permissions", "/theme",
		"SELECTED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help center omits %q:\n%s", want, out)
		}
	}
	for _, hidden := range []string{"/models ", "/mode ", "/resume ", "/quit ", "/exit "} {
		if strings.Contains(out, hidden) {
			t.Errorf("hidden alias %q leaked into the help center:\n%s", hidden, out)
		}
	}
}

// The tab strip marks the active view with a glyph, not colour alone, and the
// search label follows the active tab.
func TestHelpCenterTabStripMarksActiveView(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	commands := ansi.Strip(RenderHelpCenter(view, DarkPalette(), 96, 60))
	if !strings.Contains(commands, "● Commands") || !strings.Contains(commands, "○ Shortcuts") {
		t.Errorf("commands tab strip = %q, want Commands marked active", helpHead(commands, 4))
	}
	if !strings.Contains(commands, "Search commands…") {
		t.Error("commands tab lost its search label")
	}

	view.SwitchView(1)
	shortcuts := ansi.Strip(RenderHelpCenter(view, DarkPalette(), 96, 60))
	if !strings.Contains(shortcuts, "○ Commands") || !strings.Contains(shortcuts, "● Shortcuts") {
		t.Errorf("shortcuts tab strip = %q, want Shortcuts marked active", helpHead(shortcuts, 4))
	}
	if !strings.Contains(shortcuts, "Search shortcuts…") {
		t.Error("shortcuts tab lost its search label")
	}
}

// The Shortcuts tab lists the keybinding registry under its three categories
// with the KEY / BINDING column name.
func TestHelpCenterShortcutsRoster(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	view.SwitchView(1)
	out := ansi.Strip(RenderHelpCenter(view, DarkPalette(), 96, 80))
	for _, want := range []string{
		"KEY / BINDING", "CATEGORY", "DESCRIPTION",
		ShortcutGroupNavigation, ShortcutGroupSession, ShortcutGroupWorkspace,
		"Ctrl+S", "$sup", "Ctrl+O", "Tab / Shift+Tab", "Esc / q", "Ctrl+D", "Alt+Enter / Ctrl+J",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shortcuts roster omits %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/ask") || strings.Contains(out, "/theme") {
		t.Errorf("shortcuts roster leaked command rows:\n%s", out)
	}
}

// Every keybinding advertised in the Shortcuts tab must be non-empty and
// categorized, so the roster can never render a blank row or fall outside a
// group header.
func TestKeybindingsAreWellFormed(t *testing.T) {
	if len(Keybindings) == 0 {
		t.Fatal("keybinding registry is empty")
	}
	valid := map[string]bool{
		ShortcutGroupNavigation: true,
		ShortcutGroupSession:    true,
		ShortcutGroupWorkspace:  true,
	}
	for _, spec := range Keybindings {
		if strings.TrimSpace(spec.Keys) == "" {
			t.Errorf("keybinding with empty keys: %+v", spec)
		}
		if strings.TrimSpace(spec.Description) == "" {
			t.Errorf("keybinding %q has no description", spec.Keys)
		}
		if !valid[spec.Category] {
			t.Errorf("keybinding %q has unknown category %q", spec.Keys, spec.Category)
		}
	}
}

// Filtering the Shortcuts tab matches the binding and its description, exactly
// like the Commands tab.
func TestHelpCenterShortcutFilter(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	view.SwitchView(1)
	view.SetFilter("support")
	names := map[string]bool{}
	for _, item := range view.ActiveItems() {
		names[item.Name] = true
	}
	if !names["Ctrl+S"] || !names["$sup"] {
		t.Errorf("filtered = %v, want the support bindings", names)
	}
	view.SetFilter("zzz-nope")
	if got := view.ActiveItems(); len(got) != 0 {
		t.Errorf("filtered = %+v, want no matches", got)
	}
}

// Each tab keeps its own query and highlight, so toggling views never discards
// the other tab's search.
func TestHelpCenterTabsKeepTheirOwnFilter(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "help"}.runOn(p)
	p.Update(runes("mod"))
	if got := p.state.HelpModal.Command.Filter; got != "mod" {
		t.Fatalf("commands filter = %q, want typed text", got)
	}
	p.Update(keyMsg(tea.KeyTab))
	if p.state.HelpModal.View != HelpViewShortcuts {
		t.Fatalf("view = %v, want shortcuts", p.state.HelpModal.View)
	}
	if got := p.state.HelpModal.Shortcut.Filter; got != "" {
		t.Errorf("shortcuts filter = %q, want empty on first visit", got)
	}
	if len(p.state.HelpModal.ActiveItems()) == 0 {
		t.Error("shortcuts tab is empty on first visit")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if p.state.HelpModal.View != HelpViewCommands {
		t.Fatalf("shift+tab view = %v, want commands", p.state.HelpModal.View)
	}
	if got := p.state.HelpModal.Command.Filter; got != "mod" {
		t.Errorf("commands filter = %q, want the preserved query", got)
	}
	// shift+tab wraps the other way too, from the first tab to the last.
	p.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if p.state.HelpModal.View != HelpViewShortcuts {
		t.Fatalf("wrapped shift+tab view = %v, want shortcuts", p.state.HelpModal.View)
	}
}

// Enter on a shortcut is inert: there is nothing to run, so the modal stays
// open for browsing. Enter on a command still runs it.
func TestHelpCenterEnterOnShortcutIsInert(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "help"}.runOn(p)
	p.Update(keyMsg(tea.KeyTab))
	p.Update(keyMsg(tea.KeyEnter))
	if p.state.HelpModal == nil {
		t.Fatal("enter on a shortcut closed the modal")
	}
	if p.state.HelpModal.View != HelpViewShortcuts {
		t.Errorf("view = %v, want to stay on shortcuts", p.state.HelpModal.View)
	}
}

// The search box matches the description and usage, not only the name, so a
// user who remembers what a command does can still find it.
func TestHelpCenterFilterMatchesDescription(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	view.SetFilter("summar")
	got := view.ActiveItems()
	if len(got) != 1 || got[0].Name != "compact" {
		t.Errorf("filtered = %+v, want only /compact", got)
	}
	// Usage text is searchable too, so a remembered argument shape finds the
	// command it belongs to.
	view.SetFilter("provider/id")
	got = view.ActiveItems()
	if len(got) != 1 || got[0].Name != "model" {
		t.Errorf("filtered = %+v, want only /model", got)
	}
	view.SetFilter("zzz-nope")
	if got := view.ActiveItems(); len(got) != 0 {
		t.Errorf("filtered = %+v, want no matches", got)
	}
}

// Navigation wraps at both ends and paging parks at the ends rather than
// wrapping, matching every other ohgo roster.
func TestHelpCenterNavigationWrapsAndPagingClamps(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	n := len(view.ActiveItems())
	if n < 3 {
		t.Fatalf("registry has %d visible commands, need at least 3", n)
	}
	view.Move(-1)
	if view.Command.Cursor != n-1 {
		t.Errorf("cursor = %d after wrap up, want %d", view.Command.Cursor, n-1)
	}
	view.Move(1)
	if view.Command.Cursor != 0 {
		t.Errorf("cursor = %d after wrap down, want 0", view.Command.Cursor)
	}
	view.VisibleRows = 2
	view.Page(1)
	if view.Command.Cursor != 2 {
		t.Errorf("cursor = %d after page down, want one viewport (2)", view.Command.Cursor)
	}
	view.Page(-1)
	if view.Command.Cursor != 0 {
		t.Errorf("cursor = %d after page up, want the first row", view.Command.Cursor)
	}
	// Paging past the end parks at the end rather than wrapping.
	view.Command.Cursor = n - 1
	view.Page(1)
	if view.Command.Cursor != n-1 {
		t.Errorf("cursor = %d after paging past the end, want %d", view.Command.Cursor, n-1)
	}
	view.Bottom()
	if view.Command.Cursor != n-1 {
		t.Errorf("bottom cursor = %d, want %d", view.Command.Cursor, n-1)
	}
}

// A bounded roster keeps the highlight on a rendered line, never scrolled off.
func TestHelpCenterWindowKeepsHighlightVisible(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	n := len(view.ActiveItems())
	view.VisibleRows = 4
	view.Command.Cursor = n - 1
	shown := view.window()
	if len(shown) == 0 {
		t.Fatal("window is empty")
	}
	found := false
	for _, row := range shown {
		if row.Header == "" && row.Index == view.Command.Cursor {
			found = true
		}
	}
	if !found {
		t.Errorf("highlighted row %d is outside the %d-row window", view.Command.Cursor, len(shown))
	}
}

// The highlighted row is carried by the green cursor and the shared UserBG
// tint together, so a monochrome terminal still shows which row Enter runs.
// Colour is not emitted on a headless renderer, so the contract asserted here is
// the structural one: the cursor glyph, the full-width span the tint covers, and
// that the highlighted row renders differently from its neighbour.
func TestHelpCenterHighlightedRowIsTinted(t *testing.T) {
	pal := DarkPalette()
	view := NewHelpCenter(filterCommands(""))
	view.Command.Cursor = 0
	rows := view.rows(view.ActiveItems())
	if len(rows) < 2 || rows[0].Header == "" {
		t.Fatalf("expected a group header before the first command, got %+v", rows)
	}
	catW := helpCategoryWidth(allActiveItems(view))
	selected := renderHelpRow(rows[1], view, pal, 80, catW)
	unselectedView := view
	unselectedView.Command.Cursor = 1
	unselected := renderHelpRow(rows[1], unselectedView, pal, 80, catW)

	if !strings.HasPrefix(ansi.Strip(selected), "> ") {
		t.Errorf("selected row = %q, want the \"> \" cursor", ansi.Strip(selected))
	}
	if strings.HasPrefix(ansi.Strip(unselected), "> ") {
		t.Errorf("unselected row carries the cursor: %q", ansi.Strip(unselected))
	}
	if got := CellWidth(selected); got != 80 {
		t.Errorf("selected row width = %d, want the full interior 80 so the tint spans it", got)
	}
	if unselected == selected {
		t.Error("the highlighted row rendered identically to its neighbour")
	}
}

// Distinct categories carry distinct badge accents so the roster groups read
// apart without parsing the header text.
func TestHelpCategoryAccentsDiffer(t *testing.T) {
	pairs := [][2]string{
		{CommandGroupWorkspace, CommandGroupControl},
		{ShortcutGroupNavigation, ShortcutGroupSession},
		{ShortcutGroupSession, ShortcutGroupWorkspace},
		{ShortcutGroupWorkspace, CommandGroupControl},
	}
	for _, pair := range pairs {
		if helpCategoryAccent(pair[0]) == helpCategoryAccent(pair[1]) {
			t.Errorf("categories %q and %q share a badge accent", pair[0], pair[1])
		}
	}
}

// The footer names the keys that actually work in the modal, including the tab
// switch.
func TestHelpCenterFooterNamesKeys(t *testing.T) {
	foot := ansi.Strip(helpFooterBar(DarkPalette(), 96))
	for _, want := range []string{"⇥", "tab view", "↑↓", "navigate", "type to filter", "Esc/q", "close"} {
		if !strings.Contains(foot, want) {
			t.Errorf("footer = %q, want %q", foot, want)
		}
	}
}

// esc clears a non-empty query first and closes only when it is already empty;
// q closes from an empty query but stays a query character while filtering.
func TestHelpCenterCloseContract(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "help"}.runOn(p)
	p.Update(runes("th"))
	if p.state.HelpModal.Command.Filter != "th" {
		t.Fatalf("filter = %q, want typed text", p.state.HelpModal.Command.Filter)
	}
	// q while filtering is a query character, not a close.
	p.Update(runes("q"))
	if p.state.HelpModal == nil || p.state.HelpModal.Command.Filter != "thq" {
		t.Fatalf("q closed or mis-typed while filtering: %+v", p.state.HelpModal)
	}
	p.Update(keyMsg(tea.KeyEsc))
	if p.state.HelpModal == nil || p.state.HelpModal.Command.Filter != "" {
		t.Fatalf("esc did not clear the query first: %+v", p.state.HelpModal)
	}
	p.Update(keyMsg(tea.KeyEsc))
	if p.state.HelpModal != nil {
		t.Fatal("second esc did not close the modal")
	}

	// q closes from an empty query.
	p2 := NewProgram(nil, "test", 0)
	Command{Name: "help"}.runOn(p2)
	p2.Update(runes("q"))
	if p2.state.HelpModal != nil {
		t.Fatal("q from an empty query did not close the modal")
	}
}

// Enter runs the highlighted command and dismisses the modal, matching the
// direct invocation of the same command.
func TestHelpCenterEnterRunsHighlightedCommand(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.history = append(p.history, agent.UserMessage{Text: "stale"})
	Command{Name: "help"}.runOn(p)
	p.Update(runes("clear"))
	p.Update(keyMsg(tea.KeyEnter))
	if p.state.HelpModal != nil {
		t.Fatal("enter did not close the modal")
	}
	if len(p.history) != 0 {
		t.Errorf("history = %d messages, want /clear to have run", len(p.history))
	}
}

// The listing commands are inert inside the modal: selecting them would reopen
// the surface the user is already looking at.
func TestHelpCenterEnterOnListingCommandCloses(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "help"}.runOn(p)
	p.Update(runes("commands"))
	p.Update(keyMsg(tea.KeyEnter))
	if p.state.HelpModal != nil {
		t.Fatal("selecting /commands reopened the modal instead of closing")
	}
}

// The modal fills the conversation budget exactly at every tier and on both
// tabs, so a resize cannot short the frame or float the footer.
func TestHelpCenterFillsExactHeight(t *testing.T) {
	for _, size := range [][2]int{{44, 12}, {80, 24}, {120, 40}} {
		w, h := size[0], size[1]
		for _, view := range []HelpView{HelpViewCommands, HelpViewShortcuts} {
			s := New()
			s.Apply(TerminalResized{Width: w, Height: h})
			modal := NewHelpCenter(filterCommands(""))
			modal.View = view
			s.Apply(HelpCenterOpened{View: modal})
			if got := len(strings.Split(Render(s), "\n")); got != h {
				t.Errorf("help center view=%v %dx%d rendered %d lines, want exactly %d", view, w, h, got, h)
			}
		}
	}
}

// A narrow dialog sheds the category column rather than overlapping it, and no
// line exceeds the terminal width.
func TestHelpCenterShedsCategoryColumnWhenNarrow(t *testing.T) {
	view := NewHelpCenter(filterCommands(""))
	wide := ansi.Strip(RenderHelpCenter(view, DarkPalette(), 96, 40))
	narrow := ansi.Strip(RenderHelpCenter(view, DarkPalette(), 40, 40))
	if !strings.Contains(wide, "CATEGORY") {
		t.Error("wide dialog lost the category column")
	}
	if strings.Contains(narrow, "CATEGORY") {
		t.Error("narrow dialog kept a column it cannot fit")
	}
	for _, line := range strings.Split(narrow, "\n") {
		if got := CellWidth(line); got > 40 {
			t.Errorf("narrow line width %d exceeds 40: %q", got, line)
		}
	}
}

// The search bar's position marker counts rows, not the group headers between
// them, so a scrolled roster reports the range the user is actually looking at.
func TestHelpCenterRangeIndicatorCountsRows(t *testing.T) {
	items := []CommandSpec{
		{Name: "aa", Usage: "aa", Description: "first", Category: CommandGroupWorkspace},
		{Name: "bb", Usage: "bb", Description: "second", Category: CommandGroupWorkspace},
		{Name: "cc", Usage: "cc", Description: "third", Category: CommandGroupWorkspace},
		{Name: "dd", Usage: "dd", Description: "fourth", Category: CommandGroupWorkspace},
	}
	view := NewHelpCenter(items)
	// A header plus two of the four commands: the window scrolls to keep the
	// last command visible.
	view.VisibleRows = 3
	view.Command.Cursor = 3
	line := ansi.Strip(helpSearchLine(view, DarkPalette(), 96))
	if !strings.Contains(line, "[2–4/4]") {
		t.Errorf("range = %q, want the command range [2–4/4]", line)
	}
	// Everything visible: the plain total, no range.
	view.VisibleRows = 0
	if line := ansi.Strip(helpSearchLine(view, DarkPalette(), 96)); !strings.Contains(line, "[4]") {
		t.Errorf("count = %q, want [4]", line)
	}
}

// helpHead returns the head of a rendered block for a compact failure message.
func helpHead(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
