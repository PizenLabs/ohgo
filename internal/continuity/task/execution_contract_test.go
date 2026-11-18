package task

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The origin is captured at the master task's creation and survives a role
// handoff and a checkpoint round trip. The current role never becomes the
// origin.
func TestTaskOriginSurvivesAutomaticRoleChangeAndResume(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	role := workspace.Build
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	intent := testIntent("fix the checkout failure")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	created, ok := controller.Current()
	if !ok {
		t.Fatal("the task was not created")
	}
	if created.Origin() != workspace.Build || !created.ExecutionContract.Mutate {
		t.Fatalf("created contract = %s, want an authorized BUILD origin", created.ExecutionContract)
	}

	role = workspace.Fuzz
	if err := controller.RecordAutomaticSwitch(workspace.Build, workspace.Fuzz, "investigate the failure", ""); err != nil {
		t.Fatalf("RecordAutomaticSwitch: %v", err)
	}
	current, _ := controller.Current()
	if current.Origin() != workspace.Build || !current.ExecutionContract.Mutate {
		t.Fatalf("contract after BUILD -> FUZZ = %s, want the BUILD grant preserved", current.ExecutionContract)
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want FUZZ", current.ActiveRole)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Task.Origin() != workspace.Build || !loaded.Task.ExecutionContract.Mutate {
		t.Fatalf("reloaded contract = %s, want the persisted BUILD grant", loaded.Task.ExecutionContract)
	}
	if loaded.Task.ActiveRole != workspace.Fuzz {
		t.Fatalf("reloaded active role = %s, want FUZZ", loaded.Task.ActiveRole)
	}
}

// A human-approved widening keeps the original origin and adds the approved
// capability. An ASK task that was allowed to change the repository is still
// recorded as ASK-origin, which is what keeps the protected boundary truthful.
func TestApprovedWideningKeepsOriginAndAddsAuthority(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	role := workspace.Ask
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	intent := testIntent("understand the checkout failure")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	created, _ := controller.Current()
	if created.Origin() != workspace.Ask || created.ExecutionContract.Execute {
		t.Fatalf("created contract = %s, want a read-only ASK origin", created.ExecutionContract)
	}

	handoff, err := controller.ProposeHandoff(HandoffInput{
		To:     workspace.Build,
		Reason: "the fix needs repository mutation authority",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	// The approval does not move authority by itself; the host does, then the
	// next turn applies the approved record.
	role = workspace.Build
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin after approval: %v", err)
	}
	current, _ := controller.Current()
	if current.Origin() != workspace.Ask {
		t.Fatalf("origin = %s, want the immutable ASK origin", current.Origin())
	}
	if !current.ExecutionContract.Execute || !current.ExecutionContract.Mutate {
		t.Fatalf("contract = %s, want the human-approved BUILD capability", current.ExecutionContract)
	}
}

// A narrowing command changes the live role only. The capability a human
// granted remains in the contract, so a later return to the execution side does
// not have to re-derive authority from the current role.
func TestExplicitNarrowingPreservesGrantedAuthority(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	role := workspace.Build
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	if _, err := controller.Begin(testIntent("fix the checkout failure"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	role = workspace.Ask
	if err := controller.RecordWorkspaceSwitch(workspace.Build, workspace.Ask, ""); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}
	current, _ := controller.Current()
	if current.Origin() != workspace.Build || !current.ExecutionContract.Mutate {
		t.Fatalf("narrowed contract = %s, want origin BUILD and mutation preserved", current.ExecutionContract)
	}

	role = workspace.Build
	if err := controller.RecordWorkspaceSwitch(workspace.Ask, workspace.Build, ""); err != nil {
		t.Fatalf("RecordWorkspaceSwitch back: %v", err)
	}
	current, _ = controller.Current()
	if current.Origin() != workspace.Build || !current.ExecutionContract.Mutate {
		t.Fatalf("restored contract = %s, want the same origin and grant", current.ExecutionContract)
	}
}

// An explicit FUZZ choice grants execution, not mutation. FUZZ-origin tasks
// cannot acquire mutation authority from a role name.
func TestExplicitFuzzChoiceDoesNotGrantMutation(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	role := workspace.Ask
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	if _, err := controller.Begin(testIntent("understand the checkout failure"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	role = workspace.Fuzz
	if err := controller.RecordWorkspaceSwitch(workspace.Ask, workspace.Fuzz, ""); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}
	current, _ := controller.Current()
	if current.Origin() != workspace.Ask {
		t.Fatalf("origin = %s, want ASK", current.Origin())
	}
	if !current.ExecutionContract.Execute || current.ExecutionContract.Mutate {
		t.Fatalf("contract = %s, want execution without mutation", current.ExecutionContract)
	}
}

// A checkpoint written before the execution contract existed has no origin to
// recover. The loader derives the conservative authority the task was running
// under instead of inventing a stronger history or refusing the resume.
func TestLegacyCheckpointDerivesContractFromTheRestoredRole(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	legacy, err := New("investigate the checkout failure")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	legacy.ActiveRole = workspace.Fuzz
	legacy.UpdatedAt = time.Now().UTC()
	legacy.CreatedAt = legacy.UpdatedAt
	progress := ProgressFrom(legacy)
	progress.Version = 3
	progress.ExecutionContract = workspace.ExecutionContract{}
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Task.Origin() != workspace.Fuzz {
		t.Fatalf("derived origin = %s, want FUZZ", loaded.Task.Origin())
	}
	if !loaded.Task.ExecutionContract.Execute || loaded.Task.ExecutionContract.Mutate {
		t.Fatalf("derived contract = %s, want execution without mutation", loaded.Task.ExecutionContract)
	}
}

// A capability without a persistent origin is malformed authority evidence.
// Saving it would let a later load silently discard the grant and derive a
// different one from whatever role happened to be restored.
func TestExecutionContractRequiresAnOrigin(t *testing.T) {
	invalid := Task{
		ID: "task-1", Status: StatusRunning,
		ExecutionContract: workspace.ExecutionContract{Execute: true, Mutate: true},
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("a contract with capabilities and no origin validated")
	}
}
