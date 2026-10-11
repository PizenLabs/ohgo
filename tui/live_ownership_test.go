package tui

import (
	"strings"
	"testing"
	"time"
)

// Live transcript ownership: once rows are committed to the terminal,
// reconstructing them from State.Scroll must be impossible because the live
// path never reads it. This test scripts two identical sessions that differ
// only in fullscreen scroll state and requires byte-identical committed
// history, live frames, and shutdown seals.
//
// If live rendering ever consults Scroll/Follow again (e.g. viewport slicing
// reintroduced into livePainter), the scrolled session diverges and this
// fails. If fullscreen slicing breaks, the fullscreen tests fail instead:
// this test owns only the live boundary.
func TestLiveCommittedTranscriptIgnoresScrollState(t *testing.T) {
	script := func(p *Program) []string {
		var history []string
		history = append(history, liveEvent(p, UserSubmitted{Text: "what changed in auth?"})...)
		history = append(history, liveEvent(p, ToolStarted{ID: "c1", Name: "grep", Summary: "grep auth", At: time.Now()})...)
		history = append(history, liveEvent(p, ToolFinished{ID: "c1", Text: "auth/login.go:12: session check", At: time.Now()})...)
		history = append(history, liveEvent(p, AssistantDelta{Text: "First paragraph.\n\nSecond paragraph.\n"})...)
		return append(history, liveEvent(p, AssistantDone{})...)
	}

	plain, painter := newLiveProgram()
	plain.state.Apply(TerminalResized{Width: 80, Height: 24})
	plainHistory := script(plain)
	plainView := painter.view()

	scrolled, scrolledPainter := newLiveProgram()
	scrolled.state.Apply(TerminalResized{Width: 80, Height: 24})
	var scrolledHistory []string
	scrolledHistory = append(scrolledHistory, liveEvent(scrolled, UserSubmitted{Text: "what changed in auth?"})...)
	scrolled.state.Apply(ScrollMoved{Delta: 40})
	scrolledHistory = append(scrolledHistory, scrolledPainter.render(scrolled.state)...)
	scrolledHistory = append(scrolledHistory, liveEvent(scrolled, ToolStarted{ID: "c1", Name: "grep", Summary: "grep auth", At: time.Now()})...)
	scrolledHistory = append(scrolledHistory, liveEvent(scrolled, ToolFinished{ID: "c1", Text: "auth/login.go:12: session check", At: time.Now()})...)
	scrolled.state.Apply(ScrollMoved{Delta: 10})
	scrolledHistory = append(scrolledHistory, scrolledPainter.render(scrolled.state)...)
	scrolledHistory = append(scrolledHistory, liveEvent(scrolled, AssistantDelta{Text: "First paragraph.\n\nSecond paragraph.\n"})...)
	scrolledHistory = append(scrolledHistory, liveEvent(scrolled, AssistantDone{})...)
	scrolledView := scrolledPainter.view()

	if strings.Join(plainHistory, "\n") != strings.Join(scrolledHistory, "\n") {
		t.Errorf("scroll state changed committed history:\nplain %q\nscrolled %q",
			strings.Join(plainHistory, "\n"), strings.Join(scrolledHistory, "\n"))
	}
	if plainView != scrolledView {
		t.Errorf("scroll state changed the live frame:\nplain %q\nscrolled %q", plainView, scrolledView)
	}

	// Shutdown seals must agree too: the seal is terminal scrollback, and
	// scrollback is terminal-owned, never viewport-derived.
	var plainSeal, scrolledSeal strings.Builder
	// Re-seal from equal entry sets under different scroll offsets. Both
	// painters have consumed their histories, so drive fresh painters to the
	// same entry count and seal with divergent Scroll/Follow.
	for _, tc := range []struct {
		name   string
		scroll int
		follow bool
		out    *strings.Builder
	}{
		{"plain", 0, true, &plainSeal},
		{"scrolled", 25, false, &scrolledSeal},
	} {
		v := newLivePainter()
		s := New()
		s.Apply(TerminalResized{Width: 80, Height: 24})
		s.Apply(UserSubmitted{Text: "what changed in auth?"})
		s.Apply(AssistantDelta{Text: "an answer"})
		s.Apply(AssistantDone{})
		s.Scroll, s.Follow = tc.scroll, tc.follow
		v.render(s)
		v.sealToTerminal(tc.out, s)
	}
	if plainSeal.String() != scrolledSeal.String() {
		t.Errorf("scroll state changed the shutdown seal:\nplain %q\nscrolled %q",
			plainSeal.String(), scrolledSeal.String())
	}
}
