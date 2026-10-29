package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Interaction and rendering integrity (audit slice). Each test pins a
// reported real-terminal symptom to its responsible seam.

// A typo in the configured mode must not silently take away drag-copy, which
// is the capability a user is far likelier to notice going missing than a
// dead wheel. Only an explicit "report" opts into reporting.
func TestNormalizeMouseKeepsSelectionOnAnythingUnrecognised(t *testing.T) {
	for _, name := range []string{"report", "REPORT", " Report "} {
		if got := NormalizeMouse(name); got != MouseReport {
			t.Errorf("NormalizeMouse(%q) = %q, want %q", name, got, MouseReport)
		}
	}
	for _, name := range []string{"", "select", "repot", "yes", "true", "1"} {
		if got := NormalizeMouse(name); got != MouseSelect {
			t.Errorf("NormalizeMouse(%q) = %q, want %q — an unrecognised mode must keep drag-copy",
				name, got, MouseSelect)
		}
	}
}

// The default has to follow the environment, because the wheel is reachable
// by different routes in different ones. Measured against tmux 3.7c: a pane
// that enables alternate scroll alone receives NOTHING on the wheel — not with
// `mouse off`, not in normal screen, not with the mode emitted before or after
// alt-screen. Only a pane that requests mouse reporting is forwarded to.
//
// So a multiplexer in the path means reporting is the only mode that works,
// and defaulting to select there would make the wheel dead exactly where a
// user is most likely to have it.
func TestMouseDefaultFollowsTheEnvironment(t *testing.T) {
	if got := MouseDefaultFor(""); got != MouseSelect {
		t.Errorf("outside a multiplexer the default must keep drag-copy, got %q", got)
	}
	// tmux exports TMUX as its socket path, comma-delimited, for every pane.
	if got := MouseDefaultFor("/tmp/tmux-1000/default,1234,0"); got != MouseReport {
		t.Errorf("inside tmux the wheel needs mouse reporting, got %q", got)
	}
}

// An explicit choice always beats the environment default: a user who turns
// reporting off inside tmux must not have it silently re-enabled.
func TestExplicitMouseModeBeatsTheEnvironment(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	if p.mouseReporting {
		t.Error("WithMouseReporting must select reporting")
	}
	if NewProgram(nil, "test", 0).mouseReporting {
		t.Error("the constructor must not decide from the environment on its own")
	}
}

// ---- wheel scrolling without losing text selection ----

// The wheel and drag-select cannot both come from the terminal on its own:
// asking for mouse reporting makes it stop doing its own selection, and not
// asking means a multiplexer may swallow the wheel before the pane sees it.
// So the mode is a choice, and the default must keep selection working.
func TestMouseReportingIsOffByDefaultAndOptIn(t *testing.T) {
	plain := NewProgram(nil, "test", 0)
	if plain.mouseReporting {
		t.Error("mouse reporting must be off by default; drag-select and copy depend on it")
	}

	opted := NewProgram(nil, "test", 0, WithMouseReporting())
	if !opted.mouseReporting {
		t.Error("WithMouseReporting did not select the reporting mode")
	}
}

// Alternate scroll mode is what carries the wheel when reporting is off. It is
// emitted either way: it costs nothing, and outside a multiplexer it is the
// only path a wheel has.
func TestAlternateScrollIsEmittedInBothModes(t *testing.T) {
	for _, opts := range [][]ProgramOption{nil, {WithMouseReporting()}} {
		var out strings.Builder
		p := NewProgram(nil, "test", 0, opts...)
		p.termOut = &out
		p.enterAlternateScroll()()
		if got := out.String(); got != "\x1b[?1007h\x1b[?1007l" {
			t.Errorf("alternate scroll not bracketed: got %q", got)
		}
	}
}

