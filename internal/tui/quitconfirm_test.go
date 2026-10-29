package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// /q must open a confirmation instead of exiting: a stray command must not
// discard the session.
func TestQOpensConfirmWithoutQuitting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	if cmd := p.runCommand(Command{Name: "q"}); cmd != nil {
		t.Error("/q must not return tea.Quit: confirmation owns the exit")
	}
	if p.state.Quit {
		t.Error("/q set the quit flag without confirmation")
	}
	if p.state.QuitConfirm == nil {
		t.Fatal("no quit confirmation opened by /q")
	}
	if got := p.state.QuitConfirm.Selected(); got != "No" {
		t.Errorf("default = %q, want No so Enter alone stays", got)
	}
}

// Typing /q and pressing Enter must take the same confirmation path as
// dispatching it directly: the composer line is a command, not a prompt.
func TestQTypedEnterOpensConfirm(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "/q", Cursor: 2})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Quit {
		t.Error("typed /q quit without confirmation")
	}
	if p.state.QuitConfirm == nil {
		t.Error("typed /q did not open the quit confirmation")
	}
	if p.state.Input != "" {
		t.Errorf("input = %q, want it cleared after dispatch", p.state.Input)
	}
}

// Enter on the resting choice must stay: only a deliberate move onto Yes
// may exit.
func TestQEnterOnDefaultStays(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "q"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Quit {
		t.Error("Enter on No quit the session: the default must be safe")
	}
	if p.state.QuitConfirm != nil {
		t.Error("the dialog stayed open after answering No")
	}
}

// Arrow keys must move off the default onto Yes, and Enter there must quit
// through the same path as /quit (flag plus tea.Quit).
func TestQArrowToYesThenEnterQuits(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "q"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyLeft})
	if got := p.state.QuitConfirm.Selected(); got != "Yes" {
		t.Fatalf("after left, selected = %q, want Yes", got)
	}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("confirming Yes must return tea.Quit so alt-screen restores")
	}
	if !p.state.Quit {
		t.Error("confirming Yes did not request quit")
	}
	if p.state.QuitConfirm != nil {
		t.Error("the dialog outlives the session after confirming quit")
	}
}

// Up must also reach Yes: the affirmative is one deliberate move away.
func TestQUpReachesYes(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "q"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if got := p.state.QuitConfirm.Selected(); got != "Yes" {
		t.Errorf("after up, selected = %q, want Yes", got)
	}
}

// Forward navigation must not wrap off the safe default: Tab/down/right on
// No must stay on No, matching the tool approval dialog.
func TestQForwardNavigationStaysOnNo(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyDown}, {Type: tea.KeyRight}, {Type: tea.KeyTab},
		{Type: tea.KeyRunes, Runes: []rune("j")}, {Type: tea.KeyRunes, Runes: []rune("l")},
	} {
		p := NewProgram(nil, "test", 0)
		Command{Name: "q"}.runOn(p)
		keyPress(p, k)
		if got := p.state.QuitConfirm.Selected(); got != "No" {
			t.Errorf("key %q left No for %q: forward moves must not wrap", k.String(), got)
		}
		keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	}
}

// y quits, n and esc stay: the single-letter shortcuts match the approval
// dialog.
func TestQSingleLetterShortcuts(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "q"}.runOn(p)
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil || !p.state.Quit {
		t.Error("y must quit without arrows")
	}

	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("n")},
		{Type: tea.KeyRunes, Runes: []rune("N")},
		{Type: tea.KeyEsc},
	} {
		q := NewProgram(nil, "test", 0)
		Command{Name: "q"}.runOn(q)
		keyPress(q, k)
		if q.state.Quit {
			t.Errorf("key %q quit: n/esc must stay", k.String())
		}
		if q.state.QuitConfirm != nil {
			t.Errorf("key %q left the dialog open", k.String())
		}
	}
}

// ctrl+c must never quit from the dialog: it dismisses, cancelling a running
// turn the way esc would.
func TestQCtrlCNeverQuits(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "q"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlC})
	if p.state.Quit {
		t.Error("ctrl+c quit from the confirmation: the abort key must stay safe")
	}
	if p.state.QuitConfirm != nil {
		t.Error("ctrl+c left the dialog open")
	}

	busy := NewProgram(nil, "test", 0)
	busy.state.Apply(UserSubmitted{Text: "work"})
	Command{Name: "q"}.runOn(busy)
	keyPress(busy, tea.KeyMsg{Type: tea.KeyCtrlC})
	if busy.state.Quit {
		t.Error("busy ctrl+c quit from the confirmation")
	}
	if busy.state.QuitConfirm != nil {
		t.Error("busy ctrl+c left the dialog open")
	}
	if !busy.state.Cancelled {
		t.Error("busy ctrl+c did not cancel the turn")
	}
}

