package tools

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

// runTodo executes an op against a fresh tool and returns the rendered result.
func runTodo(t *testing.T, args map[string]any) agent.Result {
	t.Helper()
	tool := NewTodoTool()
	return tool.Execute(testContext(), args)
}

func TestTodoInitCreatesASingleInProgressTask(t *testing.T) {
	// The invariant is what makes a UI able to show "currently working on X"
	// without tracking it separately.
	result := runTodo(t, map[string]any{
		"op":    "init",
		"phase": "build",
		"tasks": []any{"first", "second", "third"},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "Todo: 0/3 done") {
		t.Errorf("summary missing or wrong: %s", result.Text)
	}
	inProgress := strings.Count(result.Text, "[>]")
	if inProgress != 1 {
		t.Errorf("in-progress markers = %d, want exactly 1", inProgress)
	}
	if !strings.Contains(result.Text, "[>] first") {
		t.Errorf("the first task was not promoted: %s", result.Text)
	}
}

func TestTodoInitRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing phase", map[string]any{"op": "init", "tasks": []any{"a"}}},
		{"missing tasks", map[string]any{"op": "init", "phase": "p"}},
		{"empty task list", map[string]any{"op": "init", "phase": "p", "tasks": []any{}}},
		{"duplicate tasks", map[string]any{"op": "init", "phase": "p", "tasks": []any{"a", "a"}}},
		{"blank task", map[string]any{"op": "init", "phase": "p", "tasks": []any{"a", "  "}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if result := runTodo(t, tc.args); !result.IsError {
				t.Errorf("expected an error, got: %s", result.Text)
			}
		})
	}
}

func TestTodoDonePromotesTheNextTask(t *testing.T) {
	// Finishing a task must move the cursor, or the list stalls with nothing in
	// progress while work remains.
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a", "b"},
	})

	result := tool.Execute(testContext(), map[string]any{"op": "done", "phase": "p", "task": "a"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "Todo: 1/2 done") {
		t.Errorf("summary = %s, want 1/2 done", result.Text)
	}
	if strings.Count(result.Text, "[>]") != 1 {
		t.Errorf("in-progress markers = %d, want 1", strings.Count(result.Text, "[>]"))
	}
	if !strings.Contains(result.Text, "[>] b") {
		t.Errorf("the next task was not promoted: %s", result.Text)
	}
}

func TestTodoStartDemotesThePreviousTask(t *testing.T) {
	// Two tasks in progress would read as "both are being done now".
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a", "b"},
	})

	result := tool.Execute(testContext(), map[string]any{"op": "start", "phase": "p", "task": "b"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if got := strings.Count(result.Text, "[>]"); got != 1 {
		t.Fatalf("in-progress markers = %d, want 1", got)
	}
	if !strings.Contains(result.Text, "[>] b") {
		t.Errorf("b was not started: %s", result.Text)
	}
	if strings.Contains(result.Text, "[>] a") {
		t.Errorf("a was not demoted: %s", result.Text)
	}
}

func TestTodoStartRefusesToRestartATerminalTask(t *testing.T) {
	// Restarting a completed task would silently un-complete it.
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a"},
	})
	tool.Execute(testContext(), map[string]any{"op": "done", "phase": "p", "task": "a"})

	if result := tool.Execute(testContext(), map[string]any{"op": "start", "phase": "p", "task": "a"}); !result.IsError {
		t.Error("a completed task was restarted")
	}
}

func TestTodoBlockRequiresAReason(t *testing.T) {
	// A blocked task with no reason is indistinguishable from a pending one to
	// everyone reading it later.
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a"},
	})
	if result := tool.Execute(testContext(), map[string]any{
		"op": "block", "phase": "p", "task": "a",
	}); !result.IsError {
		t.Error("block without a reason was accepted")
	}

	result := tool.Execute(testContext(), map[string]any{
		"op": "block", "phase": "p", "task": "a", "reason": "waiting on the API",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "blocked: waiting on the API") {
		t.Errorf("the reason is not shown: %s", result.Text)
	}
	// A blocked task cannot also be the one in progress.
	if strings.Contains(result.Text, "[>] a") {
		t.Errorf("the blocked task is still in progress: %s", result.Text)
	}
}

func TestTodoUnblockRejectsANonBlockedTask(t *testing.T) {
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a"},
	})
	if result := tool.Execute(testContext(), map[string]any{"op": "unblock", "phase": "p", "task": "a"}); !result.IsError {
		t.Error("unblocking a task that was never blocked was accepted")
	}
}

func TestTodoUnknownTaskIsRejected(t *testing.T) {
	// An exact-match miss must fail: guessing would silently edit the wrong
	// task or report a success that never happened.
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a"},
	})
	for _, op := range []string{"start", "done", "rm", "block", "unblock"} {
		args := map[string]any{"op": op, "phase": "p", "task": "nope"}
		if op == "block" {
			args["reason"] = "x"
		}
		if result := tool.Execute(testContext(), args); !result.IsError {
			t.Errorf("%s on an unknown task succeeded", op)
		}
	}
}

