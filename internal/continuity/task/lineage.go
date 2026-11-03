package task

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// LineageStage is the strongest status one record can truthfully claim.
//
// The vocabulary is deliberately separate from the lifecycle status. A fact is
// VERIFIED only while current evidence supports it; an observation is
// EXECUTED, OBSERVED, or UNCERTAIN; a hypothesis is PROPOSED until evidence
// resolves it. A downstream view must never promote one stage into another.
type LineageStage string

// Lineage stages.
const (
	// StageProposed is an unverified proposal.
	StageProposed LineageStage = "PROPOSED"
	// StageExecuted is a record of an executed process or check.
	StageExecuted LineageStage = "EXECUTED"
	// StageObserved is a direct observation that may support a conclusion.
	StageObserved LineageStage = "OBSERVED"
	// StageVerified is a conclusion with current supporting evidence.
	StageVerified LineageStage = "VERIFIED"
	// StageContradicted is a conclusion current evidence disputes.
	StageContradicted LineageStage = "CONTRADICTED"
	// StageUncertain is an inconclusive or incomplete observation.
	StageUncertain LineageStage = "UNCERTAIN"
	// StageInvalidated is a record whose support no longer applies.
	StageInvalidated LineageStage = "INVALIDATED"
	// StageSuperseded is a record replaced by a later one.
	StageSuperseded LineageStage = "SUPERSEDED"
)

// LineageEntry is one conclusion or observation with its traceable references.
//
// It is derived from the authoritative records on every call and is never
// persisted, so it cannot become a second source of truth. Artifacts and
// evidence stay references: the entry names the record, never a copied result.
type LineageEntry struct {
	// Kind is fact, hypothesis, or evidence.
	Kind string
	// ID is the record identity.
	ID string
	// Text is the bounded statement or observation summary.
	Text string
	// Stage is the strongest truthful status.
	Stage LineageStage
	// Status is the record's own disposition, for traceability.
	Status string
	// Role is the role that produced the record.
	Role workspace.Workspace
	// At is when the record was created.
	At string
	// EvidenceIDs are the records supporting this one.
	EvidenceIDs []string
	// Contradicts names records this observation disputes.
	Contradicts []string
	// Artifact is the durable reference for an observation, when any.
	Artifact string
	// Missing names referenced records that no longer resolve.
	Missing []string
	// Note explains an invalidation, supersession, or resolution.
	Note string
}

// Lineage returns the task's derived lineage with artifact references checked
// against the durable observation state. It is read-only.
func (c *Controller) Lineage() []LineageEntry {
	entries := Lineage(c.task)
	for i := range entries {
		c.checkArtifact(&entries[i])
	}
	return entries
}

// TraceLineage is the controller view of TraceLineage, with artifact references
// checked against the durable observation state.
func (c *Controller) TraceLineage(id string) (LineageEntry, []LineageEntry, bool) {
	focus, related, ok := TraceLineage(c.task, id)
	if !ok {
		return focus, related, false
	}
	c.checkArtifact(&focus)
	for i := range related {
		c.checkArtifact(&related[i])
	}
	return focus, related, true
}

// checkArtifact records an unresolvable cognition reference. Any other artifact
// is a path or an external pointer: it is preserved as a reference, and this
// package cannot prove a filesystem path still exists.
func (c *Controller) checkArtifact(entry *LineageEntry) {
	const prefix = "cognition:"
	if entry.Artifact == "" || !strings.HasPrefix(entry.Artifact, prefix) {
		return
	}
	id := strings.TrimPrefix(entry.Artifact, prefix)
	resolved := false
	if c.evidence != nil {
		for _, item := range c.evidence.Items {
			if item.ID == id {
				resolved = true
				break
			}
		}
	}
	if !resolved {
		entry.Missing = append(entry.Missing, entry.Artifact)
	}
}

