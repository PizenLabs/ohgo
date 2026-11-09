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
// later activity has taken over, the bar releases its row: a settled phase is
// not live execution, so no blank telemetry row may remain above the composer.
func TestLiveTelemetryClearsWhenReasoningSettles(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(ReasoningDelta{Text: "inspect", At: at.Add(time.Millisecond), Seq: 1})
	if !projectLiveTelemetry(s).Active {
		t.Fatal("open reasoning trace has no telemetry row")
	}
	if got := ComputeLayout(s).TelemetryLines; got != 1 {
		t.Fatalf("telemetry rows while reasoning = %d, want 1", got)
	}
	s.closeReasoning(at.Add(time.Second))
	if bar := projectLiveTelemetry(s); bar.Active {
		t.Fatalf("settled Thought block kept telemetry active: %+v", bar)
	}
	if got := ComputeLayout(s).TelemetryLines; got != 0 {
		t.Fatalf("telemetry rows after dismissal = %d, want the row released", got)
	}
	if got := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), ComputeLayout(s), 0)); got != "" {
		t.Fatalf("settled bar rendered %q, want empty", got)
	}
}

// The telemetry row exists only while a turn runs. Idle frames reserve zero
// rows and paint nothing, so the transcript flows straight into the composer
// instead of leaving a blank or summary line above it.
func TestLiveTelemetryRowExistsOnlyWhileTurnRuns(t *testing.T) {
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
	if got := ComputeLayout(s).TelemetryLines; got != 0 {
		t.Fatalf("idle telemetry rows = %d, want the row released", got)
	}
	if bar := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), ComputeLayout(s), 0)); bar != "" {
		t.Fatalf("idle frame painted a telemetry row: %q", bar)
	}
	if out := ansi.Strip(Render(s)); strings.Contains(out, "⏱") {
		t.Fatalf("fresh session showed a turn timer:\n%s", out)
	}

	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	if got := composerRow("busy"); got != want {
		t.Fatalf("busy composer row = %d, want the fixed row %d", got, want)
	}
	if got := ComputeLayout(s).TelemetryLines; got != 1 {
		t.Fatalf("busy telemetry rows = %d, want the live row", got)
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
	if got := ComputeLayout(s).TelemetryLines; got != 0 {
		t.Fatalf("settled telemetry rows = %d, want the row released", got)
	}
	if bar := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), ComputeLayout(s), 0)); bar != "" {
		t.Fatalf("settled frame painted a stale telemetry row: %q", bar)
	}
	out := ansi.Strip(Render(s))
	if strings.Contains(out, "formatting response…") {
		t.Fatalf("settled frame kept the live intent row:\n%s", out)
	}
	if !strings.Contains(out, "⏱ 2s") {
		t.Fatalf("settled frame lost the locked bottom-status timer:\n%s", out)
	}
}

// The bottom status line carries the turn timer: it starts at zero, advances on
// frame ticks, freezes on settlement, and resets for the next prompt.
func TestTurnTimerLivesInBottomStatusLine(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 30})
	s.Apply(WorkspaceUpdated{
		Cwd: "/home/user/Golang/go-template", HasCwd: true,
		Session: "#85", HasSession: true,
		GitBranch: "master", GitKnown: true, HasGit: true,
	})
	s.Apply(SessionMetricsUpdated{Input: 9700, Output: 219})
	s.Apply(ContextUpdated{Used: 219, Window: 100_000})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})

	bottom := func() string { return ansi.Strip(composerHintBar(s, 120)) }
	if got := bottom(); !strings.HasPrefix(got, "⏱ 0s  ↑9.7k") {
		t.Fatalf("busy status line = %q, want the timer at zero before the metrics", got)
	}
	if upper := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), ComputeLayout(s), 0)); strings.Contains(upper, "⏱") {
		t.Fatalf("live bar duplicated the bottom timer: %q", upper)
	}
	if live := ansi.Strip(liveStatusLine(s, PaletteFor(s.Theme), ComputeLayout(s))); !strings.HasPrefix(live, "⏱ 0s") {
		t.Fatalf("output-first status line = %q, want the timer prefix", live)
	}

	s.Apply(FrameTick{At: at.Add(4500 * time.Millisecond)})
	if got := bottom(); !strings.HasPrefix(got, "⏱ 4.5s") {
		t.Fatalf("live status line = %q, want the ticking duration", got)
	}

	s.Apply(AssistantDone{At: at.Add(6 * time.Second), Seq: 1})
	if got := bottom(); !strings.HasPrefix(got, "⏱ 6s  ↑9.7k") {
		t.Fatalf("settled status line = %q, want the locked final duration", got)
	}
	// A later frame must not restart the clock: settlement locks the measured
	// span even while idle frames continue.
	s.Apply(FrameTick{At: at.Add(time.Minute)})
	if got := bottom(); !strings.HasPrefix(got, "⏱ 6s") {
		t.Fatalf("idle status line = %q, want the locked duration", got)
	}

	s.Apply(UserSubmitted{Text: "again", At: at.Add(2 * time.Minute), Seq: 2})
	if got := bottom(); !strings.HasPrefix(got, "⏱ 0s") {
		t.Fatalf("new turn status line = %q, want the timer reset", got)
	}
}

