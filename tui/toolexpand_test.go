package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Activity presentation contract: one semantic block per tool call,
// collapsed by default, expanded through Ctrl+O as presentation-only state.

// The invocation captured at start must survive the finish: the badge keeps
// saying what ran while the summary line says what it produced.
func TestToolFinishPreservesInvocation(t *testing.T) {
	s := New()
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read internal/agent/loop.go", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "a\nb\nc\n", Summary: "3 lines", At: now})
	tool := s.tool("c1")
	if tool == nil {
		t.Fatal("no tool block for c1")
	}
	if tool.Invocation != "read internal/agent/loop.go" {
		t.Errorf("invocation = %q, want the start summary preserved", tool.Invocation)
	}
	if tool.Summary != "3 lines" {
		t.Errorf("summary = %q, want the compact outcome", tool.Summary)
	}
	if tool.Status != ToolDone {
		t.Errorf("status = %q, want done", tool.Status)
	}
}

// A failed finish updates the same block and stays visibly a failure.
func TestToolFailedUpdatesSameBlock(t *testing.T) {
	s := New()
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "FAIL TestFoo\n", Summary: "error: FAIL TestFoo", IsError: true, At: now})
	tool := s.tool("c1")
	if tool == nil {
		t.Fatal("no tool block for c1")
	}
	if tool.Status != ToolError {
		t.Errorf("status = %q, want error: a failure rendered as a success", tool.Status)
	}
	if tool.Invocation != "bash go test ./..." {
		t.Errorf("invocation = %q, want the command preserved", tool.Invocation)
	}
}

// Toggling an unknown id is a no-op: no phantom block, no panic.
func TestToolToggleUnknownIDIsNoOp(t *testing.T) {
	s := New()
	s.Apply(ToolToggled{ID: "ghost"})
	if len(s.Entries) != 0 {
		t.Errorf("entries = %d, want 0 for an unknown call id", len(s.Entries))
	}
}

// Expansion flips presentation only: status, text, and summary are untouched.
func TestToolTogglePreservesExecutionState(t *testing.T) {
	s := New()
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "ok\n", Summary: "", IsError: false, At: now})
	before := *s.tool("c1")
	s.Apply(ToolToggled{ID: "c1"})
	after := s.tool("c1")
	if !after.Expanded {
		t.Error("toggle did not expand the block")
	}
	if after.Status != before.Status || after.Text != before.Text || after.Summary != before.Summary ||
		after.Invocation != before.Invocation || after.Name != before.Name || after.ID != before.ID {
		t.Errorf("toggle mutated execution state: before %+v, after %+v", &before, after)
	}
	s.Apply(ToolToggled{ID: "c1"})
	if s.tool("c1").Expanded {
		t.Error("second toggle did not collapse: the toggle is not idempotent")
	}
}

// The latest tool block is the Ctrl+O target; empty transcript has none.
func TestLatestToolIDTargetsMostRecent(t *testing.T) {
	s := New()
	if got := s.LatestToolID(); got != "" {
		t.Errorf("latest = %q, want empty with no tools", got)
	}
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "x", At: now})
	s.Apply(AssistantDelta{Text: "answer"})
	s.Apply(ToolStarted{ID: "c2", Name: "bash", At: now})
	if got := s.LatestToolID(); got != "c2" {
		t.Errorf("latest = %q, want c2 even with assistant text between tools", got)
	}
}

// Ctrl+O toggles exactly the latest block and leaves its sibling collapsed.
func TestCtrlOTogglesOnlyLatestBlock(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	now := time.Now()
	p.state.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: now})
	p.state.Apply(ToolFinished{ID: "c1", Text: "aaa\nbbb\n", Summary: "2 lines", At: now})
	p.state.Apply(ToolStarted{ID: "c2", Name: "read", Summary: "read b.go", At: now})
	p.state.Apply(ToolFinished{ID: "c2", Text: "ccc\nddd\n", Summary: "2 lines", At: now})

	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !p.state.tool("c2").Expanded {
		t.Error("ctrl+o did not expand the latest block")
	}
	if p.state.tool("c1").Expanded {
		t.Error("ctrl+o expanded a sibling block: identity was not preserved")
	}
	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if p.state.tool("c2").Expanded {
		t.Error("second ctrl+o did not collapse the block")
	}
}

// Ctrl+O with no tool block is a silent no-op, never a quit or a prompt.
func TestCtrlOWithoutToolsIsNoOp(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(AssistantDelta{Text: "hello"})
	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if len(p.state.Entries) != 1 || p.state.Quit || p.state.Pending != nil {
		t.Errorf("ctrl+o without tools changed state: %+v", p.state)
	}
}

