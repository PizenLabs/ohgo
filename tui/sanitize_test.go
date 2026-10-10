package tui

import (
	"strings"
	"testing"
	"time"
)

// Untrusted bytes (tool results, pasted input, prompts) must never move the
// terminal cursor: each test below pins the sanitizer contract that keeps
//color styling while neutralizing cursor control.

func TestSanitizeBlockKeepsColorsDropsMovement(t *testing.T) {
	in := "\x1b[31mred\x1b[0m \x1b[2Kcleared \x1b[1Aup \x1b[3;1Hhome"
	got := sanitizeBlock(in)
	if !strings.Contains(got, "\x1b[31mred\x1b[0m") {
		t.Errorf("SGR colors stripped too: %q", got)
	}
	for _, bad := range []string{"\x1b[2K", "\x1b[1A", "\x1b[3;1H"} {
		if strings.Contains(got, bad) {
			t.Errorf("movement sequence survived: %q in %q", bad, got)
		}
	}
}

func TestSanitizeBlockTurnsProgressOverwritesIntoHistory(t *testing.T) {
	got := sanitizeBlock("50%\r60%\rDone")
	if strings.Contains(got, "\r") {
		t.Errorf("carriage return survived: %q", got)
	}
	for _, want := range []string{"50%", "60%", "Done"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress text %q lost: %q", want, got)
		}
	}
	if lines := strings.Split(got, "\n"); len(lines) != 3 {
		t.Errorf("got %d lines, want 3 readable rows: %q", len(lines), got)
	}
}

func TestSanitizeStripsUnterminatedSequences(t *testing.T) {
	// A byte-cut truncation upstream can leave a dangling introducer; it
	// must not reach the terminal half-open.
	for _, in := range []string{"text\x1b[2", "text\x1b[", "text\x1b", "text\x1b]0;title"} {
		got := sanitizeBlock(in)
		if strings.Contains(got, "\x1b") {
			t.Errorf("dangling escape survived in %q -> %q", in, got)
		}
		if !strings.Contains(got, "text") {
			t.Errorf("content lost in %q -> %q", in, got)
		}
	}
	osc := "a\x1b]8;;http://example.com\x07link"
	if got := sanitizeBlock(osc); strings.Contains(got, "\x1b") || !strings.Contains(got, "link") {
		t.Errorf("OSC hyperlink mishandled: %q", got)
	}
}

func TestSanitizeInlineFoldsNewlines(t *testing.T) {
	got := sanitizeInline("a\nb\rc\x00d\x7fe")
	if strings.ContainsAny(got, "\r\n\x00\x7f") {
		t.Errorf("control survived inline: %q", got)
	}
	for _, want := range []string{"a", "b", "c", "d", "e"} {
		if !strings.Contains(got, want) {
			t.Errorf("content %q lost: %q", want, got)
		}
	}
}

func TestSanitizeRepairsInvalidUTF8(t *testing.T) {
	got := sanitizeBlock("ok\xff\xfe done")
	if !strings.Contains(got, "ok") || !strings.Contains(got, "done") {
		t.Errorf("valid text lost: %q", got)
	}
	if got != strings.ToValidUTF8(got, "�") {
		t.Errorf("output still invalid UTF-8: %q", got)
	}
}

func TestSanitizeWithCursorMapping(t *testing.T) {
	// Plain text: cursor untouched.
	if _, c := sanitizeWithCursor("hello", 2); c != 2 {
		t.Errorf("plain cursor = %d, want 2", c)
	}
	// \r maps 1:1 to \n, so the index survives.
	if clean, c := sanitizeWithCursor("a\rb", 2); clean != "a\nb" || c != 2 {
		t.Errorf("cr mapping = (%q, %d), want (a\\nb, 2)", clean, c)
	}
	// A stripped CSI before the cursor pulls it back by its length.
	if clean, c := sanitizeWithCursor("ab\x1b[2Kcd", 8); clean != "abcd" || c != 4 {
		t.Errorf("stripped mapping = (%q, %d), want (abcd, 4)", clean, c)
	}
	// A cursor inside stripped text parks at the removal point.
	if _, c := sanitizeWithCursor("ab\x1b[2Kcd", 5); c != 2 {
		t.Errorf("parked cursor = %d, want 2", c)
	}
	// SGR styling survives, so it never disturbs the cursor either.
	if clean, c := sanitizeWithCursor("a\x1b[31mb", 4); !strings.Contains(clean, "\x1b[31m") || c != 4 {
		t.Errorf("sgr mapping = (%q, %d), want colors kept, cursor 4", clean, c)
	}
}

