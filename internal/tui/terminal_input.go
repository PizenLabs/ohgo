package tui

// Terminal-input isolation.
//
// A terminal control sequence is not text. Bubble Tea decodes a *complete* SGR
// mouse report into tea.MouseMsg, but its input decoder is byte-stream based in
// a way that matters here:
//
//   - if the leading ESC is delivered in its own read, it becomes KeyEscape and
//     the remaining bytes are decoded as printable runes;
//   - when several reports follow that ESC, every report lands in one
//     KeyRunes message (e.g. "[<65;96;26M[<65;96;26M[<65;97;24M");
//   - and if a sequence is split at any byte boundary, the pieces arrive as
//     separate KeyRunes messages.
//
// The old boundary accepted exactly one report per KeyRunes message, so the
// concatenated tail above was inserted into the composer verbatim. The fix is
// an explicit incremental recogniser at the input boundary:
//
//	Normal ──ESC──▶ Escape ──[──▶ CSI ──<──▶ SGRMouse ──params──▶ Complete
//
// It consumes any number of reports, retains a partial one until its terminator
// arrives, and only ever returns ordinary text for the composer. Nothing here
// inspects assistant text, tool output, or a rendered row: provider protocol and
// terminal protocol are both decoded at their own boundaries, and the
// presentation layer never contains a cleanup regex.
//
// ESC is ambiguous on a terminal: it is both the Escape key and the first byte
// of every CSI sequence. When mouse reporting is enabled the program therefore
// defers a lone ESC for one short frame (escDisambiguationDelay) so a fragmented
// report can claim it. A real Escape still dispatches, just after that frame.
// This is protocol disambiguation, not UI animation: no runtime path sleeps.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// maxMouseSequenceBytes bounds one report so a malformed fragment cannot grow
// memory while waiting for an impossible terminator.
const maxMouseSequenceBytes = 64

// escDisambiguationDelay is how long a lone ESC waits for the rest of a
// possible SGR mouse report. One 60 Hz frame is enough for a terminal that
// wrote the sequence in more than one packet; a real Escape key costs only this
// frame, and only while mouse reporting is enabled.
const escDisambiguationDelay = 16 * time.Millisecond

// escFlushMsg releases a deferred Escape key once no report claimed it.
type escFlushMsg struct{ seq int }

// sgrScan is the recogniser's verdict for one candidate byte slice.
type sgrScan int

const (
	// sgrNoMatch means the bytes are not an SGR mouse report and not a prefix
	// of one. They are ordinary input.
	sgrNoMatch sgrScan = iota
	// sgrPartial means more bytes are required; the candidate is retained.
	sgrPartial
	// sgrComplete means one full report was recognised.
	sgrComplete
	// sgrInvalid means the candidate began like a report and then violated the
	// grammar. Its bytes are protocol-shaped, never prose.
	sgrInvalid
)

// terminalKey is the result of decoding one key message at the terminal-input
// boundary.
type terminalKey struct {
	// msg is the ordinary key to continue with. It is only meaningful when
	// done is false.
	msg tea.KeyMsg
	// reports are semantic mouse events recovered from printable runes.
	reports []tea.MouseMsg
	// cmds are follow-up commands (the deferred-Escape timer).
	cmds []tea.Cmd
	// flushEsc reports that a deferred Escape must be processed before msg.
	flushEsc bool
	// done reports that no ordinary key remains to process.
	done bool
}

