package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/state"
	"github.com/PizenLabs/ohgo/internal/vcs"
	_ "modernc.org/sqlite"
)

// Schema is applied on open and is idempotent.
//
// Recovery data is deliberately separate from the session database. A session's
// transcript is a conversation record; this is a workspace record with its own
// retention, integrity, and failure modes. Keeping them apart means a corrupt
// recovery store cannot make a conversation unreadable, and clearing
// conversations cannot silently delete the ability to undo an edit.
const schema = `
CREATE TABLE IF NOT EXISTS blobs (
    hash    TEXT PRIMARY KEY,
    size    INTEGER NOT NULL,
    content BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS trees (
    hash        TEXT PRIMARY KEY,
    entry_count INTEGER NOT NULL,
    manifest    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS checkpoints (
    id          TEXT PRIMARY KEY,
    session_id  INTEGER NOT NULL,
    repository  TEXT NOT NULL,
    root        TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    parent_id   TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    before_tree TEXT NOT NULL,
    after_tree  TEXT NOT NULL,
    changes     TEXT NOT NULL,
    metadata    TEXT NOT NULL DEFAULT '{}',
    interrupted INTEGER NOT NULL DEFAULT 0,
    UNIQUE(session_id, repository, seq)
);

CREATE INDEX IF NOT EXISTS checkpoints_by_scope ON checkpoints(session_id, repository, seq);

CREATE TABLE IF NOT EXISTS timelines (
    session_id INTEGER NOT NULL,
    repository TEXT NOT NULL,
    cursor_id  TEXT NOT NULL DEFAULT '',
    redo_stack TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (session_id, repository)
);

CREATE TABLE IF NOT EXISTS pending_edits (
    id          TEXT PRIMARY KEY,
    session_id  INTEGER NOT NULL,
    repository  TEXT NOT NULL,
    root        TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    before_tree TEXT NOT NULL,
    metadata    TEXT NOT NULL DEFAULT '{}',
    state       TEXT NOT NULL DEFAULT 'open',
    started_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS pending_by_scope ON pending_edits(session_id, repository);

CREATE TABLE IF NOT EXISTS inflight (
    session_id       INTEGER NOT NULL,
    repository       TEXT NOT NULL,
    op               TEXT NOT NULL,
    checkpoint_id    TEXT NOT NULL,
    from_tree        TEXT NOT NULL,
    to_tree          TEXT NOT NULL,
    target_cursor    TEXT NOT NULL DEFAULT '',
    target_redo      TEXT NOT NULL DEFAULT '[]',
    changes          TEXT NOT NULL,
    started_at       TEXT NOT NULL,
    PRIMARY KEY (session_id, repository)
);

CREATE TABLE IF NOT EXISTS transitions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      INTEGER NOT NULL,
    repository      TEXT NOT NULL,
    op              TEXT NOT NULL,
    checkpoint_id   TEXT NOT NULL,
    from_checkpoint TEXT NOT NULL DEFAULT '',
    to_checkpoint   TEXT NOT NULL DEFAULT '',
    from_tree       TEXT NOT NULL,
    to_tree         TEXT NOT NULL,
    changes         TEXT NOT NULL,
    state           TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    at              TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS transitions_by_scope ON transitions(session_id, repository, id);
`

// Errors, matched with errors.Is.
var (
	// ErrNoCheckpoint means the timeline has nothing to move to.
	ErrNoCheckpoint = errors.New("recovery: no checkpoint")
	// ErrBlocked means the workspace does not match the state the transition
	// expects. No workspace mutation was performed.
	ErrBlocked = errors.New("recovery: workspace changed outside ohgo")
	// ErrNeedsInspection means an earlier recovery operation did not reach a
	// verified terminal state.
	ErrNeedsInspection = errors.New("recovery: previous recovery operation requires inspection")
	// ErrCorrupt means recorded recovery data failed its integrity check.
	ErrCorrupt = errors.New("recovery: recovery data is corrupt")
	// ErrWrongWorkspace means a checkpoint belongs to a different workspace.
	ErrWrongWorkspace = errors.New("recovery: checkpoint belongs to another workspace")
)