func TestTodoAppendAddsToAnExistingPhase(t *testing.T) {
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a"},
	})
	result := tool.Execute(testContext(), map[string]any{
		"op": "append", "phase": "p", "tasks": []any{"b"},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "Todo: 0/2 done") {
		t.Errorf("the appended task is not counted: %s", result.Text)
	}
}

func TestTodoAppendRejectsADuplicate(t *testing.T) {
	// A second "a" would be unreachable by exact match.
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a"},
	})
	if result := tool.Execute(testContext(), map[string]any{
		"op": "append", "phase": "p", "tasks": []any{"a"},
	}); !result.IsError {
		t.Error("a duplicate task was appended")
	}
}

func TestTodoRemoveDropsEmptiedPhases(t *testing.T) {
	// An empty phase header is noise, so the phase itself is removed.
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"only"},
	})
	result := tool.Execute(testContext(), map[string]any{"op": "rm", "phase": "p", "task": "only"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if strings.Contains(result.Text, "p\n") {
		t.Errorf("the emptied phase is still listed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "No tasks") {
		t.Errorf("an empty list did not render as empty: %s", result.Text)
	}
}

func TestTodoViewIsReadOnly(t *testing.T) {
	tool := NewTodoTool()
	tool.Execute(testContext(), map[string]any{
		"op": "init", "phase": "p", "tasks": []any{"a", "b"},
	})
	first := tool.Execute(testContext(), map[string]any{"op": "view"}).Text
	second := tool.Execute(testContext(), map[string]any{"op": "view"}).Text
	if first != second {
		t.Error("view changed the list")
	}
}

func TestTodoUnknownOpIsRejected(t *testing.T) {
	if result := runTodo(t, map[string]any{"op": "frobnicate"}); !result.IsError {
		t.Error("an unknown op was accepted")
	}
}

func TestTodoIsExclusive(t *testing.T) {
	// The list is mutated under a lock; two concurrent calls must not
	// interleave.
	if !NewTodoTool().Exclusive(nil) {
		t.Error("todo must run alone")
	}
}

func TestTodoNormalizeInProgressDemotesExtras(t *testing.T) {
	// The invariant is one in-progress task. Several must be demoted rather
	// than left for the UI to disambiguate.
	list := TodoList{Phases: []Phase{{
		Name: "p",
		Tasks: []Task{
			{Content: "a", Status: TaskInProgress},
			{Content: "b", Status: TaskInProgress},
			{Content: "c", Status: TaskInProgress},
		},
	}}}
	list.NormalizeInProgress()

	count := 0
	for _, task := range list.Phases[0].Tasks {
		if task.Status == TaskInProgress {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("in-progress tasks = %d, want 1", count)
	}
	// The first must be the one kept.
	if list.Phases[0].Tasks[0].Status != TaskInProgress {
		t.Error("the first task was demoted instead of a later one")
	}
}

func TestTodoProgressCountsAbandonedAsDone(t *testing.T) {
	// Abandoned is terminal: leaving it out of "done" would make the percentage
	// stall below 100 forever.
	list := TodoList{Phases: []Phase{{
		Name: "p",
		Tasks: []Task{
			{Content: "a", Status: TaskCompleted},
			{Content: "b", Status: TaskAbandoned},
			{Content: "c", Status: TaskPending},
		},
	}}}
	got := list.Progress()
	if got.Total != 3 || got.Done != 2 {
		t.Errorf("progress = %+v, want total 3 done 2", got)
	}
	if got.Percent() != 66 {
		t.Errorf("percent = %d, want 66", got.Percent())
	}
}

func TestTodoNextActionablePrefersInProgress(t *testing.T) {
	list := TodoList{Phases: []Phase{{
		Name: "p",
		Tasks: []Task{
			{Content: "pending-first", Status: TaskPending},
			{Content: "working", Status: TaskInProgress},
		},
	}}}
	_, task, ok := list.NextActionable()
	if !ok {
		t.Fatal("no actionable task found")
	}
	if task.Content != "working" {
		t.Errorf("next = %q, want the in-progress task", task.Content)
	}
}

func TestTodoNextActionableFallsBackToPending(t *testing.T) {
	list := TodoList{Phases: []Phase{{
		Name: "p",
		Tasks: []Task{
			{Content: "done", Status: TaskCompleted},
			{Content: "next-up", Status: TaskPending},
		},
	}}}
	_, task, ok := list.NextActionable()
	if !ok || task.Content != "next-up" {
		t.Errorf("next = %q (found %v), want next-up", task.Content, ok)
	}
}

func TestTodoRenderMarksEveryStatus(t *testing.T) {
	// A user reading the list must be able to tell the statuses apart without a
	// legend.
	list := TodoList{Phases: []Phase{{
		Name: "p",
		Tasks: []Task{
			{Content: "t1", Status: TaskPending},
			{Content: "t2", Status: TaskInProgress},
			{Content: "t3", Status: TaskCompleted},
			{Content: "t4", Status: TaskAbandoned},
			{Content: "t5", Status: TaskBlocked, Blocker: "api"},
		},
	}}}
	out := list.Render()
	for _, glyph := range []string{"[ ]", "[>]", "[x]", "[-]", "[!]"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("glyph %s missing from the rendering", glyph)
		}
	}
}
