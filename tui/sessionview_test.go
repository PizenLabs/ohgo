package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/session"
	tea "github.com/charmbracelet/bubbletea"
)

// sessionManagerFixture is a populated roster for the render tests.
func sessionManagerFixture() SessionManagerState {
	return SessionManagerState{
		Items: []SessionItem{
			{ID: 12, Title: "Refactor the session modal", Model: "openrouter/anthropic/claude", Updated: "2026-10-17 12:51", Tokens: 3400, Prompt: "make the modal centered", Active: true, Dirty: true, DirtyKnown: true},
			{ID: 11, Title: "", Model: "groq/llama-3", Updated: "2026-10-16 09:02", Tokens: 0, Prompt: "", DirtyKnown: false},
			{ID: 10, Title: "Fix parser", Model: "ollama/qwen", Updated: "2026-10-15 18:30", Tokens: 900, Prompt: "parser fails on empty input", Dirty: false, DirtyKnown: true},
		},
		Cursor:      2,
		ActiveID:    12,
		VisibleRows: 5,
	}
}

// sessionManagerCard returns the plain dialog for a size, with ANSI stripped.
func sessionManagerCard(view SessionManagerState, width, height int) string {
	return stripANSI(RenderSessionManager(view, DarkPalette(), width, height))
}

// sessionManagerLines is the plain dialog split into rows.
func sessionManagerLines(view SessionManagerState, width, height int) []string {
	return strings.Split(sessionManagerCard(view, width, height), "\n")
}

// rosterProgram builds a program with a fixed roster and, when actions is
// non-zero, the write hooks installed. The caller can mutate items between
// calls to simulate a store that changed.
func rosterProgram(t *testing.T, list func() ([]SessionItem, error), actions SessionActions) *Program {
	t.Helper()
	p := NewProgram(nil, "test", 0)
	p.SetSessionBrowser(list, func(id int64) (*session.Session, OpenedSession, error) {
		return &session.Session{ID: id}, OpenedSession{}, nil
	})
	p.SetSessionActions(actions)
	return p
}

// The dialog carries the border-embedded title, both sections, the table
// columns, the active marker, the selected row's details, and the action bar.
func TestRenderSessionManagerShowsTableAndDetails(t *testing.T) {
	lines := sessionManagerLines(sessionManagerFixture(), 90, 0)
	out := strings.Join(lines, "\n")

	for _, want := range []string{
		"SESSION MANAGER",
		"SESSIONS", "DETAILS",
		"STATUS", "TITLE / GOAL", "DIRTY", "LAST ACTIVE", "SLOT",
		"#12", "#11", "#10",
		"Refactor the session modal",
		"Fix parser",
		"~900 tokens", "ollama/qwen", "parser fails on empty input",
		"resume", "new", "rename", "archive", "delete", "close",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dialog missing %q:\n%s", want, out)
		}
	}

	// The title rides the top border, not a body row.
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") || !strings.Contains(lines[0], "SESSION MANAGER") {
		t.Errorf("title is not embedded in the top border: %q", lines[0])
	}

	// The loaded session carries the filled STATUS marker; the highlighted row
	// carries the cursor. They are independent facts.
	activeRow := ""
	selectedRow := ""
	for _, line := range lines {
		if strings.Contains(line, "#12") {
			activeRow = line
		}
		if strings.HasPrefix(line, "│> ") {
			selectedRow = line
		}
	}
	if !strings.Contains(activeRow, "●") {
		t.Errorf("active session row has no filled marker: %q", activeRow)
	}
	if !strings.Contains(selectedRow, "Fix parser") {
		t.Errorf("cursor is not on the selected row: %q", selectedRow)
	}
}

// The frame is rectangular: every row is exactly the dialog width, so the
// borders land in one column.
func TestRenderSessionManagerFrameAlignment(t *testing.T) {
	for _, width := range []int{50, 90} {
		lines := sessionManagerLines(sessionManagerFixture(), width, 0)
		want := CellWidth(lines[0])
		for i, line := range lines {
			if got := CellWidth(line); got != want {
				t.Errorf("width %d row %d is %d cells, want %d: %q", width, i, got, want, line)
			}
		}
	}
}

