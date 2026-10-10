package tui

import (
	"strings"
	"testing"
)

// The markdown renderer covers chat prose, not documents: fences, inline
// code, headers, emphasis, lists, quotes, rules. Anything else passes through.

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

func TestMarkdownPassthroughForTables(t *testing.T) {
	// Tables are outside the ported subset: they pass through untouched
	// rather than mangled.
	in := "| a | b |\n|---|---|\n| 1 | 2 |"
	if out := RenderMarkdown(in, DarkPalette()); !strings.Contains(out, "| a | b |") {
		t.Errorf("table = %q, want it passed through", out)
	}
}

func TestMarkdownNilPaletteRenders(t *testing.T) {
	if out := RenderMarkdown("hi", nil); !strings.Contains(out, "hi") {
		t.Errorf("nil palette = %q, want the dark default, not a panic", out)
	}
}
