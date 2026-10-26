// Package security owns OHGO's authorization decision.
//
// It answers one question — may this operation execute? — and nothing else. It
// does not execute, does not prompt, and does not know about a UI. The agent
// loop consults it immediately before the trusted execution seam, so no model
// output and no UI state can stand in for an authorization decision.
//
// The package is deliberately pure: it imports only the standard library, so
// the policy kernel cannot grow a dependency on the execution engine it
// guards. The runtime supplies the execution authority (which capabilities are
// permitted) and the human supplies approval; both are inputs, never authority
// the package grants itself.
package security

import (
	"errors"
	"time"
)

// Capability names the kind of authority an operation requires.
//
// The taxonomy is deliberately small and closed. An unknown capability is
// denied rather than admitted, so a new tool that forgets to classify itself
// fails closed instead of silently gaining authority.
type Capability string

// The capabilities the runtime exercises.
const (
	// CapabilityRead reads a file or resource without changing it.
	CapabilityRead Capability = "filesystem.read"
	// CapabilityWrite creates, modifies, or removes a file.
	CapabilityWrite Capability = "filesystem.write"
	// CapabilityExec runs a process. The command is the operation's scope and
	// risk, not a capability of its own.
	CapabilityExec Capability = "process.exec"
	// CapabilityNetwork fetches a remote resource.
	CapabilityNetwork Capability = "network.fetch"
	// CapabilityExternalSearch performs one bounded external search.
	CapabilityExternalSearch Capability = "external.search"
	// CapabilityExternalMCPRead reads an MCP resource or calls a read-only MCP
	// tool. Discovery is included: it observes a configured server and grants
	// nothing.
	CapabilityExternalMCPRead Capability = "external.mcp.read"
	// CapabilityExternalMCPMutate calls an MCP tool classified as interaction
	// or mutation. It is distinct from a local write because it changes state
	// outside the workspace, but it requires the same strongest authority and
	// an explicit human approval.
	CapabilityExternalMCPMutate Capability = "external.mcp.mutate"
	// CapabilityExternalMCPExecute runs an MCP tool classified as execution.
	// It is not a repository mutation, so FUZZ and BUILD may perform it after
	// approval; ASK may not.
	CapabilityExternalMCPExecute Capability = "external.mcp.execute"
	// CapabilityExternalBrowserRead opens, follows, or reads a bounded page.
	CapabilityExternalBrowserRead Capability = "external.browser.read"
	// CapabilityExternalBrowserMutate is the reserved capability for browser
	// interaction or mutation. No Phase 8 tool implements it; it fails closed.
	CapabilityExternalBrowserMutate Capability = "external.browser.mutate"
)

// Known reports whether cap is one the policy can classify.
func (c Capability) Known() bool {
	switch c {
	case CapabilityRead, CapabilityWrite, CapabilityExec, CapabilityNetwork,
		CapabilityExternalSearch, CapabilityExternalMCPRead,
		CapabilityExternalMCPMutate, CapabilityExternalMCPExecute,
		CapabilityExternalBrowserRead, CapabilityExternalBrowserMutate:
		return true
	}
	return false
}

// Filesystem reports whether the capability addresses the local filesystem.
func (c Capability) Filesystem() bool {
	return c == CapabilityRead || c == CapabilityWrite
}

// External reports whether the capability talks to a remote actor.
func (c Capability) External() bool {
	switch c {
	case CapabilityNetwork, CapabilityExternalSearch, CapabilityExternalMCPRead,
		CapabilityExternalMCPMutate, CapabilityExternalMCPExecute,
		CapabilityExternalBrowserRead, CapabilityExternalBrowserMutate:
		return true
	}
	return false
}

// MutationCapable reports whether the capability can durably change state
// outside the workspace. It is a classification input, never an authorization.
func (c Capability) MutationCapable() bool {
	return c == CapabilityExternalMCPMutate || c == CapabilityExternalBrowserMutate
}

// Risk is a deterministic classification input to policy.
//
// It is not a decision: the same normalized request under the same authority
// always yields the same risk, and the policy — not the model — decides what
// that risk means.
type Risk string

// Risk levels.
const (
	// RiskLow is a read that cannot change anything.
	RiskLow Risk = "LOW"
	// RiskMedium is a workspace-scoped mutation or ordinary execution.
	RiskMedium Risk = "MEDIUM"
	// RiskHigh is access outside the workspace or a destructive operation.
	RiskHigh Risk = "HIGH"
)

// Valid reports whether the risk is one this package defines.
func (r Risk) Valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh:
		return true
	}
	return false
}

// ScopeClass says where an operation is directed.
type ScopeClass string

// Scope classes.
const (
	// ScopeInside is a target inside the workspace root.
	ScopeInside ScopeClass = "workspace"
	// ScopeOutside is a target outside the workspace root.
	ScopeOutside ScopeClass = "outside_workspace"
	// ScopeExternal is a target that is not on the local filesystem (a URL).
	ScopeExternal ScopeClass = "external"
	// ScopeUnspecified is an operation with no filesystem target (an in-memory
	// tool), which is treated as workspace-scoped.
	ScopeUnspecified ScopeClass = "unspecified"
)

