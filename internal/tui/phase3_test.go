package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The read-only task views expose progression and lineage without creating a
// second store: every line points at the durable record that owns the fact.
func TestTaskTimelineAndLineageCommandsReadDurableState(t *testing.T) {
	root := t.TempDir()
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Fuzz })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "diagnose the crash", Kind: contract.ObjectiveInformational,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceCommand, Source: "go test ./...", Summary: "the test failed",
		Outcome: task.EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	fact, err := controller.RecordFact(task.FactInput{
		Text: "the scheduler drops the last job", EvidenceIDs: []string{evidence.ID},
	})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}

	program := NewProgram(scriptedLoop(t), "test", 0)
	program.SetTaskHooks(runtimeHooks(controller))

	program.taskCommand([]string{"timeline"})
	notice := lastNotice(program)
	for _, want := range []string{"task timeline", "evidence", "fact"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("timeline notice = %q, want %q", notice, want)
		}
	}
	if !strings.Contains(notice, evidence.ID) {
		t.Fatalf("timeline notice = %q, want the evidence reference", notice)
	}

	program.taskCommand([]string{"lineage", fact.ID})
	notice = lastNotice(program)
	for _, want := range []string{"fact#" + fact.ID, "VERIFIED", evidence.ID} {
		if !strings.Contains(notice, want) {
			t.Fatalf("lineage notice = %q, want %q", notice, want)
		}
	}

	program.taskCommand([]string{"lineage"})
	notice = lastNotice(program)
	if !strings.Contains(notice, "task lineage") || !strings.Contains(notice, "the scheduler drops the last job") {
		t.Fatalf("overview = %q, want the recorded conclusion", notice)
	}
}

// The status view distinguishes a proven objective from a bare lifecycle
// COMPLETED, and the transcript block shows the same truth.
func TestTaskStatusAndBlockReportProvenObjective(t *testing.T) {
	root := t.TempDir()
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "fix the parser", Kind: contract.ObjectiveMutation,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceArtifact, Source: "write:parser.go", Summary: "changed parser.go",
		Outcome: task.EvidenceObserved, At: time.Unix(1000, 0).UTC(),
	}); err != nil {
		t.Fatalf("RecordEvidence(change): %v", err)
	}
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceTest, Source: "check:go test ./...", Summary: "the parser suite passed",
		Outcome: task.EvidenceObserved, At: time.Unix(1001, 0).UTC(),
	}); err != nil {
		t.Fatalf("RecordEvidence(verification): %v", err)
	}
	completed, err := controller.CompleteWithEvidence(task.CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if !completed.Proven() {
		t.Fatalf("objective = %s, want PROVEN", completed.ObjectiveState())
	}

	program := NewProgram(scriptedLoop(t), "test", 0)
	program.SetTaskHooks(runtimeHooks(controller))
	program.taskCommand([]string{"status"})
	notice := lastNotice(program)
	if !strings.Contains(notice, "PROVEN") || !strings.Contains(notice, "parser suite passed") {
		t.Fatalf("status = %q, want the proven objective and observed check", notice)
	}

	projection := runtimeProjection(completed, nil)
	lines := strings.Join(taskLines(&projection, DarkPalette(), Layout{ContentWidth: 100}), "\n")
	if !strings.Contains(strings.ToLower(lines), "proven") {
		t.Fatalf("task block = %q, want the proven state", lines)
	}
}

// The turn boundary asks the task layer for an evidence-backed completion, not
// a bare status transition.
func TestTurnCompletionUsesEvidenceProof(t *testing.T) {
	root := t.TempDir()
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Ask })
	program := NewProgram(scriptedLoop(t, "the scheduler uses a work queue"), "test", 0)
	program.SetTaskHooks(runtimeHooks(controller))

	program.runTurn("explain the scheduler")
	drainProgram(program)
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the turn recorded no task")
	}
	if current.Status != task.StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", current.Status)
	}
	if current.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN", current.ObjectiveState())
	}
	if current.Completion == nil || !current.Completion.Answered {
		t.Fatalf("proof = %+v, want the informational answer evidence", current.Completion)
	}
}

