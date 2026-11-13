package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// ProjectionRequest is the runtime state a projection may use to build the next
// model request.
//
// The field set is deliberately narrow. A projector sees the full live
// conversation, the completed turns, the tool surface, and the current system
// blocks. It does not see authority decisions, budget internals, or the
// security policy: it may narrow the action space, never authorize it.
type ProjectionRequest struct {
	// Messages is the complete live conversation for this run, including the
	// current user turn and every tool result. A projector must not mutate it.
	Messages []Message
	// Turns are the completed turns, in order.
	Turns []Turn
	// Turn is how many turns have completed before this request.
	Turn int
	// Tools is every tool the loop was constructed with.
	Tools []Tool
	// System is the base system prompt for this request.
	System []ai.SystemPromptBlock
	// ForceAnswer asks for the answer boundary even when the task shape would
	// otherwise keep probing. The loop sets it after repeated refusal-only
	// turns: every proposed action was withheld, so no proposal can make
	// progress and the only remaining output is prose. The projector should
	// close the tool surface and say so; it must not widen authority.
	ForceAnswer bool
}

// ProjectionResult is the view of the next request a projector returns.
//
// It is a view, never state: the loop keeps the full live conversation and the
// full tool set, and applies the projection only to the request it is about to
// send. A projector that returns nil Messages or nil Tools leaves that field
// unchanged; an empty, non-nil slice means "none". System blocks are appended
// after the base prompt.
type ProjectionResult struct {
	// Messages replaces the live conversation for this request when non-nil.
	Messages []Message
	// Tools replaces the offered tool set for this request when non-nil.
	Tools []Tool
	// System is appended after the base system prompt.
	System []ai.SystemPromptBlock
	// Refusals maps a call signature (agent.CallSignature) to the runtime
	// reason it must not execute again. It is how the control plane's failure
	// memory reaches the execution boundary: a repeated failed proposal is
	// refused before any tool runs, while a different proposal stays possible.
	// A refusal narrows the action space and never grants authority.
	Refusals map[string]string
	// Report describes what the projection did, for audit.
	Report ProjectionReport
}

// ProjectionReport is the explainable record of one projection.
//
// It answers the questions a context audit needs: which tool results were
// elided and why, which tools were offered or withheld, how large the request
// was before and after, and what evidence state supported the decision.
type ProjectionReport struct {
	// Applied reports whether a projector ran.
	Applied bool `json:"applied"`
	// Turn is the completed-turn index the projection preceded.
	Turn int `json:"turn"`
	// Reason explains a no-op or fallback.
	Reason string `json:"reason,omitempty"`
	// ElidedToolResults counts tool results replaced by bounded evidence.
	ElidedToolResults int `json:"elided_tool_results"`
	// KeptToolResults counts tool results sent verbatim.
	KeptToolResults int `json:"kept_tool_results"`
	// StateBlockBytes is the size of the appended runtime state block.
	StateBlockBytes int `json:"state_block_bytes,omitempty"`
	// Messages is the number of messages in the projected request.
	Messages int `json:"messages,omitempty"`
	// BytesBefore and BytesAfter are the estimated message bytes.
	BytesBefore int `json:"bytes_before"`
	BytesAfter  int `json:"bytes_after"`
	// ToolNames is the offered tool set.
	ToolNames []string `json:"tool_names,omitempty"`
	// Excluded names the tools withheld and why.
	Excluded []CandidateExclusion `json:"excluded,omitempty"`
	// Sufficiency is the evidence-sufficiency verdict.
	Sufficiency string `json:"sufficiency,omitempty"`
	// Stage is the projected task stage.
	Stage string `json:"stage,omitempty"`
	// Progress classifies how the last completed turn changed the task:
	// "advancing", "stalled", or empty when the run has not been judged yet.
	// It is audit data for the semantic no-progress bound, never authority.
	Progress string `json:"progress,omitempty"`
	// NoProgressTurns is the number of consecutive completed turns that did
	// not advance the task boundary. It resets on any meaningful transition.
	NoProgressTurns int `json:"no_progress_turns,omitempty"`
	// AnswerEligible reports that the runtime already holds the evidence an
	// informational objective requires. The next visible assistant text is a
	// candidate final answer, so tool calls emitted alongside it are
	// continuation after the answer, not new evidence. The loop refuses those
	// calls instead of executing them.
	AnswerEligible bool `json:"answer_eligible,omitempty"`
	// Terminal reports that the runtime has closed the task. The loop must not
	// start another unrestricted reasoning cycle after the current request.
	Terminal bool `json:"terminal,omitempty"`
	// StopReason explains a terminal verdict in evidence terms.
	StopReason string `json:"stop_reason,omitempty"`
	// Provenance summarizes what kind of evidence the answer may rely on:
	// observed implementation, documentation, or external content. Completion
	// uses it to keep documentation claims distinguishable from observed facts.
	Provenance EvidenceProvenance `json:"provenance,omitempty"`
	// Shape is a one-line task-shape summary.
	Shape string `json:"shape,omitempty"`
	// Evidence is a bounded evidence-state summary.
	Evidence string `json:"evidence,omitempty"`
	// Elisions records each replaced tool result.
	Elisions []ProjectionElision `json:"elisions,omitempty"`
	// RetentionPolicy describes the bounds that decided what stayed exact.
	RetentionPolicy string `json:"retention_policy,omitempty"`
	// ExperienceApplied names the promoted experience records that influenced
	// this projection, for audit. It is never authority.
	ExperienceApplied []string `json:"experience_applied,omitempty"`
	// ExperienceHints are the bounded advisory lines appended to the runtime
	// state block. They are guidance, never a capability or completion claim.
	ExperienceHints []string `json:"experience_hints,omitempty"`
	// RequiredCapabilities names the capabilities the objective needs, in
	// shape order. It is derived from the completion contract, never from
	// model output.
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	// AvailableOperations names what the constructed tool set can perform.
	// It is the availability fact the preflight compared against.
	AvailableOperations []string `json:"available_operations,omitempty"`
	// MissingCapabilities names the required capabilities the runtime cannot
	// perform. Non-empty means the loop must not send this request: the
	// objective cannot be executed, and a model turn would only discover it.
	MissingCapabilities []string `json:"missing_capabilities,omitempty"`
}

