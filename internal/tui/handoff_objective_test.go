package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A human approval is a decision about the request that triggered it.
//
// The regression this defends against: ASK investigated a static site and the
// user approved BUILD for "ok, convert this project to Next.js help me". The turn
// attached to the open task, but ran the task's *previous* informational
// contract, so BUILD was shaped as a read-only investigation: no write, no
// command, and an execution request answered with a manual implementation plan.
func TestHumanApprovedHandoffRunsTheApprovedObjective(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	// The open ASK task: an investigation of the current project.
	askIntent := workspace.BuildIntentContract("what is this project?")
	if _, err := controller.Begin(askIntent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	before, _ := controller.Current()

	const approval = "ok, convert this project to Next.js help me"
	approved := approvedTransitionObjective(approval)
	if approved == "" {
		t.Fatalf("the approving request named no executable objective: %q", approval)
	}
	handoff, err := controller.ProposeHandoff(task.HandoffInput{
		To:                workspace.Build,
		Reason:            "the conversion needs mutation authority",
		ApprovedObjective: approved,
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	role = workspace.Build

	var (
		mu     sync.Mutex
		offers [][]string
	)
	stream := func(_ context.Context, req ai.Request) (*ai.Stream, error) {
		mu.Lock()
		names := make([]string, 0, len(req.Tools))
		for _, spec := range req.Tools {
			names = append(names, spec.Name)
		}
		offers = append(offers, names)
		mu.Unlock()
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "converted"}},
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  runtimeCaps(&idleTool{}),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetTaskHooks(runtimeHooks(controller))

	done := make(chan struct{})
	go func() { defer close(done); program.runTurn(approval) }()
	collectTurn(t, program)
	<-done

	mu.Lock()
	captured := append([][]string(nil), offers...)
	mu.Unlock()
	if len(captured) == 0 {
		t.Fatal("the approved turn never reached the model")
	}
	offered := map[string]bool{}
	for _, name := range captured[0] {
		offered[name] = true
	}
	if !offered["write"] {
		t.Fatalf("BUILD was not offered a mutation capability: %v", captured[0])
	}
	if !offered["bash"] {
		t.Fatalf("BUILD was not offered an execution capability: %v", captured[0])
	}

	after, ok := controller.Current()
	if !ok {
		t.Fatal("no task after the approved turn")
	}
	if after.ID != before.ID {
		t.Fatalf("the approval recreated the task: %q -> %q", before.ID, after.ID)
	}
	if after.Objective != approved {
		t.Fatalf("objective = %q, want the approved %q", after.Objective, approved)
	}
	if after.Contract == nil || after.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("contract = %+v, want the approved mutation contract", after.Contract)
	}
}

// Only an executable request approves a new objective. A question or an
// acknowledgement approves nothing new, so the transition preserves the task's
// own objective instead of re-scoping the task to a phrase.
func TestApprovedTransitionObjectiveOnlyTakesExecutableRequests(t *testing.T) {
	cases := map[string]bool{
		"ok, convert this project to Next.js help me": true,
		"implement the fix":                           true,
		"run the tests":                               true,
		"what does this project do?":                  false,
		"ok":                                          false,
	}
	for text, want := range cases {
		got := approvedTransitionObjective(text) != ""
		if got != want {
			t.Errorf("approvedTransitionObjective(%q) named an objective = %t, want %t", text, got, want)
		}
	}
}

// A human approval covers the turn that follows it, and that turn carries its
// own contract. An automatic control-plane handoff never replaces the task
// contract, and neither does a handoff that recorded no new objective.
func TestHumanApprovalCoversTheTurnThatFollowsIt(t *testing.T) {
	const approved = "convert this project to Next.js"
	current := TaskResume{
		Status:                   "RUNNING",
		Objective:                "understand this project",
		Role:                     "ask",
		HandoffStatus:            "APPROVED",
		HandoffTo:                "build",
		HandoffBy:                "human",
		HandoffApprovedObjective: approved,
		HasContract:              true,
		Contract: contract.IntentContract{
			Objective: "understand this project",
			Kind:      contract.ObjectiveInformational,
		},
	}

	// The turn's contract may be derived from a prepared form of the approved
	// request (a long prompt is compressed before the model sees it), so the
	// decision must not depend on the two texts matching byte for byte.
	prepared := workspace.BuildIntentContract(strings.Join(strings.Split(approved, " "), "\n"))
	decision, owned := taskTurnDecision(current, prepared.Objective, "build", prepared)
	if !owned || decision != "HANDOFF_CONTINUATION" {
		t.Fatalf("the approving turn did not attach: decision=%q owned=%t", decision, owned)
	}
	contractForTurn, ok := handoffContract(current, prepared)
	if !ok {
		t.Fatal("the approving turn did not carry its own contract")
	}
	if contractForTurn.Objective != prepared.Objective {
		t.Fatalf("turn contract objective = %q, want the turn's own %q",
			contractForTurn.Objective, prepared.Objective)
	}

	// An automatic control-plane handoff never re-scopes: its approval came from
	// the policy, not from the human deciding this objective.
	automatic := current
	automatic.HandoffBy = "control-plane"
	if _, ok := handoffContract(automatic, prepared); ok {
		t.Fatal("a control-plane handoff replaced the task contract")
	}
}

// The task layer and the turn gate must agree about where an approval points,
// so a resumed session cannot resurrect the previous contract.
func TestPendingHandoffWithoutApprovedObjectiveKeepsTheTaskContract(t *testing.T) {
	current := TaskResume{
		Status:        "APPLIED",
		Objective:     "fix the flaky test",
		HandoffStatus: "APPLIED",
		HandoffTo:     "build",
		HandoffBy:     "human",
		HasContract:   true,
		Contract: contract.IntentContract{
			Objective: "fix the flaky test",
			Kind:      contract.ObjectiveMutation,
		},
	}
	requested := workspace.BuildIntentContract("run the tests")
	if got, ok := handoffContract(current, requested); ok {
		t.Fatalf("a handoff with no approved objective replaced the contract with %+v", got)
	}
}
