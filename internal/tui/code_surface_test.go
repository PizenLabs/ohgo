package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The fenced-code surface must blend into the midnight transcript instead of
// reading as a separate gray box. Guard the design property — cool-tinted and
// subtle — rather than only the literal, so a retune inside the palette stays
// legal while a return to a neutral or green-gray surface does not.
func TestDarkCodeSurfaceIsCoolTinted(t *testing.T) {
	if got := DarkPalette().Code.GetBackground(); got != lipgloss.Color(DarkTokens().CodeBackground) {
		t.Fatalf("code background = %v, want the palette surface %s", got, DarkTokens().CodeBackground)
	}

	hex := strings.TrimPrefix(DarkTokens().CodeBackground, "#")
	if len(hex) != 6 {
		t.Fatalf("code background %q is not a six-digit hex colour", DarkTokens().CodeBackground)
	}
	channel := func(s string) int {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			t.Fatalf("code background %q is not hex: %v", DarkTokens().CodeBackground, err)
		}
		return int(v)
	}
	r, g, b := channel(hex[0:2]), channel(hex[2:4]), channel(hex[4:6])
	if b <= r || b <= g {
		t.Errorf("code background %q is not cool-tinted: r=%d g=%d b=%d", DarkTokens().CodeBackground, r, g, b)
	}
	luminance := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	if luminance > 40 {
		t.Errorf("code background %q is too bright (luminance %.1f), want a subtle surface", DarkTokens().CodeBackground, luminance)
	}
}

// The light code surface is a soft porcelain tint, never an opaque black block
// or an inverted span: an inline identifier must stay readable on a bright
// terminal.
func TestLightCodeSurfaceIsPorcelainTinted(t *testing.T) {
	pal := LightPalette()
	if got := pal.Code.GetBackground(); got != lipgloss.Color(LightTokens().CodeBackground) {
		t.Fatalf("light code background = %v, want %s", got, LightTokens().CodeBackground)
	}
	if got := pal.Code.GetForeground(); got != lipgloss.Color(LightTokens().CodeForeground) {
		t.Fatalf("light code foreground = %v, want %s", got, LightTokens().CodeForeground)
	}
	if got := pal.Code.GetBackground(); got == lipgloss.Color("#000000") {
		t.Fatal("light code background is opaque black")
	}
}

// Every surfaced code token shares a background from the palette, and no
// theme may fall back to opaque black or reverse video: either artifact
// becomes a black block on a light terminal.
func TestCodeTokensNeverUseBlackOrInverse(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		pal := PaletteFor(theme)
		tokens := map[string]lipgloss.Style{
			"code":     pal.Code,
			"keyword":  pal.CodeKeyword,
			"string":   pal.CodeString,
			"comment":  pal.CodeComment,
			"number":   pal.CodeNumber,
			"diff add": pal.DiffAdd,
		}
		for name, token := range tokens {
			if bg := token.GetBackground(); bg == lipgloss.Color("#000000") || bg == lipgloss.Color("0") {
				t.Errorf("%s %s background = %v, want a semantic surface", theme, name, bg)
			}
			if token.GetReverse() {
				t.Errorf("%s %s uses reverse video, which breaks on a bright terminal", theme, name)
			}
		}
	}
}
