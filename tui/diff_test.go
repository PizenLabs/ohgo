package tui

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// sampleDiffHunk is a realistic edit: a context line, one replacement, one
// insertion, and a closing context line, with both a summary and a path.
func sampleDiffHunk() DiffHunk {
	return DiffHunk{
		File:    "internal/api/handler.go",
		Summary: "Added 2 lines, removed 1 line",
		Lines: []DiffLine{
			{Type: DiffCtx, N: 10, Text: "func handle(w http.ResponseWriter) {"},
			{Type: DiffDel, N: 11, Text: "    oldCall()"},
			{Type: DiffAdd, N: 11, Text: "    newCall()"},
			{Type: DiffAdd, N: 12, Text: "    log()"},
			{Type: DiffCtx, N: 13, Text: "}"},
		},
	}
}

// The header names the edited file with the record glyph and the Update label.
func TestRenderDiffHunkHeaderNamesTheEditedFile(t *testing.T) {
	first := strings.Split(ansi.Strip(RenderDiffHunk(sampleDiffHunk(), 80)), "\n")[0]
	if want := "⏺ Update (internal/api/handler.go)"; first != want {
		t.Errorf("header = %q, want %q", first, want)
	}
}

// A provided summary renders indented behind the ⎿ glyph on its own row.
func TestRenderDiffHunkRendersSummaryBehindGlyph(t *testing.T) {
	lines := strings.Split(ansi.Strip(RenderDiffHunk(sampleDiffHunk(), 80)), "\n")
	if len(lines) < 2 {
		t.Fatalf("want at least a header and a summary row, got %q", lines)
	}
	if got, want := lines[1], "  ⎿ Added 2 lines, removed 1 line"; got != want {
		t.Errorf("summary row = %q, want %q", got, want)
	}
}

// A missing summary must not leave an empty ⎿ row behind: the box follows the
// header directly.
func TestRenderDiffHunkOmitsMissingSummary(t *testing.T) {
	h := sampleDiffHunk()
	h.Summary = ""
	out := ansi.Strip(RenderDiffHunk(h, 80))
	if strings.Contains(out, "⎿") {
		t.Errorf("empty summary still rendered a ⎿ row:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "╭") {
		t.Errorf("row 2 = %q, want the box top directly under the header", lines)
	}
}

// Added rows carry a +, deleted rows a -, and context rows a blank mark, each
// in front of the unchanged line text.
func TestRenderDiffHunkMarksAddedDeletedAndContextRows(t *testing.T) {
	out := ansi.Strip(RenderDiffHunk(sampleDiffHunk(), 80))
	for _, want := range []string{
		"10   func handle(w http.ResponseWriter) {", // context: blank mark
		"11 -     oldCall()",                        // deleted
		"11 +     newCall()",                        // added
		"12 +     log()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing row %q:\n%s", want, out)
		}
	}
}

// Line numbers right-align in a stable column, so a one- and two-digit file
// line up under each other.
func TestDiffGutterRightAlignsNumbers(t *testing.T) {
	cases := []struct {
		width, n int
		want     string
	}{
		{3, 7, "  7"},
		{3, 128, "128"},
		{2, 12, "12"},
		{3, 0, "   "}, // an annotating row keeps the column width blank
	}
	for _, tc := range cases {
		if got := diffGutter(tc.width, tc.n); got != tc.want {
			t.Errorf("diffGutter(%d, %d) = %q, want %q", tc.width, tc.n, got, tc.want)
		}
	}
}

// The gutter is sized for the largest line number but never collapses below
// two cells.
func TestDiffGutterWidthFloorsAtTwo(t *testing.T) {
	cases := []struct {
		lines []DiffLine
		want  int
	}{
		{[]DiffLine{{N: 1}, {N: 9}}, 2},
		{[]DiffLine{{N: 1}, {N: 1000}}, 4},
		{[]DiffLine{{N: 0}}, 2},
		{[]DiffLine{}, 2},
	}
	for _, tc := range cases {
		if got := diffGutterWidth(tc.lines); got != tc.want {
			t.Errorf("diffGutterWidth(%+v) = %d, want %d", tc.lines, got, tc.want)
		}
	}
}

// Every rendered row, header and box alike, stays within the viewport width.
func TestRenderDiffHunkFitsTheViewportWidth(t *testing.T) {
	for _, width := range []int{20, 40, 60, 80, 120} {
		out := strings.TrimRight(RenderDiffHunk(sampleDiffHunk(), width), "\n")
		for i, line := range strings.Split(out, "\n") {
			if got := CellWidth(line); got > width {
				t.Errorf("width %d: row %d is %d cells: %q", width, i, got, ansi.Strip(line))
			}
		}
	}
}

