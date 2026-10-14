package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Output-first interaction contract.
//
// These tests defend the boundary the mode is built on, not its looks: the
// terminal owns scrolling, selection and copy, so ohgo must never ask for a
// mouse mode, must never rewrite a line it already wrote, and must not lose or
// duplicate a streamed delta on the way out.

// mouseModeSequences are the private modes that take the wheel, drag selection
// and copy away from the terminal. Any one of them in the output stream means
// the mode has quietly become the thing it replaced.
var mouseModeSequences = []string{
	"\x1b[?1000h", "\x1b[?1000l",
	"\x1b[?1002h", "\x1b[?1002l",
	"\x1b[?1003h", "\x1b[?1003l",
	"\x1b[?1006h", "\x1b[?1006l",
	"\x1b[?1007h", "\x1b[?1007l",
	"\x1b[?1015h", "\x1b[?1015l",
}

// newLiveProgram returns a program in live mode plus the painter driving it.
func newLiveProgram() (*Program, *livePainter) {
	p := NewProgram(nil, "test", 0)
	painter := newLivePainter()
	p.live = painter
	return p, painter
}

// liveEvent applies one state event and folds it into output, which is what
// the running program's Update does for an event off the loop channel.
func liveEvent(p *Program, event Event) []string {
	p.state.Apply(event)
	return p.live.render(p.state)
}

// liveTurn scripts one turn's worth of events: a prompt, a tool call with an
// outcome, an answer, and the closing of the turn.
func liveTurn(p *Program, answer string) []string {
	var history []string
	history = append(history, liveEvent(p, UserSubmitted{Text: "what changed in auth?"})...)
	history = append(history, liveEvent(p, ToolStarted{ID: "c1", Name: "grep", At: time.Now()})...)
	history = append(history, liveEvent(p, ToolFinished{ID: "c1", Text: "auth/login.go:12: session check", At: time.Now()})...)
	history = append(history, liveEvent(p, AssistantDelta{Text: answer})...)
	return append(history, liveEvent(p, AssistantDone{})...)
}

// ---- native interaction ----

// The view the live session hands the renderer must never ask the terminal
// for a mouse mode or switch to the alternate screen. Those two facts are what
// leave the wheel, drag selection and copy with the terminal; a workaround
// that reintroduced either one would fail exactly where the previous
// architecture broke.
func TestLiveViewRequestsNoMouseModeAndNoAltScreen(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	liveTurn(p, "## Answer\n\nThe session check moved to `auth/login.go`.\n")
	request := approvalRequest{toolName: "bash", reason: "runs a command", answer: make(chan approvalAnswer, 1)}
	p.pending = &request
	liveEvent(p, ApprovalAsked{ToolName: "bash", Reason: "runs a command", Choices: approvalChoices})
	p.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	p.pending = nil
	liveEvent(p, ApprovalAnswered{})
	p.state.Apply(PickerOpened{Kind: PickerCommands, Title: "commands", Items: nil})
	liveEvent(p, Notice{Text: "pickers live too"})
	p.state.Apply(SecretOpened{Label: "key"})

	view := painter.view()
	for _, seq := range mouseModeSequences {
		if strings.Contains(view, seq) {
			t.Errorf("the live view requested %q; the terminal must keep the wheel and drag selection", seq)
		}
	}
	for _, seq := range []string{"\x1b[?1049h", "\x1b[?1049l"} {
		if strings.Contains(view, seq) {
			t.Errorf("the live view switched screens with %q, which would leave the terminal no scrollback", seq)
		}
	}
}

// The live region the renderer repaints has to stay bounded. Once it exceeds
// the screen, the renderer drops rows from the top of its frame and the
// transcript would start disappearing.
func TestLiveRegionNeverExceedsTheScreen(t *testing.T) {
	for _, height := range []int{10, 24, 40, 120} {
		p, painter := newLiveProgram()
		p.state.Apply(TerminalResized{Width: 80, Height: height})
		limit := liveRows(p.state)
		for i := 0; i < 40; i++ {
			liveEvent(p, AssistantDelta{Text: "line " + strconv.Itoa(i) + " of a long answer that wraps a lot\n\n"})
			if got := len(painter.live); got > limit {
				t.Fatalf("at height %d the live region grew to %d rows, past the %d-row budget", height, got, limit)
			}
		}
	}
}

