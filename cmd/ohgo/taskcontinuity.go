package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

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
	// The artifact tool runs on the loop goroutine and the task layer has one
	// writer at a turn boundary. Buffer its records during the run and project
	// them through the ordinary task boundary instead of mutating task state
	// from inside a tool call.
	artifacts := attachArtifactSink(toolSet, controller)
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
		CompleteWithEvidence: func(evidence tui.CompletionEvidence) tui.TaskProjection {
			recorded, err := controller.CompleteWithEvidence(task.CompletionEvidence{
				Complete: evidence.Complete,
				Reason:   evidence.Reason,
				Answered: evidence.Answered,
			})
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
			detached, _, err := controller.Detach()
			if err != nil {
				return tui.TaskProjection{Err: err.Error()}
			}
			// A terminal task is archived, not resumable, but it is still a
			// real detachment the transcript must show. Only an empty ID means
			// there was no active task.
			if detached.ID == "" {
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
		ListArchived: func() []tui.TaskResume {
			tasks, err := controller.ArchivedTasks()
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
			if err != nil {
				return taskProjection(recorded, err)
			}
			// An archived task can carry an approved-but-unresolved transition.
			// The restored workspace is available at this boundary too, so
			// resolve it before the next turn instead of letting Begin guess.
			recorded, notice := reconcileHandoff(controller, recorded, role)
			if notice != "" {
				program.PublishNotice(notice)
			}
			return taskProjection(recorded, nil)
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
		RecordArtifacts:  artifacts.drain,
		HasCurrentChange: controller.HasCurrentChange,
		Evidence:         controller.Evidence,
		RecordProjection: controller.RecordProjection,
		ProjectionReview: func() string {
			report, ok := controller.LastProjection()
			if !ok {
				return ""
			}
			return report.EvidenceReview()
		},
		ProjectionProvenance: func() agent.EvidenceProvenance {
			report, ok := controller.LastProjection()
			if !ok {
				return agent.EvidenceProvenance{}
			}
			return report.Provenance
		},
		Audit: controller.Audit,
		Timeline: func(limit int) []tui.TimelineView {
			current, ok := controller.Current()
			if !ok {
				return nil
			}
			views := timelineViews(current.Timeline, limit)
			if dropped := current.TimelineDropped(); dropped > 0 {
				views = append([]tui.TimelineView{{
					Kind:    "timeline",
					Summary: fmt.Sprintf("%d older event(s) elided by the timeline bound", dropped),
				}}, views...)
			}
			return views
		},
		Lineage: func(id string) (tui.LineageView, []tui.LineageView, bool) {
			if _, ok := controller.Current(); !ok {
				return tui.LineageView{}, nil, false
			}
			if strings.TrimSpace(id) == "" {
				entries := controller.Lineage()
				if len(entries) > 40 {
					views := lineageViews(entries[:40])
					views = append(views, tui.LineageView{
						Kind: "lineage",
						ID:   "…",
						Text: fmt.Sprintf("%d more records; use /task lineage <id>", len(entries)-40),
					})
					return tui.LineageView{}, views, true
				}
				return tui.LineageView{}, lineageViews(entries), len(entries) > 0
			}
			focus, related, found := controller.TraceLineage(id)
			if !found {
				// A prefix or statement fragment is a convenience, not a
				// second index: resolve it against the same derived lineage.
				for _, entry := range controller.Lineage() {
					if strings.HasPrefix(strings.ToLower(entry.ID), strings.ToLower(id)) ||
						strings.Contains(strings.ToLower(entry.Text), strings.ToLower(id)) {
						focus, related, found = controller.TraceLineage(entry.ID)
						break
					}
				}
			}
			return lineageView(focus), lineageViews(related), found
		},
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
		// The recovery classification describes what the load could actually
		// restore. It is reported once, before any other recovery narrative, so
		// a reconstructed or blocked task is never mistaken for an intact one.
		if report := controller.Recovery(); !report.Empty() && report.State != task.RecoveryIntact {
			program.PublishNotice("task recovery: " + report.Label())
		}
		// Approval is not proof that the switch happened, and it is not proof
		// that it did not. Reconcile an approved record against the authority
		// that was actually restored before any recovery narrative is shown.
		// This is the only boundary where both facts are available; the task
		// data layer never reads the session store.
		handoffNotice := ""
		if recovered.Handoff != nil && recovered.Handoff.Status == task.HandoffApproved {
			recovered, handoffNotice = reconcileHandoff(controller, recovered, role)
			if handoffNotice != "" {
				program.PublishNotice(handoffNotice)
			}
		}
		if !recovered.Status.IsTerminal() {
			program.PublishTask(taskProjection(recovered, nil))
			if resumable, lerr := controller.ListResumable(); lerr == nil && len(resumable) > 0 {
				program.PublishNotice("1 resumable task available (see /task list)")
			}
		} else {
			// Listing is also what detects unreadable archives; a terminal live
			// task must not hide a lost detached one.
			_, _ = controller.ListResumable()
		}
		if problems := controller.ArchiveProblems(); len(problems) > 0 {
			program.PublishNotice(fmt.Sprintf(
				"%d detached task file(s) could not be read (see /task list)", len(problems)))
		}
		if recovered.Handoff != nil && recovered.Handoff.Status == task.HandoffInterrupted && handoffNotice == "" {
			// An unapproved proposal is closed at load. Report only what the
			// durable state establishes rather than asserting a workspace
			// movement the recovery never observed.
			notice := "interrupted handoff " + recovered.Handoff.From.Label() + " -> " + recovered.Handoff.To.Label()
			if recovered.Handoff.Failure != "" {
				notice += ": " + recovered.Handoff.Failure
			}
			program.PublishNotice(notice)
		}
	}
	return controller
}

// artifactBuffer captures artifacts recorded by the artifact tool during a
// run. The tool executes on the loop goroutine; the task layer is written at
// the turn boundary, so the sink only appends here and drain performs the
// durable projection once.
type artifactBuffer struct {
	mu         sync.Mutex
	controller *task.Controller
	pending    []workspace.Artifact
}

func (b *artifactBuffer) record(artifact workspace.Artifact) error {
	b.mu.Lock()
	b.pending = append(b.pending, artifact)
	b.mu.Unlock()
	return nil
}

// drain projects every buffered artifact into the task, preserving the first
// failure without dropping the rest of the batch.
func (b *artifactBuffer) drain() error {
	b.mu.Lock()
	batch := b.pending
	b.pending = nil
	b.mu.Unlock()
	if b.controller == nil {
		return nil
	}
	var first error
	for _, artifact := range batch {
		if err := b.controller.RecordArtifact(artifact); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// attachArtifactSink installs the task projection on the existing artifact
// tool. The session sink stays authoritative; this is an additive observer.
func attachArtifactSink(toolSet []agent.Tool, controller *task.Controller) *artifactBuffer {
	buffer := &artifactBuffer{controller: controller}
	for _, tool := range toolSet {
		artifact, ok := tool.(*tools.ArtifactTool)
		if !ok {
			continue
		}
		artifact.AddSink(buffer.record)
	}
	return buffer
}

// reconcileHandoff resolves an approved transition against the restored
// workspace. The controller owns the decision; this function only supplies the
// live authority and projects the truthful notice.
func reconcileHandoff(controller *task.Controller, current task.Task, role func() workspace.Workspace) (task.Task, string) {
	if current.Handoff == nil || current.Handoff.Status != task.HandoffApproved {
		return current, ""
	}
	live := workspace.Workspace("")
	if role != nil {
		live = role()
	}
	recovery, err := controller.ReconcileRecoveredHandoff(live)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: task handoff recovery: %v\n", err)
	}
	if updated, ok := controller.Current(); ok {
		current = updated
	}
	return current, recovery.Notice
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
	resume.ObjectiveState = string(recorded.ObjectiveState())
	resume.CompletionSummary = recorded.CompletionSummary()
	resume.Warning = recorded.ContextWarning
	if !recorded.Recovery.Empty() {
		resume.RecoveryState = string(recorded.Recovery.State)
		resume.RecoveryDetail = recorded.Recovery.Detail
	}
	if recorded.Handoff != nil {
		resume.HandoffStatus = string(recorded.Handoff.Status)
		resume.HandoffTo = recorded.Handoff.To.String()
		resume.HandoffBy = recorded.Handoff.ApprovedBy
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
		ID:             recorded.ID,
		Status:         string(recorded.Status),
		Objective:      recorded.Objective,
		ReviewReason:   recorded.ReviewReason,
		ObjectiveState: string(recorded.ObjectiveState()),
		Recovery:       recoveryLabel(recorded),
		Warning:        recorded.ContextWarning,
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

// recoveryLabel reports a non-intact recovery, or "" when the checkpoint was
// intact. An intact recovery is not repeated in every task block.
func recoveryLabel(recorded task.Task) string {
	if recorded.Recovery.Empty() || recorded.Recovery.State == task.RecoveryIntact {
		return ""
	}
	return recorded.Recovery.Label()
}

// timelineViews converts durable events into the TUI's read-only projection.
func timelineViews(events []task.TimelineEvent, limit int) []tui.TimelineView {
	if len(events) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	events = events[len(events)-limit:]
	out := make([]tui.TimelineView, 0, len(events))
	for _, event := range events {
		view := tui.TimelineView{
			Seq:     event.Seq,
			At:      event.At,
			Kind:    event.Kind.String(),
			Status:  event.Status,
			Summary: event.Summary,
			Refs:    append([]string(nil), event.Refs...),
		}
		if event.Role.Valid() {
			view.Role = event.Role.Label()
		}
		out = append(out, view)
	}
	return out
}

// lineageView converts one derived lineage entry into the TUI projection.
func lineageView(entry task.LineageEntry) tui.LineageView {
	view := tui.LineageView{
		Kind:           entry.Kind,
		ID:             entry.ID,
		Stage:          string(entry.Stage),
		Status:         entry.Status,
		Text:           entry.Text,
		Note:           entry.Note,
		Artifact:       entry.Artifact,
		EvidenceIDs:    append([]string(nil), entry.EvidenceIDs...),
		Contradictions: append([]string(nil), entry.Contradicts...),
		Missing:        append([]string(nil), entry.Missing...),
	}
	if entry.Role.Valid() {
		view.Role = entry.Role.Label()
	}
	return view
}

// lineageViews converts a bounded set of lineage entries.
func lineageViews(entries []task.LineageEntry) []tui.LineageView {
	out := make([]tui.LineageView, 0, len(entries))
	for _, entry := range entries {
		out = append(out, lineageView(entry))
	}
	return out
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
