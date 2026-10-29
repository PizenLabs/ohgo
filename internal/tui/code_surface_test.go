package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The fenced-code surface must blend into the deep-forest transcript instead
// of reading as a separate gray box. Guard the design property — dark and
// green-dominant — rather than only the literal, so a retune inside the
// palette stays legal while a return to a neutral gray surface does not.
func TestDarkCodeSurfaceIsForestTinted(t *testing.T) {
	if got := DarkPalette().Code.GetBackground(); got != lipgloss.Color(darkCodeBg) {
		t.Fatalf("code background = %v, want the palette surface %s", got, darkCodeBg)
	}

	hex := strings.TrimPrefix(darkCodeBg, "#")
	if len(hex) != 6 {
		t.Fatalf("code background %q is not a six-digit hex colour", darkCodeBg)
	}
	channel := func(s string) int {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			t.Fatalf("code background %q is not hex: %v", darkCodeBg, err)
		}
		return int(v)
	}
	r, g, b := channel(hex[0:2]), channel(hex[2:4]), channel(hex[4:6])
	if g <= r || g <= b {
		t.Errorf("code background %q is not emerald-tinted: r=%d g=%d b=%d", darkCodeBg, r, g, b)
	}
	luminance := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	if luminance > 40 {
		t.Errorf("code background %q is too bright (luminance %.1f), want a subtle surface", darkCodeBg, luminance)
	}
}