// The wheel has to work, and drag-select has to keep working. They conflict
// whenever the program asks for mouse reporting: once the terminal is sending
// mouse events it stops doing its own selection, and the program has to parse a
// byte stream that can split across reads. Alternate scroll mode avoids both by
// leaving mouse reporting off and having the terminal translate wheel ticks
// into plain arrow keys.
//
// The contract: Run brackets the session with the mode, and always turns it
// back off, so a crashed or exited UI cannot leave the terminal rewriting
// scrollwheel input into arrows.
func TestRunBracketsAlternateScrollMode(t *testing.T) {
	var out strings.Builder
	p := NewProgram(nil, "test", 0)
	p.termOut = &out

	// Run blocks on a terminal, so drive the same bracket the session uses
	// rather than starting Bubble Tea.
	p.enterAlternateScroll()()
	p.enterAlternateScroll()()

	if got := out.String(); got != "\x1b[?1007h\x1b[?1007l\x1b[?1007h\x1b[?1007l" {
		t.Errorf("alternate scroll was not bracketed by the session: got %q, want %q",
			got, "\x1b[?1007h\x1b[?1007l\x1b[?1007h\x1b[?1007l")
	}
}

// A wheel tick arrives as a plain arrow, and the terminal cannot say which
// arrows it synthesised. So the arrows scroll the transcript whenever the
// composer has nothing to move a cursor within — the state a reader is in.
func TestWheelArrowsScrollTheTranscriptWhenTheComposerIsIdle(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	for i := 0; i < 40; i++ {
		p.state.Apply(AssistantDelta{Text: "a fairly long line of transcript content that will wrap when rendered\n"})
	}
	p.state.Apply(AssistantDone{})
	p.state.Scroll, p.state.Follow = 0, true

	p.handleKey(navKey("up"))
	p.handleKey(navKey("up"))
	if p.state.Scroll == 0 {
		t.Error("wheel-up on an idle composer did not scroll the transcript")
	}
	if p.state.Follow {
		t.Error("scrolling up must clear follow, or the view snaps back to the live edge")
	}

	before := p.state.Scroll
	p.handleKey(navKey("down"))
	if p.state.Scroll >= before {
		t.Errorf("wheel-down did not scroll back toward the live edge: %d -> %d", before, p.state.Scroll)
	}
}

// Typing must never be disturbed by the wheel. With text in the composer the
// arrows keep moving the cursor, and history recall keeps its ctrl+p / ctrl+n
// bindings, so nothing the user relies on is taken away to make the wheel work.
func TestArrowsKeepEditingAndHistoryWhileTyping(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(InputChanged{Text: "first line\nsecond line"})
	// Park inside the second line, so there is a line above to move to.
	p.state.Cursor = len([]rune("first line\nsec"))

	p.handleKey(navKey("up"))
	if p.state.Cursor >= len([]rune("first line\nsec")) {
		t.Errorf("up did not move to the line above: cursor = %d; "+
			"the wheel policy took the arrows from the composer", p.state.Cursor)
	}

	// History recall stays reachable on the ctrl bindings, which never scroll.
	p.state.Apply(InputChanged{Text: ""})
	p.state.Submitted = []string{"earlier prompt"}
	p.state.HistCursor = -1
	p.handleKey(navKey("ctrl+p"))
	if p.state.Input != "earlier prompt" {
		t.Errorf("ctrl+p did not recall history: input = %q", p.state.Input)
	}
}

// An overlay owning the keyboard keeps the arrows; only the transcript region
// gives them up. Without this a picker would scroll the conversation out from
// under itself while the user arrows through the list.
func TestWheelArrowsDoNotStealKeysFromAnOpenOverlay(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(InputChanged{Text: ""})

	p.state.Picker = &PickerState{Kind: PickerModels}
	if wheelScrolls(p.state) {
		t.Error("an open picker owns the arrow keys; the transcript must not take them")
	}
	p.state.Picker = nil

	p.state.Pending = &PendingApproval{Choices: approvalChoices}
	if wheelScrolls(p.state) {
		t.Error("an open approval owns the arrow keys; the transcript must not take them")
	}
	p.state.Pending = nil

	if !wheelScrolls(p.state) {
		t.Error("an idle empty composer must give the arrows to the transcript")
	}
}

// ---- approval: the human decision boundary ----

