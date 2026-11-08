package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// busyInterruptProgram returns a running turn whose cancellation is observable
// through a real context. The turn is synthetic: these tests cover the key
// guard, not the agent loop.
func busyInterruptProgram(t *testing.T) (*Program, context.Context) {
	t.Helper()
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(UserSubmitted{Text: "work"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p.cancelTurn = cancel
	return p, ctx
}

// The first Esc must only arm the confirmation: the running turn keeps its
// context and the hint row names the second press.
func TestFirstEscArmsInterruptWithoutCancelling(t *testing.T) {
	p, ctx := busyInterruptProgram(t)

	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})

	if !p.state.InterruptConfirm {
		t.Fatal("first Esc did not arm the interruption confirmation")
	}
	if ctx.Err() != nil {
		t.Fatal("first Esc cancelled the running turn")
	}
	if !p.state.Status.Busy || p.state.Cancelled {
		t.Fatalf("Busy=%v Cancelled=%v, want the turn still running", p.state.Status.Busy, p.state.Cancelled)
	}
	if out := Render(p.state); !strings.Contains(out, interruptConfirmText) {
		t.Fatalf("hint row does not show the confirmation warning:\n%s", out)
	}
}

// The second Esc inside the window must take the same cancellation path as
// Ctrl+C and /cancel: the context is cancelled and the turn reaches its
// terminal state.
func TestSecondEscWithinWindowInterrupts(t *testing.T) {
	p, ctx := busyInterruptProgram(t)

	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})

	if ctx.Err() == nil {
		t.Fatal("second Esc did not cancel the running turn context")
	}
	if !p.state.Cancelled || p.state.Status.Busy {
		t.Fatalf("Cancelled=%v Busy=%v, want terminal idle", p.state.Cancelled, p.state.Status.Busy)
	}
	if p.state.InterruptConfirm {
		t.Fatal("confirmation stayed armed after the interrupt")
	}
}

// An expiry message clears only the warning. The run must keep executing, and
// the bottom row must return to its ordinary telemetry.
func TestInterruptConfirmationExpiryResetsPending(t *testing.T) {
	p, ctx := busyInterruptProgram(t)

	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	gen := p.state.InterruptGen
	p.Update(interruptConfirmExpiredMsg{gen: gen})

	if p.state.InterruptConfirm {
		t.Fatal("expired confirmation is still armed")
	}
	if ctx.Err() != nil {
		t.Fatal("timeout cancelled the running turn")
	}
	if !p.state.Status.Busy {
		t.Fatal("timeout ended the running turn")
	}
	if out := Render(p.state); strings.Contains(out, interruptConfirmText) {
		t.Fatalf("expired warning is still rendered:\n%s", out)
	}
}

// After a timeout the next Esc is a fresh first press, not an inherited
// interrupt: the window is what separates the two presses.
func TestEscAfterExpiryArmsAgainInsteadOfInterrupting(t *testing.T) {
	p, ctx := busyInterruptProgram(t)

	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	gen := p.state.InterruptGen
	p.Update(interruptConfirmExpiredMsg{gen: gen})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})

	if !p.state.InterruptConfirm {
		t.Fatal("post-expiry Esc did not start a new confirmation window")
	}
	if ctx.Err() != nil || p.state.Cancelled {
		t.Fatal("post-expiry Esc cancelled without a second press")
	}
}

// A stale timer must never clear a newer confirmation. This pins the
// generation guard that keeps an old expiry from disarming a fresh Esc.
func TestStaleInterruptExpiryDoesNotClearNewerConfirmation(t *testing.T) {
	p, _ := busyInterruptProgram(t)

	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	stale := p.state.InterruptGen
	p.state.resetInterruptConfirm()
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})

	p.Update(interruptConfirmExpiredMsg{gen: stale})

	if !p.state.InterruptConfirm {
		t.Fatal("stale expiry disarmed the newer confirmation window")
	}
}
