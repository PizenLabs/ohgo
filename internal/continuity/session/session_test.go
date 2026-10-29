package session

import (
	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"path/filepath"
	"testing"
	"time"
)

// openStore creates a store in a temp directory.
func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreCreatesAndGetsASession(t *testing.T) {
	store := openStore(t)

	created, err := store.Create("/tmp/work", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("a created session has no id")
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Cwd != "/tmp/work" || got.Model != "claude-sonnet-4-5" {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at was not persisted")
	}
}

func TestStoreReportsMissingSession(t *testing.T) {
	store := openStore(t)
	if _, err := store.Get(9999); err == nil {
		t.Fatal("expected an unknown id to fail")
	}
}

func TestStoreAppendsMessagesWithStableSequence(t *testing.T) {
	store := openStore(t)
	sess, err := store.Create("/tmp", "m")
	if err != nil {
		t.Fatal(err)
	}

	msgs := []agent.Message{
		agent.UserMessage{Text: "first"},
		agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "second"}},
		}},
		agent.Notification{Text: "UI only, never stored"},
		agent.ToolResultMessage{
			ToolCallID: "c1", ToolName: "read",
			Content: []ai.ContentBlock{ai.TextContent{Text: "third"}},
			IsError: true,
		},
	}
	for _, msg := range msgs {
		stored, err := FromAgentMessage(msg)
		if err != nil {
			t.Fatalf("FromAgentMessage: %v", err)
		}
		// A UI-only message converts to a zero value; storing it would bloat
		// the transcript with content a replay cannot use.
		if stored.Role == "" {
			continue
		}
		if err := store.Append(sess.ID, stored); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	loaded, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("messages = %d, want 3 (the UI-only message is dropped)", len(loaded))
	}
	// Order must survive: replaying out of order changes what the model saw.
	wantRoles := []string{"user", "assistant", "toolResult"}
	for i, msg := range loaded {
		if msg.Role != wantRoles[i] {
			t.Errorf("message %d role = %q, want %q", i, msg.Role, wantRoles[i])
		}
	}
	if !loaded[2].IsError || loaded[2].ToolCallID != "c1" {
		t.Errorf("tool result lost its metadata: %+v", loaded[2])
	}
}

