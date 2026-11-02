package main

import (
	"fmt"
	"os"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// installTaskContinuity wires the durable task continuity layer behind the
// TUI.
//
// The task artifact is project-local (.ohgo/task.md, .ohgo/progress.json, and
// .ohgo/tasks/ for detached work), so durable work survives session
// boundaries but never owns future intent: a new session starts with an empty
// session binding, and a new intent detaches the old task rather than being
// blocked by it. The controller is bookkeeping only — the loop still owns
// execution and the workspace still owns authority — so this wiring cannot
// grant a capability. role supplies the live execution role for role-scoped
// context; it is nil-safe and a nil source keeps the pre-Phase-1 BUILD default.
//
// It returns the controller so the caller can hook a compaction report into a
// checkpoint. A nil store or missing todo tool still installs a usable layer:
// the objective and lifecycle are recorded even when no todo list exists.
func installTaskContinuity(program *tui.Program, root string, toolSet []agent.Tool, role func() workspace.Workspace, holders ...*budget.Holder) *task.Controller {
	controller := task.NewController(task.NewStore(root), todoSource(toolSet))
	controller.SetRoleSource(role)
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
			return taskResume(current), true
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
		Detach: func() tui.TaskProjection {
			detached, ok, err := controller.Detach()
			if err != nil {
				return tui.TaskProjection{Err: err.Error()}
			}
			if !ok {
				return tui.TaskProjection{}
			}
			return taskProjection(detached, nil)
		},
		ListResumable: func() []tui.TaskResume {
			tasks, err := controller.ListResumable()
			if err != nil {
				return nil
			}
			out := make([]tui.TaskResume, 0, len(tasks))
			for _, t := range tasks {
				out = append(out, taskResume(t))
			}
			return out
		},
		ResumeTask: func(id string) tui.TaskProjection {
			recorded, err := controller.ResumeTask(id)
			return taskProjection(recorded, err)
		},
		CancelResumable: func(id string) tui.TaskProjection {
			recorded, err := controller.CancelResumable(id, "cancelled by user")
			return taskProjection(recorded, err)
		},
		PreviewHandoff: func(target workspace.Workspace) (tui.HandoffView, error) {
			recorded, err := controller.PreviewHandoff(task.HandoffInput{To: target})
			return handoffView(recorded), err
		},
		ProposeHandoff: func(target workspace.Workspace, reason, summary, workspaceState string) (tui.HandoffView, error) {
			recorded, err := controller.ProposeHandoff(task.HandoffInput{
				To:        target,
				Reason:    reason,
				Summary:   summary,
				Workspace: workspaceState,
			})
			return handoffView(recorded), err
		},
		ApproveHandoff: func(id string) (tui.HandoffView, error) {
			recorded, err := controller.ApproveHandoff(id)
			return handoffView(recorded), err
		},
		FailHandoff: func(id, reason string) error {
			_, err := controller.FailHandoff(id, reason)
			return err
		},
		Context:          controller.Context,
		ObserveTurns:     controller.ObserveTurns,
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
	// the transcript notes it as resumable without blocking the next intent:
	// the new session starts cleanly and the task stays reachable via /task.
	recovered, found, err := controller.Restore()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "ohgo: task continuity unavailable: %v\n", err)
	case found:
		if !recovered.Status.IsTerminal() {
			program.PublishTask(taskProjection(recovered, nil))
			if resumable, lerr := controller.ListResumable(); lerr == nil && len(resumable) > 0 {
				program.PublishNotice("1 resumable task available (see /task list)")
			}
		}
		if recovered.Handoff != nil && recovered.Handoff.Status == task.HandoffInterrupted {
			program.PublishNotice("interrupted handoff " + recovered.Handoff.From.Label() +
				" -> " + recovered.Handoff.To.Label() + " was preserved; authority did not move")
		}
	}
	return controller
}

// taskResume converts a durable task into the TUI's resume contract.
//
// Role is the task's explicit last role, with a role-context fallback for a
// checkpoint written before the field existed. It is continuity metadata for
// the turn gate, never an authority grant.
func taskResume(recorded task.Task) tui.TaskResume {
	resume := tui.TaskResume{
		ID:           recorded.ID,
		Status:       string(recorded.Status),
		Objective:    recorded.Objective,
		ReviewReason: recorded.ReviewReason,
		Role:         string(recorded.LastRole()),
	}
	if recorded.Handoff != nil {
		resume.HandoffStatus = string(recorded.Handoff.Status)
		resume.HandoffTo = recorded.Handoff.To.String()
		resume.HandoffConsumed = recorded.Handoff.Consumed
	}
	if recorded.Contract != nil {
		resume.Contract = *recorded.Contract
		resume.HasContract = true
	}
	resume.Snapshot = recorded.Snapshot
	return resume
}

// handoffView converts a durable handoff into the TUI projection.
//
// The projection carries counts and labels, never record text: the UI can show
// what will be carried without becoming a second copy of the task context.
func handoffView(recorded task.Handoff) tui.HandoffView {
	carried := recorded.Carried()
	return tui.HandoffView{
		ID:          recorded.ID,
		From:        recorded.From.Label(),
		To:          recorded.To.Label(),
		Status:      string(recorded.Status),
		Objective:   recorded.Objective,
		Reason:      recorded.Reason,
		Summary:     recorded.Summary,
		Workspace:   recorded.Workspace,
		Facts:       carried.Facts,
		Hypotheses:  carried.Hypotheses,
		Questions:   carried.Questions,
		Evidence:    carried.Evidence,
		Artifacts:   carried.Artifacts,
		Constraints: carried.Constraints,
	}
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
