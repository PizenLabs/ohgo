package workspace

import (
	"fmt"
)

// ExecutionContract is the authority a master task was created under and the
// capabilities it has been authorized to use since.
//
// It is deliberately separate from the live Workspace and from
// contract.IntentContract:
//
//	origin    where the task was authorized (immutable)
//	contract  what the task may do (widened only by a human decision)
//	role      the authority boundary for the current phase (ephemeral)
//
// The contract is evidence about authority, never authority itself. Every tool
// call still crosses the live gate; a persisted contract cannot admit one.
type ExecutionContract struct {
	// Origin is the workspace the task was created under. It never changes, so
	// "origin=build, current=ask" remains a meaningful fact after a narrowing
	// transition and across a resume.
	Origin Workspace `json:"origin,omitempty"`
	// Execute reports that the task may run processes and investigate.
	Execute bool `json:"execute,omitempty"`
	// Mutate reports that the task may change the repository. A mutating task
	// is always an executing task; Normalize enforces that implication.
	Mutate bool `json:"mutate,omitempty"`
}

// ContractFor derives the initial contract for a task authorized under origin.
//
// The derivation is the authority chain itself, not a role catalogue: ASK reads,
// FUZZ reads and executes, BUILD reads, executes, and mutates. An unresolved
// origin yields the zero contract, which permits nothing.
func ContractFor(origin Workspace) ExecutionContract {
	contract := ExecutionContract{Origin: origin}
	if origin.Valid() {
		contract.Execute = origin != Ask
		contract.Mutate = origin == Build
	}
	return contract
}

// Valid reports whether the contract names an authority.
func (c ExecutionContract) Valid() bool { return c.Origin.Valid() }

// Normalize enforces the authority implication and returns the contract.
//
// A mutation authorization with no execution authorization cannot be true: a
// caller that constructs the struct directly must not create a task that may
// write but not run the check that follows the write.
func (c ExecutionContract) Normalize() ExecutionContract {
	if c.Mutate {
		c.Execute = true
	}
	return c
}

// Permits reports whether the contract authorizes a request capability.
//
// Reading is available to every valid authority; execution and investigation
// need Execute; mutation needs Mutate. An unknown capability is denied rather
// than folded into reading.
func (c ExecutionContract) Permits(capability Capability) bool {
	if !c.Valid() {
		return false
	}
	switch capability {
	case CapabilityRead:
		return true
	case CapabilityExecute, CapabilityInvestigate:
		return c.Execute
	case CapabilityMutate:
		return c.Mutate
	}
	return false
}

// Authorize returns the contract widened by a human-authorized target role.
//
// It only adds capability. A narrowing role leaves the origin and the granted
// capabilities intact, which is what lets a BUILD-origin task answer a question
// under ASK and later return to the execution side without losing the authority
// the human originally granted. The caller must have actually moved the live
// authority through the workspace owner before recording this.
func (c ExecutionContract) Authorize(target Workspace) ExecutionContract {
	c = c.Normalize()
	if !c.Origin.Valid() {
		// A task created before the field existed has no origin to preserve;
		// the human-authorized target is the only authority evidence.
		return ContractFor(target)
	}
	switch target {
	case Build:
		c.Execute = true
		c.Mutate = true
	case Fuzz:
		c.Execute = true
	case Ask:
	}
	return c
}

// String returns the wire form, used in diagnostics.
func (c ExecutionContract) String() string {
	if !c.Valid() {
		return "origin= valid=false"
	}
	return fmt.Sprintf("origin=%s execute=%t mutate=%t", c.Origin, c.Execute, c.Mutate)
}