// The live region is the only thing the renderer owns. Everything below the
// cut is scrollback, so a resize that shortens the screen must not shrink what
// has already been written — the terminal owns those rows now.
func TestLiveRegionOnlyHoldsLiveRows(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 30})
	liveTurn(p, "the answer\n")

	view := strings.Split(painter.view(), "\n")
	last := strings.TrimSpace(ansi.Strip(view[len(view)-1]))
	if !strings.Contains(last, "? help") {
		t.Errorf("the bottom row is %q, want the telemetry hint row; the composer must own the last row", last)
	}
	if len(view) > liveRows(p.state) {
		t.Errorf("the frame holds %d rows, past the %d-row budget", len(view), liveRows(p.state))
	}
}

// ---- input safety ----

// A paste or a typed line that looks like a mouse report is prose. The mode
// never asks the terminal for mouse input, so any such bytes are text the
// user put there, and they must reach the terminal byte for byte.
func TestLiveComposerKeepsMouseLikeTextLiteral(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	for _, text := range []string{"[<64;67;30M", "see ESC[<0;10;20m in the log", "x[<65;58;30M]y"} {
		liveEvent(p, ComposerChanged{Text: text, Cursor: len([]rune(text))})

		view := ansi.Strip(painter.view())
		if !strings.Contains(view, text) {
			t.Errorf("typed %q: the composer frame is %q, want the text verbatim", text, view)
		}
		if p.state.Input != text {
			t.Errorf("typed %q: composer holds %q", text, p.state.Input)
		}
		for _, seq := range mouseModeSequences {
			if strings.Contains(painter.view(), seq) {
				t.Errorf("painting %q emitted %q", text, seq)
			}
		}
	}
}

// ---- streaming ----

// Streaming must converge: whatever the chunk boundaries were, the sealed
// transcript ends up equal to rendering the finished text once.
//
// This is the property that lets output be incremental at all. If a partial
// fence or a half-written table produced different scrollback than the whole
// answer does, the transcript would depend on when the user read it.
func TestLiveStreamingConvergesOnTheFinishedRender(t *testing.T) {
	answer := "# Title\n\n" +
		"First paragraph with **bold** and `code`, long enough to wrap at least once " +
		"across the available width so folding is involved.\n\n" +
		"```go\nfunc sessionCheck(u User) bool {\n\treturn u.ID != \"\"\n}\n```\n\n" +
		"- one item\n- another item\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\nClosing line.\n"

	var printed []string
	v := newLivePainter()
	s := New()
	s.Apply(TerminalResized{Width: 60, Height: 30})
	s.Status.Model = "p/m"

	want := joinAll(liveMarkdownLines(answer, PaletteFor(s.Theme), ComputeLayout(s)))

	// Chunk the answer into uneven pieces, the way a provider would.
	for i := 0; i < len(answer); {
		n := 37
		if i+n > len(answer) {
			n = len(answer) - i
		}
		s.Apply(AssistantDelta{Text: answer[i : i+n]})
		i += n
		printed = append(printed, v.render(s)...)
	}
	s.Apply(AssistantDone{})
	printed = append(printed, v.render(s)...)

	if got := joinAll(printed); got != want {
		t.Errorf("streamed transcript does not converge on the finished render\ngot:\n%q\nwant:\n%q", got, want)
	}
}

