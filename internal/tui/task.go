package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// Task state values the task domain projects. They are duplicated here as
// plain strings rather than imported so the seam stays a projection: the TUI
// renders a vocabulary, it does not depend on the task package's types.
const (
	taskStatePending = "pending"
	taskStateActive  = "active"
	taskStateDone    = "done"
)

// reviewPrefix is the terminal marker for a turn that stopped at an evidence
// boundary. It is the review counterpart of the completion recap: the answer
// remains above, and this line says why the runtime did not claim completion.
const reviewPrefix = "needs review:"

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
	// ReviewReason explains a NEEDS_REVIEW transition in evidence terms. It is
	// the durable reviewer-facing reason, not a model claim. Empty means the
	// mode has no review boundary or the projection does not carry one.
	ReviewReason string
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
	// ObjectiveState projects whether the objective is actually proven. It is
	// empty for a layer that does not report it.
	ObjectiveState string
	// Recovery classifies the last load when it could not restore everything.
	// Empty means the checkpoint was intact.
	Recovery string
	// Warning reports a continuity warning that did not prevent recovery, such
	// as a bounded log refusing a new record. It is diagnostic, never status.
	Warning string
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
	// Role is the execution role the task last ran under. It is continuity
	// metadata, never authority: the turn gate uses it to tell a role change
	// on the same objective from a genuinely new request.
	Role string
	// HandoffStatus and HandoffTo describe an approved transition awaiting its
	// turn boundary. An approved handoff is the human's explicit continuation
	// decision for this task, so the next turn attaches without re-asking for
	// a phrase. They are continuity metadata, never authority.
	HandoffStatus   string
	HandoffTo       string
	HandoffBy       string
	HandoffConsumed bool
	Contract        contract.IntentContract
	HasContract     bool
	Snapshot        *agent.TargetSnapshot
	// ObjectiveState is the truthful completion projection (PROVEN,
	// NEEDS_REVIEW, BLOCKED, ...). It is read-only.
	ObjectiveState string
	// CompletionSummary explains the objective state in observed terms.
	CompletionSummary string
	// RecoveryState and RecoveryDetail classify what the last load restored.
	// They are empty when the checkpoint was intact.
	RecoveryState  string
	RecoveryDetail string
	// Warning is a continuity warning that did not block recovery.
	Warning string
}

// TimelineView is the UI projection of one durable task event.
type TimelineView struct {
	Seq     int
	At      time.Time
	Kind    string
	Role    string
	Status  string
	Summary string
	Refs    []string
}

// LineageView is the UI projection of one conclusion or observation.
type LineageView struct {
	Kind           string
	ID             string
	Stage          string
	Status         string
	Role           string
	Text           string
	Note           string
	Artifact       string
	EvidenceIDs    []string
	Contradictions []string
	Missing        []string
}

// CompletionEvidence is the host's evaluated completion decision.
type CompletionEvidence struct {
	// Complete is the evaluator's verdict.
	Complete bool
	// Reason explains a review verdict.
	Reason string
	// Answered reports a non-empty answer for an informational objective.
	Answered bool
}

