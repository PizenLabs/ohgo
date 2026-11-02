package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// ContextVersion is the schema version of the task-context subdocument stored
// inside progress.json.
//
// The subdocument is additive and self-versioned so adding it does not change
// the meaning of the surrounding checkpoint. An older reader ignores the
// unknown JSON field; a newer reader refuses a context version it does not
// understand instead of guessing at its shape, and the task remains
// recoverable without the structured context.
const ContextVersion = 1

// Bounds keep task context a briefing, never a second transcript.
const (
	// MaxFacts bounds the verified-fact log.
	MaxFacts = 128
	// MaxHypotheses bounds the hypothesis log, including disposed records.
	MaxHypotheses = 128
	// MaxEvidenceRecords bounds task-linked evidence records.
	MaxEvidenceRecords = 256
	// MaxQuestions bounds the question log.
	MaxQuestions = 64
	// MaxRoleNotes bounds each role-context list.
	MaxRoleNotes = 16
	// maxFactBytes bounds one fact or hypothesis statement.
	maxFactBytes = 1024
	// maxEvidenceSummaryBytes bounds one evidence summary. Evidence is a
	// reference, not a copy of a tool result.
	maxEvidenceSummaryBytes = 512
	// maxEvidenceRefBytes bounds a source, artifact, or dedup reference.
	maxEvidenceRefBytes = 512
)

// ErrInvalidContext means the structured task context violates an invariant.
var ErrInvalidContext = errors.New("task: invalid task context")

// ErrUnverifiedClaim means a statement cannot be promoted because no current,
// usable evidence supports it.
var ErrUnverifiedClaim = errors.New("task: claim requires current supporting evidence")

// ErrUnknownRecord means a referenced fact, hypothesis, evidence record, or
// question does not exist.
var ErrUnknownRecord = errors.New("task: unknown context record")

// ErrContextLimit means the bounded context log is full. Records are never
// silently evicted: a caller must invalidate or supersede, or the human must
// start a new task.
var ErrContextLimit = errors.New("task: task context record limit reached")

// ErrNoActiveTask means a controller operation needs an active task.
var ErrNoActiveTask = errors.New("task: no active task")

// EvidenceKind classifies where an observation came from.
type EvidenceKind string

// Evidence kinds.
const (
	EvidenceTool         EvidenceKind = "tool"
	EvidenceCommand      EvidenceKind = "command"
	EvidenceTest         EvidenceKind = "test"
	EvidenceBenchmark    EvidenceKind = "benchmark"
	EvidenceSource       EvidenceKind = "source"
	EvidenceArtifact     EvidenceKind = "artifact"
	EvidenceReproduction EvidenceKind = "reproduction"
	EvidenceExternal     EvidenceKind = "external"
	EvidenceHuman        EvidenceKind = "human"
)

// Valid reports whether the evidence kind is defined.
func (k EvidenceKind) Valid() bool {
	switch k {
	case EvidenceTool, EvidenceCommand, EvidenceTest, EvidenceBenchmark,
		EvidenceSource, EvidenceArtifact, EvidenceReproduction, EvidenceExternal,
		EvidenceHuman:
		return true
	default:
		return false
	}
}

// EvidenceOutcome is what an observation established.
//
// It is deliberately separate from EvidenceValidity: an inconclusive
// observation can still be an honest record of what was attempted, and a
// formerly observed result can later be invalidated without rewriting the
// observation itself.
type EvidenceOutcome string

// Evidence outcomes.
const (
	// EvidenceObserved is a direct observation (a command result, a file
	// window, a test run).
	EvidenceObserved EvidenceOutcome = "observed"
	// EvidenceSupporting records evidence whose purpose is to support a claim.
	EvidenceSupporting EvidenceOutcome = "supporting"
	// EvidenceContradicting records evidence that contradicts one or more
	// named facts or hypotheses. It never overwrites them.
	EvidenceContradicting EvidenceOutcome = "contradicting"
	// EvidenceInconclusive records an incomplete or unusable result. It can
	// never support a verified fact or confirm a hypothesis.
	EvidenceInconclusive EvidenceOutcome = "inconclusive"
)

// Valid reports whether the outcome is defined.
func (o EvidenceOutcome) Valid() bool {
	switch o {
	case EvidenceObserved, EvidenceSupporting, EvidenceContradicting,
		EvidenceInconclusive:
		return true
	default:
		return false
	}
}

// EvidenceValidity is whether an evidence record may still be relied on.
type EvidenceValidity string

// Evidence validity states.
const (
	// EvidenceCurrent means the record still applies.
	EvidenceCurrent EvidenceValidity = "current"
	// EvidenceSuperseded means a later observation replaced it.
	EvidenceSuperseded EvidenceValidity = "superseded"
	// EvidenceInvalid means it no longer applies (for example, the result was
	// produced against a different revision).
	EvidenceInvalid EvidenceValidity = "invalid"
)

// Valid reports whether the validity is defined.
func (v EvidenceValidity) Valid() bool {
	switch v {
	case EvidenceCurrent, EvidenceSuperseded, EvidenceInvalid:
		return true
	default:
		return false
	}
}

// EvidenceRecord is one task-linked reference to an observed result.
//
// It stores a bounded summary and a reference to the durable result, never a
// copied tool payload. Support is the fact's or hypothesis's EvidenceIDs; this
// record only names claims it contradicts, so the support relation has exactly
// one owner.
type EvidenceRecord struct {
	// ID is a stable identity for the life of the task.
	ID string `json:"id"`
	// TaskID is the task this evidence belongs to.
	TaskID string `json:"taskId"`
	// Kind classifies the origin of the observation.
	Kind EvidenceKind `json:"kind"`
	// Role is the execution role that produced it.
	Role workspace.Workspace `json:"role"`
	// Source is the execution reference: tool/call id, command, or path.
	Source string `json:"source,omitempty"`
	// Scope is the repository-relative coverage a check reported, when the
	// record is a verification. Empty means the scope was not recorded.
	Scope string `json:"scope,omitempty"`
	// Artifact references a persisted result or artifact. It is a pointer, not
	// a copy of the artifact.
	Artifact string `json:"artifact,omitempty"`
	// Summary is the bounded human- and model-readable description.
	Summary string `json:"summary"`
	// Outcome is what the observation established.
	Outcome EvidenceOutcome `json:"outcome"`
	// Validity says whether the record may still be relied on.
	Validity EvidenceValidity `json:"validity"`
	// Contradicts names facts or hypotheses this evidence contradicts.
	Contradicts []string `json:"contradicts,omitempty"`
	// DedupKey collapses repeated observations of the same result. It is an
	// identity for recording, never authority.
	DedupKey string `json:"dedupKey,omitempty"`
	// Digest optionally records an integrity value for the referenced result.
	Digest string `json:"digest,omitempty"`
	// Note explains an invalidation or supersession, when one applies.
	Note string `json:"note,omitempty"`
	// At is when the observation was recorded.
	At time.Time `json:"at"`
}

// FactStatus is where one verified fact is in its lifecycle.
type FactStatus string

// Fact states.
const (
	// FactActive means the fact is currently supported by usable evidence.
	FactActive FactStatus = "active"
	// FactSuperseded means a later fact replaced it. The record is retained.
	FactSuperseded FactStatus = "superseded"
	// FactInvalidated means its supporting evidence no longer applies. The
	// record is retained.
	FactInvalidated FactStatus = "invalidated"
)

// Valid reports whether the fact status is defined.
func (s FactStatus) Valid() bool {
	switch s {
	case FactActive, FactSuperseded, FactInvalidated:
		return true
	default:
		return false
	}
}

// Fact is a verified statement: information the runtime holds because
// observable evidence supports it.
//
// A model assertion never becomes a Fact by being recorded. Only RecordFact
// and ConfirmHypothesis create one, and both require current supporting
// evidence. A passing test proves the recorded test passed under its recorded
// conditions; it never completes the task by itself.
type Fact struct {
	// ID is stable for the life of the task.
	ID string `json:"id"`
	// TaskID is the task this fact belongs to.
	TaskID string `json:"taskId"`
	// Text is the verified statement.
	Text string `json:"text"`
	// Role is the execution role that established it.
	Role workspace.Workspace `json:"role"`
	// EvidenceIDs are the supporting evidence records.
	EvidenceIDs []string `json:"evidenceIds,omitempty"`
	// Status is the current disposition.
	Status FactStatus `json:"status"`
	// FromHypothesis is the hypothesis this fact was confirmed from, when any.
	FromHypothesis string `json:"fromHypothesis,omitempty"`
	// SupersededBy names the fact that replaced this one.
	SupersededBy string `json:"supersededBy,omitempty"`
	// At is when the fact was established.
	At time.Time `json:"at"`
}

// HypothesisStatus is where one unverified statement is in its lifecycle.
type HypothesisStatus string

