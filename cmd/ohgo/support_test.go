package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/session"
)

// TestSupIsEphemeralAndDoesNotPersist is the end-to-end contract for $sup.
//
// A support query runs against the model but must leave no trace in the
// session store and must not disturb the main conversation (§10.2, I17).
func TestSupIsEphemeralAndDoesNotPersist(t *testing.T) {
	server := newScriptedServer(t, ackStream)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("OHGO_HISTORY_FILE", "")

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

	h := &harness{
		loop:    loop,
		store:   store,
		sess:    &sess,
		support: newSupportSurface(supportLoopBuilder(opts, registry, io.Discard)),
	}

	var out strings.Builder
	if err := repl(h, &out, strings.NewReader("$sup what is Go?\n/quit\n"), nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if !strings.Contains(out.String(), "ack") {
		t.Errorf("support answer missing from output: %q", out.String())
	}
	if server.requests == 0 {
		t.Error("support never reached the model")
	}

	// Nothing may be persisted: the main session must be untouched.
	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Errorf("support persisted %d messages into the session", len(messages))
	}
}

// TestSupportSurfaceKeepsItsOwnConversation proves the surface is multi-turn
// without entering the main history.
func TestSupportSurfaceKeepsItsOwnConversation(t *testing.T) {
	server := newScriptedServer(t, ackStream)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)
	opts := options{root: t.TempDir(), model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}

	surface := newSupportSurface(supportLoopBuilder(opts, registry, io.Discard))
	if _, err := surface.ask(t.Context(), "first", nil); err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if _, err := surface.ask(t.Context(), "second", nil); err != nil {
		t.Fatalf("second ask: %v", err)
	}
	// The second request carries the first exchange, so the surface is a real
	// conversation rather than two independent queries.
	server.mu.Lock()
	count := server.lastCount
	server.mu.Unlock()
	if count < 2 {
		t.Errorf("second support request carried %d message slots, want the prior exchange", count)
	}
	surface.close()
	if len(surface.msgs) != 0 {
		t.Errorf("close left %d messages in the ephemeral buffer", len(surface.msgs))
	}
}
