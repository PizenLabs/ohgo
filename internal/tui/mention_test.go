package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
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
	out := ansi.Strip(Render(p.state))
	if !strings.Contains(out, "FILES") {
		t.Errorf("inline palette missing its FILES header:\n%s", out)
	}
	// A path row is two columns: the directory context and the target name.
	// The trigger symbol belongs to the composer, not to the row.
	for _, want := range []string{"main.go", "pkg/", "util.go", "tui/", "app.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("inline palette missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "@pkg/") {
		t.Errorf("a path row rendered as one glued reference:\n%s", out)
	}
}

// TestFilePaletteRendersTwoColumns pins the file grid: the directory context
// fills a left column and every target name starts at one shared cell, so a
// path list reads as a table instead of as a run of full references.
func TestFilePaletteRendersTwoColumns(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string {
		return []string{
			"cmd/ohgo/workspace/file.go",
			"cmd/ohgo/workspace/config.yaml",
			"docs/OVERVIEW.md",
			"main.go",
		}
	})
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "@", Cursor: 1})
	p.ensureMentionRoster()
	out := ansi.Strip(Render(p.state))

	cellOf := func(text string) int {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			if at := strings.Index(line, text); at >= 0 {
				return CellWidth(line[:at])
			}
		}
		t.Fatalf("rendered palette missing %q:\n%s", text, out)
		return 0
	}
	target := cellOf("file.go")
	for _, name := range []string{"config.yaml", "OVERVIEW.md", "main.go"} {
		if got := cellOf(name); got != target {
			t.Errorf("%s starts at cell %d, want the shared target column %d:\n%s", name, got, target, out)
		}
	}
	for _, dir := range []string{"cmd/ohgo/workspace/", "docs/"} {
		if got := cellOf(dir); got >= target {
			t.Errorf("context %q starts at cell %d, want left of the target column %d:\n%s", dir, got, target, out)
		}
	}
}

// TestFilePaletteTruncatesThePathFromTheLeft pins the overflow contract: a
// directory deeper than its column loses its head while the target name — the
// part the user is choosing between — stays whole, and the visible context
// still begins at a separator so it reads as a shortened path rather than as a
// corrupted one.
func TestFilePaletteTruncatesThePathFromTheLeft(t *testing.T) {
	parent := "alpha/bravo/charlie/delta/echo/foxtrot/golf/hotel/india/juliet/kilo/"
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{parent + "file.go"} })
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.state.Apply(ComposerChanged{Text: "@", Cursor: 1})
	p.ensureMentionRoster()
	out := ansi.Strip(Render(p.state))

	at := strings.Index(out, "file.go")
	if at < 0 {
		t.Fatalf("the target name was truncated away:\n%s", out)
	}
	head := out[:at]
	if i := strings.LastIndex(head, "▸"); i >= 0 {
		head = head[i+len("▸"):]
	}
	context := strings.TrimSpace(head)
	if !strings.HasPrefix(context, "…/") {
		t.Errorf("context column %q does not elide at a segment boundary", context)
	}
	if !strings.HasSuffix(context, "kilo/") {
		t.Errorf("context column %q does not keep the nearest directory", context)
	}
	if strings.Contains(out, "alpha") || strings.Contains(out, "charlie") {
		t.Errorf("the dropped head still renders:\n%s", out)
	}
}

// TestFilePaletteKeepsItsBoxWidth proves the file grid never overflows its
// frame, where the box would soft-wrap and push the composer down a row.
func TestFilePaletteKeepsItsBoxWidth(t *testing.T) {
	roster := []string{
		"cmd/ohgo/workspace/internal/execution/tools/palette_renderer_with_a_long_name.go",
		"docs/architecture/OVERVIEW.md",
		"main.go",
	}
	for width := 40; width <= 120; width += 7 {
		p := NewProgram(nil, "test", 0)
		p.SetMentionLister(func() []string { return roster })
		p.state.Apply(TerminalResized{Width: width, Height: 24})
		p.state.Apply(ComposerChanged{Text: "@", Cursor: 1})
		p.ensureMentionRoster()
		lines := strings.Split(ansi.Strip(Render(p.state)), "\n")
		frame := 0
		for _, line := range lines {
			if strings.Contains(line, " FILES ") && strings.HasPrefix(line, "│") {
				frame = CellWidth(line)
				break
			}
		}
		if frame == 0 {
			t.Fatalf("width %d: file palette did not render:\n%s", width, strings.Join(lines, "\n"))
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
				if got := CellWidth(line); got != frame {
					t.Fatalf("width %d: palette row is %d cells, frame is %d: %q", width, got, frame, line)
				}
			}
		}
	}
}

// TestFilePaletteRootWindowStartsNamesAtTheMarker proves a workspace-root
// listing does not indent every name under a context column that never prints:
// with no directory to show, each row begins at its marker.
func TestFilePaletteRootWindowStartsNamesAtTheMarker(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"cmd/", "docs/", "main.go"} })
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	p.state.Apply(ComposerChanged{Text: "@", Cursor: 1})
	p.ensureMentionRoster()
	out := ansi.Strip(Render(p.state))

	// Box border plus one padding cell, then the two-cell marker gutter.
	const want = 4
	for _, name := range []string{"cmd/", "docs/", "main.go"} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			at := strings.Index(line, name)
			if !strings.HasPrefix(line, "│") || at < 0 {
				continue
			}
			found = true
			if got := CellWidth(line[:at]); got != want {
				t.Errorf("%q starts at cell %d, want %d flush after the marker:\n%s", name, got, want, out)
			}
		}
		if !found {
			t.Errorf("rendered palette missing %q:\n%s", name, out)
		}
	}
}

// TestFilePalettePaintsTheSelectedRowAcrossBothColumns pins the selection
// contract: the active row carries the selection surface behind its context,
// gutter, and target — one bar spanning both columns — while an inactive row
// paints no surface at all, so the row Enter would accept is unambiguous.
func TestFilePalettePaintsTheSelectedRowAcrossBothColumns(t *testing.T) {
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	p := NewProgram(nil, "test", 0)
	p.SetMentionLister(func() []string { return []string{"cmd/file.go", "cmd/config.yaml"} })
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "@", Cursor: 1})
	p.ensureMentionRoster()
	raw := Render(p.state)

	rowOf := func(text string) string {
		t.Helper()
		for _, line := range strings.Split(raw, "\n") {
			if strings.Contains(ansi.Strip(line), text) {
				return line
			}
		}
		t.Fatalf("rendered palette missing %q:\n%s", text, ansi.Strip(raw))
		return ""
	}
	surface := "48;2;" + rgbSeq(DarkTokens().SurfaceSelection)
	painted := func(row string) (context, target bool) {
		for _, span := range strings.Split(row, "\x1b[0m") {
			text := ansi.Strip(span)
			painted := strings.Contains(span, surface)
			if strings.Contains(text, "cmd/") && painted {
				context = true
			}
			if strings.Contains(text, "file.go") && painted {
				target = true
			}
		}
		return context, target
	}
	context, target := painted(rowOf("file.go"))
	if !context || !target {
		t.Errorf("selected row paints context=%v target=%v, want a highlight across both columns:\n%q",
			context, target, rowOf("file.go"))
	}
	if got := strings.Count(rowOf("config.yaml"), surface); got != 0 {
		t.Errorf("inactive row painted the selection surface %d times", got)
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
