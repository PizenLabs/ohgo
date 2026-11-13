package contract

// Capability preflight vocabulary.
//
// A request states an objective; the objective implies the operations the
// runtime must be able to perform. This file names that implication as data.
// It never grants authority, never inspects the tool registry, and never talks
// to a model: it answers "what would executing this contract require", so the
// control plane can answer "can this runtime provide it" before a model turn
// spends a turn discovering the answer by failing.
//
// Two concepts are deliberately separate:
//
//	capability  what a request needs (read, mutate, execute)
//	operation   the concrete repository action that serves it (read, write,
//	            patch, delete, move, exec)
//
// A need is satisfied by any one of its operations: a change may be served by
// creating a file or by patching one, so a runtime with only a write tool is
// not blocked on a change request.

// Operation is a class of repository action a capability performs.
//
// Operations are coarser than tool names and finer than "mutation": the
// authority boundary cares whether an action changes the repository, not which
// tool implemented it.
type Operation string

// Operations.
const (
	// OpRead reads a file or resource without changing it.
	OpRead Operation = "read"
	// OpSearch finds files or text without changing them.
	OpSearch Operation = "search"
	// OpInspect examines state (repository, dependencies, git) without
	// changing it.
	OpInspect Operation = "inspect"
	// OpExec runs a process. Execution is not mutation: a process may run in a
	// workspace that cannot change the repository.
	OpExec Operation = "exec"
	// OpWrite creates or overwrites a file.
	OpWrite Operation = "write"
	// OpPatch applies an in-place edit to an existing file.
	OpPatch Operation = "patch"
	// OpDelete removes a file.
	OpDelete Operation = "delete"
	// OpMove renames or moves a file.
	OpMove Operation = "move"
)

// Mutates reports whether the operation changes the repository.
func (op Operation) Mutates() bool {
	switch op {
	case OpWrite, OpPatch, OpDelete, OpMove:
		return true
	}
	return false
}

// Label is the human-facing name of the operation.
//
// The wire value is not always the word a user expects ("patch" is an edit),
// and a control-plane message must read as a description of the missing
// capability rather than as an internal enum.
func (op Operation) Label() string {
	switch op {
	case OpPatch:
		return "edit"
	case OpExec:
		return "exec"
	}
	return string(op)
}

// Capability names one class of work a request needs from the runtime.
//
// It is coarser than a tool name and finer than an intent: a request to run
// tests and a request to reproduce a failure both need execution, but only the
// latter prefers autonomous investigation.
type Capability string

// Capabilities.
const (
	// CapabilityRead needs inspection, search, and reasoning only.
	CapabilityRead Capability = "read"
	// CapabilityExecute needs to run a process or check.
	CapabilityExecute Capability = "execute"
	// CapabilityInvestigate needs autonomous probing, reproduction, or
	// experimentation.
	CapabilityInvestigate Capability = "investigate"
	// CapabilityMutate needs a repository change.
	CapabilityMutate Capability = "mutate"
)

// Label is the human-facing name of the capability.
func (c Capability) Label() string {
	switch c {
	case CapabilityMutate:
		return "repository mutation"
	case CapabilityExecute:
		return "command execution"
	case CapabilityInvestigate:
		return "autonomous investigation"
	case CapabilityRead:
		return "repository read"
	}
	return string(c)
}

// Valid reports whether the capability is one this vocabulary defines.
//
// An unknown capability is never treated as a known one: a caller that cannot
// classify a request must not infer authority from the value.
func (c Capability) Valid() bool {
	switch c {
	case CapabilityRead, CapabilityExecute, CapabilityInvestigate, CapabilityMutate:
		return true
	}
	return false
}

// CapabilityNeed is one capability a completion contract requires, together
// with the concrete operations that can satisfy it.
type CapabilityNeed struct {
	// Capability is the class of work the contract needs.
	Capability Capability `json:"capability"`
	// Operations are the operations that satisfy the need. Any one of them is
	// enough; the list is a union because the contract accepts more than one
	// way to produce the same result.
	Operations []Operation `json:"operations"`
}

// SatisfiedBy reports whether any available operation serves the need.
func (n CapabilityNeed) SatisfiedBy(available []Operation) bool {
	for _, have := range available {
		for _, want := range n.Operations {
			if have == want {
				return true
			}
		}
	}
	return false
}

