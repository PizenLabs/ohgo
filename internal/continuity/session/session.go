// Package session persists and replays agent conversations.
//
// Storage is SQLite through modernc.org/sqlite, a pure-Go driver: the port's
// standing rule is no cgo, which rules out the mattn/go-sqlite3 binding. The
// alternative — a JSONL transcript — has no dependency but cannot answer the
// queries a session manager needs (list sessions, count messages, resume at a
// specific turn) without loading everything into memory.
package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/state"
	"github.com/PizenLabs/ohgo/internal/tokens"
	_ "modernc.org/sqlite"
)

// Schema is applied on open. It is idempotent so re-opening an existing
// database is safe.
//
// The workspace and project columns are part of the durable authority and
// identity record (§35): a resumed session must restore the workspace it ran
// under and the project it belonged to, not infer either from process state.
// Existing databases predate these columns; migrate adds them.
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    cwd        TEXT NOT NULL,
    model      TEXT NOT NULL,
    workspace  TEXT NOT NULL DEFAULT 'build',
    project    TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    archived   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    role       TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(session_id, seq)
);

CREATE INDEX IF NOT EXISTS messages_by_session ON messages(session_id, seq);

CREATE TABLE IF NOT EXISTS artifacts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    artifact_id TEXT NOT NULL,
    kind        TEXT NOT NULL,
    origin      TEXT NOT NULL,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    UNIQUE(session_id, artifact_id)
);

CREATE INDEX IF NOT EXISTS artifacts_by_session ON artifacts(session_id, id);
`

// DefaultWorkspace is the authority a session runs under when none is
// recorded. It preserves the pre-workspace behavior: existing ohgo sessions
// could mutate, so an unlabelled session is assumed to be BUILD rather than
// silently downgraded to a read-only authority.
const DefaultWorkspace = "build"

// Store persists sessions and their transcripts.
type Store struct {
	// db is the connection pool. It is held directly rather than behind an
	// interface because nothing substitutes for it in tests: the suite runs
	// against a real database file in a temp directory.
	db *sql.DB
	// path is the database file.
	path string
}

// Errors, matched with errors.Is.
var (
	// ErrSessionNotFound means the requested session does not exist.
	ErrSessionNotFound = errors.New("session: not found")
	// ErrEmptyPayload means a stored message could not be decoded.
	ErrEmptyPayload = errors.New("session: empty message payload")
	// ErrUnknownRole means a stored message has an unrecognised role.
	ErrUnknownRole = errors.New("session: unknown message role")
)

// Open opens or creates the database at path.
//
// A parent directory that does not exist is created, because the store is
// usually opened from a fresh install where ~/.ohgo has not been written yet.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("session: database path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// SQLite reports an opaque "unable to open database file" when the
		// parent directory is missing, so create it up front.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("session: create %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	// A single writer connection serialises writes without relying on SQLite's
	// busy timeout, which is the common source of "database is locked" under
	// the driver's default pool.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Writes must survive a crash mid-session, otherwise a resumed
	// conversation silently loses turns.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: enable foreign keys: %w", err)
	}

	return &Store{db: db, path: path}, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// DefaultPath is the database location under the user's home directory.
func DefaultPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions.db"), nil
}

// Session is one conversation.
type Session struct {
	// ID is the database identifier.
	ID int64
	// CreatedAt is when the session was opened.
	CreatedAt time.Time
	// UpdatedAt is when the last message was appended.
	UpdatedAt time.Time
	// Cwd is the working directory the session ran in.
	Cwd string
	// Model is the effective model id the session resolved, "provider/id".
	// It is the session's own snapshot: a later config change must not
	// silently alter what a resumed session calls (§13, I8).
	Model string
	// Workspace is the authority the session ran under ("ask", "fuzz",
	// "build"). Empty in a database row read before migration defaults to
	// DefaultWorkspace.
	Workspace string
	// Project is the stable project identity the session belongs to. Empty
	// when the session predates project identity.
	Project string
	// Title is a human-readable label, derived from the first turn.
	Title string
	// Archived hides the session from the default roster without deleting it.
	Archived bool
}

// CreateOptions describes a new session.
type CreateOptions struct {
	// Cwd is the working directory.
	Cwd string
	// Model is the resolved effective model, "provider/id".
	Model string
	// Workspace is the authority the session runs under. Empty means
	// DefaultWorkspace.
	Workspace string
	// Project is the stable project identity. Empty means "unidentified".
	Project string
}

// Create opens a new session under the default authority.
func (s *Store) Create(cwd, model string) (Session, error) {
	return s.CreateWith(CreateOptions{Cwd: cwd, Model: model})
}

// CreateWith opens a new session recording its workspace and project.
func (s *Store) CreateWith(opts CreateOptions) (Session, error) {
	workspace := opts.Workspace
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`INSERT INTO sessions (created_at, updated_at, cwd, model, workspace, project, title)
		 VALUES (?, ?, ?, ?, ?, ?, '')`,
		now, now, opts.Cwd, opts.Model, workspace, opts.Project,
	)
	if err != nil {
		return Session{}, fmt.Errorf("session: create: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Session{}, fmt.Errorf("session: read new id: %w", err)
	}
	return Session{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Cwd:       opts.Cwd,
		Model:     opts.Model,
		Workspace: workspace,
		Project:   opts.Project,
	}, nil
}

// SetAuthority records the effective workspace and model for a session.
//
// It is used at resolution time so the stored snapshot matches what actually
// executes, which is what makes a later restart truthful (§13, §31).
func (s *Store) SetAuthority(id int64, workspace, model string) error {
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	result, err := s.db.Exec(
		`UPDATE sessions SET workspace = ?, model = ?, updated_at = ? WHERE id = ?`,
		workspace, model, time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("session: set authority: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("session: confirm authority: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: session %d", ErrSessionNotFound, id)
	}
	return nil
}

// Get returns one session by id.
func (s *Store) Get(id int64) (Session, error) {
	row := s.db.QueryRow(
		`SELECT id, created_at, updated_at, cwd, model, workspace, project, title, archived FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

