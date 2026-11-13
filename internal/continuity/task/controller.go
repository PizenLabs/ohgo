package task

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// Source supplies the current todo snapshot from the runtime's single todo
// owner. The task package does not own todo mutation: it records the durable
// snapshot so an interrupted task can be oriented on resume.
type Source func() []Todo

// Controller is the task continuity layer around the existing execution engine.
//
// It is the single owner of Task state. It observes lifecycle boundaries the
// turn orchestrator reports (a turn beginning, a tool boundary, an
// interruption, completion, failure) and records them durably. It never
// executes anything and never grants authority: it is bookkeeping, and the loop
// remains the execution owner.
type Controller struct {
	store  *Store
	source Source
	// resetSource clears the session-scoped planning state the source reads.
	// The task package does not own that state; it invokes the owner exactly at
	// a new-task boundary so an unrelated objective cannot inherit the previous
	// task's todo list. Nil means the host has no plan owner to reset.
	resetSource func()
	// planStale records that a task was detached and the session plan now
	// belongs to that archived task. The next new task clears it before the
	// plan can be read or snapshotted; a first task, a resume, or a restored
	// checkpoint leaves it false so a legitimate current plan is preserved.
	planStale bool
	now       func() time.Time

	task       Task
	lastAction string
	restored   bool
	// resuming reports that the current turn attached to a task that was
	// paused, pending, waiting for review, or explicitly resumed. It is
	// runtime-only orientation for the context projection; it never changes
	// status or authority. resumePending is consumed by the next Begin, so an
	// explicit /task resume reaches exactly one turn.
	resuming      bool
	resumePending bool
	// recovery is the classification of the last load. It is derived and
	// never persisted: it tells the user what the recovery actually restored.
	recovery RecoveryReport

	// role is the execution role active at the last turn boundary. It is a
	// context label, never authority: it decides which role context is
	// assembled and written, not what any tool may do.
	role workspace.Workspace
	// roleKnown distinguishes "the task has run under a recorded role" from
	// the pre-Phase-1 BUILD default. Continuation across a role change must
	// not infer a role from a default that was never observed.
	roleKnown bool
	// roleSource reads the live execution role at a turn boundary. It is a
	// function field so the controller does not import the host's state.
	roleSource func() workspace.Workspace

	// budgetLimits is the grant a new task starts under. The controller owns
	// the durable ledger, never enforcement: the root loop and its
	// descendants share one budget instance per run.
	budgetLimits budget.Limits
	budgetState  *budget.State

	// evidence is the durable knowledge acquired during the task. The
	// projector updates it in place; persist checkpoints a bounded copy.
	evidence *cognition.EvidenceState
	// audit is the last rendered projection, for the debug surface. It is
	// diagnostic text, never authority. auditMu guards it because the loop
	// records it on the turn goroutine while /audit reads it on the update
	// goroutine.
	auditMu    sync.Mutex
	audit      string
	lastReport agent.ProjectionReport
	hasReport  bool
}

// NewController returns a controller backed by a store.
//
// source may be nil, in which case the controller records no todo snapshot.
func NewController(store *Store, source Source) *Controller {
	return &Controller{
		store:    store,
		source:   source,
		now:      time.Now,
		evidence: cognition.NewEvidenceState(),
		role:     workspace.Build,
	}
}

// SetRoleSource installs the live execution-role reader. The controller reads
// it at a turn boundary and when assembling context; it never changes
// authority and a nil source keeps the pre-Phase-1 BUILD default.
func (c *Controller) SetRoleSource(source func() workspace.Workspace) {
	c.roleSource = source
}

// SetTodoResetter installs the callback that clears the runtime's
// session-scoped todo list when a new task starts.
//
// The todo tool remains the single owner of todo mutation. The controller only
// invokes the boundary reset: a session list can outlive a turn, but it must
// not become the next objective's plan or be imported into its checkpoint. A
// resumed task keeps its durable snapshot and is not reset.
func (c *Controller) SetTodoResetter(reset func()) {
	c.resetSource = reset
}

// CurrentRole returns the role currently used for context assembly.
func (c *Controller) CurrentRole() workspace.Workspace {
	return c.currentRole()
}

// LastRole returns the role the task last executed under, or "" when no role
// has been recorded. It is continuity metadata, never authority: the host uses
// it to distinguish a role change from a new objective.
func (c *Controller) LastRole() workspace.Workspace {
	if !c.roleKnown {
		return ""
	}
	return c.role
}

// currentRole resolves the active role without ever widening it. An unresolved
// source is reported as unresolved rather than silently defaulting to BUILD.
func (c *Controller) currentRole() workspace.Workspace {
	if c.roleSource != nil {
		if role := c.roleSource(); role.Valid() {
			return role
		}
		return ""
	}
	if c.role.Valid() {
		return c.role
	}
	return ""
}

// Store returns the store the controller writes through.
func (c *Controller) Store() *Store { return c.store }

// SetBudgetLimits records the grant new tasks start under. It does not touch
// the current task's ledger: a resume continues the consumption already
// recorded rather than resetting it.
func (c *Controller) SetBudgetLimits(limits budget.Limits) {
	c.budgetLimits = limits
	if c.budgetState == nil {
		c.budgetState = &budget.State{Limits: limits}
		return
	}
	// The user may deliberately resume with a larger grant. Keep the
	// cumulative usage; replace only the limits.
	c.budgetState.Limits = limits
}

// RunBudget returns the fresh root grant for one execution tree.
//
// A new run is an explicit user invocation, not a continuation inside a tree:
// descendants all share this instance, and none of them can replace it. The
// task ledger still accumulates every run's consumption, so resuming never
// erases what the previous run spent.
func (c *Controller) RunBudget() *budget.Budget {
	return budget.New(c.budgetLimits)
}

// RecordBudget folds one completed run's consumption into the task ledger.
func (c *Controller) RecordBudget(snapshot budget.Snapshot) {
	if c.budgetState == nil {
		c.budgetState = &budget.State{Limits: snapshot.Limits}
	}
	c.budgetState.Limits = snapshot.Limits
	c.budgetState.Used = c.budgetState.Used.Add(snapshot.Used)
	c.budgetState.UpdatedAt = c.now().UTC()
}

// BudgetState returns the durable ledger, if one exists.
func (c *Controller) BudgetState() (budget.State, bool) {
	if c.budgetState == nil {
		return budget.State{}, false
	}
	return *c.budgetState, true
}

// Evidence returns the durable evidence state for this task.
//
// The projector updates the returned state in place; the controller
// checkpoints a bounded copy at the next lifecycle boundary. The pointer is
// stable for the life of the task, so a projector created at a turn boundary
// keeps writing to the same knowledge.
func (c *Controller) Evidence() *cognition.EvidenceState {
	if c.evidence == nil {
		c.evidence = cognition.NewEvidenceState()
	}
	return c.evidence
}

