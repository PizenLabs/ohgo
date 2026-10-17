package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSupportOverlayLifecycle pins the ephemeral contract: a support turn
// streams into the overlay, completes there, and closes without touching
// the normal transcript or the workspace authority.
func TestSupportOverlayLifecycle(t *testing.T) {
	s := New()
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "real question"})
	s.Apply(AssistantDone{})

	s.Apply(SupportOpened{Model: "anthropic/claude-haiku-4-5", Effort: "minimal"})
	if s.SupportPanel == nil || !s.Support {
		t.Fatal("bare $sup did not open the panel")
	}
	if s.SupportPanel.Status != SupportIdle {
		t.Fatalf("fresh panel status = %q, want idle", s.SupportPanel.Status)
	}

	s.Apply(SupportStarted{Seq: 1, Request: "explain this error", Model: "anthropic/claude-haiku-4-5", Effort: "minimal"})
	s.Apply(SupportDelta{Seq: 1, Text: "it is"})
	s.Apply(SupportDelta{Seq: 1, Text: " a type error"})
	s.Apply(SupportDone{Seq: 1})
	panel := s.SupportPanel
	if panel.Status != SupportCompleted {
		t.Fatalf("support status = %q, want completed", panel.Status)
	}
	if panel.Response != "it is a type error" {
		t.Fatalf("support response = %q, want streamed text", panel.Response)
	}
	if s.Status.Workspace != "BUILD" {
		t.Fatalf("workspace = %q, want BUILD throughout support", s.Status.Workspace)
	}
	for _, entry := range s.Entries {
		if entry.Kind == EntrySupport {
			t.Fatal("support leaked an entry into the normal transcript")
		}
		if entry.Kind == EntryAssistant && strings.Contains(entry.AssistantText, "type error") {
			t.Fatal("support response leaked into assistant history")
		}
	}

	out := Render(s)
	if !strings.Contains(out, "SUPPORT") || !strings.Contains(out, "it is a type error") {
		t.Error("completed support response is not visible in the overlay")
	}
	if !strings.Contains(out, "BUILD") {
		t.Error("workspace authority badge lost while support is open")
	}

	s.Apply(SupportClosed{})
	if s.SupportPanel != nil || s.Support {
		t.Error("the surface is still open after close")
	}
	out = Render(s)
	if strings.Contains(out, "it is a type error") {
		t.Error("the ephemeral answer survived close")
	}
	if !strings.Contains(out, "real question") {
		t.Error("closing support dropped the main transcript")
	}
}

// TestSupportSecondTurnSeesCleanHistory proves no history contamination:
// normal turns before and after $sup see identical normal history.
func TestSupportSecondTurnSeesCleanHistory(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "first"})
	s.Apply(AssistantDone{})
	before := len(s.Entries)

	s.Apply(SupportOpened{})
	s.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	s.Apply(SupportDelta{Seq: 1, Text: "a"})
	s.Apply(SupportDone{Seq: 1})
	s.Apply(SupportClosed{})

	s.Apply(UserSubmitted{Text: "second"})
	s.Apply(AssistantDone{})
	if len(s.Entries) != before+1 {
		t.Fatalf("entries = %d, want %d: support left transcript residue", len(s.Entries), before+1)
	}
	for _, entry := range s.Entries {
		if entry.Kind == EntrySupport {
			t.Fatal("support entry in normal history after close")
		}
	}
}

// TestSupportEmptyOpensWithoutProvider proves a bare $sup opens the panel
// with input focus and starts no provider call.
func TestSupportEmptyOpensWithoutProvider(t *testing.T) {
	called := 0
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		called++
		return "answer", nil
	})
	p.state.Apply(InputChanged{Text: "$sup"})
	cmd := p.submit("$sup")
	if cmd != nil {
		t.Fatal("bare $sup returned a run command: no provider call may start")
	}
	if p.state.SupportPanel == nil {
		t.Fatal("bare $sup did not open the panel")
	}
	if !p.state.SupportInputFocused() {
		t.Fatal("support input is not focused after bare $sup")
	}
	if called != 0 {
		t.Fatalf("provider called %d times for bare $sup, want 0", called)
	}
	p.state.Apply(InputChanged{Text: ""})
}

