package tui

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// errSwitchRefused stands in for an owner that rejects the transition.
var errSwitchRefused = errors.New("refused")

// countingOwner is a workspace owner for the transition tests. It records
// every switch the program asks for, so a test can prove an accepted proposal
// moved authority through the owner and a rejected one did not.
type countingOwner struct {
	mu       sync.Mutex
	current  string
	switches []string
}

func newCountingOwner(current string) *countingOwner {
	return &countingOwner{current: strings.ToUpper(current)}
}

func (o *countingOwner) ws() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.current
}

func (o *countingOwner) asked() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.switches...)
}

// install wires the owner onto a program. The Switch closure mirrors the
// production one: it moves the owner and publishes the new identity, so a test
// observes the same chrome a real transition produces.
func (o *countingOwner) install(p *Program, predict func(string) (TurnTransition, bool)) {
	accents := map[string]string{"ASK": "37", "BUILD": "33", "FUZZ": "200"}
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: o.ws,
		Switch: func(name string) error {
			label := strings.ToUpper(name)
			o.mu.Lock()
			o.switches = append(o.switches, label)
			o.current = label
			o.mu.Unlock()
			p.PublishWorkspace(WorkspaceUpdated{
				Workspace:       label,
				WorkspaceAccent: accents[label],
				HasWorkspace:    true,
			})
			return nil
		},
		Predict: predict,
		PrepareTurn: func(string) TurnAuthority {
			return TurnAuthority{Active: o.ws(), Handler: o.ws()}
		},
	})
}

// readProposal is a Predict that proposes ASK for a BUILD informational
// request and nothing otherwise. It mirrors the production policy's shape
// without depending on it, and reads the owner's live workspace so it stops
// proposing once the transition lands.
func readProposal(owner *countingOwner) func(string) (TurnTransition, bool) {
	return func(text string) (TurnTransition, bool) {
		if owner.ws() == "BUILD" && strings.HasPrefix(text, "why") {
			return TurnTransition{
				Current: "BUILD", Proposed: "ASK",
				CurrentAccent: "33", ProposedAccent: "37",
				Intent: "READ", Strength: "strong",
				Question: "Switch to ASK?", Reason: "This request appears informational.",
			}, true
		}
		return TurnTransition{}, false
	}
}

// press runs one key and returns the command the program asked for.
func press(p *Program, msg tea.KeyMsg) tea.Cmd {
	_, cmd := p.Update(msg)
	return cmd
}

// typeAndEnter types a prompt and presses enter, returning the command.
func typeAndEnter(p *Program, text string) tea.Cmd {
	press(p, runes(text))
	return press(p, tea.KeyMsg{Type: tea.KeyEnter})
}

func newTransitionProgram(t *testing.T, requests *int32) (*Program, *countingOwner) {
	t.Helper()
	o := newCountingOwner("BUILD")
	p := NewProgram(countingLoop(t, requests), "test", 0)
	o.install(p, readProposal(o))
	p.state.Status.Workspace = "BUILD"
	return p, o
}

// TestProposalBlocksTheTurnUntilDecided is invariant 4: nothing the model
// could do is scheduled while a proposal is unresolved. The submit returns no
// command at all, so runTurn cannot even be queued.
func TestProposalBlocksTheTurnUntilDecided(t *testing.T) {
	var requests int32
	p, _ := newTransitionProgram(t, &requests)

	cmd := typeAndEnter(p, "why does this work?")
	if cmd != nil {
		t.Fatal("a command was returned while the proposal was unresolved")
	}
	if p.state.Transition == nil {
		t.Fatal("no proposal was raised for a strong informational request in BUILD")
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0 while the proposal is pending", got)
	}
	if p.state.Status.Busy {
		t.Error("the runtime went busy with the proposal unresolved")
	}
}

// TestProposalOnlyOpensWhenThePolicyRequestsIt guards the "no proposal"
// branch: ordinary input starts the turn exactly as before.
func TestProposalOnlyOpensWhenThePolicyRequestsIt(t *testing.T) {
	var requests int32
	p, _ := newTransitionProgram(t, &requests)

	cmd := typeAndEnter(p, "implement the fix")
	if p.state.Transition != nil {
		t.Fatal("a proposal was raised where the policy requested none")
	}
	if cmd == nil {
		t.Fatal("no turn was scheduled")
	}
	cmd()
	if got := atomic.LoadInt32(&requests); got == 0 {
		t.Error("the turn never reached the model")
	}
}