// RecordProjection stores the last projection audit.
//
// It is diagnostic state. A malformed or missing audit never changes evidence,
// authority, or completion; it only makes the runtime harder to inspect.
func (c *Controller) RecordProjection(report agent.ProjectionReport) {
	text := cognition.RenderAudit(report)
	c.auditMu.Lock()
	c.audit = text
	c.lastReport = report
	c.hasReport = true
	c.auditMu.Unlock()
}

// LastProjection returns the last projection report and whether one exists.
//
// It is evidence diagnostics, not authority: a caller may use the report's
// sufficiency verdict to veto a false completion claim, but cannot use it to
// authorize an action.
func (c *Controller) LastProjection() (agent.ProjectionReport, bool) {
	c.auditMu.Lock()
	defer c.auditMu.Unlock()
	return c.lastReport, c.hasReport
}

// Audit returns the last projection audit for the debug surface.
func (c *Controller) Audit() string {
	c.auditMu.Lock()
	text := c.audit
	c.auditMu.Unlock()
	if text == "" {
		return "no context projection has run for this task"
	}
	return text
}

// Current returns the task in memory, if any.
func (c *Controller) Current() (Task, bool) {
	if c.task.ID == "" {
		return Task{}, false
	}
	return c.task.Clone(), true
}

// Restore loads any checkpoint and normalizes it for resume.
//
// A task left RUNNING on disk is restored as PAUSED: the process stopped
// without a terminal transition, so the runtime has no evidence the objective
// was reached. Restore never rewrites the files itself; the next recorded
// transition persists the normalized status.
//
// An approved handoff is returned undecided. Restore only knows durable state,
// not the workspace that was actually restored, so the caller that knows both
// must call ReconcileRecoveredHandoff before reporting the transition.
func (c *Controller) Restore() (Task, bool, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, false, err
	}
	if c.task.ID == "" {
		return Task{}, false, nil
	}
	c.reconcileStaleState()
	return c.task.Clone(), true, nil
}

// Recovery reports how the last load classified the restored task. It is empty
// before a load and never persisted.
func (c *Controller) Recovery() RecoveryReport {
	return c.recovery
}

// reconcileStaleState removes transient state that contradicts the current
// objective before any of it reaches the model.
//
// A completion proof on a non-terminal task is impossible in a valid
// checkpoint; keeping it would present completion=achieved while work is still
// running. A completion contract is derived deterministically from the user's
// objective, so an older projection can be stale after a classifier change;
// re-deriving it from the same objective is a truthful repair, not a new
// grant. Authority still decides what the turn may execute.
func (c *Controller) reconcileStaleState() bool {
	if c.task.ID == "" {
		return false
	}
	changed := false
	if !c.task.Status.IsTerminal() && c.task.Completion != nil {
		c.task.Completion = nil
		c.task.AppendEvent(TimelineEvent{
			At:      c.now().UTC(),
			Kind:    TimelineCheckpoint,
			Role:    c.role,
			Status:  string(c.task.Status),
			Summary: "discarded a completion proof attached to a non-terminal task",
		})
		changed = true
	}
	if c.task.Contract != nil && strings.TrimSpace(c.task.Objective) != "" {
		current := workspace.BuildIntentContract(c.task.Objective)
		// Only a stale informational projection of an explicit service
		// execution is reconciled. A host or test may deliberately persist a
		// narrower contract for the same text; rewriting every differing kind
		// would make the loader a second contract authority. The observed
		// stale state is narrower than that: an old classifier read "use npx
		// live-server…" as READ and then presented execution as forbidden.
		staleServiceExecution := c.task.Contract.Kind == contract.ObjectiveInformational &&
			current.Kind == contract.ObjectiveExecution &&
			workspace.RequestsServiceExecution(c.task.Objective)
		if staleServiceExecution {
			cloned := cloneContract(current)
			c.task.Contract = &cloned
			c.task.AppendEvent(TimelineEvent{
				At:      c.now().UTC(),
				Kind:    TimelineCheckpoint,
				Role:    c.role,
				Status:  string(c.task.Status),
				Summary: "completion contract re-derived from the objective because the checkpoint was stale",
			})
			changed = true
		}
		// An execution contract written before the runtime requirement existed
		// classified "run this project" as any other execution, so a finished
		// environment probe could support a completion proof. Re-deriving the
		// mode from the same objective makes that proof stop resolving, which
		// reports COMPLETED_UNPROVEN instead of a false PROVEN. Only the mode is
		// upgraded: the contract's targets and verification stay as recorded.
		if c.task.Contract.Kind == contract.ObjectiveExecution &&
			current.Kind == contract.ObjectiveExecution &&
			current.ExecutionMode == contract.ExecutionRuntime &&
			c.task.Contract.ExecutionMode != contract.ExecutionRuntime {
			c.task.Contract.ExecutionMode = contract.ExecutionRuntime
			c.task.AppendEvent(TimelineEvent{
				At:      c.now().UTC(),
				Kind:    TimelineCheckpoint,
				Role:    c.role,
				Status:  string(c.task.Status),
				Summary: "execution contract now requires a live runtime observation; a command-only proof no longer resolves",
			})
			changed = true
		}
	}
	if !changed {
		return false
	}
	if c.recovery.Empty() {
		c.recovery = RecoveryReport{State: RecoveryIntact}
	}
	if c.recovery.State == RecoveryIntact {
		c.recovery.State = RecoveryReconstructed
	}
	c.recovery.Detail = firstNonEmpty(c.recovery.Detail,
		"stale completion state was reconciled from the objective")
	const missing = "stale transient state"
	found := false
	for _, item := range c.recovery.Missing {
		if item == missing {
			found = true
			break
		}
	}
	if !found {
		c.recovery.Missing = append(c.recovery.Missing, missing)
	}
	return true
}