// Scope is the normalized target of an operation.
//
// Policy evaluates Scope, never the raw string the model supplied: Raw is kept
// only so a decision can be explained, and Path is the canonical form two
// equal requests must share for a grant to cover them.
type Scope struct {
	// Class is where the target is relative to the workspace.
	Class ScopeClass
	// Raw is what the caller supplied, for display only.
	Raw string
	// Path is the canonical target: an absolute path for a filesystem scope,
	// the host for an external one, and empty when unspecified.
	Path string
}

// Same reports whether two scopes address the same target.
//
// It compares class and canonical path, so two spellings of one path cover
// each other while a widened scope does not.
func (s Scope) Same(other Scope) bool {
	return s.Class == other.Class && s.Path == other.Path
}

// Request is one attempted operation, as the trusted runtime sees it.
//
// Every field is derived by runtime code from the tool and its arguments. A
// model may influence the operation (a command, a path) but can never set the
// capability, the risk, or an authorization flag: those are computed here, in
// the request builder, from trusted classification.
type Request struct {
	// Capability is the authority the operation requires.
	Capability Capability
	// Scope is the normalized target.
	Scope Scope
	// Risk is the deterministic classification.
	Risk Risk
	// Mutates reports whether the operation can change workspace state. A
	// process that only reads does not mutate; a shell command that redirects
	// output does.
	Mutates bool
	// ToolName is the tool that requested the operation.
	ToolName string
	// OperationID identifies the exact attempted operation (the tool call id).
	// It binds a grant to the operation the human approved, so an identical
	// second call cannot consume the first call's grant.
	OperationID string
	// Summary describes the specific operation (a command, a path, a URL), for
	// the approval prompt. It is untrusted display text, never authority.
	Summary string
	// TaskID and SessionID bind the request to the work it belongs to, when
	// known. A grant issued for one binding does not cover another.
	TaskID    string
	SessionID string
}

// Outcome is the authorization decision.
type Outcome string

// The three outcomes.
const (
	// OutcomeAuto authorizes execution with no human interaction.
	OutcomeAuto Outcome = "AUTO"
	// OutcomeApprove requires a trusted human approval before execution.
	OutcomeApprove Outcome = "APPROVE"
	// OutcomeDeny refuses the operation. It is terminal for this attempt.
	OutcomeDeny Outcome = "DENY"
)

// Decision is the structured, explainable result of policy evaluation.
type Decision struct {
	// Outcome is what may happen next.
	Outcome Outcome
	// Capability, Scope, and Risk echo the request the decision answers, so a
	// decision is self-describing for audit and for the approval prompt.
	Capability Capability
	Scope      Scope
	Risk       Risk
	// Mutates echoes whether the operation can change workspace state, so a UI
	// can accent a mutation without re-deriving it from the tool name.
	//
	// External mutation is deliberately excluded: changing a remote service is
	// not a repository change, and treating it as one would let an external
	// call demand local verification or look like completion evidence.
	Mutates bool
	// ExternalMutation marks an operation that durably changes state outside
	// the workspace. It requires the strongest authority and approval, but it
	// is not workspace mutation evidence.
	ExternalMutation bool
	// ReadOnly marks an execution decision that must run behind a read-only
	// workspace boundary. It is derived from the authority in force, not from
	// the command text: an authority that cannot write cannot be trusted to
	// recognise every mutating command.
	ReadOnly bool
	// Reason is the human-readable explanation.
	Reason string
	// Rule is the machine-readable policy rule that produced the outcome.
	Rule string
}

// Allowed reports whether the decision authorizes execution outright.
func (d Decision) Allowed() bool { return d.Outcome == OutcomeAuto }

// Grant is authority that has already been established by a trusted approval.
//
// A grant is not an approval prompt: it is the recorded outcome of one. It is
// narrow by construction — one capability, one scope, one task/session binding
// — and it never expands to cover a materially different operation.
type Grant struct {
	// Capability is the authority granted.
	Capability Capability
	// Scope is the exact target granted.
	Scope Scope
	// TaskID and SessionID bind the grant. An empty binding matches any, which
	// is only used when the runtime has no identity to bind to.
	TaskID    string
	SessionID string
	// OperationID binds the grant to one exact operation. An empty value
	// matches any operation with the same capability and scope.
	OperationID string
	// Provenance names how the grant was established (the approval path).
	Provenance string
	// ExpiresAt bounds the grant's lifetime. Zero means no time bound; the
	// grant store still consumes it after one execution.
	ExpiresAt time.Time
}

// Covers reports whether the grant authorizes req.
//
// It requires the capability and canonical scope to match exactly, and the
// task/session bindings to be compatible. A grant for one path never covers
// another, and a grant for one task never covers a different one.
func (g Grant) Covers(req Request, now time.Time) bool {
	if g.Capability != req.Capability {
		return false
	}
	if !g.Scope.Same(req.Scope) {
		return false
	}
	if g.TaskID != "" && g.TaskID != req.TaskID {
		return false
	}
	if g.SessionID != "" && g.SessionID != req.SessionID {
		return false
	}
	if g.OperationID != "" && g.OperationID != req.OperationID {
		return false
	}
	if !g.ExpiresAt.IsZero() && !now.Before(g.ExpiresAt) {
		return false
	}
	return true
}

// ErrNoGrant means no grant covered the request.
var ErrNoGrant = errors.New("security: no covering grant")