// Lineage returns every conclusion and observation in record order.
//
// Facts come first because they are the conclusions a reader is trying to
// trace, followed by hypotheses and the evidence records that support or
// dispute them.
func Lineage(t Task) []LineageEntry {
	out := make([]LineageEntry, 0, len(t.Context.Facts)+len(t.Context.Hypotheses)+len(t.Context.Evidence))
	for _, fact := range t.Context.Facts {
		out = append(out, factLineage(t, fact))
	}
	for _, hypothesis := range t.Context.Hypotheses {
		out = append(out, hypothesisLineage(t, hypothesis))
	}
	for _, record := range t.Context.Evidence {
		out = append(out, evidenceLineage(t, record))
	}
	return out
}

// TraceLineage locates one record and returns it with the records its
// conclusion depends on or disputes. Related entries are bounded and ordered:
// supporting evidence first, then contradictions, then the fact a hypothesis
// produced.
func TraceLineage(t Task, id string) (LineageEntry, []LineageEntry, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return LineageEntry{}, nil, false
	}
	var (
		focus   LineageEntry
		related []LineageEntry
		found   bool
	)
	if fact, ok := t.Context.factByID(id); ok {
		focus, found = factLineage(t, fact), true
		for _, evidenceID := range fact.EvidenceIDs {
			if record, ok := t.Context.evidenceByID(evidenceID); ok {
				related = append(related, evidenceLineage(t, record))
			}
		}
		if fact.FromHypothesis != "" {
			if hypothesis, ok := t.Context.hypothesisByID(fact.FromHypothesis); ok {
				related = append(related, hypothesisLineage(t, hypothesis))
			}
		}
	} else if hypothesis, ok := t.Context.hypothesisByID(id); ok {
		focus, found = hypothesisLineage(t, hypothesis), true
		for _, evidenceID := range hypothesis.EvidenceIDs {
			if record, ok := t.Context.evidenceByID(evidenceID); ok {
				related = append(related, evidenceLineage(t, record))
			}
		}
		for _, fact := range t.Context.Facts {
			if fact.FromHypothesis == id {
				related = append(related, factLineage(t, fact))
			}
		}
	} else if record, ok := t.Context.evidenceByID(id); ok {
		focus, found = evidenceLineage(t, record), true
		for _, target := range record.Contradicts {
			if fact, ok := t.Context.factByID(target); ok {
				related = append(related, factLineage(t, fact))
			}
			if hypothesis, ok := t.Context.hypothesisByID(target); ok {
				related = append(related, hypothesisLineage(t, hypothesis))
			}
		}
		for _, fact := range t.Context.Facts {
			if lineageContains(fact.EvidenceIDs, id) {
				related = append(related, factLineage(t, fact))
			}
		}
		for _, hypothesis := range t.Context.Hypotheses {
			if lineageContains(hypothesis.EvidenceIDs, id) {
				related = append(related, hypothesisLineage(t, hypothesis))
			}
		}
	}
	if !found {
		return LineageEntry{}, nil, false
	}
	for _, record := range t.ContradictingEvidence(id) {
		related = append(related, evidenceLineage(t, record))
	}
	return focus, boundLineage(related), true
}

func factLineage(t Task, fact Fact) LineageEntry {
	stage := StageVerified
	switch fact.Status {
	case FactSuperseded:
		stage = StageSuperseded
	case FactInvalidated:
		stage = StageInvalidated
	case FactActive:
		if len(t.ContradictingEvidence(fact.ID)) > 0 {
			stage = StageContradicted
		}
	}
	missing := missingEvidence(t, fact.EvidenceIDs)
	if stage == StageVerified && len(missing) > 0 {
		stage = StageInvalidated
	}
	note := ""
	if fact.SupersededBy != "" {
		note = "superseded by fact #" + fact.SupersededBy
	}
	return LineageEntry{
		Kind:        "fact",
		ID:          fact.ID,
		Text:        fact.Text,
		Stage:       stage,
		Status:      string(fact.Status),
		Role:        fact.Role,
		At:          timeText(fact.At),
		EvidenceIDs: append([]string(nil), fact.EvidenceIDs...),
		Missing:     missing,
		Note:        note,
	}
}

