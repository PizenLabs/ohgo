package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestShimmerTextKeepsVisibleLength: the wave must never shift layout, so the
// stripped text is byte-identical to the input at every tick.
func TestShimmerTextKeepsVisibleLength(t *testing.T) {
	label := "Responding…"
	for tick := 0; tick < len([]rune(label))+shimmerPauseTicks+2; tick++ {
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
	// The bright center must appear: #E5E9F0 is 229,233,240.
	if !strings.Contains(first, "229;233;240") {
		t.Fatalf("tick 0 missing the bright highlight: %q", first)
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