// Hypothesis states.
const (
	// HypothesisActive is a proposed explanation that has not been resolved.
	HypothesisActive HypothesisStatus = "active"
	// HypothesisConfirmed is an active hypothesis confirmed by evidence. It
	// still remains distinguishable from a directly observed Fact.
	HypothesisConfirmed HypothesisStatus = "confirmed"
	// HypothesisRejected was disproved or abandoned with a recorded reason.
	HypothesisRejected HypothesisStatus = "rejected"
	// HypothesisSuperseded was replaced by another hypothesis.
	HypothesisSuperseded HypothesisStatus = "superseded"
	// HypothesisInvalidated lost its supporting evidence after confirmation.
	HypothesisInvalidated HypothesisStatus = "invalidated"
)

// Valid reports whether the hypothesis status is defined.
func (s HypothesisStatus) Valid() bool {
	switch s {
	case HypothesisActive, HypothesisConfirmed, HypothesisRejected,
		HypothesisSuperseded, HypothesisInvalidated:
		return true
	default:
		return false
	}
}

// Hypothesis is a proposed explanation, assumption, or prediction that is not
// yet a verified fact.
type Hypothesis struct {
	// ID is stable for the life of the task.
	ID string `json:"id"`
	// TaskID is the task this hypothesis belongs to.
	TaskID string `json:"taskId"`
	// Text is the proposed explanation.
	Text string `json:"text"`
	// Role is the role that proposed it.
	Role workspace.Workspace `json:"role"`
	// Status is the current disposition.
	Status HypothesisStatus `json:"status"`
	// EvidenceIDs are evidence records offered for or against it. Only
	// current, usable evidence can confirm it.
	EvidenceIDs []string `json:"evidenceIds,omitempty"`
	// Resolution explains a confirmation, rejection, supersession, or
	// invalidation.
	Resolution string `json:"resolution,omitempty"`
	// ResolvedBy is the role that resolved it, when one did.
	ResolvedBy workspace.Workspace `json:"resolvedBy,omitempty"`
	// ResolvedAt is when it was resolved.
	ResolvedAt time.Time `json:"resolvedAt,omitempty"`
	// SupersededBy names the hypothesis that replaced this one.
	SupersededBy string `json:"supersededBy,omitempty"`
	// At is when the hypothesis was proposed.
	At time.Time `json:"at"`
}

// QuestionStatus is whether a recorded question is still outstanding.
type QuestionStatus string

// Question states.
const (
	// QuestionOpen is an outstanding question.
	QuestionOpen QuestionStatus = "open"
	// QuestionAnswered has a recorded answer.
	QuestionAnswered QuestionStatus = "answered"
)

// Valid reports whether the question status is defined.
func (s QuestionStatus) Valid() bool {
	switch s {
	case QuestionOpen, QuestionAnswered:
		return true
	default:
		return false
	}
}

// Question is an open or answered question associated with the task.
type Question struct {
	// ID is stable for the life of the task.
	ID string `json:"id"`
	// TaskID is the task this question belongs to.
	TaskID string `json:"taskId"`
	// Text is the question.
	Text string `json:"text"`
	// Role is the role that raised it.
	Role workspace.Workspace `json:"role"`
	// Status is whether it is still outstanding.
	Status QuestionStatus `json:"status"`
	// Answer is the recorded answer, when one exists.
	Answer string `json:"answer,omitempty"`
	// AnsweredBy is the role that answered it, when one did.
	AnsweredBy workspace.Workspace `json:"answeredBy,omitempty"`
	// AnsweredAt is when it was answered.
	AnsweredAt time.Time `json:"answeredAt,omitempty"`
	// At is when the question was raised.
	At time.Time `json:"at"`
}

// RoleContext is one role's execution context for the shared task.
//
// It references shared facts and hypotheses by ID instead of duplicating them.
// It is never authority: recording a plan or a mutation scope here does not
// permit an operation. The workspace and security gates remain the only
// authority paths.
type RoleContext struct {
	// Role identifies the execution role this context belongs to.
	Role workspace.Workspace `json:"role"`
	// UpdatedAt is when this role last touched the task.
	UpdatedAt time.Time `json:"updatedAt"`
	// Reasoning is ASK's reasoning continuity.
	Reasoning []string `json:"reasoning,omitempty"`
	// FactIDs are ASK's read-only findings, by reference to shared facts.
	FactIDs []string `json:"factIds,omitempty"`
	// Strategy is FUZZ's investigation strategy.
	Strategy string `json:"strategy,omitempty"`
	// HypothesisIDs are FUZZ's exploratory hypotheses, by reference.
	HypothesisIDs []string `json:"hypothesisIds,omitempty"`
	// Progress is FUZZ's reproduction progress or BUILD's verification
	// progress.
	Progress []string `json:"progress,omitempty"`
	// Artifacts references benchmarks, experiments, or patches.
	Artifacts []string `json:"artifacts,omitempty"`
	// Plan is BUILD's implementation plan.
	Plan []string `json:"plan,omitempty"`
	// Scope is BUILD's intended mutation scope. It is a plan, never a grant.
	Scope []string `json:"scope,omitempty"`
}

// RoleContextUpdate appends to one role's context. Empty fields leave the
// corresponding list unchanged.
type RoleContextUpdate struct {
	// Role must match the target role when set.
	Role workspace.Workspace
	// Reasoning appends ASK reasoning notes.
	Reasoning []string
	// FactIDs appends references to shared facts.
	FactIDs []string
	// Strategy replaces FUZZ's one-line strategy when non-empty.
	Strategy string
	// HypothesisIDs appends references to shared hypotheses.
	HypothesisIDs []string
	// Progress appends role progress notes.
	Progress []string
	// Artifacts appends references to role artifacts.
	Artifacts []string
	// Plan appends BUILD plan steps.
	Plan []string
	// Scope appends BUILD mutation-scope paths.
	Scope []string
}

// SharedContext is the role-independent task context plus the per-role
// execution contexts associated with the same task identity.
type SharedContext struct {
	// Version is the context subdocument schema version.
	Version int `json:"version,omitempty"`
	// Facts are verified, evidence-backed statements.
	Facts []Fact `json:"facts,omitempty"`
	// Hypotheses are unverified proposals and their dispositions.
	Hypotheses []Hypothesis `json:"hypotheses,omitempty"`
	// Evidence is the task-linked evidence index.
	Evidence []EvidenceRecord `json:"evidence,omitempty"`
	// Roles are the per-role execution contexts, in canonical role order.
	Roles []RoleContext `json:"roles,omitempty"`
	// Questions are outstanding or answered questions about the objective.
	Questions []Question `json:"questions,omitempty"`
}

// EvidenceInput describes one observation to record.
type EvidenceInput struct {
	// ID optionally supplies a stable identity. Empty mints one.
	ID string
	// TaskID optionally asserts the owning task; a mismatch is refused.
	TaskID string
	// Kind classifies the observation.
	Kind EvidenceKind
	// Role is the originating execution role. Empty uses the controller's
	// active role.
	Role workspace.Workspace
	// Source is a tool/call id, command, or path.
	Source string
	// Scope is the repository-relative coverage a check reported, when the
	// observation is a verification.
	Scope string
	// Artifact references a persisted result.
	Artifact string
	// Summary is the bounded description.
	Summary string
	// Outcome is what the observation established.
	Outcome EvidenceOutcome
	// Contradicts names facts or hypotheses contradicted by this evidence.
	Contradicts []string
	// DedupKey collapses a repeated observation of the same result.
	DedupKey string
	// Digest optionally records an integrity value.
	Digest string
	// At is the observation time. Zero uses the current time.
	At time.Time
}

// FactInput describes one evidence-backed fact to record.
type FactInput struct {
	// Text is the verified statement.
	Text string
	// Role is the role that established it. Empty uses the controller's role.
	Role workspace.Workspace
	// EvidenceIDs are the supporting evidence records.
	EvidenceIDs []string
	// FromHypothesis optionally names the confirmed hypothesis.
	FromHypothesis string
	// At is the establishment time. Zero uses the current time.
	At time.Time
}

// HypothesisInput describes one unverified proposal to record.
type HypothesisInput struct {
	// Text is the proposed explanation.
	Text string
	// Role is the proposing role. Empty uses the controller's role.
	Role workspace.Workspace
	// At is the proposal time. Zero uses the current time.
	At time.Time
}

