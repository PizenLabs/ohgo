package main

import (
	"context"
	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/session"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// scriptedServer replies to every request with a fixed SSE body and records
// the message count of each.
type scriptedServer struct {
	*httptest.Server
	mu        sync.Mutex
	requests  int
	lastCount int
}

func newScriptedServer(t *testing.T, body string) *scriptedServer {
	t.Helper()
	srv := &scriptedServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		srv.mu.Lock()
		srv.requests++
		srv.lastCount = strings.Count(string(buf), `"role":`)
		srv.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const ackStream = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ack"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`

// TestReplAccumulatesHistoryAcrossTurns exercises the production REPL with a
// scripted stdin.
//
// A REPL that passed only the new message each turn would forget everything
// the user said, which is the entire purpose of a session.
func TestReplAccumulatesHistoryAcrossTurns(t *testing.T) {
	server := newScriptedServer(t, ackStream)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	t.Setenv("OHGO_HISTORY_FILE", "")
	in := strings.NewReader("implement first change\nimplement second change\nimplement third change\n")
	var out strings.Builder
	if err := repl(&harness{loop: loop}, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}

	if got := server.requests; got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	server.mu.Lock()
	last := server.lastCount
	server.mu.Unlock()

	// The third request must carry the two prior turns plus its own, not just
	// the message it was given.
	if last < 5 {
		t.Errorf("last request carried %d message slots, want at least 5 (3 user + 2 assistant)", last)
	}
}

// TestReplPersistsEveryTurn proves the transcript grows with the conversation.
func TestReplPersistsEveryTurn(t *testing.T) {
	server := newScriptedServer(t, ackStream)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create(t.TempDir(), "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, io.Discard, store, &sess, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	t.Setenv("OHGO_HISTORY_FILE", "")
	in := strings.NewReader("implement one\nimplement two\n/quit\n")
	if err := repl(&harness{loop: loop, store: store, sess: &sess}, io.Discard, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}

	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Both user turns and both assistant answers must be present, or a resume
	// would replay half a conversation.
	if len(messages) != 4 {
		t.Errorf("persisted %d messages, want 4 (2 user + 2 assistant)", len(messages))
	}
	if len(messages) > 0 && messages[0].Role != "user" {
		t.Errorf("first message role = %q, want user", messages[0].Role)
	}
}

// TestResumeSeedsHistoryFromTheStore proves a resumed session continues.
func TestResumeSeedsHistoryFromTheStore(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create(t.TempDir(), "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}

	// A prior conversation to resume from.
	for _, msg := range []agent.Message{
		agent.UserMessage{Text: "what does main.go do?"},
		agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "it starts the CLI"}},
			Model:   catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		}},
	} {
		stored, err := session.FromAgentMessage(msg)
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

	history, err := loadHistory(store, &sess)
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("replayed %d messages, want 2", len(history))
	}

	// The replayed history must actually be sent on the next request, which is
	// what makes the resume meaningful.
	server := newScriptedServer(t, ackStream)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	request := append(append([]agent.Message(nil), history...), agent.UserMessage{Text: "and tests?"})
	if _, err := loop.Run(context.Background(), request); err != nil {
		t.Fatalf("Run: %v", err)
	}

	server.mu.Lock()
	count := server.lastCount
	server.mu.Unlock()
	if count < 3 {
		t.Errorf("resumed request carried %d message slots, want at least 3", count)
	}
}