// TestProposalNavigationClampsAtTheEnds pins the conservative default and the
// non-wrapping movement: an extra keypress cannot land on the authority change.
func TestProposalNavigationClampsAtTheEnds(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TransitionProposed{Text: "why?", Current: "BUILD", Proposed: "ASK"})
	if p.state.Transition.Selected() {
		t.Fatal("the proposal did not open on the conservative choice")
	}
	press(p, tea.KeyMsg{Type: tea.KeyLeft})
	if p.state.Transition.Selection != 0 {
		t.Error("left moved past the conservative choice")
	}
	press(p, tea.KeyMsg{Type: tea.KeyRight})
	if !p.state.Transition.Selected() {
		t.Error("right did not select the proposed workspace")
	}
	press(p, tea.KeyMsg{Type: tea.KeyRight})
	if p.state.Transition.Selection != 1 {
		t.Error("right wrapped off the proposed workspace")
	}
	press(p, tea.KeyMsg{Type: tea.KeyLeft})
	if p.state.Transition.Selected() {
		t.Error("left did not return to the conservative choice")
	}
}

// TestRejectedProposalKeepsAuthorityAndRunsTheTurn covers the fallback: Enter
// on the default keeps the workspace, switches nothing, and the turn still
// runs under the current authority (I2, I3).
func TestRejectedProposalKeepsAuthorityAndRunsTheTurn(t *testing.T) {
	var requests int32
	p, owner := newTransitionProgram(t, &requests)

	typeAndEnter(p, "why does this work?")
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})

	if p.state.Transition != nil {
		t.Error("the proposal survived the decision")
	}
	if got := owner.asked(); len(got) != 0 {
		t.Errorf("a rejected proposal switched the workspace: %v", got)
	}
	if owner.ws() != "BUILD" {
		t.Errorf("workspace = %q, want BUILD", owner.ws())
	}
	if p.state.Status.Workspace != "BUILD" {
		t.Errorf("displayed workspace = %q, want BUILD", p.state.Status.Workspace)
	}
	if cmd == nil {
		t.Fatal("rejecting the proposal did not run the turn")
	}
	cmd()
	if got := atomic.LoadInt32(&requests); got == 0 {
		t.Error("the turn did not reach the model after rejection")
	}
}

// TestEscRejectsWithoutSwitching pins the cancellation convention: Esc
// declines the transition and never gains authority.
func TestEscRejectsWithoutSwitching(t *testing.T) {
	var requests int32
	p, owner := newTransitionProgram(t, &requests)

	typeAndEnter(p, "why does this work?")
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEsc})

	if p.state.Transition != nil {
		t.Error("Esc left the proposal open")
	}
	if got := owner.asked(); len(got) != 0 {
		t.Errorf("Esc switched the workspace: %v", got)
	}
	if cmd == nil {
		t.Error("Esc did not continue the turn under the current workspace")
	}
}

// TestAcceptedProposalSwitchesThroughTheOwner is invariant 7 and 10: the
// accepted transition goes through the authoritative Switch, and the resulting
// chrome is the normal target identity.
func TestAcceptedProposalSwitchesThroughTheOwner(t *testing.T) {
	var requests int32
	p, owner := newTransitionProgram(t, &requests)

	typeAndEnter(p, "why does this work?")
	press(p, tea.KeyMsg{Type: tea.KeyRight})
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})

	if p.state.Transition != nil {
		t.Error("the proposal survived acceptance")
	}
	if got := owner.asked(); len(got) != 1 || got[0] != "ASK" {
		t.Fatalf("switches = %v, want exactly [ASK]", got)
	}
	if owner.ws() != "ASK" {
		t.Errorf("owner workspace = %q, want ASK", owner.ws())
	}
	if p.state.Status.Workspace != "ASK" {
		t.Errorf("displayed workspace = %q, want the normal ASK identity", p.state.Status.Workspace)
	}
	if p.state.Status.WorkspaceAccent != "37" {
		t.Errorf("accent = %q, want the ASK semantic accent", p.state.Status.WorkspaceAccent)
	}
	if cmd == nil {
		t.Fatal("an accepted proposal did not run the turn")
	}
	cmd()
	if got := atomic.LoadInt32(&requests); got == 0 {
		t.Error("the turn did not reach the model after acceptance")
	}
}