// Begin records that a turn is starting with this contract and pre-run target
// evidence.
//
// It continues the current task when one is resumable (a paused task resumes
// with its identity intact) and creates a new one when there is none or when
// the previous task is terminal. It never turns an interruption into a fresh,
// unrelated task.
//
// A non-terminal task's contract is immutable. The incoming contract must be
// the one already persisted; a different objective is refused with
// ErrObjectiveConflict so the caller must ask the human to continue or abandon
// the task explicitly. The incoming snapshot is used only when the task is new:
// a resume keeps the original pre-run baseline, because recapturing an already
// changed file would erase the evidence the contract was built on.
func (c *Controller) Begin(intent contract.IntentContract, snapshot *agent.TargetSnapshot) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	before := c.snapshotState()
	objective := strings.TrimSpace(intent.Objective)
	previousStatus := c.task.Status
	hadTask := c.task.ID != ""

	now := c.now().UTC()
	c.role = c.currentRole()
	c.roleKnown = c.role.Valid()

	// adopted reports that this boundary superseded the task's objective with
	// the one the human approved. The open-task contract check below uses it to
	// allow the incoming contract to replace the persisted one: the approval was
	// for this objective, so refusing its contract would run the previous task's
	// work under the new authority.
	adopted := false

	// An approved handoff is applied at the turn boundary, where the live role
	// is known and the task is being attached. Applying it here keeps identity
	// detection working: the turn gate still sees the source role and treats
	// the same objective as a continuation, then this records the target.
	if handoff := c.task.Handoff; handoff != nil {
		switch handoff.Status {
		case HandoffApproved:
			// A handoff that recorded an approved objective covers the turn
			// that follows the decision, and that turn states the objective it
			// runs. The recorded objective is the approving request's own text;
			// the turn's contract may be derived from a prepared form of it (a
			// long prompt is compressed before the model sees it), so the
			// approval is decided by its lifecycle, not by comparing prose. A
			// handoff that recorded no objective preserves the task objective
			// and must still match it exactly.
			approved := strings.TrimSpace(handoff.ApprovedObjective)
			switch {
			case approved == "" && handoff.Objective != objective:
				handoff.Status = HandoffFailed
				handoff.Failure = "the requested objective does not match the approved handoff"
				handoff.UpdatedAt = now
			case !c.role.Valid() || handoff.To != c.role:
				handoff.Status = HandoffFailed
				handoff.Failure = "the live role " + c.role.Label() + " does not match the approved target " + handoff.To.Label()
				handoff.UpdatedAt = now
			default:
				if handoff.Objective != objective {
					c.task.Objective = objective
					handoff.Objective = objective
					adopted = true
				}
				if err := c.applyApprovedHandoff(now); err != nil {
					return Task{}, err
				}
				handoff.Consumed = true
			}
		case HandoffApplied:
			// An explicit workspace command records an applied handoff before
			// the next turn. Consuming it here is what attaches the turn to
			// the same task without also making every later prompt attach.
			if !handoff.Consumed {
				if !c.role.Valid() || handoff.To != c.role {
					handoff.Status = HandoffFailed
					handoff.Failure = "the live role " + c.role.Label() + " does not match the applied target " + handoff.To.Label()
				} else {
					handoff.Consumed = true
				}
				handoff.UpdatedAt = now
			}
		}
	}

	resumeRequested := c.resumePending
	c.resumePending = false
	c.resuming = false
	switch {
	case c.task.ID != "" && !c.task.Status.IsTerminal():
		c.planStale = false
		c.resuming = resumeRequested ||
			c.task.Status == StatusPaused || c.task.Status == StatusPending || c.task.Status == StatusNeedsReview
		if c.task.Contract == nil {
			return Task{}, fmt.Errorf("%w: %q has no persisted completion contract; cancel or replace it explicitly",
				ErrObjectiveConflict, c.task.Objective)
		}
		if !reflect.DeepEqual(*c.task.Contract, intent) {
			if !adopted {
				return Task{}, fmt.Errorf("%w: persisted %q, requested %q",
					ErrObjectiveConflict, c.task.Contract.Objective, objective)
			}
			// The human approved this objective under the new authority, so the
			// task adopts its contract. Identity, observed evidence, and carried
			// context survive; only the completion boundary changes, which is
			// exactly what the approval decided. The previous objective's
			// requirements no longer describe this task, so they are dropped
			// while the observations that established repository facts stay.
			cloned := cloneContract(intent)
			c.task.Contract = &cloned
			if c.evidence != nil {
				c.evidence.ResetRequirements()
			}
			if c.resetSource != nil {
				c.resetSource()
			}
			c.task.AppendEvent(TimelineEvent{
				At:      now,
				Kind:    TimelineHandoff,
				Role:    c.role,
				Status:  string(HandoffApplied),
				Summary: "objective re-scoped by the approved transition: " + describeObjective(objective),
			})
		}
		// A paused, reviewable, or pending task resumes with its identity
		// intact; a running one continues (a turn boundary, not a new task).
		if c.task.Status == StatusPaused || c.task.Status == StatusPending || c.task.Status == StatusNeedsReview {
			if err := c.task.Start(now); err != nil {
				return Task{}, err
			}
		}
		if c.budgetState == nil {
			c.budgetState = &budget.State{Limits: c.budgetLimits}
		}
		c.task.UpdatedAt = now
	default:
		// A new objective starts a new plan, but only when the plan belongs to
		// a predecessor. A first task, a resume, or a restored checkpoint keeps
		// the plan it already owns; a detached or replaced task leaves its plan
		// behind, and it must not become this task's progress.
		if c.resetSource != nil && (hadTask || c.planStale) {
			c.resetSource()
		}
		c.planStale = false
		created, err := New(objective)
		if err != nil {
			return Task{}, err
		}
		created.UpdatedAt = now
		cloned := cloneContract(intent)
		created.Contract = &cloned
		if snapshot != nil {
			clonedSnapshot := cloneSnapshot(*snapshot)
			created.Snapshot = &clonedSnapshot
		}
		c.task = created
		// A new task owns a new ledger and new evidence. Its identity is new,
		// so neither prior consumption nor prior observations apply.
		c.budgetState = &budget.State{Limits: c.budgetLimits, UpdatedAt: now}
		c.evidence = cognition.NewEvidenceState()
		c.auditMu.Lock()
		c.audit = ""
		c.lastReport = agent.ProjectionReport{}
		c.hasReport = false
		c.auditMu.Unlock()
	}

	// The contract is the user-derived boundary, so its explicit constraints
	// are trustworthy task context. The contract is immutable across a resume,
	// which makes this deterministic and never a second source of truth.
	if c.task.Contract != nil && c.task.Contract.Grounding != nil {
		if constraints := boundConstraints(c.task.Contract.Grounding.Constraints); len(constraints) > 0 {
			c.task.Constraints = constraints
		}
	}
	if c.role.Valid() {
		c.task.ActiveRole = c.role
		if err := c.task.TouchRole(c.role, now); err != nil {
			return Task{}, err
		}
	}
	c.syncTodos()
	// A resume must not overwrite the last meaningful action with a generic
	// "started" line: the interruption reason or the last observed progress is
	// the checkpoint the continuation is orienting from. A genuinely new task
	// or a role-change turn starts a new action.
	if c.resuming {
		c.lastAction = firstNonEmpty(c.lastAction, "resumed: "+describeObjective(c.task.Objective))
	} else {
		c.lastAction = "started: " + describeObjective(c.task.Objective)
	}
	// The timeline records meaningful attachment boundaries, not every turn of
	// a running task. A new identity, or work continuing from an interruption,
	// is worth explaining later; an ordinary continuation is already covered by
	// the checkpoint event.
	switch {
	case !hadTask:
		c.task.AppendEvent(TimelineEvent{
			At:      now,
			Kind:    TimelineCreated,
			Role:    c.role,
			Status:  string(c.task.Status),
			Summary: "task created: " + describeObjective(c.task.Objective),
		})
	case previousStatus == StatusPaused || previousStatus == StatusPending || previousStatus == StatusNeedsReview:
		c.task.AppendEvent(TimelineEvent{
			At:      now,
			Kind:    TimelineResumed,
			Role:    c.role,
			Status:  string(c.task.Status),
			Summary: "resumed " + describeObjective(c.task.Objective) + " from " + string(previousStatus),
		})
	}
	return c.commitState(before)
}

