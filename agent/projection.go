package agent

import "github.com/PizenLabs/ohgo/ai"

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
	// Shape is a one-line task-shape summary.
	Shape string `json:"shape,omitempty"`
	// Evidence is a bounded evidence-state summary.
	Evidence string `json:"evidence,omitempty"`
	// Elisions records each replaced tool result.
	Elisions []ProjectionElision `json:"elisions,omitempty"`
	// RetentionPolicy describes the bounds that decided what stayed exact.
	RetentionPolicy string `json:"retention_policy,omitempty"`
}

// ProjectionElision is one tool-result replacement in a projection.
type ProjectionElision struct {
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
	Policy string `json:"policy"`
	Before int    `json:"before_bytes"`
	After  int    `json:"after_bytes"`
}

// CandidateExclusion is one tool withheld from the model, with the runtime
// reason. It is audit data, never authority: the security boundary still
// evaluates every call that remains.
type CandidateExclusion struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}
