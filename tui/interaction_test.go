package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/session"
	tea "github.com/charmbracelet/bubbletea"
)

// keyPress drives one key through Update, discarding the command: these tests
// assert on state, and commands that start turns are covered separately with
// scripted loops. Update runs on the same Program pointer, so no assignment
// copies the mutex the Program now holds.
func keyPress(p *Program, msg tea.KeyMsg) {
	p.Update(msg)
}

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestTypingInsertsAtTheCursor(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("ab"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyLeft})
	keyPress(p, runes("X"))
	if p.state.Input != "aXb" || p.state.Cursor != 2 {
		t.Errorf("input = %q cursor = %d, want aXb at 2", p.state.Input, p.state.Cursor)
	}
}

func TestAltEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(InputChanged{Text: "ab"})
	p.state.Apply(ComposerChanged{Text: "ab", Cursor: 1})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if p.state.Input != "a\nb" {
		t.Errorf("input = %q, want a newline inserted, not a submit", p.state.Input)
	}
	if p.state.Status.Busy {
		t.Error("a composer chord must never start a turn")
	}
}

func TestCtrlJInsertsNewline(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if p.state.Input != "\n" {
		t.Errorf("input = %q, want the ctrl+j newline", p.state.Input)
	}
}

// On an idle single-line composer the arrows belong to the transcript, because
// that is where the wheel delivers its ticks (see wheelScrolls). History recall
// is not lost — ctrl+p and ctrl+n were already bound to the same handlers and
// still reach it, including the return-to-draft step.
func TestHistoryRecallMovesToTheCtrlBindingsOnAnIdleComposer(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "earlier"})
	p.state.Apply(InputChanged{Text: ""})

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlP})
	if p.state.Input != "earlier" {
		t.Fatalf("ctrl+p did not recall history: input = %q", p.state.Input)
	}
	// The draft is restored by the same binding, so a cancelled recall does not
	// strand the user in someone else's prompt.
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlN})
	if p.state.Input != "" {
		t.Errorf("ctrl+n did not restore the draft: input = %q", p.state.Input)
	}
}

func TestUpMovesWithinMultiline(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "one\ntwo", Cursor: 6})
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Input != "one\ntwo" || p.state.Cursor != 2 {
		t.Errorf("input = %q cursor = %d, want a line move, not history", p.state.Input, p.state.Cursor)
	}
}

// Once a prompt exists, up on an empty composer recalls history instead of
// scrolling: that is the reason most people reach for the key. Overshooting
// the oldest entry clamps, and down walks back to the stashed draft.
func TestArrowUpRecallsHistoryOnceOneExists(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "first"})
	p.state.Apply(UserSubmitted{Text: "second"})
	p.state.Apply(InputChanged{Text: ""})

	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Input != "second" {
		t.Fatalf("first up = %q, want the most recent prompt", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Input != "first" {
		t.Fatalf("second up = %q, want the older prompt", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Input != "first" {
		t.Errorf("third up = %q, want it clamped at the oldest prompt", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	if p.state.Input != "second" {
		t.Errorf("down = %q, want the newer prompt", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	if p.state.Input != "" {
		t.Errorf("down past the newest = %q, want the draft restored", p.state.Input)
	}
}

// The composer folds to the live terminal width, so vertical movement must
// hop between the wrapped rows the user sees. A single 20-rune line on a
// 12-column terminal is three display rows of eight, eight, and four.
func TestArrowKeysHopWrappedComposerRows(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 12, Height: 24})
	p.state.Apply(ComposerChanged{Text: strings.Repeat("a", 20), Cursor: 19})

	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Cursor != 11 {
		t.Fatalf("up = %d, want the previous wrapped row (11)", p.state.Cursor)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Cursor != 3 {
		t.Fatalf("up again = %d, want the first wrapped row (3)", p.state.Cursor)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	if p.state.Cursor != 11 {
		t.Errorf("down = %d, want the wrapped row below (11)", p.state.Cursor)
	}
}

func TestCtrlCQuitsOnlyWhenIdle(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !p.state.Quit {
		t.Error("an idle ctrl+c must quit: there is nothing to cancel")
	}
}

func TestCtrlCCancelsInsteadOfQuitting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "work"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlC})
	if p.state.Quit {
		t.Error("a busy ctrl+c must cancel the turn, not discard the session")
	}
	// Cancellation is a synchronous semantic transition: the reducer records
	// one terminal Cancelled state and the footer converges to idle.
	if !p.state.Cancelled {
		t.Error("no cancellation recorded: want the terminal Cancelled state")
	}
	if p.state.Status.Busy {
		t.Error("spinner still claims working after cancellation")
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "cancelled") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the single cancellation line", p.state.Entries)
	}
}

func TestEnterQueuesWhileBusy(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "first"})
	p.state.Apply(InputChanged{Text: "second"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if len(p.state.Queued) != 1 || p.state.Queued[0] != "second" {
		t.Errorf("Queued = %v, want the prompt held, not raced into a second turn", p.state.Queued)
	}
	if p.state.Input != "" {
		t.Errorf("input = %q, want it cleared after queueing", p.state.Input)
	}
}

func TestQueueRefusesPastTheCap(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "first"})
	for i := 0; i < maxQueued+2; i++ {
		p.state.Apply(InputChanged{Text: "x"})
		keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if len(p.state.Queued) != maxQueued {
		t.Errorf("Queued = %d, want the cap %d", len(p.state.Queued), maxQueued)
	}
}

// scriptedLoop builds a loop answering every request with text turns.
func scriptedLoop(t *testing.T, texts ...string) *agent.Loop {
	t.Helper()
	index := 0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		text := "done"
		if index < len(texts) {
			text = texts[index]
		}
		index++
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: text}},
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop
}