// Sync refreshes the todo snapshot and checkpoints.
//
// It is called at meaningful boundaries — a todo tool call completing, a
// context compaction — rather than on every streaming event.
func (c *Controller) Sync() (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	before, beforeAction := c.task.Clone(), c.lastAction
	c.syncTodos()
	c.task.UpdatedAt = c.now().UTC()
	if active, ok := c.task.ActiveTodo(); ok {
		c.lastAction = "working on: " + active.Label
	}
	c.task.AppendEventIfChanged(TimelineEvent{
		At:      c.task.UpdatedAt,
		Kind:    TimelineCheckpoint,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: c.lastAction,
	})
	return c.commit(before, beforeAction)
}

// Pause records an interruption at a recoverable checkpoint.
//
// Pausing is idempotent and never converts a terminal task: an interruption of
// an already-completed task is not a pause.
func (c *Controller) Pause(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	before, beforeAction := c.task.Clone(), c.lastAction
	c.syncTodos()
	if c.task.Status != StatusPaused {
		if err := c.task.Pause(c.now().UTC()); err != nil {
			return c.task.Clone(), err
		}
	}
	c.lastAction = firstNonEmpty(reason, "interrupted")
	c.noteRoleProgress("paused: " + c.lastAction)
	c.task.AppendEventIfChanged(TimelineEvent{
		At:      c.task.UpdatedAt,
		Kind:    TimelinePaused,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: c.lastAction,
	})
	return c.commit(before, beforeAction)
}

// Complete records a successful end when the work is actually finished.
//
// A turn can end cleanly while outstanding todos remain. That is progress, not
// completion: the task stays RUNNING and a checkpoint records the progress,
// because marking it COMPLETED on a model's clean stop alone would assert more
// than the runtime observed.
func (c *Controller) Complete() (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	before, beforeAction := c.task.Clone(), c.lastAction
	c.syncTodos()
	now := c.now().UTC()
	if c.task.HasOpenTodos() {
		c.task.UpdatedAt = now
		c.lastAction = "turn finished with work remaining"
		c.noteRoleProgress(c.lastAction)
		return c.commit(before, beforeAction)
	}
	if err := c.task.Complete(now); err != nil {
		return c.task.Clone(), err
	}
	c.lastAction = "completed"
	c.noteRoleProgress(c.lastAction)
	// Complete() is the legacy lifecycle transition: it records the status
	// without a proof, so the objective projection reports COMPLETED_UNPROVEN
	// rather than claiming evidence it never saw. Phase 3 hosts use
	// CompleteWithEvidence.
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineCompleted,
		Role:    c.role,
		Status:  string(c.task.ObjectiveState()),
		Summary: "completed without a recorded evidence proof",
	})
	return c.commit(before, beforeAction)
}

// NeedsReview records that a turn ended without enough runtime evidence to
// claim the objective was reached.
//
// It is deliberately distinct from PAUSED (an interruption) and FAILED (an
// unrecoverable error): the work may be fine, but the runtime did not observe
// the change or its verification. A later turn resumes the same task and can
// supply the missing evidence.
func (c *Controller) NeedsReview(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	before, beforeAction := c.task.Clone(), c.lastAction
	c.syncTodos()
	if reviewed := strings.TrimSpace(reason); reviewed != "" {
		c.task.ReviewReason = reviewed
	}
	if c.task.Status != StatusNeedsReview {
		if err := c.task.NeedsReview(c.now().UTC()); err != nil {
			return c.task.Clone(), err
		}
	}
	c.lastAction = firstNonEmpty(reason, "needs review")
	c.noteRoleProgress(c.lastAction)
	c.task.AppendEventIfChanged(TimelineEvent{
		At:      c.task.UpdatedAt,
		Kind:    TimelineReview,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: c.lastAction,
	})
	return c.commit(before, beforeAction)
}

// Fail records an unrecoverable execution error.
func (c *Controller) Fail(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	before, beforeAction := c.task.Clone(), c.lastAction
	c.syncTodos()
	if err := c.task.Fail(c.now().UTC()); err != nil {
		return c.task.Clone(), err
	}
	c.lastAction = firstNonEmpty(reason, "failed")
	c.noteRoleProgress(c.lastAction)
	c.task.AppendEvent(TimelineEvent{
		At:      c.task.UpdatedAt,
		Kind:    TimelineFailed,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: c.lastAction,
	})
	return c.commit(before, beforeAction)
}

// Cancel records a deliberate abandon by the user.
func (c *Controller) Cancel(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	before, beforeAction := c.task.Clone(), c.lastAction
	c.syncTodos()
	if err := c.task.Cancel(c.now().UTC()); err != nil {
		return c.task.Clone(), err
	}
	c.lastAction = firstNonEmpty(reason, "cancelled")
	c.noteRoleProgress(c.lastAction)
	c.task.AppendEvent(TimelineEvent{
		At:      c.task.UpdatedAt,
		Kind:    TimelineCancelled,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: c.lastAction,
	})
	return c.commit(before, beforeAction)
}

// Context returns deterministic orientation blocks for the current task.
//
// It is the basis for continuing after an interruption or a compaction without
// replaying the transcript: the model is told what we were doing, what has
// happened, what is known, what remains, and what should happen next.
func (c *Controller) Context() []string {
	if c.task.ID == "" {
		return nil
	}
	task := c.task.Clone()
	task.ResumeDirective = c.resumeDirective()
	return ContextFor(task, c.currentRole(), c.lastAction)
}

// resumeDirective assembles the bounded orientation a continuation needs: what
// the interrupted execution had already done, whether it owes a verification,
// and the explicit instruction not to re-plan from the objective.
//
// It is derived from durable evidence, never from model prose. A change with no
// following check is the strongest case: the task already knows where it
// stopped, so the next turn must verify that change instead of rediscovering
// the repository. When no such change exists, a paused or reviewable task still
// gets the bounded "continue from the checkpoint" orientation.
func (c *Controller) resumeDirective() string {
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return ""
	}
	resuming := c.resuming || c.resumePending ||
		c.task.Status == StatusPaused || c.task.Status == StatusNeedsReview
	summary, pending, hasChange := c.pendingVerification()
	review := strings.TrimSpace(c.task.ReviewReason)
	if !resuming && !hasChange && review == "" {
		return ""
	}
	var parts []string
	parts = append(parts, "Continue this task from the recorded checkpoint. Do not re-plan from the objective, and do not repeat completed discovery unless current evidence is insufficient.")
	if hasChange {
		parts = append(parts, "Last observed change: "+summary+".")
		if pending {
			parts = append(parts, "Verification: pending. Resume from post-change verification: run the check the contract requires before changing anything else, and do not repeat the change.")
		} else {
			parts = append(parts, "Verification: already recorded after the last change.")
		}
	}
	if review != "" && pending {
		// The durable review remains actionable only while the change it
		// refers to is still unverified. A resolved or change-free review is
		// history, not current orientation.
		parts = append(parts, "Outstanding evidence boundary: "+review)
	}
	return boundText(strings.Join(parts, " "), contextFieldCap)
}

