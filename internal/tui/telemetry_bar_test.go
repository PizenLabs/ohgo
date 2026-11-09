package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The bar's phrase is derived from the phase the runtime can prove, not from a
// generic "working" fallback. Each transition names what is actually happening.
func TestLiveTelemetryBarNamesEveryRuntimePhase(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	base := func() State {
		s := New()
		s.Apply(TerminalResized{Width: 100, Height: 30})
		s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
		return s
	}
	tests := []struct {
		name  string
		state func() State
		want  string
	}{
		{
			name:  "waiting for the first token",
			state: base,
			want:  "preparing workspace…",
		},
		{
			name: "open reasoning trace",
			state: func() State {
				s := base()
				s.Apply(ReasoningDelta{Text: "inspect", At: at.Add(time.Millisecond), Seq: 1})
				return s
			},
			want: "reasoning internals…",
		},
		{
			name: "running tool",
			state: func() State {
				s := base()
				s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: at, Seq: 1})
				return s
			},
			want: "executing tools…",
		},
		{
			name: "streaming answer",
			state: func() State {
				s := base()
				s.Apply(AssistantDelta{Text: "hello", At: at.Add(time.Millisecond), Seq: 1})
				return s
			},
			want: "formatting response…",
		},
		{
			name: "blocked on approval",
			state: func() State {
				s := base()
				s.Apply(ApprovalAsked{ToolName: "read", Summary: "read a.go", Choices: []string{"Yes", "No"}})
				return s
			},
			want: "waiting for approval…",
		},
		{
			name: "commit proposal",
			state: func() State {
				return State{Commit: &CommitActivity{Phase: CommitProposing, StartedAt: at}}
			},
			want: "preparing commit…",
		},
		{
			name: "commit execution",
			state: func() State {
				return State{Commit: &CommitActivity{Phase: CommitExecuting, StartedAt: at}}
			},
			want: "executing commit…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := projectLiveTelemetry(tt.state())
			if !bar.Active {
				t.Fatal("phase has no telemetry row")
			}
			if bar.Label != tt.want {
				t.Fatalf("label = %q, want %q", bar.Label, tt.want)
			}
		})
	}

	if idle := projectLiveTelemetry(New()); idle.Active {
		t.Fatalf("idle runtime claimed a live telemetry row: %+v", idle)
	}
}

// When a reasoning stream closes into its collapsible Thought block and no
// later activity has taken over, the bar clears its content immediately. The
// reserved slot is untouched, so the composer cannot move.
func TestLiveTelemetryClearsWhenReasoningSettles(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(ReasoningDelta{Text: "inspect", At: at.Add(time.Millisecond), Seq: 1})
	if !projectLiveTelemetry(s).Active {
		t.Fatal("open reasoning trace has no telemetry row")
	}
	s.closeReasoning(at.Add(time.Second))
	if bar := projectLiveTelemetry(s); bar.Active {
		t.Fatalf("settled Thought block kept telemetry active: %+v", bar)
	}
	if got := ComputeLayout(s).TelemetryLines; got != 1 {
		t.Fatalf("telemetry slot = %d rows after dismissal, want it reserved", got)
	}
}

// The telemetry slot never changes height. The composer stays on the same row
// from idle through a turn and back, so a fast task cannot move the input the
// user is typing into.
func TestLiveTelemetrySlotKeepsComposerRowFixed(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})

	composerRow := func(label string) int {
		t.Helper()
		lines := strings.Split(ansi.Strip(Render(s)), "\n")
		if len(lines) != 30 {
			t.Fatalf("%s: frame is %d rows, want exactly 30", label, len(lines))
		}
		for i, line := range lines {
			if strings.Contains(line, "build › ") {
				return i
			}
		}
		t.Fatalf("%s: composer row missing:\n%s", label, strings.Join(lines, "\n"))
		return -1
	}

	want := composerRow("idle")
	if got := ComputeLayout(s).TelemetryLines; got != 1 {
		t.Fatalf("idle telemetry rows = %d, want the slot reserved", got)
	}
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	if got := composerRow("busy"); got != want {
		t.Fatalf("busy composer row = %d, want the fixed row %d", got, want)
	}
	if out := ansi.Strip(Render(s)); !strings.Contains(out, "preparing workspace…") {
		t.Fatalf("busy frame lost the telemetry phrase:\n%s", out)
	}
	s.Apply(AssistantDelta{Text: "answer", At: at.Add(time.Second), Seq: 1})
	if got := composerRow("streaming"); got != want {
		t.Fatalf("streaming composer row = %d, want the fixed row %d", got, want)
	}
	if out := ansi.Strip(Render(s)); !strings.Contains(out, "formatting response…") {
		t.Fatalf("streaming frame lost the telemetry phrase:\n%s", out)
	}
	s.Apply(AssistantDone{At: at.Add(2 * time.Second), Seq: 1})
	if got := composerRow("done"); got != want {
		t.Fatalf("settled composer row = %d, want the fixed row %d", got, want)
	}
	if out := ansi.Strip(Render(s)); strings.Contains(out, "formatting response…") {
		t.Fatalf("settled frame kept a stale telemetry phrase:\n%s", out)
	}
}

