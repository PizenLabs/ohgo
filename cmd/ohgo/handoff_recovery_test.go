package main

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// installRecovery runs the production startup boundary for one process
// lifetime: the controller is created, the live role is installed, and Restore
// plus approved-handoff reconciliation run before any turn can start. Changing
// the role between calls models the authority a restart restored.
func installRecovery(t *testing.T, root string, role *workspace.Workspace) *task.Controller {
	t.Helper()
	return installTaskContinuity(tui.NewProgram(nil, "test", 0), root, []agent.Tool{tools.NewTodoTool()}, func() workspace.Workspace {
		return *role
	})
}

func recoveryIntent(objective string) contract.IntentContract {
	return contract.IntentContract{Objective: objective, Kind: contract.ObjectiveInformational}
}

// approveHandoff records a FUZZ -> BUILD transition on a live FUZZ task.
func approveHandoff(t *testing.T, controller *task.Controller) (task.Handoff, task.Task) {
	t.Helper()
	started, err := controller.Begin(recoveryIntent("fix the flaky test"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(task.HandoffInput{
		To: workspace.Build, Reason: "needs mutation", Summary: "carry the reproduction",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	return handoff, started
}

// A crash after the authority switch and before APPLIED was persisted is
// reconciled at startup from the restored target workspace, without switching
// again and without resuming a turn.
func TestStartupReconcilesHandoffWithRestoredTarget(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := installRecovery(t, root, &role)
	_, started := approveHandoff(t, controller)

	// The switch succeeded; the process stopped before Begin recorded it.
	role = workspace.Build
	recovered := installRecovery(t, root, &role)
	current, ok := recovered.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	if current.ID != started.ID {
		t.Fatalf("recovery changed the task identity: %q -> %q", started.ID, current.ID)
	}
	if current.Handoff == nil || current.Handoff.Status != task.HandoffApplied {
		t.Fatalf("handoff = %+v, want APPLIED", current.Handoff)
	}
	if !current.Handoff.Consumed {
		t.Fatal("recovery left the approval able to continue an unrelated next prompt")
	}
	if current.ActiveRole != workspace.Build {
		t.Fatalf("active role = %s, want the restored BUILD", current.ActiveRole)
	}
	if resume := taskResume(current); !resume.HandoffConsumed || resume.HandoffStatus != string(task.HandoffApplied) {
		t.Fatalf("resume projection = %+v, want APPLIED and consumed", resume)
	}
}

// A crash before the switch is applied leaves the restored workspace at the
// source. Startup preserves the source role and reports the observed fact.
func TestStartupKeepsSourceWhenHandoffWasNotApplied(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := installRecovery(t, root, &role)
	handoff, started := approveHandoff(t, controller)

	recovered := installRecovery(t, root, &role)
	current, ok := recovered.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	if current.ID != started.ID {
		t.Fatalf("recovery changed the task identity: %q -> %q", started.ID, current.ID)
	}
	if current.Handoff == nil || current.Handoff.Status != task.HandoffInterrupted {
		t.Fatalf("handoff = %+v, want INTERRUPTED", current.Handoff)
	}
	if current.Handoff.From != handoff.From || current.Handoff.To != handoff.To {
		t.Fatalf("recovery rewrote the roles: %+v", current.Handoff)
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want the source FUZZ", current.ActiveRole)
	}
	if !strings.Contains(current.Handoff.Failure, "workspace is still FUZZ") {
		t.Fatalf("failure = %q, want the observed source workspace", current.Handoff.Failure)
	}
}

// A restored workspace that matches neither role is reported as a discrepancy;
// recovery does not rewrite the recorded role or grant authority.
func TestStartupReportsUnrelatedRestoredWorkspace(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := installRecovery(t, root, &role)
	approveHandoff(t, controller)

	role = workspace.Ask
	recovered := installRecovery(t, root, &role)
	current, ok := recovered.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	if current.Handoff == nil || current.Handoff.Status != task.HandoffInterrupted {
		t.Fatalf("handoff = %+v, want INTERRUPTED", current.Handoff)
	}
	for _, want := range []string{"ASK", "FUZZ", "BUILD", "neither"} {
		if !strings.Contains(current.Handoff.Failure, want) {
			t.Fatalf("failure = %q, want %q", current.Handoff.Failure, want)
		}
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want the recorded FUZZ left untouched", current.ActiveRole)
	}
	if err := workspace.Admit(workspace.Ask, workspace.OpExec); err == nil {
		t.Fatal("a recovery discrepancy granted ASK execution authority")
	}
}

// An applied but unconsumed transition survives a restart and is consumed by
// exactly one turn. Re-reading the same state is idempotent.
func TestStartupConsumesAppliedHandoffOnce(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := installRecovery(t, root, &role)
	intent := recoveryIntent("fix the flaky test")
	started, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := controller.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "main; clean"); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}

	role = workspace.Build
	recovered := installRecovery(t, root, &role)
	survived, ok := recovered.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	if survived.Handoff == nil || survived.Handoff.Status != task.HandoffApplied || survived.Handoff.Consumed {
		t.Fatalf("handoff = %+v, want APPLIED and unconsumed", survived.Handoff)
	}
	consumed, err := recovered.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if consumed.ID != started.ID || consumed.ActiveRole != workspace.Build {
		t.Fatalf("consumption = %q/%s, want the same BUILD task", consumed.ID, consumed.ActiveRole)
	}
	if consumed.Handoff == nil || !consumed.Handoff.Consumed {
		t.Fatalf("handoff was not consumed: %+v", consumed.Handoff)
	}

	// The durable state left by a crash during consumption is the same
	// APPLIED/consumed record; another Begin must not re-apply it.
	again := installRecovery(t, root, &role)
	replayed, err := again.Begin(intent, nil)
	if err != nil {
		t.Fatalf("replayed Begin: %v", err)
	}
	if replayed.ID != started.ID || replayed.ActiveRole != workspace.Build {
		t.Fatalf("replay = %q/%s, want the same BUILD task", replayed.ID, replayed.ActiveRole)
	}
	if replayed.Handoff == nil || replayed.Handoff.Status != task.HandoffApplied || !replayed.Handoff.Consumed {
		t.Fatalf("replay changed the record: %+v", replayed.Handoff)
	}
}

// A recovered BUILD workspace with stale FUZZ task metadata must still accept a
// valid BUILD -> FUZZ narrowing; the source comes from the live authority.
func TestRecoveredBUILDAcceptsNarrowingDespiteStaleFUZZMetadata(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := installRecovery(t, root, &role)
	if _, err := controller.Begin(recoveryIntent("fix the flaky test"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	role = workspace.Build
	recovered := installRecovery(t, root, &role)
	stale, ok := recovered.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	if stale.ActiveRole != workspace.Fuzz {
		t.Fatalf("precondition: recorded role = %s, want the stale FUZZ", stale.ActiveRole)
	}
	handoff, err := recovered.ProposeHandoff(task.HandoffInput{
		To: workspace.Fuzz, Reason: "investigate the failure",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff after stale metadata: %v", err)
	}
	if handoff.From != workspace.Build || handoff.To != workspace.Fuzz {
		t.Fatalf("handoff roles = %s -> %s, want BUILD -> FUZZ", handoff.From, handoff.To)
	}
}

// The production startup boundary reconstructs a checkpoint-only recovery and
// keeps a valid approved handoff writable.
func TestStartupRecoversProjectionlessHandoff(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := installRecovery(t, root, &role)
	approveHandoff(t, controller)
	if err := os.Remove(controller.Store().TaskPath()); err != nil {
		t.Fatalf("remove task.md: %v", err)
	}

	recovered := installRecovery(t, root, &role)
	current, ok := recovered.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	if current.Objective != "fix the flaky test" {
		t.Fatalf("objective = %q, want the contract objective", current.Objective)
	}
	if current.Handoff == nil || current.Handoff.Status != task.HandoffInterrupted {
		t.Fatalf("handoff = %+v, want the reconciled INTERRUPTED record", current.Handoff)
	}
	if err := current.Validate(); err != nil {
		t.Fatalf("reconstructed task is not writable: %v", err)
	}
	if _, err := os.Stat(controller.Store().TaskPath()); err != nil {
		t.Fatalf("task.md was not recreated: %v", err)
	}
}

// A rejected or interrupted transition never touches the live gate.
func TestHandoffDispositionsNeverChangeLiveAuthority(t *testing.T) {
	gate := newAuthorityGate(workspace.Fuzz)
	root := t.TempDir()
	live := func() workspace.Workspace { return gate.Workspace() }
	controller := installTaskContinuity(tui.NewProgram(nil, "test", 0), root, []agent.Tool{tools.NewTodoTool()}, live)
	if _, err := controller.Begin(recoveryIntent("fix the flaky test"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	rejected, err := controller.ProposeHandoff(task.HandoffInput{To: workspace.Build, Reason: "try it"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.RejectHandoff(rejected.ID, "not yet"); err != nil {
		t.Fatalf("RejectHandoff: %v", err)
	}
	if gate.Workspace() != workspace.Fuzz {
		t.Fatalf("rejection moved the live gate to %s", gate.Workspace())
	}
	interrupted, err := controller.ProposeHandoff(task.HandoffInput{To: workspace.Build, Reason: "try again"})
	if err != nil {
		t.Fatalf("second ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(interrupted.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	if _, err := controller.ReconcileRecoveredHandoff(workspace.Fuzz); err != nil {
		t.Fatalf("ReconcileRecoveredHandoff: %v", err)
	}
	if gate.Workspace() != workspace.Fuzz {
		t.Fatalf("an interrupted handoff moved the live gate to %s", gate.Workspace())
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("handoff recovery granted FUZZ mutation authority")
	}
}
