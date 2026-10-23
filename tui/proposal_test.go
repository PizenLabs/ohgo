package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

// proposalPredict uses the real control-plane assessment, so the UI test sees
// the same request-derived question and options production does. It raises a
// proposal only where the assessment does.
func proposalPredict() func(string) (TurnTransition, bool) {
	return func(text string) (TurnTransition, bool) {
		proposal, ok := workspace.AssessProposal(workspace.Build, text, workspace.ProposalFacts{})
		if !ok {
			return TurnTransition{}, false
		}
		return TurnTransition{
			Kind:     TransitionKindProposal,
			Proposal: &proposal,
			Intent:   "EXECUTE",
			Strength: "strong",
			Question: proposal.Question,
			Reason:   proposal.Reason,
		}, true
	}
}

// newProposalProgram wires a program whose only decision source is the real
// proposal assessment. The owner stays on BUILD so the released turn is
// allowed.
func newProposalProgram(t *testing.T, requests *int32) (*Program, *countingOwner) {
	t.Helper()
	o := newCountingOwner("BUILD")
	p := NewProgram(countingLoop(t, requests), "test", 0)
	o.install(p, proposalPredict())
	p.state.Status.Workspace = "BUILD"
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	return p, o
}

// capturingLoop records the system blocks and messages of every provider
// request, so a test can prove a recorded human decision reached execution and
// the user's own message did not change.
func capturingLoop(t *testing.T, requests *int32, systems *[][]ai.SystemPromptBlock, messages *[][]ai.Message) *agent.Loop {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			atomic.AddInt32(requests, 1)
			*systems = append(*systems, append([]ai.SystemPromptBlock(nil), req.System...))
			*messages = append(*messages, append([]ai.Message(nil), req.Messages...))
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop
}

const proposalText = "Create a small portfolio site for John Smith."

// TestProposalBlocksTheTurnUntilDecided extends the proposal-blocking
// invariant: no command is scheduled while the decision is open, so loop.Run
// cannot start before the human answers.
func TestUserProposalBlocksTheTurnUntilDecided(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)

	cmd := typeAndEnter(p, proposalText)
	if cmd != nil {
		t.Fatal("a command was returned while the proposal was unresolved")
	}
	if p.state.Pending == nil || !p.state.Pending.IsProposal() {
		t.Fatal("the proposal did not open the permission surface")
	}
	if p.state.Transition != nil {
		t.Fatal("the proposal opened the centered transition picker")
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0 while the proposal is pending", got)
	}
	if p.state.Status.Busy {
		t.Error("the runtime went busy with the proposal unresolved")
	}
}

// TestProposalUsesTheOriginalPermissionSurface is the required UI contract:
// the proposal is rendered by renderApproval as an inline bordered box in the
// chat/prompt area, with the yellow/orange frame, the same cursor, and the
// same key family as a tool approval. It is not a centered card.
func TestProposalUsesTheOriginalPermissionSurface(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)
	typeAndEnter(p, proposalText)

	pending := p.state.Pending
	if pending == nil || pending.Proposal == nil {
		t.Fatal("no proposal is pending")
	}
	if got := approvalBorderColor(pending); got != ColorMutedAmber {
		t.Errorf("proposal frame accent = %q, want the permission amber %q", got, ColorMutedAmber)
	}

	out := stripANSI(Render(p.state))
	for _, want := range []string{
		proposalTitle,
		pending.Proposal.Question,
		"does not say how that",
		"no convention to inherit",
		"One file",
		"Multiple files",
		"Let the model decide",
		proposalFooterHint,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("permission surface missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "◀") || strings.Contains(out, "▶") {
		t.Errorf("the centered transition picker leaked into the proposal:\n%s", out)
	}
	if strings.Contains(out, "Enter to confirm · Esc keeps") {
		t.Errorf("the transition-card key line leaked into the proposal:\n%s", out)
	}
	if strings.Contains(out, "%") {
		t.Errorf("the proposal rendered a fabricated percentage:\n%s", out)
	}

	// The permission box is emitted inline, at the left edge, like a tool
	// approval. A centered picker would indent its top border.
	top := approvalLineWith(t, out, proposalTitle)
	if !strings.HasPrefix(top, "╭") || !strings.HasSuffix(strings.TrimSpace(top), "╮") {
		t.Errorf("proposal top border is not the inline permission frame: %q", top)
	}
	if strings.Contains(out, "\n   ╭") {
		t.Errorf("proposal box is centered rather than inline:\n%s", out)
	}
	// The same cursor marker the approval dialog uses.
	if !strings.Contains(out, "❯ ") {
		t.Errorf("proposal does not use the permission cursor:\n%s", out)
	}
}

