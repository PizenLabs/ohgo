package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Ctrl+S is the out-of-band route to $sup: it opens the overlay from inside a
// prompt being composed, so the draft must survive the chord untouched and no
// provider call may start until a request is actually submitted.
func TestCtrlSOpensSupportAndKeepsDraft(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		t.Fatal("Ctrl+S started a provider call before any request was submitted")
		return "", nil
	})
	p.SetSupportInfo(func() (string, string) { return "anthropic/claude-haiku-4-5", "minimal" })
	p.state.Apply(InputChanged{Text: "half-written prompt"})

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlS})

	if p.state.SupportPanel == nil {
		t.Fatal("Ctrl+S did not open the support overlay")
	}
	if !p.state.SupportInputFocused() {
		t.Fatal("the support input did not take focus")
	}
	if p.state.SupportPanel.Input != "" {
		t.Errorf("support input = %q, want empty on open", p.state.SupportPanel.Input)
	}
	if p.state.SupportPanel.Model != "anthropic/claude-haiku-4-5" || p.state.SupportPanel.Effort != "minimal" {
		t.Errorf("panel identity = %q/%q, want the resolved SUP role", p.state.SupportPanel.Model, p.state.SupportPanel.Effort)
	}
	if p.state.Input != "half-written prompt" {
		t.Errorf("main draft = %q, want it preserved by the chord", p.state.Input)
	}
	if p.state.Cursor != len([]rune("half-written prompt")) {
		t.Errorf("main cursor = %d, want it left at the draft's end", p.state.Cursor)
	}
	if p.state.Status.Busy {
		t.Error("Ctrl+S started a workspace turn instead of opening the overlay")
	}
}

// Without a handler the chord reports the surface unavailable rather than
// opening an overlay that could never answer, matching /sup.
func TestCtrlSWithoutSupportReportsUnavailable(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(InputChanged{Text: "draft"})

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlS})

	if p.state.SupportPanel != nil {
		t.Fatal("a missing handler still opened the panel")
	}
	if p.state.Input != "draft" {
		t.Errorf("main draft = %q, want it untouched", p.state.Input)
	}
	found := false
	for _, e := range p.state.Entries {
		if e.Kind == EntryNotice && strings.Contains(e.Notice, "$sup is unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("entries = %+v, want an unavailable notice", p.state.Entries)
	}
}

// A modal owns the keyboard: an open approval must swallow Ctrl+S rather than
// let the overlay open underneath a tool that is blocked on an answer.
func TestCtrlSDuringApprovalIsNoOp(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.state.Apply(ApprovalAsked{ToolName: "bash", Reason: "runs", Summary: "bash x", Choices: approvalChoices})

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlS})

	if p.state.Pending == nil {
		t.Error("Ctrl+S dismissed the approval prompt")
	}
	if p.state.SupportPanel != nil {
		t.Error("Ctrl+S opened the overlay while a modal owned the keyboard")
	}
}

// While the overlay is already open it owns every key: Ctrl+S must not insert
// literal text, close the panel, or otherwise reach the composer.
func TestCtrlSWhileSupportOpenIsInert(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return "answer", nil
	})
	p.state.Apply(SupportOpened{})

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlS})

	if p.state.SupportPanel == nil {
		t.Fatal("Ctrl+S closed the open overlay")
	}
	if p.state.SupportPanel.Input != "" {
		t.Errorf("support input = %q, want Ctrl+S to stay out of the composer", p.state.SupportPanel.Input)
	}
}