// ProjectionElision is one tool-result replacement in a projection.
type ProjectionElision struct {
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
	Policy string `json:"policy"`
	Before int    `json:"before_bytes"`
	After  int    `json:"after_bytes"`
}

// EvidenceReview converts a projection's sufficiency verdict into a
// completion veto. An empty string means the runtime had enough evidence for
// the request it sent; any other verdict means a model that answered anyway
// did so without the evidence the task required.
//
// The mapping lives here, on the report both the UI and headless paths already
// carry, so no host has to parse audit text or duplicate the vocabulary.
func (r ProjectionReport) EvidenceReview() string {
	// A terminal projection carries the runtime's own reason for closing the
	// task. It is more specific than the sufficiency category, and it is the
	// reason a review must preserve (a stalled run must not be reported as a
	// generic evidence gap or as a resource limit).
	if r.Terminal && strings.TrimSpace(r.StopReason) != "" {
		return r.StopReason
	}
	switch r.Sufficiency {
	case "", "SUFFICIENT":
		return ""
	}
	review := "runtime evidence was not sufficient for the objective"
	if r.Sufficiency != "" {
		review += " (" + r.Sufficiency + ")"
	}
	if r.Stage != "" {
		review += " at stage " + r.Stage
	}
	return review
}

// CandidateExclusion is one tool withheld from the model, with the runtime
// reason. It is audit data, never authority: the security boundary still
// evaluates every call that remains.
type CandidateExclusion struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

// EvidenceProvenance is what a task's evidence can truthfully support.
//
// The distinction is load-bearing: a README is evidence of what the project
// says about itself, while a source read is evidence of what the implementation
// does. DocumentationTerms are the answer terms that appear in documentation
// but not in any observed implementation, so a completion check can require
// attribution instead of silently presenting a README claim as a repository
// fact. Nothing here is authority or proof by itself.
type EvidenceProvenance struct {
	// RequireAttribution reports that the objective is a broad repository
	// account, so documentation-only claims must be attributed.
	RequireAttribution bool `json:"require_attribution,omitempty"`
	// HasImplementation reports at least one observed source-content fact. A
	// manifest, configuration file, README, directory listing, or structural
	// inventory does not satisfy it: none of them proves execution.
	HasImplementation bool `json:"has_implementation,omitempty"`
	// HasConfiguration reports at least one configuration or manifest
	// observation. It declares intended topology or dependencies.
	HasConfiguration bool `json:"has_configuration,omitempty"`
	// HasDocumentation reports at least one documentation observation.
	HasDocumentation bool `json:"has_documentation,omitempty"`
	// HasRuntimeObservation reports a live runtime observation: a service
	// reached readiness and answered a check while alive. A finished build or
	// test is not a runtime observation.
	HasRuntimeObservation bool `json:"has_runtime_observation,omitempty"`
	// StructuralScope is COMPLETE, PARTIAL, or empty when no structural
	// observation exists.
	StructuralScope string `json:"structural_scope,omitempty"`
	// TestInventory is COMPLETE, PARTIAL, or NOT_QUERIED.
	TestInventory string `json:"test_inventory,omitempty"`
	// TestFiles is the observed snapshot test-file count, when known.
	TestFiles int `json:"test_files,omitempty"`
	// TestFilesKnown distinguishes an observed zero from an unknown count.
	TestFilesKnown bool `json:"test_files_known,omitempty"`
	// TestTotal is the observed test-symbol count, when known.
	TestTotal int `json:"test_total,omitempty"`
	// TestTotalKnown distinguishes an observed zero from an unknown count.
	TestTotalKnown bool `json:"test_total_known,omitempty"`
	// ImplementationPaths, ConfigurationPaths, and DocumentationPaths are
	// bounded coordinates for the observed claim classes.
	ImplementationPaths []string `json:"implementation_paths,omitempty"`
	ConfigurationPaths  []string `json:"configuration_paths,omitempty"`
	DocumentationPaths  []string `json:"documentation_paths,omitempty"`
	// ExternalRequired reports that the request explicitly needs external
	// knowledge.
	ExternalRequired bool `json:"external_required,omitempty"`
	// ExternalObserved reports at least one successful external observation.
	ExternalObserved bool `json:"external_observed,omitempty"`
	// DocumentationTerms are bounded lower-case answer terms that appear only
	// in documentation evidence. They are audit data, never authority.
	DocumentationTerms []string `json:"documentation_terms,omitempty"`
}
