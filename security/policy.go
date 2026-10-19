package security

import (
	"fmt"
	"sync"
	"time"
)

// Authority reports the execution authority in force.
//
// It is supplied by the runtime — the workspace model, in ohgo — so the policy
// kernel never grows a dependency on the component that owns authority. The
// authority can only permit or refuse; it cannot be consulted for anything the
// model controls.
type Authority interface {
	// Name identifies the authority for the decision record (for example
	// "build").
	Name() string
	// Permits reports whether the authority allows a capability at all.
	Permits(cap Capability) bool
}

// Policy rule names, recorded on every decision.
const (
	// RuleUnknownCapability marks a capability the policy cannot classify.
	RuleUnknownCapability = "unknown_capability"
	// RuleCapabilityNotPermitted marks a capability the authority forbids.
	RuleCapabilityNotPermitted = "capability_not_permitted"
	// RuleMutationNotPermitted marks a mutation the authority forbids.
	RuleMutationNotPermitted = "mutation_not_permitted"
	// RuleOutsideWorkspace marks a filesystem target outside the workspace.
	RuleOutsideWorkspace = "outside_workspace"
	// RuleGrant marks a decision satisfied by an existing grant.
	RuleGrant = "grant"
	// RuleRiskLow marks an auto-authorized low-risk operation.
	RuleRiskLow = "risk_low"
	// RuleRiskMedium marks a medium-risk operation that needs approval.
	RuleRiskMedium = "risk_medium"
	// RuleRiskHigh marks a high-risk operation that needs approval.
	RuleRiskHigh = "risk_high"
	// RuleUnknownRisk marks a request with no valid risk classification.
	RuleUnknownRisk = "unknown_risk"
)

// Policy is the deterministic authorization kernel.
//
// It is stateless: the caller supplies the authority in force and any grant
// already established, so the same request under the same inputs always yields
// the same decision. It has no configuration language and no plugin surface —
// the rules are the code, and they are deliberately few.
type Policy struct{}

// Evaluate returns the decision for a request.
//
// The order is load-bearing. Authority is checked before risk, so a capability
// the workspace forbids is denied rather than offered for approval. Scope is
// checked before grants, so no grant can widen authority past the workspace
// boundary. Only then does risk decide between automatic execution and human
// approval.
func (Policy) Evaluate(req Request, auth Authority, grant *Grant) Decision {
	decision := Decision{
		Capability: req.Capability,
		Scope:      req.Scope,
		Risk:       req.Risk,
		Mutates:    req.Mutates,
	}

	if !req.Capability.Known() {
		return deny(decision, RuleUnknownCapability,
			fmt.Sprintf("unknown capability %q", req.Capability))
	}
	if auth == nil || !auth.Permits(req.Capability) {
		return deny(decision, RuleCapabilityNotPermitted,
			fmt.Sprintf("%s is not permitted by the %s authority", req.Capability, authorityName(auth)))
	}
	if req.Mutates && !auth.Permits(CapabilityWrite) {
		return deny(decision, RuleMutationNotPermitted,
			fmt.Sprintf("the %s authority cannot change the workspace", authorityName(auth)))
	}
	if req.Capability.Filesystem() && req.Scope.Class == ScopeOutside {
		return deny(decision, RuleOutsideWorkspace,
			"the target is outside the workspace")
	}
	if grant != nil && grant.Covers(req, time.Now()) {
		decision.Outcome = OutcomeAuto
		decision.Rule = RuleGrant
		decision.Reason = "authorized by an existing grant"
		return decision
	}

	switch req.Risk {
	case RiskLow:
		decision.Outcome = OutcomeAuto
		decision.Rule = RuleRiskLow
		decision.Reason = "low-risk read within the workspace"
	case RiskMedium:
		decision.Outcome = OutcomeApprove
		decision.Rule = RuleRiskMedium
		decision.Reason = "a workspace-scoped operation that changes state or runs a process"
	case RiskHigh:
		decision.Outcome = OutcomeApprove
		decision.Rule = RuleRiskHigh
		decision.Reason = "a high-risk operation"
	default:
		return deny(decision, RuleUnknownRisk,
			fmt.Sprintf("unknown risk %q", req.Risk))
	}
	return decision
}

// deny fills a decision with a refusal.
func deny(d Decision, rule, reason string) Decision {
	d.Outcome = OutcomeDeny
	d.Rule = rule
	d.Reason = reason
	return d
}

// authorityName names the authority for a message, tolerating a nil authority.
func authorityName(auth Authority) string {
	if auth == nil {
		return "unknown"
	}
	return auth.Name()
}

// GrantStore holds the ephemeral grants established by approvals.
//
// It is in-memory on purpose: a grant authorizes one execution, not a durable
// permission, and the runtime has no need to persist one. Consumption is
// atomic, so two concurrent calls cannot share a single grant.
type GrantStore struct {
	mu     sync.Mutex
	grants []Grant
}

// NewGrantStore returns an empty store.
func NewGrantStore() *GrantStore { return &GrantStore{} }

// Add records a grant.
func (s *GrantStore) Add(g Grant) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants = append(s.grants, g)
}

// Consume returns and removes the first grant covering req.
//
// Consuming on read is what makes a grant one-shot: an approval authorizes the
// exact operation the human saw, once. A request that changed since the
// approval no longer matches and must be approved again.
func (s *GrantStore) Consume(req Request, now time.Time) (Grant, bool) {
	if s == nil {
		return Grant{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, g := range s.grants {
		if !g.Covers(req, now) {
			continue
		}
		s.grants = append(s.grants[:i], s.grants[i+1:]...)
		return g, true
	}
	return Grant{}, false
}

// Len reports how many grants are pending. It exists for tests and diagnostics.
func (s *GrantStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.grants)
}
