package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
)

// Op names a recovery operation.
type Op string

// Recovery operations.
const (
	// OpUndo restores the workspace to the state before an applied edit.
	OpUndo Op = "undo"
	// OpRedo restores the workspace to a previously undone edit's after-state.
	OpRedo Op = "redo"
)

// State is the audit state of a recovery operation.
//
// The names are the audit vocabulary and are never collapsed into a generic
// "done": a blocked request, a failed restore, and an interrupted one are
// different outcomes a user must be able to tell apart.
type State string

// Recovery states.
const (
	// StateVerified means the target state was reached and proven.
	StateVerified State = "VERIFIED"
	// StateBlocked means the workspace did not match the expected source
	// state, so nothing was mutated. This is a refusal, not a failure.
	StateBlocked State = "BLOCKED"
	// StateEmpty means the timeline had nothing to move to.
	StateEmpty State = "EMPTY"
	// StateFailed means the restore ran but did not reach the target, or the
	// recorded data could not be read.
	StateFailed State = "FAILED"
	// StateInterrupted means an earlier operation never reached a terminal
	// state and the workspace must be inspected before anything else.
	StateInterrupted State = "INTERRUPTED"
	// StatePlanned and StateExecuting are the pre-terminal audit stages. They
	// are recorded so a crash between them and the terminal row is visible as
	// an incomplete transition rather than as silence.
	StatePlanned   State = "PLANNED"
	StateExecuting State = "EXECUTING"
)

// Result is the outcome of one recovery command.
type Result struct {
	// Op is the requested operation.
	Op Op
	// State is the terminal outcome.
	State State
	// CheckpointID is the edit the operation moved across, when known.
	CheckpointID string
	// Sequence is the checkpoint's ordinal in the session timeline.
	Sequence int64
	// Files are the paths the edit changed.
	Files []Change
	// Reason explains a blocked, failed, or interrupted outcome.
	Reason string
	// Unexpected names workspace paths that differ from the expected state,
	// bounded so a large drift produces a readable message.
	Unexpected []Change
	// Notes records truthful caveats, such as a directory left in place
	// because it holds files outside the checkpoint.
	Notes []string
}

// Verified reports whether the operation completed and was proven.
func (r Result) Verified() bool { return r.State == StateVerified }

// Controller is one session's façade over the recovery store.
//
// It is both the loop's EditJournal and the human-authorized recovery surface.
// Those roles are deliberately asymmetric: BeginEdit can only capture, while
// Undo and Redo are callable only by the host on an explicit user command. No
// tool receives a Controller, and the agent package cannot reach one.
type Controller struct {
	store   *Store
	scope   Scope
	scanner *Scanner
}

// Scope returns the session and workspace this controller is bound to.
func (c *Controller) Scope() Scope { return c.scope }