// A short viewport keeps the frame and footer intact rather than overflowing.
func TestRenderSessionManagerKeepsFooterWhenShort(t *testing.T) {
	out := sessionManagerCard(sessionManagerFixture(), 90, 8)
	if !strings.Contains(out, "resume") {
		t.Errorf("footer lost on a short viewport:\n%s", out)
	}
	if strings.Contains(out, "DETAILS") {
		t.Errorf("details should yield before the footer on a short viewport:\n%s", out)
	}
}

// The manager owns the screen: the composer and its hints hide behind it.
func TestSessionManagerOwnsTheScreen(t *testing.T) {
	p := rosterProgram(t, func() ([]SessionItem, error) {
		return []SessionItem{{ID: 7, Title: "one", Model: "m"}}, nil
	}, SessionActions{})
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	p.state.Apply(InputChanged{Text: "typing"})

	Command{Name: "sessions"}.runOn(p)

	if p.state.SessionModal == nil {
		t.Fatal("/sessions did not open the manager")
	}
	out := stripANSI(Render(p.state))
	if strings.Contains(out, "typing") {
		t.Errorf("composer text rendered under the manager:\n%s", out)
	}
	if !strings.Contains(out, "SESSION MANAGER") {
		t.Errorf("manager frame missing:\n%s", out)
	}
}

// /sessions opens the manager; an empty store reports instead of opening an
// empty dialog.
func TestSessionManagerEmptyStoreReports(t *testing.T) {
	p := rosterProgram(t, func() ([]SessionItem, error) { return nil, nil }, SessionActions{})
	Command{Name: "sessions"}.runOn(p)
	if p.state.SessionModal != nil {
		t.Error("an empty roster opened a dialog")
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "no saved sessions") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the empty-roster notice", p.state.Entries)
	}
}

// Navigation wraps at both ends and keeps the highlight inside the roster.
func TestSessionManagerNavigationWraps(t *testing.T) {
	s := New()
	s.Apply(SessionManagerOpened{View: sessionManagerFixture()})
	s.SessionModal.VisibleRows = 2
	s.SessionModal.EnsureVisible()

	s.Apply(SessionManagerMoved{Delta: 1}) // 2 -> wraps to 0
	if s.SessionModal.Cursor != 0 {
		t.Fatalf("cursor = %d after wrap down, want 0", s.SessionModal.Cursor)
	}
	s.Apply(SessionManagerMoved{Delta: -1}) // 0 -> wraps to 2
	if s.SessionModal.Cursor != 2 {
		t.Fatalf("cursor = %d after wrap up, want 2", s.SessionModal.Cursor)
	}
	s.Apply(SessionManagerTop{})
	if s.SessionModal.Cursor != 0 || s.SessionModal.Offset != 0 {
		t.Errorf("top = cursor %d offset %d, want 0/0", s.SessionModal.Cursor, s.SessionModal.Offset)
	}
	s.Apply(SessionManagerBottom{})
	if s.SessionModal.Cursor != 2 {
		t.Errorf("bottom = cursor %d, want 2", s.SessionModal.Cursor)
	}
	// With a two-row window the highlight is scrolled into view.
	if end := s.SessionModal.Offset + 2; end < len(s.SessionModal.Items) {
		t.Errorf("offset %d leaves the last row hidden", s.SessionModal.Offset)
	}
}

// Enter resumes the highlighted session and closes the manager.
func TestSessionManagerEnterResumes(t *testing.T) {
	var opened int64
	p := NewProgram(nil, "test", 0)
	p.SetSessionBrowser(
		func() ([]SessionItem, error) {
			return []SessionItem{{ID: 1, Title: "first"}, {ID: 2, Title: "second"}}, nil
		},
		func(id int64) (*session.Session, OpenedSession, error) {
			opened = id
			return &session.Session{ID: id}, OpenedSession{
				History: []agent.Message{agent.UserMessage{Text: "old"}},
				Entries: []Entry{{Kind: EntryUser, UserText: "old"}},
				Notice:  "resumed",
			}, nil
		},
	)
	Command{Name: "sessions"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown}) // select the second
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})

	if opened != 2 {
		t.Errorf("resumed id = %d, want the highlighted 2", opened)
	}
	if p.state.SessionModal != nil {
		t.Error("manager stayed open after resuming")
	}
	if len(p.state.Entries) != 2 {
		t.Errorf("entries = %+v, want replay plus notice", p.state.Entries)
	}
}