// HandoffView is the UI projection of one durable role transition.
//
// It is descriptive, never authority: the counts come from the task record and
// the status is the record's own disposition. The program never derives a role
// or a capability from it.
type HandoffView struct {
	// ID is the durable handoff identity. Empty means a preview that has not
	// been recorded yet.
	ID string
	// From and To are workspace labels, e.g. "FUZZ" and "BUILD".
	From string
	To   string
	// Status is the durable lifecycle position (PROPOSED, APPLIED, ...).
	Status string
	// Objective is the task objective the transition preserves.
	Objective string
	// Reason explains why the transition is needed.
	Reason string
	// Summary is the concise description the target role will see.
	Summary string
	// Workspace is the measured workspace state at proposal time, or "" when
	// the host could not measure it.
	Workspace string
	// Carried is the number of shared records the transition references.
	Facts       int
	Hypotheses  int
	Questions   int
	Evidence    int
	Artifacts   int
	Constraints int
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
	// CompleteWithEvidence records a successful end only when the durable
	// task carries the observations the objective requires. It is the Phase 3
	// completion path; Complete remains for hosts that predate proofs.
	CompleteWithEvidence func(CompletionEvidence) TaskProjection
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
	// ListArchived returns every archived task, oldest first, including
	// terminal history. It is read-only: listing never resumes or mutates.
	ListArchived func() []TaskResume
	// ResumeTask attaches one resumable task to the current session by ID, or
	// the latest when id is empty. It restores durable state and moves the
	// task to RUNNING.
	ResumeTask func(id string) TaskProjection
	// CancelResumable closes one resumable task by ID, or the latest when id
	// is empty. It never claims completion and the task stops being resumable.
	CancelResumable func(id string) TaskProjection
	// PreviewHandoff describes the transition that would be recorded for a
	// target role, without creating it. It is used to show what will be
	// carried before the human approves. Nil disables the preview, not the
	// transition.
	PreviewHandoff func(target workspace.Workspace) (HandoffView, error)
	// ProposeHandoff records a pending transition without moving authority.
	ProposeHandoff func(target workspace.Workspace, reason, summary, workspaceState string) (HandoffView, error)
	// ApproveHandoff records the human's authorization before the host moves
	// authority. It does not move authority itself.
	ApproveHandoff func(id string) (HandoffView, error)
	// FailHandoff records that an approved transition could not be applied.
	// It never changes the recorded role.
	FailHandoff func(id, reason string) error
	// Context returns deterministic orientation blocks for the current task.
	Context func() []string
	// ObserveTurns folds a completed run's tool outcomes into the durable
	// evidence state. It is called at the turn boundary so an interrupted or
	// final run is observed even if no later projection request ran.
	ObserveTurns func([]agent.Turn)
	// RecordArtifacts drains structured artifacts produced during the turn
	// into the durable task. It runs at the same turn boundary, before the
	// completion transition, so a finding can become a BUILD-visible
	// hypothesis on the next turn. Nil disables the projection.
	RecordArtifacts func() error
	// HasCurrentChange reports whether the active task already carries a
	// current observed change. The turn evaluator uses it to let a resumed
	// verification-only turn reach the durable completion boundary instead of
	// refusing it for observing no mutation in the current run. It is evidence
	// metadata, never authorization, and it never completes a task by itself.
	HasCurrentChange func() bool
	// Evidence returns the task's durable evidence state. The projector
	// updates it in place; the controller checkpoints it at lifecycle
	// boundaries. Nil disables task-shaped projection.
	Evidence func() *cognition.EvidenceState
	// RecordProjection stores the last projection audit for /audit.
	RecordProjection func(agent.ProjectionReport)
	// LastProjection returns the last projection report, if one exists. It is
	// read-only diagnostic state used by the execution-experience observer.
	LastProjection func() (agent.ProjectionReport, bool)
	// ProjectionReview returns the last projection's evidence review, or ""
	// when the runtime had enough evidence. It is a completion veto, never an
	// authorization.
	ProjectionReview func() string
	// ProjectionProvenance returns the last projection's evidence provenance.
	// Completion uses it to keep documentation claims distinct from observed
	// implementation facts. It is a completion veto, never an authorization.
	ProjectionProvenance func() agent.EvidenceProvenance
	// Audit returns the last projection audit, for /audit.
	Audit func() string
	// RunBudget returns the fresh root grant for one turn. The host installs
	// it on the loop and on the subagent spawner before execution, so every
	// descendant of the turn shares the same instance.
	RunBudget func() *budget.Budget
	// RecordBudget folds a finished run's consumption into the durable task
	// ledger. It does not reset the in-memory grant.
	RecordBudget func(snapshot budget.Snapshot)
	// Timeline returns the durable task events, newest last, bounded by limit.
	Timeline func(limit int) []TimelineView
	// Lineage returns one record and its related records, or every record when
	// id is empty. It is read-only.
	Lineage func(id string) (LineageView, []LineageView, bool)
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
	case "history":
		if p.task.ListArchived == nil {
			p.state.Apply(Notice{Text: "task history is unavailable"})
			return
		}
		archived := p.task.ListArchived()
		var terminal []TaskResume
		for _, r := range archived {
			if taskStatusTerminal(r.Status) {
				terminal = append(terminal, r)
			}
		}
		if len(terminal) == 0 {
			p.state.Apply(Notice{Text: "no completed task history"})
			return
		}
		var sb strings.Builder
		sb.WriteString("task history (newest last):")
		for i := len(terminal) - 1; i >= 0; i-- {
			r := terminal[i]
			fmt.Fprintf(&sb, "\n  %s %s: %s", r.Status, shortTaskID(r.ID), r.Objective)
			if state := strings.TrimSpace(r.ObjectiveState); state != "" {
				sb.WriteString(" (" + state + ")")
			}
		}
		p.state.Apply(Notice{Text: sb.String()})
		return
	case "show":
		target := strings.Join(rest, " ")
		resume, ok := p.selectTask(target)
		if !ok {
			return
		}
		p.state.Apply(Notice{Text: taskStatusText(resume, true)})
		return
	case "timeline":
		if p.task.Timeline == nil {
			p.state.Apply(Notice{Text: "task timeline is unavailable"})
			return
		}
		limit := 20
		if len(rest) > 0 {
			if parsed, err := parsePositiveInt(rest[0]); err == nil {
				limit = parsed
			}
		}
		if limit > 100 {
			limit = 100
		}
		events := p.task.Timeline(limit)
		if len(events) == 0 {
			p.state.Apply(Notice{Text: "no task events recorded"})
			return
		}
		var sb strings.Builder
		sb.WriteString("task timeline (newest last):")
		for _, event := range events {
			sb.WriteString("\n")
			sb.WriteString(formatTimelineEvent(event))
		}
		p.state.Apply(Notice{Text: sb.String()})
		return
	case "lineage", "trace", "evidence":
		if p.task.Lineage == nil {
			p.state.Apply(Notice{Text: "task lineage is unavailable"})
			return
		}
		target := strings.Join(rest, " ")
		focus, related, ok := p.task.Lineage(target)
		if !ok {
			if target == "" {
				p.state.Apply(Notice{Text: "no conclusions or observations recorded"})
			} else {
				p.state.Apply(Notice{Text: "no task record matches " + target})
			}
			return
		}
		var sb strings.Builder
		if focus.ID != "" {
			sb.WriteString(formatLineageEntry(focus))
			if len(related) > 0 {
				sb.WriteString("\nrelated:")
			}
		} else {
			sb.WriteString("task lineage:")
		}
		for _, entry := range related {
			sb.WriteString("\n")
			sb.WriteString(formatLineageEntry(entry))
		}
		p.state.Apply(Notice{Text: sb.String()})
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
		p.state.Apply(Notice{Text: taskStatusText(current, false)})
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
		p.state.Apply(Notice{Text: "usage: /task [status|list|history|show [id]|timeline [n]|lineage [id]|resume [id]|cancel [id]|accept]"})
	}
}

