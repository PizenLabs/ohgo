package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/tui"
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

// The session todo tool and the task controller share one boundary: a new
// objective clears the in-process plan and does not import it, while the
// detached task keeps its own archived snapshot.
func TestNewTaskBoundaryResetsTheSessionTodoTool(t *testing.T) {
	tool := tools.NewTodoTool()
	ctx := agent.Context{Ctx: context.Background()}
	if result := tool.Execute(ctx, map[string]any{
		"op": "init", "phase": "design", "tasks": []any{"analyze", "layout"},
	}); result.IsError {
		t.Fatalf("init: %s", result.Text)
	}
	controller := task.NewController(task.NewStore(t.TempDir()), todoSource([]agent.Tool{tool}))
	controller.SetTodoResetter(todoResetter([]agent.Tool{tool}))
	first, err := controller.Begin(contract.IntentContract{
		Objective: "design the page",
		Kind:      contract.ObjectiveInformational,
	}, nil)
	if err != nil {
		t.Fatalf("Begin design: %v", err)
	}
	synced, err := controller.Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(synced.Todos) != 2 {
		t.Fatalf("design task todos = %+v, want the two plan items", synced.Todos)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	next, err := controller.Begin(contract.IntentContract{
		Objective: "redesign it",
		Kind:      contract.ObjectiveMutation,
	}, nil)
	if err != nil {
		t.Fatalf("Begin redesign: %v", err)
	}
	if next.ID == first.ID {
		t.Fatal("the redesign reused the detached design task")
	}
	if len(next.Todos) != 0 {
		t.Fatalf("redesign task imported the design plan: %+v", next.Todos)
	}
	if list := tool.Snapshot(); len(list.Phases) != 0 {
		t.Fatalf("session plan survived the new-task boundary: %+v", list)
	}
}

// The projection converts the domain vocabulary into the TUI's, dropping
// nothing the renderer needs.
func TestTaskProjectionConvertsDomain(t *testing.T) {
	projection := taskProjection(task.Task{
		ID:           "x",
		Status:       task.StatusPaused,
		Objective:    "objective",
		ReviewReason: "the change was not verified",
		Todos: []task.Todo{
			{ID: "1", Label: "done item", Status: task.TodoDone},
			{ID: "2", Label: "active item", Status: task.TodoActive},
		},
	}, nil)

	if projection.ID != "x" || projection.Status != "PAUSED" || projection.Objective != "objective" {
		t.Fatalf("projection header = %+v", projection)
	}
	if projection.ReviewReason != "the change was not verified" {
		t.Fatalf("review reason = %q, want the durable reason", projection.ReviewReason)
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
	controller := installTaskContinuity(program, root, []agent.Tool{tools.NewTodoTool()}, nil)

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

	recovered := installTaskContinuity(tui.NewProgram(nil, "test", 0), root, []agent.Tool{tools.NewTodoTool()}, nil)
	taskState, found, err := recovered.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found || taskState.Status != task.StatusPaused {
		t.Fatalf("recovered = %+v (found=%v), want PAUSED", taskState, found)
	}
}

// The turn-boundary drain is the production path that makes a FUZZ finding
// consumable by the next BUILD turn. The artifact tool must not write task
// state from its own goroutine; it buffers and the boundary projects once.
func TestArtifactBufferProjectsFindingIntoTurnTaskContext(t *testing.T) {
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	artifact := tools.NewArtifactTool(func(workspace.Artifact) error { return nil },
		func() workspace.Workspace { return workspace.Fuzz })
	buffer := attachArtifactSink([]agent.Tool{artifact}, controller)
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "investigate the checkout flow",
		Kind:      contract.ObjectiveInformational,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	result := artifact.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"kind": "finding", "target": "order/checkout", "status": "reproduced",
		"reproduction": "go test ./order/...", "evidence": "FAIL TestCheckout",
	})
	if result.IsError {
		t.Fatalf("artifact recording failed: %s", result.Text)
	}
	if err := buffer.drain(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the task disappeared")
	}
	if len(current.Context.Hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want the projected finding", len(current.Context.Hypotheses))
	}
	briefing := strings.Join(controller.Context(), "\n")
	for _, want := range []string{"order/checkout", "go test ./order/..."} {
		if !strings.Contains(briefing, want) {
			t.Fatalf("BUILD briefing missing %q:\n%s", want, briefing)
		}
	}
	if err := buffer.drain(); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	current, _ = controller.Current()
	if len(current.Context.Hypotheses) != 1 {
		t.Fatalf("a second drain duplicated the finding: %d hypotheses", len(current.Context.Hypotheses))
	}
}

// The resume projection carries the role the task last ran under, so the turn
// gate can distinguish a role change from a new objective. It is metadata for
// continuity, never authority.
func TestTaskResumeCarriesLastRole(t *testing.T) {
	recorded := task.Task{
		ID:        "task-1",
		Status:    task.StatusPaused,
		Objective: "fix the flaky test",
		Contract:  &contract.IntentContract{Objective: "fix the flaky test"},
		Context: task.SharedContext{Roles: []task.RoleContext{
			{Role: workspace.Ask, UpdatedAt: time.Now().Add(-time.Minute)},
			{Role: workspace.Fuzz, UpdatedAt: time.Now()},
		}},
	}
	resume := taskResume(recorded)
	if resume.Role != string(workspace.Fuzz) {
		t.Fatalf("resume role = %q, want %q", resume.Role, workspace.Fuzz)
	}
	if !resume.HasContract || resume.Contract.Objective != recorded.Objective {
		t.Fatalf("resume lost the completion contract: %+v", resume)
	}
	if resume.ID != recorded.ID || resume.Status != string(recorded.Status) {
		t.Fatalf("resume identity = %+v", resume)
	}
}

// The resume projection carries an explicit switch so the next turn stays
// attached to the same task. It remains continuity metadata, never authority.
func TestTaskResumeCarriesAnExplicitSwitch(t *testing.T) {
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "fix the flaky test", Kind: contract.ObjectiveInformational,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// A host records only after it has moved the live authority.
	role = workspace.Build
	if err := controller.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "main; clean"); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}
	current, _ := controller.Current()
	resume := taskResume(current)
	if resume.HandoffStatus != string(task.HandoffApplied) {
		t.Fatalf("handoff status = %q, want APPLIED", resume.HandoffStatus)
	}
	if resume.HandoffTo != "build" {
		t.Fatalf("handoff target = %q, want build", resume.HandoffTo)
	}
	if resume.HandoffConsumed {
		t.Fatal("a switch awaiting its next turn was reported consumed")
	}
}