// Scope identifies one session's edit timeline in one workspace.
type Scope struct {
	// SessionID is the ohgo session the timeline belongs to.
	SessionID int64
	// Root is the absolute workspace root.
	Root string
	// Repository is the workspace's stable identity. For a git repository it
	// is the shared git directory, so two worktrees of one repository are not
	// mistaken for unrelated workspaces; otherwise it is the root itself.
	Repository string
}

// NewScope resolves the identity of a workspace.
func NewScope(sessionID int64, root string) (Scope, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Scope{}, fmt.Errorf("recovery: resolve root %q: %w", root, err)
	}
	abs = filepath.Clean(abs)
	repository := abs
	if info, err := vcs.Discover(abs); err == nil && info != nil {
		repository = info.CommonDir
	}
	return Scope{SessionID: sessionID, Root: abs, Repository: repository}, nil
}

// Store is the persistent recovery store.
type Store struct {
	db   *sql.DB
	path string
	// mu serialises recovery transitions in this process. Restores mutate the
	// workspace, so two of them overlapping would make verification
	// meaningless.
	mu sync.Mutex
	// scanners caches per-root hash state so an unchanged file is not re-read.
	scanners map[string]*Scanner
	// known is the set of blob hashes already present. It saves a query per
	// file during capture; a stale entry can only cause a redundant
	// INSERT OR IGNORE, never a missing blob.
	known map[string]struct{}
}

