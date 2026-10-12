package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Advertised commands must parse: every non-hidden registry entry is accepted
// by LookupCommand, so the menu can never show what the parser rejects.
func TestAdvertisedCommandsParse(t *testing.T) {
	for _, name := range []string{"ask", "fuzz", "build", "model"} {
		cmd, ok := ParseCommand("/" + name)
		if !ok {
			t.Fatalf("/%s did not parse", name)
		}
		if _, known := LookupCommand(cmd.Name); !known {
			t.Errorf("/%s advertised but parser says unknown", name)
		}
	}
}

// Completion must contain the workspace commands plus /model.
func TestSlashCompletionContainsWorkspaceAndModel(t *testing.T) {
	got := map[string]bool{}
	for _, spec := range SlashSuggestions("/") {
		got[spec.Name] = true
	}
	for _, name := range []string{"ask", "fuzz", "build", "model", "help"} {
		if !got[name] {
			t.Errorf("completion missing /%s: %+v", name, got)
		}
	}
}

// Help must keep the explicit hierarchy: WORKSPACE before CONTROL.
func TestHelpTextGroupsWorkspaceBeforeControl(t *testing.T) {
	text := HelpText()
	wi := strings.Index(text, CommandGroupWorkspace)
	ci := strings.Index(text, CommandGroupControl)
	if wi < 0 || ci < 0 || wi > ci {
		t.Errorf("help hierarchy broken:\n%s", text)
	}
	for _, name := range []string{"/ask", "/fuzz", "/build", "/model", "/help"} {
		if !strings.Contains(text, name) {
			t.Errorf("help missing %s:\n%s", name, text)
		}
	}
}

// The inline menu must render the hierarchy, not a flat list.
func TestSlashMenuRendersHierarchy(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "/", Cursor: 1})
	out := Render(p.state)
	if !strings.Contains(out, CommandGroupWorkspace) || !strings.Contains(out, CommandGroupControl) {
		t.Errorf("slash menu lost its hierarchy:\n%s", out)
	}
}

// The menu must mark the current workspace without duplicating chrome.
func TestSlashMenuMarksCurrentWorkspace(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	p.state.Apply(ComposerChanged{Text: "/", Cursor: 1})
	out := Render(p.state)
	if !strings.Contains(out, "current") {
		t.Errorf("menu does not mark the current workspace:\n%s", out)
	}
}

// Workspace commands must dispatch through the owner seam, never TUI state.
func TestWorkspaceCommandsDispatchThroughOwner(t *testing.T) {
	for _, name := range []string{"ask", "fuzz", "build"} {
		p := NewProgram(nil, "test", 0)
		var switched string
		p.SetWorkspaceSwitcher(WorkspaceHooks{
			Current: func() string { return "none" },
			Switch:  func(n string) error { switched = n; return nil },
		})
		Command{Name: name}.runOn(p)
		if switched != name {
			t.Errorf("/%s switched to %q, want the owner seam called", name, switched)
		}
	}
}

// Without an owner the commands must say so, not fail silently.
func TestWorkspaceCommandsWithoutOwnerReport(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "ask"}.runOn(p)
	found := false
	for _, e := range p.state.Entries {
		if e.Kind == EntryNotice && strings.Contains(e.Notice, "unavailable") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the unavailable report", p.state.Entries)
	}
}

// A running turn keeps its authority: switching mid-turn is refused.
func TestWorkspaceSwitchRefusedWhileBusy(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "work"})
	called := false
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Switch: func(n string) error { called = true; return nil },
	})
	Command{Name: "ask"}.runOn(p)
	if called {
		t.Error("owner called mid-turn: one turn must not span two authorities")
	}
	found := false
	for _, e := range p.state.Entries {
		if e.Kind == EntryNotice && strings.Contains(e.Notice, "wait for the turn") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the turn-boundary refusal", p.state.Entries)
	}
}

// Cancelling a turn must reach one terminal Cancelled state with no spinner.
func TestCancelReachesTerminalIdle(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "work"})
	s.Apply(TurnCancelled{})
	if !s.Cancelled {
		t.Error("no terminal Cancelled state recorded")
	}
	if s.Status.Busy {
		t.Error("Busy still true after cancellation: footer would claim working")
	}
}

