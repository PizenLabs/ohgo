package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// sendEscape delivers a lone Escape key. It is deferred while mouse reporting
// is on, so callers that need the semantic key must also run the returned
// command (see flushDeferredEscape).
func sendEscape(p *Program) []tea.Cmd {
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEscape})
	return batchCmds(cmd)
}

// batchCmds flattens one command, expanding tea.Batch.
func batchCmds(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return append([]tea.Cmd(nil), batch...)
	}
	// The command already ran; return a command that replays its message so
	// callers can still process it.
	return []tea.Cmd{func() tea.Msg { return msg }}
}

// flushDeferredEscape runs the deferred-Escape timer and delivers its message.
func flushDeferredEscape(t *testing.T, p *Program, cmds []tea.Cmd) {
	t.Helper()
	for _, cmd := range cmds {
		msg := cmd()
		if _, ok := msg.(escFlushMsg); ok {
			p.Update(msg)
			return
		}
	}
	t.Fatal("no deferred-Escape command was scheduled")
}

// The exact observed failure: ESC arrives alone, then one KeyRunes message
// carries three complete reports. Every byte is protocol; none is composer
// text.
func TestObservedConcatenatedMouseReportsNeverReachComposer(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)

	sendEscape(p)
	p.Update(runes("[<65;96;26M[<65;96;26M[<65;97;24M"))

	if p.state.Input != "" {
		t.Fatalf("SGR mouse reports became composer text: %q", p.state.Input)
	}
	if p.mouseEvents != 3 {
		t.Fatalf("mouseEvents = %d, want all three reports recovered", p.mouseEvents)
	}
	if p.mouseInput != "" || p.pendingEsc {
		t.Fatalf("input boundary kept protocol state: mouseInput=%q pendingEsc=%v", p.mouseInput, p.pendingEsc)
	}
}

// Consecutive reports must each become a semantic event, including when they
// arrive in separate messages.
func TestConsecutiveMouseReportsAreAllConsumed(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)

	sendEscape(p)
	p.Update(runes("[<65;96;26M"))
	p.Update(runes("[<65;96;26M"))
	p.Update(runes("[<65;97;24M"))

	if p.state.Input != "" {
		t.Fatalf("consecutive reports leaked as text: %q", p.state.Input)
	}
	if p.mouseEvents != 3 {
		t.Fatalf("mouseEvents = %d, want 3", p.mouseEvents)
	}
}

// A report split at an arbitrary byte boundary is retained until its
// terminator arrives. This drives the bytewise case Bubble Tea can produce
// when the terminal writes one byte per read.
func TestMouseReportFragmentedAtEveryByteBoundary(t *testing.T) {
	sequence := "[<64;10;5M"
	for split := 1; split < len(sequence); split++ {
		t.Run(strconv.Itoa(split), func(t *testing.T) {
			p := NewProgram(nil, "test", 0, WithMouseReporting())
			p.state = newViewportState(40, 80, 24)
			sendEscape(p)
			p.Update(runes(sequence[:split]))
			if p.state.Input != "" {
				t.Fatalf("partial report at split %d became text: %q", split, p.state.Input)
			}
			p.Update(runes(sequence[split:]))
			if p.state.Input != "" {
				t.Fatalf("completed report at split %d became text: %q", split, p.state.Input)
			}
			if p.mouseEvents != 1 {
				t.Fatalf("split %d recovered %d mouse events, want 1", split, p.mouseEvents)
			}
		})
	}
}

// Every byte as its own message is the worst case: ESC, then "[", "<", digits,
// separators, and the terminator each arrive separately.
func TestMouseReportFragmentedBytewise(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)
	sendEscape(p)
	for _, r := range "[<64;10;5M" {
		p.Update(runes(string(r)))
	}
	if p.state.Input != "" {
		t.Fatalf("bytewise report leaked as text: %q", p.state.Input)
	}
	if p.mouseEvents != 1 {
		t.Fatalf("mouseEvents = %d, want 1", p.mouseEvents)
	}
}

// A complete report followed by a fragmented one in the same rune batch keeps
// scanning after the first completes.
func TestMixedCompleteAndFragmentedReports(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)
	sendEscape(p)
	p.Update(runes("[<64;10;5M[<64;11;6M[<65;97;"))
	if p.state.Input != "" {
		t.Fatalf("mixed batch leaked as text: %q", p.state.Input)
	}
	if p.mouseEvents != 2 {
		t.Fatalf("mouseEvents = %d after the complete prefix, want 2", p.mouseEvents)
	}
	p.Update(runes("24M"))
	if p.state.Input != "" {
		t.Fatalf("fragmented tail leaked as text: %q", p.state.Input)
	}
	if p.mouseEvents != 3 {
		t.Fatalf("mouseEvents = %d after completion, want 3", p.mouseEvents)
	}
}

// Ordinary keystrokes still compose while reporting is on. The boundary
// consumes protocol shape only.
func TestOrdinaryTypingSurvivesMouseReporting(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.Update(runes("hello"))
	if p.state.Input != "hello" {
		t.Fatalf("input = %q, want ordinary text", p.state.Input)
	}
}

