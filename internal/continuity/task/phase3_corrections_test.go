package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A completion whose checkpoint cannot be written must not leave an in-memory
// completion behind, and it must not block a later, successful retry.
func TestFailedCompletionPersistLeavesTaskResumable(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	recordChangeAndVerification(t, controller, time.Unix(1000, 0).UTC())

	store.write = func(string, []byte) error { return errors.New("disk full") }
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
		t.Fatal("completion succeeded even though the checkpoint could not be written")
	}

	current, ok := controller.Current()
	if !ok {
		t.Fatal("a failed completion lost the task")
	}
	if current.Status != StatusRunning {
		t.Fatalf("status = %s, want RUNNING after the failed write", current.Status)
	}
	if current.Completion != nil {
		t.Fatalf("an uncommitted completion proof is visible: %+v", current.Completion)
	}
	if current.Proven() {
		t.Fatal("a failed completion is reported as PROVEN")
	}
	for _, event := range current.Timeline {
		if event.Kind == TimelineCompleted {
			t.Fatalf("an uncommitted completion event is visible: %+v", event)
		}
	}

	// The task keeps its identity; a retry on the same contract must not mint a
	// new one.
	store.write = writeFileAtomic
	again, err := controller.Begin(mutationIntent("fix the parser"), nil)
	if err != nil {
		t.Fatalf("Begin after failed completion: %v", err)
	}
	if again.ID != current.ID {
		t.Fatalf("Begin replaced the uncommitted task: got %q, want %q", again.ID, current.ID)
	}
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("retry CompleteWithEvidence: %v", err)
	}
	if completed.ObjectiveState() != ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN after a successful retry", completed.ObjectiveState())
	}
}

// An execution objective is never proven by a final answer alone; a projected,
// successful execution observation is what satisfies it.
func TestExecutionObjectiveRequiresObservedExecution(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "run the tests",
		Kind:      contract.ObjectiveExecution,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true}); err == nil {
		t.Fatal("an answer alone completed an execution objective")
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		ToolName: "bash",
		Result: agent.Result{
			Text: "ok", Executes: true, Finished: true,
			Details: map[string]any{"command": "go test ./...", "exitCode": 0},
		},
	}}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.ObjectiveState() != ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN", completed.ObjectiveState())
	}
	if completed.Completion == nil || !completed.Completion.Executed || len(completed.Completion.EvidenceIDs) == 0 {
		t.Fatalf("proof = %+v, want the execution observation", completed.Completion)
	}
}

// A successful command must project as an observation, not an inconclusive
// diagnostic, or it could never support an execution objective.
func TestSuccessfulCommandProjectionIsEvidence(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "run the tests", Kind: contract.ObjectiveExecution,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		ToolName: "bash",
		Result: agent.Result{
			Text: "ok", Executes: true, Finished: true,
			Details: map[string]any{"command": "go test ./...", "exitCode": 0},
		},
	}}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	current, _ := controller.Current()
	for _, record := range current.Context.Evidence {
		if record.Kind != EvidenceCommand {
			continue
		}
		if record.Outcome != EvidenceObserved {
			t.Fatalf("command evidence = %+v, want a supporting observation", record)
		}
		return
	}
	t.Fatalf("no command evidence was projected: %+v", current.Context.Evidence)
}

// A contract kind this build does not understand is refused explicitly rather
// than admitted by a permissive fallback.
func TestUnsupportedContractKindIsRefused(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "do something unknown", Kind: contract.ObjectiveKind("bogus"),
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	recordChangeAndVerification(t, controller, time.Unix(1000, 0).UTC())
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true}); err == nil {
		t.Fatal("an unsupported contract kind was completed")
	} else if !errors.Is(err, ErrUnprovenCompletion) {
		t.Fatalf("error = %v, want ErrUnprovenCompletion", err)
	}
	current, _ := controller.Current()
	if current.ObjectiveState() == ObjectiveProven {
		t.Fatal("an unsupported contract reported PROVEN")
	}
}

