package main

import (
	"sync"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// installWorkspaceHooks wires the TUI to the one workspace owner.
//
// Both entry points use this function so an explicit slash command and a
// control-plane automatic handoff have identical authority, model, session,
// and task-continuity effects. The interface never decides the transition; it
// only calls the decision's target through this seam.
func installWorkspaceHooks(program *tui.Program, held *harness, controller *task.Controller, opts *options) {
	// A tool batch dispatches shared calls concurrently, so two capability
	// requests can reach this owner at once. The transition itself is one
	// authority move and must be serialized.
	var handoffMu sync.Mutex
	// executionContract projects the master task's authority. With no open task
	// the current role is the only authority evidence, and ContractFor derives
	// the same conservative contract a new task would get. This is a read: the
	// task layer never grants a capability by being consulted.
	executionContract := func() workspace.ExecutionContract {
		if controller != nil {
			if current, ok := controller.Current(); ok && current.ExecutionContract.Valid() {
				return current.ExecutionContract
			}
		}
		return held.activeContract()
	}
	// publishAuthority republishes the chrome that describes the live role. A
	// workspace move rebinds the session's model and effort: the header follows
	// the newly effective role rather than naming a model the loop no longer
	// calls. Under Auto the demand policy remains in charge and the next
	// provider request maps the work onto the new role's model; the old raw
	// level is not carried because it may not exist on the new ladder.
	publishAuthority := func(ws workspace.Workspace) {
		style := ws.Style()
		program.PublishWorkspace(tui.WorkspaceUpdated{
			Workspace:       style.Label,
			WorkspaceAccent: style.Accent,
			HasWorkspace:    true,
		})
		program.PublishModel(held.model.Ref())
		program.PublishEffort(held.currentEffort())
	}
	switchTo := func(name, reason, workspaceState string, automatic bool) error {
		from := held.activeWorkspace()
		ws, err := held.SwitchWorkspace(name)
		if err != nil {
			return err
		}
		// A slash command is itself the human's decision; an automatic move is
		// the control plane's decision. Both are recorded as applied
		// transitions so the next turn stays attached to the same task.
		if controller != nil {
			var recordErr error
			if automatic {
				recordErr = controller.RecordAutomaticSwitch(from, ws, reason, workspaceState)
			} else {
				recordErr = controller.RecordWorkspaceSwitch(from, ws, workspaceState)
			}
			if recordErr != nil {
				program.PublishNotice("workspace switched; task continuity was not recorded: " + recordErr.Error())
			}
		}
		opts.setAuthority(ws)
		publishAuthority(ws)
		if !automatic {
			program.PublishNotice("switched to " + ws.Style().Label)
		}
		return nil
	}
	// restoreTaskAuthority aligns the live workspace with the open task's
	// persisted contract. The task layer decides the target (Task.TurnRole);
	// this function only applies it through the one switch owner, so the live
	// gate, the session snapshot, and the task record cannot disagree. It never
	// widens the contract: a role the contract does not grant is refused.
	//
	// It is not a handoff: only the ambient session had drifted, so a handoff
	// record would claim a role change the task never made. The task gets a
	// resume event naming both roles instead.
	restoreTaskAuthority := func() error {
		handoffMu.Lock()
		defer handoffMu.Unlock()
		from, to, moved, recordErr := applyTaskAuthority(held, controller)
		if !moved {
			return recordErr
		}
		opts.setAuthority(to)
		if recordErr != nil {
			program.PublishNotice("workspace restored; task continuity was not recorded: " + recordErr.Error())
		}
		publishAuthority(to)
		program.PublishNotice("authority restored: " + from.Label() + " → " + to.Label() +
			" (the task's contract, not the session default)")
		return nil
	}
	program.SetWorkspaceSwitcher(tui.WorkspaceHooks{
		Current:          func() string { return held.workspace },
		Switch:           func(name string) error { return switchTo(name, "", "", false) },
		SwitchWithReason: func(name, reason, workspaceState string) error { return switchTo(name, reason, workspaceState, true) },
		PrepareTurn:      func(text string) tui.TurnAuthority { return turnAuthority(held, text) },
		Predict: func(text string, taskOwned bool) (tui.TurnTransition, bool) {
			return turnTransition(held, executionContractForTurn(held.activeWorkspace(), taskOwned, executionContract), text)
		},
		ExecutionContract:    executionContract,
		RestoreTaskAuthority: restoreTaskAuthority,
		// The tool boundary is the other place a staged task changes phase:
		// the model files a finding (FUZZ) or changes source (BUILD) before it
		// has any reason to ask. The control plane applies its ordinary
		// automatic policy through the same switch owner, so the move is
		// recorded on the same task and the loop re-authorizes the call under
		// the new authority.
		Handoff: func(_ ai.ToolCall, tool agent.Tool, args map[string]any) bool {
			handoffMu.Lock()
			defer handoffMu.Unlock()
			next, ok := automaticToolHandoff(held.activeWorkspace(), executionContract(), tool, args)
			if !ok {
				return false
			}
			from := held.activeWorkspace()
			if err := switchTo(next.Target.String(), next.Reason, "", true); err != nil {
				program.PublishNotice("workspace: " + err.Error())
				return false
			}
			// A tool-boundary move is as visible as a turn-boundary one: the
			// transcript names the target and the reason before the call runs
			// under the new authority.
			program.PublishNotice("workspace " + from.Label() + " → " +
				next.Target.Label() + ": " + next.Reason)
			return true
		},
	})
}

// applyTaskAuthority moves the live workspace to the authority the open task's
// persisted contract grants.
//
// It is the decision half of the turn-boundary restore, kept separate from the
// TUI chrome so the host boundary can be exercised without a program. The task
// layer supplies the target; this function only applies it through the one
// switch owner and records the realignment on the same task.
//
// moved reports that the live gate moved. recordErr reports that the move
// happened but the task record did not: the caller must report that, never run
// the turn as if the task and the gate agreed.
func applyTaskAuthority(held *harness, controller *task.Controller) (from, to workspace.Workspace, moved bool, recordErr error) {
	if held == nil || controller == nil {
		return "", "", false, nil
	}
	current, ok := controller.Current()
	if !ok {
		return "", "", false, nil
	}
	target := current.TurnRole()
	if !target.Valid() {
		return "", "", false, nil
	}
	from = held.activeWorkspace()
	if from == target {
		return from, target, false, nil
	}
	to, err := held.SwitchWorkspace(target.String())
	if err != nil {
		return from, target, false, err
	}
	return from, to, true, controller.RecordAuthorityRestore(from, to)
}

// executionContractForTurn selects the authority evidence a transition
// prediction may consult.
//
// Only a prompt the open task owns may use the task's persistent contract. A
// genuinely new objective starts a new task under the live role, so inheriting
// a previous task's grant would let one task authorize another.
func executionContractForTurn(active workspace.Workspace, taskOwned bool, taskContract func() workspace.ExecutionContract) workspace.ExecutionContract {
	if taskOwned && taskContract != nil {
		if contract := taskContract(); contract.Valid() {
			return contract
		}
	}
	return workspace.ContractFor(active)
}

// automaticToolHandoff decides whether one concrete tool call needs an
// automatic BUILD <-> FUZZ move, and returns the decision when it does.
//
// The artifact kind is the model's own phase declaration; any other tool is
// classified by the invocation's per-call operation. Both are reads of the
// call, never of the model's narration, and both go through the same policy as
// a request. ASK is never crossed automatically: a plan under FUZZ or a write
// under ASK stays where it is and the security boundary refuses it.
func automaticToolHandoff(active workspace.Workspace, contract workspace.ExecutionContract, tool agent.Tool, args map[string]any) (workspace.TransitionDecision, bool) {
	return workspace.ToolCallTransition(active, contract, tool, args)
}
