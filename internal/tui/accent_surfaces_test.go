package tui

import (
	"strings"
	"testing"
	"time"
)

// A tool-run phase is the one structural anchor on an otherwise quiet
// telemetry row. It takes the context-blue accent, while counts and durations
// stay muted; the row must not read as one flat colour.
func TestToolRunPhaseUsesContextAccent(t *testing.T) {
	pal := forcedPalette()
	start := time.Now().Add(-2 * time.Second)
	run := &toolRun{
		phaseStart: "BUILD",
		phaseEnd:   "BUILD",
		members: []*ToolEntry{
			{ID: "1", Name: "read", Status: ToolDone, Invocation: "read a.go", StartedAt: start, EndedAt: start.Add(time.Second)},
			{ID: "2", Name: "read", Status: ToolDone, Invocation: "read b.go", StartedAt: start.Add(time.Second), EndedAt: start.Add(2 * time.Second)},
		},
	}
	var sb strings.Builder
	renderToolRun(&sb, run, pal, Layout{ContentWidth: 80}, 0, "", ThemeDark, time.Now(), nil)
	out := sb.String()

	if !strings.Contains(out, pal.Cyan.Render("[Phase: BUILD]")) {
		t.Fatalf("phase span did not use the context accent: %q", out)
	}
	if strings.Contains(out, pal.Status.Render("[Phase: BUILD]")) {
		t.Fatalf("phase span stayed muted: %q", out)
	}
	if got := strings.Join(strings.Fields(stripANSI(out)), " "); !strings.Contains(got, "2 tools") {
		t.Fatalf("phase row lost its metrics: %q", got)
	}
}

// A review waiting on a human is a warning, not quiet telemetry: the header
// and its reason take the warning role so the stop is visible in a long
// transcript.
func TestNeedsReviewTaskUsesWarningRole(t *testing.T) {
	forceTrueColor(t)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "NEEDS_REVIEW", Objective: "prove the parser",
		ReviewReason: "intent grounding: the answer treats the test as absent",
	}})
	// The entrance fade owns the first 120ms; settle it so the assertion reads
	// the semantic palette rather than the transient blend.
	for i := range s.Entries {
		s.Entries[i].AppearedAt = time.Time{}
	}
	out := Render(s)

	warning := rgbSeq(DarkTokens().Warning)
	if !strings.Contains(out, "\x1b[38;2;"+warning+"mtask") {
		t.Fatalf("review header did not use the warning role:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;"+warning+"m  ⎿ intent grounding") {
		t.Fatalf("review reason did not use the warning role:\n%q", out)
	}
}

// The sticky narrow-terminal tracker is a separate surface from the transcript
// task block; it must carry the same warning role for a review stop.
func TestNeedsReviewStickyTaskUsesWarningRole(t *testing.T) {
	forceTrueColor(t)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "NEEDS_REVIEW", Objective: "prove the parser",
		Items: []TaskItem{{State: taskStateActive, Label: "run the suite"}},
	}})
	s.Entries[len(s.Entries)-1].AppearedAt = time.Time{}
	out := Render(s)

	warning := rgbSeq(DarkTokens().Warning)
	if !strings.Contains(out, "\x1b[38;2;"+warning+"mtask · prove the parser") {
		t.Fatalf("sticky review row did not use the warning role:\n%q", out)
	}
}