// List returns non-archived sessions newest first.
func (s *Store) List(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, created_at, updated_at, cwd, model, workspace, project, title, archived
		 FROM sessions WHERE archived = 0 ORDER BY updated_at DESC LIMIT ?`,
		limit)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanSessions(rows)
}

// ListByProject returns the non-archived sessions belonging to one project,
// newest first.
//
// Sessions are stored in one machine-global database, so project association is
// logical: this query is what makes "list this project's sessions" possible
// without a second database. Sessions recorded before project identity existed
// (project = ”) are included, because hiding an unidentified session from
// every project would read as lost history; a session that names another
// project is deliberately excluded and remains reachable by id.
func (s *Store) ListByProject(project string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, created_at, updated_at, cwd, model, workspace, project, title, archived
		 FROM sessions WHERE (project = ? OR project = '') AND archived = 0
		 ORDER BY updated_at DESC LIMIT ?`,
		project, limit)
	if err != nil {
		return nil, fmt.Errorf("session: list by project: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanSessions(rows)
}

// scanSessions drains session rows into values.
func scanSessions(rows *sql.Rows) ([]Session, error) {
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Append stores one message at the end of a session's transcript.
//
// The sequence number is assigned inside the same transaction as the insert so
// a concurrent append cannot interleave and leave a gap or a duplicate.
func (s *Store) Append(sessionID int64, msg StoredMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("session: encode message: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("session: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(seq), -1) + 1 FROM messages WHERE session_id = ?`, sessionID,
	).Scan(&next); err != nil {
		return fmt.Errorf("session: next seq: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`INSERT INTO messages (session_id, seq, role, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, next, msg.Role, payload, now,
	); err != nil {
		return fmt.Errorf("session: insert message: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, now, sessionID); err != nil {
		return fmt.Errorf("session: touch session: %w", err)
	}
	return tx.Commit()
}

// Messages returns the transcript, optionally limited to the last n.
func (s *Store) Messages(sessionID int64, limit int) ([]StoredMessage, error) {
	// A positive limit returns the newest messages, which is what a resumed
	// session wants. That is fetched in reverse and flipped, because a bare
	// SQL LIMIT keeps the *earliest* rows.
	query := `SELECT payload FROM messages WHERE session_id = ? ORDER BY seq `
	args := []any{sessionID}
	if limit > 0 {
		query += `DESC LIMIT ?`
		args = append(args, limit)
	} else {
		query += `ASC`
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("session: read messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []StoredMessage
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("session: scan message: %w", err)
		}
		var msg StoredMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, fmt.Errorf("session: decode message: %w", err)
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Restore chronological order: the caller expects oldest-to-newest
	// regardless of how the query fetched it.
	if limit > 0 {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// SetTitle labels a session for listing.
func (s *Store) SetTitle(sessionID int64, title string) error {
	result, err := s.db.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, title, sessionID)
	if err != nil {
		return fmt.Errorf("session: set title: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("session: confirm title: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: session %d", ErrSessionNotFound, sessionID)
	}
	return nil
}

// SetArchived hides a session from the default roster (archived = true) or
// restores it. It never deletes the transcript, so an archived conversation
// stays resumable by id.
func (s *Store) SetArchived(sessionID int64, archived bool) error {
	value := 0
	if archived {
		value = 1
	}
	result, err := s.db.Exec(`UPDATE sessions SET archived = ? WHERE id = ?`, value, sessionID)
	if err != nil {
		return fmt.Errorf("session: set archived: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("session: confirm archived: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: session %d", ErrSessionNotFound, sessionID)
	}
	return nil
}

// Stat is a lightweight per-session summary for the Session Manager roster.
//
// It is deliberately partial: the stored transcript carries no usage record,
// so Tokens is reconstructed from stored text, not a measured bill.
type Stat struct {
	// Messages is the stored message count.
	Messages int
	// Tokens is the estimated token count reconstructed from stored text.
	Tokens int
	// Prompt is the latest user prompt, or empty when none was recorded.
	Prompt string
}

// Stats returns per-session summaries for the given ids in one pass. Sessions
// with no stored messages are omitted from the result; the caller keeps its
// own zero value for them.
func (s *Store) Stats(ids []int64) (map[int64]Stat, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT session_id, payload FROM messages WHERE session_id IN (` +
		strings.Join(placeholders, ",") + `) ORDER BY session_id, seq`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("session: stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]Stat, len(ids))
	for rows.Next() {
		var (
			id      int64
			payload string
		)
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, fmt.Errorf("session: scan stat: %w", err)
		}
		var msg StoredMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, fmt.Errorf("session: decode stat: %w", err)
		}
		stat := out[id]
		stat.Messages++
		stat.Tokens += tokens.Count(msg.Text, tokens.ModeEstimate)
		if msg.Role == "user" && strings.TrimSpace(msg.Text) != "" {
			stat.Prompt = msg.Text
		}
		out[id] = stat
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a session and its transcript.
func (s *Store) Delete(sessionID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("session: begin delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Explicit message deletion first: the driver honours ON DELETE CASCADE,
	// but only when foreign keys are enabled, and a partially-applied cascade
	// would leave an unreplayable transcript.
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("session: delete messages: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM artifacts WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("session: delete artifacts: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("session: delete session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("session: confirm delete: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: session %d", ErrSessionNotFound, sessionID)
	}
	return tx.Commit()
}

// SaveArtifact records a structured handoff artifact for a session.
//
// Artifacts are runtime state worth persisting (§35): a Finding that BUILD has
// not yet acted on must survive a restart. The artifact is validated first, so
// an incomplete handoff is refused at the boundary rather than stored as a
// claim.
func (s *Store) SaveArtifact(sessionID int64, artifact workspace.Artifact) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("session: encode artifact: %w", err)
	}
	createdAt := artifact.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if _, err := s.db.Exec(
		`INSERT INTO artifacts (session_id, artifact_id, kind, origin, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, artifact_id) DO UPDATE SET payload = excluded.payload`,
		sessionID, artifact.ID, string(artifact.Kind), string(artifact.Origin), payload,
		createdAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("session: save artifact: %w", err)
	}
	return nil
}

// Artifacts returns a session's artifacts, oldest first.
func (s *Store) Artifacts(sessionID int64) ([]workspace.Artifact, error) {
	rows, err := s.db.Query(
		`SELECT payload FROM artifacts WHERE session_id = ? ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session: read artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []workspace.Artifact
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("session: scan artifact: %w", err)
		}
		var artifact workspace.Artifact
		if err := json.Unmarshal([]byte(payload), &artifact); err != nil {
			return nil, fmt.Errorf("session: decode artifact: %w", err)
		}
		out = append(out, artifact)
	}
	return out, rows.Err()
}

// StoredMessage is one persisted turn, in a form that survives a round trip.
//
// It records the agent role plus the decoded fields the loop needs, rather
// than the provider wire shape: replay must rebuild what the *agent* saw, and
// a provider message may carry host-specific extensions that a later run of a
// different model cannot consume.
type StoredMessage struct {
	// Role is user, assistant, or toolResult.
	Role string `json:"role"`
	// Text is the message content.
	Text string `json:"text,omitempty"`
	// ToolCallID links a tool result to its call.
	ToolCallID string `json:"toolCallId,omitempty"`
	// ToolName is the tool a result belongs to.
	ToolName string `json:"toolName,omitempty"`
	// IsError marks a failed tool result.
	IsError bool `json:"isError,omitempty"`
	// Model is the model that produced an assistant turn.
	Model string `json:"model,omitempty"`
	// StopReason is why an assistant turn ended.
	StopReason string `json:"stopReason,omitempty"`
	// Calls holds the tool calls an assistant turn requested.
	Calls []StoredToolCall `json:"calls,omitempty"`
}

// StoredToolCall is one persisted tool call.
type StoredToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// FromAgentMessage converts an agent message into its stored form.
func FromAgentMessage(msg agent.Message) (StoredMessage, error) {
	switch m := msg.(type) {
	case agent.UserMessage:
		return StoredMessage{Role: "user", Text: m.Text}, nil
	case agent.AssistantMessage:
		out := StoredMessage{
			Role:       "assistant",
			Text:       m.Message.Text(),
			Model:      m.Message.Model.ID,
			StopReason: string(m.Message.StopReason),
		}
		for _, block := range m.Message.Content {
			if call, ok := block.(ai.ToolCall); ok {
				out.Calls = append(out.Calls, StoredToolCall{
					ID: call.ID, Name: call.Name, Arguments: call.Arguments,
				})
			}
		}
		return out, nil
	case agent.ToolResultMessage:
		return StoredMessage{
			Role:       "toolResult",
			Text:       m.Text(),
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			IsError:    m.IsError,
		}, nil
	case agent.Notification:
		// UI-only messages never reach the model, so persisting them would
		// bloat the transcript with content a replay cannot use.
		return StoredMessage{}, nil
	default:
		return StoredMessage{}, fmt.Errorf("session: cannot store %T", msg)
	}
}

// ToAgentMessage rebuilds the agent message a stored turn came from.
func ToAgentMessage(stored StoredMessage) (agent.Message, error) {
	switch stored.Role {
	case "user":
		return agent.UserMessage{Text: stored.Text}, nil
	case "assistant":
		var content []ai.ContentBlock
		if stored.Text != "" {
			content = append(content, ai.TextContent{Text: stored.Text})
		}
		for _, call := range stored.Calls {
			args := call.Arguments
			if args == nil {
				args = map[string]any{}
			}
			content = append(content, ai.ToolCall{ID: call.ID, Name: call.Name, Arguments: args})
		}
		return agent.AssistantMessage{Message: ai.AssistantMessage{
			Content:    content,
			Model:      ai.Model{ID: stored.Model},
			StopReason: ai.StopReason(stored.StopReason),
		}}, nil
	case "toolResult":
		var content []ai.ContentBlock
		if stored.Text != "" {
			content = append(content, ai.TextContent{Text: stored.Text})
		}
		return agent.ToolResultMessage{
			ToolCallID: stored.ToolCallID,
			ToolName:   stored.ToolName,
			Content:    content,
			IsError:    stored.IsError,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownRole, stored.Role)
	}
}

// Replay rebuilds a stored transcript in message order.
//
// A message that cannot be converted is reported by name rather than skipped:
// silently dropping a turn would replay a conversation the model never had.
func Replay(stored []StoredMessage) ([]agent.Message, error) {
	out := make([]agent.Message, 0, len(stored))
	for i, msg := range stored {
		converted, err := ToAgentMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("session: message %d: %w", i, err)
		}
		out = append(out, converted)
	}
	return out, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (Session, error) {
	var (
		sess      Session
		createdAt string
		updatedAt string
		archived  int
	)
	if err := row.Scan(&sess.ID, &createdAt, &updatedAt, &sess.Cwd, &sess.Model, &sess.Workspace, &sess.Project, &sess.Title, &archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, fmt.Errorf("session: scan: %w", err)
	}
	sess.Archived = archived != 0
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Session{}, fmt.Errorf("session: parse created_at %q: %w", createdAt, err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Session{}, fmt.Errorf("session: parse updated_at %q: %w", updatedAt, err)
	}
	sess.CreatedAt = created
	sess.UpdatedAt = updated
	if sess.Workspace == "" {
		sess.Workspace = DefaultWorkspace
	}
	return sess, nil
}

// migrate adds columns that predate this build, so an existing database keeps
// working rather than failing on the first query.
//
// It is additive and idempotent: a column already present is left alone, and
// no existing row is rewritten. The workspace default preserves the previous
// behavior (a session could mutate), and an empty project means "unidentified"
// rather than inventing an identity.
func migrate(db *sql.DB) error {
	columns, err := tableColumns(db, "sessions")
	if err != nil {
		return err
	}
	additions := []struct {
		name string
		ddl  string
	}{
		{"workspace", `ALTER TABLE sessions ADD COLUMN workspace TEXT NOT NULL DEFAULT '` + DefaultWorkspace + `'`},
		{"project", `ALTER TABLE sessions ADD COLUMN project TEXT NOT NULL DEFAULT ''`},
		{"archived", `ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`},
	}
	for _, add := range additions {
		if columns[add.name] {
			continue
		}
		if _, err := db.Exec(add.ddl); err != nil {
			return fmt.Errorf("session: migrate add %s: %w", add.name, err)
		}
	}
	return nil
}

// tableColumns reports the columns of a table by name.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("session: inspect %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("session: scan %s columns: %w", table, err)
		}
		out[name] = true
	}
	return out, rows.Err()
}

// configDir locates the machine-level directory holding ohgo state.
//
// It delegates to state so the session database lives in exactly one place on
// the machine; sessions are global across projects and outlive any project's
// .ohgo directory.
func configDir() (string, error) {
	return state.Dir()
}
