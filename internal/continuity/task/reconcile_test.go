package task

import (
	"reflect"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A checkpoint completed before the runtime requirement existed can carry a
// proof that only an environment probe supports. On restore the mode is
// re-derived from the same objective, so the proof stops resolving and the
// task reports COMPLETED_UNPROVEN instead of poisoning the next turn with a
// false PROVEN. This is the durable half of the "run this project" failure: the
// session said completed · proven while no process ever ran.
func TestRestoreDowngradesCommandOnlyProofOfRuntimeObjective(t *testing.T) {
	root := t.TempDir()
	objective := "run this project help me"
	current := workspace.BuildIntentContract(objective)
	if current.ExecutionMode != contract.ExecutionRuntime {
		t.Fatalf("current execution mode = %q, want runtime", current.ExecutionMode)
	}

	// A checkpoint written by the previous classifier: same objective, no
	// runtime requirement.
	stale := current
	stale.ExecutionMode = contract.ExecutionCommand

	controller := NewController(NewStore(root), nil)
	if _, err := controller.Begin(stale, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "probe-1", ToolName: "bash",
		Result: agent.Result{
			Text: "v24.11.0\n11.6.1", Executes: true, Finished: true,
			Details: map[string]any{"command": "node -v && npm -v", "exitCode": 0},
		},
	}}}})
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("legacy completion: %v", err)
	}
	if completed.ObjectiveState() != ObjectiveProven {
		t.Fatalf("setup did not produce the legacy PROVEN state: %s", completed.ObjectiveState())
	}

	restored := NewController(NewStore(root), nil)
	loaded, found, err := restored.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found {
		t.Fatal("checkpoint was not restored")
	}
	if loaded.Contract == nil || loaded.Contract.ExecutionMode != contract.ExecutionRuntime {
		t.Fatalf("restored contract = %+v, want the runtime requirement re-derived", loaded.Contract)
	}
	if loaded.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("objective state = %s, want COMPLETED_UNPROVEN", loaded.ObjectiveState())
	}
	if loaded.Proven() {
		t.Fatal("a command-only proof still reported PROVEN")
	}
}

// Test 6: a checkpoint whose completion contract was derived by an older
// classifier is reconciled against the same user objective before it reaches
// the model. The current deterministic contract wins; the task identity and
// work remain intact. A stale completion proof on a non-terminal task is
// discarded rather than presented as achieved.
func TestControllerReconcilesStaleContractAndCompletionState(t *testing.T) {
	root := t.TempDir()
	objective := "use npx live-server test this project"
	current := workspace.BuildIntentContract(objective)
	if current.Kind != contract.ObjectiveExecution {
		t.Fatalf("current contract kind = %q, want execution", current.Kind)
	}

	stale := current
	stale.Kind = contract.ObjectiveInformational
	stale.Verification = nil
	stale.Ambiguous = false

	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(stale, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.task.Completion = &CompletionProof{
		Verdict: "COMPLETED",
		At:      time.Now().UTC(),
	}
	if err := controller.persist(); err != nil {
		t.Fatalf("persist stale checkpoint: %v", err)
	}

	restored := NewController(NewStore(root), nil)
	loaded, found, err := restored.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found {
		t.Fatal("checkpoint was not restored")
	}
	if loaded.Contract == nil {
		t.Fatal("restored task has no contract")
	}
	if loaded.Contract.Kind != contract.ObjectiveExecution {
		t.Fatalf("restored kind = %q, want the current execution contract", loaded.Contract.Kind)
	}
	if !reflect.DeepEqual(*loaded.Contract, current) {
		t.Fatalf("restored contract was not the current deterministic contract:\n got %+v\nwant %+v",
			*loaded.Contract, current)
	}
	if loaded.Completion != nil {
		t.Fatal("a non-terminal task still carried a stale completion proof")
	}
	if restored.Recovery().State != RecoveryReconstructed {
		t.Fatalf("recovery state = %q, want RECONSTRUCTED", restored.Recovery().State)
	}

	// Repeating the same objective now continues the identical task rather
	// than failing as an objective conflict.
	continued, err := restored.Begin(current, nil)
	if err != nil {
		t.Fatalf("Begin after reconciliation: %v", err)
	}
	if continued.ID != loaded.ID {
		t.Fatalf("task identity changed across reconciliation: %q -> %q", loaded.ID, continued.ID)
	}
	if continued.Contract == nil || continued.Contract.Kind != contract.ObjectiveExecution {
		t.Fatalf("continued contract = %+v", continued.Contract)
	}
}