// idleTool is never called: the scripted answers request no tools.
type idleTool struct{ blockingTool }

func TestQueuedPromptsDrainInOrder(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "one", "two"), "test", 0)
	p.state.Queued = []string{"second"}
	p.runTurn("first")

	// Draining happens on the update goroutine when the turn's message
	// arrives — the turn itself must not touch the queue. Drive both steps
	// the way the runtime would.
	model, cmd := p.Update(turnFinishedMsg{})
	if model.(*Program) != p {
		t.Fatal("Update returned a different program")
	}
	if cmd == nil {
		t.Fatal("no drain command: the queued prompt was dropped")
	}
	cmd() // runs the second turn synchronously

	var submitted []string
	timeout := time.After(5 * time.Second)
	for len(submitted) < 2 {
		select {
		case event := <-p.events:
			if user, ok := event.(UserSubmitted); ok {
				submitted = append(submitted, user.Text)
			}
		case <-timeout:
			t.Fatalf("submitted = %v, want both prompts in order", submitted)
		}
	}
	if submitted[0] != "first" || submitted[1] != "second" {
		t.Errorf("submitted = %v, want [first second]", submitted)
	}
	if len(p.state.Queued) != 0 {
		t.Errorf("Queued = %v, want it drained", p.state.Queued)
	}
}

func TestCancelReachesARunningTool(t *testing.T) {
	tool := newBlockingTool("bash")
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{}}},
			}), nil
		},
		Tools: []agent.Tool{tool},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("go")
	}()
	select {
	case <-tool.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never started")
	}
	p.cancelTurnNow("cancelled")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel never reached the running tool: the turn hangs")
	}
}

func TestCancelledStreamReportsInterruption(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("go")
	}()
	// Let the turn block in the stream, then cancel like ctrl+c does.
	time.Sleep(200 * time.Millisecond)
	p.cancelTurnNow("cancelled")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled turn never returned")
	}

	// One semantic cancellation: the synchronous transition records it in
	// state, and the unwinding turn publishes at most one terminal event
	// through the channel. Neither may report an error.
	if !p.state.Cancelled {
		t.Error("no cancellation recorded in state: want the terminal Cancelled state")
	}
	if p.state.Status.Busy {
		t.Error("spinner still claims working after cancellation")
	}
	cancelled := 0
	var notices []string
	timeout := time.After(2 * time.Second)
drain:
	for {
		select {
		case event := <-p.events:
			switch e := event.(type) {
			case Notice:
				notices = append(notices, e.Text)
			case TurnCancelled:
				cancelled++
			case AssistantDone:
				// Terminal close; idempotent after cancellation.
			}
		case <-timeout:
			break drain
		}
	}
	for _, notice := range notices {
		if strings.HasPrefix(notice, "error:") {
			t.Errorf("notices = %v, want interruption, not an error report", notices)
		}
	}
	if cancelled > 1 {
		t.Errorf("TurnCancelled published %d times, want at most one terminal transition", cancelled)
	}
	// The synchronous path already recorded the single user-visible line.
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && entry.Notice == "cancelled" {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the single cancellation line", p.state.Entries)
	}
}