// BeginEdit captures the pre-mutation workspace and makes it durable.
func (c *Controller) BeginEdit(ctx context.Context, boundary agent.EditBoundary) (agent.EditTransaction, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	if err := c.repairPendingLocked(ctx); err != nil {
		return nil, err
	}
	if corrupt, err := c.corruptPending(ctx); err != nil {
		return nil, err
	} else if corrupt != "" {
		return nil, fmt.Errorf("%w: %s", ErrNeedsInspection, corrupt)
	}

	before, err := c.store.captureTree(ctx, c.scope.Root)
	if err != nil {
		return nil, fmt.Errorf("recovery: capture workspace: %w", err)
	}

	startedAt := now()
	var sequence int64
	err = c.store.withTx(ctx, func(tx *sql.Tx) error {
		next, err := maxSequence(ctx, tx, c.scope)
		if err != nil {
			return err
		}
		sequence = next + 1
		metadata, err := json.Marshal(map[string]string{
			"turn":  fmt.Sprintf("%d", boundary.Turn),
			"tools": joinNames(boundary.Tools),
		})
		if err != nil {
			return fmt.Errorf("recovery: encode metadata: %w", err)
		}
		id := checkpointID(c.scope, sequence, before.Hash(), startedAt)
		if _, err := tx.Exec(
			`INSERT INTO pending_edits
			   (id, session_id, repository, root, seq, before_tree, metadata, state, started_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
			id, c.scope.SessionID, c.scope.Repository, c.scope.Root, sequence,
			before.Hash(), string(metadata), startedAt,
		); err != nil {
			return fmt.Errorf("recovery: record pending edit: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The before-state is durable now. Only after this point may the loop
	// authorize a mutation.
	return &editTransaction{
		controller: c,
		id:         checkpointID(c.scope, sequence, before.Hash(), startedAt),
		sequence:   sequence,
		startedAt:  startedAt,
		before:     before,
		metadata:   boundaryMetadata(boundary),
	}, nil
}

// editTransaction is one captured but unpublised edit boundary.
type editTransaction struct {
	controller *Controller
	id         string
	sequence   int64
	startedAt  string
	before     *Tree
	metadata   map[string]string
	done       bool
}

// Commit observes the post-mutation workspace and publishes the checkpoint.
func (t *editTransaction) Commit(ctx context.Context) error {
	if t == nil || t.done {
		return nil
	}
	t.done = true
	c := t.controller
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	after, err := c.store.captureTree(ctx, c.scope.Root)
	if err != nil {
		// The pending record stays: the before-state is durable, so a later
		// start can repair the checkpoint without guessing.
		return fmt.Errorf("recovery: capture post-edit workspace: %w", err)
	}
	return c.publishEdit(ctx, t, after)
}

// Abort discards an unpublished transaction.
//
// The pending record is deliberately left in place when the process cannot
// prove the mutation did not happen. Callers use Abort only when the edit was
// never authorized.
func (t *editTransaction) Abort() {
	if t == nil || t.done {
		return
	}
	t.done = true
	c := t.controller
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	_, _ = c.store.db.Exec(`DELETE FROM pending_edits WHERE id = ?`, t.id)
}

// publishEdit turns a captured edit into an immutable checkpoint.
func (c *Controller) publishEdit(ctx context.Context, t *editTransaction, after *Tree) error {
	changes := Diff(t.before, after)
	if len(changes) == 0 {
		// An authorizing path that never ran, or a no-op rewrite, is not an
		// edit. Publishing it would put a phantom undo point in the timeline.
		if _, err := c.store.db.Exec(`DELETE FROM pending_edits WHERE id = ?`, t.id); err != nil {
			return fmt.Errorf("recovery: clear pending edit: %w", err)
		}
		return nil
	}

	metadata := t.metadata
	if skipped := append(append([]string(nil), t.before.Skipped...), after.Skipped...); len(skipped) > 0 {
		metadata = cloneMetadata(metadata)
		metadata["skipped"] = joinNames(bounded(skipped, 32))
	}

	return c.store.withTx(ctx, func(tx *sql.Tx) error {
		treeHash := after.Hash()
		if err := putTree(ctx, tx, after); err != nil {
			return err
		}
		timeline, err := readTimelineTx(tx, c.scope)
		if err != nil {
			return err
		}
		changesJSON, err := json.Marshal(changes)
		if err != nil {
			return fmt.Errorf("recovery: encode changes: %w", err)
		}
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("recovery: encode metadata: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO checkpoints
			   (id, session_id, repository, root, seq, parent_id, created_at,
			    before_tree, after_tree, changes, metadata, interrupted)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			t.id, c.scope.SessionID, c.scope.Repository, c.scope.Root, t.sequence,
			timeline.Cursor, now(), t.before.Hash(), treeHash,
			string(changesJSON), string(metadataJSON),
		); err != nil {
			return fmt.Errorf("recovery: publish checkpoint: %w", err)
		}
		// A new edit makes the undone branch unreachable through /redo. The
		// checkpoints themselves are kept: explicit invalidation, never silent
		// deletion of recovery data.
		if err := writeTimelineTx(tx, c.scope, Timeline{Cursor: t.id, Redo: nil}); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM pending_edits WHERE id = ?`, t.id); err != nil {
			return fmt.Errorf("recovery: clear pending edit: %w", err)
		}
		return nil
	})
}

