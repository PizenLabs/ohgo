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
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	_ "modernc.org/sqlite"
)

// Schema is applied on open. It is idempotent so re-opening an existing
// database is safe.
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    cwd        TEXT NOT NULL,
    model      TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT ''
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
`

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
	// Model is the model id the session used.
	Model string
	// Title is a human-readable label, derived from the first turn.
	Title string
}

// Create opens a new session.
func (s *Store) Create(cwd, model string) (Session, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`INSERT INTO sessions (created_at, updated_at, cwd, model, title) VALUES (?, ?, ?, ?, '')`,
		now, now, cwd, model,
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
		Cwd:       cwd,
		Model:     model,
	}, nil
}

// Get returns one session by id.
func (s *Store) Get(id int64) (Session, error) {
	row := s.db.QueryRow(
		`SELECT id, created_at, updated_at, cwd, model, title FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

// List returns sessions newest first.
func (s *Store) List(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, created_at, updated_at, cwd, model, title FROM sessions ORDER BY updated_at DESC LIMIT ?`,
		limit)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
	)
	if err := row.Scan(&sess.ID, &createdAt, &updatedAt, &sess.Cwd, &sess.Model, &sess.Title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, fmt.Errorf("session: scan: %w", err)
	}
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
	return sess, nil
}

// configDir locates the directory holding ohgo state.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: locate home directory: %w", err)
	}
	return filepath.Join(home, ".ohgo"), nil
}
