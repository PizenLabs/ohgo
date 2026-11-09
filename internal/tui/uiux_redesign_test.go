package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// TestFastToolHoldsIntentFrameThenSettles: a call that finishes in
// milliseconds may not snap from nothing to a checkmark. It holds its executing
// frame for the perception floor, then settles with the measured duration.
func TestFastToolHoldsIntentFrameThenSettles(t *testing.T) {
	start := time.Now()
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: start})
	s.Apply(ToolFinished{ID: "c1", Text: "package a\n", Summary: "1 line", At: start.Add(2 * time.Millisecond)})
	s.Apply(FrameTick{At: start})

	running := toolStatusGlyph(ToolRunning, s.Status.Spinner)
	active := ansi.Strip(Render(s))
	if !strings.Contains(active, running+" read a.go") || strings.Contains(active, "✔ read a.go") {
		t.Fatalf("fast tool lost its intent frame:\n%s", active)
	}

	s.Apply(FrameTick{At: start.Add(toolMinDwell - time.Millisecond)})
	if held := ansi.Strip(Render(s)); !strings.Contains(held, running+" read a.go") {
		t.Fatalf("fast tool settled before the dwell floor:\n%s", held)
	}

	s.Apply(FrameTick{At: start.Add(toolMinDwell)})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "✔ read a.go") {
		t.Fatalf("fast tool lost its stable success row:\n%s", out)
	}
	if !strings.Contains(out, "2ms") {
		t.Fatalf("fast tool lost its measured duration:\n%s", out)
	}

	// A later tick must not disturb settled history.
	s.Apply(FrameTick{At: start.Add(time.Second)})
	if again := ansi.Strip(Render(s)); again != out {
		t.Fatalf("completed history changed across a tick:\nbefore:\n%s\nafter:\n%s", out, again)
	}
}

// TestRunningToolShowsSpinnerInFixedSlot: a long call animates the shared
// spinner in one invariant status cell, and its duration is the right-aligned
// measured one.
func TestRunningToolShowsSpinnerInFixedSlot(t *testing.T) {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: start})
	// The render seam measures an elapsed duration from real time; pin the
	// end so this test asserts the formatter rather than the wall clock.
	s.Entries[0].Tool.EndedAt = start.Add(3200 * time.Millisecond)

	out := ansi.Strip(Render(s))
	running := toolStatusGlyph(ToolRunning, s.Status.Spinner)
	if !strings.Contains(out, running+" bash go test ./...") {
		t.Fatalf("running tool lost its spinner or invocation:\n%s", out)
	}
	if !strings.Contains(out, "3.2s") {
		t.Fatalf("running tool lost its measured elapsed time:\n%s", out)
	}
	for tick := 0; tick < len(spinnerFrames); tick++ {
		prefix := toolStatusGlyph(ToolRunning, tick) + " bash go test ./..."
		if got, want := CellWidth(prefix), statusSlotCells+1+CellWidth("bash go test ./..."); got != want {
			t.Fatalf("tick %d status prefix = %d cells, want %d", tick, got, want)
		}
	}
}

// TestStreamingToolReplacesTransientRow: partial output has an explicit
// intermediate state, and completion replaces it rather than leaving both.
func TestStreamingToolReplacesTransientRow(t *testing.T) {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read README.md", At: start})
	s.Apply(ToolPreview{ID: "c1", Summary: "receiving headers"})

	streaming := ansi.Strip(Render(s))
	if !strings.Contains(streaming, "└─ receiving") {
		t.Fatalf("streaming tool lost its intermediate state:\n%s", streaming)
	}
	if got := strings.Count(streaming, "read README.md"); got != 1 {
		t.Fatalf("streaming invocation appeared %d times, want one:\n%s", got, streaming)
	}

	s.Apply(ToolFinished{ID: "c1", Text: "line one\nline two\n", Summary: "2 lines", At: start.Add(1200 * time.Millisecond)})
	done := ansi.Strip(Render(s))
	if strings.Contains(done, "receiving") {
		t.Fatalf("completed tool kept the transient streaming row:\n%s", done)
	}
	if got := strings.Count(done, "read README.md"); got != 1 {
		t.Fatalf("completed invocation appeared %d times, want one:\n%s", got, done)
	}
	if !strings.Contains(done, "└─ 2 lines") {
		t.Fatalf("completed tool lost its compact outcome:\n%s", done)
	}
}

