package main

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// The Phase 3 acceptance scenario at the production startup boundary: one task
// identity survives ASK -> FUZZ -> BUILD -> FUZZ -> BUILD, a crash, a restore,
// and a proven completion. The timeline and lineage explain the result.
func TestPhase3AcceptanceScenarioKeepsIdentityTimelineAndProof(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	install := func() *task.Controller {
		return installTaskContinuity(tui.NewProgram(nil, "test", 0), root,
			[]agent.Tool{tools.NewTodoTool()}, func() workspace.Workspace { return role })
	}

	controller := install()
	intent := contract.IntentContract{Objective: "fix the flaky scheduler test", Kind: contract.ObjectiveMutation}
	started, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin as ASK: %v", err)
	}

	// ASK establishes a constraint and a fact from a read observation.
	if _, err := controller.UpdateRoleContext(workspace.Ask, task.RoleContextUpdate{
		Reasoning: []string{"the failure only appears under parallel execution"},
	}); err != nil {
		t.Fatalf("ASK context: %v", err)
	}
	source, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceSource, Source: "internal/scheduler/queue.go:42",
		Summary: "the queue drops the last job when the context is cancelled",
		Outcome: task.EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence source: %v", err)
	}
	fact, err := controller.RecordFact(task.FactInput{
		Text: "the queue drops the last job on cancellation", EvidenceIDs: []string{source.ID},
	})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}

	// FUZZ reproduces the failure and produces a reproduction artifact.
	role = workspace.Fuzz
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceReproduction, Source: "go test -run TestScheduler -count=20",
		Summary: "the reproduction failed 3 of 20 runs", Outcome: task.EvidenceObserved,
		Artifact: "cognition:reproduction-1", DedupKey: "reproduction-1",
	}); err != nil {
		t.Fatalf("RecordEvidence reproduction: %v", err)
	}

	// The widening handoff is proposed, approved, and applied.
	handoff, err := controller.ProposeHandoff(task.HandoffInput{
		To: workspace.Build, Reason: "a source change is required", Summary: "carry the reproduction",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	role = workspace.Build
	if _, err := controller.ApplyHandoff(handoff.ID); err != nil {
		t.Fatalf("ApplyHandoff: %v", err)
	}

	// BUILD records the change and the verification that followed it.
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceArtifact, Source: "internal/scheduler/queue.go",
		Summary: "the cancellation path now drains the queued job", Outcome: task.EvidenceObserved,
	}); err != nil {
		t.Fatalf("RecordEvidence change: %v", err)
	}
	verification, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceTest, Source: "check:go test ./internal/scheduler/...",
		Summary: "the scheduler suite passed after the change", Outcome: task.EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence verification: %v", err)
	}

	// A short return to FUZZ and a resume of BUILD preserve the identity.
	role = workspace.Fuzz
	if err := controller.RecordWorkspaceSwitch(workspace.Build, workspace.Fuzz, "main; clean"); err != nil {
		t.Fatalf("RecordWorkspaceSwitch FUZZ: %v", err)
	}
	role = workspace.Build
	if err := controller.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "main; clean"); err != nil {
		t.Fatalf("RecordWorkspaceSwitch BUILD: %v", err)
	}
	resumed, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin resume: %v", err)
	}
	if resumed.ID != started.ID {
		t.Fatalf("role transitions replaced the task: %q -> %q", started.ID, resumed.ID)
	}

	// Simulate a crash: a new process restores the same project state. The
	// BUILD switch was applied but not consumed, so the approval is the
	// continuation signal for the next turn.
	beforeCrash, _ := controller.Current()
	controller = install()
	restored, found, err := controller.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ID != started.ID {
		t.Fatalf("restart changed the task identity: %q -> %q", started.ID, restored.ID)
	}
	if restored.ObjectiveState() == task.ObjectiveProven {
		t.Fatal("an interrupted task was reported as proven")
	}
	if controller.Recovery().State != task.RecoveryIntact {
		t.Fatalf("recovery = %+v, want an intact checkpoint", controller.Recovery())
	}

	// The timeline explains what happened, including the role transitions and
	// the observations, without copying a payload.
	var kinds []task.TimelineKind
	for _, event := range restored.Timeline {
		kinds = append(kinds, event.Kind)
	}
	for _, want := range []task.TimelineKind{
		task.TimelineCreated, task.TimelineHandoff, task.TimelineEvidence,
		task.TimelineFact, task.TimelineVerification,
	} {
		if !timelineHas(kinds, want) {
			t.Fatalf("timeline = %v, want %q", kinds, want)
		}
	}

	// The conclusion traces back to the evidence that supports it.
	focus, related, ok := task.TraceLineage(restored, fact.ID)
	if !ok {
		t.Fatal("the fact has no lineage")
	}
	if focus.Stage != task.StageVerified && focus.Stage != task.StageContradicted {
		t.Fatalf("fact stage = %s, want a verified or disputed conclusion", focus.Stage)
	}
	foundSource := false
	for _, entry := range related {
		if entry.ID == source.ID {
			foundSource = true
		}
	}
	if !foundSource {
		t.Fatalf("lineage = %+v, want the source observation", related)
	}

	// The next turn resumes the paused task before it can complete; this is the
	// same Begin boundary the real turn entrypoint uses.
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin after restore: %v", err)
	}

	// Complete from the verification the run actually observed.
	completed, err := controller.CompleteWithEvidence(task.CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.ID != started.ID {
		t.Fatalf("completion changed the identity: %q -> %q", started.ID, completed.ID)
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN", completed.ObjectiveState())
	}
	if completed.Completion == nil || len(completed.Completion.EvidenceIDs) == 0 {
		t.Fatalf("proof = %+v, want the verification references", completed.Completion)
	}
	proofReferences := false
	for _, id := range completed.Completion.EvidenceIDs {
		if id == verification.ID {
			proofReferences = true
		}
	}
	if !proofReferences {
		t.Fatalf("proof does not reference the observed verification %s", verification.ID)
	}
	if len(beforeCrash.Timeline) == 0 {
		t.Fatal("the pre-crash task had no timeline")
	}

	// The final checkpoint is restartable and keeps the proven outcome.
	final := install()
	restored, found, err = final.Restore()
	if err != nil || !found {
		t.Fatalf("final Restore: found=%v err=%v", found, err)
	}
	if restored.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("persisted objective = %s, want PROVEN", restored.ObjectiveState())
	}
	if !strings.Contains(restored.CompletionSummary(), "proven") {
		t.Fatalf("summary = %q", restored.CompletionSummary())
	}
}

func timelineHas(kinds []task.TimelineKind, want task.TimelineKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
