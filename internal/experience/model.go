// Package experience owns OHGO's verified execution-experience layer.
//
// It is deliberately not a general memory subsystem and not a second task,
// evidence, completion, or authority system. It consumes structured runtime
// outcomes, extracts deterministic incidents, turns them into bounded lessons
// and candidate path preferences, and applies a candidate only after validation.
//
// The hard boundary is the same one the rest of the runtime already enforces:
// experience may change which offered probe is tried first, but it may never
// grant a capability, bypass evidence, change a completion verdict, or move a
// workspace. A promoted record is advisory data, never an authority object.
package experience

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Scope is where one experience record may be applied.
type Scope string

// Experience scopes, from narrowest to broadest.
const (
	// ScopeTask applies only to one task identity. It is held in memory.
	ScopeTask Scope = "task"
	// ScopeProject applies to one repository checkout.
	ScopeProject Scope = "project"
	// ScopeRuntime applies to this local harness across projects.
	ScopeRuntime Scope = "runtime"
	// ScopeGlobal applies across repositories after explicit promotion.
	ScopeGlobal Scope = "global"
)

// Valid reports whether the scope is defined.
func (s Scope) Valid() bool {
	switch s {
	case ScopeTask, ScopeProject, ScopeRuntime, ScopeGlobal:
		return true
	default:
		return false
	}
}

// CandidateKind is the only effect class a candidate may carry.
type CandidateKind string

// Candidate kinds. These are advisory path-selection effects only.
const (
	// CandidatePreferredToolOrder moves already-offered tools earlier.
	CandidatePreferredToolOrder CandidateKind = "preferred_tool_order"
	// CandidateRecoveryHint explains a known recoverable or material failure.
	CandidateRecoveryHint CandidateKind = "recovery_hint"
	// CandidateDuplicateWarning warns that an equivalent probe was repeated.
	CandidateDuplicateWarning CandidateKind = "duplicate_warning"
	// CandidateEvidencePriority reminds the model which evidence is missing.
	CandidateEvidencePriority CandidateKind = "evidence_priority"
	// CandidateContextHint reminds the model to keep context bounded.
	CandidateContextHint CandidateKind = "context_hint"
)

// Valid reports whether the candidate kind is one this package can apply.
func (k CandidateKind) Valid() bool {
	switch k {
	case CandidatePreferredToolOrder, CandidateRecoveryHint,
		CandidateDuplicateWarning, CandidateEvidencePriority, CandidateContextHint:
		return true
	default:
		return false
	}
}

// CandidateStatus is the explicit promotion lifecycle.
//
// Observed is not a stored status: an event is an observation, and a record is
// not created until an incident and lesson have been derived. The stored
// lifecycle starts at Candidate.
type CandidateStatus string

// Record lifecycle states.
const (
	// StatusCandidate means the lesson exists but has not been validated.
	StatusCandidate CandidateStatus = "candidate"
	// StatusValidated means recurrence or regression evidence supports it.
	StatusValidated CandidateStatus = "validated"
	// StatusPromoted means it may influence future path selection.
	StatusPromoted CandidateStatus = "promoted"
	// StatusMonitored means a promoted record continued to succeed.
	StatusMonitored CandidateStatus = "monitored"
	// StatusRetired means the record is no longer applied.
	StatusRetired CandidateStatus = "retired"
	// StatusRejected means contradictory evidence prevented promotion.
	StatusRejected CandidateStatus = "rejected"
)

// Valid reports whether the status is defined.
func (s CandidateStatus) Valid() bool {
	switch s {
	case StatusCandidate, StatusValidated, StatusPromoted, StatusMonitored,
		StatusRetired, StatusRejected:
		return true
	default:
		return false
	}
}

// Active reports whether a record may influence path selection.
func (s CandidateStatus) Active() bool {
	return s == StatusPromoted || s == StatusMonitored
}

// Outcome is the terminal classification of one observed run.
type Outcome string

// Observed outcomes. They mirror the runtime's existing verdict vocabulary but
// are local data, not a second completion decision.
const (
	OutcomeCompleted   Outcome = "completed"
	OutcomeNeedsReview Outcome = "needs_review"
	OutcomePaused      Outcome = "paused"
	OutcomeFailed      Outcome = "failed"
	OutcomeCancelled   Outcome = "cancelled"
)