// TestSupSlashCommandRetired proves /sup is gone: the ephemeral surface keeps
// only its "$sup" trigger and the Ctrl+S chord, so a slash line can no longer
// open it and cannot silently become a workspace turn.
func TestSupSlashCommandRetired(t *testing.T) {
	if _, ok := LookupCommand("sup"); ok {
		t.Fatal("/sup is still registered")
	}
	called := 0
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		called++
		return "answer", nil
	})
	p.SetSupportInfo(func() (string, string) { return "m", "minimal" })

	p.state.Apply(InputChanged{Text: "/sup explain this"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.SupportPanel != nil {
		t.Fatal("retired /sup still opened the support panel")
	}
	found := false
	for _, e := range p.state.Entries {
		if e.Kind == EntryNotice && strings.Contains(e.Notice, "unknown command /sup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("entries = %+v, want /sup reported unknown", p.state.Entries)
	}
	if called != 0 {
		t.Fatalf("provider called %d times for a retired command, want 0", called)
	}
}

// TestSupportDirectSubmitStartsTurn proves "$sup <request>" opens the panel
// and submits directly without an extra keystroke.
func TestSupportDirectSubmitStartsTurn(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.SetSupportInfo(func() (string, string) { return "anthropic/claude-haiku-4-5", "minimal" })
	cmd := p.submit("$sup explain this")
	if cmd == nil {
		t.Fatal("$sup with a request did not start a turn")
	}
	panel := p.state.SupportPanel
	if panel == nil || panel.Status != SupportRunning || panel.Request != "explain this" {
		t.Fatalf("panel = %+v, want a running turn for the request", panel)
	}
	if panel.Model != "anthropic/claude-haiku-4-5" || panel.Effort != "minimal" {
		t.Fatalf("panel identity = %q/%q, want the resolved SUP role", panel.Model, panel.Effort)
	}
}

// TestSupportInputOwnsPrintables proves no hidden printable shortcuts
// exist: every letter is composer text while the panel is open.
func TestSupportInputOwnsPrintables(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.state.Apply(SupportOpened{})
	for _, key := range []string{"e", "m", "r", "s", "c"} {
		keyPress(p, runes(key))
	}
	if p.state.SupportPanel.Input != "emrsc" {
		t.Fatalf("support input = %q, want every printable as text", p.state.SupportPanel.Input)
	}
	if p.state.SupportPanel.Status == SupportRunning {
		t.Fatal("typing a letter started a support turn")
	}
	if p.state.SupportPanel == nil {
		t.Fatal("typing a letter closed the panel")
	}
}

// TestSupportMainComposerBlocked proves the overlay owns interaction: the
// main workspace input cannot change while support is open.
func TestSupportMainComposerBlocked(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.state.Apply(InputChanged{Text: "main draft"})
	p.state.Apply(SupportOpened{})
	keyPress(p, runes("x"))
	if p.state.Input != "main draft" {
		t.Fatalf("main input = %q, want it untouched while support is open", p.state.Input)
	}
	if p.state.SupportPanel.Input != "x" {
		t.Fatalf("support input = %q, want the typed text", p.state.SupportPanel.Input)
	}
}

// TestSupportEnterSubmitsAndEscCloses drives the keyboard contract:
// Enter submits a request, Esc after completion closes the panel.
func TestSupportEnterSubmitsAndEscCloses(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.state.Apply(SupportOpened{})
	keyPress(p, runes("why"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if !p.state.SupportBusy() {
		t.Fatal("Enter did not submit the support request")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if !p.state.SupportBusy() {
		t.Fatal("Enter while running must not restart or disturb the turn")
	}
	p.state.Apply(SupportDone{Seq: p.state.SupportPanel.Seq})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.SupportPanel != nil {
		t.Fatal("Esc after completion did not close the panel")
	}
}

// TestSupportCompletionStopsSpinner proves the terminal-state invariant:
// completed means no working indicator anywhere in the overlay.
func TestSupportCompletionStopsSpinner(t *testing.T) {
	s := New()
	s.Width, s.Height = 100, 30
	s.Apply(SupportOpened{Model: "m"})
	s.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	s.Apply(SupportDelta{Seq: 1, Text: "done text"})
	s.Apply(SupportDone{Seq: 1})
	out := Render(s)
	if strings.Contains(out, "working") {
		t.Error("completed support still reports working")
	}
	if !strings.Contains(out, "done") {
		t.Error("completed support does not report done")
	}
}

// TestSupportCancelStopsSpinner proves cancellation is a state transition
// to cancelled with no lingering working indicator.
func TestSupportCancelStopsSpinner(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	p.state.Apply(SupportOpened{})
	p.state.Apply(SupportStarted{Seq: 7, Request: "q"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlC})
	panel := p.state.SupportPanel
	if panel == nil || panel.Status != SupportCancelled {
		t.Fatalf("panel = %+v, want cancelled after Ctrl+C", panel)
	}
	s := p.state
	s.Width, s.Height = 100, 30
	if out := Render(s); strings.Contains(out, "working") {
		t.Error("cancelled support still reports working")
	}
}

// TestSupportCancelIdempotent proves repeated Ctrl+C produces one semantic
// cancellation, never a stream of duplicates.
func TestSupportCancelIdempotent(t *testing.T) {
	s := New()
	s.Apply(SupportOpened{})
	s.Apply(SupportStarted{Seq: 3, Request: "q"})
	s.Apply(SupportCancelledEvent{Seq: 3, Text: "cancelled"})
	first := s.SupportPanel.Response
	s.Apply(SupportCancelledEvent{Seq: 3, Text: "cancelled"})
	s.Apply(SupportCancelledEvent{Seq: 3, Text: "cancelled"})
	if s.SupportPanel.Status != SupportCancelled {
		t.Fatalf("status = %q, want cancelled", s.SupportPanel.Status)
	}
	if s.SupportPanel.Response != first {
		t.Error("repeated cancellation mutated the panel")
	}
	// A cancellation arriving after completion must not resurrect the
	// turn: terminal stays terminal.
	s.Apply(SupportStarted{Seq: 4, Request: "q2"})
	s.Apply(SupportDone{Seq: 4})
	s.Apply(SupportCancelledEvent{Seq: 4, Text: "cancelled"})
	if s.SupportPanel.Status != SupportCompleted {
		t.Fatalf("status = %q, want completed: late cancel overwrote a terminal state", s.SupportPanel.Status)
	}
	for _, entry := range s.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "cancelled") {
			t.Fatal("cancellation wrote a notice into the normal transcript")
		}
	}
}

// TestSupportStaleEventsDropped proves old support turns cannot mutate the
// current one: late deltas, completions, and cancellations are dropped.
func TestSupportStaleEventsDropped(t *testing.T) {
	s := New()
	s.Apply(SupportOpened{})
	s.Apply(SupportStarted{Seq: 1, Request: "first"})
	s.Apply(SupportDelta{Seq: 1, Text: "old"})
	s.Apply(SupportStarted{Seq: 2, Request: "second"})
	s.Apply(SupportDelta{Seq: 1, Text: "STALE"})
	s.Apply(SupportDone{Seq: 1})
	s.Apply(SupportCancelledEvent{Seq: 1, Text: "cancelled"})
	panel := s.SupportPanel
	if panel.Seq != 2 || panel.Status != SupportRunning {
		t.Fatalf("panel = %+v, want the new turn still running", panel)
	}
	if strings.Contains(panel.Response, "STALE") {
		t.Fatalf("stale delta mutated the new turn: %q", panel.Response)
	}
	s.Apply(SupportDelta{Seq: 2, Text: "new"})
	s.Apply(SupportDone{Seq: 2})
	if panel.Response != "new" {
		t.Fatalf("response = %q, want only the current turn's text", panel.Response)
	}
}

// TestSupportFailureDistinctFromCancel proves provider errors end as
// failed, never as cancellation, with no lingering spinner.
func TestSupportFailureDistinctFromCancel(t *testing.T) {
	s := New()
	s.Width, s.Height = 100, 30
	s.Apply(SupportOpened{Model: "m"})
	s.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	s.Apply(SupportFailedEvent{Seq: 1, Error: "boom"})
	panel := s.SupportPanel
	if panel.Status != SupportFailed {
		t.Fatalf("status = %q, want failed", panel.Status)
	}
	out := Render(s)
	if !strings.Contains(out, "failed") || !strings.Contains(out, "boom") {
		t.Error("failure is not shown as failed with its error")
	}
	if strings.Contains(out, "cancelled") {
		t.Error("failure is misrepresented as cancellation")
	}
	if strings.Contains(out, "working") {
		t.Error("failed support still reports working")
	}
}

// TestSupportOverlayIsBounded proves the panel is a centered dialog, not a
// full-screen replacement: header, workspace, and status chrome survive,
// and the footer keeps naming the workspace rather than SUP.
func TestSupportOverlayIsBounded(t *testing.T) {
	s := New()
	s.Width, s.Height = 100, 30
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "real question"})
	s.Apply(AssistantDone{})
	s.Apply(SupportOpened{Model: "anthropic/claude-haiku-4-5", Effort: "minimal"})
	s.Apply(SupportStarted{Seq: 1, Request: "q", Model: "anthropic/claude-haiku-4-5", Effort: "minimal"})
	s.Apply(SupportDelta{Seq: 1, Text: "partial"})

	out := Render(s)
	if !strings.Contains(out, "ohgo") {
		t.Error("overlay replaced the application header")
	}
	if !strings.Contains(out, "SUPPORT") {
		t.Error("support is not visually distinguishable from the conversation")
	}
	if !strings.Contains(out, "anthropic/claude-haiku-4-5") {
		t.Error("overlay does not show the effective SUP model")
	}
	for _, line := range strings.Split(out, "\n") {
		// Dialog rows pad to the terminal width with blank margins, like
		// every centered modal: the dialog itself is the bordered box,
		// whose content width stays well under the terminal width.
		trimmed := strings.TrimSpace(stripANSI(line))
		if strings.Contains(trimmed, "SUPPORT") && CellWidth(trimmed) >= 100 {
			t.Errorf("overlay dialog spans the full terminal width: %q", trimmed)
		}
	}
	rect := SupportModalRect(100, 30)
	if rect.Width >= 100 || rect.Height >= 30 {
		t.Fatalf("support rect = %+v, want a bounded dialog", rect)
	}
	if rect.X <= 0 {
		t.Errorf("support rect = %+v, want horizontal centering margins", rect)
	}
}

// TestSupportModalRectStaysInsideTerminal pins the geometry contract.
func TestSupportModalRectStaysInsideTerminal(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {70, 20}, {50, 15}, {40, 10}, {30, 8}} {
		rect := SupportModalRect(size[0], size[1])
		if rect.Width <= 0 || rect.Height <= 0 {
			t.Fatalf("size %v: empty rect %+v", size, rect)
		}
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.Width > size[0] || rect.Y+rect.Height > size[1] {
			t.Errorf("size %v: rect %+v escapes the terminal", size, rect)
		}
		if size[0] >= 70 && rect.Width >= size[0] {
			t.Errorf("size %v: rect %+v is a full-screen replacement", size, rect)
		}
	}
}

