package contract

import "strings"

// GroundingAction is the capability the user's request appears to ask for.
//
// It is deliberately separate from routing Intent. Routing decides which
// workspace may answer a turn; grounding records what the request actually
// says so a later tool call or answer can be checked against it. Unknown is a
// valid value: a request that names entities but no action must not acquire an
// action merely because a tool is available.
type GroundingAction string

// Grounding actions.
const (
	// GroundingUnknown means the request names no actionable verb. The runtime
	// may observe, but it must not claim the request asked for the observation.
	GroundingUnknown GroundingAction = "unknown"
	// GroundingRead asks to understand or inspect.
	GroundingRead GroundingAction = "read"
	// GroundingInvestigate asks to probe, reproduce, or diagnose.
	GroundingInvestigate GroundingAction = "investigate"
	// GroundingExecute asks to run a process or check.
	GroundingExecute GroundingAction = "execute"
	// GroundingMutate asks to change the repository.
	GroundingMutate GroundingAction = "mutate"
)

// Known reports whether the request grounded a concrete action.
func (a GroundingAction) Known() bool { return a != "" && a != GroundingUnknown }

// EntityKind names what a preserved entity is.
type EntityKind string

// Entity kinds.
const (
	// EntityProject is a named project, repository, service, or module.
	EntityProject EntityKind = "project"
	// EntityPath is a repository-relative file or directory.
	EntityPath EntityKind = "path"
	// EntitySymbol is a named Go symbol.
	EntitySymbol EntityKind = "symbol"
	// EntityPackage is a named Go package.
	EntityPackage EntityKind = "package"
)

// Entity is one named thing the user's request contains.
//
// Entities are extracted from the user's own text. A model answer that drops
// one, or a scope that silently replaces one, is a semantic drift the runtime
// can detect without asking a second model.
type Entity struct {
	// Value is the user's wording, preserved.
	Value string `json:"value"`
	// Kind is the entity class.
	Kind EntityKind `json:"kind"`
	// Optional marks an entity the user made conditional ("if relevant").
	// It is preserved in state but is not required in the answer.
	Optional bool `json:"optional,omitempty"`
}

// GroundingScopeKind is how wide the request's scope is.
type GroundingScopeKind string

// Scope kinds.
const (
	// ScopeUnknown means the request did not establish a usable scope.
	ScopeUnknown GroundingScopeKind = "unknown"
	// ScopeNamed means the request named the entities in scope.
	ScopeNamed GroundingScopeKind = "named"
	// ScopeRepository means the request refers to the repository itself.
	ScopeRepository GroundingScopeKind = "repository"
)

// GroundingScope is the scope the request establishes.
type GroundingScope struct {
	// Kind is the scope class.
	Kind GroundingScopeKind `json:"kind"`
	// Values are the named scope values, when Kind is named.
	Values []string `json:"values,omitempty"`
	// Conditional reports that relevance is conditional ("if relevant"),
	// so the runtime must not force execution against the scope.
	Conditional bool `json:"conditional,omitempty"`
}

// AssumptionKind is the evidence tier of one interpretation.
type AssumptionKind string

// Assumption tiers.
const (
	// AssumptionGrounded is stated by the user.
	AssumptionGrounded AssumptionKind = "grounded"
	// AssumptionInferred is a reasonable semantic expansion of the request.
	AssumptionInferred AssumptionKind = "inferred"
	// AssumptionUnsupported is not stated and not reasonably inferred. It is
	// recorded so the runtime can refuse to let it become an objective.
	AssumptionUnsupported AssumptionKind = "unsupported"
)

// Assumption is one interpretation the runtime is carrying.
type Assumption struct {
	// Text is the interpretation.
	Text string `json:"text"`
	// Kind is its evidence tier.
	Kind AssumptionKind `json:"kind"`
	// Basis explains the tier.
	Basis string `json:"basis,omitempty"`
}

// Ambiguity is one unresolved dimension in the request.
type Ambiguity struct {
	// Kind is a stable identity, e.g. "unknown_action".
	Kind string `json:"kind"`
	// Detail explains what the request leaves open.
	Detail string `json:"detail,omitempty"`
	// Material reports that resolving it changes what may be executed. A
	// material ambiguity blocks mutation; observation may continue.
	Material bool `json:"material,omitempty"`
}

// GroundingConfidence is the strength of the extraction.
type GroundingConfidence string

// Confidence levels.
const (
	// GroundingConfidenceHigh means the action and scope are explicit.
	GroundingConfidenceHigh GroundingConfidence = "high"
	// GroundingConfidenceMedium means the action is explicit but a dimension
	// remains open.
	GroundingConfidenceMedium GroundingConfidence = "medium"
	// GroundingConfidenceLow means the action is unknown.
	GroundingConfidenceLow GroundingConfidence = "low"
)