// askingApproval returns a Program parked on an approval prompt, plus a
// channel carrying the answer the blocked tool actually received. The answer is
// captured in the waiting goroutine because it consumes it itself — reading the
// reply channel from the test would race it.
func askingApproval(t *testing.T) (*Program, <-chan bool) {
	t.Helper()
	p := NewProgram(nil, "test", 0)
	got := make(chan bool, 1)
	approve := p.ApprovalFunc()
	go func() {
		ok, _ := approve(ai.ToolCall{ID: "c1", Name: "bash"}, agent.Approval{
			Tier: agent.TierExec, Reason: "runs a shell command",
		})
		got <- ok
	}()
	select {
	case request := <-p.approvals:
		p.pending = &request
		p.state.Apply(ApprovalAsked{
			ToolName: "bash", Reason: "runs a shell command",
			Summary: "ls", Choices: approvalChoices,
		})
		return p, got
	case <-time.After(2 * time.Second):
		t.Fatal("the approval prompt never reached the UI")
		return nil, nil
	}
}

func navKey(name string) tea.KeyMsg {
	switch name {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	case "ctrl+n":
		return tea.KeyMsg{Type: tea.KeyCtrlN}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
	}
}

// Navigation stops at the ends instead of wrapping.
//
// Wrapping made every navigation key an escape from the conservative default:
// the cursor opens on the last choice, and with two choices a forward move
// resolved to index 0, so Tab — the conventional "move focus away" key — landed
// on "Yes" and Enter then ran the tool. Because the default is already the last
// choice, clamping means forward navigation cannot leave it at all.
//
// Residual, not covered here: a *backward* move reaches "Yes" in a two-option
// dialog, because the affirmative is adjacent to the default. That is a
// deliberate press onto the affirmative option rather than a wrap-around, but
// Enter then confirms it, so it is tracked in UI_DECISIONS.md as still open.
func TestApprovalNavigationStopsAtTheEndInsteadOfWrapping(t *testing.T) {
	for _, key := range []string{"down", "right", "tab", "j", "l"} {
		t.Run(key, func(t *testing.T) {
			p, answers := askingApproval(t)
			defer deny(t, p, answers)

			if got := p.state.Pending.Selected(); got != "No" {
				t.Fatalf("the approval must open on the conservative choice, got %q", got)
			}
			p.handleKey(navKey(key))
			if got := p.state.Pending.Selected(); got != "No" {
				t.Errorf("key %q left the conservative default for %q; "+
					"forward navigation must not wrap onto the affirmative choice", key, got)
			}
		})
	}
}

// The cursor must never leave the choice list, for any movement, including
// repeated movement that would previously have wrapped several times around.
func TestApprovalNavigationStaysWithinTheChoiceList(t *testing.T) {
	p, answers := askingApproval(t)
	defer deny(t, p, answers)

	count := len(p.state.Pending.Choices)
	for _, key := range []string{"up", "down", "left", "right", "tab", "shift+tab", "j", "k"} {
		p.handleKey(navKey(key))
		p.handleKey(navKey(key))
		p.handleKey(navKey(key))
		c := p.state.Pending.Cursor
		if c < 0 || c >= count {
			t.Fatalf("key %q left the choice list: cursor=%d, choices=%d", key, c, count)
		}
		if got := p.state.Pending.Selected(); got != p.state.Pending.Choices[c] {
			t.Fatalf("key %q: selection %q disagrees with cursor %d", key, got, c)
		}
	}
}

