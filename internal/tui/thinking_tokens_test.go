package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderThinkingTokensFormatCleanly: a streaming state with counted
// tokens renders the token segment between the label and the timer, and a
// zero count renders no token telemetry at all.
func TestRenderThinkingTokensFormatCleanly(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Second)

	withTokens := ThinkingState{
		Active: true, Phase: ThinkingModel, Label: "Responding…",
		StartedAt: start, Interruptible: true, OutputTokens: 248,
	}
	line := RenderThinking(withTokens, now, 0)[0]
	for _, want := range []string{"Responding", "248 tokens", "5s", "esc"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line = %q, want %q", line, want)
		}
	}

	withoutTokens := ThinkingState{
		Active: true, Phase: ThinkingModel, Label: "Responding…",
		StartedAt: start, Interruptible: true,
	}
	plain := RenderThinking(withoutTokens, now, 0)[0]
	if strings.Contains(strings.ToLower(plain), "token") {
		t.Fatalf("line = %q, want no token segment when count is zero", plain)
	}
	for _, want := range []string{"Responding", "5s", "esc"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("line = %q, want %q", plain, want)
		}
	}
}

// TestProjectThinkingCarriesStreamTokens: streamed deltas counted in Apply
// reach the projection, so the line reports what arrived.
func TestProjectThinkingCarriesStreamTokens(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "go", At: time.Now()})
	if got := ProjectThinking(s); got.OutputTokens != 0 {
		t.Fatalf("working tokens = %d, want 0 before any delta", got.OutputTokens)
	}
	s.Apply(AssistantDelta{Text: "hello world, this is streamed text"})
	got := ProjectThinking(s)
	if got.Label != "Responding…" {
		t.Fatalf("label = %q, want Responding… while streaming", got.Label)
	}
	if got.OutputTokens <= 0 {
		t.Fatalf("streaming tokens = %d, want positive after a delta", got.OutputTokens)
	}
	line := RenderThinking(got, time.Now(), 80)[0]
	if !strings.Contains(line, "tokens") {
		t.Fatalf("line = %q, want the counted token segment", line)
	}
}

// TestStateOutputTokensResetEachTurn: a new turn starts from zero, so the
// previous answer's count can never leak into the next thinking line.
func TestStateOutputTokensResetEachTurn(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "first"})
	s.Apply(AssistantDelta{Text: "some streamed answer text here"})
	if s.OutputTokens <= 0 {
		t.Fatalf("tokens = %d, want positive after streaming", s.OutputTokens)
	}
	s.Apply(UserSubmitted{Text: "second"})
	if s.OutputTokens != 0 {
		t.Fatalf("tokens = %d after new turn, want reset to 0", s.OutputTokens)
	}
}

// TestRenderThinkingTickKeepsWidth: the compact line never exceeds width,
// measured in cells with ANSI stripped. The tail (timer + esc) survives
// narrowing because the head yields first; tokens live in the head block, so
// they persist at widths that fit and trim before the timer does.
func TestRenderThinkingTickKeepsWidth(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Second)
	state := ThinkingState{
		Active: true, Phase: ThinkingModel, Label: "Responding…",
		StartedAt: start, Interruptible: true, OutputTokens: 248,
	}
	for _, width := range []int{20, 40, 80} {
		line := RenderThinkingTick(state, now, 3, width)[0]
		if got := CellWidth(line); got > width {
			t.Fatalf("width %d: shimmered line is %d cells: %q", width, got, ansi.Strip(line))
		}
		if !strings.Contains(ansi.Strip(line), "5s") {
			t.Fatalf("width %d: stripped line = %q, want the timer kept", width, ansi.Strip(line))
		}
	}
	for _, width := range []int{40, 80} {
		line := RenderThinkingTick(state, now, 3, width)[0]
		if !strings.Contains(ansi.Strip(line), "248 tokens") {
			t.Fatalf("width %d: stripped line = %q, want tokens kept", width, ansi.Strip(line))
		}
	}
}

// TestFormatThinkingLineIsCompact: the row is a tight inline block with the
// metadata parenthesised directly above the composer divider, never padded to
// fill the terminal. A wide terminal shows the same cohesive row as a narrow
// one.
func TestFormatThinkingLineIsCompact(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Second)
	state := ThinkingState{
		Active: true, Phase: ThinkingModel, Label: "Responding…",
		StartedAt: start, Interruptible: true, OutputTokens: 248,
	}
	if got, want := RenderThinking(state, now, 0)[0], "✳ Responding… · (↓248 tokens · 5s · esc)"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	wide := RenderThinking(state, now, 80)[0]
	if got := CellWidth(wide); got >= 80 {
		t.Fatalf("wide line is %d cells, want a compact row well under width: %q", got, wide)
	}
	if strings.HasSuffix(wide, " ") {
		t.Fatalf("wide line = %q, want no fill padding", wide)
	}
	// Shimmer moves light across the label only: every frame strips to the
	// same compact text, so metadata never shifts.
	plain := ansi.Strip(RenderThinkingTick(state, now, 0, 80)[0])
	for tick := 1; tick < 8; tick++ {
		if framed := ansi.Strip(RenderThinkingTick(state, now, tick, 80)[0]); framed != plain {
			t.Fatalf("tick %d: stripped = %q, want %q", tick, framed, plain)
		}
	}
}