// An open approval owns the keyboard: Ctrl+O must not disturb it.
func TestCtrlODuringApprovalIsNoOp(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	now := time.Now()
	p.state.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash rm -rf /", At: now})
	p.state.Apply(ToolFinished{ID: "c1", Text: "x\ny\n", At: now})
	p.state.Apply(ApprovalAsked{ToolName: "bash", Reason: "runs", Summary: "bash x", Choices: approvalChoices})
	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if p.state.Pending == nil {
		t.Error("ctrl+o dismissed the approval prompt")
	}
	if p.state.tool("c1").Expanded {
		t.Error("ctrl+o expanded a block while a modal owned the keyboard")
	}
}

// Collapsed shows badge plus one compact outcome line; the body stays hidden
// until expansion, and collapse restores the exact compact form.
func TestCollapsedHidesBodyExpandedRestores(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "FAIL TestFoo\nexpected 1\ngot 2\n", Summary: "error: FAIL TestFoo", IsError: true, At: now})

	collapsed := ansi.Strip(Render(s))
	if !strings.Contains(collapsed, "bash go test ./...") {
		t.Errorf("collapsed lost the invocation:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "error: FAIL TestFoo") {
		t.Errorf("collapsed lost the compact outcome:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "expected 1") {
		t.Errorf("collapsed leaked the raw body:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Errorf("collapsed hides the expand affordance:\n%s", collapsed)
	}

	s.Apply(ToolToggled{ID: "c1"})
	expanded := ansi.Strip(Render(s))
	if !strings.Contains(expanded, "expected 1") || !strings.Contains(expanded, "got 2") {
		t.Errorf("expanded lost the failure detail:\n%s", expanded)
	}
	if !strings.Contains(expanded, "✗ bash") {
		t.Errorf("expanded lost the failure badge:\n%s", expanded)
	}

	s.Apply(ToolToggled{ID: "c1"})
	if got := ansi.Strip(Render(s)); got != collapsed {
		t.Errorf("collapse did not restore the compact form:\nbefore:\n%s\nafter:\n%s", collapsed, got)
	}
}

// Success detail is visible when expanded and stays a success.
func TestExpandedSuccessShowsDetail(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read loop.go", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "package agent\nfunc Run() {}\n", Summary: "2 lines", At: now})
	s.Apply(ToolToggled{ID: "c1"})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "package agent") {
		t.Errorf("expanded success lost its detail:\n%s", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("success rendered as a failure:\n%s", out)
	}
}

// A running tool shows its invocation with a running state and no outcome.
func TestRunningToolShowsInvocation(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: time.Now()})
	out := stripForTest(t, Render(s))
	if !strings.Contains(out, "bash go test ./...") || !strings.Contains(out, "running") {
		t.Errorf("running block = %q, want the invocation and its running state", out)
	}
}

// Long arguments stay bounded on the badge line.
func TestLongInvocationIsBounded(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	long := "read " + strings.Repeat("deep/nested/", 20) + "file.go"
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: long, At: time.Now()})
	for i, line := range strings.Split(Render(s), "\n") {
		if CellWidth(line) > 80 {
			t.Errorf("line %d overflows an 80-cell terminal: %q", i, line)
		}
	}
	if out := stripForTest(t, Render(s)); !strings.Contains(out, "read") {
		t.Errorf("bounded invocation lost the tool name:\n%s", out)
	}
}

// An expanded long result stays bounded by the same limits as before.
func TestExpandedLongResultIsBounded(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 40})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now})
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("output line with enough words to wrap and fill rows\n")
	}
	s.Apply(ToolFinished{ID: "c1", Text: sb.String(), Summary: "200 lines", At: now})
	s.Apply(ToolToggled{ID: "c1"})
	out := Render(s)
	if !strings.Contains(out, "truncated") {
		t.Error("expanded long result lost the truncation marker")
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 80 {
			t.Errorf("line %d overflows an 80-cell terminal", i)
		}
	}
}

// Empty results render a badge with no broken outcome line.
func TestEmptyResultRendersCleanly(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash true", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "", Summary: "", At: now})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "bash true") {
		t.Errorf("empty result lost the invocation:\n%s", out)
	}
	if strings.Contains(out, "⎿") {
		t.Errorf("empty result rendered a hollow outcome line:\n%s", out)
	}
}

// CJK and emoji content stays on rune boundaries and inside the viewport.
func TestToolBlocksAreUnicodeSafe(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 24})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read 日本語/日本語ファイル.go", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "日本語の内容 🎉\n第二行 🚀\n", Summary: "2 lines", At: now})
	s.Apply(ToolToggled{ID: "c1"})
	out := Render(s)
	// Valid input must never gain a replacement glyph: one would mean a cut
	// landed inside a multibyte rune.
	if strings.Contains(out, "�") {
		t.Errorf("multibyte content damaged:\n%q", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 40 {
			t.Errorf("line %d overflows a 40-cell terminal: %q", i, line)
		}
	}
	if stripped := ansi.Strip(out); !strings.Contains(stripped, "日本語") {
		t.Errorf("unicode content lost:\n%s", stripped)
	}
}

