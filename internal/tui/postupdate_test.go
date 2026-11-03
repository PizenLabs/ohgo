package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/update"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func samplePostUpdate() PostUpdateView {
	return PostUpdateView{
		Version: "v0.8.3",
		Title:   "What's new in v0.8.3",
		URL:     "https://github.com/PizenLabs/ohgo/releases/",
		Changes: []PostUpdateChange{
			{Type: "added", Title: "Complete update lifecycle"},
			{Type: "improved", Title: "Improved TUI update handling"},
			{Type: "fixed", Title: "Fixed concurrent update behavior"},
		},
	}
}

// An observation is not authority: UpdateFound never opens either update
// lifecycle overlay.
func TestUpdateFoundNeverOpensALifecycleOverlay(t *testing.T) {
	p := newUpdateProgram(t)
	for i := 0; i < 8; i++ {
		p.state.Apply(UpdateFound{Version: "v9.9.9"})
	}
	if p.state.UpdateRestart != nil {
		t.Fatal("an available release opened the restart-required state")
	}
	if p.state.PostUpdate != nil {
		t.Fatal("an available release opened the What's New card")
	}
}

// The restart state is derived from the successful outcome itself, so any
// reducer consumer sees the same lifecycle without a second event.
func TestUpdateFinishedDerivesTheRestartState(t *testing.T) {
	state := New()
	state.Apply(UpdateFinished{InstalledVersion: "1.2.3"})
	if state.UpdateRestart == nil {
		t.Fatal("a verified install did not open the restart-required state")
	}
	if state.UpdateRestart.Version != "v1.2.3" {
		t.Fatalf("restart version = %q, want the release spelling", state.UpdateRestart.Version)
	}

	for _, outcome := range []UpdateFinished{
		{AlreadyCurrent: true},
		{Err: "download failed"},
	} {
		state := New()
		state.Apply(outcome)
		if state.UpdateRestart != nil {
			t.Fatalf("outcome %+v opened the restart-required state", outcome)
		}
	}
}

// A verified install leaves the process on the old binary, so the completion
// state must wait for an explicit Enter instead of exiting on its own.
func TestUpdateSuccessWaitsForExplicitExit(t *testing.T) {
	p := newUpdateProgram(t)
	p.state.Status.Version = "v1.0.0"
	p.SetUpdateFunc(func(context.Context) (update.Result, error) {
		return update.Result{InstalledVersion: "v1.2.3"}, nil
	})

	start := p.runCommand(Command{Name: "update"})
	if start == nil {
		t.Fatal("/update produced no command")
	}
	_, cmd := p.Update(start())
	if cmd != nil {
		t.Fatal("a successful update exited without waiting for Enter")
	}
	if p.state.UpdateRestart == nil {
		t.Fatal("a successful update did not open the restart-required state")
	}
	if p.state.UpdateRestart.Version != "v1.2.3" {
		t.Fatalf("restart version = %q, want v1.2.3", p.state.UpdateRestart.Version)
	}
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	frame := ansi.Strip(Render(p.state))
	for _, want := range []string{
		"Update installed: v1.2.3",
		"Restart ohgo to use the new version.",
		"Press Enter to exit",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("restart frame does not show %q:\n%s", want, frame)
		}
	}

	if cmd := press(p, runes("next")); cmd != nil {
		t.Fatal("ordinary typing exited from the restart-required state")
	}
	if p.state.UpdateRestart == nil {
		t.Fatal("ordinary typing dismissed the restart-required state")
	}
	if p.state.Input != "" {
		t.Fatalf("input = %q, want the restart state to own the keyboard", p.state.Input)
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		if cmd := press(p, key); cmd != nil {
			t.Fatalf("%q exited the restart-required state; only Enter may", key.String())
		}
		if p.state.UpdateRestart == nil {
			t.Fatalf("%q dismissed the restart-required state", key.String())
		}
	}

	cmd = press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not schedule the explicit exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Enter did not return tea.Quit for a clean terminal shutdown")
	}
}

// A no-op or failed update must not imply that a restart is required.
func TestNoOpAndFailedUpdatesDoNotRequireRestart(t *testing.T) {
	cases := map[string]func(context.Context) (update.Result, error){
		"already current": func(context.Context) (update.Result, error) {
			return update.Result{AlreadyCurrent: true, Status: update.Status{CurrentVersion: "v1.2.3"}}, nil
		},
		"failed": func(context.Context) (update.Result, error) {
			return update.Result{}, errors.New("checksum mismatch")
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			p := newUpdateProgram(t)
			p.SetUpdateFunc(fn)
			cmd := p.runCommand(Command{Name: "update"})
			if cmd == nil {
				t.Fatal("/update produced no command")
			}
			p.Update(cmd())
			if p.state.UpdateRestart != nil {
				t.Fatalf("%s opened the restart-required state", name)
			}
		})
	}
}

