package tui

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// "Run it" after "run this project" asks for the action again. Treating it as a
// verification made the runtime answer "already proven" on a finished task and
// made an open task abort with an objective conflict, so the request that was
// supposed to prove the runtime never reached execution.
func TestRunItIsAnExecutionFollowUpNotVerification(t *testing.T) {
	// The workspace classifier must not call the launch imperative a check.
	if workspace.IsVerificationContinuation("run it") {
		t.Fatal("run it was classified as a verification continuation")
	}
	if !workspace.IsVerificationContinuation("is it running?") {
		t.Fatal("a question about the running state stopped being verification")
	}

	open := TaskResume{
		ID:          "task-1",
		Status:      string(task.StatusRunning),
		Role:        string(workspace.Build),
		HasContract: true,
		Contract: contract.IntentContract{
			Objective:     "run this project help me",
			Kind:          contract.ObjectiveExecution,
			ExecutionMode: contract.ExecutionRuntime,
		},
	}
	decision, owned := taskTurnDecision(open, "run it", "build", workspace.BuildIntentContract("run it"))
	if !owned || decision != "EXECUTION_FOLLOW_UP" {
		t.Fatalf("decision = %q owned=%v, want EXECUTION_FOLLOW_UP", decision, owned)
	}

	// A finished task must not own the repeated attempt: the new task starts
	// fresh and must produce its own runtime observation.
	p := NewProgram(nil, "test", 0)
	p.task.Current = func() (TaskResume, bool) {
		return TaskResume{
			ID:             "task-1",
			Status:         string(task.StatusCompleted),
			HasContract:    true,
			ObjectiveState: string(task.ObjectiveProven),
			Contract: contract.IntentContract{
				Objective:     "run this project help me",
				Kind:          contract.ObjectiveExecution,
				ExecutionMode: contract.ExecutionRuntime,
			},
		}, true
	}
	if _, refused := p.terminalTaskContinuation("run it"); refused {
		t.Fatal("a finished task refused the repeated execution as a continuation")
	}
	if _, refused := p.terminalTaskContinuation("verify the fix"); !refused {
		t.Fatal("a finished task stopped refusing a redundant verification")
	}

	// A mutation task does not inherit an execution follow-up: "run it" there
	// has no unambiguous runtime object.
	mutation := TaskResume{
		ID:          "task-2",
		Status:      string(task.StatusRunning),
		HasContract: true,
		Contract: contract.IntentContract{
			Objective: "write README.md",
			Kind:      contract.ObjectiveMutation,
		},
	}
	if decision, owned := taskTurnDecision(mutation, "run it", "build", workspace.BuildIntentContract("run it")); owned && decision == "EXECUTION_FOLLOW_UP" {
		t.Fatal("a mutation task took ownership of an execution follow-up")
	}
}
