// Package workspace defines OHGO's execution authorities.
//
// A workspace is not a UI mode: it is the runtime boundary that decides what
// the agent is allowed to do. This package is the single owner of that
// decision (ARCHITECTURE_CONTRACT_V1 §30), so capability policy lives in the
// core runtime and the UI only displays it.
//
// The three authorities are fixed (§3, I1):
//
//	ASK   Understand — read-only
//	FUZZ  Discover   — read + execute, no repository mutation
//	BUILD Change     — the sole mutation authority
//
// Everything else (plan, review, debug, test, research) is an activity
// performed inside one of these boundaries, never a fourth workspace (I20).
package workspace

import (
	"fmt"
	"strings"
)

// Workspace names one execution authority.
//
// The zero value is invalid on purpose: a Workspace that was never resolved
// must be denied rather than silently treated as one of the three. Callers
// that require a valid workspace use Parse.
type Workspace string

// The three primary workspaces. There are exactly three (I1).
const (
	// Ask is the read-only reasoning workspace. It cannot mutate the project.
	Ask Workspace = "ask"
	// Fuzz is the exploratory workspace. It may execute commands but cannot
	// mutate the project.
	Fuzz Workspace = "fuzz"
	// Build is the mutation authority: the only workspace that may change the
	// repository.
	Build Workspace = "build"
)

// All returns the three workspaces in canonical order.
func All() []Workspace { return []Workspace{Ask, Fuzz, Build} }

// Parse resolves a user-supplied workspace name.
//
// An unknown name is an error, never a silent fallback: defaulting a typo to
// one authority would grant capabilities the user did not choose.
func Parse(value string) (Workspace, error) {
	switch Workspace(strings.ToLower(strings.TrimSpace(value))) {
	case Ask:
		return Ask, nil
	case Fuzz:
		return Fuzz, nil
	case Build:
		return Build, nil
	}
	return "", fmt.Errorf("unknown workspace %q: want ask, fuzz, or build", value)
}

// Valid reports whether w is one of the three authorities.
func (w Workspace) Valid() bool {
	switch w {
	case Ask, Fuzz, Build:
		return true
	}
	return false
}

// String returns the wire name.
func (w Workspace) String() string { return string(w) }

// Label returns the uppercase display name used in chrome.
func (w Workspace) Label() string { return strings.ToUpper(string(w)) }

// Purpose is the one-line description shown to users and the model.
func (w Workspace) Purpose() string {
	switch w {
	case Ask:
		return "understand"
	case Fuzz:
		return "discover"
	case Build:
		return "change"
	}
	return "unknown"
}

// Operation is a class of action a tool may perform.
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
	// OpExec runs a process. Execution is not mutation: a process may run in
	// a workspace that cannot change the repository (§5.2).
	OpExec Operation = "exec"
	// OpWrite creates or modifies a file.
	OpWrite Operation = "write"
	// OpPatch applies an in-place edit.
	OpPatch Operation = "patch"
	// OpDelete removes a file.
	OpDelete Operation = "delete"
	// OpMove renames or moves a file.
	OpMove Operation = "move"
)

// Mutates reports whether an operation changes the repository.
//
// It is the exported form of the mutation test, so the security layer can
// classify a request as state-changing without duplicating the rule.
func (op Operation) Mutates() bool { return op.mutation() }

// mutation reports whether an operation changes the repository.
func (op Operation) mutation() bool {
	switch op {
	case OpWrite, OpPatch, OpDelete, OpMove:
		return true
	}
	return false
}

// Encompasses reports whether w holds at least other's authority.
//
// The three authorities form a strict chain — ASK ⊂ FUZZ ⊂ BUILD — because
// each workspace's permitted operations are a superset of the one below it.
// The comparison is used only to refuse: a transition policy may propose a
// workspace its current one does not encompass, so prediction confidence can
// never widen authority (I8, I9). It never grants anything.
func (w Workspace) Encompasses(other Workspace) bool {
	rank, otherRank := authorityRank(w), authorityRank(other)
	return rank > 0 && otherRank > 0 && rank >= otherRank
}

// authorityRank orders the authorities from least to most powerful. An
// unresolved workspace ranks zero so it encompasses nothing and is
// encompassed by nothing.
func authorityRank(w Workspace) int {
	switch w {
	case Ask:
		return 1
	case Fuzz:
		return 2
	case Build:
		return 3
	}
	return 0
}

// Permits reports whether the workspace may perform op.
//
// An unrecognised operation is denied: a capability the policy cannot
// classify is not one it can grant (`The model proposes, the runtime
// decides`).
func (w Workspace) Permits(op Operation) bool {
	if op.mutation() {
		return w == Build
	}
	switch op {
	case OpExec:
		// Execution belongs to FUZZ and BUILD. ASK is reasoning only.
		return w == Fuzz || w == Build
	case OpRead, OpSearch, OpInspect:
		return w.Valid()
	}
	return false
}

// Denied is the error an admission check returns when a workspace may not
// perform an operation.
//
// Its message is the runtime's refusal record, in the contract's shape
// (§5.3): "capability denied workspace=fuzz operation=write
// reason=mutation_not_permitted".
type Denied struct {
	// Workspace is the authority that refused the operation.
	Workspace Workspace
	// Operation is the refused action.
	Operation Operation
	// Reason is the machine-readable cause.
	Reason string
	// Tool names the tool that requested the operation, when known.
	Tool string
}

// Admission reasons.
const (
	// ReasonMutationNotPermitted marks a mutation attempted outside BUILD.
	ReasonMutationNotPermitted = "mutation_not_permitted"
	// ReasonExecNotPermitted marks execution attempted in ASK.
	ReasonExecNotPermitted = "exec_not_permitted"
	// ReasonUnknownOperation marks an operation the policy cannot classify.
	ReasonUnknownOperation = "unknown_operation"
	// ReasonInvalidWorkspace marks an unresolved workspace.
	ReasonInvalidWorkspace = "invalid_workspace"
)

// Error implements the error interface.
func (e *Denied) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = ReasonUnknownOperation
	}
	out := fmt.Sprintf("capability denied workspace=%s operation=%s reason=%s", e.Workspace, e.Operation, reason)
	if e.Tool != "" {
		out += " tool=" + e.Tool
	}
	return out
}

// Admit reports whether w may perform op, returning a *Denied otherwise.
//
// It is the runtime gate: every tool invocation passes through here (or the
// Authority adapter) before it may execute.
func Admit(w Workspace, op Operation) error {
	if !w.Valid() {
		return &Denied{Workspace: w, Operation: op, Reason: ReasonInvalidWorkspace}
	}
	if w.Permits(op) {
		return nil
	}
	return &Denied{Workspace: w, Operation: op, Reason: reasonFor(w, op)}
}

// reasonFor names why an operation was refused, so the refusal is legible.
func reasonFor(w Workspace, op Operation) string {
	switch {
	case op.mutation():
		return ReasonMutationNotPermitted
	case op == OpExec:
		return ReasonExecNotPermitted
	default:
		return ReasonUnknownOperation
	}
}
