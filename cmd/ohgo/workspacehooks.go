package main

import (
	"strings"
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
		style := ws.Style()
		program.PublishWorkspace(tui.WorkspaceUpdated{
			Workspace:       style.Label,
			WorkspaceAccent: style.Accent,
			HasWorkspace:    true,
		})
		// A workspace move rebinds the session's model and effort: the header
		// follows the newly effective role rather than naming a model the loop
		// no longer calls.
		program.PublishModel(held.model.Ref())
		program.PublishEffort(func() string {
			role := held.sessionRole()
			if role == "" {
				role = strings.ToLower(ws.String())
			}
			enabled, level := held.reasoningFor(held.roleEffortFor(role))
			if !enabled {
				return ""
			}
			return level
		}())
		if !automatic {
			program.PublishNotice("switched to " + style.Label)
		}
		return nil
	}
	program.SetWorkspaceSwitcher(tui.WorkspaceHooks{
		Current:          func() string { return held.workspace },
		Switch:           func(name string) error { return switchTo(name, "", "", false) },
		SwitchWithReason: func(name, reason, workspaceState string) error { return switchTo(name, reason, workspaceState, true) },
		PrepareTurn:      func(text string) tui.TurnAuthority { return turnAuthority(held, text) },
		Predict:          func(text string) (tui.TurnTransition, bool) { return turnTransition(held, text) },
		// The tool boundary is the other place a staged task changes phase:
		// the model files a finding (FUZZ) or changes source (BUILD) before it
		// has any reason to ask. The control plane applies its ordinary
		// automatic policy through the same switch owner, so the move is
		// recorded on the same task and the loop re-authorizes the call under
		// the new authority.
		Handoff: func(_ ai.ToolCall, tool agent.Tool, args map[string]any) bool {
			handoffMu.Lock()
			defer handoffMu.Unlock()
			next, ok := automaticToolHandoff(held.activeWorkspace(), tool, args)
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

// automaticToolHandoff decides whether one concrete tool call needs an
// automatic BUILD <-> FUZZ move, and returns the decision when it does.
//
// The artifact kind is the model's own phase declaration; any other tool is
// classified by the invocation's per-call operation. Both are reads of the
// call, never of the model's narration, and both go through the same policy as
// a request. ASK is never crossed automatically: a plan under FUZZ or a write
// under ASK stays where it is and the security boundary refuses it.
func automaticToolHandoff(active workspace.Workspace, tool agent.Tool, args map[string]any) (workspace.TransitionDecision, bool) {
	return workspace.ToolCallTransition(active, tool, args)
}
