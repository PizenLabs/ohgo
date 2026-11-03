package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// ErrUnprovenCompletion means a completion request did not carry the evidence
// the objective requires. It is distinct from an invalid lifecycle transition:
// the task may still be completed later, after the missing evidence exists.
var ErrUnprovenCompletion = errors.New("task: completion is not supported by evidence")

// ObjectiveState is the truthful state of the objective, derived from the task
// lifecycle and the completion proof.
//
// It is a projection, never a second lifecycle. The task status remains the
// durable owner; this vocabulary answers the user's question ("is it actually
// done?") without letting a model claim success.
type ObjectiveState string

// Objective states.
const (
	// ObjectiveActive means work is in progress.
	ObjectiveActive ObjectiveState = "ACTIVE"
	// ObjectiveInterrupted means execution stopped at a recoverable checkpoint.
	ObjectiveInterrupted ObjectiveState = "INTERRUPTED"
	// ObjectiveNeedsReview means a turn ended without enough evidence to
	// complete. It is not a failure and not a success.
	ObjectiveNeedsReview ObjectiveState = "NEEDS_REVIEW"
	// ObjectiveBlocked means an unrecoverable failure or a task that cannot
	// complete without an explicit human decision.
	ObjectiveBlocked ObjectiveState = "BLOCKED"
	// ObjectiveProven means the objective's required conditions were checked
	// against current evidence and the observed result supports completion.
	ObjectiveProven ObjectiveState = "PROVEN"
	// ObjectiveCompletedUnproven means the lifecycle reached COMPLETED but no
	// evidence proof is attached, or its proof no longer resolves. Historical
	// completions and legacy checkpoints report this honestly instead of
	// claiming PROVEN.
	ObjectiveCompletedUnproven ObjectiveState = "COMPLETED_UNPROVEN"
	// ObjectiveCancelled means the user deliberately abandoned the task.
	ObjectiveCancelled ObjectiveState = "CANCELLED"
)

// Valid reports whether the state is defined.
func (s ObjectiveState) Valid() bool {
	switch s {
	case ObjectiveActive, ObjectiveInterrupted, ObjectiveNeedsReview,
		ObjectiveBlocked, ObjectiveProven, ObjectiveCompletedUnproven,
		ObjectiveCancelled:
		return true
	default:
		return false
	}
}

// CompletionProof is the durable evidence that justified a completion.
//
// It records the evaluator's verdict and the observations the runtime actually
// saw. It never stores the assistant's answer or a tool payload: those remain
// in the session store. A proof whose referenced evidence is later invalidated
// stops supporting PROVEN, but the historical completion is not rewritten.
type CompletionProof struct {
	// Verdict is the evaluator's completion verdict. Only COMPLETED is
	// accepted when the proof is recorded.
	Verdict string `json:"verdict"`
	// Reason explains a non-completion, when the host supplied one. Empty for
	// a successful proof.
	Reason string `json:"reason,omitempty"`
	// Answered records that an informational objective produced a non-empty
	// final answer. The answer itself stays in the transcript. It is never
	// evidence that an execution or a mutation objective was satisfied.
	Answered bool `json:"answered,omitempty"`
	// Executed records that an execution objective observed a process or check
	// reach a successful terminal state.
	Executed bool `json:"executed,omitempty"`
	// Observed records that a mutation objective observed a definite change
	// against the pre-run target state.
	Observed bool `json:"observed,omitempty"`
	// Role is the execution role that completed the work.
	Role workspace.Workspace `json:"role,omitempty"`
	// EvidenceIDs are the current observations that supported the verdict.
	EvidenceIDs []string `json:"evidenceIds,omitempty"`
	// Verified are bounded descriptions of the checks that followed the last
	// change. They explain what was actually observed.
	Verified []string `json:"verified,omitempty"`
	// At is when the proof was recorded.
	At time.Time `json:"at"`
}

// Clone returns a deep copy.
func (p CompletionProof) Clone() CompletionProof {
	out := p
	out.EvidenceIDs = append([]string(nil), p.EvidenceIDs...)
	out.Verified = append([]string(nil), p.Verified...)
	return out
}

