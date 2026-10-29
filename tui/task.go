package tui

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
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
	// Recap is the semantic completion narrative, set exactly once when the
	// task reaches COMPLETED. It describes what the run accomplished from
	// observed evidence rather than echoing the objective or reporting
	// telemetry. It is presentation metadata only: never persisted, never
	// sent to the model, and never part of the block's copy payload. The
	// state layer copies it into a trailing EntryRecap after the stream
	// settles, so it renders below the answer rather than inside this block;
	// the status check prevents a premature or stale value from claiming a
	// finish.
	Recap string
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
	// Detach preserves the active task durably and clears the session binding.
	// A non-terminal task remains resumable via List/Resume; the live slot is
	// left empty so the next session or next new intent starts cleanly. It
	// never deletes durable work.
	Detach func() TaskProjection
	// ListResumable returns every resumable durable task, oldest first. Empty
	// when no layer is installed or nothing is resumable.
	ListResumable func() []TaskResume
	// ResumeTask attaches one resumable task to the current session by ID, or
	// the latest when id is empty. It restores durable state and moves the
	// task to RUNNING.
	ResumeTask func(id string) TaskProjection
	// CancelResumable closes one resumable task by ID, or the latest when id
	// is empty. It never claims completion and the task stops being resumable.
	CancelResumable func(id string) TaskProjection
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

// taskCommand handles /task status|list|show|resume|cancel|accept.
//
// Status, list, and show are read-only. Resume attaches a resumable durable
// task to the current session; cancel and accept close it as CANCELLED without
// ever claiming completion. The next prompt after a close starts a new task
// identity instead of inheriting the old contract.
//
// Durable work survives sessions: detaching (via /new or a new intent) never
// deletes it, and /task list shows what remains resumable.
func (p *Program) taskCommand(args []string) {
	action := "status"
	rest := []string{}
	if len(args) > 0 {
		action = strings.ToLower(args[0])
		rest = args[1:]
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "cancel the running turn before changing the task"})
		return
	}
	if p.task.Current == nil {
		p.state.Apply(Notice{Text: "task continuity is unavailable"})
		return
	}

	switch action {
	case "list":
		if p.task.ListResumable == nil {
			p.state.Apply(Notice{Text: "task listing is unavailable"})
			return
		}
		resumable := p.task.ListResumable()
		if len(resumable) == 0 {
			p.state.Apply(Notice{Text: "no resumable tasks"})
			return
		}
		var sb strings.Builder
		sb.WriteString("resumable tasks:")
		for _, r := range resumable {
			fmt.Fprintf(&sb, "\n  %s %s: %s", r.Status, shortTaskID(r.ID), r.Objective)
		}
		p.state.Apply(Notice{Text: sb.String()})
		return
	case "show":
		target := strings.Join(rest, " ")
		resume, ok := p.selectTask(target)
		if !ok {
			return
		}
		text := "task " + resume.Status + ": " + resume.Objective
		if resume.ReviewReason != "" {
			text += "\nneeds review: " + resume.ReviewReason
		}
		text += "\nid: " + resume.ID
		p.state.Apply(Notice{Text: text})
		return
	case "resume":
		if p.task.ResumeTask == nil {
			p.state.Apply(Notice{Text: "task resume is unavailable"})
			return
		}
		target := strings.Join(rest, " ")
		id := p.resolveResumeID(target)
		if id == "__picker__" {
			return
		}
		projection := p.task.ResumeTask(id)
		if projection.Err != "" {
			p.state.Apply(Notice{Text: "task resume: " + projection.Err})
			return
		}
		p.PublishTask(projection)
		p.state.Apply(Notice{Text: "resumed task: " + projection.Objective})
		return
	case "cancel":
		target := strings.Join(rest, " ")
		if target == "" {
			// No ID: prefer the explicit resumable canceller when present so
			// a detached task can be cancelled without resuming it first.
			if p.task.CancelResumable != nil {
				projection := p.task.CancelResumable("")
				if projection.Err != "" {
					p.state.Apply(Notice{Text: "task cancel: " + projection.Err})
					return
				}
				if projection.ID != "" {
					p.PublishTask(projection)
				}
				p.state.Apply(Notice{Text: "task cancelled; the next prompt starts a new task"})
				return
			}
		} else if p.task.CancelResumable != nil {
			projection := p.task.CancelResumable(p.resolveCancelID(target))
			if projection.Err != "" {
				p.state.Apply(Notice{Text: "task cancel: " + projection.Err})
				return
			}
			if projection.ID != "" {
				p.PublishTask(projection)
			}
			p.state.Apply(Notice{Text: "task cancelled; the next prompt starts a new task"})
			return
		}
		current, ok := p.task.Current()
		if !ok {
			p.state.Apply(Notice{Text: "no task is active"})
			return
		}
		if taskStatusTerminal(current.Status) {
			p.state.Apply(Notice{Text: "task is already " + current.Status})
			return
		}
		if p.task.Cancel == nil {
			p.state.Apply(Notice{Text: "task cancellation is unavailable"})
			return
		}
		p.PublishTask(p.task.Cancel("cancelled by user"))
		p.state.Apply(Notice{Text: "task cancelled; the next prompt starts a new task"})
		return
	case "status":
		current, ok := p.task.Current()
		if !ok {
			// No active task in this session is not an error: a durable task
			// may still be resumable. Point at the explicit recovery path.
			if p.task.ListResumable != nil {
				if resumable := p.task.ListResumable(); len(resumable) > 0 {
					p.state.Apply(Notice{Text: "no active task in this session — " + formatTaskCount(len(resumable)) + " resumable task(s) available (see /task list)"})
					return
				}
			}
			p.state.Apply(Notice{Text: "no task is active"})
			return
		}
		text := "task " + current.Status + ": " + current.Objective
		if current.ReviewReason != "" {
			text += "\nneeds review: " + current.ReviewReason
		}
		p.state.Apply(Notice{Text: text})
		return
	case "accept":
		current, ok := p.task.Current()
		if !ok {
			p.state.Apply(Notice{Text: "no task is active"})
			return
		}
		if taskStatusTerminal(current.Status) {
			p.state.Apply(Notice{Text: "task is already " + current.Status})
			return
		}
		if p.task.AcceptReview == nil {
			p.state.Apply(Notice{Text: "review acceptance is unavailable"})
			return
		}
		p.PublishTask(p.task.AcceptReview("reviewed and closed by user without a completion claim"))
		p.state.Apply(Notice{Text: "review closed without completion; the next prompt starts a new task"})
		return
	default:
		p.state.Apply(Notice{Text: "usage: /task [status|list|show [id]|resume [id]|cancel [id]|accept]"})
	}
}