// The dialog must read as a centered bordered quit question with horizontal
// buttons and the left/right keys it owns.
func TestQRenderShowsQuitQuestion(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "q"}.runOn(p)
	out := ansi.Strip(Render(p.state))
	if !strings.Contains(out, "Really quit?") {
		t.Errorf("no quit question in:\n%s", out)
	}
	if !strings.Contains(out, "Yes") || !strings.Contains(out, "No") {
		t.Errorf("choices missing in:\n%s", out)
	}
	// Centered popup: a rounded border around the card.
	if !strings.Contains(out, "╭") && !strings.Contains(out, "┌") {
		t.Errorf("no popup border in:\n%s", out)
	}
	if !strings.Contains(out, "│") && !strings.Contains(out, "─") {
		t.Errorf("no popup frame in:\n%s", out)
	}
	// Left/right selection: arrows around the highlight plus the hint.
	if !strings.Contains(out, "◀") || !strings.Contains(out, "▶") {
		t.Errorf("no selection arrows in:\n%s", out)
	}
	if !strings.Contains(out, "←") || !strings.Contains(out, "→") {
		t.Errorf("no arrow hint in:\n%s", out)
	}
}

// While the confirmation is open the arrows belong to it, not the
// transcript.
func TestQOwnsArrowKeys(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(InputChanged{Text: ""})
	if !wheelScrolls(p.state) {
		t.Fatal("idle composer must give arrows to the transcript")
	}
	Command{Name: "q"}.runOn(p)
	if wheelScrolls(p.state) {
		t.Error("open quit confirmation must own the arrow keys")
	}
}

// Confirming quit must deny a blocked tool first, like /quit does: no tool
// may wait on a UI that is gone.
func TestQConfirmDeniesBlockedTool(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	answer := make(chan approvalAnswer, 1)
	p.Update(approvalPrompt{request: approvalRequest{
		toolName: "bash", summary: "rm -rf /", answer: answer,
	}})
	if p.state.Pending == nil {
		t.Fatal("no tool approval pending")
	}
	Command{Name: "q"}.runOn(p)
	if p.state.QuitConfirm == nil {
		t.Fatal("no quit confirmation while a tool waits")
	}
	// Move onto Yes and confirm.
	keyPress(p, tea.KeyMsg{Type: tea.KeyLeft})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !p.state.Quit {
		t.Fatal("confirming Yes did not quit")
	}
	select {
	case reply := <-answer:
		if reply.approved {
			t.Error("quit approved a blocked tool: shutdown must deny")
		}
	default:
		t.Error("blocked tool got no answer after confirmed quit")
	}
}

// Dismissing must leave a blocked tool waiting: only confirming quits.
func TestQDismissKeepsBlockedTool(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	answer := make(chan approvalAnswer, 1)
	p.Update(approvalPrompt{request: approvalRequest{
		toolName: "bash", summary: "x", answer: answer,
	}})
	Command{Name: "q"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Quit {
		t.Error("dismissing quit the session")
	}
	if p.state.Pending == nil {
		t.Error("dismissing dropped the tool approval")
	}
	select {
	case <-answer:
		t.Error("dismissing answered the blocked tool")
	default:
	}
	// Release the tool so its goroutine can finish.
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
}

// The registry must resolve /q case-insensitively and keep it hidden like
// /quit and /exit.
func TestQLookupAndHidden(t *testing.T) {
	if _, ok := LookupCommand("q"); !ok {
		t.Error("LookupCommand(q) missing")
	}
	if _, ok := LookupCommand("Q"); !ok {
		t.Error("LookupCommand is not case-insensitive for q")
	}
	if strings.Contains(HelpText(), "/q") {
		t.Error("hidden alias /q leaked into help")
	}
	cmd, ok := ParseCommand("/Q")
	if !ok || cmd.Name != "q" {
		t.Errorf("ParseCommand(/Q) = (%+v,%v), want q", cmd, ok)
	}
}

// An open confirmation must keep the frame exact: the dialog claims its rows
// inside the conversation budget so chrome stays pinned.
func TestQFrameFillsViewport(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {60, 20}} {
		w, h := size[0], size[1]
		p := NewProgram(nil, "test", 0)
		p.state.Apply(TerminalResized{Width: w, Height: h})
		p.state.Apply(UserSubmitted{Text: "go"})
		p.state.Apply(AssistantDone{})
		Command{Name: "q"}.runOn(p)
		if got := len(strings.Split(Render(p.state), "\n")); got != h {
			t.Errorf("%dx%d with quit confirm rendered %d lines, want %d", w, h, got, h)
		}
	}
}
