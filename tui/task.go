package tui

import (
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/contract"
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

// TaskResume is the durable state a new prompt must be checked against.
//
// HasContract distinguishes "this task was started under the Phase 7 contract
// schema" from a version-1 checkpoint: the latter has no evidence boundary and
// cannot be resumed into a completion. The projection is read-only; the
// controller remains the owner.
type TaskResume struct {
	ID           string
	Status       string
	Objective    string
	ReviewReason string
	Contract     contract.IntentContract
	HasContract  bool
	Snapshot     *agent.TargetSnapshot
}

// TaskHooks wire the durable task continuity layer into the program.
//
// Every field is nil-safe: a program built without them runs exactly as before,
// with no task projection and no checkpointing. The hooks are the seam, so the
// program never imports the task domain and the task domain never reaches into
// the UI.
type TaskHooks struct {
	// Begin records that a turn is starting with this contract and pre-run
	// snapshot, creating or resuming the task, and returns its projection.
	Begin func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection
	// Current returns the active task's persisted boundary, if any. The
	// controller remains the owner.
	Current func() (TaskResume, bool)
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
	// Cancel closes the task as deliberately abandoned. It never claims the
	// objective was completed.
	Cancel func(reason string) TaskProjection
	// AcceptReview closes a review task as accepted-without-completion. It is
	// the explicit human replacement path: the next prompt starts a new task
	// identity instead of inheriting the old contract.
	AcceptReview func(reason string) TaskProjection
	// Context returns deterministic orientation blocks for the current task.
	Context func() []string
	// Evidence returns the task's durable evidence state. The projector
	// updates it in place; the controller checkpoints it at lifecycle
	// boundaries. Nil disables task-shaped projection.
	Evidence func() *cognition.EvidenceState
	// RecordProjection stores the last projection audit for /audit.
	RecordProjection func(agent.ProjectionReport)
	// ProjectionReview returns the last projection's evidence review, or ""
	// when the runtime had enough evidence. It is a completion veto, never an
	// authorization.
	ProjectionReview func() string
	// Audit returns the last projection audit, for /audit.
	Audit func() string
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

// taskCommand handles /task status|cancel|accept.
//
// It is the explicit human decision the review boundary requires. Cancel and
// accept both close the task as CANCELLED: neither claims the objective was
// completed, and the next prompt starts a new task identity instead of
// inheriting the old contract.
func (p *Program) taskCommand(args []string) {
	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "cancel the running turn before changing the task"})
		return
	}
	if p.task.Current == nil {
		p.state.Apply(Notice{Text: "task continuity is unavailable"})
		return
	}
	current, ok := p.task.Current()
	if !ok {
		p.state.Apply(Notice{Text: "no task is active"})
		return
	}

	switch action {
	case "status":
		text := "task " + current.Status + ": " + current.Objective
		if current.ReviewReason != "" {
			text += "\nneeds review: " + current.ReviewReason
		}
		p.state.Apply(Notice{Text: text})
	case "cancel", "accept":
		if taskStatusTerminal(current.Status) {
			p.state.Apply(Notice{Text: "task is already " + current.Status})
			return
		}
		if action == "accept" {
			if p.task.AcceptReview == nil {
				p.state.Apply(Notice{Text: "review acceptance is unavailable"})
				return
			}
			p.PublishTask(p.task.AcceptReview("reviewed and closed by user without a completion claim"))
			p.state.Apply(Notice{Text: "review closed without completion; the next prompt starts a new task"})
			return
		}
		if p.task.Cancel == nil {
			p.state.Apply(Notice{Text: "task cancellation is unavailable"})
			return
		}
		p.PublishTask(p.task.Cancel("cancelled by user"))
		p.state.Apply(Notice{Text: "task cancelled; the next prompt starts a new task"})
	default:
		p.state.Apply(Notice{Text: "usage: /task [status|cancel|accept]"})
	}
}

// taskStatusTerminal reports whether the task's status absorbs further work.
func taskStatusTerminal(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "COMPLETED", "FAILED", "CANCELLED":
		return true
	default:
		return false
	}
}

// continuationPrompt recognizes a deliberate continuation of the open task.
//
// It is a finite vocabulary, not natural-language understanding: the hard
// safety rule is that completion still uses the persisted contract. This only
// decides whether an obviously-continuing prompt may attach to it without an
// extra human step.
func continuationPrompt(text string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
	switch normalized {
	case "continue", "continue.", "go on", "keep going", "resume", "resume.",
		"retry", "retry.", "try again", "finish", "finish it", "carry on",
		"verify", "verify the fix", "verify the change", "run the check",
		"run the verification", "run the tests", "run the test":
		return true
	}
	return false
}

// contractCompatible reports whether a new prompt's contract is the same work
// as the persisted one. Targets must be a subset: a prompt may narrow the
// existing objective, but naming a new coordinate is a different task.
func contractCompatible(next, persisted contract.IntentContract) bool {
	if next.Kind != persisted.Kind {
		return false
	}
	for _, target := range next.Targets {
		found := false
		for _, existing := range persisted.Targets {
			if existing == target {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
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