// taskStatusText renders the read-only status view. It names the objective
// state and the recovery classification only when they add information the
// lifecycle status does not already carry.
func taskStatusText(task TaskResume, includeID bool) string {
	var sb strings.Builder
	sb.WriteString("task ")
	sb.WriteString(task.Status)
	if task.ObjectiveState != "" && task.ObjectiveState != "ACTIVE" {
		sb.WriteString(" · ")
		sb.WriteString(strings.ToUpper(task.ObjectiveState))
	}
	sb.WriteString(": ")
	sb.WriteString(task.Objective)
	if task.ReviewReason != "" && strings.EqualFold(task.Status, "NEEDS_REVIEW") {
		sb.WriteString("\nneeds review: ")
		sb.WriteString(task.ReviewReason)
	}
	if task.CompletionSummary != "" {
		switch task.ObjectiveState {
		case "PROVEN", "COMPLETED_UNPROVEN", "BLOCKED":
			sb.WriteString("\n")
			sb.WriteString(task.CompletionSummary)
		}
	}
	if task.RecoveryState != "" && task.RecoveryState != "INTACT" {
		sb.WriteString("\nrecovery: ")
		sb.WriteString(task.RecoveryState)
		if task.RecoveryDetail != "" {
			sb.WriteString(" — ")
			sb.WriteString(task.RecoveryDetail)
		}
	}
	if task.Warning != "" {
		sb.WriteString("\ncontinuity: ")
		sb.WriteString(task.Warning)
	}
	if includeID {
		sb.WriteString("\nid: ")
		sb.WriteString(task.ID)
	}
	return sb.String()
}

// formatTimelineEvent renders one durable event without its payload.
func formatTimelineEvent(event TimelineView) string {
	when := ""
	if !event.At.IsZero() {
		when = event.At.Local().Format("15:04:05") + " "
	}
	role := ""
	if event.Role != "" {
		role = event.Role + " "
	}
	status := ""
	if event.Status != "" {
		status = " [" + event.Status + "]"
	}
	refs := ""
	if len(event.Refs) > 0 {
		refs = " (" + strings.Join(event.Refs, ",") + ")"
	}
	return fmt.Sprintf("  %d %s%s%s%s: %s%s", event.Seq, when, role, event.Kind, status, event.Summary, refs)
}