// Undo restores the workspace to the state before the applied edit.
//
// It never rewinds the conversation, never deletes an edit from history, and
// never mutates the workspace unless the live state is proven to be the edit's
// recorded after-state.
func (c *Controller) Undo(ctx context.Context) Result {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	return c.move(ctx, OpUndo)
}

// Redo restores the workspace to the after-state of the next undone edit.
func (c *Controller) Redo(ctx context.Context) Result {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	return c.move(ctx, OpRedo)
}

// move performs one recovery transition.
func (c *Controller) move(ctx context.Context, op Op) Result {
	if result, ok := c.resolveInflightLocked(ctx); !ok {
		result.Op = op
		return result
	}
	if err := c.repairPendingLocked(ctx); err != nil {
		return Result{Op: op, State: StateFailed, Reason: err.Error()}
	}
	if corrupt, err := c.corruptPending(ctx); err != nil {
		return Result{Op: op, State: StateFailed, Reason: err.Error()}
	} else if corrupt != "" {
		return Result{Op: op, State: StateInterrupted, Reason: corrupt}
	}

	timeline, err := readTimelineTx(c.store.db, c.scope)
	if err != nil {
		return Result{Op: op, State: StateFailed, Reason: err.Error()}
	}

	var (
		checkpoint     Checkpoint
		target         string
		cursor         string
		redo           []string
		fromCheckpoint string
		toCheckpoint   string
	)
	switch op {
	case OpUndo:
		if timeline.Cursor == "" {
			return Result{Op: op, State: StateEmpty, Reason: "nothing to undo in this session"}
		}
		checkpoint, err = c.store.Checkpoint(c.scope, timeline.Cursor)
		if err != nil {
			return Result{Op: op, State: StateFailed, Reason: err.Error()}
		}
		target = checkpoint.BeforeTree
		cursor = checkpoint.Parent
		fromCheckpoint = checkpoint.ID
		toCheckpoint = checkpoint.Parent
		redo = append(append([]string(nil), timeline.Redo...), checkpoint.ID)
	case OpRedo:
		if len(timeline.Redo) == 0 {
			return Result{Op: op, State: StateEmpty, Reason: "nothing to redo in this session"}
		}
		id := timeline.Redo[len(timeline.Redo)-1]
		checkpoint, err = c.store.Checkpoint(c.scope, id)
		if err != nil {
			return Result{Op: op, State: StateFailed, Reason: err.Error()}
		}
		if checkpoint.Parent != timeline.Cursor {
			// The stored stack and the stored cursor disagree. Guessing which
			// is authoritative would apply a transition to the wrong state.
			return Result{
				Op: op, State: StateInterrupted,
				Reason: fmt.Sprintf("%v: redo target %s does not follow the current checkpoint",
					ErrCorrupt, short(id)),
			}
		}
		target = checkpoint.AfterTree
		cursor = checkpoint.ID
		fromCheckpoint = checkpoint.Parent
		toCheckpoint = checkpoint.ID
		redo = append([]string(nil), timeline.Redo[:len(timeline.Redo)-1]...)
	default:
		return Result{Op: op, State: StateFailed, Reason: fmt.Sprintf("unknown recovery operation %q", op)}
	}

	if err := c.store.verifyTree(checkpoint.BeforeTree); err != nil {
		return Result{
			Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes,
			Reason: err.Error(),
		}
	}
	if err := c.store.verifyTree(checkpoint.AfterTree); err != nil {
		return Result{
			Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes,
			Reason: err.Error(),
		}
	}

	expectedHash := checkpoint.AfterTree
	if op == OpRedo {
		expectedHash = checkpoint.BeforeTree
	}
	actual, err := c.scanner.Scan(ctx, nil)
	if err != nil {
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes,
			Reason: fmt.Sprintf("scan workspace: %v", err)}
	}
	if actual.Hash() != expectedHash {
		expected, loadErr := c.store.loadTree(expectedHash)
		if loadErr != nil {
			return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
				Sequence: checkpoint.Sequence, Reason: loadErr.Error()}
		}
		unexpected := DifferentPaths(expected, actual, 12)
		reason := ErrBlocked.Error()
		if op == OpRedo {
			reason = "workspace changed after undo"
		}
		c.recordTransition(ctx, transitionRecord{
			Op: op, Checkpoint: checkpoint.ID,
			FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
			FromTree: expectedHash, ToTree: target,
			Changes: unexpected, State: StateBlocked, Reason: reason,
		})
		return Result{
			Op: op, State: StateBlocked, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes,
			Reason: reason, Unexpected: unexpected,
		}
	}

	current, err := c.store.loadTree(expectedHash)
	if err != nil {
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Reason: err.Error()}
	}
	targetTree, err := c.store.loadTree(target)
	if err != nil {
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Reason: err.Error()}
	}

	plan := inflightRecord{
		Op:           op,
		CheckpointID: checkpoint.ID,
		FromTree:     expectedHash,
		ToTree:       target,
		Cursor:       cursor,
		Redo:         redo,
		Changes:      checkpoint.Changes,
	}
	c.recordTransition(ctx, transitionRecord{
		Op: op, Checkpoint: checkpoint.ID,
		FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
		FromTree: expectedHash, ToTree: target,
		Changes: checkpoint.Changes, State: StatePlanned, Reason: "planned",
	})
	if err := c.beginInflight(ctx, plan); err != nil {
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Reason: err.Error()}
	}
	c.recordTransition(ctx, transitionRecord{
		Op: op, Checkpoint: checkpoint.ID,
		FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
		FromTree: expectedHash, ToTree: target,
		Changes: checkpoint.Changes, State: StateExecuting, Reason: "executing",
	})

	if err := Apply(ctx, c.scope.Root, current, targetTree, c.store); err != nil {
		// The workspace may be partially written. Leaving the inflight record
		// in place is what makes the next start refuse to continue blindly.
		c.recordTransition(ctx, transitionRecord{
			Op: op, Checkpoint: checkpoint.ID,
			FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
			FromTree: expectedHash, ToTree: target,
			Changes: checkpoint.Changes, State: StateFailed, Reason: err.Error(),
		})
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes,
			Reason: "restore interrupted: " + err.Error()}
	}

	c.scanner.Forget(pathsOf(checkpoint.Changes)...)
	verified, residual, err := c.verifyRestore(ctx, current, targetTree)
	if err != nil {
		c.recordTransition(ctx, transitionRecord{
			Op: op, Checkpoint: checkpoint.ID,
			FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
			FromTree: expectedHash, ToTree: target,
			Changes: checkpoint.Changes, State: StateFailed, Reason: err.Error(),
		})
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes,
			Reason: "restore verification failed: " + err.Error()}
	}
	if !verified {
		reason := "recovery failed verification: workspace does not match the target checkpoint"
		c.recordTransition(ctx, transitionRecord{
			Op: op, Checkpoint: checkpoint.ID,
			FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
			FromTree: expectedHash, ToTree: target,
			Changes: checkpoint.Changes, State: StateFailed, Reason: reason,
		})
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes, Reason: reason}
	}

	if err := c.finishInflight(ctx, plan); err != nil {
		c.recordTransition(ctx, transitionRecord{
			Op: op, Checkpoint: checkpoint.ID,
			FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
			FromTree: expectedHash, ToTree: target,
			Changes: checkpoint.Changes, State: StateFailed, Reason: err.Error(),
		})
		return Result{Op: op, State: StateFailed, CheckpointID: checkpoint.ID,
			Sequence: checkpoint.Sequence, Files: checkpoint.Changes, Reason: err.Error()}
	}

	notes := make([]string, 0, 2)
	if len(residual) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d director%s left in place because they hold files outside the checkpoint",
			len(residual), plural(len(residual), "y", "ies")))
	}
	if skipped := checkpoint.Metadata["skipped"]; skipped != "" {
		notes = append(notes, "not covered by this checkpoint (too large or unsupported): "+skipped)
	}
	c.recordTransition(ctx, transitionRecord{
		Op: op, Checkpoint: checkpoint.ID,
		FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
		FromTree: expectedHash, ToTree: target,
		Changes: checkpoint.Changes, State: StateVerified,
	})

	return Result{
		Op: op, State: StateVerified, CheckpointID: checkpoint.ID,
		Sequence: checkpoint.Sequence, Files: checkpoint.Changes, Notes: notes,
	}
}

