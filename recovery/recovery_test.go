package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

// testHarness is one workspace and recovery store.
type testHarness struct {
	t      *testing.T
	root   string
	store  *Store
	path   string
	scope  Scope
	ctrl   *Controller
	closed bool
}

// newHarness creates a workspace and a recovery store in temporary directories.
func newHarness(t *testing.T, sessionID int64) *testHarness {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	path := filepath.Join(base, "state", "recovery.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	h := &testHarness{t: t, root: root, store: store, path: path}
	h.scope, err = NewScope(sessionID, root)
	if err != nil {
		t.Fatalf("new scope: %v", err)
	}
	h.ctrl, err = store.Controller(h.scope)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	t.Cleanup(func() { h.close() })
	return h
}

func (h *testHarness) close() {
	if h == nil || h.closed {
		return
	}
	h.closed = true
	if err := h.store.Close(); err != nil {
		h.t.Errorf("close store: %v", err)
	}
}

// reopen simulates a process restart against the same store file.
func (h *testHarness) reopen() {
	h.t.Helper()
	h.close()
	if err := os.Remove(h.path + "-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.t.Fatalf("clear wal: %v", err)
	}
	store, err := Open(h.path)
	if err != nil {
		h.t.Fatalf("reopen store: %v", err)
	}
	h.closed = false
	h.store = store
	h.ctrl, err = store.Controller(h.scope)
	if err != nil {
		h.t.Fatalf("controller after reopen: %v", err)
	}
}

// write creates or replaces a workspace file.
func (h *testHarness) write(rel, content string) {
	h.t.Helper()
	path := filepath.Join(h.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatalf("write %s: %v", rel, err)
	}
}

// read returns a workspace file's content.
func (h *testHarness) read(rel string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(rel)))
	if err != nil {
		h.t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// exists reports whether a workspace path is present.
func (h *testHarness) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(h.root, filepath.FromSlash(rel)))
	return err == nil
}

// begin opens an edit transaction, failing the test if the checkpoint cannot be
// captured.
func (h *testHarness) begin(tools ...string) agent.EditTransaction {
	h.t.Helper()
	txn, err := h.ctrl.BeginEdit(context.Background(), agent.EditBoundary{Tools: tools})
	if err != nil {
		h.t.Fatalf("begin edit: %v", err)
	}
	return txn
}

// commit publishes an edit transaction.
func (h *testHarness) commit(txn agent.EditTransaction) {
	h.t.Helper()
	if err := txn.Commit(context.Background()); err != nil {
		h.t.Fatalf("commit edit: %v", err)
	}
}

// edit runs one simulated agent mutation as a single checkpoint.
func (h *testHarness) edit(fn func()) {
	h.t.Helper()
	txn := h.begin("write")
	fn()
	h.commit(txn)
}

// countRows returns a table row count.
func (h *testHarness) countRows(query string, args ...any) int {
	h.t.Helper()
	var count int
	if err := h.store.db.QueryRow(query, args...).Scan(&count); err != nil {
		h.t.Fatalf("count: %v", err)
	}
	return count
}

