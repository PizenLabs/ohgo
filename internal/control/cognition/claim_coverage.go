package cognition

// Claim classes and their evidence requirements are the control-plane half of
// the answer audit. They do not create a second evidence store: every status is
// derived from the same ClaimSummary the answer audit and the state block use.
//
// A class answers one question: what observation can legitimately support this
// kind of statement? Configuration is not implementation, documentation is not
// implementation, inventory is not behavior, and a source observation is not
// runtime execution.

// ClaimClass is one kind of answer statement the runtime audits.
type ClaimClass string

// Claim classes.
const (
	// ClaimProjectIdentity describes what the project is.
	ClaimProjectIdentity ClaimClass = "project_identity"
	// ClaimTopology describes how components are arranged.
	ClaimTopology ClaimClass = "topology"
	// ClaimConfiguration describes declared settings or deployment intent.
	ClaimConfiguration ClaimClass = "configuration"
	// ClaimImplementationStatus describes whether a component is complete,
	// partial, a stub, a mock, or a scaffold.
	ClaimImplementationStatus ClaimClass = "implementation_status"
	// ClaimDependencyUse describes whether a dependency is declared, imported,
	// initialized, used in source, or observed at runtime.
	ClaimDependencyUse ClaimClass = "dependency_use"
	// ClaimBehavior describes what the implementation does.
	ClaimBehavior ClaimClass = "behavior"
	// ClaimRuntimeState describes what is running now.
	ClaimRuntimeState ClaimClass = "runtime_state"
	// ClaimStructuralRelationship describes a code-level relationship between
	// two endpoints.
	ClaimStructuralRelationship ClaimClass = "structural_relationship"
	// ClaimAbsence describes something that does not exist or is not
	// implemented.
	ClaimAbsence ClaimClass = "absence"
	// ClaimTestInventory describes what tests exist and how many.
	ClaimTestInventory ClaimClass = "test_inventory"
)

// ClaimCoverageState is how well the current evidence supports one class.
type ClaimCoverageState string

// Claim coverage states.
const (
	// ClaimSupported means the evidence class the claim needs was observed at
	// the required coverage.
	ClaimSupported ClaimCoverageState = "SUPPORTED"
	// ClaimPartial means some relevant evidence exists, but not at the class or
	// coverage the claim requires.
	ClaimPartial ClaimCoverageState = "PARTIAL"
	// ClaimUnobserved means the claim's evidence class was not observed.
	ClaimUnobserved ClaimCoverageState = "NOT_OBSERVED"
)

// ClaimRequirement is the evidence contract for one claim class. It is a
// projection hint and an audit vocabulary, not authority.
type ClaimRequirement struct {
	// Class is the claim kind.
	Class ClaimClass
	// Evidence names the observation classes that can support it.
	Evidence string
	// Scope names the coverage the evidence must cover.
	Scope string
}

// ClaimCoverage is the current status of one claim class.
type ClaimCoverage struct {
	// Class is the claim kind.
	Class ClaimClass
	// State is SUPPORTED, PARTIAL, or NOT_OBSERVED.
	State ClaimCoverageState
	// Detail names the missing evidence or the observed limitation.
	Detail string
}

// ClaimRequirements returns the evidence requirements for every claim class.
// The order is stable and matches the order in Coverage.
func ClaimRequirements() []ClaimRequirement {
	return []ClaimRequirement{
		{
			Class:    ClaimProjectIdentity,
			Evidence: "documentation, configuration/manifest, inventory, or source content",
			Scope:    "the project the answer names",
		},
		{
			Class:    ClaimTopology,
			Evidence: "configuration or a structural inventory",
			Scope:    "the named snapshot and scope",
		},
		{
			Class:    ClaimConfiguration,
			Evidence: "a configuration or manifest observation",
			Scope:    "the configuration path",
		},
		{
			Class:    ClaimImplementationStatus,
			Evidence: "source content for the named component; whole-component completeness needs a current complete source scope",
			Scope:    "each named component",
		},
		{
			Class:    ClaimDependencyUse,
			Evidence: "DECLARED < IMPORTED < INITIALIZED < USED_IN_SOURCE < OBSERVED_AT_RUNTIME",
			Scope:    "the component that owns the evidence",
		},
		{
			Class:    ClaimBehavior,
			Evidence: "source content or a live runtime observation",
			Scope:    "the named component; a named port must appear in the same observation",
		},
		{
			Class:    ClaimRuntimeState,
			Evidence: "a live runtime observation",
			Scope:    "the named component's live process",
		},
		{
			Class:    ClaimStructuralRelationship,
			Evidence: "a structural observation or a source call site binding both endpoints",
			Scope:    "both named endpoints",
		},
		{
			Class:    ClaimAbsence,
			Evidence: "current complete untruncated scope, complete inventory, and relevant query",
			Scope:    "the exact file or the complete repository scope",
		},
		{
			Class:    ClaimTestInventory,
			Evidence: "a complete, current, unscoped test inventory",
			Scope:    "the test inventory scope",
		},
	}
}