// pendingVerification reports the newest durable change and whether a usable
// check followed it.
//
// The task-linked evidence is authoritative when it has been projected. The
// cognition state is the fallback for a checkpoint that stopped before that
// projection ran; it is the same observation store, read directly rather than
// duplicated.
func (c *Controller) pendingVerification() (summary string, pending, found bool) {
	if change, ok := c.latestChangeRecord(); ok {
		verified := len(c.verificationsAfterChange(change)) > 0
		summary = strings.TrimSpace(change.Summary)
		if summary == "" {
			summary = "a repository change"
		}
		return summary, !verified, true
	}
	// The task-linked projection is the validity authority. When it exists but
	// names no current change, a raw cognition item may be invalidated or
	// superseded; resurrecting it as a resume obligation would be false. The
	// raw state is only a fallback for a checkpoint that stopped before any
	// projection ran.
	if c.evidence == nil || len(c.task.Context.Evidence) > 0 {
		return "", false, false
	}
	best := cognition.EvidenceItem{}
	bestSeq := -1
	for _, item := range c.evidence.Items {
		if item.Status != cognition.StatusKnown || !strings.HasPrefix(item.ID, "mutation:") {
			continue
		}
		if item.Seq > bestSeq {
			best, bestSeq = item, item.Seq
		}
	}
	if bestSeq < 0 {
		return "", false, false
	}
	verified := false
	for _, item := range c.evidence.Items {
		if item.Seq < bestSeq || item.Status != cognition.StatusKnown {
			continue
		}
		if item.Source.Check != "" || strings.HasPrefix(item.ID, "verification:") {
			verified = true
			break
		}
	}
	summary = strings.TrimSpace(best.Summary)
	if summary == "" {
		summary = "a repository change"
	}
	return summary, !verified, true
}

// ensureLoaded loads the checkpoint once.
//
// A corrupt checkpoint that still leaves a readable projection is not fatal:
// the projection carries enough to continue. Only a store with nothing
// recoverable leaves the controller with no task, and then Begin starts fresh.
func (c *Controller) ensureLoaded() error {
	if c.restored {
		return nil
	}
	c.restored = true

	snap, err := c.store.Load()
	if !snap.Found {
		return nil
	}
	c.task = NormalizeRecovered(snap.Task)
	c.recovery = snap.Recovery
	c.task.Recovery = snap.Recovery
	// Restore the role the task last executed under. The live authority is
	// still read through roleSource at the next Begin; this value only keeps
	// attribution and role-change detection truthful across a restart.
	if last := c.task.LastRole(); last.Valid() {
		c.role = last
		c.roleKnown = true
	}
	// A non-terminal task from a version-1 checkpoint has no contract. It
	// cannot be completed, and it must not be silently rebound to whatever the
	// next prompt says; say why so the human can cancel or replace it.
	if !c.task.Status.IsTerminal() && c.task.Contract == nil && c.task.ReviewReason == "" {
		c.task.ReviewReason = "the persisted task has no completion contract; it can be cancelled or replaced, not completed"
	}
	if stored := snap.Progress.Budget; stored != nil {
		state := *stored
		if c.budgetState == nil {
			c.budgetState = &state
		} else {
			// An explicit grant supplied before Restore wins over the stored
			// limits; the consumed total always comes from disk, so a resume
			// can raise the ceiling but never erase what was spent.
			c.budgetState.Used = state.Used
			c.budgetState.UpdatedAt = state.UpdatedAt
		}
	}
	if snap.Progress.EvidenceState != nil {
		c.evidence = snap.Progress.EvidenceState.Clone()
	}
	if snap.Progress.Audit != "" {
		c.auditMu.Lock()
		c.audit = snap.Progress.Audit
		c.auditMu.Unlock()
	}
	if err != nil && c.task.ID == "" {
		// Nothing recoverable: report rather than silently starting over a
		// state we could not read.
		c.recovery = RecoveryReport{
			State:  RecoveryBlocked,
			Detail: "the checkpoint could not be read and no task could be recovered",
		}
		return fmt.Errorf("%w: %v", ErrCorruptTask, err)
	}
	if err != nil && c.task.ContextWarning == "" {
		c.task.ContextWarning = "task state could not be fully restored: " + err.Error()
	}
	if err == nil && snap.Progress.LastAction != "" {
		c.lastAction = snap.Progress.LastAction
	}
	return nil
}

// syncTodos refreshes the task's todo snapshot from the runtime's owner.
func (c *Controller) syncTodos() {
	if c.source == nil {
		return
	}
	// A sync failure must not abort a lifecycle transition: the labels are
	// still recorded, only their ids would be regenerated.
	_ = c.task.SetTodos(c.source())
}

// noteRoleProgress appends one bounded progress line to the active role's
// context. It is bookkeeping only; a failure never blocks the lifecycle
// transition it describes.
func (c *Controller) noteRoleProgress(note string) {
	if !c.role.Valid() || c.task.ID == "" || strings.TrimSpace(note) == "" {
		return
	}
	_, _ = c.task.UpdateRoleContext(c.role, RoleContextUpdate{
		Role:     c.role,
		Progress: []string{note},
	}, c.now().UTC())
}

// commit persists the current in-memory transition. On failure it restores the
// prior task and last action, so the authoritative state never advances past a
// checkpoint that did not land. It is the shared shape for every lifecycle
// transition: memory is not the commit point, the checkpoint is.
func (c *Controller) commit(before Task, beforeAction string) (Task, error) {
	if err := c.persist(); err != nil {
		c.task = before
		c.lastAction = beforeAction
		return c.task.Clone(), err
	}
	return c.task.Clone(), nil
}

// controllerState is the full in-memory state a transition may replace. Begin
// and ResumeTask change more than the task, so they snapshot the whole set to
// roll back a failed checkpoint without leaving a half-attached task.
type controllerState struct {
	task       Task
	lastAction string
	role       workspace.Workspace
	roleKnown  bool
	budget     *budget.State
	evidence   *cognition.EvidenceState
	audit      string
	report     agent.ProjectionReport
	hasReport  bool
}

func (c *Controller) snapshotState() controllerState {
	var b *budget.State
	if c.budgetState != nil {
		copied := *c.budgetState
		b = &copied
	}
	c.auditMu.Lock()
	audit, report, has := c.audit, c.lastReport, c.hasReport
	c.auditMu.Unlock()
	return controllerState{
		task:       c.task.Clone(),
		lastAction: c.lastAction,
		role:       c.role,
		roleKnown:  c.roleKnown,
		budget:     b,
		evidence:   c.evidence.Clone(),
		audit:      audit,
		report:     report,
		hasReport:  has,
	}
}

func (c *Controller) restoreState(s controllerState) {
	c.task = s.task
	c.lastAction = s.lastAction
	c.role = s.role
	c.roleKnown = s.roleKnown
	c.budgetState = s.budget
	c.evidence = s.evidence
	c.auditMu.Lock()
	c.audit, c.lastReport, c.hasReport = s.audit, s.report, s.hasReport
	c.auditMu.Unlock()
}