func TestTimelineSurvivesRestart(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	h.reopen()

	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo after restart: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "base\n" {
		t.Fatalf("content after restart-undo = %q", got)
	}
	h.reopen()
	if result := h.ctrl.Redo(context.Background()); !result.Verified() {
		t.Fatalf("redo after restart: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Fatalf("content after restart-redo = %q", got)
	}
}

func TestUndoRestoresModifiedFile(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "package a\n")
	h.edit(func() { h.write("a.go", "package a\n\nfunc X() {}\n") })
	if got := h.read("a.go"); !strings.Contains(got, "func X") {
		t.Fatalf("agent edit not applied: %q", got)
	}

	result := h.ctrl.Undo(context.Background())
	if !result.Verified() {
		t.Fatalf("undo state = %s (%s)", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "package a\n" {
		t.Errorf("content after undo = %q, want the pre-edit bytes", got)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "a.go" || result.Files[0].Status != ChangeModified {
		t.Errorf("reported changes = %+v, want one modification of a.go", result.Files)
	}
}

func TestUndoRevertsCreateDeleteAndRename(t *testing.T) {
	h := newHarness(t, 1)
	h.write("keep.go", "keep\n")
	h.write("gone.go", "gone\n")
	h.write("old_name.go", "renamed\n")

	h.edit(func() {
		h.write("created.go", "created\n")
		if err := os.Remove(filepath.Join(h.root, "gone.go")); err != nil {
			t.Fatalf("remove gone.go: %v", err)
		}
		if err := os.Rename(filepath.Join(h.root, "old_name.go"), filepath.Join(h.root, "new_name.go")); err != nil {
			t.Fatalf("rename: %v", err)
		}
	})

	result := h.ctrl.Undo(context.Background())
	if !result.Verified() {
		t.Fatalf("undo state = %s (%s)", result.State, result.Reason)
	}
	if h.exists("created.go") {
		t.Error("a created file survived the undo")
	}
	if got := h.read("gone.go"); got != "gone\n" {
		t.Errorf("deleted file restored as %q", got)
	}
	if !h.exists("old_name.go") || h.exists("new_name.go") {
		t.Error("rename was not reversed")
	}
	if got := h.read("keep.go"); got != "keep\n" {
		t.Errorf("an untouched file changed: %q", got)
	}
}

func TestUndoBlocksOnUserChangeAndTouchesNothing(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	// The user edits a different file after the agent's turn.
	h.write("user.txt", "mine\n")

	result := h.ctrl.Undo(context.Background())
	if result.State != StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", result.State)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Errorf("undo mutated the workspace despite blocking: %q", got)
	}
	if got := h.read("user.txt"); got != "mine\n" {
		t.Errorf("the user file was destroyed: %q", got)
	}
	if len(result.Unexpected) == 0 {
		t.Error("the refusal did not name the unexpected path")
	}
}

func TestUndoBlocksWhenTheUserEditedTheSameFile(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	h.write("a.go", "user\n")

	result := h.ctrl.Undo(context.Background())
	if result.State != StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", result.State)
	}
	if got := h.read("a.go"); got != "user\n" {
		t.Errorf("the user's version was overwritten: %q", got)
	}
}

func TestRedoRestoresTheExactAgentState(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() {
		h.write("a.go", "agent\n")
		h.write("new.go", "new\n")
	})
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo state = %s (%s)", result.State, result.Reason)
	}
	result := h.ctrl.Redo(context.Background())
	if !result.Verified() {
		t.Fatalf("redo state = %s (%s)", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Errorf("redo content = %q", got)
	}
	if got := h.read("new.go"); got != "new\n" {
		t.Errorf("redo did not restore the created file: %q", got)
	}
}

func TestRedoBlocksAfterTheUserEditedFollowingUndo(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s", result.State)
	}
	h.write("a.go", "user\n")

	result := h.ctrl.Redo(context.Background())
	if result.State != StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", result.State)
	}
	if got := h.read("a.go"); got != "user\n" {
		t.Errorf("redo overwrote the user change: %q", got)
	}
}

func TestMultipleUndoAndRedo(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "v0\n")
	h.edit(func() { h.write("a.go", "v1\n") })
	h.edit(func() { h.write("a.go", "v2\n") })

	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("first undo: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "v1\n" {
		t.Fatalf("after first undo = %q", got)
	}
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("second undo: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "v0\n" {
		t.Fatalf("after second undo = %q", got)
	}
	if result := h.ctrl.Redo(context.Background()); !result.Verified() {
		t.Fatalf("first redo: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "v1\n" {
		t.Fatalf("after first redo = %q", got)
	}
	if result := h.ctrl.Redo(context.Background()); !result.Verified() {
		t.Fatalf("second redo: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "v2\n" {
		t.Fatalf("after second redo = %q", got)
	}
	if result := h.ctrl.Redo(context.Background()); result.State != StateEmpty {
		t.Fatalf("third redo = %s, want EMPTY", result.State)
	}
}

func TestUndoAtBaselineIsEmpty(t *testing.T) {
	h := newHarness(t, 1)
	result := h.ctrl.Undo(context.Background())
	if result.State != StateEmpty {
		t.Fatalf("state = %s, want EMPTY", result.State)
	}
}

func TestUserUntrackedFileSurvivesUndo(t *testing.T) {
	h := newHarness(t, 1)
	h.write("user.txt", "existing\n")
	h.edit(func() { h.write("agent.go", "created\n") })
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s %s", result.State, result.Reason)
	}
	if !h.exists("user.txt") {
		t.Error("a user file that predated the edit was deleted")
	}
	if h.exists("agent.go") {
		t.Error("the agent-created file survived the undo")
	}
}

