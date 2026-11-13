package cognition

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Capability preflight inside the projection.
//
// The workspace preflight decides before a task starts. This one is the
// per-request invariant: every model request is built from a task shape that
// names the operations the contract needs, and the request is only allowed to
// run when the constructed tool set can perform them. It is what makes "the
// model discovers it cannot act" structurally impossible rather than merely
// unlikely.
//
// The check reads the constructed tool set, not the narrowed candidate surface.
// Candidates narrow for task shape, stage, and budget; those are decisions that
// legitimately withdraw a tool from one turn (an answer is due, the grant is
// spent, the objective is complete). Availability is a fact about the runtime,
// so it must not vary with the stage or the budget: only a genuinely absent
// capability blocks the task.

// CapabilityGap returns the required capabilities the runtime cannot perform.
//
// It answers availability only, never authority: whether the live workspace may
// perform an operation is the workspace gate's decision and the deferral rule's
// (PhaseContract), both of which the host applies before it builds a shape. A
// need the authority forbids therefore arrives here already projected as its
// informational phase, and only a genuinely absent capability remains a gap.
func CapabilityGap(shape TaskShape, available []contract.Operation) []contract.CapabilityNeed {
	if len(shape.RequiredCapabilities) == 0 {
		return nil
	}
	var gaps []contract.CapabilityNeed
	for _, need := range shape.RequiredCapabilities {
		// A non-mandatory need is a completion question, not a reason to
		// refuse the turn; the evidence layer already reports it.
		if need.Mandatory() && !need.SatisfiedBy(available) {
			gaps = append(gaps, need)
		}
	}
	return gaps
}

// capabilityLabels renders needs for the machine-readable report.
func capabilityLabels(needs []contract.CapabilityNeed) []string {
	out := make([]string, 0, len(needs))
	for _, need := range needs {
		out = append(out, string(need.Capability))
	}
	return out
}

// operationLabels renders operations for the machine-readable report.
func operationLabels(operations []contract.Operation) []string {
	out := make([]string, 0, len(operations))
	for _, op := range operations {
		out = append(out, string(op))
	}
	return out
}

// capabilityGapReason explains a gap without naming a tool.
func capabilityGapReason(gaps []contract.CapabilityNeed) string {
	labels := make([]string, 0, len(gaps))
	for _, need := range gaps {
		labels = append(labels, need.Label())
	}
	return "the runtime cannot perform a capability this objective requires: " +
		strings.Join(labels, ", ") +
		". No model turn was run and nothing was changed; enable the capability or change the objective."
}