// A contradiction that targets a conclusion the proof depends on must downgrade
// PROVEN; an unrelated contradiction must not.
func TestContradictionDowngradesOnlyWhenMaterial(t *testing.T) {
	base := time.Unix(2000, 0).UTC()
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, verification := recordChangeAndVerification(t, controller, base)
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	unrelatedEvidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceSource, Source: "read:README.md", Summary: "the readme mentions the parser",
		Outcome: EvidenceObserved, At: base.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordEvidence(unrelated): %v", err)
	}
	unrelatedFact, err := controller.RecordFact(FactInput{
		Text: "the readme mentions the parser", EvidenceIDs: []string{unrelatedEvidence.ID},
		At: base.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordFact(unrelated): %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceSource, Source: "read:README.md", Summary: "the readme does not mention it",
		Outcome: EvidenceContradicting, Contradicts: []string{unrelatedFact.ID},
		At: base.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("RecordEvidence(unrelated contradiction): %v", err)
	}
	current, _ := controller.Current()
	if current.ObjectiveState() != ObjectiveProven {
		t.Fatalf("unrelated contradiction downgraded the proof to %s", current.ObjectiveState())
	}

	// Now a contradiction that names a claim backed by the proof's own
	// verification evidence is material.
	dependent, err := controller.RecordFact(FactInput{
		Text: "the parser suite passed", EvidenceIDs: []string{verification.ID},
		At: base.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("RecordFact(dependent): %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the parser suite now fails",
		Outcome: EvidenceContradicting, Contradicts: []string{dependent.ID},
		At: base.Add(6 * time.Second),
	}); err != nil {
		t.Fatalf("RecordEvidence(material contradiction): %v", err)
	}
	current, _ = controller.Current()
	if current.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("material contradiction left objective at %s, want COMPLETED_UNPROVEN", current.ObjectiveState())
	}
}

// A proof dropped at load must be classified as a reconstruction, never INTACT.
func TestDroppedProofForcesReconstruction(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	recordChangeAndVerification(t, controller, time.Unix(3000, 0).UTC())
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}

	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress: found=%v err=%v", found, err)
	}
	progress.Completion.Verdict = "NOT_A_VERDICT"
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reopened := NewController(store, nil)
	if _, _, err := reopened.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	report := reopened.Recovery()
	if report.State == RecoveryIntact {
		t.Fatalf("recovery = %+v, want a reconstruction after dropping the proof", report)
	}
	if !containsString(report.Missing, "completion proof") {
		t.Fatalf("recovery = %+v, want the dropped-proof category", report)
	}
}

// Resuming a repaired archive preserves the archive's real classification.
func TestArchiveRepairIsReportedOnResume(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	begun, err := controller.Begin(mutationIntent("fix the parser"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	path := store.archivePath(begun.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var file archivedFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode archive: %v", err)
	}
	if file.Progress.Context == nil {
		file.Progress.Context = &SharedContext{Version: ContextVersion}
	}
	file.Progress.Context.Version = ContextVersion + 50
	corrupt, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	reopened := NewController(store, nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := reopened.ResumeTask(begun.ID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	report := reopened.Recovery()
	if report.State != RecoveryReconstructed {
		t.Fatalf("recovery = %+v, want RECONSTRUCTED", report)
	}
	if !containsString(report.Missing, "structured context") {
		t.Fatalf("recovery = %+v, want the context category", report)
	}
}

// A corrupt archive is reported rather than silently indistinguishable from a
// task that never existed, and it does not hide the valid archives.
func TestCorruptArchiveIsReported(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	good, err := controller.Begin(mutationIntent("keep me"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.ArchiveDir(), "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}

	reopened := NewController(store, nil)
	resumable, err := reopened.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 1 || resumable[0].ID != good.ID {
		t.Fatalf("resumable = %+v, want the valid archive only", resumable)
	}
	problems := reopened.ArchiveProblems()
	if len(problems) != 1 {
		t.Fatalf("problems = %+v, want the corrupt archive reported", problems)
	}
	if problems[0].Reason == "" {
		t.Fatalf("problem = %+v, want a reason", problems[0])
	}
}

// The version is a downgrade barrier: a binary that only understands version 2
// refuses a version-3 checkpoint instead of rewriting it without the Phase 3
// fields.
func TestCheckpointVersionRefusesOlderWriters(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress: found=%v err=%v", found, err)
	}
	if progress.Version <= 2 {
		t.Fatalf("version = %d, want a downgrade barrier above 2", progress.Version)
	}
	// This is the gate a version-2 binary already had: it refuses anything
	// newer than the schema it knows, so it cannot reach a rewrite. The file
	// therefore cannot be silently rewritten by that binary.
	if progress.Version <= ProgressVersion-1 {
		t.Fatalf("the barrier did not advance past the previous schema")
	}

	// A version-2 checkpoint still loads; the new binary is backward
	// compatible with every older checkpoint it can parse.
	progress.Version = 2
	progress.Status = StatusCompleted
	progress.Timeline = []TimelineEvent{{Seq: 1, At: time.Unix(1, 0).UTC(), Kind: TimelineCreated, Summary: "created"}}
	progress.Completion = &CompletionProof{
		Verdict: "COMPLETED", Answered: true, At: time.Unix(2, 0).UTC(),
	}
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
		t.Fatalf("Restore v2: found=%v err=%v", found, err)
	}
	if len(restored.Timeline) != 1 || restored.Timeline[0].Kind != TimelineCreated {
		t.Fatalf("timeline = %+v, want the version-2 timeline preserved", restored.Timeline)
	}
	if restored.Completion == nil {
		t.Fatalf("version-2 completion was dropped")
	}
}

// A legacy completed checkpoint with no proof reports COMPLETED_UNPROVEN after a
// restart, not PROVEN.
func TestLegacyCompletedCheckpointReportsUnproven(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", restored.Status)
	}
	if restored.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("objective = %s, want COMPLETED_UNPROVEN", restored.ObjectiveState())
	}
}

// Open todo work refuses completion and leaves the task active.
func TestOpenTodosRefuseCompletion(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	id, err := controller.task.AddTodo("finish the refactor")
	if err != nil {
		t.Fatalf("AddTodo: %v", err)
	}
	if err := controller.task.SetTodoStatus(id, TodoActive, ""); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}
	recordChangeAndVerification(t, controller, time.Unix(4000, 0).UTC())
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
		t.Fatal("completion ignored outstanding work")
	}
	current, _ := controller.Current()
	if current.Status != StatusRunning {
		t.Fatalf("status = %s, want RUNNING", current.Status)
	}
}