// No delta may be lost and none may be duplicated at any point mid-stream, not
// only at the end. What the terminal holds is the scrollback plus the live
// block; together they must cover the text exactly once.
func TestLiveStreamingKeepsEveryDeltaOnceWhileItRuns(t *testing.T) {
	v := newLivePainter()
	s := New()
	s.Apply(TerminalResized{Width: 70, Height: 28})

	answer := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 60)
	var seen strings.Builder
	var scrollback []string
	for i := 0; i < len(answer); {
		n := 23
		if i+n > len(answer) {
			n = len(answer) - i
		}
		seen.WriteString(answer[i : i+n])
		s.Apply(AssistantDelta{Text: answer[i : i+n]})
		i += n

		scrollback = append(scrollback, v.render(s)...)
		onScreen := joinAll(scrollback) + joinAll(v.block)
		if got := squeeze(onScreen); got != squeeze(seen.String()) {
			t.Fatalf("after %d bytes the terminal holds:\n%q\nwant:\n%q", seen.Len(), got, squeeze(seen.String()))
		}
	}
}

// A long answer cannot grow the live region without bound: it commits a
// prefix to the scrollback. The commit point has to be a paragraph boundary,
// because that is the only place a later wrap cannot have moved.
func TestLiveCommitCutLandsOnAParagraphBoundary(t *testing.T) {
	pal := DarkPalette()
	layout := ComputeLayout(State{Width: 70, Height: 20})
	budget := 6
	para := strings.Repeat("A sentence of answer text that will wrap. ", 4) + "\n\n"
	rest := []rune(strings.Repeat(para, 12) + "A partial last paragraph with no break after it")

	if got := len(liveMarkdownLines(string(rest), pal, layout)); got <= budget {
		t.Fatalf("the fixture renders to %d rows, which already fits the budget; the cut would never be exercised", got)
	}
	cut := commitCut(rest, budget, pal, layout)
	if cut <= 0 || cut >= len(rest) {
		t.Fatalf("commitCut = %d, want a real cut inside the text", cut)
	}
	if !isParagraphEnd(rest, cut) {
		t.Errorf("commitCut = %d cuts at %q, which is not the end of a blank line; "+
			"a mid-paragraph seal would put a render in the scrollback the next delta contradicts",
			cut, string(rest[max(0, cut-12):cut]))
	}
	if got := len(liveMarkdownLines(string(rest[cut:]), pal, layout)); got > budget {
		t.Errorf("after the cut %d rows remain live, past the %d-row budget", got, budget)
	}
}

// A single paragraph with no blank line has no boundary to commit at, so the
// block seals whole rather than pretending the seam is clean.
func TestLiveCommitCutSealsWholeWhenThereIsNoBoundary(t *testing.T) {
	pal := DarkPalette()
	layout := ComputeLayout(State{Width: 70, Height: 20})
	rest := []rune(strings.Repeat("one very long paragraph with no blank line anywhere. ", 40))
	if got := commitCut(rest, 6, pal, layout); got != -1 {
		t.Errorf("commitCut = %d, want -1 when no blank line exists", got)
	}
}

// A long answer streams within the row budget the whole way and never loses or
// repeats text, however often it has to commit.
func TestLiveLongAnswerStaysBoundedAndKeepsEveryWord(t *testing.T) {
	v := newLivePainter()
	s := New()
	s.Apply(TerminalResized{Width: 70, Height: 20}) // a small live budget

	paragraph := strings.Repeat("A sentence of answer text that will wrap. ", 6) + "\n\n"
	var seen strings.Builder
	var painted []string
	for i := 0; i < 8; i++ {
		seen.WriteString(paragraph)
		s.Apply(AssistantDelta{Text: paragraph})
		painted = append(painted, v.render(s)...)
		if len(v.live) > liveBlockCap {
			t.Fatalf("the live region grew to %d rows, past the %d-row cap", len(v.live), liveBlockCap)
		}
		onScreen := joinAll(painted) + joinAll(v.block)
		if got := squeeze(onScreen); got != squeeze(seen.String()) {
			t.Fatalf("after paragraph %d the painted text is %q, want %q", i, got, squeeze(seen.String()))
		}
	}
}