// TestResumeRoundTripThroughTheStore is the end-to-end contract for resume.
//
// It runs a conversation, drops the handle, and reopens through the same code
// path the CLI uses. Anything that only works in memory — a session that is
// never written, or a replay that loses the tail — fails here.
func TestResumeRoundTripThroughTheStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	server := newScriptedServer(t, ackStream)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}

	// First run: hold a conversation and let it persist.
	store, sess, closeFn, err := openSession(opts, 0)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	loop, err := buildLoop(opts, registry, io.Discard, store, sess, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	t.Setenv("OHGO_HISTORY_FILE", "")
	if err := repl(&harness{loop: loop, store: store, sess: sess}, io.Discard, strings.NewReader("implement this note\n/quit\n"), nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	id := sess.ID
	closeFn()

	// Second run: resume by id, exactly as `ohgo --resume <id>` does.
	store2, resumed, closeFn2, err := openSession(opts, id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer closeFn2()

	history, err := loadHistory(store2, resumed)
	if err != nil {
		t.Fatalf("loadHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("a resumed session replayed an empty history")
	}

	// The replayed turn must reach the provider on the next request.
	loop2, err := buildLoop(opts, registry, io.Discard, store2, resumed, nil)
	if err != nil {
		t.Fatalf("buildLoop on resume: %v", err)
	}
	request := append(append([]agent.Message(nil), history...), agent.UserMessage{Text: "what did I say?"})
	if _, err := loop2.Run(context.Background(), request); err != nil {
		t.Fatalf("Run on resume: %v", err)
	}

	server.mu.Lock()
	count := server.lastCount
	server.mu.Unlock()
	if count < 2 {
		t.Errorf("resumed request carried %d message slots, want the replayed history", count)
	}
}

// TestOpenSessionResumesAndReportsMissing verifies the resume lookup.
func TestOpenSessionResumesAndReportsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	opts := options{root: t.TempDir(), model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default()}

	// A missing id must be fatal rather than silently starting an empty
	// session, which would look like the history was wiped.
	if _, _, _, err := openSession(opts, 9999); err == nil {
		t.Fatal("expected an unknown resume id to fail")
	}

	// A fresh session must be created without a resume id.
	store, sess, closeFn, err := openSession(opts, 0)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	defer closeFn()
	if sess == nil || sess.ID == 0 {
		t.Fatal("a new session was not created")
	}
	id := sess.ID
	closeFn()

	// The same id must resolve on reopen.
	_, resumed, _, err := openSession(opts, id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed == nil || resumed.ID != id {
		t.Errorf("resumed session = %+v, want id %d", resumed, id)
	}
	_ = store
}

// TestUseInteractiveUIDefaultsToThePlainRepl keeps escape codes out of pipes.
//
// A non-TTY session must never start an alt-screen program: it would fill a
// file or another tool's output with escape sequences.
func TestUseInteractiveUIDefaultsToThePlainRepl(t *testing.T) {
	cmd := newRootCommandForTest()
	// os.Stdin is not a terminal under `go test`, so this is the piped case.
	if useInteractiveUI(cmd) {
		t.Error("a non-TTY session selected the full-screen UI")
	}
}

func TestUseInteractiveUIHonoursOptOut(t *testing.T) {
	// A user on a real terminal must be able to force the line REPL.
	cmd := newRootCommandForTest()
	if err := cmd.PersistentFlags().Set("no-tui", "true"); err != nil {
		t.Fatal(err)
	}
	if useInteractiveUI(cmd) {
		t.Error("--no-tui did not disable the full-screen UI")
	}
}

func TestUseInteractiveUIHonoursPlain(t *testing.T) {
	cmd := newRootCommandForTest()
	if err := cmd.PersistentFlags().Set("plain", "true"); err != nil {
		t.Fatal(err)
	}
	if useInteractiveUI(cmd) {
		t.Error("--plain did not disable the full-screen UI")
	}
}

func newRootCommandForTest() *cobra.Command {
	cmd := &cobra.Command{Use: "ohgo"}
	cmd.PersistentFlags().Bool("no-tui", false, "")
	cmd.PersistentFlags().Bool("plain", false, "")
	cmd.PersistentFlags().Int64("resume", 0, "")
	return cmd
}

func TestIsTerminalRejectsAPipe(t *testing.T) {
	// A pipe is not a character device, which is what keeps the full-screen UI
	// off the non-interactive path. The test builds its own pipe rather than
	// reusing stdin, because the harness may legitimately hand the process a
	// terminal on either descriptor.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	if isTerminal(reader) {
		t.Error("a pipe was reported as a terminal")
	}

	// /dev/null is a character device but not a terminal. A file-mode check
	// would accept it and start the full-screen UI with its output discarded,
	// so the terminal check must reject it.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	if isTerminal(devNull) {
		t.Error("/dev/null was reported as a terminal")
	}
}