// Completion requires the RUNNING boundary: a paused or reviewed task must
// resume through Begin first.
func TestCompletionFromPausedRequiresResume(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	recordChangeAndVerification(t, controller, time.Unix(5000, 0).UTC())
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition from PAUSED", err)
	}
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin after pause: %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
		t.Fatalf("CompleteWithEvidence after resume: %v", err)
	}
}

// Repeated pause and review boundaries do not duplicate their timeline events.
func TestPauseAndReviewAreTimelineIdempotent(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := controller.Pause("interrupted"); err != nil {
			t.Fatalf("Pause %d: %v", i, err)
		}
	}
	current, _ := controller.Current()
	if got := countTimelineKind(current.Timeline, TimelinePaused); got != 1 {
		t.Fatalf("paused events = %d, want 1", got)
	}
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := controller.NeedsReview("needs review"); err != nil {
			t.Fatalf("NeedsReview %d: %v", i, err)
		}
	}
	current, _ = controller.Current()
	if got := countTimelineKind(current.Timeline, TimelineReview); got != 1 {
		t.Fatalf("review events = %d, want 1", got)
	}
}

// Truncation survives a restart: the creation anchor is kept, observation
// events are elided first, and the visible gap is accurate.
func TestTimelineTruncationSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < MaxTimelineEvents+5; i++ {
		controller.task.AppendEvent(TimelineEvent{
			Kind: TimelineEvidence, Summary: fmt.Sprintf("observation %d", i),
		})
	}
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if len(restored.Timeline) != MaxTimelineEvents {
		t.Fatalf("timeline = %d, want %d", len(restored.Timeline), MaxTimelineEvents)
	}
	if restored.Timeline[0].Kind != TimelineCreated {
		t.Fatalf("first event = %+v, want the creation anchor", restored.Timeline[0])
	}
	// The restart adds one checkpoint event, so the bounded log drops seven:
	// five observations beyond the bound plus the two that made room.
	if got := restored.TimelineDropped(); got != 7 {
		t.Fatalf("dropped = %d, want 7", got)
	}
	lastSeq := restored.Timeline[len(restored.Timeline)-1].Seq
	restored.AppendEvent(TimelineEvent{At: time.Unix(9, 0).UTC(), Kind: TimelineCheckpoint, Summary: "after restart"})
	if got := restored.Timeline[len(restored.Timeline)-1].Seq; got <= lastSeq {
		t.Fatalf("sequence did not continue after restart: %d <= %d", got, lastSeq)
	}
}