// TestToolFailureStaysToolLevel: a failed read is an operation outcome, not a
// global task failure, and its summary is the short truthful reason.
func TestToolFailureStaysToolLevel(t *testing.T) {
	at := time.Now()
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read .env.example", At: at})
	s.Apply(ToolFinished{
		ID: "c1", IsError: true,
		Text:    "stat \"/repo/.env.example\": no such file or directory\n",
		Summary: CompactResultSummary("stat \"/repo/.env.example\": no such file or directory\n", true),
		At:      at.Add(time.Millisecond),
	})
	s.Apply(FrameTick{At: at.Add(time.Millisecond + toolMinDwell)})

	if got := DeriveRuntime(s); got != RuntimeIdle {
		t.Fatalf("tool failure changed the runtime to %q, want idle", got)
	}
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "✗ read .env.example") {
		t.Fatalf("failure lost its tool-level marker:\n%s", out)
	}
	if !strings.Contains(out, "└─ file not found") {
		t.Fatalf("failure summary is not compact and truthful:\n%s", out)
	}
}

// TestOnlyCurrentRunningToolIsLive: concurrent calls all remain truthful, but
// only one carries the live clock; the others stay in their muted status slot.
func TestOnlyCurrentRunningToolIsLive(t *testing.T) {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: start})
	s.Apply(ToolStarted{ID: "c2", Name: "read", Summary: "read b.go", At: start})
	s.Entries[0].Tool.EndedAt = start.Add(3 * time.Second)
	s.Entries[1].Tool.EndedAt = start.Add(3 * time.Second)

	if index := latestRunningToolIndex(s); index != 1 {
		t.Fatalf("current running tool = %d, want the latest call", index)
	}
	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)
	var current, other strings.Builder
	renderTool(&current, s.Entries[1].Tool, pal, layout, true, false, false)
	renderTool(&other, s.Entries[0].Tool, pal, layout, false, false, false)
	if !strings.Contains(ansi.Strip(current.String()), "3s") {
		t.Fatalf("current tool lost its elapsed clock: %q", current.String())
	}
	if strings.Contains(ansi.Strip(other.String()), "3s") {
		t.Fatalf("non-current tool kept a competing live clock: %q", other.String())
	}
}

// TestReasoningIsSubordinateAndSettles: reasoning is always a collapsed,
// recessive block. It updates its duration while open but never borrows the
// current-action glyph or exposes its trace by default.
func TestReasoningIsSubordinateAndSettles(t *testing.T) {
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: at})
	s.Apply(ReasoningDelta{Text: "inspect the cache", At: at})

	live := ansi.Strip(Render(s))
	if !strings.Contains(live, spinnerFrames[0]+" Thought") {
		t.Fatalf("open reasoning lost its live spinner head:\n%s", live)
	}
	if strings.Contains(live, "◌ Thought") {
		t.Fatalf("reasoning borrowed the current-action glyph:\n%s", live)
	}
	if strings.Contains(live, "inspect the cache") {
		t.Fatalf("collapsed reasoning leaked its trace:\n%s", live)
	}

	s.Apply(AssistantDelta{Text: "answer", At: at.Add(2 * time.Second)})
	settled := ansi.Strip(Render(s))
	if !strings.Contains(settled, "+ Thought") {
		t.Fatalf("closed reasoning lost its collapsed marker:\n%s", settled)
	}
	if strings.Contains(settled, "inspect the cache") {
		t.Fatalf("closed reasoning leaked its trace:\n%s", settled)
	}
}

// TestCancellationLeavesNoLiveTool: cancellations converge tool and UI state
// on the same terminal transition.
func TestCancellationLeavesNoLiveTool(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go", Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "go test ./...", At: time.Now(), Seq: 1})
	s.Apply(TurnCancelled{Seq: 1, Text: "cancelled", At: time.Now()})

	out := ansi.Strip(Render(s))
	for _, marker := range []string{"◌", "⠋"} {
		if strings.Contains(out, marker) {
			t.Fatalf("cancelled view kept live marker %q:\n%s", marker, out)
		}
	}
	if !strings.Contains(out, "⊘ go test ./...") {
		t.Fatalf("cancelled tool lost its terminal marker:\n%s", out)
	}
}

