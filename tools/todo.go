package tools

import (
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/agent"
)

// TaskStatus is where a task is in its lifecycle.
type TaskStatus string

// Task lifecycle, matching the original's vocabulary.
const (
	// TaskPending is not started.
	TaskPending TaskStatus = "pending"
	// TaskInProgress is being worked on now.
	TaskInProgress TaskStatus = "in_progress"
	// TaskCompleted is finished.
	TaskCompleted TaskStatus = "completed"
	// TaskAbandoned will not be done.
	TaskAbandoned TaskStatus = "abandoned"
	// TaskBlocked cannot proceed.
	TaskBlocked TaskStatus = "blocked"
)

// Task is one todo item.
type Task struct {
	// Content is the task text. It is the identity of a task across operations,
	// which is why it must be unique within a phase.
	Content string
	// Status is the lifecycle position.
	Status TaskStatus
	// Blocker explains why a blocked task cannot proceed.
	Blocker string
}

// Phase is a named group of tasks.
type Phase struct {
	// Name identifies the phase. Operations address tasks by phase plus content.
	Name string
	// Tasks are the phase's items, in order.
	Tasks []Task
}

// TodoList is a session's task list.
//
// The zero value is an empty list, which is a valid starting state.
type TodoList struct {
	// Phases are the task groups, in order.
	Phases []Phase
}

// Find locates a task by phase name and content.
//
// Content is matched exactly: a partial match would make "add tests" and "add
// tests for the parser" ambiguous, and a wrong guess here silently edits the
// wrong task.
func (l *TodoList) Find(phase, content string) (Phase, Task, bool) {
	for _, p := range l.Phases {
		if p.Name != phase {
			continue
		}
		for _, task := range p.Tasks {
			if task.Content == content {
				return p, task, true
			}
		}
	}
	return Phase{}, Task{}, false
}

// NextActionable returns the task the model should work on next.
//
// An in-progress task always wins: starting something else would leave two
// tasks marked in progress, which the display and the model both read as
// "both are being done now".
func (l *TodoList) NextActionable() (Phase, Task, bool) {
	var firstPending Phase
	var pending Task
	var havePending bool

	for _, p := range l.Phases {
		for _, task := range p.Tasks {
			switch task.Status {
			case TaskInProgress:
				return p, task, true
			case TaskPending:
				if !havePending {
					firstPending, pending, havePending = p, task, true
				}
			}
		}
	}
	return firstPending, pending, havePending
}

// NormalizeInProgress enforces the single-in-progress invariant.
//
// Exactly one task may be in progress. If several are, all but the first are
// demoted to pending. If none are and something is pending, the first pending
// task is promoted. This is what lets a UI show "currently working on X"
// without tracking it separately.
func (l *TodoList) NormalizeInProgress() {
	seenInProgress := false
	promoted := false

	for pi := range l.Phases {
		for ti := range l.Phases[pi].Tasks {
			task := &l.Phases[pi].Tasks[ti]
			switch task.Status {
			case TaskInProgress:
				if seenInProgress {
					task.Status = TaskPending
					continue
				}
				seenInProgress = true
			case TaskPending:
				if !seenInProgress && !promoted {
					task.Status = TaskInProgress
					seenInProgress = true
					promoted = true
				}
			}
		}
	}
}

// Progress summarises completion for the status display.
type Progress struct {
	// Total is the number of tasks across all phases.
	Total int
	// Done counts completed and abandoned tasks, which are both terminal.
	Done int
	// InProgress is the single in-progress task, if any.
	InProgress int
}

// Percent returns completion as 0-100, or 0 for an empty list.
func (p Progress) Percent() int {
	if p.Total == 0 {
		return 0
	}
	return p.Done * 100 / p.Total
}

// Progress summarises the list.
func (l *TodoList) Progress() Progress {
	var out Progress
	for _, phase := range l.Phases {
		for _, task := range phase.Tasks {
			out.Total++
			switch task.Status {
			case TaskCompleted, TaskAbandoned:
				out.Done++
			case TaskInProgress:
				out.InProgress++
			}
		}
	}
	return out
}

