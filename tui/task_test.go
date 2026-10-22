package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
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
		Begin: func(string) TaskProjection {
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
		Begin: func(string) TaskProjection { return TaskProjection{ID: "t1", Status: "RUNNING"} },
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
		Begin:   func(string) TaskProjection { return TaskProjection{ID: "t1", Status: "RUNNING"} },
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