// Esc, q, and ctrl+c all dismiss without resuming.
func TestSessionManagerCloses(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyCtrlC},
	} {
		p := rosterProgram(t, func() ([]SessionItem, error) {
			return []SessionItem{{ID: 1, Title: "one"}}, nil
		}, SessionActions{})
		Command{Name: "sessions"}.runOn(p)
		keyPress(p, key)
		if p.state.SessionModal != nil {
			t.Errorf("key %q did not close the manager", key.String())
		}
	}
}

// Rename is an inline editor: it seeds the buffer, commits through the hook,
// and re-lists rather than trusting the local value.
func TestSessionManagerRename(t *testing.T) {
	var gotID int64
	var gotTitle string
	p := rosterProgram(t, func() ([]SessionItem, error) {
		return []SessionItem{{ID: 5, Title: "old title"}}, nil
	}, SessionActions{
		Rename: func(id int64, title string) error {
			gotID, gotTitle = id, title
			return nil
		},
	})
	Command{Name: "sessions"}.runOn(p)

	keyPress(p, runes("r"))
	if p.state.SessionModal.Mode != SessionRename {
		t.Fatalf("mode = %v, want rename", p.state.SessionModal.Mode)
	}
	if p.state.SessionModal.Rename != "old title" {
		t.Errorf("rename buffer = %q, want the seeded title", p.state.SessionModal.Rename)
	}
	// Replace the buffer and commit.
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlU})
	keyPress(p, runes("new"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})

	if gotID != 5 || gotTitle != "new" {
		t.Errorf("rename hook got (%d, %q), want (5, \"new\")", gotID, gotTitle)
	}
	if p.state.SessionModal.Mode != SessionBrowse {
		t.Errorf("mode = %v, want browse after commit", p.state.SessionModal.Mode)
	}
	if !strings.Contains(p.state.SessionModal.Notice, "renamed") {
		t.Errorf("notice = %q, want a rename confirmation", p.state.SessionModal.Notice)
	}
}

// Esc abandons a rename without calling the hook.
func TestSessionManagerRenameCancel(t *testing.T) {
	called := false
	p := rosterProgram(t, func() ([]SessionItem, error) {
		return []SessionItem{{ID: 5, Title: "keep"}}, nil
	}, SessionActions{Rename: func(int64, string) error { called = true; return nil }})
	Command{Name: "sessions"}.runOn(p)

	keyPress(p, runes("r"))
	keyPress(p, runes("X"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if called {
		t.Error("cancel called the rename hook")
	}
	if p.state.SessionModal.Mode != SessionBrowse {
		t.Errorf("mode = %v, want browse after cancel", p.state.SessionModal.Mode)
	}
}

// Delete is gated behind a confirmation: d asks, y acts, n cancels.
func TestSessionManagerDeleteConfirms(t *testing.T) {
	var deleted []int64
	p := rosterProgram(t, func() ([]SessionItem, error) {
		return []SessionItem{{ID: 5, Title: "gone"}, {ID: 6, Title: "stays"}}, nil
	}, SessionActions{Delete: func(id int64) error { deleted = append(deleted, id); return nil }})
	Command{Name: "sessions"}.runOn(p)

	keyPress(p, runes("d"))
	if p.state.SessionModal.Mode != SessionConfirm || p.state.SessionModal.ConfirmID != 5 {
		t.Fatalf("delete asked mode=%v id=%d, want confirm/5", p.state.SessionModal.Mode, p.state.SessionModal.ConfirmID)
	}
	keyPress(p, runes("y"))
	if len(deleted) != 1 || deleted[0] != 5 {
		t.Errorf("deleted = %v, want [5]", deleted)
	}

	// n cancels without touching the store.
	keyPress(p, runes("d"))
	keyPress(p, runes("n"))
	if len(deleted) != 1 {
		t.Errorf("cancel deleted %v", deleted)
	}
	if p.state.SessionModal.Mode != SessionBrowse {
		t.Errorf("mode = %v, want browse after cancel", p.state.SessionModal.Mode)
	}
}

// Archive hides a session through its hook and re-lists.
func TestSessionManagerArchive(t *testing.T) {
	var archived int64
	p := rosterProgram(t, func() ([]SessionItem, error) {
		return []SessionItem{{ID: 9, Title: "nine"}}, nil
	}, SessionActions{Archive: func(id int64) error { archived = id; return nil }})
	Command{Name: "sessions"}.runOn(p)

	keyPress(p, runes("a"))
	if archived != 9 {
		t.Errorf("archived = %d, want 9", archived)
	}
	if !strings.Contains(p.state.SessionModal.Notice, "archived") {
		t.Errorf("notice = %q, want an archive confirmation", p.state.SessionModal.Notice)
	}
}

// New switches the program to a fresh session and closes the manager.
func TestSessionManagerNew(t *testing.T) {
	p := rosterProgram(t, func() ([]SessionItem, error) {
		return []SessionItem{{ID: 1, Title: "one"}}, nil
	}, SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			return &session.Session{ID: 42}, OpenedSession{Notice: "started new session 42"}, nil
		},
	})
	Command{Name: "sessions"}.runOn(p)

	keyPress(p, runes("n"))
	if p.state.SessionModal != nil {
		t.Error("manager stayed open after creating a session")
	}
	if p.session == nil || p.session.ID != 42 {
		t.Errorf("session = %+v, want the new session 42", p.session)
	}
}