// A refused completion is reviewable, not silently completed; outstanding work
// keeps the task active instead.
func TestRefusedCompletionNeverClaimsSuccess(t *testing.T) {
	t.Run("all work done becomes review", func(t *testing.T) {
		var reviewed string
		program := completionRefusalProgram(t, []TaskItem{{State: taskStateDone, Label: "write code"}}, func(reason string) {
			reviewed = reason
		})
		program.runTurn("explain the parser")
		drainProgram(program)
		if !strings.Contains(reviewed, "completion evidence") {
			t.Fatalf("review reason = %q, want the refused evidence", reviewed)
		}
	})

	t.Run("open work stays active", func(t *testing.T) {
		reviewed := false
		program := completionRefusalProgram(t, []TaskItem{{State: taskStateActive, Label: "write code"}}, func(string) {
			reviewed = true
		})
		program.runTurn("explain the parser")
		drainProgram(program)
		if reviewed {
			t.Fatal("outstanding work was closed as a review verdict")
		}
		entry := lastTaskEntry(program)
		if entry == nil || entry.Status != "RUNNING" {
			t.Fatalf("task entry = %+v, want RUNNING with the refusal", entry)
		}
	})

	t.Run("an uncommitted completion status is not published", func(t *testing.T) {
		program := NewProgram(scriptedLoop(t, "done"), "test", 0)
		program.SetTaskHooks(TaskHooks{
			Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
				return TaskProjection{ID: "t1", Status: "RUNNING", Objective: "explain the parser"}
			},
			CompleteWithEvidence: func(CompletionEvidence) TaskProjection {
				// A host hook that reports the mutation before its checkpoint
				// landed. The UI must never publish this as completed.
				return TaskProjection{ID: "t1", Status: "COMPLETED", Objective: "explain the parser",
					Err: "task: write checkpoint: disk full"}
			},
			NeedsReview: func(string) TaskProjection {
				return TaskProjection{ID: "t1", Status: "NEEDS_REVIEW", Objective: "explain the parser"}
			},
		})
		program.runTurn("explain the parser")
		drainProgram(program)
		entry := lastTaskEntry(program)
		if entry == nil {
			t.Fatal("the turn published no task entry")
		}
		if strings.EqualFold(entry.Status, "COMPLETED") {
			t.Fatalf("an uncommitted completion was published as %+v", entry)
		}
	})
}

func completionRefusalProgram(t *testing.T, items []TaskItem, onReview func(string)) *Program {
	t.Helper()
	program := NewProgram(scriptedLoop(t, "done"), "test", 0)
	program.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING", Objective: "fix the parser", Items: items}
		},
		CompleteWithEvidence: func(CompletionEvidence) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING", Objective: "fix the parser", Items: items,
				Err: "task: no current verification followed the change"}
		},
		NeedsReview: func(reason string) TaskProjection {
			onReview(reason)
			return TaskProjection{ID: "t1", Status: "NEEDS_REVIEW", Objective: "fix the parser", Items: items}
		},
	})
	return program
}

// A recovery classification reaches the transcript block instead of being
// hidden behind a generic task status.
func TestTaskBlockRendersRecoveryClassification(t *testing.T) {
	projection := TaskProjection{
		ID: "t1", Status: "PAUSED", Objective: "recover the parser",
		Recovery: "RECONSTRUCTED: recovered from the checkpoint; no projection was present",
		Warning:  "task context limit reached: newer facts were not added",
	}
	lines := strings.Join(taskLines(&projection, DarkPalette(), Layout{ContentWidth: 100}), "\n")
	for _, want := range []string{"recovery: RECONSTRUCTED", "context limit reached"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("block = %q, want %q", lines, want)
		}
	}
}

func drainProgram(program *Program) {
	for {
		select {
		case event := <-program.events:
			program.state.Apply(event)
		default:
			return
		}
	}
}

func lastTaskEntry(program *Program) *TaskProjection {
	for i := len(program.state.Entries) - 1; i >= 0; i-- {
		if program.state.Entries[i].Kind == EntryTask && program.state.Entries[i].Task != nil {
			return program.state.Entries[i].Task
		}
	}
	return nil
}
