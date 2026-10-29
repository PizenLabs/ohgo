package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/history"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// fakePromptHistory is an in-memory stand-in for the durable store. It lets the
// program contract be tested without touching a file.
type fakePromptHistory struct {
	entries []string
	added   []string
}

func (f *fakePromptHistory) Entries() []string {
	return append([]string(nil), f.entries...)
}

func (f *fakePromptHistory) Add(prompt string) {
	f.added = append(f.added, prompt)
	f.entries = append(f.entries, prompt)
}

func (f *fakePromptHistory) Flush() error { return nil }

// TestWithPromptHistorySeedsUpArrowRecall is the session-to-session contract:
// the first up arrow in a fresh program reaches a prompt saved earlier.
func TestWithPromptHistorySeedsUpArrowRecall(t *testing.T) {
	store := &fakePromptHistory{entries: []string{"older prompt", "previous session"}}
	p := NewProgram(nil, "test", 0, WithPromptHistory(store))
	if got, want := strings.Join(p.state.Submitted, "|"), "older prompt|previous session"; got != want {
		t.Fatalf("Submitted = %q, want %q", got, want)
	}

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlP})
	if p.state.Input != "previous session" {
		t.Fatalf("ctrl+p recalled %q, want the newest persisted prompt", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlP})
	if p.state.Input != "older prompt" {
		t.Fatalf("second ctrl+p recalled %q, want the older persisted prompt", p.state.Input)
	}
}

// TestUpRecallsProjectHistoryBeforeGlobal pins the scoped navigation order:
// the project tail is reached before the merged global fallback.
func TestUpRecallsProjectHistoryBeforeGlobal(t *testing.T) {
	store := &fakePromptHistory{entries: []string{"global prompt", "project older", "project newest"}}
	p := NewProgram(nil, "test", 0, WithPromptHistory(store))

	for _, want := range []string{"project newest", "project older", "global prompt"} {
		keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlP})
		if p.state.Input != want {
			t.Fatalf("ctrl+p recalled %q, want %q", p.state.Input, want)
		}
	}
}

// TestSubmittedPromptJoinsPersistentHistory proves only a real turn is saved:
// a typed draft and its ghost suggestion are not.
func TestSubmittedPromptJoinsPersistentHistory(t *testing.T) {
	store := &fakePromptHistory{entries: []string{"deploy production"}}
	p := NewProgram(nil, "test", 0, WithPromptHistory(store))

	keyPress(p, runes("deploy p"))
	if len(store.added) != 0 {
		t.Fatalf("typing added %q to history, want nothing before submission", store.added)
	}

	p.apply(UserSubmitted{Text: "deploy p"})
	if got, want := strings.Join(store.added, "|"), "deploy p"; got != want {
		t.Fatalf("added = %q, want only the submitted prompt %q", got, want)
	}
}

// TestComposerRendersInlineGhostText proves the completion is visible after
// the cursor and remains purely visual: the input buffer is untouched.
func TestComposerRendersInlineGhostText(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Submitted = []string{"deploy production"}
	s.Apply(ComposerChanged{Text: "deploy p", Cursor: len([]rune("deploy p"))})

	if s.Input != "deploy p" {
		t.Fatalf("rendering changed the input to %q", s.Input)
	}
	out := ansi.Strip(renderComposer(s, DarkPalette(), ComputeLayout(s)))
	if !strings.Contains(out, "deploy p▌roduction") {
		t.Fatalf("composer did not render the ghost tail after the cursor:\n%s", out)
	}
}

// TestInlineGhostHidesWhenItWouldNotCompleteFromTheCursor proves moving into
// the line withdraws the suggestion: accepting it there would rewrite text the
// user is editing.
func TestInlineGhostHidesWhenItWouldNotCompleteFromTheCursor(t *testing.T) {
	s := New()
	s.Submitted = []string{"deploy production"}
	s.Apply(ComposerChanged{Text: "deploy p", Cursor: len([]rune("deploy p"))})
	if got := s.inlineSuggestion(); got != "deploy production" {
		t.Fatalf("inlineSuggestion = %q, want the match", got)
	}

	s.Apply(ComposerChanged{Text: s.Input, Cursor: 4})
	if got := s.inlineSuggestion(); got != "" {
		t.Fatalf("inlineSuggestion with the cursor inside the line = %q, want none", got)
	}
}