// VerifiesEvidence reports whether the proof names one evidence record.
func (p CompletionProof) VerifiesEvidence(id string) bool {
	for _, existing := range p.EvidenceIDs {
		if existing == id {
			return true
		}
	}
	return false
}

// validateCompletionProofShape checks the fields a persisted proof must carry.
// It deliberately does not require the referenced evidence to still be current:
// a historical completion stays readable after an invalidation.
func validateCompletionProofShape(proof CompletionProof) error {
	if strings.TrimSpace(proof.Verdict) != "COMPLETED" {
		return fmt.Errorf("completion verdict %q", proof.Verdict)
	}
	if proof.At.IsZero() {
		return fmt.Errorf("completion proof has no timestamp")
	}
	if proof.Role != "" && !proof.Role.Valid() {
		return fmt.Errorf("completion proof has unknown role %q", proof.Role)
	}
	return nil
}

// CompletionEvidence is the host's completion decision at a turn boundary.
//
// The host owns the evaluator (agent.EvaluateTaskCompletion / ClassifyRun), so
// it supplies the verdict. The task layer independently refuses to record
// PROVEN unless the durable task carries the observations the objective needs.
type CompletionEvidence struct {
	// Complete is the evaluator's verdict.
	Complete bool
	// Reason explains a review verdict.
	Reason string
	// Answered reports a non-empty final answer for an informational
	// objective.
	Answered bool
	// Role is the role that completed the work. Empty uses the live role.
	Role workspace.Workspace
}

// ObjectiveState returns the objective's truthful state.
//
// A COMPLETED task is PROVEN only when its proof is present, its verdict is
// COMPLETED, and every referenced observation still resolves as current
// support. An invalidated check therefore downgrades the projection without
// rewriting the historical lifecycle record.
func (t Task) ObjectiveState() ObjectiveState {
	switch t.Status {
	case StatusPending, StatusRunning:
		if t.Contract == nil {
			return ObjectiveBlocked
		}
		return ObjectiveActive
	case StatusPaused:
		return ObjectiveInterrupted
	case StatusNeedsReview:
		if t.Contract == nil {
			return ObjectiveBlocked
		}
		return ObjectiveNeedsReview
	case StatusFailed:
		return ObjectiveBlocked
	case StatusCancelled:
		return ObjectiveCancelled
	case StatusCompleted:
		if t.proofCurrent() {
			return ObjectiveProven
		}
		return ObjectiveCompletedUnproven
	default:
		return ""
	}
}

// Proven reports whether the objective is currently backed by a valid proof.
func (t Task) Proven() bool { return t.ObjectiveState() == ObjectiveProven }

// Completion returns the recorded proof, if any.
func (t Task) CompletionProof() (CompletionProof, bool) {
	if t.Completion == nil {
		return CompletionProof{}, false
	}
	return t.Completion.Clone(), true
}

// proofCurrent reports whether the recorded proof still supports completion.
//
// The check is deliberately conservative. A proof is current only while every
// observation it names still resolves as current support, no current
// contradiction targets a conclusion the proof depends on, and the proof's
// own evidence kind still meets the contract. An unrelated contradiction does
// not invalidate an otherwise sound proof.
func (t Task) proofCurrent() bool {
	proof := t.Completion
	if proof == nil || t.Status != StatusCompleted {
		return false
	}
	if strings.TrimSpace(proof.Verdict) != "COMPLETED" || proof.At.IsZero() {
		return false
	}
	if proof.Role != "" && !proof.Role.Valid() {
		return false
	}
	for _, id := range proof.EvidenceIDs {
		record, ok := t.Context.evidenceByID(id)
		if !ok || !isSupporting(record) {
			return false
		}
	}
	if t.proofMateriallyContradicted(proof) {
		return false
	}
	switch {
	case t.Contract == nil:
		// A legacy completion with a recorded proof is still checked against
		// the evidence it names; no contract means no stronger claim.
		return proof.Answered || len(proof.EvidenceIDs) > 0
	case t.Contract.Kind == contract.ObjectiveInformational:
		return proof.Answered
	case t.Contract.Kind == contract.ObjectiveExecution:
		return len(proof.EvidenceIDs) > 0 && (proof.Executed || len(proof.Verified) > 0)
	case t.Contract.Kind == contract.ObjectiveMutation:
		return proof.Observed && len(proof.Verified) > 0 && len(proof.EvidenceIDs) > 0
	default:
		// An unknown contract kind cannot authorize any completion claim.
		return false
	}
}