func hypothesisLineage(t Task, hypothesis Hypothesis) LineageEntry {
	stage := StageProposed
	switch hypothesis.Status {
	case HypothesisConfirmed:
		stage = StageVerified
	case HypothesisRejected:
		stage = StageContradicted
	case HypothesisSuperseded:
		stage = StageSuperseded
	case HypothesisInvalidated:
		stage = StageInvalidated
	}
	missing := missingEvidence(t, hypothesis.EvidenceIDs)
	if stage == StageVerified && len(missing) > 0 {
		stage = StageInvalidated
	}
	return LineageEntry{
		Kind:        "hypothesis",
		ID:          hypothesis.ID,
		Text:        hypothesis.Text,
		Stage:       stage,
		Status:      string(hypothesis.Status),
		Role:        hypothesis.Role,
		At:          timeText(hypothesis.At),
		EvidenceIDs: append([]string(nil), hypothesis.EvidenceIDs...),
		Missing:     missing,
		Note:        hypothesis.Resolution,
	}
}

func evidenceLineage(t Task, record EvidenceRecord) LineageEntry {
	stage := StageObserved
	switch {
	case record.Validity == EvidenceInvalid:
		stage = StageInvalidated
	case record.Validity == EvidenceSuperseded:
		stage = StageSuperseded
	case record.Outcome == EvidenceContradicting:
		stage = StageContradicted
	case record.Outcome == EvidenceInconclusive:
		stage = StageUncertain
	case isExecutedEvidence(record):
		stage = StageExecuted
	}
	missing := missingClaims(t, record.Contradicts)
	return LineageEntry{
		Kind:        "evidence",
		ID:          record.ID,
		Text:        record.Summary,
		Stage:       stage,
		Status:      string(record.Validity),
		Role:        record.Role,
		At:          timeText(record.At),
		Contradicts: append([]string(nil), record.Contradicts...),
		Artifact:    record.Artifact,
		Missing:     missing,
		Note:        record.Note,
	}
}

// isExecutedEvidence reports whether the record came from running a process or
// check. A passing check is still an observation of that check, never proof
// that the whole objective was met.
func isExecutedEvidence(record EvidenceRecord) bool {
	switch record.Kind {
	case EvidenceCommand, EvidenceTest, EvidenceBenchmark, EvidenceReproduction:
		return true
	}
	return strings.Contains(record.Source, "check:")
}

func missingClaims(t Task, ids []string) []string {
	var missing []string
	for _, id := range ids {
		if !t.hasClaim(id) {
			missing = append(missing, id)
		}
	}
	return missing
}

func missingEvidence(t Task, ids []string) []string {
	var missing []string
	for _, id := range ids {
		if _, ok := t.Context.evidenceByID(id); !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func timeText(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// boundLineage keeps related entries bounded and de-duplicated by kind and id.
func boundLineage(entries []LineageEntry) []LineageEntry {
	const limit = 16
	out := make([]LineageEntry, 0, limit)
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		key := entry.Kind + ":" + entry.ID
		if entry.ID == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, entry)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func lineageContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// LineageSummary renders a bounded one-line explanation for a record. It is
// presentation used by hosts that do not have their own renderer.
func LineageSummary(entry LineageEntry) string {
	parts := []string{fmt.Sprintf("%s#%s [%s/%s]", entry.Kind, entry.ID, entry.Stage, entry.Status)}
	if text := strings.TrimSpace(entry.Text); text != "" {
		parts = append(parts, truncate(text, 200))
	}
	if len(entry.EvidenceIDs) > 0 {
		parts = append(parts, "evidence:"+strings.Join(entry.EvidenceIDs, ","))
	}
	if entry.Artifact != "" {
		parts = append(parts, "artifact:"+entry.Artifact)
	}
	if len(entry.Contradicts) > 0 {
		parts = append(parts, "contradicts:"+strings.Join(entry.Contradicts, ","))
	}
	if len(entry.Missing) > 0 {
		parts = append(parts, "missing:"+strings.Join(entry.Missing, ","))
	}
	if entry.Note != "" {
		parts = append(parts, entry.Note)
	}
	return truncate(strings.Join(parts, " "), 420)
}