// TestTabAcceptsInlineSuggestion pins the accept keybinding and its result:
// the whole prompt replaces the prefix, with the cursor at the end.
func TestTabAcceptsInlineSuggestion(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Submitted = []string{"deploy production"}
	keyPress(p, runes("deploy p"))

	keyPress(p, tea.KeyMsg{Type: tea.KeyTab})
	if p.state.Input != "deploy production" {
		t.Fatalf("tab left input %q, want the accepted suggestion", p.state.Input)
	}
	if p.state.Cursor != len([]rune(p.state.Input)) {
		t.Fatalf("cursor = %d, want the end of the accepted prompt", p.state.Cursor)
	}
	if p.state.Status.Busy {
		t.Fatal("accepting a suggestion must not submit a turn")
	}
	if got := p.state.suggestionTail(); got != "" {
		t.Fatalf("ghost %q survived acceptance", got)
	}
}

// TestRightAcceptsInlineSuggestionOnlyAtTheEnd proves the arrow accepts the
// completion when it is meaningful and remains a cursor move otherwise.
func TestRightAcceptsInlineSuggestionOnlyAtTheEnd(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Submitted = []string{"deploy production"}
	keyPress(p, runes("deploy p"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyRight})
	if p.state.Input != "deploy production" {
		t.Fatalf("right arrow left input %q, want the accepted suggestion", p.state.Input)
	}

	// With the cursor inside the line there is no ghost; right only moves.
	keyPress(p, tea.KeyMsg{Type: tea.KeyLeft})
	before := p.state.Cursor
	keyPress(p, tea.KeyMsg{Type: tea.KeyRight})
	if p.state.Input != "deploy production" || p.state.Cursor != before+1 {
		t.Fatalf("right inside the line = %q at %d, want a one-rune move", p.state.Input, p.state.Cursor)
	}
}

// TestGhostDoesNotEnterTheSubmissionBuffer proves the suggestion cannot leak
// into the agent pipeline: enter submits exactly what was typed.
func TestGhostDoesNotEnterTheSubmissionBuffer(t *testing.T) {
	s := New()
	s.Submitted = []string{"deploy production"}
	s.Apply(ComposerChanged{Text: "deploy p", Cursor: len([]rune("deploy p"))})
	if tail := s.suggestionTail(); tail != "roduction" {
		t.Fatalf("ghost tail = %q, want rroduction", tail)
	}
	if s.Input != "deploy p" {
		t.Fatalf("Input = %q, want only the typed prefix", s.Input)
	}
}

// TestPersistentHistoryReachesTheNextProgram is the end-to-end acceptance
// path: a prompt saved by one program lands in both scopes and is recalled by
// a new program opened on the same project.
func TestPersistentHistoryReachesTheNextProgram(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)

	first := history.Load(root)
	p := NewProgram(nil, "test", 0, WithPromptHistory(first))
	p.apply(UserSubmitted{Text: "from the previous session"})
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, ".ohgo", "history"),
		filepath.Join(home, ".ohgo", "history"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		if !strings.Contains(string(data), "from the previous session") {
			t.Fatalf("%q does not contain the submitted prompt: %q", path, data)
		}
	}

	second := history.Load(root)
	defer func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second store: %v", err)
		}
	}()
	q := NewProgram(nil, "test", 0, WithPromptHistory(second))
	keyPress(q, tea.KeyMsg{Type: tea.KeyUp})
	if q.state.Input != "from the previous session" {
		t.Fatalf("up recalled %q, want the persisted prompt", q.state.Input)
	}
}