// Text that merely looks like protocol but is part of a bracketed paste is
// user content, not terminal control.
func TestBracketedPasteIsNeverProtocol(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;10;5M"), Paste: true})
	if p.state.Input != "[<64;10;5M" {
		t.Fatalf("pasted protocol-shaped prose was consumed: %q", p.state.Input)
	}
	if p.mouseEvents != 0 {
		t.Fatalf("pasted prose produced %d mouse events", p.mouseEvents)
	}
}

// Without reporting the terminal never sends mouse protocol, so protocol-like
// prose stays verbatim. This keeps documentation and pasted captures intact.
func TestProtocolLikeProseStaysVerbatimWithoutReporting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(runes("[<64;10;5M"))
	if p.state.Input != "[<64;10;5M" {
		t.Fatalf("input = %q, want the typed prose preserved", p.state.Input)
	}
}

// Bubble Tea decodes a split between "ESC[" and "<" as an Alt+[ key followed
// by the remainder. That shape must be reconstructed too, not only a lone
// Escape followed by the whole tail.
func TestAltBracketSplitMouseReport(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)

	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true})
	if p.state.Input != "" {
		t.Fatalf("Alt+[ prefix became text: %q", p.state.Input)
	}
	p.Update(runes("<64;10;5M"))
	if p.state.Input != "" {
		t.Fatalf("Alt+[ split leaked as text: %q", p.state.Input)
	}
	if p.mouseEvents != 1 {
		t.Fatalf("mouseEvents = %d, want 1", p.mouseEvents)
	}
}

// A whole report decoded by Bubble Tea is followed by a fragmented report in
// the same read. The whole event and the recovered tail must both be consumed.
func TestWholeThenFragmentedMouseReports(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)

	p.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	sendEscape(p)
	p.Update(runes("[<65;96;26M"))
	if p.state.Input != "" {
		t.Fatalf("fragmented tail after a whole report leaked: %q", p.state.Input)
	}
	if p.mouseEvents != 1 {
		t.Fatalf("mouseEvents = %d, want the recovered tail", p.mouseEvents)
	}
}

// A wheel report recovered from runes reaches the same scroll handler a whole
// report uses. Wheel up detaches from the live edge.
func TestRecoveredWheelReportScrolls(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)
	sendEscape(p)
	p.Update(runes("[<64;10;5M"))
	if p.state.Follow {
		t.Fatal("recovered wheel-up did not detach the viewport")
	}
	if p.mouseEvents != 1 {
		t.Fatalf("mouseEvents = %d, want 1", p.mouseEvents)
	}
}

// A click report becomes a semantic press, not text and not a wheel tick.
func TestMousePressReportRoutesToSelection(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.syncTranscript()

	layout := ComputeLayout(p.state)
	wireY := layout.HeaderLines + 2 + 1
	sendEscape(p)
	p.Update(runes("[<0;2;" + strconv.Itoa(wireY) + "M"))
	if p.state.Input != "" {
		t.Fatalf("mouse press became input: %q", p.state.Input)
	}
	if p.drag == nil {
		t.Fatal("mouse press did not reach the application hit-test path")
	}
}

// A real Escape key is still delivered while reporting is on; it is deferred
// by one frame so a fragmented report can claim it, not dropped.
func TestDeferredEscapeStillDispatches(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state.Picker = NewPicker(PickerSessions, "Sessions", nil)

	cmds := sendEscape(p)
	if p.state.Picker == nil {
		t.Fatal("Escape acted before a possible mouse report could claim it")
	}
	flushDeferredEscape(t, p, cmds)
	if p.state.Picker != nil {
		t.Fatal("deferred Escape never reached the semantic key handler")
	}
}

// A fragmented report that arrives before the deferred Escape timer fires
// cancels the Escape rather than closing the surface it would have acted on.
func TestMouseReportClaimsDeferredEscape(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)
	p.state.Picker = NewPicker(PickerSessions, "Sessions", nil)

	cmds := sendEscape(p)
	p.Update(runes("[<65;96;26M"))
	if p.state.Picker == nil {
		t.Fatal("mouse report was misread as Escape and closed the picker")
	}
	if p.state.Input != "" {
		t.Fatalf("mouse report became text: %q", p.state.Input)
	}
	// A stale timer must not dispatch the ESC after the report claimed it.
	for _, cmd := range cmds {
		if msg, ok := cmd().(escFlushMsg); ok {
			p.Update(msg)
		}
	}
	if p.state.Picker == nil {
		t.Fatal("stale deferred Escape closed the picker after the report claimed it")
	}
}

// Protocol bytes never survive into the transcript or an overlay buffer.
func TestMouseReportNeverEntersOverlayTyping(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state.HelpModal = &HelpCenterState{}

	sendEscape(p)
	p.Update(runes("[<65;96;26M[<65;96;26M"))
	filter := strings.TrimSpace(p.state.HelpModal.ActiveFilter())
	if filter != "" {
		t.Fatalf("mouse protocol entered the help filter: %q", filter)
	}
}
