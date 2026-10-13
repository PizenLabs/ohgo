package tui

import (
	"strings"
	"testing"
	"time"
)

// TestProjectThinkingIdleIsInactive: nothing is running, so no activity line
// may exist. This is the baseline the whole projection rests on.
func TestProjectThinkingIdleIsInactive(t *testing.T) {
	got := ProjectThinking(New())
	if got.Active {
		t.Fatalf("idle projected active: %+v", got)
	}
	if got.Phase != ThinkingNone || got.Label != "" || got.Detail != "" {
		t.Fatalf("idle fabricated a phase: %+v", got)
	}
	if len(RenderThinking(got, time.Now(), 80)) != 0 {
		t.Fatalf("inactive state rendered lines: %v", RenderThinking(got, time.Now(), 80))
	}
}

// TestProjectThinkingModelPhase: a busy turn with no running tool is the model
// phase, and its start is the real turn start, not render time.
func TestProjectThinkingModelPhase(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(UserSubmitted{Text: "go", At: start})

	got := ProjectThinking(s)
	if !got.Active || got.Phase != ThinkingModel {
		t.Fatalf("busy turn projected %+v, want active model phase", got)
	}
	if got.Label != "Thinking…" {
		t.Fatalf("label = %q, want the working vocabulary", got.Label)
	}
	if !got.StartedAt.Equal(start) {
		t.Fatalf("started at %v, want the real turn start %v", got.StartedAt, start)
	}
	if !got.Interruptible {
		t.Fatal("a running turn is interruptible by esc but projected otherwise")
	}
}

// TestProjectThinkingStreamingIsResponding: once visible text exists the model
// is responding, not merely thinking. The distinction comes from the existing
// DeriveRuntime authority, not from reading the prose here.
func TestProjectThinkingStreamingIsResponding(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(AssistantDelta{Text: "answer"})
	got := ProjectThinking(s)
	if got.Phase != ThinkingModel || got.Label != "Responding…" {
		t.Fatalf("streaming projected %+v, want responding model phase", got)
	}
}

// TestProjectThinkingToolPhaseCarriesTruthfulDetail: a running tool supplies
// its own argument-derived summary and start time; none of it is inferred.
func TestProjectThinkingToolPhaseCarriesTruthfulDetail(t *testing.T) {
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: at})

	got := ProjectThinking(s)
	if got.Phase != ThinkingTool || got.Label != "Running" {
		t.Fatalf("running tool projected %+v, want tool phase", got)
	}
	if got.Detail != "bash go test ./..." {
		t.Fatalf("detail = %q, want the tool's own summary", got.Detail)
	}
	if !got.StartedAt.Equal(at) {
		t.Fatalf("started at %v, want the tool start %v", got.StartedAt, at)
	}
	if !got.Interruptible {
		t.Fatal("a running tool is interruptible by esc but projected otherwise")
	}
}

// TestProjectThinkingFinishedToolIsNotRunning: a finished tool must not be
// claimed as running even while the turn is still busy.
func TestProjectThinkingFinishedToolIsNotRunning(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: time.Now()})
	s.Apply(ToolFinished{ID: "c1", Text: "ok", Summary: "", At: time.Now()})

	got := ProjectThinking(s)
	if got.Phase != ThinkingModel {
		t.Fatalf("finished tool projected %+v, want the model phase", got)
	}
}

// TestProjectThinkingWaitingIsDistinct: a turn blocked on the user is the
// waiting phase — no spinner, no elapsed timer, and no esc hint, because the
// approval dialog owns the keyboard.
func TestProjectThinkingWaitingIsDistinct(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "go", At: time.Now()})
	s.Apply(ApprovalAsked{ToolName: "bash", Summary: "run it", Choices: []string{"Yes", "No"}})

	got := ProjectThinking(s)
	if got.Phase != ThinkingWaiting || got.Label != "Waiting for approval" {
		t.Fatalf("approval projected %+v, want the waiting phase", got)
	}
	if got.Interruptible {
		t.Fatal("waiting projected interruptible: the dialog owns the keyboard")
	}
	if !got.StartedAt.IsZero() {
		t.Fatalf("waiting carried a start time %v: a blocked tool is not running", got.StartedAt)
	}
	if lines := RenderThinking(got, time.Now(), 80); len(lines) != 1 || !strings.Contains(lines[0], "◇") {
		t.Fatalf("waiting render = %v, want the ◇ marker", lines)
	}
}

// TestProjectThinkingTerminalStatesAreInactive covers every way a turn ends:
// completion, cancellation, and failure all leave nothing to claim, so a stale
// "Thinking…" can never outlive the turn.
func TestProjectThinkingTerminalStatesAreInactive(t *testing.T) {
	completed := New()
	completed.Apply(UserSubmitted{Text: "go"})
	completed.Apply(AssistantDone{})

	cancelled := New()
	cancelled.Apply(UserSubmitted{Text: "go"})
	cancelled.Apply(TurnCancelled{Seq: 1, Text: "cancelled"})

	failed := New()
	failed.Apply(UserSubmitted{Text: "go"})
	failed.Apply(Notice{Text: "error: boom"})
	failed.Apply(AssistantDone{})

	for name, s := range map[string]State{"completed": completed, "cancelled": cancelled, "failed": failed} {
		if got := ProjectThinking(s); got.Active {
			t.Errorf("%s projected active: %+v", name, got)
		}
	}
}

