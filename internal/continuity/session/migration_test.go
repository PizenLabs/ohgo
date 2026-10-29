package session

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// legacySchema is the sessions table as it existed before workspace and
// project identity were recorded.
const legacySchema = `
CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    cwd        TEXT NOT NULL,
    model      TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT ''
);
`

func TestOpenMigratesLegacyDatabase(t *testing.T) {
	// A database written by an earlier build must keep working: the migration
	// adds the authority columns without rewriting existing rows, and an
	// unlabelled session defaults to BUILD so its prior behavior is preserved.
	path := filepath.Join(t.TempDir(), "sessions.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO sessions (created_at, updated_at, cwd, model, title) VALUES ('2020-01-01T00:00:00Z','2020-01-01T00:00:00Z','/old','anthropic/legacy','old')`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	_ = raw.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated database: %v", err)
	}
	defer func() { _ = store.Close() }()

	sess, err := store.Get(1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sess.Workspace != DefaultWorkspace {
		t.Errorf("workspace = %q, want %q", sess.Workspace, DefaultWorkspace)
	}
	if sess.Project != "" {
		t.Errorf("project = %q, want empty for a pre-identity row", sess.Project)
	}
	if sess.Model != "anthropic/legacy" {
		t.Errorf("model = %q, want the stored snapshot", sess.Model)
	}

	// The migrated database must accept new writes with the new columns.
	if _, err := store.CreateWith(CreateOptions{Cwd: "/new", Model: "m", Workspace: "fuzz", Project: "proj-1"}); err != nil {
		t.Fatalf("CreateWith after migration: %v", err)
	}
}

func TestCreateWithRecordsAuthorityAndProject(t *testing.T) {
	store := openTempStore(t)

	sess, err := store.CreateWith(CreateOptions{
		Cwd: "/work", Model: "openrouter/model-x", Workspace: "fuzz", Project: "proj-9",
	})
	if err != nil {
		t.Fatalf("CreateWith: %v", err)
	}
	if sess.Workspace != "fuzz" || sess.Project != "proj-9" {
		t.Fatalf("session = %+v, want fuzz/proj-9", sess)
	}

	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Workspace != "fuzz" || got.Project != "proj-9" || got.Model != "openrouter/model-x" {
		t.Errorf("read back %+v, want the stored authority/project/model", got)
	}
}

func TestCreateDefaultsToBuildWorkspace(t *testing.T) {
	// Create keeps its old signature and behavior: with no workspace named, a
	// session is assumed to be BUILD, not silently made read-only.
	store := openTempStore(t)
	sess, err := store.Create("/work", "m")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Workspace != DefaultWorkspace {
		t.Errorf("workspace = %q, want %q", sess.Workspace, DefaultWorkspace)
	}
}

func TestSetAuthorityUpdatesSnapshotAndRejectsMissing(t *testing.T) {
	store := openTempStore(t)
	sess, err := store.CreateWith(CreateOptions{Cwd: "/w", Model: "old", Workspace: "ask"})
	if err != nil {
		t.Fatalf("CreateWith: %v", err)
	}
	if err := store.SetAuthority(sess.ID, "build", "new/model"); err != nil {
		t.Fatalf("SetAuthority: %v", err)
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Workspace != "build" || got.Model != "new/model" {
		t.Errorf("authority = %s/%s, want build/new/model", got.Workspace, got.Model)
	}
	if err := store.SetAuthority(999, "build", "m"); err == nil {
		t.Error("SetAuthority accepted an unknown session id")
	}
}

func openTempStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