// Clone returns a deep copy, so a checkpoint cannot alias the live context.
func (c SharedContext) Clone() SharedContext {
	out := c
	out.Facts = make([]Fact, len(c.Facts))
	for i, fact := range c.Facts {
		out.Facts[i] = fact
		out.Facts[i].EvidenceIDs = append([]string(nil), fact.EvidenceIDs...)
	}
	out.Hypotheses = make([]Hypothesis, len(c.Hypotheses))
	for i, hypothesis := range c.Hypotheses {
		out.Hypotheses[i] = hypothesis
		out.Hypotheses[i].EvidenceIDs = append([]string(nil), hypothesis.EvidenceIDs...)
	}
	out.Evidence = make([]EvidenceRecord, len(c.Evidence))
	for i, evidence := range c.Evidence {
		out.Evidence[i] = evidence
		out.Evidence[i].Contradicts = append([]string(nil), evidence.Contradicts...)
	}
	out.Roles = make([]RoleContext, len(c.Roles))
	for i, role := range c.Roles {
		out.Roles[i] = role
		out.Roles[i].Reasoning = append([]string(nil), role.Reasoning...)
		out.Roles[i].FactIDs = append([]string(nil), role.FactIDs...)
		out.Roles[i].HypothesisIDs = append([]string(nil), role.HypothesisIDs...)
		out.Roles[i].Progress = append([]string(nil), role.Progress...)
		out.Roles[i].Artifacts = append([]string(nil), role.Artifacts...)
		out.Roles[i].Plan = append([]string(nil), role.Plan...)
		out.Roles[i].Scope = append([]string(nil), role.Scope...)
	}
	out.Questions = make([]Question, len(c.Questions))
	copy(out.Questions, c.Questions)
	return out
}

// Empty reports whether no structured context has been recorded.
func (c SharedContext) Empty() bool {
	return len(c.Facts) == 0 && len(c.Hypotheses) == 0 &&
		len(c.Evidence) == 0 && len(c.Roles) == 0 && len(c.Questions) == 0
}

// Validate reports whether the context is internally consistent.
//
// It is strict: a persisted context that violates a reference or a status
// invariant must not be written, because a silently misread trust record is
// worse than a reported one. Load uses Sanitize instead so one damaged record
// does not destroy the whole task.
func (c SharedContext) Validate(taskID string) error {
	if c.Empty() {
		return nil
	}
	if c.Version > ContextVersion {
		return fmt.Errorf("%w: version %d > %d", ErrInvalidContext, c.Version, ContextVersion)
	}
	if len(c.Facts) > MaxFacts {
		return fmt.Errorf("%w: %d facts > %d", ErrInvalidContext, len(c.Facts), MaxFacts)
	}
	if len(c.Hypotheses) > MaxHypotheses {
		return fmt.Errorf("%w: %d hypotheses > %d", ErrInvalidContext, len(c.Hypotheses), MaxHypotheses)
	}
	if len(c.Evidence) > MaxEvidenceRecords {
		return fmt.Errorf("%w: %d evidence records > %d", ErrInvalidContext, len(c.Evidence), MaxEvidenceRecords)
	}
	if len(c.Questions) > MaxQuestions {
		return fmt.Errorf("%w: %d questions > %d", ErrInvalidContext, len(c.Questions), MaxQuestions)
	}

	evidence := make(map[string]EvidenceRecord, len(c.Evidence))
	for i, record := range c.Evidence {
		if err := c.validateEvidence(record, taskID); err != nil {
			return fmt.Errorf("evidence %d: %w", i, err)
		}
		if _, exists := evidence[record.ID]; exists {
			return fmt.Errorf("%w: duplicate evidence id %q", ErrInvalidContext, record.ID)
		}
		evidence[record.ID] = record
	}

	facts := make(map[string]Fact, len(c.Facts))
	for i, fact := range c.Facts {
		if err := c.validateFact(fact, taskID, evidence); err != nil {
			return fmt.Errorf("fact %d: %w", i, err)
		}
		if _, exists := facts[fact.ID]; exists {
			return fmt.Errorf("%w: duplicate fact id %q", ErrInvalidContext, fact.ID)
		}
		facts[fact.ID] = fact
	}

	hypotheses := make(map[string]Hypothesis, len(c.Hypotheses))
	for i, hypothesis := range c.Hypotheses {
		if err := c.validateHypothesis(hypothesis, taskID, evidence); err != nil {
			return fmt.Errorf("hypothesis %d: %w", i, err)
		}
		if _, exists := hypotheses[hypothesis.ID]; exists {
			return fmt.Errorf("%w: duplicate hypothesis id %q", ErrInvalidContext, hypothesis.ID)
		}
		hypotheses[hypothesis.ID] = hypothesis
	}

	for i, record := range c.Evidence {
		for _, target := range record.Contradicts {
			if _, ok := facts[target]; !ok {
				if _, ok := hypotheses[target]; !ok {
					return fmt.Errorf("%w: evidence %d contradicts unknown %q", ErrInvalidContext, i, target)
				}
			}
		}
	}

	for i, fact := range c.Facts {
		if fact.FromHypothesis == "" {
			continue
		}
		if _, ok := hypotheses[fact.FromHypothesis]; !ok {
			return fmt.Errorf("%w: fact %d derives from unknown hypothesis %q", ErrInvalidContext, i, fact.FromHypothesis)
		}
	}

	// A supersession back-pointer must name a record in this context. A
	// dangling pointer would make the audit trail claim a replacement that
	// does not exist.
	for i, fact := range c.Facts {
		if fact.SupersededBy == "" {
			continue
		}
		if fact.SupersededBy == fact.ID {
			return fmt.Errorf("%w: fact %q supersedes itself", ErrInvalidContext, fact.ID)
		}
		if _, ok := facts[fact.SupersededBy]; !ok {
			return fmt.Errorf("%w: fact %d superseded by unknown fact %q", ErrInvalidContext, i, fact.SupersededBy)
		}
	}
	for i, hypothesis := range c.Hypotheses {
		if hypothesis.SupersededBy == "" {
			continue
		}
		if hypothesis.SupersededBy == hypothesis.ID {
			return fmt.Errorf("%w: hypothesis %q supersedes itself", ErrInvalidContext, hypothesis.ID)
		}
		if _, ok := hypotheses[hypothesis.SupersededBy]; !ok {
			return fmt.Errorf("%w: hypothesis %d superseded by unknown hypothesis %q", ErrInvalidContext, i, hypothesis.SupersededBy)
		}
	}

	roles := make(map[workspace.Workspace]bool, len(c.Roles))
	for i, role := range c.Roles {
		if err := validateRoleContext(role, facts, hypotheses); err != nil {
			return fmt.Errorf("role context %d: %w", i, err)
		}
		if roles[role.Role] {
			return fmt.Errorf("%w: duplicate role context %q", ErrInvalidContext, role.Role)
		}
		roles[role.Role] = true
	}

	questions := make(map[string]bool, len(c.Questions))
	for i, question := range c.Questions {
		if err := validateQuestion(question, taskID); err != nil {
			return fmt.Errorf("question %d: %w", i, err)
		}
		if questions[question.ID] {
			return fmt.Errorf("%w: duplicate question id %q", ErrInvalidContext, question.ID)
		}
		questions[question.ID] = true
	}
	return nil
}

func (c SharedContext) validateEvidence(record EvidenceRecord, taskID string) error {
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("%w: empty evidence id", ErrInvalidContext)
	}
	if record.TaskID != taskID {
		return fmt.Errorf("%w: evidence task %q, want %q", ErrInvalidContext, record.TaskID, taskID)
	}
	if !record.Kind.Valid() {
		return fmt.Errorf("%w: unknown evidence kind %q", ErrInvalidContext, record.Kind)
	}
	if !record.Role.Valid() {
		return fmt.Errorf("%w: unknown evidence role %q", ErrInvalidContext, record.Role)
	}
	if !record.Outcome.Valid() {
		return fmt.Errorf("%w: unknown evidence outcome %q", ErrInvalidContext, record.Outcome)
	}
	if !record.Validity.Valid() {
		return fmt.Errorf("%w: unknown evidence validity %q", ErrInvalidContext, record.Validity)
	}
	if strings.TrimSpace(record.Summary) == "" {
		return fmt.Errorf("%w: empty evidence summary", ErrInvalidContext)
	}
	if record.Outcome == EvidenceContradicting && len(record.Contradicts) == 0 {
		return fmt.Errorf("%w: contradicting evidence names no target", ErrInvalidContext)
	}
	if record.Outcome != EvidenceContradicting && len(record.Contradicts) > 0 {
		return fmt.Errorf("%w: only contradicting evidence names targets", ErrInvalidContext)
	}
	return nil
}

