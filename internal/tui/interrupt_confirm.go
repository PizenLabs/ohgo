package tui

// Double-tap Esc interruption.
//
// Esc is a reflex key, and during a running turn it used to cancel the stream
// on the first press. A single stray Escape could therefore discard minutes of
// model or tool work with no way to undo it. The first Esc now arms a short
// confirmation window instead of cancelling: the hint row asks for a second
// Esc, and only that second press reaches the same cancellation path as
// Ctrl+C and /cancel. Ctrl+C and /cancel stay single-step because they are
// deliberate control chords, not a reflex.
//
// The state is ephemeral UI only: it never enters the transcript, the session,
// the runtime authority, or the agent context. Expiry is a Bubble Tea message
// carrying the generation that armed it, so a stale timer can never clear a
// newer confirmation.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// interruptConfirmText is the warning shown while a second Esc is awaited.
const interruptConfirmText = "Press Esc again to interrupt execution"

// interruptConfirmTTL is how long the first Esc waits for its confirmation.
// It is long enough for a deliberate second tap, short enough that a stray
// Escape stops changing the hint row almost immediately.
const interruptConfirmTTL = 2 * time.Second

// interruptConfirmExpiredMsg closes one confirmation window. It carries the
// generation that armed it; a message whose generation trails the current one
// is stale and must be ignored.
type interruptConfirmExpiredMsg struct {
	gen uint64
}

// interruptConfirmExpireCmd schedules the expiry of one confirmation
// generation. It is a single one-shot timer per arm, not a loop, and it never
// touches state directly: the update goroutine owns the clear.
func interruptConfirmExpireCmd(gen uint64) tea.Cmd {
	return tea.Tick(interruptConfirmTTL, func(time.Time) tea.Msg {
		return interruptConfirmExpiredMsg{gen: gen}
	})
}

// armInterruptConfirm records the first Esc and schedules its expiry.
func (p *Program) armInterruptConfirm() tea.Cmd {
	p.state.InterruptGen++
	p.state.InterruptConfirm = true
	return interruptConfirmExpireCmd(p.state.InterruptGen)
}

// expireInterruptConfirm clears the confirmation when its generation matches
// the current one. It reports whether the state changed, so stale expiries
// are observable as no-ops.
func (s *State) expireInterruptConfirm(gen uint64) bool {
	if s.InterruptGen != gen {
		return false
	}
	if !s.InterruptConfirm {
		return false
	}
	s.InterruptConfirm = false
	return true
}

// resetInterruptConfirm disarms any confirmation and invalidates its pending
// timer. It is used when the window is consumed by a real interrupt or when
// the turn it guarded ends, so a later Esc starts a fresh first press.
func (s *State) resetInterruptConfirm() {
	s.InterruptGen++
	s.InterruptConfirm = false
}

// interruptConfirmVisible reports whether the warning belongs on the bottom
// row. The guard binds the warning to the turn it protects: a turn that has
// already ended must never leave a stale instruction on screen.
func interruptConfirmVisible(state State) bool {
	return state.InterruptConfirm && state.Status.Busy
}
