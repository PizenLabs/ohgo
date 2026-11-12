package task

import (
	"errors"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// Bounds for the host projection. They keep a long run from turning a
// checkpoint into a transcript: the newest observations are projected first,
// role notes are capped per pass, and the task's own record bounds still
// apply.
const (
	maxProjectedPerPass = 48
	maxRoleNotesPerPass = 6
)

// ObserveTurns folds completed tool outcomes into the durable evidence state.
//
// The loop already observes outcomes before the next request. This call covers
// a run that stopped before that projection ran — an interruption, a deadline,
// or a final answer — so the turn boundary is also an evidence boundary. The
// fold is idempotent per call id, so calling it twice adds nothing.
func (c *Controller) ObserveTurns(turns []agent.Turn) {
	if len(turns) == 0 {
		return
	}
	if c.evidence == nil {
		c.evidence = cognition.NewEvidenceState()
	}
	c.evidence.ObserveTurns(cognition.TaskShape{}, turns)
}

// projectTaskContext turns the runtime's material observations into durable,
// task-linked context at a checkpoint boundary.
//
// The cognition evidence state remains the observation owner: the task records
// a bounded summary, a provenance reference, and an artifact pointer into that
// state, never a second copy of the payload. Only the newest observations are
// projected per pass; the task's own bounds stop the log from growing without
// limit.
func (c *Controller) projectTaskContext() {
	if c.task.ID == "" {
		return
	}
	role := c.role
	if !role.Valid() {
		role = c.currentRole()
	}
	if role.Valid() && c.evidence != nil && !c.evidence.Empty() {
		projected, noted := 0, 0
		for i := len(c.evidence.Items) - 1; i >= 0 && projected < maxProjectedPerPass; i-- {
			item := c.evidence.Items[i]
			if !materialObservation(item) {
				continue
			}
			record, created := c.projectObservation(item, role)
			if created {
				projected++
			}
			if record.ID == "" || noted >= maxRoleNotesPerPass {
				continue
			}
			c.projectRoleObservation(item, role, record)
			noted++
		}
	}
	c.projectTodos()
}

// materialObservation reports whether an evidence item is worth a task-linked
// reference. Transient output (a background spawn, a one-line exploration) is
// deliberately excluded; a diagnostic is kept because a failure is a real
// observation even when it cannot support a fact.
func materialObservation(item cognition.EvidenceItem) bool {
	switch item.Status {
	case cognition.StatusKnown, cognition.StatusDerived, cognition.StatusConflicting:
	default:
		return false
	}
	if item.Retention == cognition.RetentionTransient {
		return false
	}
	return strings.TrimSpace(item.Summary) != ""
}

// projectObservation records one evidence reference and, when the observation
// is a successful check, the fact it directly establishes. A conflicting
// observation is recorded as inconclusive and never replaces the observation
// it disagrees with.
func (c *Controller) projectObservation(item cognition.EvidenceItem, role workspace.Workspace) (EvidenceRecord, bool) {
	dedup := observationDedup(item)
	if existing, ok := c.evidenceByDedup(dedup); ok {
		return existing, false
	}
	record, err := c.task.RecordEvidence(EvidenceInput{
		Kind:     observationKind(item),
		Role:     role,
		Source:   observationSource(item),
		Scope:    item.Source.Scope,
		Artifact: "cognition:" + item.ID,
		Summary:  item.Summary,
		Outcome:  observationOutcome(item),
		DedupKey: dedup,
		Digest:   item.Digest,
		At:       c.now().UTC(),
	})
	if err != nil {
		if errors.Is(err, ErrContextLimit) {
			c.noteProjectionLimit("evidence")
		}
		return EvidenceRecord{}, false
	}
	if item.Status != cognition.StatusConflicting {
		if prior, ok := c.currentEvidenceByArtifact("cognition:" + item.ID); ok && prior.ID != record.ID {
			_, _ = c.task.SupersedeEvidence(prior.ID, record.ID, c.now().UTC())
		}
	}
	if isVerificationObservation(item) {
		c.recordVerificationFact(item, record, role)
	}
	return record, true
}

// recordVerificationFact records what a successful check directly established.
//
// It does not claim the objective is achieved: a passing test proves the
// recorded test passed under its recorded conditions, which is exactly the
// statement persisted here. Semantic completion remains the evaluator's job.
func (c *Controller) recordVerificationFact(item cognition.EvidenceItem, record EvidenceRecord, role workspace.Workspace) {
	text := boundText(item.Summary, maxFactBytes)
	if text == "" {
		return
	}
	for _, fact := range c.task.Context.Facts {
		if fact.Text != text {
			continue
		}
		// Respect every disposition, not just active: a host that superseded
		// or rejected this conclusion must not have it recreated from the same
		// evidence on the next checkpoint.
		for _, id := range fact.EvidenceIDs {
			if id == record.ID {
				return
			}
		}
	}
	if _, err := c.task.RecordFact(FactInput{
		Text:        text,
		Role:        role,
		EvidenceIDs: []string{record.ID},
		At:          c.now().UTC(),
	}); err != nil && errors.Is(err, ErrContextLimit) {
		c.noteProjectionLimit("fact")
	}
}

// noteProjectionLimit reports that a bounded log stopped accepting records.
// The existing load warning wins when one is already present: a repair is the
// more important continuity fact for this checkpoint.
func (c *Controller) noteProjectionLimit(kind string) {
	if c.task.ContextWarning != "" {
		return
	}
	c.task.ContextWarning = "task context limit reached: newer " + kind + " records were not added"
}

// projectRoleObservation records the role-specific meaning of one material
// observation. Each role receives only the facts about its own execution; a
// role never sees another role's plan, scope, strategy, or notes.
func (c *Controller) projectRoleObservation(item cognition.EvidenceItem, role workspace.Workspace, record EvidenceRecord) {
	note := boundText(item.Summary, maxFactBytes)
	if note == "" {
		return
	}
	switch role {
	case workspace.Ask:
		if !isReadObservation(item) {
			return
		}
		c.appendRoleContext(role, RoleContextUpdate{
			Progress:  []string{"inspected " + note},
			Artifacts: []string{cognitionArtifact(item)},
		})
	case workspace.Fuzz:
		if !isInvestigationObservation(item) {
			return
		}
		c.appendRoleContext(role, RoleContextUpdate{
			Progress:  []string{note},
			Artifacts: []string{cognitionArtifact(item)},
		})
	case workspace.Build:
		if !isImplementationObservation(item) {
			return
		}
		update := RoleContextUpdate{Progress: []string{note}}
		if isMutationObservation(item) {
			if path := observationPath(item); path != "" {
				update.Scope = []string{path}
			}
			update.Artifacts = []string{cognitionArtifact(item)}
		}
		if isVerificationObservation(item) {
			update.Artifacts = []string{cognitionArtifact(item)}
		}
		c.appendRoleContext(role, update)
	}
}

// projectTodos turns the runtime's todo snapshot into BUILD's plan and every
// role's current step. A todo is a model proposal, not a verified fact, so it
// is recorded as execution context and never promoted to a fact.
func (c *Controller) projectTodos() {
	if c.task.ID == "" || !c.role.Valid() {
		return
	}
	var plan, progress []string
	if active, ok := c.task.ActiveTodo(); ok {
		progress = append(progress, "working on: "+active.Label)
	}
	if c.role == workspace.Build {
		for _, todo := range c.task.Todos {
			if todo.Status == TodoDone {
				continue
			}
			plan = append(plan, todo.Label)
			if len(plan) >= MaxRoleNotes {
				break
			}
		}
	}
	if len(plan) == 0 && len(progress) == 0 {
		return
	}
	c.appendRoleContext(c.role, RoleContextUpdate{Plan: plan, Progress: progress})
}

func (c *Controller) appendRoleContext(role workspace.Workspace, update RoleContextUpdate) {
	if !role.Valid() || c.task.ID == "" {
		return
	}
	update.Role = role
	_, _ = c.task.UpdateRoleContext(role, update, c.now().UTC())
}

func (c *Controller) evidenceByDedup(key string) (EvidenceRecord, bool) {
	for _, record := range c.task.Context.Evidence {
		if record.DedupKey == key {
			return record, true
		}
	}
	return EvidenceRecord{}, false
}

func (c *Controller) currentEvidenceByArtifact(artifact string) (EvidenceRecord, bool) {
	for i := len(c.task.Context.Evidence) - 1; i >= 0; i-- {
		record := c.task.Context.Evidence[i]
		if record.Artifact == artifact && record.Validity == EvidenceCurrent {
			return record, true
		}
	}
	return EvidenceRecord{}, false
}

// observationDedup identifies one exact observation. The digest is part of the
// key so a changed file or a rerun with different output is a new observation,
// not a silent replay of the old one.
func observationDedup(item cognition.EvidenceItem) string {
	key := "cognition:" + item.ID
	if item.Digest != "" {
		key += ":" + item.Digest
	}
	return key
}

// observationKind maps the runtime's evidence source to the task vocabulary.
func observationKind(item cognition.EvidenceItem) EvidenceKind {
	switch {
	case item.Source.URL != "" || item.Source.Trust != "":
		return EvidenceExternal
	case item.Runtime:
		// A live runtime is an execution observation, not merely a test. The
		// distinct kind is what lets a runtime objective prove itself without
		// accepting any other successful command.
		return EvidenceRuntime
	case isVerificationObservation(item):
		return EvidenceTest
	case item.Source.Tool == "bash":
		return EvidenceCommand
	case isMutationObservation(item):
		return EvidenceArtifact
	case isReadTool(item.Source.Tool):
		return EvidenceSource
	default:
		return EvidenceTool
	}
}

// observationOutcome separates what was observed from what can support a
// claim. A conflict, a derived inference, and a diagnostic are honest records,
// but none of them may establish a fact. A process that actually ran to a
// successful terminal state is an observation even when its retention class is
// diagnostic: that is what lets a projected successful command support an
// execution objective.
func observationOutcome(item cognition.EvidenceItem) EvidenceOutcome {
	if item.Executed && item.Status == cognition.StatusKnown {
		return EvidenceObserved
	}
	if item.Status == cognition.StatusConflicting || item.Status == cognition.StatusDerived {
		return EvidenceInconclusive
	}
	if item.Retention == cognition.RetentionDiagnostic && !isVerificationObservation(item) {
		return EvidenceInconclusive
	}
	return EvidenceObserved
}

// observationSource is a reference, not a payload: tool and call identity,
// path, URL, and check kind are enough to find the durable observation.
func observationSource(item cognition.EvidenceItem) string {
	var parts []string
	if item.Source.Tool != "" {
		parts = append(parts, item.Source.Tool)
	}
	if item.Source.Path != "" {
		parts = append(parts, item.Source.Path)
	}
	if item.Source.CallID != "" {
		parts = append(parts, item.Source.CallID)
	}
	if item.Source.URL != "" {
		parts = append(parts, item.Source.URL)
	}
	if item.Source.Check != "" {
		parts = append(parts, "check:"+item.Source.Check)
	}
	if len(parts) == 0 {
		return cognitionArtifact(item)
	}
	return boundText(strings.Join(parts, ":"), maxEvidenceRefBytes)
}

func cognitionArtifact(item cognition.EvidenceItem) string { return "cognition:" + item.ID }

func isVerificationObservation(item cognition.EvidenceItem) bool {
	return item.Source.Check != "" || strings.HasPrefix(item.ID, "verification:")
}

func isMutationObservation(item cognition.EvidenceItem) bool {
	return strings.HasPrefix(item.ID, "mutation:")
}

func isReadObservation(item cognition.EvidenceItem) bool {
	// A failed read is a diagnostic, not an inspection. It is still shared
	// evidence; it is just not reported as work ASK completed.
	return isReadTool(item.Source.Tool) && item.Retention != cognition.RetentionDiagnostic
}

func isReadTool(tool string) bool {
	switch tool {
	case "read", "ls", "glob", "grep", "symbols", "refs", "graph", "structure":
		return true
	default:
		return false
	}
}

func isInvestigationObservation(item cognition.EvidenceItem) bool {
	return item.Source.Tool == "bash" || isVerificationObservation(item) ||
		isMutationObservation(item) || item.Retention == cognition.RetentionDiagnostic ||
		item.Retention == cognition.RetentionExternal
}

func isImplementationObservation(item cognition.EvidenceItem) bool {
	if isMutationObservation(item) || isVerificationObservation(item) {
		return true
	}
	// A failed write or check is implementation progress even though it can
	// never support a fact.
	return item.Retention == cognition.RetentionDiagnostic &&
		(isWriteTool(item.Source.Tool) || item.Source.Tool == "bash")
}

func isWriteTool(tool string) bool {
	switch tool {
	case "write", "edit", "delete":
		return true
	default:
		return false
	}
}

func observationPath(item cognition.EvidenceItem) string {
	// The source path is the only trustworthy coordinate. A bash mutation's
	// id carries a command digest, which is not a path and must never become
	// BUILD's recorded scope.
	return item.Source.Path
}