func (c SharedContext) validateFact(fact Fact, taskID string, evidence map[string]EvidenceRecord) error {
	if strings.TrimSpace(fact.ID) == "" {
		return fmt.Errorf("%w: empty fact id", ErrInvalidContext)
	}
	if fact.TaskID != taskID {
		return fmt.Errorf("%w: fact task %q, want %q", ErrInvalidContext, fact.TaskID, taskID)
	}
	if strings.TrimSpace(fact.Text) == "" {
		return fmt.Errorf("%w: empty fact text", ErrInvalidContext)
	}
	if !fact.Role.Valid() {
		return fmt.Errorf("%w: unknown fact role %q", ErrInvalidContext, fact.Role)
	}
	if !fact.Status.Valid() {
		return fmt.Errorf("%w: unknown fact status %q", ErrInvalidContext, fact.Status)
	}
	if len(fact.EvidenceIDs) == 0 {
		return fmt.Errorf("%w: fact has no supporting evidence", ErrInvalidContext)
	}
	current := 0
	for _, id := range fact.EvidenceIDs {
		record, ok := evidence[id]
		if !ok {
			return fmt.Errorf("%w: fact references unknown evidence %q", ErrInvalidContext, id)
		}
		if isSupporting(record) {
			current++
		}
	}
	if fact.Status == FactActive && current == 0 {
		return fmt.Errorf("%w: active fact has no current supporting evidence", ErrInvalidContext)
	}
	if fact.FromHypothesis != "" {
		// The fact is allowed to outlive the hypothesis, but the back-reference
		// must name a hypothesis record in this context. The full check runs
		// after hypotheses are indexed; this validates the shape.
		if strings.TrimSpace(fact.FromHypothesis) == fact.ID {
			return fmt.Errorf("%w: fact %q derived from itself", ErrInvalidContext, fact.ID)
		}
	}
	return nil
}

func (c SharedContext) validateHypothesis(hypothesis Hypothesis, taskID string, evidence map[string]EvidenceRecord) error {
	if strings.TrimSpace(hypothesis.ID) == "" {
		return fmt.Errorf("%w: empty hypothesis id", ErrInvalidContext)
	}
	if hypothesis.TaskID != taskID {
		return fmt.Errorf("%w: hypothesis task %q, want %q", ErrInvalidContext, hypothesis.TaskID, taskID)
	}
	if strings.TrimSpace(hypothesis.Text) == "" {
		return fmt.Errorf("%w: empty hypothesis text", ErrInvalidContext)
	}
	if !hypothesis.Role.Valid() {
		return fmt.Errorf("%w: unknown hypothesis role %q", ErrInvalidContext, hypothesis.Role)
	}
	if !hypothesis.Status.Valid() {
		return fmt.Errorf("%w: unknown hypothesis status %q", ErrInvalidContext, hypothesis.Status)
	}
	if hypothesis.ResolvedBy != "" && !hypothesis.ResolvedBy.Valid() {
		return fmt.Errorf("%w: unknown resolving role %q", ErrInvalidContext, hypothesis.ResolvedBy)
	}
	for _, id := range hypothesis.EvidenceIDs {
		if _, ok := evidence[id]; !ok {
			return fmt.Errorf("%w: hypothesis references unknown evidence %q", ErrInvalidContext, id)
		}
	}
	if hypothesis.Status == HypothesisConfirmed {
		current := 0
		for _, id := range hypothesis.EvidenceIDs {
			if isSupporting(evidence[id]) {
				current++
			}
		}
		if current == 0 {
			return fmt.Errorf("%w: confirmed hypothesis has no current supporting evidence", ErrInvalidContext)
		}
	}
	if hypothesis.SupersededBy != "" && hypothesis.SupersededBy == hypothesis.ID {
		return fmt.Errorf("%w: hypothesis %q supersedes itself", ErrInvalidContext, hypothesis.ID)
	}
	return nil
}

func validateRoleContext(role RoleContext, facts map[string]Fact, hypotheses map[string]Hypothesis) error {
	if !role.Role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidContext, role.Role)
	}
	if len(role.Reasoning) > MaxRoleNotes || len(role.FactIDs) > MaxRoleNotes ||
		len(role.HypothesisIDs) > MaxRoleNotes || len(role.Progress) > MaxRoleNotes ||
		len(role.Artifacts) > MaxRoleNotes || len(role.Plan) > MaxRoleNotes ||
		len(role.Scope) > MaxRoleNotes {
		return fmt.Errorf("%w: role %q exceeds the note bound", ErrInvalidContext, role.Role)
	}
	switch role.Role {
	case workspace.Ask:
		if len(role.HypothesisIDs) > 0 || role.Strategy != "" || len(role.Plan) > 0 || len(role.Scope) > 0 {
			return fmt.Errorf("%w: ASK context carries another role's fields", ErrInvalidContext)
		}
	case workspace.Fuzz:
		if len(role.FactIDs) > 0 || len(role.Reasoning) > 0 || len(role.Plan) > 0 || len(role.Scope) > 0 {
			return fmt.Errorf("%w: FUZZ context carries another role's fields", ErrInvalidContext)
		}
	case workspace.Build:
		if len(role.FactIDs) > 0 || len(role.Reasoning) > 0 || role.Strategy != "" || len(role.HypothesisIDs) > 0 {
			return fmt.Errorf("%w: BUILD context carries another role's fields", ErrInvalidContext)
		}
	}
	for _, id := range role.FactIDs {
		if _, ok := facts[id]; !ok {
			return fmt.Errorf("%w: role references unknown fact %q", ErrInvalidContext, id)
		}
	}
	for _, id := range role.HypothesisIDs {
		if _, ok := hypotheses[id]; !ok {
			return fmt.Errorf("%w: role references unknown hypothesis %q", ErrInvalidContext, id)
		}
	}
	return nil
}

func validateQuestion(question Question, taskID string) error {
	if strings.TrimSpace(question.ID) == "" {
		return fmt.Errorf("%w: empty question id", ErrInvalidContext)
	}
	if question.TaskID != taskID {
		return fmt.Errorf("%w: question task %q, want %q", ErrInvalidContext, question.TaskID, taskID)
	}
	if strings.TrimSpace(question.Text) == "" {
		return fmt.Errorf("%w: empty question text", ErrInvalidContext)
	}
	if !question.Role.Valid() {
		return fmt.Errorf("%w: unknown question role %q", ErrInvalidContext, question.Role)
	}
	if !question.Status.Valid() {
		return fmt.Errorf("%w: unknown question status %q", ErrInvalidContext, question.Status)
	}
	if question.AnsweredBy != "" && !question.AnsweredBy.Valid() {
		return fmt.Errorf("%w: unknown answering role %q", ErrInvalidContext, question.AnsweredBy)
	}
	if question.Status == QuestionAnswered && strings.TrimSpace(question.Answer) == "" {
		return fmt.Errorf("%w: answered question has no answer", ErrInvalidContext)
	}
	return nil
}

