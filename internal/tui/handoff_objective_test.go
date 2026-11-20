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

// Only a self-sufficient executable request approves a new objective. A
// question or an acknowledgement approves nothing new, and a follow-up that
// names no target or check of its own ("fix it", "implement the fix") approves
// the task's existing objective: adopting its ambiguous contract would replace
// a verifiable boundary with a targetless one.
func TestApprovedTransitionObjectiveOnlyTakesExecutableRequests(t *testing.T) {
	cases := map[string]bool{
		"ok, convert this project to Next.js help me": true,
		"implement the fix":                           false,
		"fix it":                                      false,
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

// Approving a workspace move for an informational request is not a decision to
// continue the open task. The observed failure: a NEEDS_REVIEW ".q" task and a
// new "check this project" question; the approved BUILD -> ASK handoff attached
// the question to ".q", so the run executed the old contract and was briefed to
// resume work the user never asked to continue.
func TestHumanApprovalOfAnInformationalRequestDoesNotAttachIt(t *testing.T) {
	const question = "check this project and analyze what this project is about?"
	current := TaskResume{
		Status:                   "NEEDS_REVIEW",
		Objective:                ".q",
		Role:                     "ask",
		HandoffStatus:            "APPROVED",
		HandoffTo:                "ask",
		HandoffBy:                "human",
		HandoffApprovedObjective: "",
		HasContract:              true,
		Contract: contract.IntentContract{
			Objective: ".q",
			Kind:      contract.ObjectiveInformational,
		},
	}
	next := workspace.BuildIntentContract(question)
	if decision, owned := taskTurnDecision(current, question, "ask", next); owned {
		t.Fatalf("an unrelated informational request attached to the open task: decision=%q", decision)
	}

	// The same objective still continues through the approval: the turn is the
	// task's own work under the newly approved authority.
	same := current
	same.Objective = question
	same.Contract.Objective = question
	if decision, owned := taskTurnDecision(same, question, "ask", next); !owned || decision != "HANDOFF_CONTINUATION" {
		t.Fatalf("the task's own objective did not continue: decision=%q owned=%t", decision, owned)
	}
}

// End-to-end regression for the observed continuation failure: a NEEDS_REVIEW
// ".q" task under ASK, an applied human BUILD -> ASK switch, then the unrelated
// question "check this project...". The question must start a new task; before
// the fix it attached to ".q", ran the old informational contract, and the turn
// briefing told the model to continue from ".q"'s checkpoint.
func TestInformationalQuestionAfterAHumanSwitchStartsANewTask(t *testing.T) {
	role := workspace.Build
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	started, err := controller.Begin(workspace.BuildIntentContract(".q"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("intent grounding: unsupported runtime claim"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	// The accepted switch moves the live authority and records the applied
	// human transition on the open task.
	role = workspace.Ask
	if err := controller.RecordWorkspaceSwitch(workspace.Build, workspace.Ask, ""); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}

	var runtimeState string
	stream := func(_ context.Context, req ai.Request) (*ai.Stream, error) {
		if last := req.Messages[len(req.Messages)-1]; last != nil {
			if state, ok := last.(ai.RuntimeStateMessage); ok {
				runtimeState = state.Text
			}
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "GoCommerce is a Go e-commerce app."}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  runtimeCaps(&runtimeBashTool{}),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetTaskHooks(runtimeHooks(controller))

	const question = "check this project and analyze what this project is about?"
	program.runTurn(question)

	current, ok := controller.Current()
	if !ok {
		t.Fatal("the question recorded no task")
	}
	if current.ID == started.ID {
		t.Fatalf("the question continued the stale task %q instead of starting a new one", started.ID)
	}
	if current.Objective != question {
		t.Fatalf("new task objective = %q, want the question", current.Objective)
	}
	// The stale task's orientation must not reach the model: the resume
	// directive is the observable instruction the observed run followed.
	if strings.Contains(runtimeState, "Continue this task from the recorded checkpoint") ||
		strings.Contains(runtimeState, ".q") {
		t.Fatalf("the new turn carried the stale task directive:\n%s", runtimeState)
	}
}
