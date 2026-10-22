package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/task"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/tui"
)

// The todo tool stays the single mutation owner; the snapshot is a faithful
// projection, and a completed item records the model's claim rather than
// pretending it is independent verification.
func TestTodoSnapshotProjectsStatusAndClaim(t *testing.T) {
	tool := tools.NewTodoTool()
	ctx := agent.Context{Ctx: context.Background()}
	for _, args := range []map[string]any{
		{"op": "init", "phase": "work", "tasks": []any{"a", "b"}},
		{"op": "start", "phase": "work", "task": "a"},
		{"op": "done", "phase": "work", "task": "a"},
	} {
		if result := tool.Execute(ctx, args); result.IsError {
			t.Fatalf("todo %v: %s", args["op"], result.Text)
		}
	}

	snapshot := todoSnapshot(tool)
	if len(snapshot) != 2 {
		t.Fatalf("snapshot = %d items, want 2", len(snapshot))
	}
	if snapshot[0].Label != "a" || snapshot[0].Status != task.TodoDone {
		t.Fatalf("first = %+v, want a/DONE", snapshot[0])
	}
	if snapshot[0].Evidence != "model claim (completed)" {
		t.Errorf("evidence = %q, want the model's claim tagged as such", snapshot[0].Evidence)
	}
	if snapshot[1].Status != task.TodoActive {
		t.Errorf("second = %s, want ACTIVE (the next pending is promoted)", snapshot[1].Status)
	}
}

// The projection converts the domain vocabulary into the TUI's, dropping
// nothing the renderer needs.
func TestTaskProjectionConvertsDomain(t *testing.T) {
	projection := taskProjection(task.Task{
		ID:        "x",
		Status:    task.StatusPaused,
		Objective: "objective",
		Todos: []task.Todo{
			{ID: "1", Label: "done item", Status: task.TodoDone},
			{ID: "2", Label: "active item", Status: task.TodoActive},
		},
	}, nil)

	if projection.ID != "x" || projection.Status != "PAUSED" || projection.Objective != "objective" {
		t.Fatalf("projection header = %+v", projection)
	}
	if len(projection.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(projection.Items))
	}
	if projection.Items[0].State != "done" || projection.Items[1].State != "active" {
		t.Fatalf("item states = %+v", projection.Items)
	}
	if projection.Err != "" {
		t.Errorf("unexpected error projection: %q", projection.Err)
	}
}

// Wiring the layer into a program must make a turn's task durable: the project
// artifacts appear on disk and a fresh controller recovers them.
func TestInstallTaskContinuityWritesAndRecoversArtifacts(t *testing.T) {
	root := t.TempDir()
	program := tui.NewProgram(nil, "test", 0)
	controller := installTaskContinuity(program, root, []agent.Tool{tools.NewTodoTool()})

	if _, err := controller.Begin(contract.IntentContract{Objective: "ship phase 1", Kind: contract.ObjectiveInformational}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted by user"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	for _, name := range []string{"task.md", "progress.json"} {
		if _, err := os.Stat(filepath.Join(root, ".ohgo", name)); err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
	}

	recovered := installTaskContinuity(tui.NewProgram(nil, "test", 0), root, []agent.Tool{tools.NewTodoTool()})
	taskState, found, err := recovered.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found || taskState.Status != task.StatusPaused {
		t.Fatalf("recovered = %+v (found=%v), want PAUSED", taskState, found)
	}
}
