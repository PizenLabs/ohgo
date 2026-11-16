package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// shimmerHasRGB reports whether rendered carries the exact truecolor
// components for hex, independent of the bold prefix the wave may add.
func shimmerHasRGB(rendered, hex string) bool {
	r, g, b := parseHexColor(hex)
	seq := "38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
	return strings.Contains(rendered, seq)
}

// TestShimmerTextKeepsVisibleLength: the wave must never shift layout, so the
// stripped text is byte-identical to the input at every tick.
func TestShimmerTextKeepsVisibleLength(t *testing.T) {
	label := "Responding…"
	for tick := 0; tick < len([]rune(label))+int(shimmerPauseCells)+2; tick++ {
		got := ShimmerText(label, tick)
		if stripped := ansi.Strip(got); stripped != label {
			t.Fatalf("tick %d: stripped = %q, want %q", tick, stripped, label)
		}
		if CellWidth(got) != CellWidth(label) {
			t.Fatalf("tick %d: width %d, want %d", tick, CellWidth(got), CellWidth(label))
		}
	}
}

// TestShimmerTextMovesHighlight: successive frames differ (the light moves)
// while the visible text stays identical.
func TestShimmerTextMovesHighlight(t *testing.T) {
	label := "Responding…"
	first := ShimmerText(label, 0)
	second := ShimmerText(label, 1)
	if first == second {
		t.Fatal("ticks 0 and 1 rendered identically, want the wave to move")
	}
	if ansi.Strip(first) != label || ansi.Strip(second) != label {
		t.Fatalf("stripped frames lost the label: %q %q", ansi.Strip(first), ansi.Strip(second))
	}
	// The bright centre must appear and must come from the active palette.
	if !shimmerHasRGB(first, DarkTokens().TextPrimary) {
		t.Fatalf("tick 0 missing the bright highlight: %q", first)
	}
	light := ShimmerTextForTheme(label, 0, ThemeLight)
	if !shimmerHasRGB(light, LightTokens().TextPrimary) {
		t.Fatalf("light tick 0 missing the bright highlight: %q", light)
	}
	if shimmerHasRGB(light, LightTokens().BorderSubtle) {
		t.Fatalf("light shimmer borrowed a border role: %q", light)
	}
}

// TestShimmerTextEmptyStaysEmpty: no input, no output, no panic on any tick.
func TestShimmerTextEmptyStaysEmpty(t *testing.T) {
	for _, tick := range []int{-5, 0, 7} {
		if got := ShimmerText("", tick); got != "" {
			t.Fatalf("tick %d: empty label rendered %q", tick, got)
		}
	}
}

// TestShimmerTextNegativeTickNormalises: a negative tick wraps into the cycle
// instead of panicking or dropping the highlight.
func TestShimmerTextNegativeTickNormalises(t *testing.T) {
	label := "Responding…"
	got := ShimmerText(label, -1)
	if ansi.Strip(got) != label {
		t.Fatalf("stripped = %q, want %q", ansi.Strip(got), label)
	}
}

// TestShimmerCosineFalloffBoldsOnlyThePeak: the wave is a cosine window, not a
// hard 3-cell block. Only characters above the intensity threshold gain weight,
// and characters beyond the radius keep the dim base colour.
func TestShimmerCosineFalloffBoldsOnlyThePeak(t *testing.T) {
	label := "abcdefghij"
	frame := shimmerText(label, 3.0, ThemeDark)
	if ansi.Strip(frame) != label {
		t.Fatalf("stripped = %q, want %q", ansi.Strip(frame), label)
	}
	// At radius 5 and threshold 0.65 the head plus two neighbours on each
	// side are bold; a discrete window would light exactly one fixed cell.
	if got := strings.Count(frame, "\x1b[1;"); got != 5 {
		t.Fatalf("bold characters = %d, want the five above 0.65", got)
	}
	if !shimmerHasRGB(frame, DarkTokens().TextMuted) {
		t.Fatalf("frame lost the dim base beyond the radius: %q", frame)
	}
	if !shimmerHasRGB(frame, DarkTokens().TextPrimary) {
		t.Fatalf("frame lost the glowing peak: %q", frame)
	}
}

// TestShimmerAcceptsContinuousCoordinates: the head is a float, so a sub-cell
// move changes the gradient without changing the visible text.
func TestShimmerAcceptsContinuousCoordinates(t *testing.T) {
	label := "Responding…"
	first := shimmerText(label, 2.0, ThemeDark)
	second := shimmerText(label, 2.1, ThemeDark)
	if first == second {
		t.Fatal("sub-cell head movement rendered identically")
	}
	if ansi.Strip(first) != label || ansi.Strip(second) != label {
		t.Fatalf("continuous frames lost the label: %q %q", ansi.Strip(first), ansi.Strip(second))
	}
}
