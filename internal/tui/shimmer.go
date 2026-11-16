package tui

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/PizenLabs/ohgo/internal/tui/design"
	"github.com/muesli/termenv"
)

// The wave is a continuous coordinate, not a discrete highlight cell: the view
// advances one frame at the presentation clock's 30 FPS and the head slides a
// fraction of a cell per frame, so the cosine falloff is sampled smoothly.
// A busy window of five cells on each side keeps the glow local while still
// letting neighbouring characters brighten before the peak reaches them.
const (
	// shimmerRadius is the cosine window half-width in character cells.
	shimmerRadius = 5.0
	// shimmerBoldAt is the intensity above which a character gains weight.
	// The peak plus roughly two neighbours cross it at radius 5.
	shimmerBoldAt = 0.65
	// shimmerHeadStep is how many cells the wave head travels per frame.
	// At the 33ms presentation frame this is one cell every ~94ms, matching
	// the spinner's readable cadence without stepping the gradient.
	shimmerHeadStep = 0.35
	// shimmerPauseCells parks the head past the label between sweeps so the
	// wave reads as a pass rather than a permanent glow.
	shimmerPauseCells = 4.0
)

// ShimmerText renders label with a moving cosine wave using the dark
// appearance. It is the historical entry point for frozen callers; live
// surfaces call ShimmerTextForTheme so the wave stays on the current palette.
func ShimmerText(label string, tick int) string {
	return ShimmerTextForTheme(label, tick, ThemeDark)
}

// ShimmerTextForTheme resolves the sweep from the effective tokens. On the
// light appearance the highlight moves from muted slate toward body text
// rather than toward a near-white that would disappear on a bright screen.
//
// tick is a presentation frame, not a spinner step: callers pass the 30 FPS
// frame clock so the float coordinate advances at the designed cadence.
//
// It is pure: the same label, tick, and theme always produce the same bytes,
// with no clock, I/O, or global state. The stripped width always equals the
// input width, so the activity line never shifts layout as the wave moves.
func ShimmerTextForTheme(label string, tick int, theme string) string {
	return shimmerText(label, float64(tick)*shimmerHeadStep, theme)
}

// shimmerText renders one frame at an explicit float64 head position. It is
// the seam tests use to pin a sub-cell coordinate; production ticks derive
// their position from the presentation frame.
func shimmerText(label string, phase float64, theme string) string {
	runes := []rune(label)
	n := len(runes)
	if n == 0 {
		return ""
	}
	tok := design.For(theme)
	baseR, baseG, baseB := parseHexColor(tok.TextMuted)
	highR, highG, highB := parseHexColor(tok.TextPrimary)

	cycle := float64(n) + shimmerPauseCells
	head := math.Mod(phase, cycle)
	if head < 0 {
		head += cycle
	}

	var b strings.Builder
	b.Grow(len(label) + n*20)
	scratch := make([]byte, 0, 8)
	for i, r := range runes {
		intensity := 0.0
		if d := math.Abs(float64(i) - head); d <= shimmerRadius {
			// start at the falling edge: cos(0)=1 at the head and cos(pi)=-1
			// at the window boundary, so every covered character is lit and
			// the falloff never produces a hard seam.
			intensity = 0.5 * (1 + math.Cos(math.Pi*d/shimmerRadius))
		}
		rgbR := roundChannel(float64(baseR) + float64(highR-baseR)*intensity)
		rgbG := roundChannel(float64(baseG) + float64(highG-baseG)*intensity)
		rgbB := roundChannel(float64(baseB) + float64(highB-baseB)*intensity)
		b.WriteString("\x1b[")
		if intensity > shimmerBoldAt {
			b.WriteString("1;")
		}
		b.WriteString("38;2;")
		scratch = strconv.AppendInt(scratch[:0], int64(rgbR), 10)
		b.Write(scratch)
		b.WriteByte(';')
		scratch = strconv.AppendInt(scratch[:0], int64(rgbG), 10)
		b.Write(scratch)
		b.WriteByte(';')
		scratch = strconv.AppendInt(scratch[:0], int64(rgbB), 10)
		b.Write(scratch)
		b.WriteByte('m')
		b.WriteRune(r)
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// roundChannel clamps and rounds an interpolated colour channel.
func roundChannel(v float64) int {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return int(math.Round(v))
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

func parseHexColor(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 255, 255, 255
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 255, 255, 255
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}
