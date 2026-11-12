package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Scenario A: a question in ASK is not a workspace decision. It runs
// read-only, without a picker and without mutation.
func TestAskQuestionStaysAskWithoutWorkspaceDecision(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root)},
		[]ai.AssistantMessage{readinessAnswer("This is a Go project with a small tools package.")})

	cmd := r.program.submit("What is this project about?")
	if cmd == nil {
		t.Fatalf("the ASK question opened a decision: %+v", r.program.state.Transition)
	}
	if r.program.state.Transition != nil {
		t.Fatalf("the ASK question opened a workspace transition: %+v", r.program.state.Transition)
	}
	if r.program.state.Permissions != nil {
		t.Fatalf("the ASK question opened the workspace picker: %+v", r.program.state.Permissions)
	}
	cmd()
	drainProgram(r.program)
	if *r.role != workspace.Ask {
		t.Fatalf("role = %s, want ASK", *r.role)
	}
}

// Scenario D: BUILD may inspect, but an explicit analysis request still asks
// before crossing into the read-only ASK authority. Nothing runs until the
// human decision, and accepting changes no repository source.
func TestBuildAnalysisConfirmsAskBoundary(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("The order/checkout flow is represented by the tools package in this snapshot."),
		})

	if cmd := r.program.submit("Check this project and understand how the order/checkout flow works."); cmd != nil {
		t.Fatalf("BUILD analysis ran before the ASK decision: %+v", r.program.state.Transition)
	}
	pending := r.program.state.Transition
	if pending == nil || pending.Proposed != "ASK" || pending.Automatic {
		t.Fatalf("transition = %+v, want a confirmed ASK boundary", pending)
	}
	if *r.role != workspace.Build {
		t.Fatalf("the pending decision moved authority to %s", *r.role)
	}

	_, cmd := r.program.resolveTransition(true)
	if cmd == nil {
		t.Fatal("accepting the ASK boundary did not release the turn")
	}
	cmd()
	drainProgram(r.program)

	current := r.currentTask()
	if current.Status == task.StatusNeedsReview {
		t.Fatalf("the investigation was flagged as a failed change: %s", current.ReviewReason)
	}
	if current.Completion != nil && current.Completion.Observed {
		t.Fatal("an investigation with no change claimed a proven repository change")
	}
	if *r.role != workspace.Ask {
		t.Fatalf("role = %s, want ASK", *r.role)
	}
}

// Scenario G: a strong human-facing question from an execution workspace asks
// before moving to ASK; accepting moves through the owner, and rejecting keeps
// the request under the capable current workspace.
func TestBuildStrongQuestionConfirmsAskBoundary(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root)},
		[]ai.AssistantMessage{readinessAnswer("The repository is organized by vertical slice.")})

	r.program.state.Apply(InputChanged{Text: "Why is this repository structured this way?"})
	if cmd := r.program.submit("Why is this repository structured this way?"); cmd != nil {
		t.Fatal("the ASK boundary ran before the human decision")
	}
	pending := r.program.state.Transition
	if pending == nil || pending.Proposed != "ASK" {
		t.Fatalf("transition = %+v, want the ASK boundary", pending)
	}
	if pending.Automatic {
		t.Fatal("crossing ASK was marked automatic")
	}
	if *r.role != workspace.Build {
		t.Fatalf("the prompt moved authority to %s", *r.role)
	}

	// Declining keeps BUILD, and the request runs there because BUILD can read.
	_, cmd := r.program.resolveTransition(false)
	if cmd == nil {
		t.Fatal("declining a narrowing decision did not release the turn")
	}
	cmd()
	drainProgram(r.program)
	if *r.role != workspace.Build {
		t.Fatalf("declining moved authority to %s", *r.role)
	}
}

// Scenario B: ASK -> BUILD is a human authority boundary. The request does not
// run until the decision; declining restores it, accepting switches through
// the owner.
func TestAskMutationRequiresBuildConfirmation(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{readinessAnswer("done")})

	r.program.state.Apply(InputChanged{Text: "Fix the checkout validation bug and run the tests."})
	if cmd := r.program.submit("Fix the checkout validation bug and run the tests."); cmd != nil {
		t.Fatal("ASK mutation ran before the human decision")
	}
	pending := r.program.state.Transition
	if pending == nil || pending.Proposed != "BUILD" || pending.Kind != TransitionKindHandoff {
		t.Fatalf("transition = %+v, want an ASK -> BUILD handoff", pending)
	}
	if pending.Automatic {
		t.Fatal("ASK -> BUILD was marked automatic")
	}
	if *r.role != workspace.Ask {
		t.Fatalf("the proposal moved authority to %s", *r.role)
	}

	if _, cmd := r.program.resolveTransition(false); cmd != nil {
		t.Fatal("declining ASK -> BUILD ran the request")
	}
	if *r.role != workspace.Ask {
		t.Fatalf("declining moved authority to %s", *r.role)
	}

	r.program.state.Apply(InputChanged{Text: "Fix the checkout validation bug and run the tests."})
	if cmd := r.program.submit("Fix the checkout validation bug and run the tests."); cmd != nil {
		t.Fatal("the second ASK mutation ran before the decision")
	}
	_, cmd := r.program.resolveTransition(true)
	if cmd == nil {
		t.Fatal("accepting ASK -> BUILD did not release the turn")
	}
	if *r.role != workspace.Build {
		t.Fatalf("accepted handoff left authority at %s", *r.role)
	}
	cmd()
	drainProgram(r.program)
}

