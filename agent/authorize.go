package agent

import (
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/security"
)

// Authorizer is the security boundary the loop consults before execution.
//
// It is the single seam between an LLM proposal and the trusted execution of
// that proposal. The loop calls Evaluate for every tool call; it executes only
// on AUTO, and it obtains a grant for APPROVE through Approve. Because the
// loop — not a UI, not a tool, and not the model — performs that sequence, no
// other path can reach Execute without an authorization decision.
//
// Both methods are implemented by trusted runtime code. The model can never
// satisfy the interface, and a tool cannot: the loop owns the call.
type Authorizer interface {
	// Evaluate classifies one call and applies policy. It must not execute and
	// must not prompt; it returns AUTO, APPROVE, or DENY.
	Evaluate(call ai.ToolCall, tool Tool) security.Decision
	// Approve obtains trusted human authorization for an APPROVE decision and
	// returns the grant it established. It is called only after Evaluate
	// returned APPROVE. ok=false refuses the operation; there is no fallback.
	Approve(call ai.ToolCall, decision security.Decision) (security.Grant, bool)
}

// CapabilityNamer lets a tool name the security capability it exercises when
// its approval tier alone would misclassify it.
//
// A read tier is the default for anything that observes, but a network fetch is
// not a filesystem read; the boundary must see the difference. Tools whose tier
// is accurate implement nothing and are classified from Approval.
type CapabilityNamer interface {
	// SecurityCapability returns the capability every invocation of this tool
	// exercises.
	SecurityCapability() security.Capability
}

// InvocationCapabilityNamer lets a tool refine the security capability for one
// call when trusted configuration distinguishes otherwise identical
// invocations.
//
// The model never supplies the class: the tool resolves it from its own
// configuration. A tool that cannot resolve one must return the conservative
// capability, not a weaker guess.
type InvocationCapabilityNamer interface {
	// SecurityCapabilityFor returns the capability this invocation exercises.
	SecurityCapabilityFor(args map[string]any) security.Capability
}

// SecurityCapabilityFor returns the per-call security capability, falling back
// to the tool's declared capability.
func SecurityCapabilityFor(tool Tool, args map[string]any) (security.Capability, bool) {
	if namer, ok := tool.(InvocationCapabilityNamer); ok {
		return namer.SecurityCapabilityFor(args), true
	}
	if namer, ok := tool.(CapabilityNamer); ok {
		return namer.SecurityCapability(), true
	}
	return "", false
}