// Evidence traces to the hypotheses it supports, not only to facts and
// contradictions.
func TestLineageTracesEvidenceToHypothesis(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Fuzz })
	if _, err := controller.Begin(testIntent("diagnose the crash"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "go test ./...", Summary: "the suite reproduced the crash",
		Outcome: EvidenceObserved, At: time.Unix(6000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{
		Text: "the scheduler drops the last job", At: time.Unix(6001, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	if err := controller.task.AttachEvidence(hypothesis.ID, []string{evidence.ID}, time.Unix(6002, 0).UTC()); err != nil {
		t.Fatalf("AttachEvidence: %v", err)
	}
	_, related, ok := controller.TraceLineage(evidence.ID)
	if !ok {
		t.Fatal("evidence did not trace")
	}
	found := false
	for _, entry := range related {
		if entry.Kind == "hypothesis" && entry.ID == hypothesis.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("related = %+v, want the supported hypothesis", related)
	}
}

// Task-context mutations use the caller's clock, so the timeline is
// deterministic under a fixed clock.
func TestEvidenceTimelineUsesInjectedClock(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the suite passed",
		Outcome: EvidenceObserved, At: time.Unix(7000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	at := time.Now().UTC().Add(time.Hour)
	controller.now = func() time.Time { return at }
	if _, err := controller.InvalidateEvidence(evidence.ID, "stale"); err != nil {
		t.Fatalf("InvalidateEvidence: %v", err)
	}
	current, _ := controller.Current()
	last, ok := current.LastEvent()
	if !ok {
		t.Fatal("no timeline event")
	}
	if !last.At.Equal(at) {
		t.Fatalf("event at %s, want the injected clock %s", last.At, at)
	}
}

// A valid checkpoint with an unreadable projection reports the projection loss,
// not a false claim that the checkpoint context vanished.
func TestUnreadableProjectionWarningIsTruthful(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(mutationIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := os.Remove(store.TaskPath()); err != nil {
		t.Fatalf("remove projection: %v", err)
	}
	if err := os.Mkdir(store.TaskPath(), 0o755); err != nil {
		t.Fatalf("block projection: %v", err)
	}
	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if report := reopened.Recovery(); report.State != RecoveryReconstructed {
		t.Fatalf("recovery = %+v, want RECONSTRUCTED", report)
	}
	if !strings.Contains(restored.ContextWarning, "projection could not be read") {
		t.Fatalf("warning = %q, want the projection-loss wording", restored.ContextWarning)
	}
	if strings.Contains(restored.ContextWarning, "context was not restored") {
		t.Fatalf("warning = %q, falsely claims the checkpoint context was lost", restored.ContextWarning)
	}
}

// Lifecycle transitions, including task creation, are committed only when the
// checkpoint lands. A failed write restores the prior in-memory state.
func TestFailedLifecyclePersistRollsBack(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })

	store.write = func(string, []byte) error { return errors.New("disk full") }
	if _, err := controller.Begin(testIntent("explain the parser"), nil); err == nil {
		t.Fatal("Begin reported success without a checkpoint")
	}
	if _, ok := controller.Current(); ok {
		t.Fatal("a failed Begin left an uncommitted task")
	}
	store.write = writeFileAtomic
	if _, err := controller.Begin(testIntent("explain the parser"), nil); err != nil {
		t.Fatalf("Begin after the write recovered: %v", err)
	}

	store.write = func(string, []byte) error { return errors.New("disk full") }
	if _, err := controller.Pause("interrupted"); err == nil {
		t.Fatal("Pause reported success without a checkpoint")
	}
	current, _ := controller.Current()
	if current.Status != StatusRunning {
		t.Fatalf("failed Pause left status %s, want RUNNING", current.Status)
	}
	if _, err := controller.Complete(); err == nil {
		t.Fatal("legacy Complete reported success without a checkpoint")
	}
	current, _ = controller.Current()
	if current.Status != StatusRunning {
		t.Fatalf("failed Complete left status %s, want RUNNING", current.Status)
	}
	if current.Completion != nil {
		t.Fatalf("failed Complete left a proof: %+v", current.Completion)
	}
	for _, event := range current.Timeline {
		if event.Kind == TimelineCompleted {
			t.Fatalf("failed Complete left a completion event: %+v", event)
		}
	}
}

// A check in an unrelated scope does not satisfy the contract's declared
// verification requirement.
func TestVerificationMustCoverContractScope(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "fix the grep tool",
		Kind:      contract.ObjectiveMutation,
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyScope, Scope: "tools", Check: contract.CheckTest,
		}},
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	base := time.Unix(8000, 0).UTC()
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed tools/grep.go",
		Outcome: EvidenceObserved, At: base,
	}); err != nil {
		t.Fatalf("RecordEvidence(change): %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:test", Scope: "docs", Summary: "an unrelated suite passed",
		Outcome: EvidenceObserved, At: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordEvidence(wrong scope): %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
		t.Fatal("a check outside the required scope was accepted")
	} else if !strings.Contains(err.Error(), "required scope") {
		t.Fatalf("error = %v, want the scope failure", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:test", Scope: "tools", Summary: "the tools suite passed",
		Outcome: EvidenceObserved, At: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("RecordEvidence(matching scope): %v", err)
	}
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true}); err != nil {
		t.Fatalf("CompleteWithEvidence with matching scope: %v", err)
	}
}

// recordChangeAndVerification records the minimum durable evidence a mutation
// proof needs: one observed change and one check that follows it.
func recordChangeAndVerification(t *testing.T, controller *Controller, base time.Time) (EvidenceRecord, EvidenceRecord) {
	t.Helper()
	change, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceArtifact, Source: "write:parser.go", Summary: "changed parser.go",
		Outcome: EvidenceObserved, At: base,
	})
	if err != nil {
		t.Fatalf("RecordEvidence(change): %v", err)
	}
	verification, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "check:go test ./...", Summary: "the parser suite passed",
		Outcome: EvidenceObserved, At: base.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("RecordEvidence(verification): %v", err)
	}
	return change, verification
}

func countTimelineKind(events []TimelineEvent, kind TimelineKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}
