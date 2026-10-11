package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Rendering audit fixtures (markdown/streaming slice). Each test names the
// observable terminal behavior it defends; see the slice report for the
// fixture matrix and the defects these caught.

// A raw carriage return moves the terminal cursor to the row start,
// overwriting visible text. Model output routinely uses CRLF endings, so the
// markdown boundary folds them exactly like sanitizeBlock does for every
// other untrusted-text path.
func TestMarkdownCRLFFoldsToNewlines(t *testing.T) {
	out := ansi.Strip(RenderMarkdownWrapped("line one\r\nline two\r\n", DarkPalette(), 40))
	if strings.Contains(out, "\r") {
		t.Fatalf("CR reaches rendered output: %q", out)
	}
	// A single newline inside a paragraph reflows to a space; the fold's
	// job is only that no CR survives, here or across a blank line.
	if out != "line one line two" {
		t.Errorf("CRLF prose = %q, want the reflowed paragraph", out)
	}
	paras := ansi.Strip(RenderMarkdownWrapped("line one\r\n\r\nline two\r\n", DarkPalette(), 40))
	if paras != "line one\n\nline two" {
		t.Errorf("CRLF paragraphs = %q, want two blocks", paras)
	}
}

// Fenced code is where CRLF appears most: a pasted Windows file keeps its
// endings, and the fold must hold per code row, not just in prose.
func TestMarkdownCRInFenceStaysOnItsRows(t *testing.T) {
	out := RenderMarkdownWrapped("```go\nx := 1\r\ny := 2\r\n```", DarkPalette(), 40)
	if strings.Contains(out, "\r") {
		t.Fatalf("CR reaches fenced output: %q", out)
	}
	stripped := ansi.Strip(out)
	if !strings.Contains(stripped, "x := 1") || !strings.Contains(stripped, "y := 2") {
		t.Errorf("fold lost code rows: %q", stripped)
	}
}

// A stream split right after the \r of a CRLF pair must not flash a raw CR:
// every accumulation prefix renders clean and the final prefix converges to
// the whole-document render.
func TestMarkdownCRConvergesAcrossStreamPrefixes(t *testing.T) {
	full := "line one\r\nline two\r\n"
	pal := DarkPalette()
	want := ansi.Strip(RenderMarkdownWrapped(full, pal, 40))
	for _, prefix := range []string{"line one", "line one\r", "line one\r\nline", full} {
		if got := RenderMarkdownWrapped(prefix, pal, 40); strings.Contains(got, "\r") {
			t.Errorf("prefix %q renders a raw CR", prefix)
		}
	}
	if got := ansi.Strip(RenderMarkdownWrapped(full, pal, 40)); got != want {
		t.Errorf("final = %q, want the whole-document render %q", got, want)
	}
}

// TruncateForDisplay cuts tool output at a byte budget. Cutting inside a
// multi-byte rune hands an invalid string to every downstream consumer;
// the cut backs off to the rune boundary so the kept text stays valid.
// A rune straddling the budget is dropped whole rather than kept whole:
// the budget is a flood cap, not a promise to keep the last character.
func TestTruncateForDisplayKeepsRuneBoundary(t *testing.T) {
	text := strings.Repeat("a", 3999) + "中tail"
	got, truncated := TruncateForDisplay(text, 4000)
	if !truncated {
		t.Fatal("expected truncation past the budget")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("cut splits a rune: tail %q", got[len(got)-16:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 3999)) {
		t.Errorf("rune-safe cut lost kept bytes: tail %q", got[len(got)-16:])
	}
	if strings.Contains(got, "中") {
		t.Errorf("straddling rune kept half: %q", got[len(got)-16:])
	}
	if !strings.HasSuffix(got, "\n… [truncated]") {
		t.Errorf("truncation marker missing: %q", got[len(got)-16:])
	}
	// A rune ending exactly on the budget survives the cut untouched.
	aligned := strings.Repeat("a", 3997) + "中tail"
	kept, _ := TruncateForDisplay(aligned, 4000)
	if !strings.HasPrefix(kept, strings.Repeat("a", 3997)+"中") {
		t.Errorf("aligned rune dropped: %q", kept[len(kept)-16:])
	}
}

// oneLine feeds approval dialogs and live tool previews. A byte cut inside
// a rune shows as � in the safety-critical dialog; the straddler drops
// whole, like the tool-output cut above.
func TestOneLineKeepsRuneBoundary(t *testing.T) {
	got := oneLine(strings.Repeat("b", 59) + "中tail here")
	if !utf8.ValidString(got) {
		t.Fatalf("oneLine splits a rune: %q", got)
	}
	if got != strings.Repeat("b", 59)+"…" {
		t.Errorf("oneLine = %q, want the straddler dropped whole", got)
	}
	aligned := oneLine(strings.Repeat("b", 57) + "中tail here")
	if !strings.HasPrefix(aligned, strings.Repeat("b", 57)+"中") {
		t.Errorf("aligned rune dropped: %q", aligned)
	}
}

// Lock-in: combining marks measure by base cell, not by rune, and wrapping
// never alters the sequence. Vietnamese is the probe, not the limit.
func TestCombiningMarksSurviveWrapUnchanged(t *testing.T) {
	text := "tiếng việt"
	if w := CellWidth(text); w != 10 {
		t.Errorf("combining width = %d, want base cells 10", w)
	}
	if lines := WrapToWidth(text, 40); len(lines) != 1 || lines[0] != text {
		t.Errorf("wrap altered combining text: %q", lines)
	}
}

// Lock-in: a zero-width-joiner emoji sequence keeps its cells and its bytes
// through prose rendering. go-runewidth already reports the terminal width.
func TestZWJSequenceSurvivesProseRender(t *testing.T) {
	family := "👨\u200d👩\u200d👧"
	if w := CellWidth(family); w != 2 {
		t.Errorf("ZWJ width = %d, want the terminal's 2 cells", w)
	}
	stripped := ansi.Strip(RenderMarkdownWrapped("hi "+family+" bye", DarkPalette(), 40))
	if !strings.Contains(stripped, family) {
		t.Errorf("ZWJ sequence corrupted: %q", stripped)
	}
}