// TestProposalSurfaceMatchesToolApproval compares the proposal and a tool
// approval directly: both use the same rounded frame, cursor, and numbered
// choices, and both are inline. Only the legend and the decision data differ.
func TestProposalSurfaceMatchesToolApproval(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)
	typeAndEnter(p, proposalText)

	proposalOut := stripANSI(Render(p.state))
	toolOut := stripANSI(Render(approvalState(100, 30, true)))

	for name, out := range map[string]string{"proposal": proposalOut, "tool approval": toolOut} {
		if line := approvalLineWith(t, out, "╭"); !strings.HasPrefix(line, "╭") {
			t.Errorf("%s frame is not inline: %q", name, line)
		}
		if !strings.Contains(out, "❯ ") {
			t.Errorf("%s does not use the permission cursor", name)
		}
		if !strings.Contains(out, "│") {
			t.Errorf("%s does not use the permission side frame", name)
		}
	}
	if !strings.Contains(proposalOut, proposalTitle) {
		t.Errorf("proposal lost the permission legend:\n%s", proposalOut)
	}
	if strings.Contains(proposalOut, approvalTitle) {
		t.Errorf("proposal claimed to be a tool approval:\n%s", proposalOut)
	}
}

// TestProposalNavigationUsesUpDownEnterEsc pins the original interaction:
// arrows move without wrapping, Enter confirms, Esc cancels.
func TestProposalNavigationUsesUpDownEnterEsc(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)
	typeAndEnter(p, proposalText)

	last := len(p.state.Pending.Choices) - 1
	if p.state.Pending.Cursor != last {
		t.Fatalf("cursor = %d, want the conservative default %d", p.state.Pending.Cursor, last)
	}
	press(p, tea.KeyMsg{Type: tea.KeyDown})
	if p.state.Pending.Cursor != last {
		t.Error("down wrapped off the last choice")
	}
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Pending.Cursor != last-1 {
		t.Errorf("up moved to %d, want %d", p.state.Pending.Cursor, last-1)
	}
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Pending.Cursor != 0 {
		t.Errorf("cursor = %d, want the first choice", p.state.Pending.Cursor)
	}
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Pending.Cursor != 0 {
		t.Error("up moved past the first choice")
	}

	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Pending != nil {
		t.Fatal("Enter left the proposal open")
	}
	if cmd == nil {
		t.Fatal("Enter did not release the turn")
	}
	p.mu.Lock()
	res := p.proposal
	p.mu.Unlock()
	if res == nil || res.Option != workspace.ProposalOptionOneFile {
		t.Errorf("recorded decision = %+v, want the first option", res)
	}
}

// TestProposalEscCancelsWithoutRecordingOrRunning pins the cancel contract:
// Esc closes the permission surface, records nothing, and schedules nothing.
func TestProposalEscCancelsWithoutRecordingOrRunning(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)
	typeAndEnter(p, proposalText)

	cmd := press(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Pending != nil {
		t.Error("Esc left the proposal open")
	}
	if cmd != nil {
		t.Error("Esc scheduled the turn it was supposed to cancel")
	}
	p.mu.Lock()
	res := p.proposal
	p.mu.Unlock()
	if res != nil {
		t.Errorf("Esc recorded a decision: %+v", res)
	}
	if p.state.Input != proposalText {
		t.Errorf("input = %q, want the typed request preserved", p.state.Input)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Errorf("provider requests = %d, want 0 after cancelling", got)
	}
}

