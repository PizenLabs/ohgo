package task

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A meaningful transition is durable, references the record it describes, and
// is not duplicated by a later checkpoint of the same action.
func TestTimelineRecordsMeaningfulEventsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	role := workspace.Build
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the parser suite passed",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	fact, err := controller.RecordFact(FactInput{
		Text: "the parser handles the reported input", EvidenceIDs: []string{evidence.ID},
	})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}

	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	var kinds []TimelineKind
	for _, event := range restored.Timeline {
		kinds = append(kinds, event.Kind)
	}
	for _, want := range []TimelineKind{TimelineCreated, TimelineVerification, TimelineFact} {
		if !containsKind(kinds, want) {
			t.Fatalf("timeline = %v, want %q", kinds, want)
		}
	}
	last, ok := restored.LastEvent()
	if !ok {
		t.Fatal("timeline is empty after restart")
	}
	if last.Kind != TimelineFact || len(last.Refs) == 0 || last.Refs[0] != fact.ID {
		t.Fatalf("last event = %+v, want the fact reference", last)
	}
	// The event is an index: the fact text is bounded and the authoritative
	// record is still the one that carries the evidence reference.
	if len(last.Summary) > maxTimelineSummaryBytes {
		t.Fatalf("event summary is unbounded: %d bytes", len(last.Summary))
	}
}

// The bounded timeline keeps the creation event and the newest events, and the
// sequence gap makes the omission visible rather than silent.
func TestTimelineBoundKeepsCreationAndReportsDroppedEvents(t *testing.T) {
	task := Task{ID: "t1", Objective: "keep history bounded", Status: StatusRunning}
	for i := 0; i < MaxTimelineEvents+10; i++ {
		task.AppendEvent(TimelineEvent{Kind: TimelineCheckpoint, Summary: "checkpoint"})
	}
	if len(task.Timeline) != MaxTimelineEvents {
		t.Fatalf("timeline = %d events, want %d", len(task.Timeline), MaxTimelineEvents)
	}
	if task.Timeline[0].Seq != 1 {
		t.Fatalf("creation event seq = %d, want 1", task.Timeline[0].Seq)
	}
	if got := task.TimelineDropped(); got != 10 {
		t.Fatalf("dropped = %d, want 10 visible from the sequence gap", got)
	}
}

// A damaged event is repaired or dropped; valid history survives and ordering
// is restored.
func TestTimelineSanitizeRepairsDamageWithoutDiscardingHistory(t *testing.T) {
	events := []TimelineEvent{
		{Seq: 1, Kind: TimelineCreated, Summary: "created"},
		{Seq: 1, Kind: TimelineCheckpoint, Summary: ""},
		{Seq: 2, Kind: TimelineKind("bogus"), Summary: "not a real event"},
		{Seq: 2, Kind: TimelinePaused, Summary: "paused", Role: workspace.Workspace("nope")},
	}
	clean, changes := sanitizeTimeline(events)
	if changes == 0 {
		t.Fatal("damaged events were accepted without a repair count")
	}
	if len(clean) != 3 {
		t.Fatalf("clean = %d events, want 3", len(clean))
	}
	for i := 1; i < len(clean); i++ {
		if clean[i].Seq <= clean[i-1].Seq {
			t.Fatalf("sequence not repaired: %+v", clean)
		}
	}
	if clean[1].Summary != string(TimelineCheckpoint) {
		t.Fatalf("empty summary = %q, want the kind label", clean[1].Summary)
	}
	if clean[2].Role != "" {
		t.Fatalf("invalid role = %q, want dropped", clean[2].Role)
	}
}