// formatLineageEntry renders one conclusion or observation with its stage.
func formatLineageEntry(entry LineageView) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %s#%s [%s/%s]", entry.Kind, entry.ID, entry.Stage, entry.Status)
	if entry.Role != "" {
		sb.WriteString(" ")
		sb.WriteString(entry.Role)
	}
	if entry.Text != "" {
		sb.WriteString(": ")
		sb.WriteString(entry.Text)
	}
	if len(entry.EvidenceIDs) > 0 {
		sb.WriteString(" evidence:")
		sb.WriteString(strings.Join(entry.EvidenceIDs, ","))
	}
	if entry.Artifact != "" {
		sb.WriteString(" artifact:")
		sb.WriteString(entry.Artifact)
	}
	if len(entry.Contradictions) > 0 {
		sb.WriteString(" contradicts:")
		sb.WriteString(strings.Join(entry.Contradictions, ","))
	}
	if len(entry.Missing) > 0 {
		sb.WriteString(" missing:")
		sb.WriteString(strings.Join(entry.Missing, ","))
	}
	if entry.Note != "" {
		sb.WriteString(" — ")
		sb.WriteString(entry.Note)
	}
	return sb.String()
}

// parsePositiveInt parses a bounded positive integer argument.
func parsePositiveInt(text string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return n, nil
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
	// Resumable work is the common case. Only read the archive when the query
	// does not resolve there, so an ordinary /task show never scans history.
	if match, ok := matchTaskResume(p.task.ListResumable(), query); ok {
		return match, true
	}
	if p.task.ListArchived != nil {
		// Terminal work is archived, not resumable, but it remains inspectable.
		// Searching history here keeps /task show truthful for completed tasks.
		if match, ok := matchTaskResume(p.task.ListArchived(), query); ok {
			return match, true
		}
	}
	if query == "" {
		p.state.Apply(Notice{Text: "no task is active"})
	} else {
		p.state.Apply(Notice{Text: "no task matches " + query})
	}
	return TaskResume{}, false
}

// matchTaskResume finds one task by exact/prefix ID, then objective substring.
// An empty query selects the newest candidate. It is a pure projection helper:
// it never mutates a task or changes which task is active.
func matchTaskResume(candidates []TaskResume, query string) (TaskResume, bool) {
	if len(candidates) == 0 {
		return TaskResume{}, false
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return candidates[len(candidates)-1], true
	}
	for _, r := range candidates {
		if strings.EqualFold(r.ID, query) || strings.HasPrefix(strings.ToLower(r.ID), strings.ToLower(query)) {
			return r, true
		}
	}
	lowered := strings.ToLower(query)
	for _, r := range candidates {
		if strings.Contains(strings.ToLower(r.Objective), lowered) {
			return r, true
		}
	}
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

// objectiveContinuation reports whether this turn repeats the open task's own
// objective with the same completion contract.
//
// An exact repeat is another turn on the work already in flight, not a second
// task: detaching would silently split the evidence, timeline, and proof across
// two identities. The rule is deliberately narrower than semantic similarity —
// only the same normalized objective and the same contract kind continue.
// Anything else still starts a new task.
func objectiveContinuation(current TaskResume, next contract.IntentContract) bool {
	if !current.HasContract {
		return false
	}
	persisted := strings.TrimSpace(current.Contract.Objective)
	requested := strings.TrimSpace(next.Objective)
	if persisted == "" || !strings.EqualFold(persisted, requested) {
		return false
	}
	return current.Contract.Kind == next.Kind
}

// roleContinuation reports whether this turn continues the current task
// because the execution role changed while the objective did not.
//
// Prompt phrasing alone is not identity: a user who switches from BUILD to
// FUZZ and repeats the same objective is continuing one task, not starting a
// second one. A different objective still starts a new task, and an unchanged
// role still requires an explicit continuation prompt, so neither unrelated
// prompts nor repeated work attach themselves to the old task.
func roleContinuation(current TaskResume, next contract.IntentContract, active string) bool {
	if !current.HasContract || current.Role == "" || active == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(current.Role), strings.TrimSpace(active)) {
		return false
	}
	persisted := strings.TrimSpace(current.Contract.Objective)
	requested := strings.TrimSpace(next.Objective)
	return persisted != "" && persisted == requested
}

