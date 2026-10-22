package tui

import (
	"strings"

	"github.com/PizenLabs/ohgo/budget"
)

// Task state values the task domain projects. They are duplicated here as
// plain strings rather than imported so the seam stays a projection: the TUI
// renders a vocabulary, it does not depend on the task package's types.
const (
	taskStatePending = "pending"
	taskStateActive  = "active"
	taskStateDone    = "done"
)

// TaskItem is one projected todo row.
type TaskItem struct {
	// State is one of pending, active, or done.
	State string
	// Label is the todo text.
	Label string
}

// TaskProjection is the UI projection of durable task state.
//
// It is a projection, never an owner: the TUI renders what the task layer
// reports, and never derives execution state from it. Err carries a checkpoint
// failure so the transcript can say the durable write did not land rather than
// showing a state that was not persisted.
type TaskProjection struct {
	// ID identifies the task. Empty means no task layer is installed and the
	// projection is dropped.
	ID string
	// Status is the lifecycle status (RUNNING, PAUSED, COMPLETED, ...).
	Status string
	// Objective is what the task is trying to accomplish.
	Objective string
	// Items are the todos, in order.
	Items []TaskItem
	// Err reports a projection or checkpoint failure, empty on success.
	Err string
}

// TaskHooks wire the durable task continuity layer into the program.
//
// Every field is nil-safe: a program built without them runs exactly as before,
// with no task projection and no checkpointing. The hooks are the seam, so the
// program never imports the task domain and the task domain never reaches into
// the UI.
type TaskHooks struct {
	// Begin records that a turn is starting with this objective, creating or
	// resuming the task, and returns its projection.
	Begin func(objective string) TaskProjection
	// Sync refreshes and checkpoints the current task, returning a projection.
	Sync func() TaskProjection
	// Pause records an interruption and returns the paused projection.
	Pause func(reason string) TaskProjection
	// NeedsReview records that the turn ended without enough runtime evidence
	// to claim completion, and returns the reviewable projection.
	NeedsReview func(reason string) TaskProjection
	// Complete records a successful end and returns the projection.
	Complete func() TaskProjection
	// Fail records an execution failure and returns the projection.
	Fail func(reason string) TaskProjection
	// Context returns deterministic orientation blocks for the current task.
	Context func() []string
	// RunBudget returns the fresh root grant for one turn. The host installs
	// it on the loop and on the subagent spawner before execution, so every
	// descendant of the turn shares the same instance.
	RunBudget func() *budget.Budget
	// RecordBudget folds a finished run's consumption into the durable task
	// ledger. It does not reset the in-memory grant.
	RecordBudget func(snapshot budget.Snapshot)
}

// TaskProjected publishes a task projection into the transcript.
type TaskProjected struct {
	Projection TaskProjection
}

func (TaskProjected) eventKind() string { return "task_projected" }

// SetTaskHooks installs the durable task continuity layer.
func (p *Program) SetTaskHooks(hooks TaskHooks) { p.task = hooks }

// PublishTask projects a task state into the transcript. It is safe to call
// from outside the update goroutine, like PublishNotice.
func (p *Program) PublishTask(projection TaskProjection) {
	if projection.ID == "" {
		return
	}
	p.publish(TaskProjected{Projection: projection})
}

// SyncTask checkpoints and republishes the current task, if a layer is
// installed. It is the hook a compaction report uses to keep the checkpoint in
// step with a reconstructed context.
func (p *Program) SyncTask() {
	if p.task.Sync == nil {
		return
	}
	p.PublishTask(p.task.Sync())
}

// taskGlyph maps a projection state to its single status marker. One glyph
// column, no colour-only signal: done, active, pending read apart in a
// monochrome terminal.
func taskGlyph(state string) string {
	switch state {
	case taskStateDone:
		return "✔"
	case taskStateActive:
		return "◼"
	default:
		return "◻"
	}
}

// taskLines renders a projection as terminal lines.
//
// The form is deliberately compact: one header naming the objective and status,
// then one row per todo under a single ⎿ continuation column. Completed work is
// styled subtly, active work distinctly, and pending work quietly, so the shape
// is legible without an IDE-style panel.
func taskLines(projection *TaskProjection, pal *Palette, layout Layout) []string {
	if projection == nil || projection.ID == "" {
		return nil
	}
	width := textWidth(layout)
	fit := func(s string) string {
		if width <= 0 {
			return s
		}
		return TruncateToWidth(s, width)
	}

	header := "task"
	if objective := strings.TrimSpace(projection.Objective); objective != "" {
		header += "  " + objective
	}
	if projection.Status != "" {
		header += "  " + strings.ToLower(projection.Status)
	}
	lines := []string{pal.Notice.Render(fit(header))}

	if len(projection.Items) == 0 {
		lines = append(lines, pal.Status.Render(fit("  ◻ no todos")))
	}
	for i, item := range projection.Items {
		prefix := "  "
		if i == 0 {
			prefix = "⎿ "
		}
		text := fit(prefix + taskGlyph(item.State) + " " + item.Label)
		switch item.State {
		case taskStateDone:
			text = pal.ToolDone.Render(text)
		case taskStateActive:
			text = pal.ToolRun.Render(text)
		default:
			text = pal.Status.Render(text)
		}
		lines = append(lines, text)
	}
	if projection.Err != "" {
		lines = append(lines, pal.ToolError.Render(fit("  ! "+projection.Err)))
	}
	return lines
}