// A conclusion traces to the observations that support it, then to the
// observation that contradicts it. Proposed, executed, observed, and verified
// states stay distinguishable, and artifacts remain references.
func TestLineageTracesSupportContradictionAndArtifact(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("diagnose the crash"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	observed, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "go test ./...", Summary: "the test failed",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	executed, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test -run TestCrash", Summary: "the reproduction failed",
		Outcome: EvidenceObserved, Artifact: "cognition:observed-result",
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{Text: "the scheduler drops the last job"})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	current, _ := controller.Current()
	proposed := hypothesisLineage(current, current.Context.Hypotheses[0])
	if proposed.Stage != StageProposed {
		t.Fatalf("hypothesis stage = %s, want PROPOSED", proposed.Stage)
	}

	confirmed, fact, err := controller.ConfirmHypothesis(hypothesis.ID, []string{observed.ID}, workspace.Fuzz)
	if err != nil {
		t.Fatalf("ConfirmHypothesis: %v", err)
	}
	if confirmed.Status != HypothesisConfirmed {
		t.Fatalf("hypothesis status = %s", confirmed.Status)
	}
	current, _ = controller.Current()
	verified := factLineage(current, current.Context.Facts[0])
	if verified.Stage != StageVerified {
		t.Fatalf("fact stage = %s, want VERIFIED", verified.Stage)
	}
	if len(verified.EvidenceIDs) != 1 || verified.EvidenceIDs[0] != observed.ID {
		t.Fatalf("verified evidence = %v, want %s", verified.EvidenceIDs, observed.ID)
	}

	// The executed observation keeps its artifact reference and is classified
	// as EXECUTED, not promoted to a verified conclusion.
	executedEntry := evidenceLineage(current, current.Context.Evidence[1])
	if executedEntry.Stage != StageExecuted || executedEntry.Artifact != "cognition:observed-result" {
		t.Fatalf("executed evidence = %+v, want EXECUTED with its artifact reference", executedEntry)
	}

	contradiction, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "go test -run TestScheduler", Summary: "the scheduler kept the job",
		Outcome: EvidenceContradicting, Contradicts: []string{fact.ID},
	})
	if err != nil {
		t.Fatalf("RecordEvidence contradiction: %v", err)
	}
	current, _ = controller.Current()
	focus, related, ok := TraceLineage(current, fact.ID)
	if !ok {
		t.Fatal("fact lineage was not found")
	}
	if focus.Stage != StageContradicted {
		t.Fatalf("contradicted fact stage = %s", focus.Stage)
	}
	foundContradiction := false
	for _, entry := range related {
		if entry.ID == contradiction.ID && entry.Stage == StageContradicted {
			foundContradiction = true
			if len(entry.Missing) > 0 {
				t.Fatalf("contradiction refs = %v, want the claim to resolve", entry.Missing)
			}
		}
	}
	if !foundContradiction {
		t.Fatalf("trace = %+v, want the contradicting observation", related)
	}

	// Invalidating the supporting observation keeps the fact as invalidated
	// history instead of silently deleting it.
	if _, err := controller.InvalidateEvidence(observed.ID, "the result was produced against another revision"); err != nil {
		t.Fatalf("InvalidateEvidence: %v", err)
	}
	current, _ = controller.Current()
	focus, _, _ = TraceLineage(current, fact.ID)
	if focus.Stage != StageInvalidated {
		t.Fatalf("invalidated fact stage = %s", focus.Stage)
	}
	if _, ok := current.Context.factByID(fact.ID); !ok {
		t.Fatal("invalidated fact was deleted")
	}
	_ = executed
}

