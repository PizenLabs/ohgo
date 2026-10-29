package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// longSupportAnswer builds a multi-paragraph markdown answer that overflows
// the dialog on a standard terminal.
func longSupportAnswer() string {
	var sb strings.Builder
	sb.WriteString("Java is a general-purpose, object-oriented programming language.\n\n")
	sb.WriteString("**Key characteristics:**\n\n")
	for i := 1; i <= 20; i++ {
		sb.WriteString("- **Feature " + itoaTest(i) + "**: description of feature number " + itoaTest(i) + " with enough words to wrap\n")
	}
	sb.WriteString("\n```java\nclass Main { public static void main(String[] a) {} }\n```\n")
	return sb.String()
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func openCompletedSupport(t *testing.T, answer string) State {
	t.Helper()
	s := New()
	s.Width, s.Height = 100, 30
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(SupportOpened{Model: "openrouter/cohere/north-mini-code:free", Effort: "minimal"})
	s.Apply(SupportStarted{Seq: 1, Request: "what is java?", Model: "openrouter/cohere/north-mini-code:free", Effort: "minimal"})
	s.Apply(SupportDone{Seq: 1, Text: answer})
	if s.SupportPanel.Status != SupportCompleted {
		t.Fatalf("status = %q, want completed", s.SupportPanel.Status)
	}
	return s
}

// TestSupportRendersMarkdownNotMarkers proves the response goes through the
// markdown layer: emphasis becomes styling, bullets become markers, and raw
// "**" never reaches the screen.
func TestSupportRendersMarkdownNotMarkers(t *testing.T) {
	answer := "**Key characteristics:**\n\n- **Platform independence**: runs anywhere\n- **Object-oriented**: everything is an object\n"
	s := openCompletedSupport(t, answer)
	out := stripANSI(Render(s))
	if strings.Contains(out, "**") {
		t.Error("support response shows raw ** markers instead of rendered emphasis")
	}
	if !strings.Contains(out, "Key characteristics") {
		t.Error("support response lost its heading text while rendering markdown")
	}
	if !strings.Contains(out, "•") {
		t.Error("support response list did not render bullet markers")
	}
}

// TestSupportKeepsIdentityVisibleWithLongAnswer proves the pinned-block
// contract: a long answer scrolls inside its window while the SUP model
// identity stays on screen, instead of being truncated away.
func TestSupportKeepsIdentityVisibleWithLongAnswer(t *testing.T) {
	s := openCompletedSupport(t, longSupportAnswer())
	out := stripANSI(Render(s))
	if !strings.Contains(out, "openrouter/cohere/north-mini-code:free") {
		t.Error("SUP model identity scrolled out of the dialog on a long answer")
	}
	if !strings.Contains(out, "done") {
		t.Error("support completion status is not visible with a long answer")
	}
}

// TestSupportOrganizesContentBlocks proves the redesigned layout names its
// sections: query, answer, and the footer hints stay distinguishable.
func TestSupportOrganizesContentBlocks(t *testing.T) {
	s := openCompletedSupport(t, "plain answer")
	out := stripANSI(Render(s))
	for _, want := range []string{"SUPPORT QUERY", "QUERY", "ANSWER", "what is java?"} {
		if !strings.Contains(out, want) {
			t.Errorf("support dialog is missing block %q", want)
		}
	}
	if !strings.Contains(out, "PgUp/PgDn") {
		t.Error("support footer does not advertise its scroll keys")
	}
}

// TestSupportScrollWindowMoves proves PgUp/PgDn drive the response viewport:
// paging up hides the tail and names what is hidden below, and returning to
// the bottom restores the tail.
func TestSupportScrollWindowMoves(t *testing.T) {
	s := openCompletedSupport(t, longSupportAnswer())
	atBottom := stripANSI(Render(s))
	if !strings.Contains(atBottom, "↑") {
		t.Fatal("long answer shows no scroll-up marker at the live edge")
	}
	if strings.Contains(atBottom, "↓") {
		t.Error("live edge claims hidden lines below it")
	}
	s.Apply(SupportScrolled{Delta: 1000})
	scrolled := stripANSI(Render(s))
	if !strings.Contains(scrolled, "↓") {
		t.Fatal("scrolled-up answer names nothing hidden below it")
	}
	if scrolled == atBottom {
		t.Fatal("paging up did not move the support response viewport")
	}
	// The pinned identity survives scrolling in both directions, and no
	// window ever shows raw markdown markers.
	for _, view := range []string{atBottom, scrolled} {
		if !strings.Contains(view, "openrouter/cohere/north-mini-code:free") {
			t.Fatal("SUP identity left the dialog while scrolling")
		}
		if strings.Contains(view, "**") {
			t.Fatal("scrolled support window shows raw ** markers")
		}
	}
	s.Apply(SupportScrollToBottom{})
	if back := stripANSI(Render(s)); back != atBottom {
		t.Error("scroll-to-bottom did not restore the live edge")
	}
}

// TestSupportScrollKeysWork proves the keyboard contract: PgUp/PgDn move the
// response viewport without touching the main transcript scroll.
func TestSupportScrollKeysWork(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Width, p.state.Height = 100, 30
	p.state.Apply(SupportOpened{Model: "m"})
	p.state.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	p.state.Apply(SupportDone{Seq: 1, Text: longSupportAnswer()})
	keyPress(p, tea.KeyMsg{Type: tea.KeyPgUp})
	if p.state.SupportPanel.Scroll <= 0 {
		t.Fatal("PgUp did not scroll the support response")
	}
	if p.state.Scroll != 0 {
		t.Error("support scrolling leaked into the main transcript viewport")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyPgDown})
	if p.state.SupportPanel.Scroll != 0 {
		t.Error("PgDn did not return the support response to the live edge")
	}
}