// Sanitize returns the largest internally consistent subset of a decoded
// context. It is used on load: one damaged optional record must not destroy
// the task, and the dropped or repaired count is reported so continuity is
// never silently invented.
func (c SharedContext) Sanitize(taskID string) (SharedContext, int) {
	if c.Empty() {
		return SharedContext{}, 0
	}
	if c.Version > ContextVersion {
		return SharedContext{}, len(c.Facts) + len(c.Hypotheses) + len(c.Evidence) + len(c.Roles) + len(c.Questions)
	}
	changes := 0
	if len(c.Facts) > MaxFacts {
		changes += len(c.Facts) - MaxFacts
		c.Facts = c.Facts[:MaxFacts]
	}
	if len(c.Hypotheses) > MaxHypotheses {
		changes += len(c.Hypotheses) - MaxHypotheses
		c.Hypotheses = c.Hypotheses[:MaxHypotheses]
	}
	if len(c.Evidence) > MaxEvidenceRecords {
		changes += len(c.Evidence) - MaxEvidenceRecords
		c.Evidence = c.Evidence[:MaxEvidenceRecords]
	}
	if len(c.Questions) > MaxQuestions {
		changes += len(c.Questions) - MaxQuestions
		c.Questions = c.Questions[:MaxQuestions]
	}
	out := SharedContext{Version: ContextVersion}

	evidence := make(map[string]EvidenceRecord, len(c.Evidence))
	for _, record := range c.Evidence {
		if err := c.validateEvidence(record, taskID); err != nil {
			changes++
			continue
		}
		if _, exists := evidence[record.ID]; exists {
			changes++
			continue
		}
		evidence[record.ID] = record
		out.Evidence = append(out.Evidence, record)
	}

	facts := make(map[string]Fact, len(c.Facts))
	for _, fact := range c.Facts {
		if kept := keepKnownEvidence(fact.EvidenceIDs, evidence); len(kept) != len(fact.EvidenceIDs) {
			fact.EvidenceIDs = kept
			changes++
		}
		if len(fact.EvidenceIDs) == 0 {
			changes++
			continue
		}
		if err := c.validateFact(fact, taskID, evidence); err != nil {
			if strings.TrimSpace(fact.ID) == "" || strings.TrimSpace(fact.Text) == "" ||
				!fact.Role.Valid() || !fact.Status.Valid() || fact.TaskID != taskID {
				changes++
				continue
			}
			// The statement is well formed but its support is not: retain it as
			// invalidated history rather than silently presenting it as verified.
			fact.Status = FactInvalidated
			changes++
		}
		if _, exists := facts[fact.ID]; exists {
			changes++
			continue
		}
		facts[fact.ID] = fact
		out.Facts = append(out.Facts, fact)
	}

	hypotheses := make(map[string]Hypothesis, len(c.Hypotheses))
	for _, hypothesis := range c.Hypotheses {
		if kept := keepKnownEvidence(hypothesis.EvidenceIDs, evidence); len(kept) != len(hypothesis.EvidenceIDs) {
			hypothesis.EvidenceIDs = kept
			changes++
		}
		if err := c.validateHypothesis(hypothesis, taskID, evidence); err != nil {
			if strings.TrimSpace(hypothesis.ID) == "" || strings.TrimSpace(hypothesis.Text) == "" ||
				!hypothesis.Role.Valid() || !hypothesis.Status.Valid() || hypothesis.TaskID != taskID ||
				(hypothesis.ResolvedBy != "" && !hypothesis.ResolvedBy.Valid()) {
				changes++
				continue
			}
			if hypothesis.Status == HypothesisConfirmed {
				hypothesis.Status = HypothesisInvalidated
				// The live invalidation path rewrites this string; repair must
				// reach the same state rather than leave "confirmed by
				// supporting evidence" on an invalidated record.
				hypothesis.Resolution = "supporting evidence is no longer current"
			}
			changes++
		}
		if _, exists := hypotheses[hypothesis.ID]; exists {
			changes++
			continue
		}
		hypotheses[hypothesis.ID] = hypothesis
		out.Hypotheses = append(out.Hypotheses, hypothesis)
	}

	// A supersession pointer whose target did not survive repair is cleared:
	// the record remains, but it no longer claims a replacement that is not in
	// the context. The same rule applies to facts and hypotheses.
	for i := range out.Facts {
		switch out.Facts[i].SupersededBy {
		case "":
			continue
		case out.Facts[i].ID:
			out.Facts[i].SupersededBy = ""
			changes++
		default:
			if _, ok := facts[out.Facts[i].SupersededBy]; !ok {
				out.Facts[i].SupersededBy = ""
				changes++
			}
		}
	}
	for i := range out.Hypotheses {
		switch out.Hypotheses[i].SupersededBy {
		case "":
			continue
		case out.Hypotheses[i].ID:
			out.Hypotheses[i].SupersededBy = ""
			changes++
		default:
			if _, ok := hypotheses[out.Hypotheses[i].SupersededBy]; !ok {
				out.Hypotheses[i].SupersededBy = ""
				changes++
			}
		}
	}

	// Contradiction targets must exist; a stale target is dropped, the
	// evidence record itself is retained.
	for i := range out.Evidence {
		kept := out.Evidence[i].Contradicts[:0]
		for _, target := range out.Evidence[i].Contradicts {
			if _, ok := facts[target]; ok {
				kept = append(kept, target)
				continue
			}
			if _, ok := hypotheses[target]; ok {
				kept = append(kept, target)
				continue
			}
			changes++
		}
		out.Evidence[i].Contradicts = kept
		if out.Evidence[i].Outcome == EvidenceContradicting && len(kept) == 0 {
			// The named claim did not survive repair, so the record can no
			// longer contradict anything. Retain the observation as
			// inconclusive rather than claiming a live contradiction.
			out.Evidence[i].Outcome = EvidenceInconclusive
			changes++
		}
	}

	for i := range out.Facts {
		if out.Facts[i].FromHypothesis == "" {
			continue
		}
		if _, ok := hypotheses[out.Facts[i].FromHypothesis]; !ok {
			out.Facts[i].FromHypothesis = ""
			changes++
		}
	}

	// Duplicates are detected across the whole collection, not just against
	// the last appended record: a corrupted file can interleave roles, and a
	// duplicate that survives repair would make every later write fail
	// validation.
	seenRoles := make(map[workspace.Workspace]bool, len(c.Roles))
	for _, role := range c.Roles {
		if !role.Role.Valid() {
			changes++
			continue
		}
		if seenRoles[role.Role] {
			changes++
			continue
		}
		role.FactIDs = keepKnown(role.FactIDs, facts)
		role.HypothesisIDs = keepKnownHypotheses(role.HypothesisIDs, hypotheses)
		if err := validateRoleContext(role, facts, hypotheses); err != nil {
			changes++
			continue
		}
		seenRoles[role.Role] = true
		out.Roles = append(out.Roles, role)
	}

	questions := make(map[string]bool, len(c.Questions))
	for _, question := range c.Questions {
		if err := validateQuestion(question, taskID); err != nil {
			changes++
			continue
		}
		if questions[question.ID] {
			changes++
			continue
		}
		questions[question.ID] = true
		out.Questions = append(out.Questions, question)
	}

	if out.Empty() {
		return SharedContext{}, changes
	}
	// The repair must produce a context the writer will accept. Returning a
	// still-invalid context would make every later checkpoint fail, so the
	// last resort is to drop the optional subdocument and report the loss.
	if err := out.Validate(taskID); err != nil {
		return SharedContext{}, changes + 1
	}
	return out, changes
}

func keepKnown(ids []string, known map[string]Fact) []string {
	return keepKnownGeneric(ids, func(id string) bool { _, ok := known[id]; return ok })
}

func keepKnownHypotheses(ids []string, known map[string]Hypothesis) []string {
	return keepKnownGeneric(ids, func(id string) bool { _, ok := known[id]; return ok })
}

func keepKnownEvidence(ids []string, known map[string]EvidenceRecord) []string {
	return keepKnownGeneric(ids, func(id string) bool { _, ok := known[id]; return ok })
}

func keepKnownGeneric(ids []string, exists func(string) bool) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if exists(id) {
			out = append(out, id)
		}
	}
	return out
}

func isSupporting(record EvidenceRecord) bool {
	return record.Validity == EvidenceCurrent &&
		(record.Outcome == EvidenceObserved || record.Outcome == EvidenceSupporting)
}

// Role returns one role's context, if recorded.
func (c SharedContext) Role(role workspace.Workspace) (RoleContext, bool) {
	for _, candidate := range c.Roles {
		if candidate.Role == role {
			return candidate, true
		}
	}
	return RoleContext{}, false
}

// LastRole returns the role that most recently touched the task, or "" when no
// role context has been recorded.
//
// It is continuity metadata, never authority: the host uses it to tell an
// ordinary new intent from a role change on the same objective, and it never
// grants the returned role anything. Ties are resolved by canonical role order
// so a repaired or hand-edited checkpoint still assembles deterministically.
func (c SharedContext) LastRole() workspace.Workspace {
	var (
		best   workspace.Workspace
		bestAt time.Time
	)
	for _, role := range c.Roles {
		if !role.Role.Valid() {
			continue
		}
		if best == "" || role.UpdatedAt.After(bestAt) ||
			(role.UpdatedAt.Equal(bestAt) && roleOrder(role.Role) < roleOrder(best)) {
			best, bestAt = role.Role, role.UpdatedAt
		}
	}
	return best
}

// roleOrder is the canonical ASK, FUZZ, BUILD order used only to break ties.
func roleOrder(role workspace.Workspace) int {
	switch role {
	case workspace.Ask:
		return 0
	case workspace.Fuzz:
		return 1
	case workspace.Build:
		return 2
	default:
		return 3
	}
}

// ensureRole returns a mutable pointer to one role's context, creating it when
// absent. The pointer is into the slice and is only valid until the next
// append.
func (c *SharedContext) ensureRole(role workspace.Workspace, at time.Time) *RoleContext {
	for i := range c.Roles {
		if c.Roles[i].Role == role {
			return &c.Roles[i]
		}
	}
	c.Roles = append(c.Roles, RoleContext{Role: role, UpdatedAt: at})
	return &c.Roles[len(c.Roles)-1]
}

func (c *SharedContext) ensureVersion() {
	if c.Version == 0 {
		c.Version = ContextVersion
	}
}

func (c SharedContext) evidenceByID(id string) (EvidenceRecord, bool) {
	for _, record := range c.Evidence {
		if record.ID == id {
			return record, true
		}
	}
	return EvidenceRecord{}, false
}

func (c SharedContext) factByID(id string) (Fact, bool) {
	for _, fact := range c.Facts {
		if fact.ID == id {
			return fact, true
		}
	}
	return Fact{}, false
}

func (c SharedContext) hypothesisByID(id string) (Hypothesis, bool) {
	for _, hypothesis := range c.Hypotheses {
		if hypothesis.ID == id {
			return hypothesis, true
		}
	}
	return Hypothesis{}, false
}

func (c SharedContext) hasQuestion(id string) bool {
	for _, question := range c.Questions {
		if question.ID == id {
			return true
		}
	}
	return false
}

func (t Task) hasClaim(id string) bool {
	if _, ok := t.Context.factByID(id); ok {
		return true
	}
	_, ok := t.Context.hypothesisByID(id)
	return ok
}

