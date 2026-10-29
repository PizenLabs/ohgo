package main

import (
	"context"

	"github.com/PizenLabs/ohgo/exec"
	"github.com/PizenLabs/ohgo/security"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/tui"
)

// attachShellEscape wires the "!" escape to the local shell and to the same
// mutation classifier the bash tool's admission uses.
//
// The runner is rooted at the workspace directory and runs through the
// platform's shell; the classifier is the exported form of the admission rule,
// so the ASK confirmation gate and the runtime authority cannot drift.
func attachShellEscape(program *tui.Program, root string, gate *securityGate) {
	hooks := tui.ShellHooks{
		Run: func(ctx context.Context, command string, onChunk func(string)) (exec.EscapeResult, error) {
			// The escape's execution boundary is the same authority decision
			// that admitted it. A human in ASK or FUZZ may execute, but the
			// command runs read-only there however it is spelled.
			readOnly := false
			if gate != nil {
				readOnly = gate.EvaluateCommand(command).ReadOnly
			}
			return exec.ShellRunner{Dir: root, ReadOnly: readOnly}.Run(ctx, command, onChunk)
		},
		Mutates: tools.CommandMutates,
	}
	if gate != nil {
		hooks.Authorize = func(command string) (bool, bool, string) {
			decision := gate.EvaluateCommand(command)
			switch decision.Outcome {
			case security.OutcomeDeny:
				return false, false, decision.Reason
			case security.OutcomeApprove:
				return true, true, ""
			default:
				return true, false, ""
			}
		}
	}
	program.SetShellEscape(hooks)
}