// TestSupportScrollsWhileRunning proves a streaming answer stays
// inspectable mid-turn: scroll keys work while running and Enter cannot
// restart the turn underneath.
func TestSupportScrollsWhileRunning(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Width, p.state.Height = 100, 30
	p.state.Apply(SupportOpened{Model: "m"})
	p.state.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	p.state.Apply(SupportDelta{Seq: 1, Text: longSupportAnswer()})
	keyPress(p, tea.KeyMsg{Type: tea.KeyPgUp})
	if p.state.SupportPanel.Scroll <= 0 {
		t.Fatal("PgUp did not scroll a running support turn")
	}
	if !p.state.SupportBusy() {
		t.Fatal("scrolling disturbed the running support turn")
	}
}

// TestSupportWheelScrollsResponse proves wheel ticks scroll the overlay
// response instead of the background transcript while support is open.
func TestSupportWheelScrollsResponse(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Width, p.state.Height = 100, 30
	p.state.Apply(SupportOpened{Model: "m"})
	p.state.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	p.state.Apply(SupportDone{Seq: 1, Text: longSupportAnswer()})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if p.state.SupportPanel.Scroll <= 0 {
		t.Fatal("wheel-up did not scroll the support response")
	}
	if p.state.Scroll != 0 {
		t.Error("support wheel scrolled the background transcript")
	}
}

// TestSupportHighlightsKeyInformation proves status and failure reasons are
// visible as words: completion, failure, and the error that caused it.
func TestSupportHighlightsKeyInformation(t *testing.T) {
	s := openCompletedSupport(t, "answer")
	if out := stripANSI(Render(s)); !strings.Contains(out, "done") {
		t.Error("completed support does not name its done state")
	}
	failed := New()
	failed.Width, failed.Height = 100, 30
	failed.Apply(SupportOpened{Model: "m"})
	failed.Apply(SupportStarted{Seq: 1, Request: "q", Model: "m"})
	failed.Apply(SupportFailedEvent{Seq: 1, Error: "boom"})
	out := stripANSI(Render(failed))
	if !strings.Contains(out, "failed") || !strings.Contains(out, "boom") {
		t.Error("failed support does not highlight its reason")
	}
}
