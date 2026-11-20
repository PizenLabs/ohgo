package session

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The session ledger is additive and survives a reopen. This is the durable
// half of headless usage accounting: the provider's measured totals must not be
// replaced by a text estimate when the session is listed or resumed.
func TestAddUsageAccumulatesAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create("/work", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Usage.Known() {
		t.Fatal("a new session reported measured usage")
	}
	if err := store.AddUsage(sess.ID, Usage{Turns: 2, Input: 100, Output: 40, CacheRead: 10, CacheWrite: 5}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddUsage(sess.ID, Usage{Turns: 1, Input: 50, Output: 20}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Usage.Known() || got.Usage.Turns != 3 ||
		got.Usage.Input != 150 || got.Usage.Output != 60 ||
		got.Usage.CacheRead != 10 || got.Usage.CacheWrite != 5 {
		t.Fatalf("usage = %+v, want accumulated measured totals", got.Usage)
	}
	stats, err := store.Stats([]int64{sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	if stat := stats[sess.ID]; !stat.Usage.Known() || stat.Usage.Total() != 210 {
		t.Fatalf("stat usage = %+v, want the same ledger", stat.Usage)
	}
}

// A provider that reports no usage must not become a measured zero.
func TestAddUsageIgnoresUnmeasuredRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create("/work", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddUsage(sess.ID, Usage{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.Known() {
		t.Fatalf("unmeasured run became %+v", got.Usage)
	}
}

// A pre-usage database must migrate in place. The old rows keep their
// transcript and gain the zero ledger, which is "unmeasured", not "zero spent".
func TestUsageColumnsMigrateOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		cwd TEXT NOT NULL,
		model TEXT NOT NULL,
		workspace TEXT NOT NULL DEFAULT 'build',
		project TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		archived INTEGER NOT NULL DEFAULT 0,
		protected INTEGER NOT NULL DEFAULT 0
	);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (created_at, updated_at, cwd, model)
		VALUES ('2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '/work', 'provider/model')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	got, err := store.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.Known() {
		t.Fatalf("migrated row reported measured usage: %+v", got.Usage)
	}
	if err := store.AddUsage(1, Usage{Turns: 1, Input: 7, Output: 3}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.Turns != 1 || got.Usage.Input != 7 || got.Usage.Output != 3 {
		t.Fatalf("usage after migration = %+v", got.Usage)
	}
}