// Open opens or creates the recovery store at path.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("recovery: store path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("recovery: create %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("recovery: open %s: %w", path, err)
	}
	// One writer connection keeps recovery transitions serialised and avoids
	// the driver's "database is locked" path. Nested queries never happen: a
	// transaction is passed down explicitly.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		// Durable commit is the whole point: a checkpoint published before a
		// crash must still be there after it.
		`PRAGMA synchronous=FULL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("recovery: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recovery: apply schema: %w", err)
	}
	// Captured content is source code, so the file is private to the user.
	// Best-effort: a filesystem without POSIX modes is not a reason to refuse
	// recovery, and the containing directory is already 0700.
	_ = os.Chmod(path, 0o600)
	return &Store{
		db:       db,
		path:     path,
		scanners: map[string]*Scanner{},
		known:    map[string]struct{}{},
	}, nil
}

// StorePath returns where a workspace's recovery store lives.
//
// The path is keyed by the workspace's repository identity and lives under the
// machine-level state directory, outside every workspace. Per-repository files
// keep two ohgo processes working in different repositories from serialising
// behind one write lock, and they keep a workspace's recovery history together
// for inspection or deletion.
func StorePath(root string) (string, error) {
	scope, err := NewScope(0, root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(scope.Repository))
	return state.Path("recovery", hex.EncodeToString(sum[:8]), "recovery.db")
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path returns the store file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// scannerFor returns the shared scanner for a root.
func (s *Store) scannerFor(root string) *Scanner {
	if scanner, ok := s.scanners[root]; ok {
		return scanner
	}
	scanner := NewScanner(root)
	s.scanners[root] = scanner
	return scanner
}

// Controller returns the recovery façade for one session and workspace.
//
// It repairs any interrupted edit checkpoint first: a capture that was durable
// before the mutation is a complete recovery point even when the process died
// before publishing it. Repair never touches the workspace.
func (s *Store) Controller(scope Scope) (*Controller, error) {
	if s == nil {
		return nil, errors.New("recovery: store is not open")
	}
	if scope.Root == "" || scope.Repository == "" {
		return nil, errors.New("recovery: scope requires a root and repository")
	}
	if err := s.ensureTimeline(scope); err != nil {
		return nil, err
	}
	controller := &Controller{
		store:   s,
		scope:   scope,
		scanner: s.scannerFor(scope.Root),
	}
	if err := controller.repairPending(context.Background()); err != nil {
		return nil, err
	}
	return controller, nil
}

// ensureTimeline creates the scope's timeline row when it is missing.
func (s *Store) ensureTimeline(scope Scope) error {
	_, err := s.db.Exec(
		`INSERT INTO timelines (session_id, repository, cursor_id, redo_stack, updated_at)
		 VALUES (?, ?, '', '[]', ?)
		 ON CONFLICT(session_id, repository) DO NOTHING`,
		scope.SessionID, scope.Repository, now(),
	)
	if err != nil {
		return fmt.Errorf("recovery: create timeline: %w", err)
	}
	return nil
}

// Timeline is the persisted position in one session's edit history.
type Timeline struct {
	// Cursor is the checkpoint currently applied to the workspace, or empty
	// when the workspace is at the pre-edit baseline.
	Cursor string
	// Redo is the stack of undone checkpoints, most recently undone last. It is
	// cleared when a new edit is recorded, but the checkpoints it named are
	// never deleted.
	Redo []string
}

// Timeline reads the scope's position.
func (s *Store) Timeline(scope Scope) (Timeline, error) {
	var cursor, redo string
	err := s.db.QueryRow(
		`SELECT cursor_id, redo_stack FROM timelines WHERE session_id = ? AND repository = ?`,
		scope.SessionID, scope.Repository,
	).Scan(&cursor, &redo)
	if errors.Is(err, sql.ErrNoRows) {
		return Timeline{}, nil
	}
	if err != nil {
		return Timeline{}, fmt.Errorf("recovery: read timeline: %w", err)
	}
	var stack []string
	if err := json.Unmarshal([]byte(redo), &stack); err != nil {
		return Timeline{}, fmt.Errorf("%w: timeline redo stack: %v", ErrCorrupt, err)
	}
	return Timeline{Cursor: cursor, Redo: stack}, nil
}

// Checkpoint is one immutable published edit boundary.
type Checkpoint struct {
	// ID is the checkpoint identity.
	ID string
	// SessionID is the session whose timeline owns it.
	SessionID int64
	// Repository is the workspace identity it belongs to.
	Repository string
	// Root is the absolute workspace root it was captured in.
	Root string
	// Sequence is its ordinal in the session's timeline.
	Sequence int64
	// Parent is the checkpoint applied before it, or empty for the baseline.
	Parent string
	// CreatedAt is when it was published.
	CreatedAt time.Time
	// BeforeTree is the tree hash before the edit.
	BeforeTree string
	// AfterTree is the tree hash after the edit.
	AfterTree string
	// Changes are the paths the edit touched.
	Changes []Change
	// Metadata records the tools and turn that produced it.
	Metadata map[string]string
	// Interrupted marks a checkpoint published during restart repair: its
	// before-state is verified, its after-state is what the workspace held when
	// ohgo next started.
	Interrupted bool
}

// Checkpoint reads one checkpoint, validating that it belongs to the scope.
func (s *Store) Checkpoint(scope Scope, id string) (Checkpoint, error) {
	if strings.TrimSpace(id) == "" {
		return Checkpoint{}, ErrNoCheckpoint
	}
	row := s.db.QueryRow(
		`SELECT id, session_id, repository, root, seq, parent_id, created_at,
		        before_tree, after_tree, changes, metadata, interrupted
		 FROM checkpoints WHERE id = ?`, id)
	checkpoint, err := scanCheckpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, fmt.Errorf("%w: %s", ErrNoCheckpoint, id)
	}
	if err != nil {
		return Checkpoint{}, err
	}
	if checkpoint.SessionID != scope.SessionID ||
		checkpoint.Repository != scope.Repository ||
		checkpoint.Root != scope.Root {
		return Checkpoint{}, fmt.Errorf("%w: %s", ErrWrongWorkspace, id)
	}
	return checkpoint, nil
}

// History returns a session's checkpoints oldest first, bounded by limit.
func (s *Store) History(scope Scope, limit int) ([]Checkpoint, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, session_id, repository, root, seq, parent_id, created_at,
		        before_tree, after_tree, changes, metadata, interrupted
		 FROM checkpoints WHERE session_id = ? AND repository = ?
		 ORDER BY seq ASC LIMIT ?`,
		scope.SessionID, scope.Repository, limit)
	if err != nil {
		return nil, fmt.Errorf("recovery: read history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Checkpoint
	for rows.Next() {
		checkpoint, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, checkpoint)
	}
	return out, rows.Err()
}

// Transition is one audit record of an attempted recovery operation.
type Transition struct {
	Op             Op
	Checkpoint     string
	FromCheckpoint string
	ToCheckpoint   string
	Changes        []Change
	State          State
	Reason         string
	At             time.Time
}