// verifyRestore re-scans the workspace and proves it matches target.
//
// Directories that existed in the expected state but not in the target are
// allowed to remain when they are non-empty: their remaining content is outside
// the checkpoint's scope, and deleting it would be exactly the silent
// destruction this package refuses to perform. They are reported, not ignored.
func (c *Controller) verifyRestore(ctx context.Context, expected, target *Tree) (bool, []string, error) {
	actual, err := c.scanner.Scan(ctx, nil)
	if err != nil {
		return false, nil, err
	}
	var residual []string
	for _, entry := range target.Entries {
		found, ok := actual.Lookup(entry.Path)
		if !ok || !equalEntry(entry, found) {
			return false, nil, nil
		}
	}
	for _, change := range Diff(expected, target) {
		if change.Status != ChangeDeleted {
			continue
		}
		entry, _ := expected.Lookup(change.Path)
		found, present := actual.Lookup(change.Path)
		if !present {
			continue
		}
		if entry.Kind == KindDir && found.Kind == KindDir {
			residual = append(residual, change.Path)
			continue
		}
		return false, nil, nil
	}
	return true, residual, nil
}

// inflightRecord is a planned transition persisted before the restore starts.
type inflightRecord struct {
	Op           Op
	CheckpointID string
	FromTree     string
	ToTree       string
	Cursor       string
	Redo         []string
	Changes      []Change
}

