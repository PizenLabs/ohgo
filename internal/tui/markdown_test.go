package tui

import (
	"strings"
	"testing"
)

// The markdown renderer covers chat prose, not documents: fences, inline
// code, headers, emphasis, lists, quotes, rules, and tables, which render
// as grids with a stacked narrow fallback.

func TestMarkdownFenceMarkersAreNotContent(t *testing.T) {
	out := RenderMarkdown("```go\nfmt.Println()\n```", DarkPalette())
	if strings.Contains(out, "```") {
		t.Errorf("fence = %q, want both delimiter rows dropped", out)
	}
	if !strings.Contains(out, "fmt.Println()") {
		t.Errorf("fence = %q, want the body kept", out)
	}
}

// The info string is metadata for the highlighter, not a line of the answer:
// the language must still colour the body while never being printed.
func TestMarkdownFenceLanguageDrivesHighlightingOnly(t *testing.T) {
	pal := forcedPalette()
	out := RenderMarkdown("```rust\nfn main() {}\n```", pal)
	if strings.Contains(out, "```") || strings.Contains(out, "rust") {
		t.Errorf("fence = %q, want the language label and delimiters dropped", out)
	}
	if !strings.Contains(out, pal.CodeKeyword.Render("fn")) {
		t.Errorf("fence = %q, want rust keywords highlighted from the info string", out)
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

// A fenced code block indented under a bullet is still a code block. Consuming
// its delimiter as item prose printed the backticks and lost the code styling
// and wrapping; the model uses this shape for every inline example.
func TestMarkdownFenceInsideListItemStaysCode(t *testing.T) {
	pal := forcedPalette()
	in := "- **Go Code**:\n" +
		"  ```go\n" +
		"  package main\n" +
		"  ```\n" +
		"- **After**: the list continues\n"
	out := RenderMarkdownWrapped(in, pal, 40)
	plain := stripForTest(t, out)
	if strings.Contains(plain, "```") {
		t.Errorf("fence delimiters leaked into the transcript: %q", plain)
	}
	if !strings.Contains(plain, "package main") {
		t.Errorf("fence body was lost: %q", plain)
	}
	if !strings.Contains(out, pal.CodeKeyword.Render("package")) {
		t.Errorf("fence body lost its code styling: %q", plain)
	}
	if !strings.Contains(plain, "• After") {
		t.Errorf("the list did not continue after the fence: %q", plain)
	}
}

// A wrapped source bullet is a continuation of its item, not a new
// paragraph. Joining it after the item was already wrapped bypasses the width
// budget: the terminal then truncates the oversized row and the tail of the
// model's answer disappears. The continuation must join the body first so it
// wraps and keeps its inline styling.
func TestMarkdownListContinuationJoinsBeforeWrapping(t *testing.T) {
	pal := forcedPalette()
	const width = 46
	in := "5. Example: a comparison\n" +
		"  • Java Application: `public class HelloWorld` with a long body that must wrap before the terminal edge\n" +
		"  • Go Application: `package main` with another long body that must wrap before the terminal edge\n"
	out := RenderMarkdownWrapped(in, pal, width)

	for i, line := range strings.Split(out, "\n") {
		if got := CellWidth(line); got > width {
			t.Errorf("row %d = %d cells, want <= %d: %q", i, got, width, stripForTest(t, line))
		}
	}
	plain := stripForTest(t, out)
	for _, want := range []string{"Java Application", "HelloWorld", "terminal edge", "Go Application", "package main"} {
		if !strings.Contains(plain, want) {
			t.Errorf("continuation content %q was truncated: %q", want, plain)
		}
	}
	if !strings.Contains(out, pal.InlineCode.Render("package")) || !strings.Contains(out, pal.InlineCode.Render("main")) {
		t.Errorf("continuation lost its inline code styling: %q", plain)
	}
}

// An ordered marker is not always one cell: a short placeholder ("X ") let
// the rebuilt first row grow past the budget when the real marker was "10."
// or wider, and the terminal cut its last cell.
func TestMarkdownOrderedMarkerFitsTheFirstRow(t *testing.T) {
	const width = 30
	in := "10. first item with a long body that must wrap and keep wrapping\n" +
		"    continuation text that must also fit the width\n"
	out := RenderMarkdownWrapped(in, DarkPalette(), width)
	for i, line := range strings.Split(out, "\n") {
		if got := CellWidth(line); got > width {
			t.Errorf("row %d = %d cells, want <= %d: %q", i, got, width, stripForTest(t, line))
		}
	}
	if plain := stripForTest(t, out); !strings.Contains(plain, "continuation") || !strings.Contains(plain, "width") {
		t.Errorf("ordered item lost its continuation: %q", plain)
	}
}

func TestMarkdownTableRendersGrid(t *testing.T) {
	// Tables render as a solid-border grid, not raw pipes: the top rule,
	// the header row, the header separator, the body row, and the bottom
	// rule each own a row.
	in := "| a | b |\n|---|---|\n| 1 | 2 |"
	out := stripForTest(t, RenderMarkdown(in, DarkPalette()))
	for _, want := range []string{"┌───┬───┐", "│ a │ b │", "├───┼───┤", "│ 1 │ 2 │", "└───┴───┘"} {
		if !strings.Contains(out, want) {
			t.Errorf("table = %q, want grid row %q", out, want)
		}
	}
}

func TestMarkdownTableAlignment(t *testing.T) {
	// The delimiter row owns alignment: `:---` left, `:--:` center,
	// `---:` right. Padding is spaces around display cells, so a
	// right-aligned number visibly shifts and centering splits the slack.
	// Borders are solid │, not raw pipes.
	in := "| left | center | right |\n|:---|:---:|---:|\n| a | b | c |"
	out := stripForTest(t, RenderMarkdownWrapped(in, DarkPalette(), 40))
	for _, want := range []string{"│ a    │", "│   b    │", "│     c │"} {
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
	if !strings.Contains(out, "│ x|y │ 2 │") {
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
	if strings.Contains(out, "┌") || strings.Contains(out, "┼") {
		t.Errorf("table = %q, want no grid borders in stacked form", out)
	}
}

func TestMarkdownNilPaletteRenders(t *testing.T) {
	if out := RenderMarkdown("hi", nil); !strings.Contains(out, "hi") {
		t.Errorf("nil palette = %q, want the dark default, not a panic", out)
	}
}