func TestNewEditAfterUndoKeepsTheUndoneBranch(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "v0\n")
	h.edit(func() { h.write("a.go", "v1\n") }) // checkpoint 1
	h.edit(func() { h.write("a.go", "v2\n") }) // checkpoint 2

	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s %s", result.State, result.Reason)
	}
	h.edit(func() { h.write("a.go", "branch\n") }) // checkpoint 3

	if result := h.ctrl.Redo(context.Background()); result.State != StateEmpty {
		t.Fatalf("redo after a new edit = %s, want EMPTY (explicit redo invalidation)", result.State)
	}
	// The undone checkpoint must still exist: invalidation clears reachability
	// on the active branch, never the recorded recovery data.
	history, err := h.store.History(h.scope, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history has %d checkpoints, want 3 (the undone branch is preserved)", len(history))
	}
	if history[1].AfterTree == history[2].BeforeTree {
		t.Errorf("the undone checkpoint's recorded after-state was rewritten")
	}
}

func TestSessionIsolation(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "session-a\n") })

	otherScope, err := NewScope(2, h.root)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	other, err := h.store.Controller(otherScope)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	result := other.Undo(context.Background())
	if result.State != StateEmpty {
		t.Fatalf("session B undo = %s, want EMPTY", result.State)
	}
	if got := h.read("a.go"); got != "session-a\n" {
		t.Errorf("session A's workspace was restored by session B: %q", got)
	}
}