// Repeated cancellation is idempotent: one transition, one line.
func TestDuplicateCancelIsIdempotent(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "work"})
	s.Apply(TurnCancelled{Text: "cancelled"})
	s.Apply(TurnCancelled{Text: "cancelled"})
	s.Apply(TurnCancelled{Text: "cancelled"})
	if !s.Cancelled {
		t.Fatal("cancellation lost")
	}
	if s.Status.Busy {
		t.Error("Busy still true after cancellation")
	}
	n := 0
	for _, e := range s.Entries {
		if e.Kind == EntryNotice && e.Notice == "cancelled" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("cancellation lines = %d, want exactly one semantic transition", n)
	}
}

// Late progress after cancellation must not resurrect Running.
func TestLateEventsCannotResurrectCancelledTurn(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "work"})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "bash"})
	s.Apply(TurnCancelled{})
	s.Apply(ToolPreview{ID: "c1", Summary: "late"})
	s.Apply(ToolFinished{ID: "c1", Text: "late", Summary: "late"})
	s.Apply(AssistantDelta{Text: "late"})
	s.Apply(AssistantDone{})
	if !s.Cancelled {
		t.Error("terminal Cancelled state lost to a late event")
	}
	if s.Status.Busy {
		t.Error("Busy resurrected by a late event: footer would claim working")
	}
	for _, e := range s.Entries {
		if e.Kind == EntryTool && e.Tool != nil && e.Tool.ID == "c1" && e.Tool.Status == ToolRunning {
			t.Error("tool block resurrected to running after cancellation")
		}
	}
	if strings.Contains(s.AssistantText(), "late") {
		t.Error("late assistant text leaked past the terminal state")
	}
}

// Stale turns must not mutate the new turn.
func TestStaleTurnEventsIgnored(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "first", Seq: 1})
	s.Apply(UserSubmitted{Text: "second", Seq: 2})
	s.Apply(AssistantDone{Seq: 1})
	if !s.Status.Busy {
		t.Error("stale AssistantDone cleared the new turn: Busy must stay true")
	}
	s.Apply(AssistantDelta{Text: "stale", Seq: 1})
	if strings.Contains(s.AssistantText(), "stale") {
		t.Error("stale delta leaked into the new turn")
	}
	s.Apply(AssistantDone{Seq: 2})
	if s.Status.Busy {
		t.Error("current AssistantDone did not close the turn")
	}
}

// Ctrl+C during a turn cancels; Esc with the model plane open closes it.
func TestCtrlCCancelsBusyTurn(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "work"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !p.state.Cancelled || p.state.Status.Busy {
		t.Errorf("Cancelled=%v Busy=%v, want terminal idle", p.state.Cancelled, p.state.Status.Busy)
	}
	if p.state.Quit {
		t.Error("a busy ctrl+c must cancel, never quit")
	}
}

func TestEscClosesModelPlaneWithoutCancelling(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "work"})
	p.state.Apply(ModelPlaneOpened{})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	// The plane consumes Esc stepwise; it must never flip the runtime into
	// Cancelled as a side effect of closing chrome.
	if p.state.Cancelled {
		t.Error("closing the model plane cancelled the turn")
	}
}

// A failing turn must stay distinguishable from a cancelled one.
func TestFailedTurnIsNotCancelled(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "work"})
	s.Apply(Notice{Text: "error: boom"})
	s.Apply(AssistantDone{})
	if s.Cancelled {
		t.Error("a failed turn was recorded as cancelled")
	}
	if s.Status.Busy {
		t.Error("Busy still true after failure: spinner must stop on every terminal state")
	}
}

// Completed turns also converge the spinner.
func TestCompletedTurnStopsSpinner(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "work"})
	s.Apply(AssistantDone{})
	if s.Status.Busy {
		t.Error("Busy still true after completion")
	}
	if s.Cancelled {
		t.Error("a completed turn was recorded as cancelled")
	}
}