// handoffContinuation reports whether an approved or automatically applied
// handoff owns this turn.
//
// A human approval is itself the continuation decision for the task. An
// automatic control-plane handoff is narrower: it attaches the next prompt only
// when the prompt continues the same objective or refers to the task's own
// result ("fix it"). After the handoff is consumed, later turns use the
// ordinary continuation policy.
func handoffContinuation(current TaskResume, active, text string, next contract.IntentContract) bool {
	// APPROVED is a proposal awaiting its turn boundary. APPLIED but not
	// consumed is an already-applied workspace command awaiting its next turn.
	pending := current.HandoffStatus == "APPROVED" ||
		(current.HandoffStatus == "APPLIED" && !current.HandoffConsumed)
	if !pending || current.HandoffTo == "" || active == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(current.HandoffTo), strings.TrimSpace(active)) {
		return false
	}
	// A human switch is itself a continuation decision for this task. An
	// automatic control-plane switch is not: it preserves the task only when
	// this prompt continues the same objective or refers to its own result.
	// Otherwise an unrelated new objective would inherit the old contract.
	if strings.EqualFold(strings.TrimSpace(current.HandoffBy), "control-plane") {
		return objectiveContinuation(current, next) || taskFollowUp(current, text)
	}
	return true
}

// continuationPrompt recognizes a deliberate continuation of the open task.
//
// The vocabulary lives in the workspace request-classification package because
// the router must apply the same rule before it can propose a role change. It
// is an explicit-intent classifier, not semantic similarity: only language
// that names continuation resumes a resumable task. A new intent — even one
// about the same repository or the same files — is never a continuation merely
// because it is related. The hard safety rule is unchanged: completion still
// uses the persisted contract, this only decides attachment.
func continuationPrompt(text string) bool {
	return workspace.IsContinuation(text)
}

// verificationContinuation recognizes a natural request to check the open
// task's own result ("check whether the fix worked"). It is not a new READ
// objective: the task already owns the change and its verification contract.
//
// Only an execution or mutation task has a result to verify. An informational
// task keeps the ordinary new-intent rule, so "check the parser" cannot attach
// itself to an unrelated task merely because the wording resembles a check.
func verificationContinuation(current TaskResume, text string) bool {
	if !current.HasContract {
		return false
	}
	switch current.Contract.Kind {
	case contract.ObjectiveMutation, contract.ObjectiveExecution:
	default:
		return false
	}
	return workspace.IsVerificationContinuation(text)
}

// taskFollowUp recognizes a short request whose object is the open task's own
// result ("fix it", "implement the fix"). Only a task that can own a result
// (mutation or execution) may claim one; an informational task does not become
// a mutation task because the user said "fix it".
func taskFollowUp(current TaskResume, text string) bool {
	if !current.HasContract {
		return false
	}
	switch current.Contract.Kind {
	case contract.ObjectiveMutation, contract.ObjectiveExecution:
	default:
		return false
	}
	return workspace.IsTaskFollowUp(text)
}

// implementationRefinement reports whether an open mutation task owns a delta
// turn that states no capability of its own.
//
// A BUILD objective is an evolving unit of work: after a change is applied and
// before it is proven, the user often answers with a refinement ("I want to use
// green and black", "make it green", "make the buttons bigger") instead of
// repeating the objective. Such a statement carries no verb the classifier can
// place, so classifying it in isolation falls back to READ and builds an
// informational contract, which withholds the write surface from a BUILD task
// that is still executing. The delta therefore inherits the open mutation
// contract and its evidence.
//
// The rule is not "sticky BUILD", and it is not a general similarity rule. An
// explicit mutation prompt is a new mutation objective: it still reaches BUILD
// and the write surface, but it does not impersonate the open objective. An
// explicit read or investigation request states a different authority class; a
// whole-repository no-mutation limit vetoes attachment. Only a delta with no
// capability signal of its own, against an open mutation contract under BUILD,
// is a refinement.
func implementationRefinement(current TaskResume, text, active string, next contract.IntentContract) bool {
	if !current.HasContract || strings.TrimSpace(text) == "" {
		return false
	}
	if current.Contract.Kind != contract.ObjectiveMutation {
		return false
	}
	if role, err := workspace.Parse(active); err != nil || role != workspace.Build {
		return false
	}
	// The delta must be the READ fallback, not a request with an effect. A
	// mutation, execution, read, or investigation action chooses the turn's
	// own contract and is handled by the ordinary objective/authority policy.
	if next.Kind != contract.ObjectiveInformational {
		return false
	}
	if next.Grounding != nil && len(next.Grounding.Actions) > 0 {
		return false
	}
	if hasNoMutationConstraint(next) {
		return false
	}
	return true
}