// proofMateriallyContradicted reports whether current evidence disputes a
// conclusion the proof depends on.
//
// A contradiction is material when it targets a claim backed by the proof's
// own evidence, or when it names a proof evidence record directly. An
// unrelated contradiction elsewhere in the task does not invalidate the proof.
func (t Task) proofMateriallyContradicted(proof *CompletionProof) bool {
	if proof == nil || len(proof.EvidenceIDs) == 0 {
		return false
	}
	for _, record := range t.Context.Evidence {
		if record.Validity != EvidenceCurrent || record.Outcome != EvidenceContradicting {
			continue
		}
		if intersects(record.Contradicts, proof.EvidenceIDs) {
			return true
		}
	}
	for _, fact := range t.Context.Facts {
		if fact.Status != FactActive || !intersects(fact.EvidenceIDs, proof.EvidenceIDs) {
			continue
		}
		if len(t.ContradictingEvidence(fact.ID)) > 0 {
			return true
		}
	}
	for _, hypothesis := range t.Context.Hypotheses {
		if hypothesis.Status != HypothesisConfirmed || !intersects(hypothesis.EvidenceIDs, proof.EvidenceIDs) {
			continue
		}
		if len(t.ContradictingEvidence(hypothesis.ID)) > 0 {
			return true
		}
	}
	return false
}

// CompletionSummary renders a bounded explanation of the objective state.
// It says what the runtime observed, never more.
func (t Task) CompletionSummary() string {
	state := t.ObjectiveState()
	switch state {
	case ObjectiveProven:
		proof := t.Completion
		parts := []string{"objective proven"}
		switch {
		case proof.Answered:
			parts = append(parts, "a non-empty answer was produced; answer correctness is not verified")
		case proof.Observed:
			parts = append(parts, "an observed change was verified afterwards")
		case proof.Executed:
			parts = append(parts, "a successful execution was observed")
		}
		if len(proof.Verified) > 0 {
			parts = append(parts, "observed: "+strings.Join(proof.Verified, "; "))
		}
		if len(proof.EvidenceIDs) > 0 {
			parts = append(parts, "evidence:"+strings.Join(proof.EvidenceIDs, ","))
		}
		return truncate(strings.Join(parts, " — "), 520)
	case ObjectiveCompletedUnproven:
		if t.Completion == nil {
			return "completed without a recorded proof; completion is not verified"
		}
		return "completed, but the recorded proof no longer resolves to current evidence"
	case ObjectiveBlocked:
		if t.Contract == nil {
			return "blocked: the task has no completion contract; it can be cancelled or replaced, not completed"
		}
		return "blocked: the task failed and cannot be resumed in place"
	default:
		return strings.ToLower(string(state))
	}
}

