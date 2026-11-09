package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// A fast call may not snap from nothing to a checkmark. The executing frame is
// held for the perception floor, while the measured duration and outcome are
// already final in state.
func TestFastToolHoldsTheExecutingFrameForTheDwellFloor(t *testing.T) {
	start := time.Now()
	s := New()
	s.Apply(TerminalResized{Width: 90, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: start})
	s.Apply(ToolFinished{ID: "c1", Text: "contents", Summary: "1 line", At: start.Add(2 * time.Millisecond)})
	s.Apply(FrameTick{At: start})

	if got := s.tool("c1").Status; got != ToolDone {
		t.Fatalf("semantic status = %q, want done immediately", got)
	}
	if got := ansi.Strip(Render(s)); !strings.Contains(got, "◌ read a.go") || strings.Contains(got, "✓ read a.go") {
		t.Fatalf("fast call did not hold its intent frame:\n%s", got)
	}

	s.Apply(FrameTick{At: start.Add(toolMinDwell - time.Millisecond)})
	if got := ansi.Strip(Render(s)); !strings.Contains(got, "◌ read a.go") {
		t.Fatalf("fast call settled below the dwell floor:\n%s", got)
	}

	s.Apply(FrameTick{At: start.Add(toolMinDwell)})
	done := ansi.Strip(Render(s))
	if !strings.Contains(done, "✓ read a.go") {
		t.Fatalf("fast call never settled:\n%s", done)
	}
	if !strings.Contains(done, "2ms") {
		t.Fatalf("settled outcome lost its measured duration:\n%s", done)
	}
}

// The retained layout must repaint the dwell frame when it expires, even
// though no stream or tool event arrives at that instant.
func TestRetainedLayoutSettlesTheDwellFrame(t *testing.T) {
	start := time.Now()
	s := New()
	s.Apply(TerminalResized{Width: 90, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: start})
	s.Apply(ToolFinished{ID: "c1", Text: "contents", Summary: "1 line", At: start.Add(time.Millisecond)})
	s.Apply(FrameTick{At: start})
	layout := &TranscriptLayout{}
	layout.Refresh(&s, ComputeLayout(s))
	if got := rowsPlain(layout.AllRows()); !strings.Contains(got, "◌ read a.go") {
		t.Fatalf("retained layout lost the active frame:\n%s", got)
	}
	s.Apply(FrameTick{At: start.Add(toolMinDwell)})
	layout.Refresh(&s, ComputeLayout(s))
	settled := rowsPlain(layout.AllRows())
	if !strings.Contains(settled, "✓ read a.go") || strings.Contains(settled, "◌ read a.go") {
		t.Fatalf("retained layout kept the expired dwell frame:\n%s", settled)
	}
}

// rowsPlain joins layout rows for content assertions.
func rowsPlain(rows []LayoutRow) string {
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = row.Plain()
	}
	return strings.Join(lines, "\n")
}

// A completed adjacent batch is one persistent telemetry footer. Expansion
// reveals the per-call tree with the same measured durations.
func TestToolBatchCollapsesIntoTelemetryFooter(t *testing.T) {
	at := time.Now().Add(-time.Second)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "run the batch", At: at, Seq: 1})
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("c%02d", i)
		s.Apply(ToolStarted{ID: id, Name: "read", Summary: "read file-" + id, At: at.Add(time.Duration(i) * 10 * time.Millisecond)})
		s.Apply(ToolFinished{ID: id, Text: "one\ntwo", Summary: "2 lines", At: at.Add(time.Duration(i)*10*time.Millisecond + time.Millisecond)})
	}
	s.Apply(AssistantDelta{Text: "batch complete", At: at.Add(time.Second)})
	s.Apply(AssistantDone{At: at.Add(time.Second)})

	collapsed := ansi.Strip(Render(s))
	if !strings.Contains(collapsed, "+ Executed 8 tools in ") || !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Fatalf("batch did not collapse into a telemetry footer:\n%s", collapsed)
	}
	for i := 0; i < 8; i++ {
		if strings.Contains(collapsed, fmt.Sprintf("read file-c%02d", i)) {
			t.Fatalf("collapsed footer leaked call %d:\n%s", i, collapsed)
		}
	}

	s.Apply(ToolToggled{ID: "c07"})
	expanded := ansi.Strip(Render(s))
	if !strings.Contains(expanded, "- Executed 8 tools in ") {
		t.Fatalf("expanded batch lost its telemetry header:\n%s", expanded)
	}
	for i := 0; i < 8; i++ {
		if !strings.Contains(expanded, fmt.Sprintf("└ read file-c%02d (1ms)", i)) {
			t.Fatalf("expanded tree lost call %d's invocation/duration:\n%s", i, expanded)
		}
	}
}