// currentSupport returns the evidence records that may support a claim.
func (t Task) currentSupport(ids []string) ([]EvidenceRecord, error) {
	out := make([]EvidenceRecord, 0, len(ids))
	for _, id := range ids {
		record, ok := t.Context.evidenceByID(id)
		if !ok {
			return nil, fmt.Errorf("%w: evidence %q", ErrUnknownRecord, id)
		}
		if !isSupporting(record) {
			return nil, fmt.Errorf("%w: evidence %q is %s/%s", ErrUnverifiedClaim, id, record.Validity, record.Outcome)
		}
		out = append(out, record)
	}
	return out, nil
}

// RecordEvidence appends a task-linked observation.
//
// Evidence is a reference plus a bounded summary; callers must pass the source
// or artifact rather than duplicating a large payload. A DedupKey returns the
// existing record instead of creating a duplicate.
func (t *Task) RecordEvidence(in EvidenceInput) (EvidenceRecord, error) {
	if strings.TrimSpace(t.ID) == "" {
		return EvidenceRecord{}, ErrNoActiveTask
	}
	if in.TaskID != "" && in.TaskID != t.ID {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence task %q, want %q", ErrInvalidContext, in.TaskID, t.ID)
	}
	if !in.Kind.Valid() {
		return EvidenceRecord{}, fmt.Errorf("%w: unknown evidence kind %q", ErrInvalidContext, in.Kind)
	}
	if !in.Role.Valid() {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence needs an originating role", ErrInvalidContext)
	}
	if !in.Outcome.Valid() {
		return EvidenceRecord{}, fmt.Errorf("%w: unknown evidence outcome %q", ErrInvalidContext, in.Outcome)
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence summary is required", ErrInvalidContext)
	}
	contradicts := uniqueStrings(in.Contradicts)
	if in.Outcome == EvidenceContradicting {
		if len(contradicts) == 0 {
			return EvidenceRecord{}, fmt.Errorf("%w: contradicting evidence needs a target", ErrInvalidContext)
		}
	} else if len(contradicts) > 0 {
		return EvidenceRecord{}, fmt.Errorf("%w: only contradicting evidence names targets", ErrInvalidContext)
	}
	for _, target := range contradicts {
		if !t.hasClaim(target) {
			return EvidenceRecord{}, fmt.Errorf("%w: contradiction target %q", ErrUnknownRecord, target)
		}
	}
	// A dedup key identifies one observation, not one arbitrary reuse of the
	// key. Returning an earlier supporting record for a call that meant to
	// record a contradiction would silently lose the contradiction.
	dedup := strings.TrimSpace(in.DedupKey)
	if dedup != "" {
		for _, existing := range t.Context.Evidence {
			if existing.DedupKey != dedup {
				continue
			}
			if existing.Outcome != in.Outcome || !equalStrings(existing.Contradicts, contradicts) {
				return EvidenceRecord{}, fmt.Errorf(
					"%w: dedup key %q already names a different observation", ErrInvalidContext, dedup)
			}
			return existing, nil
		}
	}
	if len(t.Context.Evidence) >= MaxEvidenceRecords {
		return EvidenceRecord{}, ErrContextLimit
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		minted, err := newID()
		if err != nil {
			return EvidenceRecord{}, err
		}
		id = minted
	} else if _, exists := t.Context.evidenceByID(id); exists {
		return EvidenceRecord{}, fmt.Errorf("%w: duplicate evidence id %q", ErrInvalidContext, id)
	}
	record := EvidenceRecord{
		ID:          id,
		TaskID:      t.ID,
		Kind:        in.Kind,
		Role:        in.Role,
		Source:      boundText(in.Source, maxEvidenceRefBytes),
		Scope:       boundText(strings.TrimSpace(in.Scope), maxEvidenceRefBytes),
		Artifact:    boundText(in.Artifact, maxEvidenceRefBytes),
		Summary:     boundText(summary, maxEvidenceSummaryBytes),
		Outcome:     in.Outcome,
		Validity:    EvidenceCurrent,
		Contradicts: contradicts,
		DedupKey:    boundText(dedup, maxEvidenceRefBytes),
		Digest:      boundText(strings.TrimSpace(in.Digest), maxEvidenceRefBytes),
		At:          stamp(in.At),
	}
	t.Context.ensureVersion()
	t.Context.Evidence = append(t.Context.Evidence, record)
	kind := TimelineEvidence
	if record.Kind == EvidenceTest || strings.Contains(record.Source, "check:") {
		kind = TimelineVerification
	}
	refs := []string{record.ID}
	if record.Artifact != "" {
		refs = append(refs, record.Artifact)
	}
	t.AppendEvent(TimelineEvent{
		At:      record.At,
		Kind:    kind,
		Role:    record.Role,
		Status:  string(record.Outcome),
		Summary: record.Summary,
		Refs:    refs,
	})
	return record, nil
}

// RecordFact appends a verified fact. It refuses a statement with no current,
// usable supporting evidence, so a model or role assertion cannot become a
// fact merely by being written down.
func (t *Task) RecordFact(in FactInput) (Fact, error) {
	if strings.TrimSpace(t.ID) == "" {
		return Fact{}, ErrNoActiveTask
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return Fact{}, fmt.Errorf("%w: empty fact text", ErrInvalidContext)
	}
	if !in.Role.Valid() {
		return Fact{}, fmt.Errorf("%w: fact needs an originating role", ErrInvalidContext)
	}
	evidenceIDs := uniqueStrings(in.EvidenceIDs)
	if len(evidenceIDs) == 0 {
		return Fact{}, fmt.Errorf("%w: a fact needs at least one supporting evidence record", ErrUnverifiedClaim)
	}
	if _, err := t.currentSupport(evidenceIDs); err != nil {
		return Fact{}, err
	}
	if in.FromHypothesis != "" {
		if _, ok := t.Context.hypothesisByID(in.FromHypothesis); !ok {
			return Fact{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownRecord, in.FromHypothesis)
		}
	}
	if len(t.Context.Facts) >= MaxFacts {
		return Fact{}, ErrContextLimit
	}
	id, err := newID()
	if err != nil {
		return Fact{}, err
	}
	fact := Fact{
		ID:             id,
		TaskID:         t.ID,
		Text:           boundText(text, maxFactBytes),
		Role:           in.Role,
		EvidenceIDs:    evidenceIDs,
		Status:         FactActive,
		FromHypothesis: in.FromHypothesis,
		At:             stamp(in.At),
	}
	t.Context.ensureVersion()
	t.Context.Facts = append(t.Context.Facts, fact)
	t.AppendEvent(TimelineEvent{
		At:      fact.At,
		Kind:    TimelineFact,
		Role:    fact.Role,
		Status:  string(fact.Status),
		Summary: fact.Text,
		Refs:    []string{fact.ID},
	})
	return fact, nil
}

// RecordHypothesis appends an unverified proposal. It never creates a fact.
func (t *Task) RecordHypothesis(in HypothesisInput) (Hypothesis, error) {
	if strings.TrimSpace(t.ID) == "" {
		return Hypothesis{}, ErrNoActiveTask
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return Hypothesis{}, fmt.Errorf("%w: empty hypothesis text", ErrInvalidContext)
	}
	if !in.Role.Valid() {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis needs an originating role", ErrInvalidContext)
	}
	if len(t.Context.Hypotheses) >= MaxHypotheses {
		return Hypothesis{}, ErrContextLimit
	}
	id, err := newID()
	if err != nil {
		return Hypothesis{}, err
	}
	hypothesis := Hypothesis{
		ID:     id,
		TaskID: t.ID,
		Text:   boundText(text, maxFactBytes),
		Role:   in.Role,
		Status: HypothesisActive,
		At:     stamp(in.At),
	}
	t.Context.ensureVersion()
	t.Context.Hypotheses = append(t.Context.Hypotheses, hypothesis)
	t.AppendEvent(TimelineEvent{
		At:      hypothesis.At,
		Kind:    TimelineHypothesis,
		Role:    hypothesis.Role,
		Status:  string(hypothesis.Status),
		Summary: hypothesis.Text,
		Refs:    []string{hypothesis.ID},
	})
	return hypothesis, nil
}