// TestPathTruncationKeepsBasename: a long path is cut in the middle so the
// operation and the file name both survive, and the result is width-stable.
func TestPathTruncationKeepsBasename(t *testing.T) {
	path := "/Users/anvndev/Documents/Project/Golang/go-ecommerce/src/App.tsx"
	for width := 12; width <= 50; width++ {
		got := TruncateMiddleToWidth(path, width)
		if w := CellWidth(got); w > width {
			t.Fatalf("width %d: result is %d cells: %q", width, w, got)
		}
		if !strings.Contains(got, "…") {
			t.Fatalf("width %d: result did not mark the cut: %q", width, got)
		}
	}
	got := TruncateMiddleToWidth(path, 32)
	if !strings.HasSuffix(got, "App.tsx") {
		t.Fatalf("middle truncation lost the basename: %q", got)
	}
	if !strings.HasPrefix(got, "/Users") {
		t.Fatalf("middle truncation lost the path head: %q", got)
	}
	if again := TruncateMiddleToWidth(path, 32); again != got {
		t.Fatalf("middle truncation is not deterministic: %q vs %q", got, again)
	}

	s := New()
	s.Apply(TerminalResized{Width: 44, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read " + path, At: time.Now()})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "App.tsx") {
		t.Fatalf("narrow tool row lost the target basename:\n%s", out)
	}
	if again := ansi.Strip(Render(s)); again != out {
		t.Fatalf("resize-stable render changed across frames:\n%s\n%s", out, again)
	}
}

// TestTranscriptReadsAsStateTransitions: the finished rows, the settled
// reasoning footer, and the one live call appear in execution order, and no
// historical row keeps a live marker.
func TestTranscriptReadsAsStateTransitions(t *testing.T) {
	at := time.Now().Add(-time.Minute)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "analyze this project", At: at, Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "ls /go-ecommerce", At: at, Seq: 1})
	s.Apply(ToolFinished{ID: "c1", Text: "a\nb\n", Summary: "21 lines", At: at.Add(time.Millisecond), Seq: 1})
	s.Apply(ToolStarted{ID: "c2", Name: "read", Summary: "read package.json", At: at, Seq: 1})
	s.Apply(ToolFinished{ID: "c2", Text: "x\ny\n", Summary: "35 lines", At: at.Add(2 * time.Millisecond), Seq: 1})
	s.Apply(ReasoningDelta{Text: "inspect the backend", At: at, Seq: 1})
	s.Apply(AssistantDelta{Text: "I will inspect the backend.", At: at.Add(13 * time.Second), Seq: 1})
	s.Apply(ToolStarted{ID: "c3", Name: "glob", Summary: "glob backend/**/*.go", At: at.Add(13 * time.Second), Seq: 1})

	out := ansi.Strip(Render(s))
	running := toolStatusGlyph(ToolRunning, s.Status.Spinner)
	ordered := []string{
		"+ 2 tools · ",
		"+ Thought",
		running + " glob backend/**/*.go",
	}
	last := -1
	for _, marker := range ordered {
		index := strings.Index(out, marker)
		if index < 0 {
			t.Fatalf("transcript lost %q:\n%s", marker, out)
		}
		if index <= last {
			t.Fatalf("transcript order is wrong at %q:\n%s", marker, out)
		}
		last = index
	}
	if got := strings.Count(out, running+" glob backend/**/*.go"); got != 1 {
		t.Fatalf("transcript has %d live tool markers, want exactly one:\n%s", got, out)
	}
	if strings.Contains(out, "running") {
		t.Fatalf("transcript used generic running vocabulary:\n%s", out)
	}
}

// TestCompletedHistoryIsNotRepaintedByTicks: the live clock advances the one
// active block only. Settled rows keep their cached projection, which is what
// makes history stable and rendering cheap during a long run.
func TestCompletedHistoryIsNotRepaintedByTicks(t *testing.T) {
	at := time.Now().Add(-time.Second)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	for i := 0; i < 12; i++ {
		id := "c" + string(rune('a'+i))
		s.Apply(ToolStarted{ID: id, Name: "read", Summary: "read file", At: at})
		s.Apply(ToolFinished{ID: id, Text: "a\nb\n", Summary: "2 lines", At: at.Add(time.Millisecond)})
	}
	layout := &TranscriptLayout{}
	layout.Refresh(&s, ComputeLayout(s))
	if layout.LastRendered != 12 {
		t.Fatalf("initial render count = %d, want 12", layout.LastRendered)
	}
	s.Status.Spinner = 9
	layout.Refresh(&s, ComputeLayout(s))
	if layout.LastRendered != 0 {
		t.Fatalf("settled history repainted %d blocks on a tick, want 0", layout.LastRendered)
	}

	// Adding one running call reopens the batch as individual rows, so every
	// member's cached zero-row group projection is replaced instead of leaving
	// invisible history behind.
	s.Apply(ToolStarted{ID: "live", Name: "bash", Summary: "go test ./...", At: time.Now()})
	layout.Refresh(&s, ComputeLayout(s))
	if layout.LastRendered != 13 {
		t.Fatalf("new live call rendered %d blocks, want the reopened batch plus the live call", layout.LastRendered)
	}
	s.Apply(FrameTick{At: time.Now()})
	layout.Refresh(&s, ComputeLayout(s))
	if layout.LastRendered != 1 {
		t.Fatalf("tick rendered %d blocks, want only the live call", layout.LastRendered)
	}
}