// The What's New card is consumed only by Enter. Opening it, typing, or Esc
// must not acknowledge the persisted transition.
func TestPostUpdateNoticeAcknowledgesOnlyOnEnter(t *testing.T) {
	p := newUpdateProgram(t)
	acks := 0
	p.ShowPostUpdateNotice(samplePostUpdate(), func() error {
		acks++
		return nil
	})
	if p.state.PostUpdate == nil {
		t.Fatal("the What's New card did not open")
	}

	for _, key := range []tea.KeyMsg{
		runes("next"),
		{Type: tea.KeyEsc},
		{Type: tea.KeySpace},
	} {
		if cmd := press(p, key); cmd != nil {
			t.Fatalf("key %q returned a command", key.String())
		}
		if p.state.PostUpdate == nil {
			t.Fatalf("key %q dismissed the notice without acknowledgement", key.String())
		}
	}
	if acks != 0 {
		t.Fatalf("acknowledgements before Enter = %d, want 0", acks)
	}

	if cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("acknowledging the notice returned a modal command")
	}
	if acks != 1 {
		t.Fatalf("acknowledgements = %d, want 1", acks)
	}
	if p.state.PostUpdate != nil {
		t.Fatal("Enter left the acknowledged card open")
	}

	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if acks != 1 {
		t.Fatalf("a later Enter acknowledged again: %d", acks)
	}
}

// A persistence failure must keep the card open: the next launch still owes
// the user a notice it can truthfully show.
func TestPostUpdateAckFailureKeepsTheCardOpen(t *testing.T) {
	p := newUpdateProgram(t)
	fail := true
	acks := 0
	p.ShowPostUpdateNotice(samplePostUpdate(), func() error {
		acks++
		if fail {
			return errors.New("disk unavailable")
		}
		return nil
	})

	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.PostUpdate == nil {
		t.Fatal("a failed acknowledgement dismissed the card")
	}
	if p.state.PostUpdate.Err == "" {
		t.Fatal("the card did not report the persistence failure")
	}

	fail = false
	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.PostUpdate != nil {
		t.Fatal("the retry did not dismiss the card")
	}
	if acks != 2 {
		t.Fatalf("acknowledgement attempts = %d, want 2", acks)
	}
}

// Ctrl+C exits without consuming the pending notice; the next interactive
// launch still owns the acknowledgement.
func TestCtrlCLeavesThePendingNoticeForTheNextLaunch(t *testing.T) {
	p := newUpdateProgram(t)
	acks := 0
	p.ShowPostUpdateNotice(samplePostUpdate(), func() error {
		acks++
		return nil
	})
	cmd := press(p, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C did not schedule an exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C did not return tea.Quit")
	}
	if acks != 0 {
		t.Fatalf("Ctrl+C acknowledged the notice %d times", acks)
	}
}

// The acknowledgement is presentation only: it never becomes a transcript
// entry or a model-visible message.
func TestPostUpdateCardIsChromeNotTranscript(t *testing.T) {
	state := New()
	state.Apply(TerminalResized{Width: 100, Height: 30})
	state.Apply(PostUpdateOpened{Notice: samplePostUpdate()})
	if len(state.Entries) != 0 {
		t.Fatalf("the card added %d transcript entries", len(state.Entries))
	}

	out := ansi.Strip(Render(state))
	for _, want := range []string{
		"Updated to v0.8.3",
		"What's new in v0.8.3",
		"Complete update lifecycle",
		"Improved TUI update handling",
		"Fixed concurrent update behavior",
		"https://github.com/PizenLabs/ohgo/releases/",
		"Enter dismiss",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("frame does not show %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Updated to v0.8.3") && !strings.HasPrefix(line, " ") {
			t.Errorf("the card is not horizontally centered: %q", line)
		}
	}
	if strings.Contains(out, "spinner") || strings.Contains(out, "⠋") {
		t.Errorf("the ack card rendered animation:\n%s", out)
	}
}

func TestPostUpdateCardBoundsALongRelease(t *testing.T) {
	view := PostUpdateView{Version: "v9.9.9", Title: "Notes"}
	for i := 0; i < postUpdateMaxChanges+4; i++ {
		view.Changes = append(view.Changes, PostUpdateChange{Type: "added", Title: strings.Repeat("change ", 20)})
	}
	state := New()
	state.Apply(TerminalResized{Width: 80, Height: 24})
	state.Apply(PostUpdateOpened{Notice: view})
	out := ansi.Strip(Render(state))
	if !strings.Contains(out, "and 4 more") {
		t.Fatalf("the bounded card did not report hidden changes:\n%s", out)
	}
}