// Mandatory reports whether the objective cannot be attempted at all without
// this capability.
//
// Reading is deliberately not mandatory. An answer can be produced from the
// conversation, and the evidence layer already reports a repository-scoped
// answer with no observation as insufficient, which is a truthful review. A
// block here would replace a reviewable answer with an unreachable objective.
// Mutation and execution are different: without them the requested work cannot
// happen, so a model turn would only discover that by failing.
func (n CapabilityNeed) Mandatory() bool {
	return n.Capability != CapabilityRead
}

// Label renders the need for a control-plane message, e.g.
// "repository mutation (write or edit)".
func (n CapabilityNeed) Label() string {
	label := n.Capability.Label()
	if len(n.Operations) == 0 {
		return label
	}
	out := label + " ("
	for i, op := range n.Operations {
		if i > 0 {
			out += " or "
		}
		out += op.Label()
	}
	return out + ")"
}

// RequiredCapabilities is the minimum the runtime must be able to perform to
// execute this contract.
//
// The derivation is finite and deterministic, and deliberately minimal: a
// documentation change must not require command execution unless the contract
// itself asks for a check. It never reads a tool registry, so the same contract
// always implies the same requirements.
func (c IntentContract) RequiredCapabilities() []CapabilityNeed {
	// Reading is part of every objective: even a change has to be located and
	// checked before it can be made.
	needs := []CapabilityNeed{{
		Capability: CapabilityRead,
		Operations: []Operation{OpRead},
	}}
	if c.ConditionalMutation {
		// A staged request ("investigate first; if confirmed, fix it") starts
		// with the investigation it names. Requiring the change's capabilities
		// now would refuse the phase the user asked for first; the change
		// branch is judged by completion when it is actually reached.
		return needs
	}
	switch c.Kind {
	case ObjectiveMutation:
		operations := c.mutationOperations()
		if len(operations) > 0 {
			needs = append(needs, CapabilityNeed{
				Capability: CapabilityMutate,
				Operations: operations,
			})
		}
		// A change contract that names a behavioural check ("npm run build
		// passed") cannot be satisfied by mutation alone: the runtime has to
		// run the check it promised.
		if len(c.Verification) > 0 || c.ExecutionMode == ExecutionRuntime {
			needs = append(needs, CapabilityNeed{
				Capability: CapabilityExecute,
				Operations: []Operation{OpExec},
			})
		}
	case ObjectiveExecution:
		needs = append(needs, CapabilityNeed{
			Capability: CapabilityExecute,
			Operations: []Operation{OpExec},
		})
	}
	return needs
}

// mutationOperations lists the operations that could serve the requested
// change.
//
// The list is a union on purpose. A request that names no direction ("convert
// this project") can be served by creating new files or by editing existing
// ones, so requiring both would block a runtime that can perform the task.
// A direction the user did name is kept exact: a delete request is not served
// by a create-only capability.
func (c IntentContract) mutationOperations() []Operation {
	var out []Operation
	add := func(op Operation) {
		for _, existing := range out {
			if existing == op {
				return
			}
		}
		out = append(out, op)
	}
	for _, change := range c.ExpectedChanges {
		switch change.Operation {
		case ChangeCreate:
			add(OpWrite)
		case ChangeModify:
			add(OpPatch)
		case ChangeDelete:
			add(OpDelete)
		default:
			add(OpWrite)
			add(OpPatch)
		}
	}
	if len(c.ExpectedChanges) == 0 {
		// A behavioural mutation objective ("fix the failing tests") names no
		// target and therefore no direction. Either way of producing the change
		// is acceptable.
		add(OpWrite)
		add(OpPatch)
	}
	return out
}

// UnmetCapabilities returns the mandatory needs no available operation can
// serve, in requirement order.
//
// It is the blocking verdict, not a full inventory: RequiredCapabilities is the
// inventory. A need is unmet only when the objective cannot be attempted
// without it, so a missing observation capability stays a completion question
// rather than an unreachable task. It is a pure set difference: availability is
// the caller's fact (the constructed tool surface, or the surface the authority
// permits), never something this package infers.
func (c IntentContract) UnmetCapabilities(available []Operation) []CapabilityNeed {
	var unmet []CapabilityNeed
	for _, need := range c.RequiredCapabilities() {
		if !need.Mandatory() || need.SatisfiedBy(available) {
			continue
		}
		unmet = append(unmet, need)
	}
	return unmet
}