// ClaimRequirementFor returns one class's contract.
func ClaimRequirementFor(class ClaimClass) (ClaimRequirement, bool) {
	for _, requirement := range ClaimRequirements() {
		if requirement.Class == class {
			return requirement, true
		}
	}
	return ClaimRequirement{}, false
}

// Coverage derives every claim class's current status from the evidence
// summary. It is deterministic and adds no state.
func (s ClaimSummary) Coverage() []ClaimCoverage {
	out := make([]ClaimCoverage, 0, len(ClaimRequirements()))
	for _, requirement := range ClaimRequirements() {
		out = append(out, s.coverageFor(requirement.Class))
	}
	return out
}

// CoverageFor returns one claim class's current status.
func (s ClaimSummary) CoverageFor(class ClaimClass) ClaimCoverage {
	return s.coverageFor(class)
}

// coverageFor is the one status rule per class.
func (s ClaimSummary) coverageFor(class ClaimClass) ClaimCoverage {
	switch class {
	case ClaimProjectIdentity:
		if s.HasDocumentation || s.HasConfiguration || s.HasInventory || s.HasImplementation {
			return claimCoverage(class, ClaimSupported, "project identity observed")
		}
		return claimCoverage(class, ClaimUnobserved, "no documentation, configuration, inventory, or source identity observed")
	case ClaimTopology:
		if s.HasConfiguration || s.HasInventory {
			return claimCoverage(class, ClaimSupported, "topology or inventory observed")
		}
		return claimCoverage(class, ClaimUnobserved, "no configuration or structural inventory observed")
	case ClaimConfiguration:
		if s.HasConfiguration {
			return claimCoverage(class, ClaimSupported, "configuration observed")
		}
		return claimCoverage(class, ClaimUnobserved, "no configuration or manifest observed")
	case ClaimImplementationStatus:
		if s.HasImplementation {
			return claimCoverage(class, ClaimSupported, "source content observed; component coverage is audited per claim")
		}
		return claimCoverage(class, ClaimUnobserved, "no source content observed; documentation, configuration, and inventory are not implementation")
	case ClaimDependencyUse:
		switch s.DependencyLevel {
		case "RUNTIME":
			return claimCoverage(class, ClaimSupported, "dependency observed at runtime")
		case "USED_IN_SOURCE":
			return claimCoverage(class, ClaimSupported, "concrete source operation observed")
		case "IMPORTED":
			return claimCoverage(class, ClaimPartial, "import observed; a concrete operation is still missing")
		case "DECLARED":
			return claimCoverage(class, ClaimPartial, "declaration observed; import or use is still missing")
		default:
			return claimCoverage(class, ClaimUnobserved, "no dependency declaration, import, or use observed")
		}
	case ClaimBehavior:
		if s.HasRuntime {
			return claimCoverage(class, ClaimSupported, "live runtime behavior observed")
		}
		if s.HasImplementation {
			return claimCoverage(class, ClaimSupported, "source content observed; behavior and port binding are audited per claim")
		}
		return claimCoverage(class, ClaimUnobserved, "no source or runtime behavior observed")
	case ClaimRuntimeState:
		if s.HasRuntime {
			return claimCoverage(class, ClaimSupported, "live runtime observed")
		}
		return claimCoverage(class, ClaimUnobserved, "no live runtime observed; configuration and source declare what would run")
	case ClaimStructuralRelationship:
		if s.HasStructural {
			return claimCoverage(class, ClaimSupported, "structural relationship observed")
		}
		if s.HasImplementation {
			return claimCoverage(class, ClaimPartial, "source content observed; a structural or call-site binding is still missing")
		}
		return claimCoverage(class, ClaimUnobserved, "no structural or source relationship observed")
	case ClaimAbsence:
		if s.StructuralScope == "" {
			return claimCoverage(class, ClaimUnobserved, "no structural scope observed")
		}
		if s.absenceScopeComplete() {
			return claimCoverage(class, ClaimSupported, "complete current untruncated scope observed")
		}
		return claimCoverage(class, ClaimPartial, "scope, inventory, structural completeness, or truncation leaves absence unproven")
	case ClaimTestInventory:
		switch s.TestInventory {
		case "COMPLETE":
			return claimCoverage(class, ClaimSupported, "complete current unscoped test inventory observed")
		case "PARTIAL":
			return claimCoverage(class, ClaimPartial, "test inventory observed but not complete, current, or unscoped")
		default:
			return claimCoverage(class, ClaimUnobserved, "no complete test inventory observed")
		}
	}
	return claimCoverage(class, ClaimUnobserved, "unknown claim class")
}

// absenceScopeComplete reports the axes a repository-wide absence claim needs.
func (s ClaimSummary) absenceScopeComplete() bool {
	return s.StructuralScope == "COMPLETE" &&
		s.InventoryCompleteness == "COMPLETE" &&
		s.StructuralCompleteness == "COMPLETE" &&
		!s.Truncated &&
		s.ExcludedRoots == 0 &&
		s.ExcludedSourceFiles == 0 &&
		s.UnsupportedSources == 0
}

// claimCoverage constructs one status entry.
func claimCoverage(class ClaimClass, state ClaimCoverageState, detail string) ClaimCoverage {
	return ClaimCoverage{Class: class, State: state, Detail: detail}
}