// Render produces the text a model and a user both read.
//
// One line per task, with a status glyph, grouped under its phase. Empty phases
// are omitted: a phase header with nothing under it is noise.
func (l *TodoList) Render() string {
	if len(l.Phases) == 0 || l.Progress().Total == 0 {
		return "No tasks."
	}

	var sb strings.Builder
	progress := l.Progress()
	fmt.Fprintf(&sb, "Todo: %d/%d done\n\n", progress.Done, progress.Total)

	for _, phase := range l.Phases {
		if len(phase.Tasks) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s\n", phase.Name)
		for _, task := range phase.Tasks {
			fmt.Fprintf(&sb, "  %s %s\n", taskGlyph(task.Status), task.Content)
			if task.Blocker != "" {
				fmt.Fprintf(&sb, "      blocked: %s\n", task.Blocker)
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// taskGlyph maps a status to its display marker.
func taskGlyph(status TaskStatus) string {
	switch status {
	case TaskCompleted:
		return "[x]"
	case TaskInProgress:
		return "[>]"
	case TaskAbandoned:
		return "[-]"
	case TaskBlocked:
		return "[!]"
	default:
		return "[ ]"
	}
}

// TodoTool is the model-facing task list.
//
// State is in-process rather than on disk: the original keeps it in the
// session, and a task list that outlived its session would show stale work on
// a resume.
type TodoTool struct {
	Base
	// mu guards list; a tool may be executed concurrently within one batch.
	mu   sync.Mutex
	list TodoList
}

// NewTodoTool returns an empty task list.
func NewTodoTool() *TodoTool {
	tool := &TodoTool{}
	tool.Base = NewBase("todo",
		"Track a task list for the current session. One task may be in progress "+
			"at a time; starting one demotes the previous to pending.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op": map[string]any{
					"type": "string",
					"enum": []string{
						"init", "start", "done", "rm", "drop", "block",
						"unblock", "append", "view",
					},
					"description": "The operation to perform.",
				},
				"phase": map[string]any{
					"type":        "string",
					"description": "Phase name. Required by every op except view.",
				},
				"task": map[string]any{
					"type":        "string",
					"description": "Task content, verbatim. Addressed by exact match.",
				},
				"tasks": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task contents, for init and append.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Why a task is blocked. For block.",
				},
			},
			"required": []string{"op"},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	tool.SetActionClasses(agent.ActionPlan)
	return tool
}

// Exclusive implements agent.Tool: the list is mutated under a lock, so two
// concurrent calls must not interleave.
func (t *TodoTool) Exclusive(map[string]any) bool { return true }

// Snapshot returns a deep copy of the current list.
//
// It is the read seam for a projection (the task continuity layer records a
// durable snapshot without becoming a second mutation owner): the copy means a
// caller iterating it cannot race an in-flight todo call.
func (t *TodoTool) Snapshot() TodoList {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := TodoList{Phases: make([]Phase, 0, len(t.list.Phases))}
	for _, phase := range t.list.Phases {
		copied := Phase{Name: phase.Name, Tasks: append([]Task(nil), phase.Tasks...)}
		out.Phases = append(out.Phases, copied)
	}
	return out
}

// Execute implements agent.Tool.
func (t *TodoTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	op, err := strArg(args, "op", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	phase, err := strArg(args, "phase", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	taskContent, err := strArg(args, "task", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	reason, err := strArg(args, "reason", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	tasks, err := stringSliceArg(args, "tasks")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	switch op {
	case "init":
		return t.doInit(phase, tasks)
	case "append":
		return t.doAppend(phase, tasks)
	case "start":
		return t.doStart(phase, taskContent)
	case "done":
		return t.doDone(phase, taskContent)
	case "rm", "drop":
		return t.doRemove(phase, taskContent)
	case "block":
		return t.doBlock(phase, taskContent, reason)
	case "unblock":
		return t.doUnblock(phase, taskContent)
	case "view":
		return agent.Result{Text: t.list.Render()}
	default:
		return agent.ErrorResult("unknown op %q; valid ops are init, append, start, done, rm, drop, block, unblock, view", op)
	}
}

// doInit replaces the list with one phase of tasks.
func (t *TodoTool) doInit(phase string, tasks []string) agent.Result {
	if phase == "" {
		return agent.ErrorResult("init requires a phase name")
	}
	if len(tasks) == 0 {
		return agent.ErrorResult("init requires at least one task")
	}
	// A duplicate would make an exact-match lookup ambiguous, so refuse it
	// rather than silently operating on the first.
	if hasDuplicate(tasks) {
		return agent.ErrorResult("init contains duplicate tasks")
	}

	built := make([]Task, 0, len(tasks))
	for _, content := range tasks {
		if strings.TrimSpace(content) == "" {
			return agent.ErrorResult("init contains an empty task")
		}
		built = append(built, Task{Content: content, Status: TaskPending})
	}
	t.list = TodoList{Phases: []Phase{{Name: phase, Tasks: built}}}
	t.list.NormalizeInProgress()

	return agent.Result{
		Text:    t.list.Render(),
		Details: map[string]any{"op": "init", "phase": phase, "tasks": len(tasks)},
	}
}

// doAppend adds tasks to an existing phase, creating it when absent.
func (t *TodoTool) doAppend(phase string, tasks []string) agent.Result {
	if phase == "" {
		return agent.ErrorResult("append requires a phase name")
	}
	if len(tasks) == 0 {
		return agent.ErrorResult("append requires at least one task")
	}
	if hasDuplicate(tasks) {
		return agent.ErrorResult("append contains duplicate tasks")
	}

	index := t.phaseIndex(phase)
	if index < 0 {
		t.list.Phases = append(t.list.Phases, Phase{Name: phase})
		index = len(t.list.Phases) - 1
	}

	// An exact match against an existing task would make the new one
	// unreachable by content, so it is refused rather than shadowed.
	for _, content := range tasks {
		if _, _, ok := t.list.Find(phase, content); ok {
			return agent.ErrorResult("task %q already exists in phase %q", content, phase)
		}
	}

	for _, content := range tasks {
		if strings.TrimSpace(content) == "" {
			return agent.ErrorResult("append contains an empty task")
		}
		t.list.Phases[index].Tasks = append(t.list.Phases[index].Tasks,
			Task{Content: content, Status: TaskPending})
	}
	t.list.NormalizeInProgress()

	return agent.Result{
		Text:    t.list.Render(),
		Details: map[string]any{"op": "append", "phase": phase, "tasks": len(tasks)},
	}
}

// doStart marks a task in progress, demoting whatever was.
func (t *TodoTool) doStart(phase, content string) agent.Result {
	if phase == "" || content == "" {
		return agent.ErrorResult("start requires a phase and a task")
	}
	_, task, ok := t.list.Find(phase, content)
	if !ok {
		return agent.ErrorResult("task %q not found in phase %q", content, phase)
	}
	if task.Status == TaskCompleted || task.Status == TaskAbandoned {
		// Restarting a terminal task would silently un-complete it.
		return agent.ErrorResult("task %q is already %s", content, task.Status)
	}

	for pi := range t.list.Phases {
		for ti := range t.list.Phases[pi].Tasks {
			if t.list.Phases[pi].Tasks[ti].Status == TaskInProgress {
				t.list.Phases[pi].Tasks[ti].Status = TaskPending
			}
		}
	}
	t.setStatus(phase, content, TaskInProgress)

	return agent.Result{
		Text:    fmt.Sprintf("Started: %s\n\n%s", content, t.list.Render()),
		Details: map[string]any{"op": "start", "phase": phase, "task": content},
	}
}

// doDone completes a task.
func (t *TodoTool) doDone(phase, content string) agent.Result {
	if phase == "" || content == "" {
		return agent.ErrorResult("done requires a phase and a task")
	}
	if _, _, ok := t.list.Find(phase, content); !ok {
		return agent.ErrorResult("task %q not found in phase %q", content, phase)
	}
	t.setStatus(phase, content, TaskCompleted)
	t.clearBlocker(phase, content)
	// Another task becomes in progress, so the list never sits with nothing
	// being worked on while work remains.
	t.list.NormalizeInProgress()

	return agent.Result{
		Text:    fmt.Sprintf("Completed: %s\n\n%s", content, t.list.Render()),
		Details: map[string]any{"op": "done", "phase": phase, "task": content},
	}
}

// doRemove removes a task entirely.
func (t *TodoTool) doRemove(phase, content string) agent.Result {
	if phase == "" || content == "" {
		return agent.ErrorResult("rm requires a phase and a task")
	}
	if _, _, ok := t.list.Find(phase, content); !ok {
		return agent.ErrorResult("task %q not found in phase %q", content, phase)
	}

	for pi := range t.list.Phases {
		if t.list.Phases[pi].Name != phase {
			continue
		}
		kept := t.list.Phases[pi].Tasks[:0]
		for _, task := range t.list.Phases[pi].Tasks {
			if task.Content != content {
				kept = append(kept, task)
			}
		}
		t.list.Phases[pi].Tasks = kept
	}
	// An empty phase is dropped so the display does not carry a header with
	// nothing under it.
	t.list.Phases = nonEmptyPhases(t.list.Phases)
	t.list.NormalizeInProgress()

	return agent.Result{
		Text:    fmt.Sprintf("Removed: %s\n\n%s", content, t.list.Render()),
		Details: map[string]any{"op": "rm", "phase": phase, "task": content},
	}
}

// doBlock marks a task blocked with a reason.
func (t *TodoTool) doBlock(phase, content, reason string) agent.Result {
	if phase == "" || content == "" {
		return agent.ErrorResult("block requires a phase and a task")
	}
	if _, _, ok := t.list.Find(phase, content); !ok {
		return agent.ErrorResult("task %q not found in phase %q", content, phase)
	}
	if reason == "" {
		// A blocked task with no reason is indistinguishable from a pending
		// one to everyone reading it later.
		return agent.ErrorResult("block requires a reason")
	}
	t.setStatus(phase, content, TaskBlocked)
	t.setBlocker(phase, content, reason)
	// The blocked task can no longer be the one in progress.
	t.list.NormalizeInProgress()

	return agent.Result{
		Text:    fmt.Sprintf("Blocked: %s (%s)\n\n%s", content, reason, t.list.Render()),
		Details: map[string]any{"op": "block", "phase": phase, "task": content},
	}
}

// doUnblock returns a task to pending.
func (t *TodoTool) doUnblock(phase, content string) agent.Result {
	if phase == "" || content == "" {
		return agent.ErrorResult("unblock requires a phase and a task")
	}
	_, task, ok := t.list.Find(phase, content)
	if !ok {
		return agent.ErrorResult("task %q not found in phase %q", content, phase)
	}
	if task.Status != TaskBlocked {
		return agent.ErrorResult("task %q is %s, not blocked", content, task.Status)
	}
	t.setStatus(phase, content, TaskPending)
	t.clearBlocker(phase, content)
	t.list.NormalizeInProgress()

	return agent.Result{
		Text:    fmt.Sprintf("Unblocked: %s\n\n%s", content, t.list.Render()),
		Details: map[string]any{"op": "unblock", "phase": phase, "task": content},
	}
}

// phaseIndex returns the index of a phase, or -1.
func (t *TodoTool) phaseIndex(name string) int {
	for i, phase := range t.list.Phases {
		if phase.Name == name {
			return i
		}
	}
	return -1
}

// setStatus updates one task's status, leaving others untouched.
func (t *TodoTool) setStatus(phase, content string, status TaskStatus) {
	for pi := range t.list.Phases {
		if t.list.Phases[pi].Name != phase {
			continue
		}
		for ti := range t.list.Phases[pi].Tasks {
			if t.list.Phases[pi].Tasks[ti].Content == content {
				t.list.Phases[pi].Tasks[ti].Status = status
				return
			}
		}
	}
}

func (t *TodoTool) setBlocker(phase, content, reason string) {
	for pi := range t.list.Phases {
		if t.list.Phases[pi].Name != phase {
			continue
		}
		for ti := range t.list.Phases[pi].Tasks {
			if t.list.Phases[pi].Tasks[ti].Content == content {
				t.list.Phases[pi].Tasks[ti].Blocker = reason
				return
			}
		}
	}
}

func (t *TodoTool) clearBlocker(phase, content string) { t.setBlocker(phase, content, "") }

// stringSliceArg extracts a []string argument.
func stringSliceArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		// Accept an already-decoded []string too, for callers that build one.
		if typed, ok := raw.([]string); ok {
			return typed, nil
		}
		return nil, fmt.Errorf("argument %q must be an array of strings", key)
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("argument %q item %d must be a string", key, i)
		}
		out = append(out, text)
	}
	return out, nil
}

// hasDuplicate reports whether a slice contains a repeated value.
func hasDuplicate(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

// nonEmptyPhases drops phases with no tasks.
func nonEmptyPhases(phases []Phase) []Phase {
	out := phases[:0]
	for _, phase := range phases {
		if len(phase.Tasks) > 0 {
			out = append(out, phase)
		}
	}
	return out
}
