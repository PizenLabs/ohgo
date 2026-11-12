package task

import (
	"reflect"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

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