// Expanding a batch must re-resolve the viewport in the same update. A reading
// anchor above the batch stays exactly where it was; the offset is recomputed
// from the new geometry rather than waiting for a later paint.
func TestBatchToggleRecalculatesViewportInTheSameUpdate(t *testing.T) {
	at := time.Now().Add(-time.Second)
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "run the batch", At: at, Seq: 1})
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("c%d", i)
		p.state.Apply(ToolStarted{ID: id, Name: "grep", Summary: "grep target-" + id, At: at})
		p.state.Apply(ToolFinished{ID: id, Text: "a\nb", Summary: "2 lines", At: at.Add(time.Millisecond)})
	}
	p.state.Apply(AssistantDelta{Text: "done", At: at.Add(time.Second)})
	p.state.Apply(AssistantDone{At: at.Add(time.Second)})
	for i := 0; i < 20; i++ {
		p.state.Apply(Notice{Text: fmt.Sprintf("history-%02d", i)})
	}
	p.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	p.state.Apply(ScrollMoved{Delta: 12})
	p.syncTranscript()
	topBefore := viewportTopRow(t, p.transcriptFrame)

	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !p.state.toolsExpandedForTest() {
		t.Fatal("ctrl+o did not expand the latest batch")
	}
	topAfter := viewportTopRow(t, p.transcriptFrame)
	if topAfter.BlockID != topBefore.BlockID || topAfter.Row != topBefore.Row {
		t.Fatalf("toggle moved the reading anchor in-frame: (%s,%d) -> (%s,%d)",
			topBefore.BlockID, topBefore.Row, topAfter.BlockID, topAfter.Row)
	}
	// YOffset is recomputed against the expanded geometry before the next
	// paint, so the resolved window starts exactly at the anchored row.
	block, ok := p.transcriptIndex.BlockIndexForID(topAfter.BlockID)
	if !ok {
		t.Fatalf("anchor block %q disappeared after expansion", topAfter.BlockID)
	}
	if want := p.transcriptIndex.BlockStart(block) + topAfter.Row; p.transcriptFrame.vp.Offset != want {
		t.Fatalf("viewport offset = %d, want the recomputed anchor row %d", p.transcriptFrame.vp.Offset, want)
	}
}

// The batch footer only appears after the run closes. A live call remains an
// individual row, and a trailing batch does not collapse while the turn is
// still running and another call may join it.
func TestLiveToolBatchStaysExpandedUntilSettled(t *testing.T) {
	at := time.Now().Add(-time.Second)
	s := New()
	s.Apply(UserSubmitted{Text: "batch", At: at, Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a", At: at})
	s.Apply(ToolFinished{ID: "c1", Text: "x", Summary: "1 line", At: at.Add(time.Millisecond)})
	s.Apply(ToolStarted{ID: "c2", Name: "read", Summary: "read b", At: at})
	if got := ansi.Strip(Render(s)); !strings.Contains(got, "read a") || !strings.Contains(got, "◌ read b") {
		t.Fatalf("a live batch member disappeared behind a summary:\n%s", got)
	}
	s.Apply(ToolFinished{ID: "c2", Text: "y", Summary: "1 line", At: at.Add(2 * time.Millisecond)})
	s.Apply(AssistantDelta{Text: "done", At: at.Add(time.Second)})
	s.Apply(AssistantDone{At: at.Add(time.Second)})
	if got := ansi.Strip(Render(s)); !strings.Contains(got, "+ Executed 2 tools") {
		t.Fatalf("settled batch did not collapse:\n%s", got)
	}
}

// The fade is a three-phase RGB ramp, not a single frame pop. The plain text
// is unchanged at every phase; only the entrance colour/dim flag moves.
func TestFadeStepsThroughThreeRGBPhases(t *testing.T) {
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	at := time.Now()
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(AssistantDelta{Text: "fade target", At: at})
	s.Apply(FrameTick{At: at})

	vapor := Render(s)
	if !strings.Contains(vapor, "38;2;51;51;51") || !strings.Contains(vapor, "\x1b[2m") {
		t.Fatalf("vapour phase lost its dark neutral/dim frame:\n%q", vapor)
	}
	if got := ansi.Strip(vapor); !strings.Contains(got, "fade target") {
		t.Fatalf("fade changed the block text:\n%s", got)
	}

	s.Apply(FrameTick{At: at.Add(60 * time.Millisecond)})
	solid := Render(s)
	if solid == vapor {
		t.Fatal("solidifying phase did not change the frame")
	}
	if strings.Contains(solid, "38;2;51;51;51") {
		t.Fatalf("solidifying phase kept the vapour colour:\n%q", solid)
	}

	s.Apply(FrameTick{At: at.Add(fadeDuration)})
	settled := Render(s)
	if strings.Contains(settled, "\x1b[2m") || strings.Contains(settled, "38;2;51;51;51") {
		t.Fatalf("settled block kept an entrance frame:\n%q", settled)
	}
	if got := ansi.Strip(settled); !strings.Contains(got, "fade target") {
		t.Fatalf("settled text changed:\n%s", got)
	}

	if fadePhaseAt(0) != fadeVapor || fadePhaseAt(60*time.Millisecond) != fadeSolidify ||
		fadePhaseAt(100*time.Millisecond) != fadeFinal || fadePhaseAt(fadeDuration) != fadeSettled {
		t.Fatalf("fade phases = %d/%d/%d/%d, want vapour/solidify/final/settled",
			fadePhaseAt(0), fadePhaseAt(60*time.Millisecond), fadePhaseAt(100*time.Millisecond), fadePhaseAt(fadeDuration))
	}
}

// The closing answer line eases in on AssistantDone even when the block itself
// appeared long ago; the rest of the answer is already settled history.
func TestAssistantFinalLineFadesOnSettle(t *testing.T) {
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	at := time.Now().Add(-time.Second)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: at})
	s.Apply(AssistantDelta{Text: "streamed answer", At: at})
	doneAt := at.Add(time.Second)
	s.Apply(AssistantDone{At: doneAt})
	s.Apply(FrameTick{At: doneAt})

	entering := Render(s)
	if !strings.Contains(entering, "38;2;51;51;51") || !strings.Contains(entering, "\x1b[2m") {
		t.Fatalf("settled answer line skipped its entrance fade:\n%q", entering)
	}
	if got := ansi.Strip(entering); !strings.Contains(got, "streamed answer") {
		t.Fatalf("final-line fade changed the answer text:\n%s", got)
	}

	s.Apply(FrameTick{At: doneAt.Add(fadeFinalAt)})
	if settled := Render(s); strings.Contains(settled, "38;2;51;51;51") {
		t.Fatalf("final-line fade survived the target instant:\n%q", settled)
	}
}