// beginInflight records the plan durably. A crash after this point is
// detectable; a crash before it changed nothing.
func (c *Controller) beginInflight(ctx context.Context, plan inflightRecord) error {
	changes, err := json.Marshal(plan.Changes)
	if err != nil {
		return fmt.Errorf("recovery: encode plan: %w", err)
	}
	redo, err := json.Marshal(plan.Redo)
	if err != nil {
		return fmt.Errorf("recovery: encode redo plan: %w", err)
	}
	_, err = c.store.db.Exec(
		`INSERT INTO inflight
		   (session_id, repository, op, checkpoint_id, from_tree, to_tree,
		    target_cursor, target_redo, changes, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, repository) DO UPDATE SET
		   op = excluded.op,
		   checkpoint_id = excluded.checkpoint_id,
		   from_tree = excluded.from_tree,
		   to_tree = excluded.to_tree,
		   target_cursor = excluded.target_cursor,
		   target_redo = excluded.target_redo,
		   changes = excluded.changes,
		   started_at = excluded.started_at`,
		c.scope.SessionID, c.scope.Repository, string(plan.Op), plan.CheckpointID,
		plan.FromTree, plan.ToTree, plan.Cursor, string(redo), string(changes), now(),
	)
	if err != nil {
		return fmt.Errorf("recovery: record in-flight transition: %w", err)
	}
	return nil
}