// The bottom timer uses the shared measured vocabulary: zero reads as "0s",
// sub-second work keeps milliseconds, ordinary work one decimal, and long work
// the compact minute/hour form.
func TestTurnElapsedLabelFormatsMeasuredSpans(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "⏱ 0s"},
		{420 * time.Millisecond, "⏱ 420ms"},
		{4500 * time.Millisecond, "⏱ 4.5s"},
		{72 * time.Second, "⏱ 1m 12s"},
	}
	for _, tt := range cases {
		got := turnElapsedLabel(State{SessionActive: true, TurnElapsed: tt.elapsed})
		if got != tt.want {
			t.Errorf("turnElapsedLabel(%s) = %q, want %q", tt.elapsed, got, tt.want)
		}
	}
	if got := turnElapsedLabel(New()); got != "" {
		t.Errorf("fresh session timer = %q, want empty", got)
	}
}

// The live bar owns intent only: the spinner and the semantic phrase. Elapsed,
// tokens, context, and orientation belong to the bottom status line and must
// not be duplicated above the composer.
func TestLiveTelemetryBarCarriesIntentOnly(t *testing.T) {
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(AssistantDelta{Text: strings.Repeat("hello world ", 8), At: at.Add(time.Second), Seq: 1})

	layout := ComputeLayout(s)
	bar := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), layout, 0))
	if got, want := bar, spinnerFrames[0]+" formatting response…"; got != want {
		t.Fatalf("live bar = %q, want intent only %q", got, want)
	}
	for _, banned := range []string{"⏱", "↓", "tokens", "#", "master"} {
		if strings.Contains(bar, banned) {
			t.Fatalf("live bar duplicated bottom telemetry %q: %q", banned, bar)
		}
	}

	// The spinner advances without adding or moving any metric block.
	for tick := 0; tick < len(spinnerFrames)+2; tick++ {
		frame := ansi.Strip(renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), layout, tick))
		if strings.Contains(frame, "⏱") || strings.Contains(frame, "tokens") {
			t.Fatalf("tick %d: live bar grew telemetry: %q", tick, frame)
		}
		if !strings.HasSuffix(frame, "formatting response…") {
			t.Fatalf("tick %d: live intent moved: %q", tick, frame)
		}
	}

	// Before answer text the phrase changes; no token or elapsed anchor
	// appears as a fallback.
	s2 := New()
	s2.Apply(TerminalResized{Width: 100, Height: 30})
	s2.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	thinking := ansi.Strip(renderLiveTelemetryBarAt(s2, PaletteFor(s2.Theme), ComputeLayout(s2), 0))
	if strings.Contains(thinking, "⏱") || strings.Contains(thinking, "↓") {
		t.Fatalf("thinking bar leaked bottom telemetry: %q", thinking)
	}
	if !strings.HasSuffix(thinking, "preparing workspace…") {
		t.Fatalf("thinking bar lost its intent: %q", thinking)
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

// Idle output-first frames add no telemetry row: the prompt is the first
// composer line, directly below the transcript.
func TestOutputFirstIdleFrameHasNoTelemetryRow(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})

	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)
	lines := liveComposerLines(s, pal, layout)
	if len(lines) == 0 {
		t.Fatal("idle frame has no composer rows")
	}
	if !strings.Contains(ansi.Strip(lines[0]), "› ") {
		t.Fatalf("first composer row = %q, want the prompt", ansi.Strip(lines[0]))
	}
	if got := layout.TelemetryLines; got != 0 {
		t.Fatalf("idle telemetry rows = %d, want the row released", got)
	}
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "preparing workspace…") || strings.Contains(plain, "⏱") {
			t.Fatalf("idle composer kept a telemetry row: %q", plain)
		}
	}
}