// Valid reports whether the outcome is defined.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeCompleted, OutcomeNeedsReview, OutcomePaused, OutcomeFailed, OutcomeCancelled:
		return true
	default:
		return false
	}
}

// Context is the deterministic condition under which a lesson applies.
//
// It intentionally does not contain the objective text. Matching on a coarse,
// structural context avoids overfitting a learned path to one sentence while
// still refusing to apply it to a different kind of task or authority.
type Context struct {
	Workspace                   string `json:"workspace,omitempty"`
	TaskKind                    string `json:"taskKind,omitempty"`
	Intent                      string `json:"intent,omitempty"`
	RepositoryScope             bool   `json:"repositoryScope,omitempty"`
	BroadRepositoryAnalysis     bool   `json:"broadRepositoryAnalysis,omitempty"`
	NeedsCodeNavigation         bool   `json:"needsCodeNavigation,omitempty"`
	NeedsExecution              bool   `json:"needsExecution,omitempty"`
	NeedsRelationshipNavigation bool   `json:"needsRelationshipNavigation,omitempty"`
	ExternalRequired            bool   `json:"externalRequired,omitempty"`
	MutationAllowed             bool   `json:"mutationAllowed,omitempty"`
}

// Key renders the stable identity of one condition.
func (c Context) Key() string {
	flag := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(c.Workspace)),
		strings.ToLower(strings.TrimSpace(c.TaskKind)),
		strings.ToLower(strings.TrimSpace(c.Intent)),
		flag(c.RepositoryScope),
		flag(c.BroadRepositoryAnalysis),
		flag(c.NeedsCodeNavigation),
		flag(c.NeedsExecution),
		flag(c.NeedsRelationshipNavigation),
		flag(c.ExternalRequired),
		flag(c.MutationAllowed),
	}, "|")
}

// Summary renders a short human-readable condition.
func (c Context) Summary() string {
	parts := make([]string, 0, 6)
	if c.Workspace != "" {
		parts = append(parts, strings.ToUpper(c.Workspace))
	}
	if c.TaskKind != "" {
		parts = append(parts, c.TaskKind)
	}
	if c.RepositoryScope {
		parts = append(parts, "repository")
	}
	if c.BroadRepositoryAnalysis {
		parts = append(parts, "broad-survey")
	}
	if c.NeedsCodeNavigation {
		parts = append(parts, "code-nav")
	}
	if c.NeedsExecution {
		parts = append(parts, "execution")
	}
	if c.NeedsRelationshipNavigation {
		parts = append(parts, "relationships")
	}
	if c.ExternalRequired {
		parts = append(parts, "external")
	}
	if c.MutationAllowed {
		parts = append(parts, "mutation")
	}
	if len(parts) == 0 {
		return "unspecified"
	}
	return strings.Join(parts, "/")
}

// Cost is the measured price of one observed path.
type Cost struct {
	ToolCalls         int   `json:"toolCalls,omitempty"`
	RepeatedToolCalls int   `json:"repeatedToolCalls,omitempty"`
	ModelTurns        int   `json:"modelTurns,omitempty"`
	InputTokens       int   `json:"inputTokens,omitempty"`
	OutputTokens      int   `json:"outputTokens,omitempty"`
	DurationMS        int64 `json:"durationMs,omitempty"`
	EvidenceGained    int   `json:"evidenceGained,omitempty"`
	Verifications     int   `json:"verifications,omitempty"`
	RecoveryAttempts  int   `json:"recoveryAttempts,omitempty"`
}

// LowerThan reports whether c is a better path cost than other.
//
// Repeated calls come first because they are the waste the runtime can remove
// without reducing evidence. A cheaper path never wins if it did not complete;
// callers compare costs only among successful observations.
func (c Cost) LowerThan(other Cost) bool {
	if c.RepeatedToolCalls != other.RepeatedToolCalls {
		return c.RepeatedToolCalls < other.RepeatedToolCalls
	}
	if c.ToolCalls != other.ToolCalls {
		return c.ToolCalls < other.ToolCalls
	}
	if c.InputTokens+c.OutputTokens != other.InputTokens+other.OutputTokens {
		return c.InputTokens+c.OutputTokens < other.InputTokens+other.OutputTokens
	}
	return c.DurationMS < other.DurationMS
}

