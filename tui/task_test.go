package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/charmbracelet/x/ansi"
)

// The projection is the TUI's whole task surface: it must read as one compact
// block with a single status column, or the user cannot tell done from active.
func TestTaskProjectionRendersCompactTodoBlock(t *testing.T) {
	projection := &TaskProjection{
		ID:        "t1",
		Status:    "RUNNING",
		Objective: "ship phase 1",
		Items: []TaskItem{
			{State: taskStateDone, Label: "wrote the domain"},
			{State: taskStateActive, Label: "wiring the loop"},
			{State: taskStatePending, Label: "validate"},
		},
	}
	lines := taskLines(projection, DarkPalette(), Layout{ContentWidth: 80})
	joined := ansi.Strip(strings.Join(lines, "\n"))

	if !strings.Contains(joined, "ship phase 1") || !strings.Contains(joined, "running") {
		t.Fatalf("header is missing objective or status:\n%s", joined)
	}
	for _, want := range []string{"✔ wrote the domain", "◼ wiring the loop", "◻ validate"} {
		if !strings.Contains(joined, want) {
			t.Errorf("projection is missing %q:\n%s", want, joined)
		}
	}
	// One continuation column: the first todo hangs off ⎿, the rest align.
	if !strings.Contains(joined, "⎿ ✔") {
		t.Errorf("the first todo is not on the continuation column:\n%s", joined)
	}
}

// A projection updates the one task block in place, so a long turn cannot stack
// a fresh panel on every todo change.
func TestTaskProjectedReplacesInPlace(t *testing.T) {
	state := New()
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "first",
		Items: []TaskItem{{State: taskStatePending, Label: "a"}},
	}})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "PAUSED", Objective: "first",
		Items: []TaskItem{{State: taskStateDone, Label: "a"}},
	}})

	tasks := 0
	var block *TaskProjection
	for i := range state.Entries {
		if state.Entries[i].Kind == EntryTask {
			tasks++
			block = state.Entries[i].Task
		}
	}
	if tasks != 1 {
		t.Fatalf("task entries = %d, want 1 (the projection replaces in place)", tasks)
	}
	if block == nil || block.Status != "PAUSED" || block.Items[0].State != taskStateDone {
		t.Fatalf("stale projection survived: %+v", block)
	}
}

// An interruption must pause the task and must never report completion: the
// runtime observed a cancellation, not a finished objective.
func TestRunTurnPausesTaskOnInterruption(t *testing.T) {
	begun := make(chan struct{})
	var (
		mu        sync.Mutex
		paused    bool
		completed bool
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			close(begun)
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Pause: func(string) TaskProjection {
			mu.Lock()
			paused = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "PAUSED"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("do a long thing") }()

	select {
	case <-begun:
	case <-time.After(2 * time.Second):
		t.Fatal("the task was never begun")
	}
	p.cancelTurnNow("cancelled")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn did not return after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if !paused {
		t.Error("interruption did not pause the task")
	}
	if completed {
		t.Error("interruption was reported as completion")
	}
}

// A run whose final turn stopped calling tools completes the task.
func TestRunTurnCompletesTaskOnFinalTurn(t *testing.T) {
	var (
		mu        sync.Mutex
		completed bool
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "all done"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("finish it") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn hung")
	}

	mu.Lock()
	defer mu.Unlock()
	if !completed {
		t.Error("a run whose final turn stopped calling tools did not complete the task")
	}
}

// A continuation of a review task is judged against the persisted contract, not
// the continuation prompt. A clean informational answer cannot complete a
// mutation objective that still has no observed change.
func TestResumedReviewUsesPersistedContract(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "explained"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	persisted := contract.IntentContract{
		Objective: "fix note.txt",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "note.txt"}},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyAny,
		}},
	}
	var (
		mu        sync.Mutex
		begunWith contract.IntentContract
		completed bool
		reviewed  string
		began     bool
	)
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{
				ID:          "t1",
				Status:      "NEEDS_REVIEW",
				Objective:   persisted.Objective,
				Contract:    persisted,
				HasContract: true,
				Snapshot:    &agent.TargetSnapshot{Root: t.TempDir()},
			}, true
		},
		Begin: func(intent contract.IntentContract, _ *agent.TargetSnapshot) TaskProjection {
			mu.Lock()
			began = true
			begunWith = intent
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Complete: func() TaskProjection {
			mu.Lock()
			completed = true
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
		NeedsReview: func(reason string) TaskProjection {
			mu.Lock()
			reviewed = reason
			mu.Unlock()
			return TaskProjection{ID: "t1", Status: "NEEDS_REVIEW"}
		},
	})

	p.runTurn("continue")

	mu.Lock()
	defer mu.Unlock()
	if !began {
		t.Fatal("the continuation did not begin the task")
	}
	if begunWith.Objective != persisted.Objective || begunWith.Kind != persisted.Kind {
		t.Fatalf("continuation used %+v, want the persisted contract", begunWith)
	}
	if completed {
		t.Fatal("a mutation review task completed on an informational answer")
	}
	if reviewed == "" {
		t.Fatal("the unresolved task was not left in review")
	}
}