// AttachEvidence adds current supporting evidence to an existing fact or
// hypothesis without changing its disposition. Evidence never promotes a
// hypothesis by itself.
func (t *Task) AttachEvidence(claimID string, evidenceIDs []string, at time.Time) error {
	ids := uniqueStrings(evidenceIDs)
	if len(ids) == 0 {
		return fmt.Errorf("%w: no evidence to attach", ErrInvalidContext)
	}
	if _, err := t.currentSupport(ids); err != nil {
		return err
	}
	if fact, index := t.factIndex(claimID); index >= 0 {
		if fact.Status != FactActive {
			return fmt.Errorf("%w: fact %q is %s", ErrInvalidContext, claimID, fact.Status)
		}
		fact.EvidenceIDs = uniqueStrings(append(fact.EvidenceIDs, ids...))
		t.Context.Facts[index] = fact
		t.AppendEvent(TimelineEvent{
			At:      stamp(at),
			Kind:    TimelineEvidence,
			Role:    fact.Role,
			Status:  "attached",
			Summary: "attached evidence to fact " + fact.ID,
			Refs:    append([]string{fact.ID}, ids...),
		})
		return nil
	}
	if hypothesis, index := t.hypothesisIndex(claimID); index >= 0 {
		switch hypothesis.Status {
		case HypothesisActive, HypothesisConfirmed:
		default:
			return fmt.Errorf("%w: hypothesis %q is %s", ErrInvalidContext, claimID, hypothesis.Status)
		}
		hypothesis.EvidenceIDs = uniqueStrings(append(hypothesis.EvidenceIDs, ids...))
		t.Context.Hypotheses[index] = hypothesis
		t.AppendEvent(TimelineEvent{
			At:      stamp(at),
			Kind:    TimelineEvidence,
			Role:    hypothesis.Role,
			Status:  "attached",
			Summary: "attached evidence to hypothesis " + hypothesis.ID,
			Refs:    append([]string{hypothesis.ID}, ids...),
		})
		return nil
	}
	return fmt.Errorf("%w: claim %q", ErrUnknownRecord, claimID)
}

// ConfirmHypothesis resolves an active hypothesis with current evidence and
// records the resulting fact. Repeating the same assertion in another role
// changes nothing: only evidence can confirm.
func (t *Task) ConfirmHypothesis(id string, evidenceIDs []string, role workspace.Workspace, at time.Time) (Hypothesis, Fact, error) {
	index := -1
	for i := range t.Context.Hypotheses {
		if t.Context.Hypotheses[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Hypothesis{}, Fact{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownRecord, id)
	}
	hypothesis := t.Context.Hypotheses[index]
	if hypothesis.Status != HypothesisActive {
		return Hypothesis{}, Fact{}, fmt.Errorf("%w: hypothesis %q is %s", ErrInvalidContext, id, hypothesis.Status)
	}
	if !role.Valid() {
		return Hypothesis{}, Fact{}, fmt.Errorf("%w: confirmation needs a role", ErrInvalidContext)
	}
	ids := uniqueStrings(evidenceIDs)
	if len(ids) == 0 {
		return Hypothesis{}, Fact{}, fmt.Errorf("%w: confirmation needs evidence", ErrUnverifiedClaim)
	}
	if _, err := t.currentSupport(ids); err != nil {
		return Hypothesis{}, Fact{}, err
	}
	fact, err := t.RecordFact(FactInput{
		Text: hypothesis.Text,
		// The confirming role established the fact; the proposing role stays
		// on the hypothesis so both halves of the provenance survive.
		Role:           role,
		EvidenceIDs:    ids,
		FromHypothesis: hypothesis.ID,
		At:             at,
	})
	if err != nil {
		return Hypothesis{}, Fact{}, err
	}
	resolved := stamp(at)
	hypothesis.Status = HypothesisConfirmed
	hypothesis.EvidenceIDs = uniqueStrings(append(hypothesis.EvidenceIDs, ids...))
	hypothesis.ResolvedBy = role
	hypothesis.ResolvedAt = resolved
	hypothesis.Resolution = "confirmed by supporting evidence"
	t.Context.Hypotheses[index] = hypothesis
	t.AppendEvent(TimelineEvent{
		At:      resolved,
		Kind:    TimelineHypothesisConfirmed,
		Role:    role,
		Status:  string(hypothesis.Status),
		Summary: "hypothesis confirmed: " + hypothesis.Text,
		Refs:    []string{hypothesis.ID, fact.ID},
	})
	return hypothesis, fact, nil
}

// RejectHypothesis records a disposition without creating a fact.
func (t *Task) RejectHypothesis(id, reason string, role workspace.Workspace, at time.Time) (Hypothesis, error) {
	hypothesis, index := t.hypothesisIndex(id)
	if index < 0 {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownRecord, id)
	}
	if hypothesis.Status != HypothesisActive {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis %q is %s", ErrInvalidContext, id, hypothesis.Status)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Hypothesis{}, fmt.Errorf("%w: rejection needs a reason", ErrInvalidContext)
	}
	if !role.Valid() {
		return Hypothesis{}, fmt.Errorf("%w: rejection needs a role", ErrInvalidContext)
	}
	hypothesis.Status = HypothesisRejected
	hypothesis.Resolution = boundText(reason, maxFactBytes)
	hypothesis.ResolvedBy = role
	hypothesis.ResolvedAt = stamp(at)
	t.Context.Hypotheses[index] = hypothesis
	t.AppendEvent(TimelineEvent{
		At:      hypothesis.ResolvedAt,
		Kind:    TimelineHypothesisRejected,
		Role:    role,
		Status:  string(hypothesis.Status),
		Summary: "hypothesis rejected: " + hypothesis.Text,
		Refs:    []string{hypothesis.ID},
	})
	return hypothesis, nil
}

// SupersedeHypothesis replaces one hypothesis with another, retaining both.
func (t *Task) SupersedeHypothesis(id, byID string, role workspace.Workspace, at time.Time) (Hypothesis, error) {
	target, index := t.hypothesisIndex(id)
	if index < 0 {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownRecord, id)
	}
	if target.Status != HypothesisActive && target.Status != HypothesisConfirmed {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis %q is %s", ErrInvalidContext, id, target.Status)
	}
	replacement, ok := t.Context.hypothesisByID(byID)
	if !ok {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownRecord, byID)
	}
	if replacement.ID == target.ID {
		return Hypothesis{}, fmt.Errorf("%w: a hypothesis cannot supersede itself", ErrInvalidContext)
	}
	if !role.Valid() {
		return Hypothesis{}, fmt.Errorf("%w: supersession needs a role", ErrInvalidContext)
	}
	target.Status = HypothesisSuperseded
	target.SupersededBy = replacement.ID
	target.Resolution = boundText("superseded by hypothesis "+replacement.ID, maxFactBytes)
	target.ResolvedBy = role
	target.ResolvedAt = stamp(at)
	t.Context.Hypotheses[index] = target
	t.AppendEvent(TimelineEvent{
		At:      target.ResolvedAt,
		Kind:    TimelineHypothesisSuperseded,
		Role:    role,
		Status:  string(target.Status),
		Summary: "hypothesis superseded: " + target.Text,
		Refs:    []string{target.ID, replacement.ID},
	})
	return target, nil
}

// SupersedeFact replaces one active fact with another, retaining both.
func (t *Task) SupersedeFact(id, byID string, at time.Time) (Fact, error) {
	target, index := t.factIndex(id)
	if index < 0 {
		return Fact{}, fmt.Errorf("%w: fact %q", ErrUnknownRecord, id)
	}
	if target.Status != FactActive {
		return Fact{}, fmt.Errorf("%w: fact %q is %s", ErrInvalidContext, id, target.Status)
	}
	replacement, ok := t.Context.factByID(byID)
	if !ok {
		return Fact{}, fmt.Errorf("%w: fact %q", ErrUnknownRecord, byID)
	}
	if replacement.Status != FactActive {
		return Fact{}, fmt.Errorf("%w: replacement fact %q is %s", ErrInvalidContext, byID, replacement.Status)
	}
	if replacement.ID == target.ID {
		return Fact{}, fmt.Errorf("%w: a fact cannot supersede itself", ErrInvalidContext)
	}
	target.Status = FactSuperseded
	target.SupersededBy = replacement.ID
	t.Context.Facts[index] = target
	t.AppendEvent(TimelineEvent{
		At:      stamp(at),
		Kind:    TimelineFactSuperseded,
		Role:    target.Role,
		Status:  string(target.Status),
		Summary: "fact superseded: " + target.Text,
		Refs:    []string{target.ID, replacement.ID},
	})
	return target, nil
}