func TestStoreRoundTripsThroughReplay(t *testing.T) {
	store := openStore(t)
	sess, err := store.Create("/tmp", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}

	original := []agent.Message{
		agent.UserMessage{Text: "read a.go"},
		agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
			},
			StopReason: ai.StopToolUse,
			Model:      catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		}},
		agent.ToolResultMessage{
			ToolCallID: "c1", ToolName: "read",
			Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}},
		},
	}
	for _, msg := range original {
		stored, err := FromAgentMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Role == "" {
			continue
		}
		if err := store.Append(sess.ID, stored); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(loaded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != len(original) {
		t.Fatalf("replayed %d messages, want %d", len(replayed), len(original))
	}

	// The tool call must come back with its arguments, or a resumed session
	// would replay a different request than the one that ran.
	assistant, ok := replayed[1].(agent.AssistantMessage)
	if !ok {
		t.Fatalf("message 1 is %T, want AssistantMessage", replayed[1])
	}
	calls := assistant.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Arguments["path"] != "a.go" {
		t.Errorf("tool call arguments lost in the round trip: %+v", calls)
	}
}

func TestReplayRejectsUnknownRoles(t *testing.T) {
	// A corrupt transcript must fail loudly; silently skipping a turn would
	// replay a conversation the model never had.
	if _, err := Replay([]StoredMessage{{Role: "banana"}}); err == nil {
		t.Fatal("expected an unknown role to fail replay")
	}
}

func TestStoreLimitsAndOrdersMessages(t *testing.T) {
	store := openStore(t)
	sess, _ := store.Create("/tmp", "m")
	for i := 0; i < 10; i++ {
		stored, _ := FromAgentMessage(agent.UserMessage{Text: string(rune('a' + i))})
		if err := store.Append(sess.ID, stored); err != nil {
			t.Fatal(err)
		}
	}

	// A limit takes the most recent messages, which is what a resumed session
	// wants.
	recent, err := store.Messages(sess.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 {
		t.Fatalf("got %d messages, want 3", len(recent))
	}
	if recent[0].Text != "h" || recent[2].Text != "j" {
		t.Errorf("limit returned %q..%q, want the last three", recent[0].Text, recent[2].Text)
	}
}

func TestStoreListByProjectScopesSessions(t *testing.T) {
	// One machine-global store holds every project's sessions, so project
	// association must be a query, not a second database. A project's list
	// must contain its own sessions and the unidentified legacy ones, but not
	// another project's.
	store := openTempStore(t)
	if _, err := store.CreateWith(CreateOptions{Cwd: "/a", Model: "m", Project: "proj-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWith(CreateOptions{Cwd: "/b", Model: "m", Project: "proj-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWith(CreateOptions{Cwd: "/legacy", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListByProject("proj-a", 10)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("proj-a list has %d sessions, want proj-a plus the unidentified one", len(list))
	}
	for _, sess := range list {
		if sess.Project == "proj-b" {
			t.Errorf("another project's session leaked into the list: %+v", sess)
		}
	}
}

func TestStoreListsSessionsNewestFirst(t *testing.T) {
	store := openStore(t)
	first, _ := store.Create("/tmp/a", "m1")
	// A distinct timestamp so ordering is not decided by insert order alone.
	time.Sleep(2 * time.Millisecond)
	second, _ := store.Create("/tmp/b", "m2")

	list, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(list))
	}
	if list[0].ID != second.ID {
		t.Errorf("first listed = %d, want %d (newest)", list[0].ID, second.ID)
	}
	if list[1].ID != first.ID {
		t.Errorf("second listed = %d, want %d", list[1].ID, first.ID)
	}
}

func TestStoreUpdatesSessionTimestampOnAppend(t *testing.T) {
	// The listing order depends on updated_at, so an append that did not touch
	// it would leave a session sorted as stale forever.
	store := openStore(t)
	sess, _ := store.Create("/tmp", "m")
	before, _ := store.Get(sess.ID)

	time.Sleep(2 * time.Millisecond)
	stored, _ := FromAgentMessage(agent.UserMessage{Text: "hi"})
	if err := store.Append(sess.ID, stored); err != nil {
		t.Fatal(err)
	}

	after, _ := store.Get(sess.ID)
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at did not advance: %v then %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestStoreDeletesSessionAndTranscript(t *testing.T) {
	store := openStore(t)
	sess, _ := store.Create("/tmp", "m")
	stored, _ := FromAgentMessage(agent.UserMessage{Text: "hi"})
	if err := store.Append(sess.ID, stored); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(sess.ID); err == nil {
		t.Error("the session still exists after deletion")
	}
	// Orphaned messages would make a later id collision replay a stranger's
	// transcript.
	left, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d messages survived the delete", len(left))
	}
}

func TestStoreDeleteReportsMissingSession(t *testing.T) {
	store := openStore(t)
	if err := store.Delete(4242); err == nil {
		t.Fatal("expected deleting an unknown session to fail")
	}
}

func TestStoreSetTitle(t *testing.T) {
	store := openStore(t)
	sess, _ := store.Create("/tmp", "m")

	if err := store.SetTitle(sess.ID, "debug the parser"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	got, _ := store.Get(sess.ID)
	if got.Title != "debug the parser" {
		t.Errorf("title = %q, want the value that was set", got.Title)
	}
}

func TestStoreReopensPersistently(t *testing.T) {
	// The point of a store is that data survives the process, so a reopen must
	// see everything the first handle wrote.
	path := filepath.Join(t.TempDir(), "sessions.db")

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := store.Create("/tmp", "m")
	stored, _ := FromAgentMessage(agent.UserMessage{Text: "remember me"})
	if err := store.Append(sess.ID, stored); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	loaded, err := reopened.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Text != "remember me" {
		t.Errorf("reopen lost the transcript: %+v", loaded)
	}
}

func TestStoreSchemaExcludesProjectKnowledge(t *testing.T) {
	// sessions.db is the session owner, not the project-memory or graph owner.
	// A durable project fact or a code-intelligence table appearing here would
	// be the "sessions.db → project-wide knowledge" collapse the boundary
	// forbids: project knowledge must not be reachable only through a session,
	// and source-derived structure must never live beside a transcript.
	store := openStore(t)
	rows, err := store.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	defer func() { _ = rows.Close() }()

	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema: %v", err)
	}

	for _, name := range []string{"sessions", "messages", "artifacts"} {
		if !tables[name] {
			t.Errorf("session table %q is missing", name)
		}
	}
	for _, name := range []string{"memory", "facts", "knowledge", "graph", "nodes", "edges", "symbols", "embeddings"} {
		if tables[name] {
			t.Errorf("project knowledge table %q was created in the session store", name)
		}
	}
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected an empty path to fail")
	}
}

func TestOpenIsIdempotentOnAnExistingDatabase(t *testing.T) {
	// Re-opening applies the schema again; CREATE TABLE IF NOT EXISTS must make
	// that a no-op rather than an error.
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	if _, err := Open(path); err != nil {
		t.Fatalf("second open failed: %v", err)
	}
}

// Archiving hides a session from the default rosters without deleting it:
// Get still resolves it, so an archived conversation stays resumable by id.
func TestStoreArchiveHidesWithoutDeleting(t *testing.T) {
	store := openStore(t)
	sess, err := store.CreateWith(CreateOptions{Cwd: "/w", Model: "m", Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetArchived(sess.ID, true); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}

	list, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("List returned %d sessions, want the archived one hidden", len(list))
	}
	byProject, err := store.ListByProject("proj", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byProject) != 0 {
		t.Errorf("ListByProject returned %d sessions, want the archived one hidden", len(byProject))
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("archived session is no longer reachable: %v", err)
	}
	if !got.Archived {
		t.Error("the archived flag did not persist")
	}
}

func TestStoreArchiveReportsMissingSession(t *testing.T) {
	store := openStore(t)
	if err := store.SetArchived(4242, true); err == nil {
		t.Fatal("expected archiving an unknown session to fail")
	}
}

// Stats reconstructs the roster summary from the stored transcript: the
// message count, an estimated token total, and the latest user prompt.
func TestStoreStats(t *testing.T) {
	store := openStore(t)
	first, _ := store.Create("/w", "m")
	second, _ := store.Create("/w", "m")

	appendMsg := func(id int64, msg agent.Message) {
		t.Helper()
		stored, err := FromAgentMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Append(id, stored); err != nil {
			t.Fatal(err)
		}
	}
	appendMsg(first.ID, agent.UserMessage{Text: "first prompt"})
	appendMsg(first.ID, agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "an answer"}}}})
	appendMsg(first.ID, agent.UserMessage{Text: "second prompt"})
	appendMsg(second.ID, agent.UserMessage{Text: "only"})

	stats, err := store.Stats([]int64{first.ID, second.ID})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got := stats[first.ID]; got.Messages != 3 || got.Prompt != "second prompt" || got.Tokens <= 0 {
		t.Errorf("first stats = %+v, want 3 messages, the last prompt, and tokens", got)
	}
	if got := stats[second.ID]; got.Messages != 1 || got.Prompt != "only" {
		t.Errorf("second stats = %+v, want 1 message and its prompt", got)
	}
	if _, ok := stats[999]; ok {
		t.Error("an unrequested session appeared in the stats")
	}
	// No ids is a cheap no-op, not a query with an empty IN clause.
	if out, err := store.Stats(nil); err != nil || out != nil {
		t.Errorf("Stats(nil) = %v, %v; want nil, nil", out, err)
	}
}