// Recovery distinguishes an intact checkpoint, a reconstruction, a state that
// needs a human decision, and an unrecoverable load.
func TestRecoveryClassificationIsTruthful(t *testing.T) {
	t.Run("intact", func(t *testing.T) {
		root := t.TempDir()
		controller := NewController(NewStore(root), nil)
		if _, err := controller.Begin(testIntent("recover me"), nil); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		reopened := NewController(NewStore(root), nil)
		if _, found, err := reopened.Restore(); err != nil || !found {
			t.Fatalf("Restore: found=%v err=%v", found, err)
		}
		report := reopened.Recovery()
		if report.State != RecoveryIntact {
			t.Fatalf("recovery = %+v, want INTACT", report)
		}
		if !strings.Contains(report.Detail, "valid checkpoint") {
			t.Fatalf("detail = %q, want the observed checkpoint fact", report.Detail)
		}
	})

	t.Run("reconstructed without projection", func(t *testing.T) {
		root := t.TempDir()
		store := NewStore(root)
		controller := NewController(store, nil)
		if _, err := controller.Begin(testIntent("recover me"), nil); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := os.Remove(store.TaskPath()); err != nil {
			t.Fatalf("remove projection: %v", err)
		}
		reopened := NewController(store, nil)
		if _, found, err := reopened.Restore(); err != nil || !found {
			t.Fatalf("Restore: found=%v err=%v", found, err)
		}
		report := reopened.Recovery()
		if report.State != RecoveryReconstructed {
			t.Fatalf("recovery = %+v, want RECONSTRUCTED", report)
		}
		if !containsString(report.Missing, TaskFileName) {
			t.Fatalf("missing = %v, want %s", report.Missing, TaskFileName)
		}
	})

	t.Run("needs human without contract", func(t *testing.T) {
		store := NewStore(t.TempDir())
		stored := Task{ID: "legacy", Objective: "finish the old work", Status: StatusRunning}
		if err := store.Save(stored); err != nil {
			t.Fatalf("Save: %v", err)
		}
		controller := NewController(store, nil)
		if _, found, err := controller.Restore(); err != nil || !found {
			t.Fatalf("Restore: found=%v err=%v", found, err)
		}
		report := controller.Recovery()
		if report.State != RecoveryNeedsHuman {
			t.Fatalf("recovery = %+v, want NEEDS_HUMAN", report)
		}
		if !strings.Contains(report.Detail, "completion contract") {
			t.Fatalf("detail = %q", report.Detail)
		}
	})

	t.Run("blocked unreadable state", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(store.ProgressPath(), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write bad checkpoint: %v", err)
		}
		controller := NewController(store, nil)
		if _, _, err := controller.Restore(); err == nil {
			t.Fatal("Restore accepted an unreadable checkpoint")
		}
		if report := controller.Recovery(); report.State != RecoveryBlocked {
			t.Fatalf("recovery = %+v, want BLOCKED", report)
		}
	})
}

// A mutation completion needs the verification the objective requires; a model
// claim or an evaluator boolean alone cannot record PROVEN.
func TestCompletionProofRequiresObservedVerification(t *testing.T) {
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
		t.Fatal("a mutation objective completed without observed verification")
	}
	if current, _ := controller.Current(); current.Status != StatusRunning {
		t.Fatalf("refused completion changed the status to %s", current.Status)
	}

	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceArtifact, Source: "write:parser.go", Summary: "changed parser.go",
		Outcome: EvidenceObserved, At: time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordEvidence(change): %v", err)
	}
	verification, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the parser suite passed",
		Outcome: EvidenceObserved, At: time.Unix(1001, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordEvidence(verification): %v", err)
	}
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.ObjectiveState() != ObjectiveProven {
		t.Fatalf("objective state = %s, want PROVEN", completed.ObjectiveState())
	}
	if completed.Completion == nil || len(completed.Completion.EvidenceIDs) != 2 ||
		!containsString(completed.Completion.EvidenceIDs, evidence.ID) ||
		!containsString(completed.Completion.EvidenceIDs, verification.ID) {
		t.Fatalf("proof = %+v, want both the change and its verification", completed.Completion)
	}
	if !completed.Completion.Observed || len(completed.Completion.Verified) != 1 {
		t.Fatalf("proof = %+v, want an observed change and one ordered verification", completed.Completion)
	}

	// The proof survives a restart.
	reopened := NewController(NewStore(root), nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ObjectiveState() != ObjectiveProven {
		t.Fatalf("restored objective state = %s, want PROVEN", restored.ObjectiveState())
	}

	// Invalidating the proof's evidence downgrades the projection without
	// rewriting the historical completion.
	if _, err := reopened.InvalidateEvidence(verification.ID, "the check was run against another revision"); err != nil {
		t.Fatalf("InvalidateEvidence: %v", err)
	}
	current, _ := reopened.Current()
	if current.Status != StatusCompleted {
		t.Fatalf("invalidation rewrote the lifecycle status to %s", current.Status)
	}
	if current.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("objective state = %s, want COMPLETED_UNPROVEN", current.ObjectiveState())
	}
	if !strings.Contains(current.CompletionSummary(), "no longer resolves") {
		t.Fatalf("summary = %q", current.CompletionSummary())
	}
}

