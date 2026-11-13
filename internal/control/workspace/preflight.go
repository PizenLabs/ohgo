package workspace

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// Capability preflight.
//
// The control plane answers one question before a model turn: can this runtime
// perform the operations the objective requires? It is the boundary that keeps
// a capability mismatch out of the model's hands. Without it, a BUILD turn
// starts, the model proposes a command, the runtime refuses it, and the model
// improvises — spending a turn to discover a fact the runtime already knew.
//
// The answer is deterministic and cheap: the contract says what is needed, the
// constructed tool set says what can be performed, and the live workspace says
// what is permitted. No model call, no repository probe, no second planner.

// PreflightState is the control plane's capability verdict for one turn.
type PreflightState string

// Preflight states.
const (
	// PreflightReady means the runtime can perform everything the contract
	// requires.
	PreflightReady PreflightState = "ready"
	// PreflightCapabilityUnavailable means the objective needs an operation no
	// constructed capability can perform. Retrying the same turn cannot help;
	// the human must change the runtime (enable the tool, change the request)
	// or the objective.
	PreflightCapabilityUnavailable PreflightState = "capability_unavailable"
	// PreflightAuthorityDenied means the objective needs an operation the live
	// authority forbids. It is a different failure from an absent capability:
	// the runtime has the ability and the policy refuses it.
	PreflightAuthorityDenied PreflightState = "authority_denied"
)

// Preflight is the control plane's capability decision for one turn.
//
// It is data, never authority. A Ready verdict grants nothing: every call still
// passes the security boundary, and the authority still decides what may run.
type Preflight struct {
	// Authority is the workspace the turn would run under.
	Authority Workspace
	// Required is every capability the objective needs.
	Required []CapabilityNeed
	// Available is every operation the constructed tool set can perform.
	Available []Operation
	// Denied is the required capabilities the authority forbids.
	Denied []CapabilityNeed
	// Missing is the required capabilities no constructed tool can perform.
	Missing []CapabilityNeed
}

// State is the verdict.
func (p Preflight) State() PreflightState {
	switch {
	case len(p.Denied) > 0:
		return PreflightAuthorityDenied
	case len(p.Missing) > 0:
		return PreflightCapabilityUnavailable
	}
	return PreflightReady
}

// Ready reports whether the turn may start.
func (p Preflight) Ready() bool { return p.State() == PreflightReady }

// Reason explains a non-ready verdict in the user's terms.
//
// It names the missing capability, not a tool: a user decides about a
// capability ("repository mutation"), and a tool name is an implementation
// detail that changes between releases.
func (p Preflight) Reason() string {
	var lines []string
	for _, need := range p.Denied {
		lines = append(lines, need.Label()+" is not permitted by the "+p.Authority.Label()+" workspace")
	}
	for _, need := range p.Missing {
		lines = append(lines, need.Label()+" is not available in this runtime")
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "; ")
}

// PreflightTurn resolves whether the active authority and the constructed tool
// set can serve one contract.
//
// intent must be the contract the turn will actually be judged against: the
// informational phase of an objective the live authority cannot perform yet
// requires nothing beyond reading, and passing the undeferred objective would
// report a block for work the control plane deliberately deferred.
func PreflightTurn(active Workspace, intent IntentContract, tools []agent.Tool) Preflight {
	available := agent.AvailableOperations(tools)
	preflight := Preflight{
		Authority: active,
		Required:  intent.RequiredCapabilities(),
		Available: available,
	}
	for _, need := range preflight.Required {
		if !permitsAny(active, need.Operations) {
			preflight.Denied = append(preflight.Denied, need)
			continue
		}
		// A non-mandatory need (reading) that nothing can serve is an evidence
		// question, not an unreachable objective: the answer can still be
		// produced and reviewed. See CapabilityNeed.Mandatory.
		if need.Mandatory() && !need.SatisfiedBy(available) {
			preflight.Missing = append(preflight.Missing, need)
		}
	}
	return preflight
}

// permitsAny reports whether the authority permits at least one of the
// operations a need accepts.
func permitsAny(active Workspace, operations []Operation) bool {
	if !active.Valid() {
		return false
	}
	for _, op := range operations {
		if active.Permits(op) {
			return true
		}
	}
	return false
}
