package workspace

import "github.com/PizenLabs/ohgo/internal/agent"

// Authority binds a workspace to the agent's tool-admission seam.
//
// The agent loop calls Authority.Admit before approving or executing a call,
// which is what puts the authority check below the UI and above execution (§7,
// I5). The UI never sees this type: it may display the workspace, but it
// cannot grant capability.
type Authority struct {
	// Workspace is the authority in force.
	Workspace Workspace
}

// Admit reports whether the tool call may proceed.
//
// It classifies the invocation as an Operation and consults the workspace
// policy. A tool that refines its capability for this specific call (a shell
// command that writes a file, for example) is honoured through the
// agent.CapabilityReporter interface.
func (a Authority) Admit(tool agent.Tool, args map[string]any) error {
	op := OperationForTool(tool, args)
	if err := Admit(a.Workspace, op); err != nil {
		if denied, ok := err.(*Denied); ok {
			denied.Tool = tool.Name()
		}
		return err
	}
	return nil
}

// OperationForTool classifies a tool invocation.
//
// The declared tier is the default; a tool may override it per call via
// agent.CapabilityReporter, because a single tool can exercise different
// capabilities depending on its arguments (bash runs a build, or rewrites a
// file).
func OperationForTool(tool agent.Tool, args map[string]any) Operation {
	tier := tool.Approval().Tier
	if reporter, ok := tool.(agent.CapabilityReporter); ok {
		tier = reporter.Capability(args)
	}
	return OperationForTier(tier)
}

// OperationForTier maps an agent capability tier to an operation.
//
// An unknown tier maps to an operation the policy denies, so a newly added tier
// is refused by default rather than silently permitted.
func OperationForTier(tier agent.Tier) Operation {
	switch tier {
	case agent.TierRead:
		return OpRead
	case agent.TierExec:
		return OpExec
	case agent.TierWrite:
		return OpWrite
	}
	return Operation("unknown:" + string(tier))
}