// CompleteWithEvidence records a completion only when the durable state
// supports it.
//
// The host's evaluator remains the completion authority; this is the durable
// evidence boundary. Each contract kind has an explicit requirement:
//
//   - informational: a non-empty answer. It proves answer production, never
//     answer correctness.
//   - execution: a current attributable execution observation, or the
//     contract's required verification. A final answer alone is refused.
//   - mutation: an observed definite change and a current verification that
//     followed it. A generic successful check with no observed change is
//     refused.
//
// An unsupported contract kind is refused rather than admitted by a permissive
// fallback. A refusal is not a failure: the same task can be completed after
// the missing evidence exists.
//
// The transition is atomic with its checkpoint. On a persistence failure the
// authoritative in-memory task is restored to its prior state, so no
// uncommitted completion proof or completion event can be observed.
//
// The task must be RUNNING. A PAUSED or NEEDS_REVIEW task must first resume
// through Begin; completing it in place would skip the boundary that reconciles
// the live workspace.
func (c *Controller) CompleteWithEvidence(in CompletionEvidence) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	// The turn boundary is the evidence boundary: project the just-finished
	// observations into the durable task context before deciding whether the
	// proof exists. Persist would do this later, but the completion check must
	// see the same evidence the projection will write.
	c.projectTaskContext()
	if !in.Complete {
		reason := strings.TrimSpace(in.Reason)
		if reason == "" {
			reason = "the runtime did not observe the objective's required result"
		}
		return c.task.Clone(), fmt.Errorf("%w: %s", ErrUnprovenCompletion, reason)
	}
	if c.task.Contract == nil {
		return c.task.Clone(), fmt.Errorf("%w: the task has no completion contract", ErrUnprovenCompletion)
	}
	if c.task.HasOpenTodos() {
		now := c.now().UTC()
		c.task.UpdatedAt = now
		c.lastAction = "turn finished with work remaining"
		c.noteRoleProgress(c.lastAction)
		_ = c.persist()
		return c.task.Clone(), fmt.Errorf("%w: work remains in the todo list", ErrUnprovenCompletion)
	}

	role := in.Role
	if !role.Valid() {
		role = c.currentRole()
	}
	now := c.now().UTC()
	proof := CompletionProof{
		Verdict: "COMPLETED",
		Role:    role,
		At:      now,
	}
	switch c.task.Contract.Kind {
	case contract.ObjectiveMutation:
		change, ok := c.latestChangeRecord()
		if !ok {
			return c.task.Clone(), fmt.Errorf("%w: no observed change supports the mutation objective", ErrUnprovenCompletion)
		}
		verified := c.verificationsAfterChange(change)
		if len(verified) == 0 {
			return c.task.Clone(), fmt.Errorf("%w: the last observed change has no verification after it", ErrUnprovenCompletion)
		}
		if !c.verificationsCoverContract(verified) {
			return c.task.Clone(), fmt.Errorf("%w: no current verification covers the contract's required scope", ErrUnprovenCompletion)
		}
		proof.Observed = true
		proof.Verified = verificationSummaries(verified)
		proof.EvidenceIDs = appendUniqueEvidenceIDs(proof.EvidenceIDs, change)
		for _, record := range verified {
			proof.EvidenceIDs = appendUniqueEvidenceIDs(proof.EvidenceIDs, record)
		}
	case contract.ObjectiveExecution:
		executed := c.executionEvidence()
		verified := c.verificationEvidence()
		if len(executed) == 0 && len(verified) == 0 {
			return c.task.Clone(), fmt.Errorf("%w: an answer alone cannot prove an execution objective", ErrUnprovenCompletion)
		}
		if len(c.task.Contract.Verification) > 0 && (len(verified) == 0 || !c.verificationsCoverContract(verified)) {
			return c.task.Clone(), fmt.Errorf("%w: no current verification covers the contract's required scope", ErrUnprovenCompletion)
		}
		proof.Executed = len(executed) > 0
		proof.Verified = verificationSummaries(verified)
		for _, record := range executed {
			proof.EvidenceIDs = appendUniqueEvidenceIDs(proof.EvidenceIDs, record)
		}
		for _, record := range verified {
			proof.EvidenceIDs = appendUniqueEvidenceIDs(proof.EvidenceIDs, record)
		}
	case contract.ObjectiveInformational:
		if !in.Answered {
			return c.task.Clone(), fmt.Errorf("%w: the objective asked for an answer but none was recorded", ErrUnprovenCompletion)
		}
		proof.Answered = true
	default:
		return c.task.Clone(), fmt.Errorf("%w: unsupported contract kind %q", ErrUnprovenCompletion, c.task.Contract.Kind)
	}

	// Stage the transition, then commit it with its checkpoint. The clone is
	// the rollback point: a failed write must not expose a completed task.
	before := c.task.Clone()
	beforeAction := c.lastAction
	beforeEvidence := c.evidence.Clone()
	if err := c.task.Complete(now); err != nil {
		return c.task.Clone(), err
	}
	c.task.Completion = &proof
	c.task.UpdatedAt = now
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineCompleted,
		Role:    role,
		Status:  string(ObjectiveProven),
		Summary: completionEventSummary(c.task.Contract.Kind, proof),
		Refs:    proof.EvidenceIDs,
	})
	c.lastAction = "completed and proven"
	c.noteRoleProgress(c.lastAction)
	if err := c.persist(); err != nil {
		c.task = before
		c.lastAction = beforeAction
		c.evidence = beforeEvidence
		return c.task.Clone(), err
	}
	return c.task.Clone(), nil
}

