package tui

import "io"

// Terminal mode control.
//
// The wheel and text selection are in tension: the usual way to give a program
// the wheel is to ask for mouse reporting, but once the terminal is sending
// mouse events it stops performing its own selection, so drag-select and copy
// break. That is why mouse reporting stays off (see Program.Run).
//
// Alternate scroll mode (DEC private mode 1007) resolves the conflict without a
// mouse protocol at all. The terminal keeps selection and copy exactly as they
// are, and translates wheel ticks into plain arrow keys for the program. The
// program then keeps parsing whole, well-delimited key sequences instead of a
// mouse report that can split across input reads.
const (
	alternateScrollOn  = "\x1b[?1007h"
	alternateScrollOff = "\x1b[?1007l"
)

// setAlternateScroll turns the mode on or off on the program's terminal.
//
// It is written outside Bubble Tea's render loop, before and after the session,
// so the sequence can never interleave with a painted frame.
func setAlternateScroll(w io.Writer, on bool) {
	if w == nil {
		return
	}
	seq := alternateScrollOff
	if on {
		seq = alternateScrollOn
	}
	_, _ = io.WriteString(w, seq)
}

// MouseDefaultFor resolves the mouse mode for an environment, given the value
// of TMUX.
//
// The wheel reaches a program by two different routes, and which one works
// depends on what sits between it and the terminal:
//
//   - A terminal implementing alternate scroll (1007) sends wheel ticks as
//     arrow keys, leaving selection and copy entirely to the terminal. This is
//     the preferred route: nothing is taken away from the user.
//   - A terminal or multiplexer that does not implement it sends nothing at
//     all. tmux 3.7c was measured to drop wheel events for a pane that enabled
//     1007 alone, in every combination of alt-screen and `mouse off`; only a
//     pane that requests mouse reporting is forwarded to.
//
// So the default follows the environment: with a multiplexer in the path,
// reporting is the only mode that works, and choosing otherwise would leave the
// wheel dead. tmux exports TMUX as its socket path for every pane, which makes
// it a reliable signal rather than a guess.
func MouseDefaultFor(tmuxEnv string) string {
	if tmuxEnv != "" {
		return MouseReport
	}
	return MouseSelect
}