// The framed panel is rectangular: every box row occupies the same width as
// its borders, so the right edge lines up.
func TestRenderDiffHunkBoxIsRectangular(t *testing.T) {
	lines := strings.Split(strings.TrimRight(RenderDiffHunk(sampleDiffHunk(), 60), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("want a framed panel, got %q", lines)
	}
	box := lines[2:] // skip the header and summary rows
	want := CellWidth(box[0])
	for i, line := range box {
		if got := CellWidth(line); got != want {
			t.Errorf("box row %d is %d cells, want %d: %q", i, got, want, ansi.Strip(line))
		}
	}
}

// Without a known viewport the panel still renders at its natural width and
// keeps every row aligned.
func TestRenderDiffHunkRendersNaturalWidthWithoutViewport(t *testing.T) {
	out := strings.TrimRight(RenderDiffHunk(sampleDiffHunk(), 0), "\n")
	if !strings.Contains(ansi.Strip(out), "func handle(w http.ResponseWriter) {") {
		t.Errorf("natural width truncated content:\n%s", ansi.Strip(out))
	}
	lines := strings.Split(out, "\n")
	box := lines[2:]
	want := CellWidth(box[0])
	for i, line := range box {
		if got := CellWidth(line); got != want {
			t.Errorf("box row %d is %d cells, want %d: %q", i, got, want, ansi.Strip(line))
		}
	}
}

// An overlong row truncates with an ellipsis rather than wrapping or
// overflowing the panel.
func TestRenderDiffHunkTruncatesOverlongRows(t *testing.T) {
	h := DiffHunk{File: "x.go", Lines: []DiffLine{{Type: DiffAdd, N: 1, Text: strings.Repeat("x", 200)}}}
	out := RenderDiffHunk(h, 40)
	if !strings.Contains(ansi.Strip(out), "…") {
		t.Errorf("overlong row was not truncated:\n%s", ansi.Strip(out))
	}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if CellWidth(line) > 40 {
			t.Errorf("row %d overflows 40 cells: %q", i, ansi.Strip(line))
		}
	}
}

// A hunk with no changed rows renders its header and nothing else, rather than
// an empty box.
func TestRenderDiffHunkWithoutLinesRendersHeaderOnly(t *testing.T) {
	out := ansi.Strip(RenderDiffHunk(DiffHunk{File: "a.go"}, 40))
	if !strings.Contains(out, "⏺ Update (a.go)") {
		t.Errorf("header missing:\n%s", out)
	}
	if strings.ContainsAny(out, "╭╰│─") {
		t.Errorf("empty hunk drew a box:\n%s", out)
	}
}

// File content is untrusted: cursor control is stripped and carriage returns
// fold so they cannot move the terminal cursor across rendered rows.
func TestRenderDiffHunkSanitizesUntrustedContent(t *testing.T) {
	h := DiffHunk{
		File: "a.go",
		Lines: []DiffLine{
			{Type: DiffAdd, N: 1, Text: "\x1b[2Jcleared"},
			{Type: DiffDel, N: 2, Text: "carriage\rreturn"},
		},
	}
	out := RenderDiffHunk(h, 60)
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("cursor-clear escape reached the terminal: %q", out)
	}
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "cleared") || !strings.Contains(plain, "carriage return") {
		t.Errorf("sanitized content lost: %q", plain)
	}
	if strings.ContainsRune(plain, '\r') {
		t.Errorf("carriage return survived: %q", plain)
	}
}

// The panic-free contract for malformed input: a wrong-profile renderer and
// an empty hunk still render without panicking.
func TestRenderDiffHunkNeverPanics(t *testing.T) {
	if got := RenderDiffHunk(DiffHunk{}, 0); got == "" {
		t.Error("empty hunk rendered nothing at all")
	}
	if got := RenderDiffHunk(DiffHunk{File: "x"}, -5); got == "" {
		t.Error("negative width rendered nothing at all")
	}
}

// The design tokens are wired through the styles: with a forced true-color
// profile the mint glyph, cyan path, row tints, panel background, and border
// all reach the terminal.
func TestRenderDiffHunkWiresTheDesignTokens(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	out := renderDiffHunk(newDiffStyles(r), sampleDiffHunk(), 80)
	tokens := map[string]string{
		"mint glyph":  "38;2;78;169;111",  // #4EA96F
		"cyan path":   "38;2;125;207;255", // #7DCFFF
		"add tint":    "48;2;22;40;30",    // #16281E
		"delete tint": "48;2;42;25;29",    // #2A191D
		"panel bg":    "48;2;16;16;16",    // #101010
		"panel edge":  "38;2;32;32;34",    // #202022
	}
	for name, seq := range tokens {
		if !strings.Contains(out, seq) {
			t.Errorf("%s (%s) missing from styled output", name, seq)
		}
	}
}