// The tool → streaming answer order survives: the tool block stays above the
// prose that follows it, with no duplicate entries.
func TestToolThenAnswerStaysOrdered(t *testing.T) {
	s := New()
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "contents here\n", Summary: "", At: now})
	s.Apply(AssistantDelta{Text: "the file holds the config"})
	s.Apply(AssistantDone{})
	if len(s.Entries) != 2 {
		t.Fatalf("entries = %d, want tool + answer", len(s.Entries))
	}
	if s.Entries[0].Kind != EntryTool || s.Entries[1].Kind != EntryAssistant {
		t.Errorf("order = %q, %q, want tool then answer", s.Entries[0].Kind, s.Entries[1].Kind)
	}
	out := ansi.Strip(Render(s))
	if strings.Index(out, "read a.go") > strings.Index(out, "the file holds") {
		t.Errorf("tool block rendered below the answer:\n%s", out)
	}
}

// Toggling must not disturb the viewport: scroll offset and follow state are
// presentation-chrome the toggle does not own.
func TestTogglePreservesScrollState(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 10})
	for i := 0; i < 10; i++ {
		s.Apply(Notice{Text: "older"})
	}
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "x\ny\n", At: now})
	s.Apply(ScrollMoved{Delta: 4})
	scroll, follow := s.Scroll, s.Follow
	s.Apply(ToolToggled{ID: "c1"})
	if s.Scroll != scroll || s.Follow != follow {
		t.Errorf("toggle moved the viewport: scroll %d→%d follow %v→%v", scroll, s.Scroll, follow, s.Follow)
	}
}

// Live and fullscreen project the same semantic block: the sealed live lines
// carry the same badge and outcome as the fullscreen render.
func TestLiveAndFullscreenShareToolProjection(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 30})
	now := time.Now()
	var history []string
	history = append(history, liveEvent(p, ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: now})...)
	history = append(history, liveEvent(p, ToolFinished{ID: "c1", Text: "one\ntwo\n", Summary: "2 lines", At: now})...)
	history = append(history, liveEvent(p, AssistantDone{})...)
	sealed := ansi.Strip(strings.Join(history, "\n"))
	full := ansi.Strip(Render(p.state))
	for _, want := range []string{"read a.go", "2 lines"} {
		if !strings.Contains(sealed, want) {
			t.Errorf("live projection lost %q:\n%s", want, sealed)
		}
		if !strings.Contains(full, want) {
			t.Errorf("fullscreen projection lost %q:\n%s", want, full)
		}
	}
	_ = painter
}

// Expanding a sealed block in live mode prints its bounded detail once as new
// scrollback; collapsing flips state without rewriting sealed lines.
func TestLiveExpandSealedBlockPrintsDetailOnce(t *testing.T) {
	p, _ := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 30})
	now := time.Now()
	for _, e := range []Event{
		ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now},
		ToolFinished{ID: "c1", Text: "FAIL TestFoo\nexpected 1\n", Summary: "error: FAIL TestFoo", IsError: true, At: now},
		AssistantDone{},
	} {
		p.state.Apply(e)
		p.live.render(p.state)
	}
	_, cmd := p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !p.state.tool("c1").Expanded {
		t.Fatal("ctrl+o did not expand the sealed block")
	}
	if cmd == nil {
		t.Fatal("expanding a sealed block produced no output: the detail is unreachable in live mode")
	}
	msg := cmd()
	if got := fmt.Sprintf("%T", msg); !strings.Contains(strings.ToLower(got), "printline") {
		t.Fatalf("expand command sent %s, want a print line for the scrollback", got)
	}
	body := ansi.Strip(fmt.Sprintf("%v", msg))
	if !strings.Contains(body, "expected 1") {
		t.Errorf("printed detail lost the failure content:\n%s", body)
	}
	for _, seq := range append(append([]string{}, mouseModeSequences...), "\x1b[?1049h", "\x1b[?1049l") {
		if strings.Contains(fmt.Sprintf("%v", msg), seq) {
			t.Errorf("expansion requested %q: detail must not cost terminal ownership", seq)
		}
	}
	_, cmd = p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if p.state.tool("c1").Expanded {
		t.Error("second ctrl+o did not collapse")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("collapse printed %v: sealed scrollback is append-only, collapse is state-only", msg)
		}
	}
}

// Expanding a running tool in live mode keeps it in the live region instead
// of sealing it early as if it were done.
func TestLiveExpandRunningToolStaysLive(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 30})
	now := time.Now()
	liveEvent(p, ToolStarted{ID: "c1", Name: "bash", Summary: "bash go test ./...", At: now})
	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !p.state.tool("c1").Expanded {
		t.Fatal("ctrl+o did not expand the running block")
	}
	if got := ansi.Strip(painter.view()); !strings.Contains(got, "bash") {
		t.Errorf("expanded running tool left the live region: %q", got)
	}
	if p.live.painted != 0 {
		t.Errorf("painted = %d, want 0: expanding must not seal a running tool", p.live.painted)
	}
}