// TestLongPathSummaryKeepsTarget: the semantic summary keeps the complete
// target so expansion can reveal it; only the collapsed paint is bounded.
func TestLongPathSummaryKeepsTarget(t *testing.T) {
	path := "/Users/anvndev/Documents/Project/Golang/go-ecommerce/src/App.tsx"
	got := ToolSummaryForCall("read", map[string]any{"path": path})
	if got != "read "+path {
		t.Fatalf("summary = %q, want the complete target", got)
	}
	if !strings.HasSuffix(got, "App.tsx") {
		t.Fatalf("summary lost the basename: %q", got)
	}
	if !strings.HasPrefix(got, "read /Users") {
		t.Fatalf("summary lost the operation and path head: %q", got)
	}

	s := New()
	s.Apply(TerminalResized{Width: 44, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: got, At: time.Now()})
	collapsed := ansi.Strip(Render(s))
	for i, line := range strings.Split(collapsed, "\n") {
		if CellWidth(line) > 44 {
			t.Fatalf("collapsed badge row %d overflows: %q", i, line)
		}
	}
	if !strings.Contains(collapsed, "…") || !strings.Contains(collapsed, "App.tsx") {
		t.Fatalf("collapsed badge lost its bounded target:\n%s", collapsed)
	}
}

// TestWhitespaceBetweenToolRoundsDoesNotSplitTheRun: providers frame a tool
// round with whitespace-only text. That is not an answer, so it must not open
// an assistant block: a blank gap between consecutive calls and a second
// expand hint are both visible proof that the transcript split one run in two.
func TestWhitespaceBetweenToolRoundsDoesNotSplitTheRun(t *testing.T) {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "inspect the module"})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "ls /repo/backend", At: start})
	s.Apply(ToolFinished{ID: "c1", Text: "a\nb", Summary: "19 lines", At: start.Add(time.Millisecond)})
	s.Apply(ToolStarted{ID: "c2", Name: "ls", Summary: "ls /repo", At: start})
	s.Apply(ToolFinished{ID: "c2", Text: "a\nb", Summary: "17 lines", At: start.Add(time.Millisecond)})
	// The provider's own framing between the round's results and the next call.
	s.Apply(AssistantDelta{Text: "\n\n", At: start.Add(2 * time.Millisecond)})
	s.Apply(ToolStarted{ID: "c3", Name: "read", Summary: "read /repo/go.mod", At: start})
	s.Apply(ToolFinished{ID: "c3", Text: "a\nb", Summary: "52 lines", At: start.Add(time.Millisecond)})

	rows := strings.Split(ansi.Strip(Render(s)), "\n")
	outcome, next := -1, -1
	for i, row := range rows {
		if strings.Contains(row, "17 lines") {
			outcome = i
		}
		if strings.Contains(row, "read /repo/go.mod") {
			next = i
		}
	}
	if outcome < 0 || next < 0 {
		t.Fatalf("transcript lost a call row:\n%s", strings.Join(rows, "\n"))
	}
	if next != outcome+1 {
		t.Fatalf("gap of %d blank rows split one tool run:\n%s", next-outcome-1, strings.Join(rows[outcome:next+1], "\n"))
	}
	if got := strings.Count(strings.Join(rows, "\n"), "(ctrl+o to expand)"); got != 1 {
		t.Fatalf("expand hint appeared %d times in one run, want once on the last call", got)
	}
}

// TestAssistantWhitespaceInsideAnAnswerIsKept: dropping framing whitespace
// must not collapse a paragraph break the answer itself streamed.
func TestAssistantWhitespaceInsideAnAnswerIsKept(t *testing.T) {
	s := New()
	s.Apply(AssistantDelta{Text: "First paragraph."})
	s.Apply(AssistantDelta{Text: "\n\n"})
	s.Apply(AssistantDelta{Text: "Second paragraph."})

	var assistant *Entry
	for i := range s.Entries {
		if s.Entries[i].Kind == EntryAssistant {
			assistant = &s.Entries[i]
		}
	}
	if assistant == nil {
		t.Fatal("the answer produced no transcript entry")
	}
	if assistant.AssistantText != "First paragraph.\n\nSecond paragraph." {
		t.Fatalf("answer text = %q, want the streamed paragraph break preserved", assistant.AssistantText)
	}
}