// Ctrl+C is the reflex abort key. With a tool blocked on an answer it used to
// be swallowed by the approval switch, so the turn stayed parked and the user
// had to discover esc or n instead.
func TestCtrlCDuringApprovalDeniesAndCancels(t *testing.T) {
	p, answers := askingApproval(t)

	p.handleKey(navKey("ctrl+c"))

	select {
	case approved := <-answers:
		if approved {
			t.Error("ctrl+c approved a blocked tool; it must deny")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctrl+c left the blocked tool unanswered")
	}
	if p.state.Pending != nil {
		t.Error("the approval prompt is still open after ctrl+c")
	}
}

// The composer hint row sat under the dialog and advertised keys the dialog had
// already taken: "esc cancel" denied the tool instead of cancelling the turn,
// and "enter queue" confirmed whatever was highlighted. Two contradictory
// instruction rows on screen is how a user denies work they meant to approve.
// The dialog prints its own key line, so the composer row has nothing left to
// teach while a prompt is open.
func TestComposerHintsNeverAdvertiseKeysTheApprovalHasCaptured(t *testing.T) {
	p, answers := askingApproval(t)
	defer deny(t, p, answers)
	p.state.Width, p.state.Height = 120, 40
	p.state.Status.Busy = true

	frame := ansi.Strip(Render(p.state))
	for _, captured := range []string{"esc cancel", "alt+enter", "pgup", "pgdn", "enter queue", "navigate"} {
		if strings.Contains(frame, captured) {
			t.Errorf("the frame advertises %q while the open approval captures it:\n%s", captured, frame)
		}
	}
	if !strings.Contains(frame, "Do you want to proceed?") {
		t.Error("the approval dialog is missing from the frame")
	}
}

// deny releases the blocked tool so its goroutine can finish, rather than
// leaving every test to wait out a timeout.
func deny(t *testing.T, p *Program, answers <-chan bool) {
	t.Helper()
	p.handleKey(navKey("n"))
	select {
	case approved := <-answers:
		if approved {
			t.Error("cleanup denied the prompt with an approval")
		}
	case <-time.After(2 * time.Second):
		t.Error("the blocked tool never received the answer")
	}
}

// Scrolling past the top of a long transcript must not collapse the frame:
// the transcript area always emits exactly its budget and the composer stays
// pinned to the bottom rows. Before the clamp, an overshooting offset
// emitted a 7-row frame on a 24-row terminal with the composer floating
// mid-screen.
func TestScrollOvershootKeepsComposerPinned(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	for i := 0; i < 10; i++ {
		s.Apply(UserSubmitted{Text: "question " + strings.Repeat("x", 20)})
		s.Apply(AssistantDelta{Text: "answer body " + strings.Repeat("y", 100)})
		s.Apply(AssistantDone{})
	}
	s.Apply(ScrollMoved{Delta: 10000})

	rows := strings.Split(strings.TrimRight(Render(s), "\n"), "\n")
	if len(rows) != 24 {
		t.Fatalf("frame = %d rows, want the full 24-row viewport", len(rows))
	}
	// Bottom block is composer rule, composer, composer rule, hint bar: the
	// telemetry footer stays pinned last so the prompt never drifts.
	if bottom := ansi.Strip(rows[22]); strings.Trim(bottom, "─") != "" {
		t.Errorf("row 22 = %q, want the composer rule above the pinned footer", bottom)
	}
	if bottom := ansi.Strip(rows[23]); !strings.Contains(bottom, "? help") {
		t.Errorf("bottom row = %q, want the hint bar pinned to the viewport base", bottom)
	}
	var marker int
	found := false
	for _, row := range rows {
		plain := ansi.Strip(row)
		if strings.HasPrefix(plain, "↑ ") && strings.Contains(plain, "lines above") {
			found = true
			if _, err := fmt.Sscanf(plain, "↑ %d lines above", &marker); err != nil {
				t.Fatalf("marker unreadable: %q", plain)
			}
		}
	}
	if !found {
		t.Fatal("no scrolled-up marker while offset past the top")
	}
	if marker <= 0 || marker >= 10000 {
		t.Errorf("marker counts %d lines above, want the truthful remainder, not the raw offset", marker)
	}
	if !strings.Contains(Render(s), "> ") {
		t.Error("composer prompt missing from the pinned frame")
	}
}

// Scrolling exactly to the top shows the first content row under the marker,
// not an empty region.
func TestScrollAtTopShowsFirstContent(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "first question"})
	s.Apply(AssistantDelta{Text: strings.Repeat("body text here. ", 60)})
	s.Apply(AssistantDone{})
	s.Apply(ScrollMoved{Delta: 10000})

	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "first question") {
		t.Errorf("top of transcript not visible at max scroll:\n%s", out)
	}
}

