package main

import (
	"fmt"
	"os"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/task"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/tui"
)

// installTaskContinuity wires the durable task continuity layer behind the
// TUI.
//
// The task artifact is project-local (.ohgo/task.md and .ohgo/progress.json),
// so one project has one active task: a resume continues that task rather than
// creating a new session-scoped one. The controller is bookkeeping only — the
// loop still owns execution and the workspace still owns authority — so this
// wiring cannot grant a capability.
//
// It returns the controller so the caller can hook a compaction report into a
// checkpoint. A nil store or missing todo tool still installs a usable layer:
// the objective and lifecycle are recorded even when no todo list exists.
func installTaskContinuity(program *tui.Program, root string, toolSet []agent.Tool, holders ...*budget.Holder) *task.Controller {
	controller := task.NewController(task.NewStore(root), todoSource(toolSet))
	var holder *budget.Holder
	if len(holders) > 0 {
		holder = holders[0]
	}
	if holder != nil && holder.Get() != nil {
		controller.SetBudgetLimits(holder.Get().Limits())
	}
	hooks := tui.TaskHooks{
		Begin: func(intent contract.IntentContract, snapshot *agent.TargetSnapshot) tui.TaskProjection {
			recorded, err := controller.Begin(intent, snapshot)
			return taskProjection(recorded, err)
		},
		Current: func() (tui.TaskResume, bool) {
			current, ok := controller.Current()
			if !ok {
				return tui.TaskResume{}, false
			}
			resume := tui.TaskResume{
				ID:           current.ID,
				Status:       string(current.Status),
				Objective:    current.Objective,
				ReviewReason: current.ReviewReason,
			}
			if current.Contract != nil {
				resume.Contract = *current.Contract
				resume.HasContract = true
			}
			resume.Snapshot = current.Snapshot
			return resume, true
		},
		Sync: func() tui.TaskProjection {
			recorded, err := controller.Sync()
			return taskProjection(recorded, err)
		},
		Pause: func(reason string) tui.TaskProjection {
			recorded, err := controller.Pause(reason)
			return taskProjection(recorded, err)
		},
		NeedsReview: func(reason string) tui.TaskProjection {
			recorded, err := controller.NeedsReview(reason)
			return taskProjection(recorded, err)
		},
		Complete: func() tui.TaskProjection {
			recorded, err := controller.Complete()
			return taskProjection(recorded, err)
		},
		Fail: func(reason string) tui.TaskProjection {
			recorded, err := controller.Fail(reason)
			return taskProjection(recorded, err)
		},
		Cancel: func(reason string) tui.TaskProjection {
			recorded, err := controller.Cancel(reason)
			return taskProjection(recorded, err)
		},
		AcceptReview: func(reason string) tui.TaskProjection {
			recorded, err := controller.Cancel(reason)
			return taskProjection(recorded, err)
		},
		Context:          controller.Context,
		Evidence:         controller.Evidence,
		RecordProjection: controller.RecordProjection,
		ProjectionReview: func() string {
			report, ok := controller.LastProjection()
			if !ok {
				return ""
			}
			return report.EvidenceReview()
		},
		Audit: controller.Audit,
	}
	if holder != nil {
		// The TUI grants a fresh run budget at each turn boundary through the
		// controller, then records what the tree consumed. The holder is the
		// same instance every spawner reads, so descendants cannot obtain a
		// separate grant.
		hooks.RunBudget = func() *budget.Budget {
			grant := controller.RunBudget()
			holder.Set(grant)
			return grant
		}
		hooks.RecordBudget = controller.RecordBudget
	}
	program.SetTaskHooks(hooks)

	// A checkpoint recovered from disk is shown before the next turn. A
	// non-terminal one was interrupted (a crash is normalized to PAUSED), so
	// the transcript says how to continue rather than silently resuming.
	recovered, found, err := controller.Restore()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "ohgo: task continuity unavailable: %v\n", err)
	case found:
		program.PublishTask(taskProjection(recovered, nil))
		switch recovered.Status {
		case task.StatusPaused:
			program.PublishNotice("a paused task was recovered; your next prompt continues it")
		case task.StatusRunning:
			program.PublishNotice("a task was recovered from an interrupted run")
		}
	}
	return controller
}

// taskProjection converts a domain task into the TUI projection.
func taskProjection(recorded task.Task, err error) tui.TaskProjection {
	projection := tui.TaskProjection{
		ID:        recorded.ID,
		Status:    string(recorded.Status),
		Objective: recorded.Objective,
	}
	for _, item := range task.Items(recorded) {
		projection.Items = append(projection.Items, tui.TaskItem{
			State: string(item.State),
			Label: item.Label,
		})
	}
	if err != nil {
		projection.Err = err.Error()
	}
	return projection
}

// structuralResolver returns the existing symbol index through the symbols
// tool, or nil when the tool set has no structural tool.
//
// It is the same instance the model queries, so semantic completion never
// builds a second repository index.
func structuralResolver(toolSet []agent.Tool) agent.StructuralResolver {
	for _, tool := range toolSet {
		if symbols, ok := tool.(*tools.SymbolsTool); ok {
			return symbols
		}
	}
	return nil
}

// todoSource finds the session's todo tool and returns its snapshot.
//
// The todo tool stays the single owner of todo mutation; the task layer records
// a durable snapshot rather than introducing a second writer. No todo tool
// means no snapshot, which the controller handles.
func todoSource(toolSet []agent.Tool) task.Source {
	for _, tool := range toolSet {
		todo, ok := tool.(*tools.TodoTool)
		if !ok {
			continue
		}
		return func() []task.Todo { return todoSnapshot(todo) }
	}
	return nil
}

// todoSnapshot projects the tool list into the task's todo vocabulary.
//
// A completed item carries the tool's report as evidence, tagged as a model
// claim rather than independent verification: the todo tool records what the
// model asserted, and the architecture must not present that as proof.
func todoSnapshot(todo *tools.TodoTool) []task.Todo {
	list := todo.Snapshot()
	var out []task.Todo
	for _, phase := range list.Phases {
		for _, item := range phase.Tasks {
			status := task.TodoTodo
			switch item.Status {
			case tools.TaskCompleted, tools.TaskAbandoned:
				status = task.TodoDone
			case tools.TaskInProgress:
				status = task.TodoActive
			}
			entry := task.Todo{Label: item.Content, Status: status}
			if status == task.TodoDone {
				entry.Evidence = "model claim (" + string(item.Status) + ")"
			}
			out = append(out, entry)
		}
	}
	return out
}