// TestProposalDecisionReachesExecutionAsTrustedState proves the human's choice
// becomes structured control-plane state and reaches the model as a
// human-authored block, while the user's message is untouched.
func TestProposalDecisionReachesExecutionAsTrustedState(t *testing.T) {
	var requests int32
	var systems [][]ai.SystemPromptBlock
	var messages [][]ai.Message
	o := newCountingOwner("BUILD")
	p := NewProgram(capturingLoop(t, &requests, &systems, &messages), "test", 0)
	o.install(p, proposalPredict())
	p.state.Status.Workspace = "BUILD"

	typeAndEnter(p, proposalText)
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("resolving the proposal did not run the turn")
	}
	cmd()

	p.mu.Lock()
	res := p.proposal
	p.mu.Unlock()
	if res == nil {
		t.Fatal("no proposal decision was recorded")
	}
	if res.Option != workspace.ProposalOptionOneFile ||
		res.Label != "One file" ||
		res.Dimension != workspace.ProposalStructure ||
		res.Request != proposalText ||
		!strings.Contains(res.Subject, "portfolio site") ||
		!strings.Contains(res.Directive, "small portfolio site") {
		t.Errorf("decision = %+v, want structured request-derived state", res)
	}

	var block string
	for _, sys := range systems {
		for _, b := range sys {
			if strings.Contains(b.Text, "decided by the human") {
				block = b.Text
			}
		}
	}
	if block == "" {
		t.Fatalf("no human-decision system block reached the provider: %+v", systems)
	}
	for _, want := range []string{"One file", "one self-contained file", "small portfolio site"} {
		if !strings.Contains(block, want) {
			t.Errorf("system block %q lost %q", block, want)
		}
	}
	for _, batch := range messages {
		for _, msg := range batch {
			if user, ok := msg.(ai.UserMessage); ok {
				if user.Text() != proposalText {
					t.Errorf("the user message was rewritten: %q", user.Text())
				}
			}
		}
	}
}

// TestProposalDelegationAddsNoConstraint proves the delegating choice runs the
// turn with no structural directive: the human explicitly granted the
// decision, so there is nothing to convey.
func TestProposalDelegationAddsNoConstraint(t *testing.T) {
	var requests int32
	var systems [][]ai.SystemPromptBlock
	var messages [][]ai.Message
	o := newCountingOwner("BUILD")
	p := NewProgram(capturingLoop(t, &requests, &systems, &messages), "test", 0)
	o.install(p, proposalPredict())
	p.state.Status.Workspace = "BUILD"

	typeAndEnter(p, proposalText)
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("resolving the proposal did not run the turn")
	}
	cmd()

	for _, sys := range systems {
		for _, b := range sys {
			if strings.Contains(b.Text, "decided by the human") {
				t.Errorf("a delegated choice still constrained execution: %q", b.Text)
			}
		}
	}
	p.mu.Lock()
	res := p.proposal
	p.mu.Unlock()
	if res == nil || res.Option != workspace.ProposalOptionModelDecides {
		t.Errorf("decision = %+v, want the recorded delegation", res)
	}
}