// /clear rejoins the live edge: a stale offset must not pin the next turn's
// output to the top of the fresh transcript.
func TestClearedRejoinsLiveEdge(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "old"})
	s.Apply(AssistantDelta{Text: strings.Repeat("old body. ", 60)})
	s.Apply(AssistantDone{})
	s.Apply(ScrollMoved{Delta: 10000})
	s.Apply(Cleared{})

	if s.Scroll != 0 || !s.Follow {
		t.Fatalf("Scroll = %d Follow = %v after clear, want the live edge back", s.Scroll, s.Follow)
	}
	s.Apply(UserSubmitted{Text: "new question"})
	s.Apply(AssistantDelta{Text: "new answer"})
	out := ansi.Strip(Render(s))
	if strings.Contains(out, "lines above") {
		t.Errorf("stale scroll marker over fresh content:\n%s", out)
	}
	if !strings.Contains(out, "new answer") {
		t.Errorf("fresh answer not at the live edge:\n%s", out)
	}
}

// A regex failure is a tool outcome, not a run failure: it renders as an
// error card the model can retry, and it never aborts the session or leaks
// outside the tool's own rows.
func TestRegexToolErrorRendersAsErrorCard(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ToolStarted{ID: "call_1", Name: "grep", At: time.Now()})
	s.Apply(ToolFinished{
		ID:      "call_1",
		Text:    "invalid regular expression \"[unclosed\": error parsing regexp: missing closing ]: `[`",
		IsError: true,
		At:      time.Now(),
	})

	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "✗ grep") {
		t.Errorf("no error card for the failed grep:\n%s", out)
	}
	if !strings.Contains(out, "invalid regular expression") {
		t.Errorf("tool outcome text lost:\n%s", out)
	}
	if s.Quit {
		t.Error("a tool outcome requested quit: outcomes must not end the session")
	}
	if s.Pending != nil {
		t.Error("a tool outcome opened an approval prompt")
	}
}

// Fenced code is code, not Markdown: delimiters the model wrote inside the// fence stay literal, emphasis styling never crosses the fence boundary,
// and highlighting only changes presentation. The ** markers below are
// model-authored source bytes; stripping them would destroy code content.
func TestFencedCodeKeepsDelimitersLiteral(t *testing.T) {
	pal := forcedPalette()
	src := "**A Simple Class Example**\n\n```java\n*// Define a class*\n\n**class** Person {\n\n    String name;\n\n    **int** age;\n\n    Person(String name, **int** age) {\n```"
	out := RenderMarkdownWrapped(src, pal, 80)
	stripped := ansi.Strip(out)

	for _, literal := range []string{"**class** Person {", "**int** age;", "Person(String name, **int** age) {"} {
		if !strings.Contains(stripped, literal) {
			t.Errorf("code content altered: missing %q in:\n%s", literal, stripped)
		}
	}
	if probe := pal.Bold.Render("class"); strings.Contains(out, probe) {
		t.Errorf("Markdown emphasis leaked into fenced code: %q", probe)
	}
	if probe := pal.CodeKeyword.Render("class"); !strings.Contains(out, probe) {
		t.Errorf("keyword highlighting missing inside the fence; want presentation-only styling")
	}
	// Prose emphasis outside the fence still renders as emphasis (the word
	// painter emits per-word runs, so probe one word, not the whole line).
	if probe := pal.Bold.Render("Example"); !strings.Contains(out, probe) {
		t.Errorf("prose emphasis lost outside the fence")
	}
}