// An informational objective is proven by a non-empty answer, which is the
// evidence its contract requires. A mutation proof cannot be borrowed for it.
func TestCompletionProofForInformationalObjectiveRequiresAnAnswer(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("explain the scheduler"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
		t.Fatal("an informational objective completed without an answer")
	}
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.ObjectiveState() != ObjectiveProven {
		t.Fatalf("objective state = %s, want PROVEN", completed.ObjectiveState())
	}
	if completed.Completion == nil || !completed.Completion.Answered {
		t.Fatalf("proof = %+v, want the answer evidence", completed.Completion)
	}
}

// The legacy Complete path records the lifecycle transition but never claims a
// proof it did not see.
func TestLegacyCompletionReportsUnproven(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("ship the change"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	completed, err := controller.Complete()
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", completed.Status)
	}
	if completed.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("objective state = %s, want COMPLETED_UNPROVEN", completed.ObjectiveState())
	}
}

// A completed task's timeline and proof survive the archive boundary.
func TestCompletionAndTimelineSurviveArchive(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceArtifact, Source: "write:parser.go", Summary: "changed parser.go",
		Outcome: EvidenceObserved, At: time.Unix(1000, 0).UTC(),
	}); err != nil {
		t.Fatalf("RecordEvidence(change): %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the suite passed",
		Outcome: EvidenceObserved, At: time.Unix(1001, 0).UTC(),
	}); err != nil {
		t.Fatalf("RecordEvidence(verification): %v", err)
	}
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	snapshot, err := store.LoadArchived(completed.ID)
	if err != nil || !snapshot.Found {
		t.Fatalf("LoadArchived: found=%v err=%v", snapshot.Found, err)
	}
	if snapshot.Task.Completion == nil || snapshot.Task.ObjectiveState() != ObjectiveProven {
		t.Fatalf("archived completion = %+v", snapshot.Task.Completion)
	}
	if len(snapshot.Task.Timeline) == 0 {
		t.Fatal("archived task lost its timeline")
	}
	foundCompleted := false
	for _, event := range snapshot.Task.Timeline {
		if event.Kind == TimelineCompleted && event.Status == string(ObjectiveProven) {
			foundCompleted = true
		}
	}
	if !foundCompleted {
		t.Fatalf("timeline = %+v, want a proven completion event", snapshot.Task.Timeline)
	}
}

// A persisted timeline with a damaged event is repaired at load, and the
// checkpoint remains readable.
func TestLoadRepairsTimelineAndCompletionProof(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("recover the timeline"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress: found=%v err=%v", found, err)
	}
	progress.Timeline = append(progress.Timeline, TimelineEvent{Seq: 1, Kind: TimelineKind("bogus")})
	progress.Completion = &CompletionProof{Verdict: "COMPLETED"}
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	for _, event := range restored.Timeline {
		if !event.Kind.Valid() {
			t.Fatalf("invalid event survived: %+v", event)
		}
	}
	if restored.Completion != nil {
		t.Fatalf("completion proof on a non-completed task survived: %+v", restored.Completion)
	}
	if !strings.Contains(restored.ContextWarning, "completion proof") {
		t.Fatalf("warning = %q, want the dropped proof diagnostic", restored.ContextWarning)
	}
}

func mutationIntent(objective string) contract.IntentContract {
	return contract.IntentContract{Objective: objective, Kind: contract.ObjectiveMutation}
}