// finishInflight records the verified terminal state.
func (c *Controller) finishInflight(ctx context.Context, plan inflightRecord) error {
	return c.store.withTx(ctx, func(tx *sql.Tx) error {
		if err := writeTimelineTx(tx, c.scope, Timeline{Cursor: plan.Cursor, Redo: plan.Redo}); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`DELETE FROM inflight WHERE session_id = ? AND repository = ?`,
			c.scope.SessionID, c.scope.Repository,
		); err != nil {
			return fmt.Errorf("recovery: clear in-flight transition: %w", err)
		}
		return nil
	})
}

// resolveInflightLocked reconciles a transition interrupted by a crash.
//
// It never mutates the workspace. When the observed state is unambiguously the
// target, the transition had completed and only its bookkeeping is finished.
// When it is unambiguously the source, nothing was applied and the record is
// cleared. Anything else is reported for inspection and left in place.
func (c *Controller) resolveInflightLocked(ctx context.Context) (Result, bool) {
	plan, ok, err := c.readInflight()
	if err != nil {
		return Result{State: StateFailed, Reason: err.Error()}, false
	}
	if !ok {
		return Result{}, true
	}
	actual, err := c.scanner.Scan(ctx, nil)
	if err != nil {
		return Result{State: StateInterrupted, CheckpointID: plan.CheckpointID,
			Reason: fmt.Sprintf("%v: scan workspace: %v", ErrNeedsInspection, err)}, false
	}
	switch actual.Hash() {
	case plan.ToTree:
		if err := c.finishInflight(ctx, plan); err != nil {
			return Result{State: StateInterrupted, CheckpointID: plan.CheckpointID,
				Reason: fmt.Sprintf("%v: %v", ErrNeedsInspection, err)}, false
		}
		c.recordTransition(ctx, transitionRecord{
			Op: plan.Op, Checkpoint: plan.CheckpointID,
			FromCheckpoint: plan.CheckpointID, ToCheckpoint: plan.Cursor,
			FromTree: plan.FromTree, ToTree: plan.ToTree,
			Changes: plan.Changes, State: StateVerified,
			Reason: "completed before interruption",
		})
		return Result{}, true
	case plan.FromTree:
		if _, err := c.store.db.Exec(
			`DELETE FROM inflight WHERE session_id = ? AND repository = ?`,
			c.scope.SessionID, c.scope.Repository,
		); err != nil {
			return Result{State: StateInterrupted, CheckpointID: plan.CheckpointID,
				Reason: fmt.Sprintf("%v: %v", ErrNeedsInspection, err)}, false
		}
		c.recordTransition(ctx, transitionRecord{
			Op: plan.Op, Checkpoint: plan.CheckpointID,
			FromCheckpoint: plan.CheckpointID, ToCheckpoint: plan.Cursor,
			FromTree: plan.FromTree, ToTree: plan.ToTree,
			Changes: plan.Changes, State: StateInterrupted,
			Reason: "interrupted before any change was applied",
		})
		return Result{}, true
	default:
		return Result{
			State:        StateInterrupted,
			CheckpointID: plan.CheckpointID,
			Files:        plan.Changes,
			Reason: fmt.Sprintf(
				"%v: the previous %s did not reach a verified terminal state; "+
					"no automatic destructive action was taken", ErrNeedsInspection, plan.Op),
			Unexpected: DifferentPaths(treeOrNil(c.store, plan.ToTree), actual, 12),
		}, false
	}
}