// A prompt with a different objective is refused before the model runs; it is
// not silently attached to the review task.
func TestResumedReviewRefusesDifferentObjective(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			t.Fatal("the model was called for a refused continuation")
			return nil, nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{
				ID: "t1", Status: "NEEDS_REVIEW", Objective: "fix note.txt", HasContract: true,
				Contract: contract.IntentContract{
					Objective: "fix note.txt",
					Kind:      contract.ObjectiveMutation,
					Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "note.txt"}},
				},
			}, true
		},
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			t.Fatal("a different objective was attached to the review task")
			return TaskProjection{}
		},
	})

	p.runTurn("what does this project do?")
}

// /task cancel is the explicit abandonment path: it never reports completion.
func TestTaskCommandCancelDoesNotComplete(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var completed, cancelled bool
	p.SetTaskHooks(TaskHooks{
		Current: func() (TaskResume, bool) {
			return TaskResume{ID: "t1", Status: "NEEDS_REVIEW", Objective: "fix note.txt"}, true
		},
		Cancel: func(string) TaskProjection {
			cancelled = true
			return TaskProjection{ID: "t1", Status: "CANCELLED"}
		},
		Complete: func() TaskProjection {
			completed = true
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
	})
	p.taskCommand([]string{"cancel"})
	if !cancelled {
		t.Fatal("/task cancel did not close the task")
	}
	if completed {
		t.Fatal("/task cancel claimed completion")
	}
}

// A task projection is display-only: it must never flip execution state. This
// is the negative half of "TUI and task.md are projections, not authorities".
func TestTaskProjectionDoesNotChangeExecutionState(t *testing.T) {
	state := New()
	state.Apply(UserSubmitted{Text: "go", Seq: 1})
	if !state.Status.Busy {
		t.Fatal("the turn did not start busy")
	}
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "PAUSED", Objective: "x",
		Items: []TaskItem{{State: taskStatePending, Label: "pending"}},
	}})
	if !state.Status.Busy {
		t.Error("a paused task projection stopped the running turn")
	}
	if state.Cancelled {
		t.Error("a task projection cancelled the turn")
	}
}

// Context reconstruction must reach the model: the task orientation is injected
// as system context on the turn, not kept as a UI decoration.
func TestRunTurnInjectsTaskContext(t *testing.T) {
	var (
		mu     sync.Mutex
		system []ai.SystemPromptBlock
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			mu.Lock()
			system = req.System
			mu.Unlock()
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Context: func() []string { return []string{"Objective: ship phase 1", "Next action: run tests"} },
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("continue") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn hung")
	}

	mu.Lock()
	defer mu.Unlock()
	var joined strings.Builder
	for _, block := range system {
		joined.WriteString(block.Text)
		joined.WriteString("\n")
	}
	got := joined.String()
	for _, want := range []string{"Task continuity", "Objective: ship phase 1", "Next action: run tests"} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt is missing %q:\n%s", want, got)
		}
	}
}