// hasNoMutationConstraint reports whether the delta itself forbids repository
// mutation. That limit is an authority constraint, not a refinement.
func hasNoMutationConstraint(next contract.IntentContract) bool {
	if next.Grounding == nil {
		return false
	}
	for _, constraint := range next.Grounding.Constraints {
		if constraint == "no_mutation" {
			return true
		}
	}
	return false
}

// taskTurnDecision reports which continuation rule owns the prompt, if any.
// The task layer makes one decision; routing, prediction, and attachment all
// consume it instead of each classifying the text separately.
func taskTurnDecision(current TaskResume, text, active string, next contract.IntentContract) (string, bool) {
	switch {
	case continuationPrompt(text):
		return "EXPLICIT_RESUME", true
	case handoffContinuation(current, active, text, next):
		return "HANDOFF_CONTINUATION", true
	case verificationContinuation(current, text):
		return "VERIFICATION_CONTINUATION", true
	case taskFollowUp(current, text):
		return "TASK_FOLLOW_UP", true
	case roleContinuation(current, next, active):
		return "ROLE_CONTINUATION", true
	case objectiveContinuation(current, next):
		return "OBJECTIVE_CONTINUATION", true
	case implementationRefinement(current, text, active, next):
		return "IMPLEMENTATION_REFINEMENT", true
	default:
		return "", false
	}
}

// taskIntentLabel projects the persisted contract as the turn's semantic
// intent. A continuation runs the task's work, not the new prompt's surface
// verb, so "check whether the fix worked" is an EXECUTE turn inside BUILD.
func taskIntentLabel(current TaskResume) string {
	if current.HasContract {
		switch current.Contract.Kind {
		case contract.ObjectiveMutation, contract.ObjectiveExecution:
			return "EXECUTE"
		}
	}
	return "READ"
}

// terminalTaskContinuation reports whether a continuation names a task that
// has already finished. It is a refusal, not a new task: a proven objective
// must terminate instead of being archived and re-run merely because the user
// repeated continuation vocabulary.
func (p *Program) terminalTaskContinuation(text string) (TaskResume, bool) {
	if p.task.Current == nil {
		return TaskResume{}, false
	}
	current, ok := p.task.Current()
	if !ok || current.ID == "" || !taskStatusTerminal(current.Status) {
		return TaskResume{}, false
	}
	if continuationPrompt(text) || verificationContinuation(current, text) || taskFollowUp(current, text) {
		return current, true
	}
	return TaskResume{}, false
}

// terminalTaskNotice explains why a continuation of a finished task is refused
// without inventing a new objective.
func terminalTaskNotice(current TaskResume) string {
	if strings.EqualFold(strings.TrimSpace(current.ObjectiveState), "PROVEN") {
		return "task already proven; no further verification is needed"
	}
	status := strings.ToLower(strings.TrimSpace(current.Status))
	if status == "" {
		status = "finished"
	}
	return "task already " + status + "; start a new objective to continue"
}

// openTask returns the active, non-terminal task that may own the next turn.
// A terminal task is archived by the turn boundary, not continued by it.
func (p *Program) openTask() (TaskResume, bool) {
	if p.task.Current == nil {
		return TaskResume{}, false
	}
	current, ok := p.task.Current()
	if !ok || current.ID == "" || taskStatusTerminal(current.Status) {
		return TaskResume{}, false
	}
	return current, true
}

// taskContinuationFor resolves task ownership for one prompt under the given
// authority. It is side-effect-free; the turn boundary records the decision
// only after this has determined that the task owns the prompt.
func (p *Program) taskContinuationFor(text, active string) (TaskResume, string, bool) {
	current, ok := p.openTask()
	if !ok {
		return TaskResume{}, "", false
	}
	decision, owned := taskTurnDecision(current, text, active, workspace.BuildIntentContract(text))
	return current, decision, owned
}

