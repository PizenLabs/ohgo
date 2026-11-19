package task

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

func informationalIntent(objective string) contract.IntentContract {
	return contract.IntentContract{Objective: objective, Kind: contract.ObjectiveInformational}
}

// TurnRole is the restore target for a continuation. It is bounded by the
// persisted contract in both directions: a role the contract never authorized
// is not restored, and a granted capability is not dropped because the ambient
// session happened to be narrower or wider.
func TestTurnRoleIsBoundedByThePersistedContract(t *testing.T) {
	cases := []struct {
		name     string
		role     workspace.Workspace
		contract workspace.ExecutionContract
		want     workspace.Workspace
	}{
		{
			name:     "fuzz task restores fuzz",
			role:     workspace.Fuzz,
			contract: workspace.ContractFor(workspace.Fuzz),
			want:     workspace.Fuzz,
		},
		{
			name:     "an unrecorded build role is not restored over a fuzz contract",
			role:     workspace.Build,
			contract: workspace.ContractFor(workspace.Fuzz),
			want:     workspace.Fuzz,
		},
		{
			name:     "a missing role derives the contract origin",
			role:     "",
			contract: workspace.ContractFor(workspace.Build),
			want:     workspace.Build,
		},
		{
			name:     "a read-only task restores ask",
			role:     workspace.Ask,
			contract: workspace.ContractFor(workspace.Ask),
			want:     workspace.Ask,
		},
		{
			name:     "a human-granted widening is restored",
			role:     workspace.Build,
			contract: workspace.ContractFor(workspace.Ask).Authorize(workspace.Build),
			want:     workspace.Build,
		},
		{
			name:     "a narrowing role is honored without losing the grant",
			role:     workspace.Ask,
			contract: workspace.ContractFor(workspace.Build),
			want:     workspace.Ask,
		},
		{
			name:     "a role with no contract is the only evidence",
			role:     workspace.Fuzz,
			contract: workspace.ExecutionContract{},
			want:     workspace.Fuzz,
		},
		{
			name:     "an unresolved task stays unresolved",
			role:     "",
			contract: workspace.ExecutionContract{},
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := Task{ActiveRole: tc.role, ExecutionContract: tc.contract}
			if got := task.TurnRole(); got != tc.want {
				t.Fatalf("TurnRole() = %q, want %q (contract %s)", got, tc.want, tc.contract.String())
			}
		})
	}
}

// A checkpoint whose recorded role is wider than its contract cannot be a
// truthful authority record. Load repairs the role to the granted authority and
// reports the repair instead of restoring the escalation.
func TestLoadRepairsAnUnauthorizedRoleAndReportsIt(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	now := time.Unix(1000, 0).UTC()
	intent := informationalIntent("investigate the bottleneck")
	recorded := Task{
		ID:                "task-authority-repair",
		Objective:         intent.Objective,
		Status:            StatusPaused,
		Contract:          &intent,
		CreatedAt:         now,
		UpdatedAt:         now,
		ActiveRole:        workspace.Build,
		ExecutionContract: workspace.ContractFor(workspace.Fuzz),
	}
	progress := ProgressFrom(recorded)
	progress.CheckpointAt = now
	if err := store.SaveCheckpoint(recorded, progress); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := snap.Task.ActiveRole; got != workspace.Fuzz {
		t.Fatalf("repaired role = %s, want the granted FUZZ", got)
	}
	if snap.Task.TurnRole() != workspace.Fuzz {
		t.Fatalf("TurnRole = %s, want FUZZ", snap.Task.TurnRole())
	}
	if snap.Recovery.State != RecoveryReconstructed {
		t.Fatalf("recovery = %s (%s), want RECONSTRUCTED", snap.Recovery.State, snap.Recovery.Detail)
	}
	if !strings.Contains(snap.Recovery.Detail, "authority") {
		t.Fatalf("recovery detail = %q, want the authority repair named", snap.Recovery.Detail)
	}
	if !strings.Contains(snap.Task.ContextWarning, "authority") {
		t.Fatalf("context warning = %q, want the repair surfaced", snap.Task.ContextWarning)
	}
	// The contract is the grant; the repair must not have widened it.
	if snap.Task.ExecutionContract.Mutate {
		t.Fatalf("the repair widened the contract: %s", snap.Task.ExecutionContract.String())
	}
}

// Begin records the role a turn actually runs under. A live role the contract
// never authorized must fail loudly instead of persisting an escalation the
// next resume would then restore.
func TestBeginRefusesAnUnauthorizedLiveRole(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := informationalIntent("investigate the bottleneck")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin FUZZ: %v", err)
	}

	role = workspace.Build
	if _, err := controller.Begin(intent, nil); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatalf("Begin as an unauthorized BUILD = %v, want ErrAuthorityConflict", err)
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the refused turn removed the task")
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("refused Begin recorded role = %s, want the persisted FUZZ", current.ActiveRole)
	}
	if current.ExecutionContract.Mutate {
		t.Fatalf("refused Begin widened the contract: %s", current.ExecutionContract.String())
	}

	// A narrowing role is always permitted: ASK may read a task whose contract
	// grants execution, and the grant survives the turn.
	role = workspace.Ask
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin as a narrowing ASK: %v", err)
	}
	current, _ = controller.Current()
	if current.ActiveRole != workspace.Ask {
		t.Fatalf("recorded role = %s, want ASK", current.ActiveRole)
	}
	if !current.ExecutionContract.Execute {
		t.Fatalf("narrowing dropped the execution grant: %s", current.ExecutionContract.String())
	}
}