// TestProposalQuestionVariesAtTheUISurface proves the rendered question comes
// from the proposal data, not from the surface: two requests render two
// different questions through the same dialog.
func TestProposalQuestionVariesAtTheUISurface(t *testing.T) {
	requests := []string{
		proposalText,
		"Add support for this new configuration format.",
	}
	questions := map[string]bool{}
	for _, request := range requests {
		var count int32
		p, _ := newProposalProgram(t, &count)
		typeAndEnter(p, request)
		if p.state.Pending == nil || p.state.Pending.Proposal == nil {
			t.Fatalf("%q did not open the proposal surface", request)
		}
		question := p.state.Pending.Proposal.Question
		out := stripANSI(Render(p.state))
		if !strings.Contains(out, question) {
			t.Errorf("%q surface does not render its own question %q:\n%s", request, question, out)
		}
		if question == "How should this be structured?" {
			t.Errorf("%q rendered the fixed template", request)
		}
		questions[question] = true
	}
	if len(questions) != len(requests) {
		t.Fatalf("distinct rendered questions = %d, want %d", len(questions), len(requests))
	}
}

// TestStandingProposalSkipsReproposal proves a resolved choice is not asked
// twice: resubmitting the same request starts the turn directly.
func TestStandingProposalSkipsReproposal(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)

	typeAndEnter(p, proposalText)
	cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("resolving the proposal did not run the turn")
	}
	cmd()
	p.state.Apply(InputChanged{Text: ""})

	cmd = typeAndEnter(p, proposalText)
	if p.state.Pending != nil {
		t.Fatal("a resolved proposal was re-proposed")
	}
	if cmd == nil {
		t.Fatal("the resolved turn was not scheduled")
	}
}

// TestNewSessionDropsStandingProposal proves session isolation: adopting or
// attaching a session clears the recorded choice and closes any open proposal.
func TestNewSessionDropsStandingProposal(t *testing.T) {
	var requests int32
	p, _ := newProposalProgram(t, &requests)

	typeAndEnter(p, proposalText)
	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	p.mu.Lock()
	if p.proposal == nil {
		p.mu.Unlock()
		t.Fatal("no decision was recorded")
	}
	p.mu.Unlock()

	p.adoptSession(nil, OpenedSession{})
	p.mu.Lock()
	adopted := p.proposal
	p.mu.Unlock()
	if adopted != nil {
		t.Errorf("a new session inherited the decision: %+v", adopted)
	}
	if p.state.Pending != nil {
		t.Errorf("a new session inherited the open proposal: %+v", p.state.Pending)
	}

	typeAndEnter(p, proposalText)
	press(p, tea.KeyMsg{Type: tea.KeyEnter})
	p.AttachSession(nil, nil, nil)
	p.mu.Lock()
	attached := p.proposal
	p.mu.Unlock()
	if attached != nil {
		t.Errorf("an attached session inherited the decision: %+v", attached)
	}
}

// TestContinuationReusesStandingProposal proves the decision serves the
// execution it released and explicit continuations of it, then drops.
func TestContinuationReusesStandingProposal(t *testing.T) {
	var requests int32
	var systems [][]ai.SystemPromptBlock
	var messages [][]ai.Message
	o := newCountingOwner("BUILD")
	p := NewProgram(capturingLoop(t, &requests, &systems, &messages), "test", 0)
	o.install(p, proposalPredict())
	p.state.Status.Workspace = "BUILD"

	typeAndEnter(p, proposalText)
	press(p, tea.KeyMsg{Type: tea.KeyUp})
	if cmd := press(p, tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("resolving the proposal did not run the turn")
	} else {
		cmd()
	}
	systems = nil

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("continue") }()
	collectTurn(t, p)
	<-done

	constrained := false
	for _, sys := range systems {
		for _, b := range sys {
			if strings.Contains(b.Text, "decided by the human") {
				constrained = true
			}
		}
	}
	if !constrained {
		t.Error("an explicit continuation lost the recorded decision")
	}

	systems = nil
	done = make(chan struct{})
	go func() { defer close(done); p.runTurn("explain how the build works") }()
	collectTurn(t, p)
	<-done

	for _, sys := range systems {
		for _, b := range sys {
			if strings.Contains(b.Text, "decided by the human") {
				t.Errorf("an unrelated turn inherited the decision: %q", b.Text)
			}
		}
	}
}