func TestToolOutputWithControlsRendersSafely(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 60, Height: 30})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "building\x1b[2K\r50%\r\x1b[1Adone\nok", At: now})
	out := Render(s)
	if strings.Contains(out, "\x1b[1A") || strings.Contains(out, "\x1b[2K") || strings.Contains(out, "\r") {
		t.Errorf("cursor control reached the terminal:\n%q", out)
	}
	stripped := stripForTest(t, out)
	for _, want := range []string{"building", "50%", "done", "ok"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("tool text %q lost:\n%s", want, stripped)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 60 {
			t.Errorf("line %d overflows: %q", i, line)
		}
	}
}

func TestHeaderTruncatesLongModel(t *testing.T) {
	s := New()
	s.Status.Model = "provider/" + strings.Repeat("m", 200)
	s.Apply(TerminalResized{Width: 80, Height: 24})
	lines := strings.Split(Render(s), "\n")
	if CellWidth(lines[0]) > 80 {
		t.Errorf("header soft-wraps the terminal: %d cells: %q", CellWidth(lines[0]), lines[0])
	}
	if !strings.Contains(lines[0], "ohgo") {
		t.Errorf("truncated header lost identity: %q", lines[0])
	}
}

func TestMiniFitsNarrowScreen(t *testing.T) {
	s := New()
	s.Status.Model = "provider/" + strings.Repeat("m", 100)
	s.Apply(ComposerChanged{Text: strings.Repeat("i", 100), Cursor: 100})
	s.Apply(TerminalResized{Width: 30, Height: 10})
	out := Render(s)
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 30 {
			t.Errorf("mini line %d = %d cells, want no soft-wrap: %q", i, CellWidth(line), line)
		}
	}
	if !strings.Contains(out, "too small") {
		t.Errorf("mini lost its notice: %q", out)
	}
}

func TestQueuedMultilinePromptStaysSingleRow(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(MessageQueued{Text: "line one\nline two\x1b[2K"})
	out := Render(s)
	if got := len(strings.Split(out, "\n")); got != 24 {
		t.Errorf("rendered %d lines, want exactly 24", got)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "queued") {
			found = true
		}
		if strings.Contains(line, "\x1b[2K") || strings.Contains(line, "line two") && strings.Contains(line, "\n") {
			t.Errorf("queue row split or leaked control: %q", line)
		}
	}
	if !found {
		t.Error("composer lost the queue state")
	}
}

func TestComposerPasteWithANSIKeepsCursor(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	pasted := "see \x1b[31merror\x1b[0m here"
	s.Apply(ComposerChanged{Text: pasted, Cursor: len([]rune(pasted))})
	out := Render(s)
	if got := strings.Count(out, "▌"); got != 1 {
		t.Errorf("cursor markers = %d, want exactly one:\n%s", got, out)
	}
	// Uniform policy: SGR colors survive everywhere, movement dies. The
	// cursor still lands at the end, after the styled word.
	stripped := stripForTest(t, out)
	if !strings.Contains(stripped, "error") || !strings.Contains(stripped, "here") {
		t.Errorf("pasted text lost:\n%s", stripped)
	}
	if strings.Contains(out, "\x1b[2K") || strings.Contains(out, "\x1b[1A") {
		t.Errorf("movement sequences reached the composer")
	}
	// The stored draft stays raw for editing; only the display is cleaned.
	if s.Input != pasted {
		t.Errorf("sanitizer mutated stored input: %q", s.Input)
	}
	for _, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 80 {
			t.Errorf("composer overflow: %q", line)
		}
	}
}