// readInflight reads the scope's in-flight transition.
func (c *Controller) readInflight() (inflightRecord, bool, error) {
	var (
		plan    inflightRecord
		op      string
		redo    string
		changes string
	)
	err := c.store.db.QueryRow(
		`SELECT op, checkpoint_id, from_tree, to_tree, target_cursor, target_redo, changes
		 FROM inflight WHERE session_id = ? AND repository = ?`,
		c.scope.SessionID, c.scope.Repository,
	).Scan(&op, &plan.CheckpointID, &plan.FromTree, &plan.ToTree, &plan.Cursor, &redo, &changes)
	if errors.Is(err, sql.ErrNoRows) {
		return inflightRecord{}, false, nil
	}
	if err != nil {
		return inflightRecord{}, false, fmt.Errorf("recovery: read in-flight transition: %w", err)
	}
	plan.Op = Op(op)
	if err := json.Unmarshal([]byte(redo), &plan.Redo); err != nil {
		return inflightRecord{}, false, fmt.Errorf("%w: in-flight redo plan: %v", ErrCorrupt, err)
	}
	if err := json.Unmarshal([]byte(changes), &plan.Changes); err != nil {
		return inflightRecord{}, false, fmt.Errorf("%w: in-flight plan: %v", ErrCorrupt, err)
	}
	return plan, true, nil
}

// pendingRow is one captured but unpublished edit.
type pendingRow struct {
	ID         string
	Root       string
	Sequence   int64
	BeforeTree string
	Metadata   string
	StartedAt  string
	State      string
}