// Event is one deterministic runtime signal extracted from an observation.
//
// Events are input to learning, not durable transcript records. They are kept
// bounded and reference the authoritative call/target rather than copying a
// tool payload.
type Event struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Key          string    `json:"key"`
	Summary      string    `json:"summary"`
	Tool         string    `json:"tool,omitempty"`
	Target       string    `json:"target,omitempty"`
	FailureClass string    `json:"failureClass,omitempty"`
	Workspace    string    `json:"workspace,omitempty"`
	TaskKind     string    `json:"taskKind,omitempty"`
	Outcome      Outcome   `json:"outcome,omitempty"`
	Cost         Cost      `json:"cost,omitempty"`
	Tools        []string  `json:"tools,omitempty"`
	NonMaterial  bool      `json:"nonMaterial,omitempty"`
	Refs         []string  `json:"refs,omitempty"`
	At           time.Time `json:"at"`
}

// Event kinds. They are stable strings because they are persisted in records.
const (
	EventRepeatedProbe      = "repeated_probe"
	EventRecoverableFailure = "recoverable_failure"
	EventMaterialFailure    = "material_failure"
	EventSuccessfulShortcut = "successful_shortcut"
	EventExpensivePath      = "expensive_path"
	EventEvidenceGap        = "evidence_gap"
	EventStaleReview        = "stale_review"
	EventHandoffRedundancy  = "handoff_redundancy"
	EventAuthorityRefusal   = "authority_refusal"
	EventContextGrowth      = "context_growth"
)

// Incident is the structured pattern recognized in one observation.
type Incident struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	Kind         string    `json:"kind"`
	Summary      string    `json:"summary"`
	Context      Context   `json:"context"`
	Tool         string    `json:"tool,omitempty"`
	Target       string    `json:"target,omitempty"`
	FailureClass string    `json:"failureClass,omitempty"`
	Evidence     []string  `json:"evidence,omitempty"`
	Cost         Cost      `json:"cost,omitempty"`
	Occurrences  int       `json:"occurrences,omitempty"`
	FirstSeen    time.Time `json:"firstSeen,omitempty"`
	LastSeen     time.Time `json:"lastSeen,omitempty"`
}

// Lesson is the bounded generalization derived from an incident.
type Lesson struct {
	ID         string         `json:"id"`
	Key        string         `json:"key"`
	Kind       string         `json:"kind"`
	Statement  string         `json:"statement"`
	Context    Context        `json:"context"`
	Regression RegressionCase `json:"regression"`
	At         time.Time      `json:"at"`
}

// Candidate is a proposed future path preference. It is not policy.
type Candidate struct {
	ID                string          `json:"id"`
	LessonID          string          `json:"lessonId"`
	Key               string          `json:"key"`
	Kind              CandidateKind   `json:"kind"`
	Statement         string          `json:"statement"`
	Hint              string          `json:"hint,omitempty"`
	PreferredTools    []string        `json:"preferredTools,omitempty"`
	Context           Context         `json:"context"`
	Scope             Scope           `json:"scope"`
	ScopeKey          string          `json:"scopeKey,omitempty"`
	Status            CandidateStatus `json:"status"`
	Observations      int             `json:"observations,omitempty"`
	Contradictions    int             `json:"contradictions,omitempty"`
	LastContradiction string          `json:"lastContradiction,omitempty"`
	Cost              Cost            `json:"cost,omitempty"`
	Validation        []Validation    `json:"validation,omitempty"`
	Regression        RegressionCase  `json:"regression"`
	LastValidatedAt   time.Time       `json:"lastValidatedAt,omitempty"`
	PromotedAt        *time.Time      `json:"promotedAt,omitempty"`
	RetiredAt         *time.Time      `json:"retiredAt,omitempty"`
	RetirementReason  string          `json:"retirementReason,omitempty"`
	Transitions       []Transition    `json:"transitions,omitempty"`
}

// ValidationKind classifies one piece of promotion evidence.
type ValidationKind string

// Validation kinds.
const (
	ValidationRecurrence    ValidationKind = "recurrence"
	ValidationRegression    ValidationKind = "regression"
	ValidationContradiction ValidationKind = "contradiction"
	ValidationArchitecture  ValidationKind = "architecture"
)

// Valid reports whether the validation kind is defined.
func (k ValidationKind) Valid() bool {
	switch k {
	case ValidationRecurrence, ValidationRegression, ValidationContradiction, ValidationArchitecture:
		return true
	default:
		return false
	}
}