// Pickers.

func TestSessionsCommandNeedsAProvider(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "sessions"}.runOn(p)
	select {
	case event := <-p.events:
		t.Errorf("event = %#v, want state-only notice without a runtime", event)
	default:
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "unavailable") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the unavailable notice in state", p.state.Entries)
	}
}

func TestSessionPickerResumeReplacesHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetSessionBrowser(
		func() ([]SessionItem, error) {
			return []SessionItem{{ID: 7, Title: "", Model: "m", Updated: "today", Cwd: "/w"}}, nil
		},
		func(id int64) (*session.Session, OpenedSession, error) {
			if id != 7 {
				t.Errorf("open id = %d, want 7", id)
			}
			sess := &session.Session{ID: 7}
			return sess, OpenedSession{
				History: []agent.Message{agent.UserMessage{Text: "earlier"}},
				Entries: []Entry{{Kind: EntryUser, UserText: "earlier"}},
				Notice:  "resumed session 7 (1 messages)",
			}, nil
		},
	)
	p.state.Apply(UserSubmitted{Text: "current"})
	Command{Name: "sessions"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("no picker opened")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})

	if len(p.history) != 1 {
		t.Errorf("history = %d messages, want the resumed one, not a merge", len(p.history))
	}
	if len(p.state.Entries) != 2 { // replayed user + resume notice
		t.Errorf("entries = %+v, want replay plus notice", p.state.Entries)
	}
	if p.state.Picker != nil {
		t.Error("the picker must close after choosing")
	}
}

func TestModelPickerChooses(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var switched string
	p.SetModelSwitcher(func(id string) error { switched = id; return nil }, func() string { return switched })
	p.SetModelLister(func() []string { return []string{"a/1", "b/2"} })

	Command{Name: "model"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("no picker for bare /model")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b/")})
	if len(p.state.Picker.Visible()) != 1 {
		t.Fatalf("visible = %v, want the filter to narrow", p.state.Picker.Visible())
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if switched != "b/2" {
		t.Errorf("switched = %q, want b/2", switched)
	}
	if p.state.Status.Model != "b/2" {
		t.Errorf("status model = %q, want the footer in step", p.state.Status.Model)
	}
}

func TestJobsViewShowsTail(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetJobsLister(func() []JobItem {
		return []JobItem{{ID: "job-1", Command: "make", Running: false, ExitCode: 0, Elapsed: "1s", Tail: "build ok"}}
	})
	Command{Name: "jobs"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("no jobs view opened")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "build ok") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the job tail in the transcript", p.state.Entries)
	}
}

func TestThemeCommandPersists(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var saved string
	p.SetThemeChangedHandler(func(theme string) { saved = theme })
	Command{Name: "theme", Args: []string{"light"}}.runOn(p)
	if p.state.Theme != ThemeLight || saved != ThemeLight {
		t.Errorf("theme = %q saved = %q, want light applied and persisted", p.state.Theme, saved)
	}
	Command{Name: "theme", Args: []string{"nope"}}.runOn(p)
	if p.state.Theme != ThemeDark {
		t.Errorf("theme = %q, want an unknown name to degrade to dark", p.state.Theme)
	}
}

func TestScrollKeysMoveTheViewport(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	keyPress(p, tea.KeyMsg{Type: tea.KeyPgUp})
	if p.state.Scroll <= 0 || p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want scrolled up", p.state.Scroll, p.state.Follow)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyPgDown})
	if p.state.Scroll != 0 || !p.state.Follow {
		t.Errorf("Scroll = %d Follow = %v, want the live edge back", p.state.Scroll, p.state.Follow)
	}
}

func TestWindowSizeReachesState(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.Update(tea.WindowSizeMsg{Width: 90, Height: 25})
	if p.state.Width != 90 || p.state.Height != 25 {
		t.Errorf("size = %dx%d, want 90x25", p.state.Width, p.state.Height)
	}
}