// Transitions returns the audit trail newest last.
func (s *Store) Transitions(scope Scope, limit int) ([]Transition, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT op, checkpoint_id, from_checkpoint, to_checkpoint, changes, state, reason, at
		 FROM transitions WHERE session_id = ? AND repository = ?
		 ORDER BY id DESC LIMIT ?`,
		scope.SessionID, scope.Repository, limit)
	if err != nil {
		return nil, fmt.Errorf("recovery: read transitions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Transition
	for rows.Next() {
		var (
			op, checkpoint, fromCheckpoint, toCheckpoint string
			changes, state, reason, at                   string
		)
		if err := rows.Scan(&op, &checkpoint, &fromCheckpoint, &toCheckpoint,
			&changes, &state, &reason, &at); err != nil {
			return nil, fmt.Errorf("recovery: scan transition: %w", err)
		}
		var parsed []Change
		if err := json.Unmarshal([]byte(changes), &parsed); err != nil {
			return nil, fmt.Errorf("%w: transition changes: %v", ErrCorrupt, err)
		}
		stamp, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("%w: transition time %q: %v", ErrCorrupt, at, err)
		}
		out = append(out, Transition{
			Op: Op(op), Checkpoint: checkpoint,
			FromCheckpoint: fromCheckpoint, ToCheckpoint: toCheckpoint,
			Changes: parsed, State: State(state), Reason: reason, At: stamp,
		})
	}
	return out, rows.Err()
}

// loadTree reads a stored tree manifest.
func (s *Store) loadTree(hash string) (*Tree, error) {
	var manifest string
	err := s.db.QueryRow(`SELECT manifest FROM trees WHERE hash = ?`, hash).Scan(&manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: tree %s is missing", ErrCorrupt, short(hash))
	}
	if err != nil {
		return nil, fmt.Errorf("recovery: read tree %s: %w", short(hash), err)
	}
	var entries []Entry
	if err := json.Unmarshal([]byte(manifest), &entries); err != nil {
		return nil, fmt.Errorf("%w: tree %s: %v", ErrCorrupt, short(hash), err)
	}
	return &Tree{Entries: entries}, nil
}

// scanCheckpoint decodes one checkpoint row.
func scanCheckpoint(row rowScanner) (Checkpoint, error) {
	var (
		checkpoint               Checkpoint
		createdAt, changes, meta string
		interrupted              int
	)
	if err := row.Scan(
		&checkpoint.ID, &checkpoint.SessionID, &checkpoint.Repository, &checkpoint.Root,
		&checkpoint.Sequence, &checkpoint.Parent, &createdAt,
		&checkpoint.BeforeTree, &checkpoint.AfterTree, &changes, &meta, &interrupted,
	); err != nil {
		return Checkpoint{}, err
	}
	stamp, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint time %q: %v", ErrCorrupt, createdAt, err)
	}
	checkpoint.CreatedAt = stamp
	checkpoint.Interrupted = interrupted != 0
	if err := json.Unmarshal([]byte(changes), &checkpoint.Changes); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint changes: %v", ErrCorrupt, err)
	}
	if err := json.Unmarshal([]byte(meta), &checkpoint.Metadata); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint metadata: %v", ErrCorrupt, err)
	}
	return checkpoint, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// queryer is the shared surface of a pooled handle and a transaction.
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// captureSink writes blobs inside one transaction.
type captureSink struct {
	store  *Store
	tx     *sql.Tx
	added  map[string]struct{}
	closed bool
}

// beginCapture starts a blob-writing transaction.
func (s *Store) beginCapture(ctx context.Context) (*captureSink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("recovery: begin capture: %w", err)
	}
	return &captureSink{store: s, tx: tx, added: map[string]struct{}{}}, nil
}

// Want reports whether the store needs the content behind a hash.
func (c *captureSink) Want(hash string) bool {
	if hash == "" {
		return false
	}
	if _, ok := c.added[hash]; ok {
		return false
	}
	if _, ok := c.store.known[hash]; ok {
		return false
	}
	// The in-memory set is an optimisation, never the authority: an entry may
	// be stale if another process wrote the blob. One existence query settles
	// it before the content is read from disk.
	var draft1 int
	err := c.tx.QueryRow(`SELECT 1 FROM blobs WHERE hash = ?`, hash).Scan(&draft1)
	return errors.Is(err, sql.ErrNoRows)
}

// Put stores blob content.
func (c *captureSink) Put(hash string, data []byte) error {
	if _, ok := c.added[hash]; ok {
		return nil
	}
	if _, err := c.tx.Exec(
		`INSERT OR IGNORE INTO blobs (hash, size, content) VALUES (?, ?, ?)`,
		hash, len(data), data,
	); err != nil {
		return fmt.Errorf("recovery: store blob %s: %w", short(hash), err)
	}
	c.added[hash] = struct{}{}
	return nil
}

// commit writes the capture durably.
func (c *captureSink) commit() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if err := c.tx.Commit(); err != nil {
		return fmt.Errorf("recovery: commit capture: %w", err)
	}
	for hash := range c.added {
		c.store.known[hash] = struct{}{}
	}
	return nil
}

// rollback discards an unfinished capture.
func (c *captureSink) rollback() {
	if c.closed {
		return
	}
	c.closed = true
	_ = c.tx.Rollback()
}

// putTree stores a tree manifest inside an existing transaction.
func putTree(ctx context.Context, q queryer, tree *Tree) error {
	manifest, err := json.Marshal(tree.Entries)
	if err != nil {
		return fmt.Errorf("recovery: encode tree: %w", err)
	}
	if _, err := q.Exec(
		`INSERT OR IGNORE INTO trees (hash, entry_count, manifest) VALUES (?, ?, ?)`,
		tree.Hash(), len(tree.Entries), string(manifest),
	); err != nil {
		return fmt.Errorf("recovery: store tree: %w", err)
	}
	return nil
}

// captureTree captures the workspace, storing every needed blob.
func (s *Store) captureTree(ctx context.Context, root string) (*Tree, error) {
	sink, err := s.beginCapture(ctx)
	if err != nil {
		return nil, err
	}
	tree, scanErr := s.scannerFor(root).Scan(ctx, sink)
	if scanErr != nil {
		sink.rollback()
		return nil, scanErr
	}
	if err := putTree(ctx, sink.tx, tree); err != nil {
		sink.rollback()
		return nil, err
	}
	if err := sink.commit(); err != nil {
		return nil, err
	}
	return tree, nil
}

// verifyTree checks that a stored tree is present and every blob it names is
// intact: the recorded size and the recorded content hash must both match.
//
// It is the integrity gate before a restore. A checkpoint that references
// content the store cannot reproduce must fail closed rather than restore a
// plausible-looking wrong state.
func (s *Store) verifyTree(hash string) error {
	tree, err := s.loadTree(hash)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(tree.Entries))
	for _, entry := range tree.Entries {
		if entry.Hash == "" || seen[entry.Hash] {
			continue
		}
		seen[entry.Hash] = true
		var (
			size    int64
			content []byte
		)
		err := s.db.QueryRow(`SELECT size, content FROM blobs WHERE hash = ?`, entry.Hash).
			Scan(&size, &content)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: blob %s for %s is missing", ErrCorrupt, short(entry.Hash), entry.Path)
		}
		if err != nil {
			return fmt.Errorf("recovery: check blob %s: %w", short(entry.Hash), err)
		}
		if entry.Kind == KindFile && size != entry.Size {
			return fmt.Errorf("%w: blob %s for %s is %d bytes, manifest says %d",
				ErrCorrupt, short(entry.Hash), entry.Path, size, entry.Size)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != entry.Hash {
			return fmt.Errorf("%w: blob %s for %s does not match its hash",
				ErrCorrupt, short(entry.Hash), entry.Path)
		}
	}
	return nil
}

// Open implements BlobReader.
func (s *Store) Open(hash string) (io.ReadCloser, int64, error) {
	var (
		content []byte
		size    int64
	)
	err := s.db.QueryRow(`SELECT size, content FROM blobs WHERE hash = ?`, hash).Scan(&size, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, fmt.Errorf("%w: %s", ErrMissingBlob, short(hash))
	}
	if err != nil {
		return nil, 0, fmt.Errorf("recovery: read blob %s: %w", short(hash), err)
	}
	return io.NopCloser(strings.NewReader(string(content))), size, nil
}

// withTx runs fn in a transaction.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recovery: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recovery: commit: %w", err)
	}
	return nil
}

// appendTransition records one terminal recovery outcome.
func appendTransition(ctx context.Context, q queryer, scope Scope, record transitionRecord) error {
	encoded, err := json.Marshal(record.Changes)
	if err != nil {
		return fmt.Errorf("recovery: encode changes: %w", err)
	}
	_, err = q.Exec(
		`INSERT INTO transitions
		   (session_id, repository, op, checkpoint_id, from_checkpoint, to_checkpoint,
		    from_tree, to_tree, changes, state, reason, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope.SessionID, scope.Repository, string(record.Op), record.Checkpoint,
		record.FromCheckpoint, record.ToCheckpoint, record.FromTree, record.ToTree,
		string(encoded), string(record.State), record.Reason, now(),
	)
	if err != nil {
		return fmt.Errorf("recovery: record transition: %w", err)
	}
	return nil
}