// Completed thought headers keep a measured duration even when the trace is
// shorter than one millisecond; an unknown start still refuses to invent one.
func TestCompletedReasoningKeepsZeroDurationTelemetry(t *testing.T) {
	at := time.Now().Add(-time.Second)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ReasoningDelta{Text: "instant", At: at})
	s.Apply(AssistantDelta{Text: "answer", At: at})
	s.Apply(FrameTick{At: at.Add(toolMinDwell)})
	if got := ansi.Strip(Render(s)); !strings.Contains(got, "+ Thought · 0ms") {
		t.Fatalf("completed thought lost its measured zero duration:\n%s", got)
	}

	unknown := New()
	unknown.Apply(TerminalResized{Width: 80, Height: 24})
	unknown.Apply(ReasoningDelta{Text: "unknown start"})
	unknown.Apply(AssistantDelta{Text: "answer"})
	if got := ansi.Strip(Render(unknown)); strings.Contains(got, "Thought · 0ms") {
		t.Fatalf("unknown start fabricated a zero duration:\n%s", got)
	}
}

// The composer is fixed geometry, not a function of execution telemetry. The
// prompt row stays at the same vertical coordinate while a batch streams,
// completes, and collapses, and typing still edits the draft meanwhile.
func TestComposerRowIsInvariantDuringToolTelemetry(t *testing.T) {
	at := time.Now()
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.state.Apply(UserSubmitted{Text: "work", At: at, Seq: 1})
	baseline := composerRowIndex(t, p.View())

	step := func(events ...Event) {
		t.Helper()
		for _, event := range events {
			p.state.Apply(event)
		}
		if got := composerRowIndex(t, p.View()); got != baseline {
			t.Fatalf("composer row moved from %d to %d during telemetry", baseline, got)
		}
	}
	step(ToolStarted{ID: "c1", Name: "read", Summary: "read a", At: at, Seq: 1})
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")})
	if p.state.Input != "draft" {
		t.Fatalf("composer input = %q, want the typed draft", p.state.Input)
	}
	step(ToolFinished{ID: "c1", Text: "x", Summary: "1 line", At: at.Add(time.Millisecond), Seq: 1})
	step(ToolStarted{ID: "c2", Name: "read", Summary: "read b", At: at, Seq: 1})
	step(ToolFinished{ID: "c2", Text: "y", Summary: "1 line", At: at.Add(2 * time.Millisecond), Seq: 1})
	step(AssistantDelta{Text: "done", At: at.Add(time.Second)}, AssistantDone{At: at.Add(time.Second)})
}

// toolsExpandedForTest reports whether the latest run owns an open tree.
func (s State) toolsExpandedForTest() bool {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := s.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.run != nil {
			return entry.Tool.run.expanded()
		}
	}
	return false
}

// composerRow returns the vertical position of the live prompt row.
func composerRowIndex(t *testing.T, frame string) int {
	t.Helper()
	for i, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "›") || strings.Contains(plain, "> ") {
			return i
		}
	}
	t.Fatal("composer row not found")
	return -1
}