// completionEventSummary states what the proof actually established. It never
// claims more evidence than the proof carries.
func completionEventSummary(kind contract.ObjectiveKind, proof CompletionProof) string {
	switch kind {
	case contract.ObjectiveInformational:
		return "objective completed: a non-empty answer was produced (answer correctness is not verified)"
	case contract.ObjectiveExecution:
		return "objective completed: a successful execution was observed"
	case contract.ObjectiveMutation:
		return "objective completed: an observed change was verified afterwards"
	default:
		return "objective completed"
	}
}

// HasCurrentChange reports whether the durable task carries a current observed
// change. It is a read-only projection for the turn evaluator: a resumed turn
// may verify a change a prior run already recorded without repeating it.
//
// It never establishes completion by itself. The task layer still requires the
// verification to follow that change and to cover the contract, so this only
// lets the runtime ask the durable completion boundary instead of refusing at
// the per-run gate.
func (c *Controller) HasCurrentChange() bool {
	if err := c.ensureLoaded(); err != nil {
		return false
	}
	_, ok := c.latestChangeRecord()
	return ok
}

// latestChangeRecord returns the newest current, supporting change observation.
func (c *Controller) latestChangeRecord() (EvidenceRecord, bool) {
	if c.evidence != nil {
		bestID, bestSeq := "", -1
		for _, item := range c.evidence.Items {
			if !isMutationObservation(item) || item.Status != cognition.StatusKnown {
				continue
			}
			if item.Seq > bestSeq {
				bestID, bestSeq = item.ID, item.Seq
			}
		}
		if bestID != "" {
			for i := len(c.task.Context.Evidence) - 1; i >= 0; i-- {
				record := c.task.Context.Evidence[i]
				if record.Artifact == "cognition:"+bestID && isSupporting(record) {
					return record, true
				}
			}
		}
	}
	// A host may record the change directly. Fall back to the newest current
	// change record the task itself carries.
	var best EvidenceRecord
	found := false
	for _, record := range c.task.Context.Evidence {
		if !isChangeEvidence(record) || !isSupporting(record) {
			continue
		}
		if !found || record.At.After(best.At) {
			best, found = record, true
		}
	}
	return best, found
}

// verificationsAfterChange returns current supporting checks recorded after the
// change they are meant to cover. The runtime's observation order is the
// ordering authority when both records resolve to cognition items; the durable
// record timestamp is the fallback for host-recorded evidence.
func (c *Controller) verificationsAfterChange(change EvidenceRecord) []EvidenceRecord {
	changeSeq, changeSeqOK := c.observationSeq(change)
	var out []EvidenceRecord
	for _, record := range c.task.Context.Evidence {
		if record.ID == change.ID || record.Validity != EvidenceCurrent || !isVerificationEvidence(record) {
			continue
		}
		if !isSupporting(record) {
			continue
		}
		if changeSeqOK {
			if seq, ok := c.observationSeq(record); ok {
				if seq > changeSeq {
					out = append(out, record)
				}
				continue
			}
			// An unordered record cannot be shown to follow the change.
			continue
		}
		if record.At.After(change.At) {
			out = append(out, record)
		}
	}
	return out
}