// IntentGrounding is the runtime's structured reading of one user request.
//
// It is built deterministically from the user's text before execution and is
// carried with the completion contract across resumes. It is not model output
// and it grants nothing: it is the reference a model proposal is checked
// against.
type IntentGrounding struct {
	// Objective is the user's request, trimmed.
	Objective string `json:"objective"`
	// Action is the primary requested action.
	Action GroundingAction `json:"action"`
	// Actions preserves every requested action in a compound request, in
	// request order.
	Actions []GroundingAction `json:"actions,omitempty"`
	// Entities are the named things the request contains.
	Entities []Entity `json:"entities,omitempty"`
	// Scope is the request's declared breadth.
	Scope GroundingScope `json:"scope"`
	// ExpectedOutcome is what a truthful completion would look like.
	ExpectedOutcome string `json:"expectedOutcome,omitempty"`
	// Constraints are explicit limits the request states.
	Constraints []string `json:"constraints,omitempty"`
	// Assumptions are interpretations carried by the runtime, with their
	// evidence tier.
	Assumptions []Assumption `json:"assumptions,omitempty"`
	// Ambiguities are dimensions the request leaves open.
	Ambiguities []Ambiguity `json:"ambiguities,omitempty"`
	// MutationIntent reports that the request clearly asks for a repository
	// change. It is false for an actionless request.
	MutationIntent bool `json:"mutationIntent"`
	// Confidence is the extraction strength.
	Confidence GroundingConfidence `json:"confidence"`
}

// HasMaterialAmbiguity reports whether an unresolved dimension affects
// execution.
func (g *IntentGrounding) HasMaterialAmbiguity() bool {
	if g == nil {
		return false
	}
	for _, ambiguity := range g.Ambiguities {
		if ambiguity.Material {
			return true
		}
	}
	return false
}

// RequiredEntities returns the entities a faithful answer must preserve.
//
// Path, symbol, and package entities are repository coordinates: a mutation
// already proves them through target evidence, and an informational answer may
// legitimately quote only part of a path. Named projects are different: they
// are the semantic subject of the request, so dropping one is exactly the
// failure this contract exists to catch.
func (g *IntentGrounding) RequiredEntities() []Entity {
	if g == nil {
		return nil
	}
	var out []Entity
	for _, entity := range g.Entities {
		if entity.Optional || entity.Kind != EntityProject {
			continue
		}
		if strings.TrimSpace(entity.Value) == "" {
			continue
		}
		out = append(out, entity)
	}
	return out
}

// HasAction reports whether the request asked for action.
func (g *IntentGrounding) HasAction(action GroundingAction) bool {
	if g == nil {
		return false
	}
	if g.Action == action {
		return true
	}
	for _, candidate := range g.Actions {
		if candidate == action {
			return true
		}
	}
	return false
}

// EntityValues returns the unique entity values in request order.
func (g *IntentGrounding) EntityValues() []string {
	if g == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, entity := range g.Entities {
		value := strings.TrimSpace(entity.Value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// ActionProposal is the model's concrete interpretation of one tool call.
//
// It is derived from the tool's declared metadata and the call's arguments,
// never from model narration. A proposal is untrusted input: the grounding
// validator decides whether it may become execution.
type ActionProposal struct {
	// Action is the capability the call exercises.
	Action GroundingAction `json:"action"`
	// Targets are the repository coordinates the call names.
	Targets []string `json:"targets,omitempty"`
	// Mutates reports a per-call repository change.
	Mutates bool `json:"mutates"`
	// Executes reports a per-call process start.
	Executes bool `json:"executes"`
	// External reports knowledge from outside the workspace.
	External bool `json:"external"`
	// Delegate reports that the call hands work to another agent. Delegation
	// is an action with a potential objective, so an actionless request
	// cannot ground it.
	Delegate bool `json:"delegate"`
}

// GroundingVerdict is the deterministic semantic decision.
type GroundingVerdict string

// Verdicts.
const (
	// GroundingPass means the proposal or answer is grounded.
	GroundingPass GroundingVerdict = "PASS"
	// GroundingReview means the proposal may proceed only as bounded
	// observation, or the answer cannot be called complete without review.
	GroundingReview GroundingVerdict = "REVIEW"
	// GroundingReject means the proposal contradicts the request and must not
	// execute.
	GroundingReject GroundingVerdict = "REJECT"
)

// GroundingIssue is one machine-readable reason for a verdict.
type GroundingIssue struct {
	// Code is a stable identity, e.g. "entity_missing".
	Code string `json:"code"`
	// Detail explains the observation.
	Detail string `json:"detail,omitempty"`
}

// GroundingAssessment is the result of validating a proposal or answer.
type GroundingAssessment struct {
	// Verdict is the decision.
	Verdict GroundingVerdict `json:"verdict"`
	// Reasons are the human-readable explanations.
	Reasons []string `json:"reasons,omitempty"`
	// Issues are the machine-readable explanations.
	Issues []GroundingIssue `json:"issues,omitempty"`
}

// Pass reports whether the assessment allows the normal path.
func (a GroundingAssessment) Pass() bool { return a.Verdict == GroundingPass }

// Reason renders the assessment's reasons for a review boundary.
func (a GroundingAssessment) Reason() string {
	if len(a.Reasons) == 0 {
		return ""
	}
	return strings.Join(a.Reasons, "; ")
}