// UTF-8 must survive the commit boundary. A CJK answer commits in character
// units, so the scrollback can never hold half a rune.
func TestLiveCommitKeepsUTF8Intact(t *testing.T) {
	v := newLivePainter()
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 20})

	paragraph := strings.Repeat("日本語のテキストがここにあります。絵文字もあります。", 8) + "\n\n"
	var seen strings.Builder
	var painted []string
	for i := 0; i < 10; i++ {
		seen.WriteString(paragraph)
		s.Apply(AssistantDelta{Text: paragraph})
		history := v.render(s)
		painted = append(painted, history...)
		all := joinAll(painted) + joinAll(v.block)
		if !utf8Valid(all) {
			t.Fatalf("painted output is not valid UTF-8: %q", all)
		}
		if got := squeeze(all); got != squeeze(seen.String()) {
			t.Fatalf("painted %q, want %q", got, squeeze(seen.String()))
		}
	}
}

// Each scrollback line leaves exactly once. A frame that emitted a line twice
// would put two copies of it in the terminal, and there is no later frame that
// can take one back.
func TestLiveAppendsEachScrollbackLineOnce(t *testing.T) {
	p, _ := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 70, Height: 40})
	p.state.Status.Model = "p/m"

	var history []string
	for i := 0; i < 6; i++ {
		history = append(history, liveEvent(p, UserSubmitted{Text: "question " + strconv.Itoa(i)})...)
	}
	history = append(history, liveEvent(p, AssistantDelta{Text: "an answer"})...)
	history = append(history, liveEvent(p, AssistantDone{})...)

	joined := joinAll(history)
	for i := 0; i < 6; i++ {
		line := "question " + strconv.Itoa(i)
		if n := strings.Count(joined, line); n != 1 {
			t.Errorf("%q appears %d times in the scrollback, want exactly one", line, n)
		}
	}
	if strings.Contains(joined, "> question") {
		t.Errorf("scrollback keeps the old \"> \" question prefix, want the background block with no prefix")
	}
}

// A wiped transcript must restart the painter's progress. /clear leaves the
// scrollback alone, so the painter has to stop counting entries that are no
// longer there.
func TestLiveForgetRestartsProgressAfterClear(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 70, Height: 30})
	liveTurn(p, "the first answer\n")

	p.runCommand(Command{Name: "clear", Raw: "/clear"})
	if painter.painted != 0 {
		t.Fatalf("after /clear the painter still counts %d entries", painter.painted)
	}
	liveEvent(p, UserSubmitted{Text: "a fresh question"})
	liveEvent(p, AssistantDelta{Text: "a fresh answer"})
	liveEvent(p, AssistantDone{})

	view := ansi.Strip(painter.view())
	if !strings.Contains(view, "? help") {
		t.Errorf("the composer is missing after /clear:\n%s", view)
	}
	if painter.painted != len(p.state.Entries) {
		t.Errorf("painted %d of %d entries", painter.painted, len(p.state.Entries))
	}
}

// A viewport scroll offset is the alt-screen path's concern. In live mode the
// terminal owns scrolling, so no offset may reach the frame: the live region
// is the same whether the user is at the live edge or forty lines back.
func TestLiveRegionIgnoresTheViewportScrollOffset(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	liveTurn(p, "the answer\n")
	before := painter.view()

	p.state.Apply(ScrollMoved{Delta: 40})
	if got := painter.view(); got != before {
		t.Errorf("a scroll offset changed the frame:\nbefore %q\nafter  %q", before, got)
	}
	p.state.Apply(ScrollToBottom{})
	if got := painter.view(); got != before {
		t.Errorf("re-engaging the live edge changed the frame:\nbefore %q\nafter  %q", before, got)
	}
}