// continuesOpenTask reports whether the open task already owns the prompt in a
// way that must bypass workspace prediction.
//
// Only continuations whose wording carries no capability signal bypass
// prediction: an explicit "continue", a verification of the task's own result,
// an implementation refinement of the open task's own object, or a handoff the
// human already decided. An exact repeat of the objective still states what the
// turn needs, so the control plane must be allowed to route it: a BUILD task
// resubmitted with an investigation objective has to reach FUZZ instead of
// silently staying in BUILD merely because the task is open. A task follow-up
// ("fix it") is predicted for the same reason.
func (p *Program) continuesOpenTask(text string) bool {
	active := ""
	if p.workspace.Current != nil {
		active = strings.ToLower(strings.TrimSpace(p.workspace.Current()))
	}
	_, decision, owned := p.taskContinuationFor(text, active)
	if !owned {
		return false
	}
	switch decision {
	case "EXPLICIT_RESUME", "HANDOFF_CONTINUATION", "VERIFICATION_CONTINUATION", "IMPLEMENTATION_REFINEMENT":
		return true
	default:
		return false
	}
}

// latestTaskProjection returns the most recent durable task projection, or
// nil when the session has none. Apply keeps the pointer cached so the sticky
// tracker never scans a long transcript; hand-built fixtures fall back to one
// reverse scan and then cache the result.
func latestTaskProjection(s *State) *TaskProjection {
	if s == nil {
		return nil
	}
	if s.task != nil && s.task.ID != "" {
		return s.task
	}
	if s.taskKnown {
		return nil
	}
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == EntryTask && s.Entries[i].Task != nil && s.Entries[i].Task.ID != "" {
			s.task = s.Entries[i].Task
			s.taskKnown = true
			return s.task
		}
	}
	s.taskKnown = true
	return nil
}

// activeTaskProjection returns the latest task while it still owns a plan.
//
// The task rail is transient chrome, not a transcript for every request. It
// appears only when three facts hold: the preference is enabled, the task is
// still open, and the task has a todo plan. The rail closes on the terminal
// transition (COMPLETED, FAILED, CANCELLED), not when the last todo is marked
// done: the final answer still belongs to the task and must keep it anchored
// while the transcript scrolls. An ordinary question with no plan keeps the
// full-width transcript; a terminal projection settles back into history
// rather than staying pinned beside every later turn.
func activeTaskProjection(s *State) *TaskProjection {
	if s == nil || s.TaskRailDisabled {
		return nil
	}
	task := latestTaskProjection(s)
	if task == nil || taskStatusTerminal(task.Status) || len(task.Items) == 0 {
		return nil
	}
	return task
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
	if state := strings.ToUpper(strings.TrimSpace(projection.ObjectiveState)); state == "PROVEN" || state == "COMPLETED_UNPROVEN" {
		header += "  · " + strings.ToLower(state)
	}
	// A review is a stop that needs a human: the header and its reason take
	// the warning role so the row cannot hide among muted telemetry. Every
	// other task state stays in the quiet notice role.
	headerStyle := pal.Notice
	needsReview := strings.EqualFold(projection.Status, "needs_review")
	if needsReview {
		headerStyle = pal.Warning
	}
	lines := []string{headerStyle.Render(fit(header))}

	// A review is waiting on a human decision, so the reason is the most
	// useful row. Render it wrapped instead of leaving the user with an
	// unexplained status; the "no todos" placeholder would only crowd it out.
	review := strings.TrimSpace(projection.ReviewReason)
	if review != "" && strings.EqualFold(projection.Status, "needs_review") {
		const prefix = "  ⎿ "
		avail := width
		if avail > 0 {
			avail -= len(prefix)
			if avail < 20 {
				avail = width
			}
		}
		wrapped := WrapToWidth(sanitizeInline(review), avail)
		for i, line := range wrapped {
			if i == 0 {
				line = prefix + line
			} else {
				line = "    " + line
			}
			lines = append(lines, headerStyle.Render(fit(line)))
		}
	} else if len(projection.Items) == 0 {
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
	if recovery := strings.TrimSpace(projection.Recovery); recovery != "" {
		lines = append(lines, pal.Status.Render(fit("  recovery: "+recovery)))
	}
	if warning := strings.TrimSpace(projection.Warning); warning != "" {
		lines = append(lines, pal.Status.Render(fit("  ! "+warning)))
	}
	return lines
}