// transitionRecord carries the fields of one audit row.
type transitionRecord struct {
	Op             Op
	Checkpoint     string
	FromCheckpoint string
	ToCheckpoint   string
	FromTree       string
	ToTree         string
	Changes        []Change
	State          State
	Reason         string
}

// now returns the durable timestamp form.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// short truncates a hash for a message without losing identity in storage.
func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// checkpointID derives a stable checkpoint identity.
//
// The sequence, scope, and before-state make it unique; deriving it rather than
// generating it randomly is what lets a repaired checkpoint keep the identity
// its pending record was written with.
func checkpointID(scope Scope, sequence int64, beforeHash, startedAt string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%d\x00%s\x00%s",
		scope.SessionID, scope.Repository, sequence, beforeHash, startedAt)))
	return "cp-" + hex.EncodeToString(sum[:12])
}

// maxSequence returns the highest sequence in a scope.
func maxSequence(ctx context.Context, q queryer, scope Scope) (int64, error) {
	var seq int64
	err := q.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) FROM checkpoints WHERE session_id = ? AND repository = ?`,
		scope.SessionID, scope.Repository,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("recovery: read sequence: %w", err)
	}
	return seq, nil
}

// readTimelineTx reads the timeline inside a transaction.
func readTimelineTx(q queryer, scope Scope) (Timeline, error) {
	var cursor, redo string
	err := q.QueryRow(
		`SELECT cursor_id, redo_stack FROM timelines WHERE session_id = ? AND repository = ?`,
		scope.SessionID, scope.Repository,
	).Scan(&cursor, &redo)
	if errors.Is(err, sql.ErrNoRows) {
		return Timeline{}, nil
	}
	if err != nil {
		return Timeline{}, fmt.Errorf("recovery: read timeline: %w", err)
	}
	var stack []string
	if err := json.Unmarshal([]byte(redo), &stack); err != nil {
		return Timeline{}, fmt.Errorf("%w: redo stack: %v", ErrCorrupt, err)
	}
	return Timeline{Cursor: cursor, Redo: stack}, nil
}

// writeTimelineTx persists a timeline inside a transaction.
func writeTimelineTx(q queryer, scope Scope, timeline Timeline) error {
	if timeline.Redo == nil {
		timeline.Redo = []string{}
	}
	encoded, err := json.Marshal(timeline.Redo)
	if err != nil {
		return fmt.Errorf("recovery: encode redo stack: %w", err)
	}
	_, err = q.Exec(
		`INSERT INTO timelines (session_id, repository, cursor_id, redo_stack, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, repository) DO UPDATE SET
		   cursor_id = excluded.cursor_id,
		   redo_stack = excluded.redo_stack,
		   updated_at = excluded.updated_at`,
		scope.SessionID, scope.Repository, timeline.Cursor, string(encoded), now(),
	)
	if err != nil {
		return fmt.Errorf("recovery: write timeline: %w", err)
	}
	return nil
}

// sortedChanges returns changes in path order.
func sortedChanges(changes []Change) []Change {
	out := append([]Change(nil), changes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