// A mouse message cannot reach the live frame. The mode never asks for a
// mouse mode, so any bytes that look like one are text; dropping them here is
// what keeps the alt-screen handler from becoming part of this boundary.
func TestLiveDropsMouseMessagesInsteadOfRoutingThem(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	liveTurn(p, "the answer\n")
	before := painter.view()

	for _, m := range []tea.MouseMsg{
		{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress},
		{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress},
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
	} {
		p.Update(m)
	}
	if got := painter.view(); got != before {
		t.Errorf("a mouse message changed the frame:\nbefore %q\nafter  %q", before, got)
	}
}

// ---- live edge ----

// An approval must stay modal in output-first mode: the dialog owns the
// keyboard, so it replaces the answer and the composer entirely rather than
// competing with them for rows.
func TestLiveApprovalOwnsTheLiveRegion(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 80, Height: 30})
	liveEvent(p, AssistantDelta{Text: "I will run a shell command.\n"})

	request := approvalRequest{
		toolName: "bash", reason: "runs a shell command",
		summary: "rm -rf build", answer: make(chan approvalAnswer, 1),
	}
	p.pending = &request
	liveEvent(p, ApprovalAsked{
		ToolName: "bash", Reason: "runs a shell command",
		Summary: "rm -rf build", Choices: approvalChoices,
	})

	view := ansi.Strip(painter.view())
	if !strings.Contains(view, "Run this tool?") {
		t.Fatalf("the approval dialog is missing:\n%s", view)
	}
	if !strings.Contains(view, "rm -rf build") {
		t.Errorf("the approval summary is missing:\n%s", view)
	}
	if strings.Contains(view, "? help") {
		t.Errorf("the composer is still live while the dialog owns the keyboard:\n%s", view)
	}

	// Answering it brings the composer back.
	p.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	p.pending = nil
	liveEvent(p, ApprovalAnswered{})
	if !strings.Contains(ansi.Strip(painter.view()), "? help") {
		t.Errorf("the composer did not come back after the dialog closed:\n%s", ansi.Strip(painter.view()))
	}
}

// Shutdown must not lose work. A turn interrupted mid-answer, or an answer
// hidden behind an overlay, is still sealed to the scrollback on the way out.
func TestLiveShutdownSealsWhatWasStillStreaming(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 70, Height: 12}) // append-only: no live budget
	liveEvent(p, AssistantDelta{Text: "the first half of an interrupted answer"})

	var out strings.Builder
	painter.sealToTerminal(&out, p.state)
	if got := ansi.Strip(out.String()); !strings.Contains(got, "the first half of an interrupted answer") {
		t.Errorf("the interrupted answer was lost on shutdown:\n%s", got)
	}
	if painter.view() != "" {
		t.Errorf("the live region survives shutdown: %q", painter.view())
	}
}

// squeeze removes every whitespace rune. Wrapping reflows spaces and newlines,
// so a character-level comparison has to ignore them; nothing else may change.
func squeeze(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// joinAll flattens rendered rows into the string a terminal would show.
func joinAll(lines []string) string { return strings.Join(lines, "\n") }

// utf8Valid reports whether every rune survived.
func utf8Valid(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// A narrower terminal must re-wrap what is still live, and must leave what is
// already in the scrollback alone: the terminal owns those rows now. A
// painter that tried to re-flow history would be rewriting scrollback.
func TestLiveResizeRewrapsOnlyTheLiveRegion(t *testing.T) {
	p, painter := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 120, Height: 30})
	liveTurn(p, "an answer that fits on one line easily\n")

	p.state.Apply(TerminalResized{Width: 40, Height: 30})
	liveEvent(p, TerminalResized{Width: 40, Height: 30})
	rows := painter.view()
	for _, row := range strings.Split(rows, "\n") {
		if w := CellWidth(row); w > 38 {
			t.Errorf("a live row is %d cells wide on a 40-column terminal: %q", w, row)
		}
	}
	// The composer is still the last thing on screen.
	lines := strings.Split(rows, "\n")
	if last := strings.TrimSpace(ansi.Strip(lines[len(lines)-1])); !strings.Contains(last, "? help") {
		t.Errorf("the composer lost the bottom row after the resize: %q", last)
	}
}
