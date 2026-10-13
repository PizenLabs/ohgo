package tui

import (
	"strings"
	"testing"
	"time"
)

// zoneMarkers substitutes a visible bracket per zone so tests can assert the
// exact color mapping without a terminal. The markers are plain text, so
// zoning tests use unbounded width where no truncation can disturb them.
func zoneMarkers() thinkingZones {
	brack := func(tag string) func(string) string {
		return func(s string) string { return "[" + tag + "]" + s + "[/" + tag + "]" }
	}
	return thinkingZones{
		active:  brack("A"),
		waiting: brack("W"),
		meta:    brack("M"),
		dim:     brack("D"),
		tail:    brack("T"),
		wave:    func(label string, _ int) string { return "[S]" + label + "[/S]" },
	}
}

// TestThinkingZonesSeparateLabelFromMetadata: the live label reads active,
// tokens recede into the muted zone, and punctuation plus tail status sit in
// the dim zone — never one flat block of color.
func TestThinkingZonesSeparateLabelFromMetadata(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Second)
	state := ThinkingState{
		Active: true, Phase: ThinkingModel, Label: "Responding…",
		StartedAt: start, Interruptible: true, OutputTokens: 248,
	}
	got := renderThinkingZoned(state, now, 0, false, 0, zoneMarkers())[0]
	want := "[A]✳[/A] [A]Responding…[/A] [D]·[/D] [M]248 tokens[/M]    [T]5s[/T] [D]·[/D] [T]esc[/T]"
	if got != want {
		t.Fatalf("zoned line = %q, want %q", got, want)
	}
	if stripped := stripZoneMarkers(got); stripped != "✳ Responding… · 248 tokens    5s · esc" {
		t.Fatalf("stripped line = %q, want the compact plain text", stripped)
	}
}

// stripZoneMarkers removes the test bracket tags, mirroring what ansi.Strip
// does for production color codes: layout assertions read plain cells.
func stripZoneMarkers(s string) string {
	r := strings.NewReplacer(
		"[A]", "", "[/A]", "",
		"[W]", "", "[/W]", "",
		"[M]", "", "[/M]", "",
		"[D]", "", "[/D]", "",
		"[T]", "", "[/T]", "",
		"[S]", "", "[/S]", "",
	)
	return r.Replace(s)
}

// TestThinkingZonesParkWaiting: a turn blocked on approval zones the whole
// row muted with no timer and no interrupt hint.
func TestThinkingZonesParkWaiting(t *testing.T) {
	state := ThinkingState{Active: true, Phase: ThinkingWaiting, Label: "Waiting for approval"}
	got := renderThinkingZoned(state, time.Now(), 0, false, 0, zoneMarkers())[0]
	want := "[W]◇[/W] [W]Waiting for approval[/W]"
	if got != want {
		t.Fatalf("zoned line = %q, want %q", got, want)
	}
}

// TestThinkingZonesKeepToolDetailActive: the tool's own summary rides with
// the active head while the timer stays tail status.
func TestThinkingZonesKeepToolDetailActive(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	state := ThinkingState{
		Active: true, Phase: ThinkingTool, Label: "Running",
		Detail: "bash go test", StartedAt: start.Add(-12 * time.Second),
		Interruptible: true,
	}
	got := renderThinkingZoned(state, start, 0, false, 0, zoneMarkers())[0]
	want := "[A]✳[/A] [A]Running[/A] [A]bash go test[/A]    [T]12s[/T] [D]·[/D] [T]esc[/T]"
	if got != want {
		t.Fatalf("zoned line = %q, want %q", got, want)
	}
}

// TestThinkingZonesConfineShimmerToLabel: the wave wraps only the label, so
// every frame zones identically and metadata never shifts.
func TestThinkingZonesConfineShimmerToLabel(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Second)
	state := ThinkingState{
		Active: true, Phase: ThinkingModel, Label: "Responding…",
		StartedAt: start, Interruptible: true, OutputTokens: 248,
	}
	first := renderThinkingZoned(state, now, 0, true, 0, zoneMarkers())[0]
	for tick := 1; tick < 8; tick++ {
		if framed := renderThinkingZoned(state, now, tick, true, 0, zoneMarkers())[0]; framed != first {
			t.Fatalf("tick %d: zoned = %q, want %q", tick, framed, first)
		}
	}
	if !strings.Contains(first, "[S]Responding…[/S]") {
		t.Fatalf("zoned line = %q, want the wave confined to the label", first)
	}
	if strings.Contains(first, "[S]5s") || strings.Contains(first, "tokens[/S]") {
		t.Fatalf("zoned line = %q, want no shimmer outside the label", first)
	}
}