// commitState persists a multi-field transition and restores everything on
// failure.
func (c *Controller) commitState(s controllerState) (Task, error) {
	if err := c.persist(); err != nil {
		c.restoreState(s)
		return c.task.Clone(), err
	}
	return c.task.Clone(), nil
}

// persist writes the checkpoint and projection, stamping the last action.
func (c *Controller) persist() error {
	if c.task.ID == "" {
		return nil
	}
	// A checkpoint is the host boundary where material observations become
	// durable task context. Projection is best-effort: a full evidence log
	// must not block the lifecycle transition it is describing.
	c.projectTaskContext()
	progress := ProgressFrom(c.task)
	if progress.CheckpointAt.IsZero() {
		progress.CheckpointAt = c.task.UpdatedAt
	}
	progress.LastAction = c.lastAction
	if c.budgetState != nil {
		state := *c.budgetState
		progress.Budget = &state
	}
	if c.evidence != nil && !c.evidence.Empty() {
		progress.EvidenceState = c.evidence.Clone()
	}
	c.auditMu.Lock()
	audit := c.audit
	c.auditMu.Unlock()
	if audit != "" {
		progress.Audit = audit
	}
	return c.store.SaveCheckpoint(c.task, progress)
}

// SetTodo marks one todo, for the controller's own tests and for a host that
// records an observed todo outcome. It is not exposed to the model.
func (c *Controller) SetTodo(id string, status TodoStatus, evidence string) (Task, error) {
	if c.task.ID == "" {
		return Task{}, ErrNoActiveTask
	}
	if err := c.task.SetTodoStatus(id, status, evidence); err != nil {
		return Task{}, err
	}
	c.task.UpdatedAt = c.now().UTC()
	return c.task.Clone(), c.persist()
}

// Detach preserves the active task durably and clears the session binding.
//
// A non-terminal task is archived as resumable (RUNNING is first paused, so an
// interruption is never recorded as success) and the live slot is cleared, so
// the next session or the next new intent starts with no active task. A
// terminal task is archived as history and then cleared: completed, failed,
// and cancelled work is never resumable, but its objective, evidence, and
// outcome are never deleted. The archived copy keeps the full task, progress,
// budget, evidence, and audit, so /task resume can restore resumable work
// without replaying the transcript.
//
// It never deletes durable work: detaching moves the live task to the archive,
// it never drops it.
func (c *Controller) Detach() (Task, bool, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, false, err
	}
	if c.task.ID == "" {
		return Task{}, false, nil
	}
	c.projectTaskContext()
	detached := c.task.Clone()
	if detached.Status.IsTerminal() {
		c.task.AppendEvent(TimelineEvent{
			At:      c.task.UpdatedAt,
			Kind:    TimelineDetached,
			Role:    c.role,
			Status:  string(c.task.Status),
			Summary: "detached terminal task: " + describeObjective(c.task.Objective),
		})
		progress := ProgressFrom(c.task)
		if progress.CheckpointAt.IsZero() {
			progress.CheckpointAt = c.task.UpdatedAt
		}
		progress.LastAction = firstNonEmpty(c.lastAction, "detached terminal task")
		if c.budgetState != nil {
			state := *c.budgetState
			progress.Budget = &state
		}
		// A terminal archive keeps the observation state too, so a completed
		// task's lineage artifacts still resolve after the session boundary.
		if c.evidence != nil && !c.evidence.Empty() {
			progress.EvidenceState = c.evidence.Clone()
		}
		c.auditMu.Lock()
		audit := c.audit
		c.auditMu.Unlock()
		if audit != "" {
			progress.Audit = audit
		}
		if err := c.store.ArchiveTask(c.task, progress); err != nil {
			return Task{}, false, err
		}
		c.task = Task{}
		c.roleKnown = false
		c.planStale = true
		c.evidence = cognition.NewEvidenceState()
		c.auditMu.Lock()
		c.audit = ""
		c.lastReport = agent.ProjectionReport{}
		c.hasReport = false
		c.auditMu.Unlock()
		c.lastAction = "detached terminal task"
		_ = c.store.ClearActive()
		return detached, false, nil
	}
	c.syncTodos()
	if detached.Status == StatusRunning {
		if err := c.task.Pause(c.now().UTC()); err != nil {
			return Task{}, false, err
		}
		detached = c.task.Clone()
		c.lastAction = firstNonEmpty(c.lastAction, "interrupted")
	}
	c.task.AppendEvent(TimelineEvent{
		At:      c.task.UpdatedAt,
		Kind:    TimelineDetached,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: "detached for a new session: " + describeObjective(c.task.Objective),
	})
	progress := ProgressFrom(c.task)
	if progress.CheckpointAt.IsZero() {
		progress.CheckpointAt = c.task.UpdatedAt
	}
	progress.LastAction = firstNonEmpty(c.lastAction, "detached for a new session")
	if c.budgetState != nil {
		state := *c.budgetState
		progress.Budget = &state
	}
	if c.evidence != nil && !c.evidence.Empty() {
		progress.EvidenceState = c.evidence.Clone()
	}
	c.auditMu.Lock()
	audit := c.audit
	c.auditMu.Unlock()
	if audit != "" {
		progress.Audit = audit
	}
	if err := c.store.ArchiveTask(c.task, progress); err != nil {
		return Task{}, false, err
	}
	archived := c.task.Clone()
	c.task = Task{}
	c.roleKnown = false
	c.planStale = true
	c.evidence = cognition.NewEvidenceState()
	c.auditMu.Lock()
	c.audit = ""
	c.lastReport = agent.ProjectionReport{}
	c.hasReport = false
	c.auditMu.Unlock()
	c.lastAction = "detached: " + describeObjective(archived.Objective)
	if err := c.store.ClearActive(); err != nil {
		return Task{}, false, err
	}
	return archived, true, nil
}

// ListResumable returns the active non-terminal task plus every archived
// non-terminal task, oldest first.
//
// The order is deterministic (checkpoint stamp, then ID), so /task list and
// /task resume with no ID resolve the same way every time. Terminal tasks are
// never resumable and are excluded.
func (c *Controller) ListResumable() ([]Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return nil, err
	}
	var out []Task
	seen := map[string]bool{}
	if c.task.ID != "" && IsResumable(c.task.Status) {
		out = append(out, c.task.Clone())
		seen[c.task.ID] = true
	}
	archived, err := c.store.ListArchived()
	if err != nil {
		return nil, err
	}
	for _, snap := range archived {
		t := NormalizeRecovered(snap.Task)
		if !IsResumable(t.Status) || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t.Clone())
	}
	return out, nil
}

// ArchiveProblems returns the corrupt detached-task files observed by the most
// recent archive listing. It is diagnostic only: naming a lost archive never
// grants authority and never fabricates a task.
func (c *Controller) ArchiveProblems() []ArchiveProblem {
	return c.store.ArchiveProblems()
}