// TestCtrlCAbandonsThePendingTurn: the reflex abort drops the turn instead of
// running it, leaves authority untouched, and keeps the typed prompt.
func TestCtrlCAbandonsThePendingTurn(t *testing.T) {
	var requests int32
	p, owner := newTransitionProgram(t, &requests)

	typeAndEnter(p, "why does this work?")
	press(p, tea.KeyMsg{Type: tea.KeyCtrlC})

	if p.state.Transition != nil {
		t.Error("Ctrl+C left the proposal open")
	}
	if got := owner.asked(); len(got) != 0 {
		t.Errorf("Ctrl+C switched the workspace: %v", got)
	}
	if p.state.Input != "why does this work?" {
		t.Errorf("input = %q, want the typed prompt preserved", p.state.Input)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0 after abandoning", got)
	}
}

// TestAcceptedSwitchFailureDoesNotRunTheTurn: when the owner refuses the
// switch, the turn must not run under the workspace the user declined.
func TestAcceptedSwitchFailureDoesNotRunTheTurn(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	p.state.Status.Workspace = "BUILD"
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return "BUILD" },
		Switch:  func(string) error { return errSwitchRefused },
		Predict: func(string) (TurnTransition, bool) {
			return TurnTransition{Current: "BUILD", Proposed: "ASK", Question: "Switch to ASK?", Reason: "info"}, true
		},
	})

	typeAndEnter(p, "why does this work?")
	press(p, tea.KeyMsg{Type: tea.KeyRight})
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})

	if cmd != nil {
		t.Error("a refused switch still started the turn")
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0 when the switch failed", got)
	}
	if p.state.Status.Workspace != "BUILD" {
		t.Errorf("displayed workspace = %q, want BUILD", p.state.Status.Workspace)
	}
}

// TestNoPredictHookKeepsTheOldBehavior: a program without a Predict hook never
// raises a proposal, so the feature is opt-in at the seam.
func TestNoPredictHookKeepsTheOldBehavior(t *testing.T) {
	var requests int32
	p := NewProgram(countingLoop(t, &requests), "test", 0)
	p.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:     func() string { return "BUILD" },
		Switch:      func(string) error { return nil },
		PrepareTurn: func(string) TurnAuthority { return TurnAuthority{Active: "BUILD", Handler: "BUILD"} },
	})

	cmd := typeAndEnter(p, "why does this work?")
	if p.state.Transition != nil {
		t.Fatal("a proposal appeared without a Predict hook")
	}
	if cmd == nil {
		t.Fatal("the turn was not scheduled")
	}
}

// TestStaleProposalCannotSurviveALifecycleEvent is the state-invariant
// contract: turn completion, cancellation, history replacement, an explicit
// workspace change, and a clear all drop a proposal.
func TestStaleProposalCannotSurviveALifecycleEvent(t *testing.T) {
	cases := []struct {
		name  string
		event Event
	}{
		{"turn submitted", UserSubmitted{Text: "next", Seq: 2}},
		{"turn done", AssistantDone{Seq: 1}},
		{"turn cancelled", TurnCancelled{Seq: 1}},
		{"history replaced", HistoryReplaced{}},
		{"workspace changed", WorkspaceUpdated{Workspace: "FUZZ", HasWorkspace: true}},
		{"cleared", Cleared{}},
		{"quit", QuitRequested{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.Apply(TransitionProposed{Text: "why?", Current: "BUILD", Proposed: "ASK"})
			if s.Transition == nil {
				t.Fatal("the proposal did not open")
			}
			s.Apply(tc.event)
			if s.Transition != nil {
				t.Errorf("the proposal survived %s", tc.name)
			}
		})
	}
}

// TestTransitionPromptRendersTheDecision pins the decision surface: the
// question, the reason, both workspace identities, and no percentage.
func TestTransitionPromptRendersTheDecision(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(TransitionProposed{
		Text: "why does this work?", Current: "BUILD", Proposed: "ASK",
		CurrentAccent: "33", ProposedAccent: "37",
		Intent: "READ", Strength: "strong",
		Question: "Switch to ASK?", Reason: "This request appears informational.",
	})

	out := stripANSI(Render(s))
	for _, want := range []string{"Switch to ASK?", "This request appears informational.", "Keep BUILD", "ASK"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "%") {
		t.Errorf("the decision surface rendered a fabricated percentage:\n%s", out)
	}
}