// The highlight moves through the visible notes and parks on the release link
// at the bottom. Clamping keeps a repeated arrow from wrapping onto a row the
// user did not intend to select.
func TestPostUpdateArrowKeysMoveTheHighlightToTheReleaseLink(t *testing.T) {
	p := newUpdateProgram(t)
	p.ShowPostUpdateNotice(samplePostUpdate(), nil)
	if got := p.state.PostUpdate.Cursor; got != 0 {
		t.Fatalf("opening cursor = %d, want the first note", got)
	}

	press(p, tea.KeyMsg{Type: tea.KeyUp})
	if got := p.state.PostUpdate.Cursor; got != 0 {
		t.Fatalf("up at the top moved the highlight to %d", got)
	}
	press(p, tea.KeyMsg{Type: tea.KeyDown})
	if got := p.state.PostUpdate.Cursor; got != 1 {
		t.Fatalf("down cursor = %d, want 1", got)
	}
	for i := 0; i < 4; i++ {
		press(p, tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := p.state.PostUpdate.Cursor; got != 3 {
		t.Fatalf("down past the link cursor = %d, want the link row 3", got)
	}
	press(p, tea.KeyMsg{Type: tea.KeyDown})
	if got := p.state.PostUpdate.Cursor; got != 3 {
		t.Fatalf("down past the link moved off it to %d", got)
	}
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	if got := p.state.PostUpdate.Cursor; got != 2 {
		t.Fatalf("up from the link cursor = %d, want 2", got)
	}

	press(p, runes("m"))
	if got := p.state.PostUpdate.Cursor; got != 3 {
		t.Fatalf("m cursor = %d, want the release link", got)
	}
}

// Enter on a note acknowledges as before. Only the selected release link adds
// the browser launch, and the notice is consumed after the launch succeeds.
func TestPostUpdateLinkOpensOnEnterOnlyWhenSelected(t *testing.T) {
	p := newUpdateProgram(t)
	var opened []string
	p.SetURLOpener(func(target string) error {
		opened = append(opened, target)
		return nil
	})
	acks := 0
	ack := func() error {
		acks++
		return nil
	}

	p.ShowPostUpdateNotice(samplePostUpdate(), ack)
	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if len(opened) != 0 {
		t.Fatalf("Enter on a note opened %v", opened)
	}
	if acks != 1 || p.state.PostUpdate != nil {
		t.Fatalf("Enter on a note: acks=%d cardOpen=%v", acks, p.state.PostUpdate != nil)
	}

	p.ShowPostUpdateNotice(samplePostUpdate(), ack)
	press(p, runes("m"))
	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if len(opened) != 1 || opened[0] != samplePostUpdate().URL {
		t.Fatalf("opened = %v, want the release URL", opened)
	}
	if acks != 2 || p.state.PostUpdate != nil {
		t.Fatalf("Enter on the link: acks=%d cardOpen=%v", acks, p.state.PostUpdate != nil)
	}
}

// A release with no local notes still shows the link, but it opens without a
// highlight: a plain Enter keeps its old dismiss meaning until the user
// chooses the link with "m" or Down.
func TestPostUpdateNoNotesEnterStillDismisses(t *testing.T) {
	p := newUpdateProgram(t)
	opened := false
	p.SetURLOpener(func(string) error {
		opened = true
		return nil
	})
	acks := 0
	p.ShowPostUpdateNotice(PostUpdateView{
		Version: "v0.8.3",
		URL:     samplePostUpdate().URL,
	}, func() error {
		acks++
		return nil
	})
	if got := p.state.PostUpdate.Cursor; got != -1 {
		t.Fatalf("opening cursor = %d, want no highlight", got)
	}

	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if opened {
		t.Fatal("Enter opened the release page without selecting its link")
	}
	if acks != 1 || p.state.PostUpdate != nil {
		t.Fatalf("Enter: acks=%d cardOpen=%v", acks, p.state.PostUpdate != nil)
	}
}

// A failed browser launch must not consume the notice: the user has not seen
// the release the card owes them.
func TestPostUpdateLinkFailureKeepsTheCardOpen(t *testing.T) {
	p := newUpdateProgram(t)
	p.SetURLOpener(func(string) error { return errors.New("no browser") })
	acks := 0
	p.ShowPostUpdateNotice(samplePostUpdate(), func() error {
		acks++
		return nil
	})

	press(p, runes("m"))
	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.PostUpdate == nil {
		t.Fatal("a failed launch dismissed the card")
	}
	if acks != 0 {
		t.Fatalf("a failed launch acknowledged the notice %d times", acks)
	}
	if p.state.PostUpdate.LinkErr == "" {
		t.Fatal("the card did not report the failed launch")
	}
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	if out := ansi.Strip(Render(p.state)); !strings.Contains(out, "Could not open the release page.") {
		t.Fatalf("frame does not report the failed launch:\n%s", out)
	}
}