// Scenario H and I: a staged conditional request begins in investigation,
// crosses ASK only with confirmation, then continues the same task into BUILD
// when the change is requested. The mutation branch still needs verification
// and proof.
func TestConditionalWorkflowStartsInvestigatingThenContinuesIntoBuild(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("The order/checkout flow is mocked and no backend order path exists in this snapshot."),
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "order/checkout",
				"content": "checkout flow implementation\n",
			}),
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./..."}),
			readinessAnswer("implemented and verified the checkout fix"),
		})

	r.program.state.Apply(InputChanged{Text: ecommerceInvestigationPrompt})
	if cmd := r.program.submit(ecommerceInvestigationPrompt); cmd != nil {
		t.Fatal("the staged ASK request ran before the FUZZ decision")
	}
	pending := r.program.state.Transition
	if pending == nil || pending.Proposed != "FUZZ" || pending.Automatic {
		t.Fatalf("transition = %+v, want a confirmed ASK -> FUZZ decision", pending)
	}
	if r.program.state.Permissions != nil {
		t.Fatalf("the staged request opened the workspace picker: %+v", r.program.state.Permissions)
	}
	if *r.role != workspace.Ask {
		t.Fatalf("the proposal moved authority to %s", *r.role)
	}

	_, cmd := r.program.resolveTransition(true)
	if cmd == nil {
		t.Fatal("the accepted FUZZ handoff did not release the turn")
	}
	cmd()
	drainProgram(r.program)

	investigated := r.currentTask()
	if investigated.ID == "" {
		t.Fatal("the investigation created no durable task")
	}
	if investigated.Status != task.StatusRunning || investigated.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("investigation phase = %s/%s, want RUNNING/ACTIVE (review=%q)",
			investigated.Status, investigated.ObjectiveState(), investigated.ReviewReason)
	}
	if investigated.Completion != nil {
		t.Fatal("the investigation phase recorded a mutation proof")
	}
	if strings.Contains(investigated.ReviewReason, "no change was observed") {
		t.Fatalf("the investigation was reported as a failed change: %s", investigated.ReviewReason)
	}
	if *r.role != workspace.Fuzz {
		t.Fatalf("role = %s, want FUZZ", *r.role)
	}

	// "fix it" refers to the task's own result. FUZZ cannot mutate, so the
	// control plane hands the same task to BUILD automatically.
	cmd = r.program.submit("fix it")
	if cmd == nil {
		t.Fatal("the automatic FUZZ -> BUILD handoff did not release the turn")
	}
	if r.program.state.Transition != nil {
		t.Fatalf("automatic handoff opened a decision surface: %+v", r.program.state.Transition)
	}
	if *r.role != workspace.Build {
		t.Fatalf("role = %s, want BUILD after the automatic handoff", *r.role)
	}
	cmd()
	drainProgram(r.program)

	proven := r.currentTask()
	if proven.ID != investigated.ID {
		t.Fatalf("BUILD recreated the task: %q -> %q", investigated.ID, proven.ID)
	}
	if proven.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("BUILD did not prove the same task: %s (%s)",
			proven.ObjectiveState(), proven.ReviewReason)
	}
	if proven.Completion == nil || len(proven.Completion.Verified) == 0 {
		t.Fatalf("completion proof = %+v, want the post-change verification", proven.Completion)
	}
	if _, ok := proven.Context.Role(workspace.Fuzz); !ok {
		t.Fatal("the investigation role context did not survive the BUILD handoff")
	}
	if _, ok := proven.Context.Role(workspace.Build); !ok {
		t.Fatal("BUILD did not receive a role context")
	}
}

// A conditional investigation that runs directly under BUILD (for example after
// an explicit selection) is still a phase, not a failed mutation.
func TestBuildConditionalInvestigationStaysActiveWithoutMutation(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("The order/checkout flow is mocked and no backend order path exists in this snapshot."),
		})

	r.turnAs(ecommerceInvestigationPrompt, workspace.Build)
	current := r.currentTask()
	if current.Status == task.StatusNeedsReview {
		t.Fatalf("the conditional investigation was flagged as a failed change: %s", current.ReviewReason)
	}
	if current.Status != task.StatusRunning || current.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("conditional investigation = %s/%s, want RUNNING/ACTIVE",
			current.Status, current.ObjectiveState())
	}
	if current.Completion != nil {
		t.Fatal("the investigation phase recorded a proof")
	}
	if strings.Contains(current.ReviewReason, "no change was observed") {
		t.Fatalf("review reason = %q", current.ReviewReason)
	}
}