// observationSeq maps a task evidence record to the runtime observation order
// of the cognition item it references, when that item is still retained.
func (c *Controller) observationSeq(record EvidenceRecord) (int, bool) {
	if c.evidence == nil || !strings.HasPrefix(record.Artifact, "cognition:") {
		return 0, false
	}
	id := strings.TrimPrefix(record.Artifact, "cognition:")
	for _, item := range c.evidence.Items {
		if item.ID == id {
			return item.Seq, true
		}
	}
	return 0, false
}

// verificationEvidence returns current check observations, newest first.
func (c *Controller) verificationEvidence() []EvidenceRecord {
	var out []EvidenceRecord
	for i := len(c.task.Context.Evidence) - 1; i >= 0; i-- {
		record := c.task.Context.Evidence[i]
		if record.Validity != EvidenceCurrent || !isVerificationEvidence(record) {
			continue
		}
		if isSupporting(record) {
			out = append(out, record)
		}
	}
	return out
}

// executionEvidence returns current observations of a successful execution.
func (c *Controller) executionEvidence() []EvidenceRecord {
	var out []EvidenceRecord
	for i := len(c.task.Context.Evidence) - 1; i >= 0; i-- {
		record := c.task.Context.Evidence[i]
		if !isSupporting(record) {
			continue
		}
		if isExecutedEvidence(record) {
			out = append(out, record)
		}
	}
	return out
}

// isVerificationEvidence reports whether a task record is a check observation.
func isVerificationEvidence(record EvidenceRecord) bool {
	return record.Kind == EvidenceTest || strings.Contains(record.Source, "check:")
}

// verificationsCoverContract reports whether every verification requirement
// the contract declares is met by at least one current check. It is a coarse
// durable check, not a replacement for the evaluator's target and behavioural
// analysis: it verifies that a check of the required kind reported coverage of
// the required scope, and refuses a generic check that covers neither.
func (c *Controller) verificationsCoverContract(records []EvidenceRecord) bool {
	if c.task.Contract == nil {
		return true
	}
	requirements := c.task.Contract.Verification
	if len(requirements) == 0 {
		return true
	}
	for _, requirement := range requirements {
		covered := false
		for _, record := range records {
			if verificationCovers(record, requirement) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// verificationCovers reports whether one check meets one requirement.
func verificationCovers(record EvidenceRecord, requirement contract.VerificationRequirement) bool {
	if check := requirement.EffectiveCheck(); check != contract.CheckAny && check != "" {
		if !strings.Contains(record.Source, "check:"+string(check)) {
			return false
		}
	}
	// A requirement that names no scope accepts any coverage; the evaluator
	// still bounds a wildcard behavioural objective to the changed packages.
	if strings.TrimSpace(requirement.Scope) == "" {
		return true
	}
	want := normalizeVerificationScope(requirement.Scope)
	if want == "*" {
		return true
	}
	got := normalizeVerificationScope(record.Scope)
	if got == "" {
		// A check whose scope was not recorded cannot be shown to cover a
		// named requirement.
		return false
	}
	if got == "*" || got == want {
		return true
	}
	return strings.HasPrefix(got, want+"/")
}

func normalizeVerificationScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "*" {
		return "*"
	}
	scope = strings.TrimPrefix(scope, "./")
	scope = strings.TrimSuffix(scope, "/")
	if scope == "" {
		return "."
	}
	return scope
}

// isChangeEvidence reports whether a task record describes a definite change.
func isChangeEvidence(record EvidenceRecord) bool {
	return record.Kind == EvidenceArtifact
}

func appendUniqueEvidenceIDs(ids []string, record EvidenceRecord) []string {
	for _, existing := range ids {
		if existing == record.ID {
			return ids
		}
	}
	return append(ids, record.ID)
}

func intersects(values []string, candidates []string) bool {
	for _, value := range values {
		for _, candidate := range candidates {
			if value == candidate {
				return true
			}
		}
	}
	return false
}

func verificationSummaries(records []EvidenceRecord) []string {
	const limit = 8
	out := make([]string, 0, limit)
	for _, record := range records {
		if len(out) >= limit {
			break
		}
		out = append(out, boundText(record.Summary, maxEvidenceSummaryBytes))
	}
	return out
}
