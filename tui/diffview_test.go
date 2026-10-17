package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	diffview "github.com/PizenLabs/ohgo/tui/diff"
)

// stubDiffFetcher scripts the inspector's two git reads, so the program's
// wiring is exercised without a repository or a subprocess.
type stubDiffFetcher struct {
	changes []diffview.FileChange
	diffs   map[string][]diffview.DiffChunk
}

func (s stubDiffFetcher) ChangedFiles(context.Context, diffview.Spec) ([]diffview.FileChange, error) {
	return s.changes, nil
}

func (s stubDiffFetcher) FileDiff(_ context.Context, _ diffview.Spec, path string) ([]diffview.DiffChunk, error) {
	return s.diffs[path], nil
}

// openFakeDiff installs an inspector backed by a stub and drives its initial
// load to completion, so a test starts from the loaded sidebar.
func openFakeDiff(t *testing.T, p *Program, fetcher stubDiffFetcher) {
	t.Helper()
	model := diffview.New(fetcher, diffview.DefaultSpec(), "working tree vs HEAD")
	p.state.Apply(DiffOpened{Model: model})
	p.syncDiffSize()
	p.handleDiffMsg(p.state.Diff.Init()())
	if p.state.Diff == nil {
		t.Fatal("diff overlay did not open")
	}
	if len(p.state.Diff.View()) == 0 {
		t.Fatal("diff overlay rendered nothing")
	}
}

func sampleDiffFetcher() stubDiffFetcher {
	return stubDiffFetcher{
		changes: []diffview.FileChange{{Path: "tui/app.go", Status: diffview.StatusModified, Added: 2, Removed: 1}},
		diffs: map[string][]diffview.DiffChunk{
			"tui/app.go": {{
				Header:   "@@ -1,2 +1,2 @@ func run()",
				OldStart: 1, NewStart: 1, OldLines: 2, NewLines: 2,
				Lines: []diffview.DiffLine{
					{Type: diffview.LineContext, OldLineNum: 1, NewLineNum: 1, Content: "func run() {"},
					{Type: diffview.LineDeletion, OldLineNum: 2, Content: "old body"},
					{Type: diffview.LineAddition, NewLineNum: 2, Content: "new body"},
				},
			}},
		},
	}
}

// The inspector is a modal: while open it replaces the transcript, hides the
// composer, and owns the keyboard; Esc closes it.
func TestDiffOverlayOwnsTheScreenAndEscCloses(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	openFakeDiff(t, p, sampleDiffFetcher())

	out := Render(p.state)
	if !strings.Contains(out, "app.go") {
		t.Errorf("sidebar does not list the changed file:\n%s", out)
	}
	if !strings.Contains(out, "esc close") {
		t.Errorf("footer does not advertise the close key:\n%s", out)
	}
	// The composer is hidden while the modal is up: no authority prefix row.
	if strings.Contains(out, "build > ") || strings.Contains(out, "ask > ") {
		t.Errorf("composer rendered behind the diff modal:\n%s", out)
	}
	if lines := len(strings.Split(out, "\n")); lines != 30 {
		t.Errorf("frame rows = %d, want 30", lines)
	}

	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Diff != nil {
		t.Fatal("esc did not close the inspector")
	}
}

// Selecting a file fetches its hunks on demand through the program's message
// routing, and the changed line becomes visible.
func TestDiffSelectFetchesAndRendersHunks(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	openFakeDiff(t, p, sampleDiffFetcher())

	// The tree starts on the "tui" directory; move onto the file first.
	p.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	_, cmd := p.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		p.handleDiffMsg(cmd())
	}
	out := Render(p.state)
	if !strings.Contains(out, "new body") {
		t.Errorf("selected file's hunks not rendered:\n%s", out)
	}
	if !strings.Contains(out, "-1,2 +1,2") {
		t.Errorf("hunk header not rendered:\n%s", out)
	}
}

// A typed filter survives the first Esc: the query is cleared, the inspector
// stays open, and only a second Esc closes it.
func TestDiffEscClearsFilterBeforeClosing(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	openFakeDiff(t, p, sampleDiffFetcher())

	p.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	p.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !p.state.Diff.FilterActive() {
		t.Fatal("filter did not activate after '/' + typing")
	}

	p.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Diff == nil {
		t.Fatal("first esc closed the inspector instead of clearing the filter")
	}
	if p.state.Diff.FilterActive() {
		t.Fatal("first esc did not clear the filter")
	}

	p.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Diff != nil {
		t.Fatal("second esc did not close the inspector")
	}
}

// /diff must not stack a second modal on top of one already owning the keys.
func TestDiffDeclinesWhenAnotherOverlayIsOpen(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerJobs, Title: "commands"})
	p.openDiff(nil)
	if p.state.Diff != nil {
		t.Fatal("diff opened over another modal")
	}
}

// The composer yields its rows to the inspector: ComputeLayout reports zero
// composer lines while the modal is up, so the reserved and painted frames
// agree.
func TestDiffReleasesComposerRows(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	openFakeDiff(t, p, sampleDiffFetcher())
	if got := ComputeLayout(p.state).ComposerLines; got != 0 {
		t.Errorf("composer lines = %d, want 0 while the diff modal is up", got)
	}
}

// A wheel tick reaches the inspector instead of scrolling the hidden
// transcript.
func TestDiffWheelRoutesToInspector(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	openFakeDiff(t, p, sampleDiffFetcher())

	// Move the tree selection down with two wheel ticks; the tree holds one
	// file, so the cursor stays put — the point is that neither tick touched
	// transcript scroll.
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if p.state.Scroll != 0 {
		t.Errorf("transcript scroll = %d, want 0 while the inspector owns the wheel", p.state.Scroll)
	}
	if p.state.Diff == nil {
		t.Fatal("wheel closed the inspector")
	}
}
