package main

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// installWorkspaceHooks wires the TUI to the one workspace owner.
//
// Both entry points use this function so an explicit slash command and a
// control-plane automatic handoff have identical authority, model, session,
// and task-continuity effects. The interface never decides the transition; it
// only calls the decision's target through this seam.
func installWorkspaceHooks(program *tui.Program, held *harness, controller *task.Controller, opts *options) {
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
	})
}
