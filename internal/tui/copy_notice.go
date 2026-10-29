package tui

// Transient clipboard confirmation.
//
// A successful mouse selection copy surfaces a brief "Copied" confirmation
// in the existing top header, aligned to the far right. It is ephemeral UI
// state only: it never enters the document, the transcript, the session, the
// runtime authority, or any execution state. It lives in State alongside the
// selection and the viewport, and it is rendered through the existing header
// path, never as transcript content or raw terminal writes.
//
// Lifetime is event-driven: MouseUp finalizes the logical selection, the
// clipboard path reports success, and only then is the status set. Expiry is
// a Bubble Tea message carrying the generation that set it, so a stale timer
// can never clear a newer confirmation. The latest copy owns the expiry.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// copyNoticeText is the only transient clipboard confirmation text.
const copyNoticeText = "Copied"

// copyNoticeTTL is how long the confirmation stays visible. It sits inside
// the required 1.5-2.0s window: long enough to read, short enough to feel
// instantaneous on the drag-release-paste path.
const copyNoticeTTL = 1500 * time.Millisecond

// copyNoticeExpiredMsg clears a transient clipboard confirmation. It carries
// the generation that scheduled it; a message whose generation trails the
// current one is stale and must be ignored.
type copyNoticeExpiredMsg struct {
	gen uint64
}

// copyNoticeExpireCmd schedules the expiry of one confirmation generation.
// It is a single one-shot timer per copy, not a loop, and it never touches
// state directly: the update goroutine owns the clear.
func copyNoticeExpireCmd(gen uint64) tea.Cmd {
	return tea.Tick(copyNoticeTTL, func(time.Time) tea.Msg {
		return copyNoticeExpiredMsg{gen: gen}
	})
}

// markCopySuccess records a successful clipboard write and schedules its
// expiry. It must only be called after the clipboard path reports success:
// empty, disabled, unsupported, or failed writes never reach here, so they
// never show a false confirmation.
func (p *Program) markCopySuccess() tea.Cmd {
	p.state.CopyGen++
	p.state.CopyNotice = copyNoticeText
	return copyNoticeExpireCmd(p.state.CopyGen)
}

// clearCopyNotice clears the confirmation when its generation matches the
// current one. It reports whether the state changed, so stale expiries are
// observable as no-ops.
func (s *State) clearCopyNotice(gen uint64) bool {
	if s.CopyGen != gen {
		return false
	}
	if s.CopyNotice == "" {
		return false
	}
	s.CopyNotice = ""
	return true
}