func TestCheckpointIsImmutableAcrossUndoAndRedo(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	before, err := h.store.History(h.scope, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s", result.State)
	}
	if result := h.ctrl.Redo(context.Background()); !result.Verified() {
		t.Fatalf("redo: %s", result.State)
	}
	after, err := h.store.History(h.scope, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(before) != len(after) || !checkpointsEqual(before[0], after[0]) {
		t.Errorf("the checkpoint changed across undo/redo:\nbefore %+v\nafter  %+v", before[0], after[0])
	}
}

// checkpointsEqual compares the immutable fields of two checkpoints.
func checkpointsEqual(a, b Checkpoint) bool {
	if a.ID != b.ID || a.Sequence != b.Sequence || a.Parent != b.Parent ||
		a.BeforeTree != b.BeforeTree || a.AfterTree != b.AfterTree ||
		a.Interrupted != b.Interrupted || len(a.Changes) != len(b.Changes) {
		return false
	}
	for i := range a.Changes {
		if a.Changes[i] != b.Changes[i] {
			return false
		}
	}
	return true
}

func TestCorruptBlobFailsClosed(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	if _, err := h.store.db.Exec(
		`UPDATE blobs SET content = ? WHERE hash = (
		   SELECT hash FROM blobs ORDER BY size DESC LIMIT 1)`, []byte("tampered")); err != nil {
		t.Fatalf("tamper blob: %v", err)
	}

	result := h.ctrl.Undo(context.Background())
	if result.State != StateFailed {
		t.Fatalf("state = %s, want FAILED", result.State)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Errorf("a corrupt checkpoint mutated the workspace: %q", got)
	}
}

func TestMissingBlobFailsClosed(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	if _, err := h.store.db.Exec(`DELETE FROM blobs`); err != nil {
		t.Fatalf("delete blobs: %v", err)
	}
	result := h.ctrl.Undo(context.Background())
	if result.State != StateFailed {
		t.Fatalf("state = %s, want FAILED", result.State)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Errorf("workspace mutated with a missing blob: %q", got)
	}
}

func TestInterruptedCheckpointIsRepairedOnRestart(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	if _, err := h.ctrl.BeginEdit(context.Background(), agent.EditBoundary{Tools: []string{"write"}}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// The mutation lands, then the process dies before Commit.
	h.write("a.go", "agent\n")
	h.reopen()

	history, err := h.store.History(h.scope, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d checkpoints after repair, want 1", len(history))
	}
	if !history[0].Interrupted {
		t.Error("a repaired checkpoint was not marked interrupted")
	}
	result := h.ctrl.Undo(context.Background())
	if !result.Verified() {
		t.Fatalf("undo after repair: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "base\n" {
		t.Errorf("content after undo = %q", got)
	}
}

func TestInterruptedCheckpointWithMissingBeforeBlobIsRefused(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	if _, err := h.ctrl.BeginEdit(context.Background(), agent.EditBoundary{Tools: []string{"write"}}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	h.write("a.go", "agent\n")
	if _, err := h.store.db.Exec(`DELETE FROM blobs`); err != nil {
		t.Fatalf("delete blobs: %v", err)
	}
	h.reopen()

	history, err := h.store.History(h.scope, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("a checkpoint with no recoverable before-state was published")
	}
	result := h.ctrl.Undo(context.Background())
	if result.State != StateInterrupted {
		t.Fatalf("state = %s, want INTERRUPTED", result.State)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Errorf("refused recovery mutated the workspace: %q", got)
	}
}

func TestInterruptedRestoreRequiresInspection(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })

	// Simulate a crash mid-restore: a plan is durable, and the workspace is
	// neither the source nor the target state.
	agentTree := hashOfWorkspace(t, h, "agent\n")
	baseTree := hashOfWorkspace(t, h, "base\n")
	h.write("a.go", "partial\n")
	plan := inflightRecord{
		Op:           OpUndo,
		CheckpointID: "cp-test",
		FromTree:     agentTree,
		ToTree:       baseTree,
		Cursor:       "",
		Redo:         []string{"cp-test"},
	}
	h.ctrl.store.mu.Lock()
	err := h.ctrl.beginInflight(context.Background(), plan)
	h.ctrl.store.mu.Unlock()
	if err != nil {
		t.Fatalf("begin inflight: %v", err)
	}

	result := h.ctrl.Undo(context.Background())
	if result.State != StateInterrupted {
		t.Fatalf("state = %s, want INTERRUPTED", result.State)
	}
	if got := h.read("a.go"); got != "partial\n" {
		t.Errorf("an interrupted restore mutated the workspace: %q", got)
	}
}

func TestInterruptedRestoreThatCompletedIsFinalized(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	history, err := h.store.History(h.scope, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %v (%d)", err, len(history))
	}
	cp := history[0]

	// The restore ran to completion, then the process died before bookkeeping.
	h.write("a.go", "base\n")
	plan := inflightRecord{
		Op:           OpUndo,
		CheckpointID: cp.ID,
		FromTree:     cp.AfterTree,
		ToTree:       cp.BeforeTree,
		Cursor:       "",
		Redo:         []string{cp.ID},
		Changes:      cp.Changes,
	}
	h.ctrl.store.mu.Lock()
	err = h.ctrl.beginInflight(context.Background(), plan)
	h.ctrl.store.mu.Unlock()
	if err != nil {
		t.Fatalf("begin inflight: %v", err)
	}

	// The next operation reconciles the completed restore, then runs normally.
	result := h.ctrl.Redo(context.Background())
	if !result.Verified() {
		t.Fatalf("redo after reconciliation: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "agent\n" {
		t.Errorf("content after reconciled redo = %q", got)
	}
}

func TestGitMetadataAndIgnoredFilesAreOutsideTheCheckpoint(t *testing.T) {
	h := newHarness(t, 1)
	h.write(".git/HEAD", "ref: refs/heads/main\n")
	h.write(".gitignore", "*.log\n")
	h.write("a.go", "base\n")

	h.edit(func() {
		h.write("a.go", "agent\n")
		h.write("debug.log", "noise\n")      // ignored
		h.write(".git/HEAD", "ref: other\n") // VCS metadata
	})

	history, err := h.store.History(h.scope, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %v (%d)", err, len(history))
	}
	for _, path := range pathsOf(history[0].Changes) {
		if path == ".git/HEAD" || path == "debug.log" {
			t.Errorf("checkpoint captured out-of-scope path %q", path)
		}
	}
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s %s", result.State, result.Reason)
	}
	if got := h.read("a.go"); got != "base\n" {
		t.Errorf("tracked file not restored: %q", got)
	}
	if got := h.read("debug.log"); got != "noise\n" {
		t.Errorf("an ignored file was touched by recovery: %q", got)
	}
	if got := h.read(".git/HEAD"); got != "ref: other\n" {
		t.Errorf("git metadata was touched by recovery: %q", got)
	}
}

func TestIdenticalContentIsStoredOnce(t *testing.T) {
	h := newHarness(t, 1)
	h.write("one.txt", "same\n")
	h.write("two.txt", "same\n")
	h.edit(func() { h.write("three.txt", "same\n") })
	if got := h.countRows(`SELECT COUNT(*) FROM blobs`); got != 1 {
		t.Errorf("blob count = %d, want 1 for identical content", got)
	}
}

func TestExecutableBitIsRestored(t *testing.T) {
	h := newHarness(t, 1)
	h.write("run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(h.root, "run.sh"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	h.edit(func() {
		if err := os.Chmod(filepath.Join(h.root, "run.sh"), 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
	})
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s %s", result.State, result.Reason)
	}
	info, err := os.Stat(filepath.Join(h.root, "run.sh"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("mode = %v, want the executable bit restored", info.Mode().Perm())
	}
}

func TestSymlinkIsRestored(t *testing.T) {
	h := newHarness(t, 1)
	h.write("target.txt", "target\n")
	if err := os.Symlink("target.txt", filepath.Join(h.root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	h.edit(func() {
		if err := os.Remove(filepath.Join(h.root, "link.txt")); err != nil {
			t.Fatalf("remove link: %v", err)
		}
	})
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s %s", result.State, result.Reason)
	}
	target, err := os.Readlink(filepath.Join(h.root, "link.txt"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "target.txt" {
		t.Errorf("link target = %q", target)
	}
}

func TestWrongWorkspaceCheckpointIsRefused(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	history, err := h.store.History(h.scope, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %v (%d)", err, len(history))
	}
	other, err := NewScope(1, t.TempDir())
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, err := h.store.Checkpoint(other, history[0].ID); !errors.Is(err, ErrWrongWorkspace) {
		t.Fatalf("error = %v, want ErrWrongWorkspace", err)
	}
}

func TestAuditRecordNamesTheTransitionChain(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "one\n") })
	h.edit(func() { h.write("a.go", "two\n") })
	history, err := h.store.History(h.scope, 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("history: %v (%d)", err, len(history))
	}
	latest := history[1]
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s %s", result.State, result.Reason)
	}
	transitions, err := h.store.Transitions(h.scope, 0)
	if err != nil {
		t.Fatalf("transitions: %v", err)
	}
	var verified *Transition
	for i := range transitions {
		if transitions[i].State == StateVerified {
			verified = &transitions[i]
			break
		}
	}
	if verified == nil {
		t.Fatal("no VERIFIED audit row was written")
	}
	if verified.Checkpoint != latest.ID {
		t.Errorf("checkpoint = %q, want %q", verified.Checkpoint, latest.ID)
	}
	if verified.FromCheckpoint != latest.ID {
		t.Errorf("from checkpoint = %q, want the undone edit", verified.FromCheckpoint)
	}
	if verified.ToCheckpoint != latest.Parent {
		t.Errorf("to checkpoint = %q, want its parent %q", verified.ToCheckpoint, latest.Parent)
	}
	if verified.At.IsZero() {
		t.Error("audit row has no timestamp")
	}
}

func TestAuditTrailDistinguishesStates(t *testing.T) {
	h := newHarness(t, 1)
	h.write("a.go", "base\n")
	h.edit(func() { h.write("a.go", "agent\n") })
	if result := h.ctrl.Undo(context.Background()); !result.Verified() {
		t.Fatalf("undo: %s", result.State)
	}
	h.write("a.go", "user\n")
	if result := h.ctrl.Redo(context.Background()); result.State != StateBlocked {
		t.Fatalf("redo: %s", result.State)
	}
	transitions, err := h.store.Transitions(h.scope, 0)
	if err != nil {
		t.Fatalf("transitions: %v", err)
	}
	var sawVerified, sawBlocked bool
	for _, transition := range transitions {
		switch transition.State {
		case StateVerified:
			sawVerified = true
		case StateBlocked:
			sawBlocked = true
		}
	}
	if !sawVerified || !sawBlocked {
		t.Errorf("audit trail = %+v, want both VERIFIED and BLOCKED", transitions)
	}
}

func TestFailedCaptureRefusesTheEdit(t *testing.T) {
	h := newHarness(t, 1)
	// A workspace that cannot be scanned must not produce a recovery point; the
	// journal therefore refuses the transaction.
	h.scope.Root = filepath.Join(h.root, "missing")
	controller, err := h.store.Controller(h.scope)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	if _, err := controller.BeginEdit(context.Background(), agent.EditBoundary{}); err == nil {
		t.Fatal("BeginEdit succeeded against a missing workspace")
	}
}

// hashOfWorkspace captures a workspace after replacing a.go with content.
func hashOfWorkspace(t *testing.T, h *testHarness, content string) string {
	t.Helper()
	h.write("a.go", content)
	tree, err := h.store.captureTree(context.Background(), h.root)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	return tree.Hash()
}