// decodeTerminalInput isolates terminal protocol from user input.
//
// It runs before any overlay or composer sees the key, so a fragmented report
// cannot be typed into a picker filter, a rename buffer, or the main composer.
func (p *Program) decodeTerminalInput(msg tea.KeyMsg) terminalKey {
	out := terminalKey{msg: msg}
	if !p.mouseReporting {
		return out
	}

	switch msg.Type {
	case tea.KeyEscape:
		// A real Escape (or the start of a new report) ends any retained
		// fragment: the terminal abandoned that sequence.
		p.mouseInput = ""
		// A second Escape is a real key: dispatch the one already waiting and
		// let this one become the new candidate.
		if p.pendingEsc {
			p.pendingEsc = false
			p.escSeq++
			out.flushEsc = true
			return out
		}
		p.pendingEsc = true
		p.mouseEscSeen = true
		p.escSeq++
		seq := p.escSeq
		out.cmds = append(out.cmds, tea.Tick(escDisambiguationDelay, func(time.Time) tea.Msg {
			return escFlushMsg{seq: seq}
		}))
		out.done = true
		return out

	case tea.KeyRunes:
		if msg.Paste {
			// Bracketed paste is user text by contract. It can legitimately
			// contain protocol-shaped prose (a documentation capture, for
			// example), so it never enters the recogniser.
			out.flushEsc = p.pendingEsc
			p.pendingEsc = false
			p.escSeq++
			p.mouseInput = ""
			p.mouseEscSeen = false
			return out
		}
		text := string(msg.Runes)
		// ESC followed by "[" (or "<", already retained) is the only way a
		// fragmented report begins. Any other key means the ESC was real.
		candidate := p.mouseEscSeen || msg.Alt || p.mouseInput != ""
		ordinary, reports, consumed := p.consumeTerminalMouse(text, candidate)
		p.mouseEscSeen = false
		if !consumed {
			// Nothing protocol-shaped was found. The deferred ESC (if any)
			// was a real key and is dispatched before this one.
			if p.pendingEsc {
				p.pendingEsc = false
				p.escSeq++
				out.flushEsc = true
			}
			out.msg = msg
			out.done = false
			return out
		}
		// Protocol claimed the pending ESC.
		if p.pendingEsc {
			p.pendingEsc = false
			p.escSeq++
		}
		out.reports = reports
		if ordinary == "" {
			out.done = !out.flushEsc
			return out
		}
		out.msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(ordinary)}
		if len(out.msg.Runes) == 1 && out.msg.Runes[0] == ' ' {
			out.msg.Type = tea.KeySpace
		}
		out.done = false
		return out

	default:
		// Any non-rune key ends a candidate. A deferred ESC is real and is
		// dispatched before this key.
		out.flushEsc = p.pendingEsc
		p.pendingEsc = false
		p.escSeq++
		p.mouseInput = ""
		p.mouseEscSeen = false
		return out
	}
}