// repairPendingLocked publishes checkpoints whose before-state survived a crash.
func (c *Controller) repairPendingLocked(ctx context.Context) error {
	rows, err := c.pendingRows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.State != "open" {
			continue
		}
		if err := c.repairOne(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

// repairOne reconciles one pending capture.
func (c *Controller) repairOne(ctx context.Context, row pendingRow) error {
	var existing int
	err := c.store.db.QueryRow(`SELECT 1 FROM checkpoints WHERE id = ?`, row.ID).Scan(&existing)
	if err == nil {
		_, err := c.store.db.Exec(`DELETE FROM pending_edits WHERE id = ?`, row.ID)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("recovery: check pending checkpoint: %w", err)
	}
	if err := c.store.verifyTree(row.BeforeTree); err != nil {
		// The recovery point is not trustworthy. Refusing to publish it is the
		// only honest outcome; the record stays visible as an inspection item.
		if _, markErr := c.store.db.Exec(
			`UPDATE pending_edits SET state = 'corrupt' WHERE id = ?`, row.ID); markErr != nil {
			return fmt.Errorf("recovery: mark corrupt pending edit: %w", markErr)
		}
		c.recordTransition(ctx, transitionRecord{
			Op: OpUndo, Checkpoint: row.ID, ToCheckpoint: row.ID,
			FromTree: row.BeforeTree, ToTree: row.BeforeTree,
			State: StateInterrupted, Reason: err.Error(),
		})
		return nil
	}
	before, err := c.store.loadTree(row.BeforeTree)
	if err != nil {
		return err
	}
	after, err := c.store.captureTree(ctx, row.Root)
	if err != nil {
		return fmt.Errorf("recovery: repair interrupted edit: %w", err)
	}
	changes := Diff(before, after)
	if len(changes) == 0 {
		_, err := c.store.db.Exec(`DELETE FROM pending_edits WHERE id = ?`, row.ID)
		return err
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(row.Metadata), &metadata); err != nil {
		metadata = map[string]string{}
	}
	metadata["interrupted"] = "true"

	treeHash := after.Hash()
	return c.store.withTx(ctx, func(tx *sql.Tx) error {
		if err := putTree(ctx, tx, after); err != nil {
			return err
		}
		timeline, err := readTimelineTx(tx, c.scope)
		if err != nil {
			return err
		}
		changesJSON, err := json.Marshal(changes)
		if err != nil {
			return fmt.Errorf("recovery: encode changes: %w", err)
		}
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("recovery: encode metadata: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO checkpoints
			   (id, session_id, repository, root, seq, parent_id, created_at,
			    before_tree, after_tree, changes, metadata, interrupted)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			row.ID, c.scope.SessionID, c.scope.Repository, row.Root, row.Sequence,
			timeline.Cursor, row.StartedAt, row.BeforeTree, treeHash,
			string(changesJSON), string(metadataJSON),
		); err != nil {
			return fmt.Errorf("recovery: publish repaired checkpoint: %w", err)
		}
		if err := writeTimelineTx(tx, c.scope, Timeline{Cursor: row.ID, Redo: nil}); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM pending_edits WHERE id = ?`, row.ID); err != nil {
			return fmt.Errorf("recovery: clear pending edit: %w", err)
		}
		return nil
	})
}

// pendingRows returns this scope's pending captures oldest first.
func (c *Controller) pendingRows(ctx context.Context) ([]pendingRow, error) {
	rows, err := c.store.db.Query(
		`SELECT id, root, seq, before_tree, metadata, started_at, state
		 FROM pending_edits WHERE session_id = ? AND repository = ?
		 ORDER BY started_at ASC`,
		c.scope.SessionID, c.scope.Repository)
	if err != nil {
		return nil, fmt.Errorf("recovery: read pending edits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []pendingRow
	for rows.Next() {
		var row pendingRow
		if err := rows.Scan(&row.ID, &row.Root, &row.Sequence, &row.BeforeTree,
			&row.Metadata, &row.StartedAt, &row.State); err != nil {
			return nil, fmt.Errorf("recovery: scan pending edit: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// corruptPending returns a description of an unusable pending capture, if any.
func (c *Controller) corruptPending(ctx context.Context) (string, error) {
	rows, err := c.pendingRows(ctx)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.State == "corrupt" {
			return fmt.Sprintf(
				"%v: the recovery point for an interrupted edit (%s) failed its integrity check",
				ErrNeedsInspection, short(row.ID)), nil
		}
	}
	return "", nil
}

// repairPending reconciles interrupted captures, taking the store lock.
func (c *Controller) repairPending(ctx context.Context) error {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	return c.repairPendingLocked(ctx)
}

// Inspection reports whether this scope needs human attention before any
// further recovery, without mutating anything.
func (c *Controller) Inspection(ctx context.Context) (string, bool) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if err := c.repairPendingLocked(ctx); err != nil {
		return err.Error(), true
	}
	if corrupt, err := c.corruptPending(ctx); err != nil {
		return err.Error(), true
	} else if corrupt != "" {
		return corrupt, true
	}
	plan, ok, err := c.readInflight()
	if err != nil {
		return err.Error(), true
	}
	if !ok {
		return "", false
	}
	return fmt.Sprintf(
		"%v: the previous %s did not reach a verified terminal state; no automatic destructive action was taken",
		ErrNeedsInspection, plan.Op), true
}

// recordTransition appends one audit row.
func (c *Controller) recordTransition(ctx context.Context, record transitionRecord) {
	record.Changes = sortedChanges(record.Changes)
	_ = appendTransition(ctx, c.store.db, c.scope, record)
}

// boundaryMetadata renders an edit boundary for storage.
func boundaryMetadata(boundary agent.EditBoundary) map[string]string {
	return map[string]string{
		"turn":  fmt.Sprintf("%d", boundary.Turn),
		"tools": joinNames(boundary.Tools),
	}
}

// joinNames joins a sorted name list for display and metadata.
func joinNames(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

// bounded returns at most limit values.
func bounded(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

// pathsOf returns the paths named by changes.
func pathsOf(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		out = append(out, change.Path)
	}
	return out
}

// cloneMetadata copies a metadata map.
func cloneMetadata(source map[string]string) map[string]string {
	out := make(map[string]string, len(source)+1)
	for key, value := range source {
		out[key] = value
	}
	return out
}

// treeOrNil loads a tree for a diagnostic comparison. A missing tree must not
// change the refusal it is explaining, so the error is deliberately swallowed.
func treeOrNil(store *Store, hash string) *Tree {
	tree, err := store.loadTree(hash)
	if err != nil {
		return nil
	}
	return tree
}

// plural picks a suffix by count.
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// Ensure the controller satisfies the loop's capture-only seam.
var _ agent.EditJournal = (*Controller)(nil)