// The bar is exactly: spinner, one space, semantic label, pure whitespace
// spacer, then elapsed plus the compact downward token volume pinned to the
// right edge. Unknown volume stays absent.
func TestLiveTelemetryMetricsAreRightAlignedAndMeasured(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(AssistantDelta{Text: strings.Repeat("hello world ", 8), At: at.Add(time.Second), Seq: 1})

	layout := ComputeLayout(s)
	bar := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), layout, at.Add(2*time.Second), 0))
	if got := CellWidth(bar); got != columnWidth(s, layout) {
		t.Fatalf("bar width = %d, want the full conversation row %d: %q", got, columnWidth(s, layout), bar)
	}
	if !strings.HasPrefix(bar, spinnerFrames[0]+" formatting response…") {
		t.Fatalf("left group is not the contiguous spinner and intent: %q", bar)
	}
	if !strings.Contains(bar, "2s · ↓") || !strings.HasSuffix(bar, "tokens") {
		t.Fatalf("right metrics lost the elapsed or token volume: %q", bar)
	}
	if got := formatOutputTokens(1100); got != "↓1.1k tokens" {
		t.Fatalf("compact token volume = %q, want the ↓1.1k contract", got)
	}

	// The right block must not move as the spinner advances: each frame keeps
	// the same cell width and the same suffix anchor.
	tokenSuffix := formatOutputTokens(projectLiveTelemetry(s).OutputTokens)
	for tick := 0; tick < len(spinnerFrames)+2; tick++ {
		frame := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), layout, at.Add(2*time.Second), tick))
		if got := CellWidth(frame); got != columnWidth(s, layout) {
			t.Fatalf("tick %d: bar width = %d, want %d: %q", tick, got, columnWidth(s, layout), frame)
		}
		if tokenSuffix != "" && !strings.HasSuffix(frame, tokenSuffix) {
			t.Fatalf("tick %d: right block moved: %q", tick, frame)
		}
	}

	// Everything between the intent and the right metrics must be whitespace:
	// no dot track, dashes, or other punctuation fill.
	labelEnd := strings.Index(bar, "formatting response…") + len("formatting response…")
	rest := bar[labelEnd:]
	rightStart := strings.Index(rest, "2s · ↓")
	if rightStart < 0 {
		t.Fatalf("right metrics have no elapsed/token group: %q", bar)
	}
	if gap := rest[:rightStart]; strings.TrimSpace(gap) != "" {
		t.Fatalf("middle spacer contains fill characters: %q", gap)
	}

	// Before answer text, the token volume is unknown and stays absent rather
	// than rendering as zero.
	s2 := New()
	s2.Apply(TerminalResized{Width: 100, Height: 30})
	s2.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	thinking := ansi.Strip(renderLiveTelemetryBarAt(s2, PaletteFor(s2.Theme), ComputeLayout(s2), at.Add(2*time.Second), 0))
	if strings.Contains(thinking, "tokens") || strings.Contains(thinking, "↓") {
		t.Fatalf("unknown volume rendered as telemetry: %q", thinking)
	}
	if !strings.Contains(thinking, "2s") {
		t.Fatalf("thinking bar lost the elapsed anchor: %q", thinking)
	}
}

// Output-first mode paints the bar as the row immediately above the prompt,
// and the painter counts it in the chrome it erases each frame.
func TestOutputFirstTelemetryBarSitsAboveComposer(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})

	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)
	lines := liveComposerLines(s, pal, layout)
	if len(lines) < 2 {
		t.Fatalf("live composer rows = %d, want bar plus input", len(lines))
	}
	if !strings.Contains(ansi.Strip(lines[0]), "preparing workspace…") {
		t.Fatalf("row above the prompt = %q, want the telemetry bar", ansi.Strip(lines[0]))
	}
	if !strings.Contains(ansi.Strip(lines[1]), "› ") {
		t.Fatalf("prompt is not directly below the bar: %q", ansi.Strip(lines[1]))
	}
	if got, want := liveChrome(s, layout), len(lines)+1; got != want {
		t.Fatalf("live chrome = %d rows, want the bar plus %d composer rows", got, want-1)
	}
}