// TestProjectThinkingShuttingDownIsInactive: exiting is not an activity to
// advertise; the projection claims nothing it cannot sustain.
func TestProjectThinkingShuttingDownIsInactive(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(QuitRequested{})
	if got := ProjectThinking(s); got.Active {
		t.Fatalf("shutting down projected active: %+v", got)
	}
}

// TestRenderThinkingElapsedDerivesFromStartedAt: the displayed duration is the
// real age of the activity, computed from StartedAt and the supplied now — not
// from a counter and not from wall-clock time.
func TestRenderThinkingElapsedDerivesFromStartedAt(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	now := start.Add(4 * time.Second)
	state := ThinkingState{Active: true, Phase: ThinkingModel, Label: "Thinking…", StartedAt: start}

	lines := RenderThinking(state, now, 60)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want exactly one", lines)
	}
	if !strings.Contains(lines[0], "4s") {
		t.Fatalf("line = %q, want the real 4s elapsed", lines[0])
	}
	if !strings.Contains(lines[0], "Thinking") {
		t.Fatalf("line = %q, want the label kept", lines[0])
	}
}

// TestRenderThinkingUnknownStartShowsNoTimer: an unknown start must not become
// a fabricated duration.
func TestRenderThinkingUnknownStartShowsNoTimer(t *testing.T) {
	state := ThinkingState{Active: true, Phase: ThinkingModel, Label: "Thinking…"}
	line := RenderThinking(state, time.Now(), 60)[0]
	if strings.ContainsAny(line, "0123456789") {
		t.Fatalf("line = %q, want no invented timer", line)
	}
}

// TestRenderThinkingNoFabricatedTelemetry: this slice has no token or progress
// telemetry at the activity boundary, so none may appear.
func TestRenderThinkingNoFabricatedTelemetry(t *testing.T) {
	state := ThinkingState{Active: true, Phase: ThinkingModel, Label: "Thinking…", StartedAt: time.Now().Add(-9 * time.Second)}
	line := RenderThinking(state, time.Now(), 80)[0]
	lowered := strings.ToLower(line)
	for _, forbidden := range []string{"token", "tok", "%", "eta"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("line = %q fabricates telemetry %q", line, forbidden)
		}
	}
}

// TestRenderThinkingInterruptHintIsGated: the esc affordance appears only when
// the runtime actually allows interruption.
func TestRenderThinkingInterruptHintIsGated(t *testing.T) {
	interruptible := ThinkingState{Active: true, Phase: ThinkingTool, Label: "Running", Interruptible: true}
	if line := RenderThinking(interruptible, time.Now(), 80)[0]; !strings.Contains(line, "esc") {
		t.Fatalf("line = %q, want the esc hint when interruptible", line)
	}
	blocked := ThinkingState{Active: true, Phase: ThinkingWaiting, Label: "Waiting for approval"}
	if line := RenderThinking(blocked, time.Now(), 80)[0]; strings.Contains(line, "esc") {
		t.Fatalf("line = %q, want no esc hint while waiting", line)
	}
}

// TestRenderThinkingIsDeterministic: identical inputs must render identical
// output, so the renderer can be tested without a clock and cannot drift.
func TestRenderThinkingIsDeterministic(t *testing.T) {
	state := ThinkingState{
		Active:        true,
		Phase:         ThinkingTool,
		Label:         "Running",
		Detail:        "bash go test ./...",
		StartedAt:     time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC),
		Interruptible: true,
	}
	now := time.Date(2026, 10, 13, 10, 0, 8, 0, time.UTC)
	first := RenderThinking(state, now, 50)
	second := RenderThinking(state, now, 50)
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatalf("render not deterministic: %v vs %v", first, second)
	}
}

// TestRenderThinkingRespectsWidth: a narrow terminal must never overflow, and
// the duration must survive the trim because it is right-aligned.
func TestRenderThinkingRespectsWidth(t *testing.T) {
	state := ThinkingState{
		Active: true, Phase: ThinkingTool, Label: "Running",
		Detail:    strings.Repeat("very long tool summary ", 8),
		StartedAt: time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC),
	}
	now := time.Date(2026, 10, 13, 10, 0, 12, 0, time.UTC)
	for width := 1; width <= 60; width++ {
		line := RenderThinking(state, now, width)[0]
		if got := CellWidth(line); got > width {
			t.Fatalf("width %d: line is %d cells: %q", width, got, line)
		}
	}
	wide := RenderThinking(state, now, 60)[0]
	if !strings.Contains(wide, "12s") {
		t.Fatalf("wide line = %q, want the timer kept", wide)
	}
}

// TestViewShowsAndClearsThinkingLine is the integration contract: the
// full-screen view paints the projected activity line while a turn runs and
// removes it the moment the turn completes.
func TestViewShowsAndClearsThinkingLine(t *testing.T) {
	s := New()
	s.Status.Model = "m"
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})

	out := Render(s)
	if !strings.Contains(out, "Thinking") {
		t.Fatalf("busy view lost the activity line:\n%s", out)
	}
	s.Apply(AssistantDone{})
	if out := Render(s); strings.Contains(out, "Thinking") {
		t.Fatalf("completed view kept a stale activity line:\n%s", out)
	}
}
