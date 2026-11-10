package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/internal/tui/design"
	"github.com/muesli/termenv"
)

// shimmerPauseTicks parks the highlight off-label between sweeps so the wave
// reads as a pass rather than a constant glow.
const shimmerPauseTicks = 4

// ShimmerText renders label with a moving 3-cell highlight window using the
// dark appearance. It is the historical entry point for frozen callers; live
// surfaces call ShimmerTextForTheme so the wave stays on the current palette.
func ShimmerText(label string, tick int) string {
	return ShimmerTextForTheme(label, tick, ThemeDark)
}

// ShimmerTextForTheme resolves the sweep from the effective tokens. On the
// light appearance the highlight moves from muted slate toward body text
// rather than toward a near-white that would disappear on a bright screen.
//
// It is pure: the same label, tick, and theme always produce the same bytes,
// with no clock, I/O, or global state. The stripped width always equals the
// input width, so the activity line never shifts layout as the wave moves.
func ShimmerTextForTheme(label string, tick int, theme string) string {
	runes := []rune(label)
	n := len(runes)
	if n == 0 {
		return ""
	}
	tok := design.For(theme)
	baseHex := tok.TextMuted
	midHex := tok.Brand
	brightHex := tok.TextPrimary
	cycle := n + shimmerPauseTicks
	t := tick % cycle
	if t < 0 {
		t += cycle
	}
	center := -1 << 30
	if t < n {
		center = t
	}
	var sb strings.Builder
	sb.Grow(len(label) + n*16)
	for i, r := range runes {
		hex := baseHex
		switch i {
		case center:
			hex = brightHex
		case center - 1, center + 1:
			hex = midHex
		}
		sb.WriteString(truecolorFg(hex, string(r)))
	}
	return sb.String()
}

// shimmerSupported reports whether the wave should run. Reduced motion,
// NO_COLOR, dumb terminals, and non-TrueColor profiles all fall back to the
// plain palette without shimmer. It is deliberately impure (env + terminal
// probe), so pure renderers never call it: the view seam checks once and
// picks the plain or the tick path.
func shimmerSupported() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if term := os.Getenv("TERM"); term == "dumb" {
		return false
	}
	if os.Getenv("REDUCED_MOTION") != "" || os.Getenv("NO_ANIMATION") != "" {
		return false
	}
	return termenv.ColorProfile() == termenv.TrueColor
}

func truecolorFg(hex, s string) string {
	r, g, b := parseHexColor(hex)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, s)
}

func parseHexColor(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 255, 255, 255
	}
	var r, g, b int
	_, _ = fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}