// consumeTerminalMouse scans text for any number of SGR mouse reports.
//
// It returns the ordinary text that surrounds them, the semantic events, and
// whether any protocol bytes were recognised or retained. A partial report is
// held in p.mouseInput; a malformed report-shaped run is dropped rather than
// returned as text, because protocol bytes must never reach the composer.
func (p *Program) consumeTerminalMouse(text string, allowBare bool) (string, []tea.MouseMsg, bool) {
	if text == "" {
		return "", nil, false
	}
	continuing := p.mouseInput != ""
	// Fast path: without a retained candidate, a bare call, or any protocol
	// introducer, this is ordinary typing.
	if !continuing && !allowBare && !strings.ContainsAny(text, "\x1b[") {
		return text, nil, false
	}
	candidate := text
	if continuing {
		candidate = p.mouseInput + text
		p.mouseInput = ""
	}

	var out strings.Builder
	var reports []tea.MouseMsg
	consumed := continuing
	for i := 0; i < len(candidate); {
		c := candidate[i]
		introducer := c == '\x1b' || c == '['
		if !introducer {
			out.WriteByte(c)
			i++
			continue
		}
		// ESC is only a report introducer when it is followed by '[' (or is
		// the final byte of a retained fragment). A bare '[' is only a report
		// introducer when it is followed by '<', when it continues a retained
		// candidate, or when a preceding ESC/Alt made it significant.
		if c == '\x1b' {
			if i+1 < len(candidate) && candidate[i+1] != '[' {
				out.WriteByte(c)
				i++
				continue
			}
		} else {
			nextIsLT := i+1 < len(candidate) && candidate[i+1] == '<'
			atEnd := i+1 == len(candidate)
			attempt := nextIsLT || (i == 0 && (continuing || (allowBare && atEnd)))
			if !attempt {
				out.WriteByte(c)
				i++
				continue
			}
		}

		n, msg, state := scanSGRMouse(candidate[i:])
		switch state {
		case sgrComplete:
			consumed = true
			reports = append(reports, msg)
			i += n
		case sgrPartial:
			p.mouseInput = candidate[i:]
			return out.String(), reports, true
		case sgrInvalid:
			// Drop the report-shaped prefix up to the offending byte. The
			// offending byte resumes as ordinary text, so a malformed run can
			// never leak its digits and separators into the composer.
			consumed = true
			if n < 1 {
				n = 1
			}
			i += n
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), reports, consumed
}

// scanSGRMouse recognises one report:
//
//	[ESC] [ < Cb ; Cx ; Cy (M|m)
//
// The leading ESC is optional because Bubble Tea may already have consumed it
// as KeyEscape. Coordinates are 1-based on the wire and 0-based in tea.MouseMsg.
// Cb carries the button, the motion bit (32), the wheel bit (64), and the
// modifier bits.
//
// consumed is the number of bytes before the first violation on sgrInvalid, and
// the full length on sgrComplete and sgrPartial.
func scanSGRMouse(sequence string) (int, tea.MouseMsg, sgrScan) {
	if sequence == "" {
		return 0, tea.MouseMsg{}, sgrNoMatch
	}
	i := 0
	if sequence[i] == '\x1b' {
		i++
		if i == len(sequence) {
			return len(sequence), tea.MouseMsg{}, sgrPartial
		}
	}
	if sequence[i] != '[' {
		return 0, tea.MouseMsg{}, sgrNoMatch
	}
	i++
	if i == len(sequence) {
		return len(sequence), tea.MouseMsg{}, sgrPartial
	}
	if sequence[i] != '<' {
		return 0, tea.MouseMsg{}, sgrNoMatch
	}
	i++
	if i == len(sequence) {
		return len(sequence), tea.MouseMsg{}, sgrPartial
	}

	var values [3]int
	for field := 0; field < 3; field++ {
		if i == len(sequence) {
			return len(sequence), tea.MouseMsg{}, sgrPartial
		}
		if sequence[i] < '0' || sequence[i] > '9' {
			return i, tea.MouseMsg{}, sgrInvalid
		}
		value := 0
		digits := 0
		for i < len(sequence) && sequence[i] >= '0' && sequence[i] <= '9' {
			value = value*10 + int(sequence[i]-'0')
			i++
			digits++
			if digits > 6 {
				return i, tea.MouseMsg{}, sgrInvalid
			}
		}
		values[field] = value
		if field < 2 {
			if i == len(sequence) {
				return len(sequence), tea.MouseMsg{}, sgrPartial
			}
			if sequence[i] != ';' {
				return i, tea.MouseMsg{}, sgrInvalid
			}
			i++
		}
	}
	if i == len(sequence) {
		return len(sequence), tea.MouseMsg{}, sgrPartial
	}
	final := sequence[i]
	if final != 'M' && final != 'm' {
		return i, tea.MouseMsg{}, sgrInvalid
	}
	i++
	if i > maxMouseSequenceBytes {
		return i, tea.MouseMsg{}, sgrInvalid
	}

	cb, x, y := values[0], values[1]-1, values[2]-1
	if x < 0 || y < 0 {
		return i, tea.MouseMsg{}, sgrInvalid
	}

	msg := tea.MouseMsg{X: x, Y: y}
	if cb&4 != 0 {
		msg.Shift = true
	}
	if cb&8 != 0 {
		msg.Alt = true
	}
	if cb&16 != 0 {
		msg.Ctrl = true
	}
	button := cb & 3
	wheel := cb&64 != 0
	motion := cb&32 != 0

	switch {
	case wheel:
		msg.Action = tea.MouseActionPress
		if button == 0 {
			msg.Button = tea.MouseButtonWheelUp
		} else {
			msg.Button = tea.MouseButtonWheelDown
		}
	case motion:
		msg.Action = tea.MouseActionMotion
		msg.Button = mouseButtonFor(button)
	case final == 'm':
		msg.Action = tea.MouseActionRelease
		if button == 3 {
			msg.Button = tea.MouseButtonNone
		} else {
			msg.Button = mouseButtonFor(button)
		}
	default:
		msg.Action = tea.MouseActionPress
		msg.Button = mouseButtonFor(button)
	}
	return i, msg, sgrComplete
}

// mouseButtonFor maps the SGR button code to Bubble Tea's button vocabulary.
func mouseButtonFor(code int) tea.MouseButton {
	switch code {
	case 0:
		return tea.MouseButtonLeft
	case 1:
		return tea.MouseButtonMiddle
	case 2:
		return tea.MouseButtonRight
	default:
		return tea.MouseButtonNone
	}
}
