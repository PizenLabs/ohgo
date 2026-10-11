package tui

import (
	"strings"
	"testing"
)

// The markdown renderer covers chat prose, not documents: fences, inline
// code, headers, emphasis, lists, quotes, rules, and tables, which render
// as grids with a stacked narrow fallback.

func TestMarkdownFenceKeepsTheLanguage(t *testing.T) {
	out := RenderMarkdown("```go\nfmt.Println()\n```", DarkPalette())
	if !strings.Contains(out, "```go") || !strings.Contains(out, "fmt.Println()") {
		t.Errorf("fence = %q, want the language label and body kept", out)
	}
}

func TestMarkdownUnclosedFenceStylesToTheEnd(t *testing.T) {
	// A model that never closes its fence still gets its code styled rather
	// than dropped: best-effort beats strict.
	out := RenderMarkdown("```py\nx = 1", DarkPalette())
	if !strings.Contains(out, "x = 1") {
		t.Errorf("unclosed fence = %q, want the body kept", out)
	}
}

func TestMarkdownInlineCodeAndBold(t *testing.T) {
	out := RenderMarkdown("run `make check` and **do not** skip", DarkPalette())
	for _, want := range []string{"make check", "do not"} {
		if !strings.Contains(out, want) {
			t.Errorf("inline = %q, want %q kept", out, want)
		}
	}
}

func TestMarkdownUnclosedMarkersStayLiteral(t *testing.T) {
	// Stray asterisks are model output, not markup: an unclosed marker must
	// survive rather than eat the rest of the line.
	out := RenderMarkdown("a **bold move and 2*3 math", DarkPalette())
	if !strings.Contains(out, "**bold move") || !strings.Contains(out, "2*3") {
		t.Errorf("unclosed markers = %q, want them literal", out)
	}
}

func TestMarkdownHeadersBulletsQuotes(t *testing.T) {
	out := RenderMarkdown("# Title\n- one\n1. two\n> quoted\n---", DarkPalette())
	for _, want := range []string{"Title", "•", "one", "two", "quoted", "─ ─ ─"} {
		if !strings.Contains(out, want) {
			t.Errorf("prose = %q, want %q rendered", out, want)
		}
	}
}

func TestMarkdownTableRendersGrid(t *testing.T) {
	// Tables render as an aligned grid, not raw pipes: the header,
	// the separator, and padded body cells each own a row.
	in := "| a | b |\n|---|---|\n| 1 | 2 |"
	out := stripForTest(t, RenderMarkdown(in, DarkPalette()))
	for _, want := range []string{"| a | b |", "|---|---|", "| 1 | 2 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("table = %q, want grid row %q", out, want)
		}
	}
}

func TestMarkdownTableAlignment(t *testing.T) {
	// The delimiter row owns alignment: `:---` left, `:--:` center,
	// `---:` right. Padding is spaces around display cells, so a
	// right-aligned number visibly shifts and centering splits the slack.
	in := "| left | center | right |\n|:---|:---:|---:|\n| a | b | c |"
	out := stripForTest(t, RenderMarkdownWrapped(in, DarkPalette(), 40))
	for _, want := range []string{"| a    |", "|   b    |", "|     c |"} {
		if !strings.Contains(out, want) {
			t.Errorf("table = %q, want aligned cell %q", out, want)
		}
	}
}

func TestMarkdownTableEscapedPipe(t *testing.T) {
	// A backslash-escaped pipe stays inside its cell: splitting on it
	// would corrupt the grid, and leaking the backslash would corrupt
	// the content.
	in := "| a | b |\n|---|---|\n| x\\|y | 2 |"
	out := stripForTest(t, RenderMarkdownWrapped(in, DarkPalette(), 40))
	if !strings.Contains(out, "| x|y | 2 |") {
		t.Errorf("table = %q, want the escaped pipe kept in its cell", out)
	}
	if strings.Contains(out, "\\") {
		t.Errorf("table = %q, want no leaked backslash", out)
	}
}

func TestMarkdownTableStackedFallback(t *testing.T) {
	// Below the minimum column budget the grid yields to "header: value"
	// lines so nothing overflows and no grid corrupts.
	in := "| a | b |\n|---|---|\n| 1 | 2 |"
	out := stripForTest(t, RenderMarkdownWrapped(in, DarkPalette(), 14))
	for _, want := range []string{"a: 1", "b: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("table = %q, want stacked line %q", out, want)
		}
	}
	if strings.Contains(out, "|---|---|") {
		t.Errorf("table = %q, want no grid separator in stacked form", out)
	}
}

func TestMarkdownNilPaletteRenders(t *testing.T) {
	if out := RenderMarkdown("hi", nil); !strings.Contains(out, "hi") {
		t.Errorf("nil palette = %q, want the dark default, not a panic", out)
	}
}