// ArchivedTasks returns every archived task, oldest first, including terminal
// history as well as resumable work.
//
// It is read-only recovery evidence: a completed task that was archived before
// a new objective stays inspectable through /task history. Listing never
// resumes, mutates, or deletes durable work, and a corrupt archive is reported
// through ArchiveProblems rather than silently skipped.
func (c *Controller) ArchivedTasks() ([]Task, error) {
	archived, err := c.store.ListArchived()
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(archived))
	for _, snap := range archived {
		out = append(out, NormalizeRecovered(snap.Task))
	}
	return out, nil
}

// ResumeTask attaches a resumable task to the current session.
//
// An empty id resumes the latest resumable task (newest stamp, ID tie-break);
// an unknown id is an error, never an arbitrary pick. Resuming archives the
// current active task first when it is a different non-terminal task, so no
// durable work is lost by switching. The resumed task moves to RUNNING with
// its persisted contract, snapshot, review reason, budget, evidence, and audit
// intact: durable state remains authoritative and is never reconstructed from
// the transcript.
func (c *Controller) ResumeTask(id string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	target, progress, recovery, found, err := c.findResumable(id)
	if err != nil {
		return Task{}, err
	}
	if !found {
		if strings.TrimSpace(id) == "" {
			return Task{}, errors.New("task: no resumable task")
		}
		return Task{}, fmt.Errorf("task: no resumable task %q", id)
	}
	if c.task.ID != "" && c.task.ID != target.ID && IsResumable(c.task.Status) {
		if _, _, err := c.Detach(); err != nil {
			return Task{}, err
		}
	}
	wasLive := c.task.ID == target.ID
	if !wasLive && c.resetSource != nil {
		// The session plan belongs to the task being left behind. Clear it so
		// the resumed task cannot read the previous task's items as its own;
		// the resumed task keeps its durable snapshot in the checkpoint.
		c.resetSource()
	}
	c.task = target
	c.planStale = false
	// A restored checkpoint can carry a contract from an older deterministic
	// classifier. Re-derive it before the resumed turn is routed or offered a
	// tool surface, so contradictory authority/completion state is never
	// presented to the model.
	c.reconcileStaleState()
	// Attribute the resumed task's projection to the role it last ran under,
	// not to whatever role the previously active task used.
	if last := target.LastRole(); last.Valid() {
		c.role = last
		c.roleKnown = true
	}
	if stored := progress.Budget; stored != nil {
		state := *stored
		if c.budgetState == nil {
			c.budgetState = &state
		} else {
			c.budgetState.Used = state.Used
			c.budgetState.UpdatedAt = state.UpdatedAt
			if c.budgetState.Limits == (budget.Limits{}) {
				c.budgetState.Limits = state.Limits
			}
		}
	}
	if progress.EvidenceState != nil {
		c.evidence = progress.EvidenceState.Clone()
	} else if c.evidence == nil {
		c.evidence = cognition.NewEvidenceState()
	}
	if progress.Audit != "" {
		c.auditMu.Lock()
		c.audit = progress.Audit
		c.auditMu.Unlock()
	}
	if progress.LastAction != "" {
		c.lastAction = progress.LastAction
	}
	now := c.now().UTC()
	c.resumePending = true
	switch c.task.Status {
	case StatusPaused, StatusPending, StatusNeedsReview:
		if err := c.task.Start(now); err != nil {
			return Task{}, err
		}
	case StatusRunning:
		c.task.UpdatedAt = now
	}
	c.lastAction = "resumed: " + describeObjective(c.task.Objective)
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineResumed,
		Role:    c.role,
		Status:  string(c.task.Status),
		Summary: c.lastAction + " from the detached archive",
	})
	// Keep the classification the archive sanitizer produced. Claiming INTACT
	// after a repair would tell the user nothing was dropped when something
	// was; append the resume detail without erasing the truth.
	if recovery.Empty() {
		detail := "resumed from the detached archive"
		if wasLive {
			detail = "resumed the active task"
		}
		recovery = RecoveryReport{State: RecoveryIntact, Detail: detail}
	} else if recovery.Detail != "" {
		recovery.Detail += "; resumed from the detached archive"
	} else {
		recovery.Detail = "resumed from the detached archive"
	}
	c.recovery = recovery
	c.task.Recovery = c.recovery
	if err := c.persist(); err != nil {
		return Task{}, err
	}
	_ = c.store.RemoveArchived(c.task.ID)
	return c.task.Clone(), nil
}

// CancelResumable cancels one resumable task by ID, or the latest when empty.
//
// Cancelling is terminal and never claims completion. A cancelled archived
// task stays on disk as CANCELLED so it is never listed as resumable again;
// the live task uses the same transition and remains CANCELLED in place.
func (c *Controller) CancelResumable(id, reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(id) == "" {
		resumable, err := c.ListResumable()
		if err != nil {
			return Task{}, err
		}
		if len(resumable) == 0 {
			return Task{}, errors.New("task: no resumable task")
		}
		id = resumable[len(resumable)-1].ID
	}
	if c.task.ID == id {
		closed, err := c.Cancel(reason)
		if err != nil {
			return Task{}, err
		}
		return closed, nil
	}
	snap, err := c.store.LoadArchived(id)
	if err != nil {
		return Task{}, err
	}
	if !snap.Found {
		return Task{}, fmt.Errorf("task: no resumable task %q", id)
	}
	t := NormalizeRecovered(snap.Task)
	if t.Status.IsTerminal() {
		return t.Clone(), nil
	}
	now := c.now().UTC()
	switch t.Status {
	case StatusRunning:
		if err := t.Pause(now); err != nil {
			return Task{}, err
		}
		fallthrough
	default:
		if err := t.Cancel(now); err != nil {
			// NEEDS_REVIEW -> CANCELLED is permitted; RUNNING was paused above.
			// Any other refusal is a lifecycle error worth surfacing.
			return Task{}, err
		}
	}
	progress := snap.Progress
	progress.Status = t.Status
	progress.TaskID = t.ID
	progress.CheckpointAt = t.UpdatedAt
	if err := c.store.ArchiveTask(t, progress); err != nil {
		return Task{}, err
	}
	return t.Clone(), nil
}