// InvalidateEvidence marks an observation as no longer applicable. Facts and
// confirmed hypotheses that lose their last current support are invalidated,
// never deleted, so the trail remains inspectable.
func (t *Task) InvalidateEvidence(id, reason string, at time.Time) (EvidenceRecord, error) {
	index := -1
	for i := range t.Context.Evidence {
		if t.Context.Evidence[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence %q", ErrUnknownRecord, id)
	}
	record := t.Context.Evidence[index]
	if record.Validity == EvidenceInvalid {
		return record, nil
	}
	record.Validity = EvidenceInvalid
	record.Note = boundText(strings.TrimSpace(reason), maxEvidenceSummaryBytes)
	t.Context.Evidence[index] = record
	t.invalidateUnsupported()
	t.AppendEvent(TimelineEvent{
		At:      stamp(at),
		Kind:    TimelineEvidenceInvalidated,
		Role:    record.Role,
		Status:  string(record.Validity),
		Summary: "evidence invalidated: " + record.Summary,
		Refs:    []string{record.ID},
	})
	return record, nil
}

// SupersedeEvidence marks one observation as replaced by a later one.
func (t *Task) SupersedeEvidence(id, byID string, at time.Time) (EvidenceRecord, error) {
	index := -1
	for i := range t.Context.Evidence {
		if t.Context.Evidence[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence %q", ErrUnknownRecord, id)
	}
	replacement, ok := t.Context.evidenceByID(byID)
	if !ok {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence %q", ErrUnknownRecord, byID)
	}
	if replacement.ID == id {
		return EvidenceRecord{}, fmt.Errorf("%w: evidence cannot supersede itself", ErrInvalidContext)
	}
	if replacement.Validity != EvidenceCurrent {
		return EvidenceRecord{}, fmt.Errorf("%w: replacement evidence is %s", ErrInvalidContext, replacement.Validity)
	}
	record := t.Context.Evidence[index]
	if record.Validity != EvidenceCurrent {
		return record, nil
	}
	record.Validity = EvidenceSuperseded
	record.Note = "superseded by #" + replacement.ID
	t.Context.Evidence[index] = record
	t.invalidateUnsupported()
	t.AppendEvent(TimelineEvent{
		At:      stamp(at),
		Kind:    TimelineEvidenceSuperseded,
		Role:    record.Role,
		Status:  string(record.Validity),
		Summary: "evidence superseded: " + record.Summary,
		Refs:    []string{record.ID, replacement.ID},
	})
	return record, nil
}

// invalidateUnsupported keeps conclusions whose last current support is gone
// in the record as invalidated history.
func (t *Task) invalidateUnsupported() {
	for i := range t.Context.Facts {
		fact := &t.Context.Facts[i]
		if fact.Status != FactActive {
			continue
		}
		if !t.hasCurrentSupport(fact.EvidenceIDs) {
			fact.Status = FactInvalidated
		}
	}
	for i := range t.Context.Hypotheses {
		hypothesis := &t.Context.Hypotheses[i]
		if hypothesis.Status != HypothesisConfirmed {
			continue
		}
		if !t.hasCurrentSupport(hypothesis.EvidenceIDs) {
			hypothesis.Status = HypothesisInvalidated
			hypothesis.Resolution = "supporting evidence is no longer current"
		}
	}
}

func (t Task) hasCurrentSupport(ids []string) bool {
	for _, id := range ids {
		if record, ok := t.Context.evidenceByID(id); ok && isSupporting(record) {
			return true
		}
	}
	return false
}

// AddQuestion records an outstanding question.
func (t *Task) AddQuestion(text string, role workspace.Workspace, at time.Time) (Question, error) {
	if strings.TrimSpace(t.ID) == "" {
		return Question{}, ErrNoActiveTask
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Question{}, fmt.Errorf("%w: empty question", ErrInvalidContext)
	}
	if !role.Valid() {
		return Question{}, fmt.Errorf("%w: question needs a role", ErrInvalidContext)
	}
	if len(t.Context.Questions) >= MaxQuestions {
		return Question{}, ErrContextLimit
	}
	id, err := newID()
	if err != nil {
		return Question{}, err
	}
	question := Question{ID: id, TaskID: t.ID, Text: boundText(text, maxFactBytes), Role: role, Status: QuestionOpen, At: stamp(at)}
	t.Context.ensureVersion()
	t.Context.Questions = append(t.Context.Questions, question)
	t.AppendEvent(TimelineEvent{
		At:      question.At,
		Kind:    TimelineQuestion,
		Role:    question.Role,
		Status:  string(question.Status),
		Summary: "question raised: " + question.Text,
		Refs:    []string{question.ID},
	})
	return question, nil
}

// AnswerQuestion records an answer without deleting the question.
func (t *Task) AnswerQuestion(id, answer string, role workspace.Workspace, at time.Time) (Question, error) {
	index := -1
	for i := range t.Context.Questions {
		if t.Context.Questions[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Question{}, fmt.Errorf("%w: question %q", ErrUnknownRecord, id)
	}
	question := t.Context.Questions[index]
	if question.Status == QuestionAnswered {
		return question, nil
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return Question{}, fmt.Errorf("%w: empty answer", ErrInvalidContext)
	}
	if !role.Valid() {
		return Question{}, fmt.Errorf("%w: answer needs a role", ErrInvalidContext)
	}
	question.Status = QuestionAnswered
	question.Answer = boundText(answer, maxFactBytes)
	question.AnsweredBy = role
	question.AnsweredAt = stamp(at)
	t.Context.Questions[index] = question
	t.AppendEvent(TimelineEvent{
		At:      question.AnsweredAt,
		Kind:    TimelineQuestion,
		Role:    role,
		Status:  string(question.Status),
		Summary: "question answered: " + question.Text,
		Refs:    []string{question.ID},
	})
	return question, nil
}

// UpdateRoleContext appends to one role's context without touching another
// role's record. It refuses references to facts or hypotheses that do not
// exist, so a role context cannot invent shared knowledge.
func (t *Task) UpdateRoleContext(role workspace.Workspace, update RoleContextUpdate, at time.Time) (RoleContext, error) {
	if strings.TrimSpace(t.ID) == "" {
		return RoleContext{}, ErrNoActiveTask
	}
	if !role.Valid() {
		return RoleContext{}, fmt.Errorf("%w: unknown role %q", ErrInvalidContext, role)
	}
	if update.Role != "" && update.Role != role {
		return RoleContext{}, fmt.Errorf("%w: update role %q, want %q", ErrInvalidContext, update.Role, role)
	}
	for _, id := range update.FactIDs {
		if _, ok := t.Context.factByID(id); !ok {
			return RoleContext{}, fmt.Errorf("%w: fact %q", ErrUnknownRecord, id)
		}
	}
	for _, id := range update.HypothesisIDs {
		if _, ok := t.Context.hypothesisByID(id); !ok {
			return RoleContext{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownRecord, id)
		}
	}
	t.Context.ensureVersion()
	roleCtx := t.Context.ensureRole(role, stamp(at))
	roleCtx.UpdatedAt = stamp(at)
	if strategy := strings.TrimSpace(update.Strategy); strategy != "" {
		roleCtx.Strategy = boundText(strategy, maxFactBytes)
	}
	roleCtx.Reasoning = appendNotes(roleCtx.Reasoning, update.Reasoning)
	roleCtx.FactIDs = appendNotes(roleCtx.FactIDs, update.FactIDs)
	roleCtx.HypothesisIDs = appendNotes(roleCtx.HypothesisIDs, update.HypothesisIDs)
	roleCtx.Progress = appendNotes(roleCtx.Progress, update.Progress)
	roleCtx.Artifacts = appendNotes(roleCtx.Artifacts, update.Artifacts)
	roleCtx.Plan = appendNotes(roleCtx.Plan, update.Plan)
	roleCtx.Scope = appendNotes(roleCtx.Scope, update.Scope)
	return *roleCtx, nil
}

// TouchRole records that a role is active on the task. It creates the role's
// context record when absent and never clears another role's context.
func (t *Task) TouchRole(role workspace.Workspace, at time.Time) error {
	if !role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidContext, role)
	}
	t.Context.ensureVersion()
	roleCtx := t.Context.ensureRole(role, stamp(at))
	roleCtx.UpdatedAt = stamp(at)
	return nil
}

// ContradictingEvidence returns current evidence that contradicts a claim.
func (t Task) ContradictingEvidence(claimID string) []EvidenceRecord {
	var out []EvidenceRecord
	for _, record := range t.Context.Evidence {
		if record.Validity != EvidenceCurrent || record.Outcome != EvidenceContradicting {
			continue
		}
		for _, target := range record.Contradicts {
			if target == claimID {
				out = append(out, record)
				break
			}
		}
	}
	return out
}

func (t Task) hypothesisIndex(id string) (Hypothesis, int) {
	for i := range t.Context.Hypotheses {
		if t.Context.Hypotheses[i].ID == id {
			return t.Context.Hypotheses[i], i
		}
	}
	return Hypothesis{}, -1
}

func (t Task) factIndex(id string) (Fact, int) {
	for i := range t.Context.Facts {
		if t.Context.Facts[i].ID == id {
			return t.Context.Facts[i], i
		}
	}
	return Fact{}, -1
}

func stamp(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

func boundText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

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

// equalStrings compares two already-deduplicated lists in order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// appendNotes appends unique, bounded notes in order. A full list keeps the
// most recent notes: progress is orientation, not an audit log.
func appendNotes(existing, add []string) []string {
	values := uniqueStrings(append(append([]string(nil), existing...), add...))
	if len(values) > MaxRoleNotes {
		values = values[len(values)-MaxRoleNotes:]
	}
	for i := range values {
		values[i] = boundText(values[i], maxFactBytes)
	}
	return values
}