// Validation is one auditable promotion check.
type Validation struct {
	Kind         ValidationKind `json:"kind"`
	Ref          string         `json:"ref,omitempty"`
	Passed       bool           `json:"passed"`
	Observations int            `json:"observations,omitempty"`
	At           time.Time      `json:"at"`
}

// RegressionCase is the deterministic regression coverage linked to a lesson.
// The runtime stores the reference; the repository test suite executes it.
type RegressionCase struct {
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Transition is one auditable promotion lifecycle move.
type Transition struct {
	From   CandidateStatus `json:"from,omitempty"`
	To     CandidateStatus `json:"to"`
	Reason string          `json:"reason,omitempty"`
	At     time.Time       `json:"at"`
}

// Record is one durable candidate and its complete explanation trail.
type Record struct {
	ID        string    `json:"id"`
	Incident  Incident  `json:"incident"`
	Lesson    Lesson    `json:"lesson"`
	Candidate Candidate `json:"candidate"`
	Scope     Scope     `json:"scope"`
	ScopeKey  string    `json:"scopeKey,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Active reports whether the record may influence path selection.
func (r Record) Active() bool { return r.Candidate.Status.Active() }

// Explanation is the read-only trace exposed to an operator.
type Explanation struct {
	Record          Record         `json:"record"`
	Applied         bool           `json:"applied"`
	Regression      RegressionCase `json:"regression"`
	LastValidatedAt time.Time      `json:"lastValidatedAt,omitempty"`
	Contradictions  []string       `json:"contradictions,omitempty"`
}

// Observation is the structured runtime state one learning pass consumes.
//
// It is supplied by the host at a turn boundary. It deliberately contains no
// transcript messages: turns are the loop's structured outcomes, and evidence
// is the runtime's own bounded state.
type Observation struct {
	TaskID            string
	ProjectID         string
	SessionID         string
	Workspace         string
	PriorWorkspace    string
	Intent            contract.IntentContract
	Shape             cognition.TaskShape
	Turns             []agent.Turn
	Projection        agent.ProjectionReport
	Evidence          *cognition.EvidenceState
	PriorEvidence     *cognition.EvidenceState
	Outcome           Outcome
	Reason            string
	PriorStatus       string
	PriorReviewReason string
	StartedAt         time.Time
	FinishedAt        time.Time
	Scope             Scope
}

// ContextFor derives the stable context from an observation.
func ContextFor(obs Observation) Context {
	shape := obs.Shape
	workspace := strings.ToLower(strings.TrimSpace(obs.Workspace))
	if workspace == "" {
		workspace = strings.ToLower(strings.TrimSpace(shape.Authority.Workspace))
	}
	kind := string(shape.Kind)
	if kind == "" {
		kind = string(obs.Intent.Kind)
	}
	intent := strings.ToLower(strings.TrimSpace(shape.Intent))
	if intent == "" && obs.Intent.Grounding != nil {
		intent = string(obs.Intent.Grounding.Action)
	}
	return Context{
		Workspace:                   workspace,
		TaskKind:                    strings.ToLower(strings.TrimSpace(kind)),
		Intent:                      intent,
		RepositoryScope:             shape.RepositoryScope,
		BroadRepositoryAnalysis:     shape.BroadRepositoryAnalysis,
		NeedsCodeNavigation:         shape.NeedsCodeNavigation,
		NeedsExecution:              shape.NeedsExecution,
		NeedsRelationshipNavigation: shape.NeedsRelationshipNavigation,
		ExternalRequired:            shape.External.Required,
		MutationAllowed:             shape.Mutation.Allowed,
	}
}

// ContextForShape derives the same context at advice time.
func ContextForShape(shape cognition.TaskShape, workspace string) Context {
	return ContextFor(Observation{Shape: shape, Workspace: workspace})
}

// stableID renders a bounded stable identity for an event or record.
func stableID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + ":" + hex.EncodeToString(sum[:8])
}

// boundText trims and bounds a persisted string.
func boundText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && (text[cut]&0xc0) == 0x80 {
		cut--
	}
	return strings.TrimSpace(text[:cut])
}

// uniqueStrings returns unique, non-empty values in order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// sortScopeKey orders scopes from narrowest to broadest.
func scopeRank(scope Scope) int {
	switch scope {
	case ScopeTask:
		return 0
	case ScopeProject:
		return 1
	case ScopeRuntime:
		return 2
	case ScopeGlobal:
		return 3
	default:
		return 4
	}
}