// TestSupportRefusesNestedSurfaces proves the one-overlay rule: workspace
// switching and /model are refused while support is open.
func TestSupportRefusesNestedSurfaces(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	switched := false
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return "BUILD" },
		Switch:  func(name string) error { switched = true; return nil },
	})
	p.state.Apply(SupportOpened{})
	p.switchWorkspace("ask")
	if switched {
		t.Fatal("workspace switched underneath an open support panel")
	}
	p.runCommand(Command{Name: "model"})
	if p.state.Plane != nil {
		t.Fatal("/model opened over an open support panel")
	}
	p.state.Apply(SupportClosed{})
	p.switchWorkspace("ask")
	if !switched {
		t.Error("workspace switching did not work after support closed")
	}
}

// TestSupportClearsInvoker proves the invoking line leaves the main
// composer when the overlay opens: it must never resubmit as a workspace
// turn or concatenate with the next command.
func TestSupportClearsInvoker(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.state.Apply(InputChanged{Text: "$sup"})
	p.submit("$sup")
	if p.state.Input != "" {
		t.Fatalf("main input = %q, want it cleared when support opens", p.state.Input)
	}
	p.state.Apply(SupportClosed{})
	p.state.Apply(InputChanged{Text: "$sup explain"})
	p.submit("$sup explain")
	if p.state.Input != "" {
		t.Fatalf("main input = %q, want it cleared on direct submit", p.state.Input)
	}
}

// TestSupportRunStreamsToCompletion drives the async path with a scripted
// handler: deltas arrive in order and the terminal event carries only the
// unstreamed tail, never a duplicate.
func TestSupportRunStreamsToCompletion(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		onDelta("chunk one ")
		onDelta("chunk two")
		return "chunk one chunk two", nil
	})
	p.state.Apply(SupportOpened{})
	p.state.SupportSeq++
	seq := p.state.SupportSeq
	p.state.Apply(SupportStarted{Seq: seq, Request: "q"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runSupport(context.Background(), seq, "q", p.support)
	}()
	timeout := time.After(5 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case event := <-p.events:
			p.state.Apply(event)
		case <-timeout:
			t.Fatal("timed out waiting for support events")
		}
	}
	<-done
	panel := p.state.SupportPanel
	if panel.Status != SupportCompleted {
		t.Fatalf("status = %q, want completed", panel.Status)
	}
	if panel.Response != "chunk one chunk two" {
		t.Fatalf("response = %q, want streamed text exactly once", panel.Response)
	}
}
