package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fileRoster is a small workspace used by the "@" trigger tests.
func fileRoster() []string {
	return []string{"main.go", "pkg/", "pkg/util.go", "tui/app.go"}
}

// TestFileTriggerRendersInlinePalette proves a typed "@" opens the SAME inline
// palette as "/" — positioned above the composer — and never a modal picker.
func TestFileTriggerRendersInlinePalette(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(fileRoster)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "@", Cursor: 1})
	p.ensureMentionRoster()
	if p.state.Picker != nil {
		t.Fatalf("picker = %+v, want no modal overlay for @", p.state.Picker)
	}
	out := Render(p.state)
	if !strings.Contains(out, "FILES") {
		t.Errorf("inline palette missing its FILES header:\n%s", out)
	}
	for _, want := range []string{"@main.go", "@pkg/", "@pkg/util.go", "@tui/app.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("inline palette missing %q:\n%s", want, out)
		}
	}
}

// TestFileTriggerFilters proves the palette narrows with the typed path text.
func TestFileTriggerFilters(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(fileRoster)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "@pkg", Cursor: 4})
	p.ensureMentionRoster()
	entries := paletteEntriesFor(p.state)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the two pkg paths", entries)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, "@pkg/") {
			t.Errorf("entry %q does not match the query", e.Name)
		}
	}
}

// TestEnterSplicesFileReference proves Enter accepts the highlighted file,
// splices "@path " over the trigger token, and leaves a trailing space so the
// next word starts a new token instead of gluing to the path.
func TestEnterSplicesFileReference(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"tui/app.go"} })
	keyPress(p, runes("see "))
	keyPress(p, runes("@"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "see @tui/app.go " {
		t.Fatalf("input = %q, want the reference spliced with a trailing space", p.state.Input)
	}
	if p.state.Picker != nil {
		t.Fatal("accept opened a modal picker")
	}
}

// TestTabSplicesFileReference proves Tab is an accept key too, so the palette
// behaves the same for every trigger.
func TestTabSplicesFileReference(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"tui/app.go"} })
	keyPress(p, runes("@"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyTab})
	if p.state.Input != "@tui/app.go " {
		t.Fatalf("input = %q, want Tab to accept the highlighted reference", p.state.Input)
	}
}

// TestFileAcceptAppendsTrailingSpace pins the separation contract: accepting a
// file leaves a space so the reference cannot glue to the next typed word.
func TestFileAcceptAppendsTrailingSpace(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"index.html"} })
	keyPress(p, runes("@"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	keyPress(p, runes("check this"))
	if p.state.Input != "@index.html check this" {
		t.Fatalf("input = %q, want the word to follow the reference, not glue to it", p.state.Input)
	}
}

// TestFolderAcceptOmitsTrailingSpace pins the other half: a folder reference
// keeps the cursor flush against the trailing slash so the next keystroke
// continues the path.
func TestFolderAcceptOmitsTrailingSpace(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"pkg/", "pkg/util.go"} })
	p.state.Apply(ComposerChanged{Text: "@pkg", Cursor: 4})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "@pkg/" {
		t.Fatalf("input = %q, want the folder accepted without a trailing space", p.state.Input)
	}
	// The cursor still sits inside the trigger word, so typing continues the
	// nested path rather than starting a new token.
	keyPress(p, runes("util.go"))
	if p.state.Input != "@pkg/util.go" {
		t.Fatalf("input = %q, want the nested path filtered after the folder", p.state.Input)
	}
}

// TestFolderReferenceKeepsSlash proves a folder row splices with its trailing
// slash, so the next character continues a path instead of a sibling name.
func TestFolderReferenceKeepsSlash(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"src/"} })
	keyPress(p, runes("@"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "@src/" {
		t.Fatalf("input = %q, want the folder reference to keep its slash", p.state.Input)
	}
}

// TestReferenceFollowsCompletedCommand proves the slash completion's trailing
// space lets an "@" reference follow a command in the same composer line.
func TestReferenceFollowsCompletedCommand(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"tui/app.go"} })
	p.state.Apply(ComposerChanged{Text: "/bui", Cursor: 4})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "/build " {
		t.Fatalf("input = %q, want the completion to leave a trailing space", p.state.Input)
	}
	keyPress(p, runes("@"))
	if len(paletteEntriesFor(p.state)) == 0 {
		t.Fatalf("no inline palette for @ after a completed command")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "/build @tui/app.go " {
		t.Fatalf("input = %q, want the reference appended after the command", p.state.Input)
	}
}

// TestAtInsideWordStaysLiteral proves an "@" that does not open a token is
// ordinary text: no palette rises and nothing is spliced.
func TestAtInsideWordStaysLiteral(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(fileRoster)
	keyPress(p, runes("foo"))
	keyPress(p, runes("@"))
	if len(paletteEntriesFor(p.state)) != 0 {
		t.Fatal("an at inside a word opened the palette")
	}
	if p.state.Input != "foo@" {
		t.Fatalf("input = %q, want the at kept literal", p.state.Input)
	}
}

// TestNoMentionListerLeavesAtLiteral proves the feature is off when the owner
// installs no workspace roster: "@" is just a character.
func TestNoMentionListerLeavesAtLiteral(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("@"))
	if len(paletteEntriesFor(p.state)) != 0 {
		t.Fatal("an uninstalled mention lister still opened the palette")
	}
	if p.state.Input != "@" {
		t.Fatalf("input = %q, want the at kept literal", p.state.Input)
	}
}

// TestSpliceTokenReplacesTheWholeToken proves the reference replaces the token
// the user was editing, not just the runes before the cursor, and that the
// cursor lands after the insertion.
func TestSpliceTokenReplacesTheWholeToken(t *testing.T) {
	text, cursor := SpliceToken("see @tu here", 4, 7, "@tui/app.go")
	if text != "see @tui/app.go here" {
		t.Fatalf("text = %q, want the token replaced whole", text)
	}
	if cursor != 4+len("@tui/app.go") {
		t.Fatalf("cursor = %d, want it after the reference", cursor)
	}
}