// An unwired write action reports itself unavailable instead of failing
// silently or panicking. Rename and delete report on commit, since the key
// itself only opens their editor or confirmation.
func TestSessionManagerActionsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		keys []tea.KeyMsg
	}{
		{"rename", []tea.KeyMsg{runes("r"), {Type: tea.KeyEnter}}},
		{"delete", []tea.KeyMsg{runes("d"), runes("y")}},
		{"archive", []tea.KeyMsg{runes("a")}},
		{"new", []tea.KeyMsg{runes("n")}},
	}
	for _, tc := range cases {
		p := rosterProgram(t, func() ([]SessionItem, error) {
			return []SessionItem{{ID: 1, Title: "one"}}, nil
		}, SessionActions{})
		Command{Name: "sessions"}.runOn(p)
		for _, key := range tc.keys {
			keyPress(p, key)
		}
		if p.state.SessionModal == nil {
			t.Fatalf("%s closed the manager unexpectedly", tc.name)
		}
		if !strings.Contains(p.state.SessionModal.Notice, "unavailable") {
			t.Errorf("%s notice = %q, want an unavailable report", tc.name, p.state.SessionModal.Notice)
		}
	}
}

// Apply is the only transition path: opening installs the snapshot, and
// /clear drops a manager caught mid-session.
func TestSessionManagerStateTransitions(t *testing.T) {
	s := New()
	s.Apply(SessionManagerOpened{View: sessionManagerFixture()})
	if s.SessionModal == nil || len(s.SessionModal.Items) != 3 {
		t.Fatalf("SessionModal = %+v, want the installed snapshot", s.SessionModal)
	}
	s.Apply(Cleared{})
	if s.SessionModal != nil {
		t.Error("/clear left the manager open")
	}
	s.Apply(SessionManagerOpened{View: sessionManagerFixture()})
	s.Apply(SessionManagerClosed{})
	if s.SessionModal != nil {
		t.Error("close did not clear the manager")
	}
}

// A reload preserves the highlight by id when the selected session survives.
func TestSessionManagerReloadKeepsSelection(t *testing.T) {
	s := New()
	s.Apply(SessionManagerOpened{View: sessionManagerFixture()})
	// Select #11 (index 1).
	s.Apply(SessionManagerMoved{Delta: -1})
	if got, _ := s.SessionModal.Selected(); got.ID != 11 {
		t.Fatalf("selected = %d, want 11", got.ID)
	}
	// Re-list newest-first with an item prepended: the index shifts, so only
	// an id-preserving reload keeps #11 highlighted.
	s.Apply(SessionManagerReloaded{Items: []SessionItem{
		{ID: 99, Title: "newest"},
		{ID: 12, Title: "a"},
		{ID: 11, Title: "b"},
		{ID: 10, Title: "c"},
	}, Notice: "reloaded"})
	if got, _ := s.SessionModal.Selected(); got.ID != 11 {
		t.Errorf("selected = %d after reload, want 11", got.ID)
	}
	if s.SessionModal.Notice != "reloaded" {
		t.Errorf("notice = %q, want the reload notice", s.SessionModal.Notice)
	}
}