// findResumable locates one resumable task and its progress, latest when empty.
//
// The returned recovery report is the classification of the artifact the task
// actually came from, so a resumed archive keeps the truth of what sanitizing
// it repaired.
func (c *Controller) findResumable(id string) (Task, Progress, RecoveryReport, bool, error) {
	id = strings.TrimSpace(id)
	if id != "" && c.task.ID == id && IsResumable(c.task.Status) {
		progress := ProgressFrom(c.task)
		progress.LastAction = c.lastAction
		return c.task.Clone(), progress, c.recovery, true, nil
	}
	archived, err := c.store.ListArchived()
	if err != nil {
		return Task{}, Progress{}, RecoveryReport{}, false, err
	}
	if id != "" {
		for _, snap := range archived {
			if snap.Task.ID == id && IsResumable(NormalizeRecovered(snap.Task).Status) {
				t := NormalizeRecovered(snap.Task)
				return t.Clone(), snap.Progress, snap.Recovery, true, nil
			}
		}
		if c.task.ID == id {
			progress := ProgressFrom(c.task)
			return c.task.Clone(), progress, c.recovery, IsResumable(c.task.Status), nil
		}
		return Task{}, Progress{}, RecoveryReport{}, false, nil
	}
	var (
		best       Task
		bestProg   Progress
		bestReport RecoveryReport
		bestFound  bool
	)
	consider := func(t Task, p Progress, report RecoveryReport) {
		t = NormalizeRecovered(t)
		if !IsResumable(t.Status) {
			return
		}
		if !bestFound || p.CheckpointAt.After(bestProg.CheckpointAt) ||
			(p.CheckpointAt.Equal(bestProg.CheckpointAt) && t.ID > best.ID) {
			best, bestProg, bestReport, bestFound = t.Clone(), p, report, true
		}
	}
	if c.task.ID != "" {
		p := ProgressFrom(c.task)
		p.CheckpointAt = c.task.UpdatedAt
		p.LastAction = c.lastAction
		consider(c.task, p, c.recovery)
	}
	for _, snap := range archived {
		consider(snap.Task, snap.Progress, snap.Recovery)
	}
	return best, bestProg, bestReport, bestFound, nil
}

// withTask applies one context mutation atomically with its checkpoint. A
// failure restores the prior in-memory task, so a rejected or unwritable
// context change can never look applied.
func withTask[T any](c *Controller, fn func() (T, error)) (T, error) {
	var zero T
	if err := c.ensureLoaded(); err != nil {
		return zero, err
	}
	if c.task.ID == "" {
		return zero, ErrNoActiveTask
	}
	before := c.task.Clone()
	result, err := fn()
	if err != nil {
		c.task = before
		return zero, err
	}
	c.task.UpdatedAt = c.now().UTC()
	if err := c.persist(); err != nil {
		c.task = before
		return zero, err
	}
	return result, nil
}

// RecordEvidence appends a task-linked observation.
func (c *Controller) RecordEvidence(in EvidenceInput) (EvidenceRecord, error) {
	return withTask(c, func() (EvidenceRecord, error) {
		if !in.Role.Valid() {
			in.Role = c.currentRole()
		}
		return c.task.RecordEvidence(in)
	})
}

// RecordFact appends an evidence-backed verified fact. A caller must supply
// current supporting evidence; a model assertion belongs in RecordHypothesis.
func (c *Controller) RecordFact(in FactInput) (Fact, error) {
	return withTask(c, func() (Fact, error) {
		if !in.Role.Valid() {
			in.Role = c.currentRole()
		}
		return c.task.RecordFact(in)
	})
}

// RecordHypothesis appends an unverified proposal.
func (c *Controller) RecordHypothesis(in HypothesisInput) (Hypothesis, error) {
	return withTask(c, func() (Hypothesis, error) {
		if !in.Role.Valid() {
			in.Role = c.currentRole()
		}
		return c.task.RecordHypothesis(in)
	})
}

type confirmedHypothesis struct {
	hypothesis Hypothesis
	fact       Fact
}

// AttachEvidence adds current supporting evidence to an existing fact or
// hypothesis without asserting that the claim is confirmed.
func (c *Controller) AttachEvidence(claimID string, evidenceIDs []string) error {
	_, err := withTask(c, func() (struct{}, error) {
		return struct{}{}, c.task.AttachEvidence(claimID, evidenceIDs, c.now().UTC())
	})
	return err
}

// ConfirmHypothesis resolves a hypothesis with evidence and records the fact.
func (c *Controller) ConfirmHypothesis(id string, evidenceIDs []string, role workspace.Workspace) (Hypothesis, Fact, error) {
	result, err := withTask(c, func() (confirmedHypothesis, error) {
		if !role.Valid() {
			role = c.currentRole()
		}
		hypothesis, fact, err := c.task.ConfirmHypothesis(id, evidenceIDs, role, c.now().UTC())
		return confirmedHypothesis{hypothesis: hypothesis, fact: fact}, err
	})
	return result.hypothesis, result.fact, err
}

// RejectHypothesis records a disposition without creating a fact.
func (c *Controller) RejectHypothesis(id, reason string) (Hypothesis, error) {
	return withTask(c, func() (Hypothesis, error) {
		return c.task.RejectHypothesis(id, reason, c.currentRole(), c.now().UTC())
	})
}

// SupersedeHypothesis replaces one hypothesis with another, retaining both.
func (c *Controller) SupersedeHypothesis(id, byID string) (Hypothesis, error) {
	return withTask(c, func() (Hypothesis, error) {
		return c.task.SupersedeHypothesis(id, byID, c.currentRole(), c.now().UTC())
	})
}

// SupersedeFact replaces one active fact with another, retaining both.
func (c *Controller) SupersedeFact(id, byID string) (Fact, error) {
	return withTask(c, func() (Fact, error) {
		return c.task.SupersedeFact(id, byID, c.now().UTC())
	})
}

// InvalidateEvidence marks an observation no longer applicable and invalidates
// conclusions that depended only on it.
func (c *Controller) InvalidateEvidence(id, reason string) (EvidenceRecord, error) {
	return withTask(c, func() (EvidenceRecord, error) {
		return c.task.InvalidateEvidence(id, reason, c.now().UTC())
	})
}

// SupersedeEvidence marks one observation replaced by a later one.
func (c *Controller) SupersedeEvidence(id, byID string) (EvidenceRecord, error) {
	return withTask(c, func() (EvidenceRecord, error) {
		return c.task.SupersedeEvidence(id, byID, c.now().UTC())
	})
}

// AddQuestion records an outstanding question.
func (c *Controller) AddQuestion(text string) (Question, error) {
	return withTask(c, func() (Question, error) {
		return c.task.AddQuestion(text, c.currentRole(), c.now().UTC())
	})
}

// AnswerQuestion records an answer without deleting the question.
func (c *Controller) AnswerQuestion(id, answer string) (Question, error) {
	return withTask(c, func() (Question, error) {
		return c.task.AnswerQuestion(id, answer, c.currentRole(), c.now().UTC())
	})
}

// UpdateRoleContext appends to one role's execution context. It never touches
// another role's record and never grants authority.
func (c *Controller) UpdateRoleContext(role workspace.Workspace, update RoleContextUpdate) (RoleContext, error) {
	return withTask(c, func() (RoleContext, error) {
		return c.task.UpdateRoleContext(role, update, c.now().UTC())
	})
}

// RoleContext returns one role's execution context from the active task.
func (c *Controller) RoleContext(role workspace.Workspace) (RoleContext, bool) {
	if c.task.ID == "" {
		return RoleContext{}, false
	}
	return c.task.Context.Role(role)
}

// boundConstraints normalises the explicit limits the contract carries. The
// cap keeps a pathological request from turning the constraint line into a
// second brief.
func boundConstraints(values []string) []string {
	out := uniqueStrings(values)
	if len(out) > 8 {
		out = out[:8]
	}
	for i := range out {
		out[i] = boundText(out[i], maxFactBytes)
	}
	return out
}

func describeObjective(objective string) string {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return "task"
	}
	return objective
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