func containsKind(kinds []TimelineKind, want TimelineKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// A verification that happened before the last change cannot prove the change,
// even when it is still current evidence. Both durable timestamp order and the
// runtime's own observation order are checked.
func TestCompletionRefusesVerificationBeforeTheLastChange(t *testing.T) {
	t.Run("durable order", func(t *testing.T) {
		controller := NewController(NewStore(t.TempDir()), nil)
		controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
		if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := controller.RecordEvidence(EvidenceInput{
			Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the suite passed",
			Outcome: EvidenceObserved, At: time.Unix(1001, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordEvidence(verification): %v", err)
		}
		if _, err := controller.RecordEvidence(EvidenceInput{
			Kind: EvidenceArtifact, Source: "write:parser.go", Summary: "changed parser.go",
			Outcome: EvidenceObserved, At: time.Unix(1002, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordEvidence(change): %v", err)
		}
		if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
			t.Fatal("a verification recorded before the change was accepted")
		} else if !strings.Contains(err.Error(), "after it") {
			t.Fatalf("error = %v, want the ordering failure", err)
		}
		if _, err := controller.RecordEvidence(EvidenceInput{
			Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the suite passed after the change",
			Outcome: EvidenceObserved, At: time.Unix(1003, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordEvidence(second verification): %v", err)
		}
		if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
			t.Fatalf("CompleteWithEvidence after the ordered check: %v", err)
		}
	})

	t.Run("runtime observation order wins over record time", func(t *testing.T) {
		controller := NewController(NewStore(t.TempDir()), nil)
		controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
		if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := controller.RecordEvidence(EvidenceInput{
			ID: "change", Kind: EvidenceArtifact, Artifact: "cognition:mutation:parser.go",
			Source: "write:parser.go", Summary: "changed parser.go",
			Outcome: EvidenceObserved, At: time.Unix(1000, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordEvidence(change): %v", err)
		}
		if _, err := controller.RecordEvidence(EvidenceInput{
			ID: "verify-old", Kind: EvidenceTest, Artifact: "cognition:verification:old",
			Source: "check:go test ./...", Summary: "an earlier check passed",
			Outcome: EvidenceObserved, At: time.Unix(1001, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordEvidence(verification): %v", err)
		}
		controller.evidence.Items = []cognition.EvidenceItem{
			{ID: "verification:old", Status: cognition.StatusKnown, Retention: cognition.RetentionDiagnostic,
				Summary: "an earlier check passed", Source: cognition.Source{Check: "test"}, Seq: 1},
			{ID: "mutation:parser.go", Status: cognition.StatusKnown, Retention: cognition.RetentionExact,
				Summary: "the parser changed", Source: cognition.Source{Tool: "write", Path: "parser.go"}, Seq: 2},
		}
		if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
			t.Fatal("a verification older than the mutation in observation order was accepted")
		} else if !strings.Contains(err.Error(), "after it") {
			t.Fatalf("error = %v, want the ordering failure", err)
		}
		// A newer verification passes even though its record timestamp is
		// earlier than the change: the runtime's observation order is the
		// stronger authority.
		if _, err := controller.RecordEvidence(EvidenceInput{
			ID: "verify-new", Kind: EvidenceTest, Artifact: "cognition:verification:new",
			Source: "check:go test ./...", Summary: "the suite passed after the change",
			Outcome: EvidenceObserved, At: time.Unix(999, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordEvidence(new verification): %v", err)
		}
		controller.evidence.Items = append(controller.evidence.Items, cognition.EvidenceItem{
			ID: "verification:new", Status: cognition.StatusKnown, Retention: cognition.RetentionDiagnostic,
			Summary: "the suite passed after the change", Source: cognition.Source{Check: "test"}, Seq: 3,
		})
		if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
			t.Fatalf("CompleteWithEvidence after the ordered check: %v", err)
		}
	})
}

// A shell mutation whose command text names no paths is still provable when
// the host's edit journal recorded the workspace diff and a check followed it.
func TestJournalMutationSupportsDurableCompletion(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	intent := mutationIntent("convert this project to Next.js")
	intent.Verification = []contract.VerificationRequirement{{Kind: contract.VerifyAny, Check: contract.CheckBuild}}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		ToolName: "bash",
		Result: agent.Result{
			Text:     "$ npm run build",
			Executes: true,
			Finished: true,
			Verifies: true,
			Verification: &agent.VerificationEvidence{
				Check:  contract.CheckBuild,
				Scopes: []agent.VerificationScope{{All: true}},
			},
		},
	}}}})
	controller.ObserveJournal(agent.MutationProvenance{
		Captured: true,
		Source:   "recovery.edit",
		Changes: []agent.MutationChange{
			{Path: "package.json", Status: "M"},
			{Path: "app/page.tsx", Status: "A"},
		},
	}, 0)
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
		t.Fatalf("journal mutation did not support completion: %v", err)
	}
	recorded, ok := controller.Current()
	if !ok {
		t.Fatal("the completed task was not readable")
	}
	if state := recorded.ObjectiveState(); state != ObjectiveProven {
		t.Fatalf("objective state = %s, want PROVEN", state)
	}
}