// KeyRunes is user text, full stop. Strings that resemble SGR mouse reports
// must insert verbatim: mouse reporting is off (see Program.Run), so the
// terminal never emits protocol bytes, and any "[<Cb;Cx;CyM" shape arriving
// here is prose the user typed or pasted — documentation, code, a copied
// terminal capture. Dropping or rewriting it would destroy legitimate input
// while leaving the protocol path unfixed. Each shape lands exactly, with
// the cursor parked after it.
func TestComposerPreservesMouseLikeText(t *testing.T) {
	for _, text := range []string{
		"[<64;67;30M",
		"[<0;10;20m",
		"x[<65;58;30M]y",
		"[<65",
		"[",
		"see ESC[<64;67;30M in the log",
	} {
		p := NewProgram(nil, "test", 0)
		keyPress(p, runes(text))
		if p.state.Input != text || p.state.Cursor != len([]rune(text)) {
			t.Errorf("typed %q: input %q cursor %d, want verbatim insert",
				text, p.state.Input, p.state.Cursor)
		}
	}
}

// Mouse messages never author prompt text. Press, release, and motion are
// no-ops by contract; wheel ticks scroll the transcript without touching
// the composer.
func TestMouseMessagesNeverMutatePrompt(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("draft"))
	before, cursor := p.state.Input, p.state.Cursor
	for _, m := range []tea.MouseMsg{
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
		{Button: tea.MouseButtonNone, Action: tea.MouseActionRelease},
		{Button: tea.MouseButtonNone, Action: tea.MouseActionMotion},
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
	} {
		p.Update(m)
		if p.state.Input != before || p.state.Cursor != cursor {
			t.Errorf("mouse %v mutated the prompt: input %q cursor %d",
				tea.MouseEvent(m).String(), p.state.Input, p.state.Cursor)
		}
	}
}

// Wheel ticks scroll the region in focus and never type. Without a picker
// the transcript offset moves; with one open the selection moves instead.
func TestMouseWheelScrollsWithoutTyping(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("draft"))
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if p.state.Input != "draft" {
		t.Errorf("wheel typed into the composer: input %q", p.state.Input)
	}
	if p.state.Scroll != mouseScrollLines || p.state.Follow {
		t.Errorf("wheel up: Scroll %d Follow %v, want %d and false",
			p.state.Scroll, p.state.Follow, mouseScrollLines)
	}
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if p.state.Scroll != 0 || !p.state.Follow {
		t.Errorf("wheel down: Scroll %d Follow %v, want the live edge back",
			p.state.Scroll, p.state.Follow)
	}
	if p.state.Input != "draft" {
		t.Errorf("wheel typed into the composer: input %q", p.state.Input)
	}

	Command{Name: "commands"}.runOn(p)
	if p.state.HelpModal == nil {
		t.Fatal("no help center opened")
	}
	// Dispatch clears the composer; the wheel must still type nothing.
	if p.state.Input != "" {
		t.Fatalf("command dispatch left input %q, want cleared", p.state.Input)
	}
	at := p.state.HelpModal.Command.Cursor
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if p.state.HelpModal.Command.Cursor == at && len(p.state.HelpModal.ActiveItems()) > 1 {
		t.Error("wheel down over the help center left the selection still")
	}
	if p.state.Input != "" {
		t.Errorf("wheel over the help center typed into the composer: input %q", p.state.Input)
	}
}

// Credential prompts and picker queries collect keystrokes through the same
// insertion path, so mouse-shaped text is preserved there too: a pasted
// secret containing "[<" must not be silently rewritten, and a picker
// search for protocol documentation must match literally.
func TestConnectKeyAndPickerPreserveMouseLikeText(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ConnectKeyOpened{View: ConnectKeyView{Provider: ProviderRow{ID: "openrouter"}}})
	keyPress(p, runes("[<64;67;30M"))
	if p.state.ConnectKey == nil || p.state.ConnectKey.Key != "[<64;67;30M" {
		t.Errorf("key modal rewrote pasted text: %+v", p.state.ConnectKey)
	}
	p.state.Apply(ConnectKeyClosed{})
	p.flow = nil

	Command{Name: "commands"}.runOn(p)
	if p.state.HelpModal == nil {
		t.Fatal("no help center opened")
	}
	p.Update(runes("[<64;67;30M"))
	if p.state.HelpModal.Command.Filter != "[<64;67;30M" {
		t.Errorf("help query rewrote typed text: %q", p.state.HelpModal.Command.Filter)
	}
}