// selectTask locates one task for /task show by ID, objective substring, or
// the latest resumable when the query is empty.
func (p *Program) selectTask(query string) (TaskResume, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		if current, ok := p.task.Current(); ok {
			return current, true
		}
	}
	if p.task.ListResumable == nil {
		if current, ok := p.task.Current(); ok {
			return current, true
		}
		p.state.Apply(Notice{Text: "no task is active"})
		return TaskResume{}, false
	}
	resumable := p.task.ListResumable()
	if len(resumable) == 0 {
		p.state.Apply(Notice{Text: "no task is active"})
		return TaskResume{}, false
	}
	if query == "" {
		return resumable[len(resumable)-1], true
	}
	for _, r := range resumable {
		if strings.EqualFold(r.ID, query) || strings.HasPrefix(strings.ToLower(r.ID), strings.ToLower(query)) {
			return r, true
		}
	}
	lowered := strings.ToLower(query)
	for _, r := range resumable {
		if strings.Contains(strings.ToLower(r.Objective), lowered) {
			return r, true
		}
	}
	p.state.Apply(Notice{Text: "no resumable task matches " + query})
	return TaskResume{}, false
}

// resolveResumeID maps a /task resume argument to an ID. An empty argument
// resumes the latest; an ambiguous non-empty argument without an ID match opens
// the task picker instead of picking arbitrarily.
func (p *Program) resolveResumeID(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	if p.task.ListResumable == nil {
		return query
	}
	resumable := p.task.ListResumable()
	for _, r := range resumable {
		if strings.EqualFold(r.ID, query) || strings.HasPrefix(strings.ToLower(r.ID), strings.ToLower(query)) {
			return r.ID
		}
	}
	lowered := strings.ToLower(query)
	var matched []TaskResume
	for _, r := range resumable {
		if strings.Contains(strings.ToLower(r.Objective), lowered) {
			matched = append(matched, r)
		}
	}
	if len(matched) == 1 {
		return matched[0].ID
	}
	if len(matched) == 0 {
		return query
	}
	items := make([]PickerItem, 0, len(matched))
	for _, r := range matched {
		items = append(items, PickerItem{
			Title:  shortTaskID(r.ID) + " " + r.Objective,
			Detail: r.Status,
			Value:  r.ID,
		})
	}
	p.state.Apply(PickerOpened{Kind: PickerTasks, Title: "resume task", Items: items})
	return "__picker__"
}

// resolveCancelID maps a /task cancel argument to an ID, preferring an exact
// ID prefix and otherwise passing the query through for the controller to
// match deterministically.
func (p *Program) resolveCancelID(query string) string {
	query = strings.TrimSpace(query)
	if query == "" || p.task.ListResumable == nil {
		return query
	}
	for _, r := range p.task.ListResumable() {
		if strings.EqualFold(r.ID, query) || strings.HasPrefix(strings.ToLower(r.ID), strings.ToLower(query)) {
			return r.ID
		}
	}
	return query
}

// shortTaskID renders the leading bytes of a task ID for list rows.
func shortTaskID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// formatTaskCount renders a small count without importing strconv.
func formatTaskCount(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
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
// It is an explicit-intent classifier, not semantic similarity: only language
// that names continuation resumes a resumable task. A new intent — even one
// about the same repository or the same files — is never a continuation merely
// because it is related. The hard safety rule is unchanged: completion still
// uses the persisted contract, this only decides attachment.
func continuationPrompt(text string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
	switch normalized {
	case "continue", "continue.", "go on", "keep going", "resume", "resume.",
		"retry", "retry.", "try again", "finish", "finish it", "carry on",
		"verify", "verify the fix", "verify the change", "run the check",
		"run the verification", "run the tests", "run the test":
		return true
	}
	// Explicit continuation phrases: "continue the previous task",
	// "resume the README task", "continue where we left off". Any prompt that
	// names continuation with these verbs is an explicit resume request.
	if containsContinuationVerb(normalized) {
		return true
	}
	return false
}

// containsContinuationVerb reports whether the prompt explicitly names
// continuation. It matches whole words so "migrating" or "continuous" do not
// count, while "continue the previous task" and "resume the README" do.
func containsContinuationVerb(normalized string) bool {
	words := strings.Fields(normalized)
	has := func(want string) bool {
		for _, w := range words {
			trimmed := strings.Trim(w, ".,!?;:")
			if trimmed == want {
				return true
			}
		}
		return false
	}
	if has("continue") || has("resume") || has("resumes") || has("resuming") {
		return true
	}
	for _, phrase := range []string{
		"keep going", "go on", "carry on", "where we left off", "previous task",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
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
