package main

import (
	"context"
	"fmt"
	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// anthropicTurnSSE builds a Messages-API stream that asks for a tool call.
func anthropicTurnSSE(toolName, args string) string {
	return fmt.Sprintf(`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`, toolName, args)
}

const anthropicFinalTurn = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

func TestEndToEndToolCallRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The model first reads the file, then declares itself done. The file
	// contents must reach the second request, which is what proves the tool
	// actually ran against the real filesystem.
	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		requestBodies = append(requestBodies, string(buf))

		w.Header().Set("Content-Type", "text/event-stream")
		if len(requestBodies) == 1 {
			_, _ = fmt.Fprint(w, anthropicTurnSSE("read", `{"path":"hello.txt"}`))
			return
		}
		_, _ = fmt.Fprint(w, anthropicFinalTurn)
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	opts := options{
		root:        root,
		model:       catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		settings:    config.Default(),
		autoApprove: true,
		effort:      "medium",
	}
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "read hello.txt"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if turns[len(turns)-1].Assistant.Text() != "done" {
		t.Errorf("final text = %q, want %q", turns[len(turns)-1].Assistant.Text(), "done")
	}

	if len(requestBodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(requestBodies))
	}
	// The tool result must be replayed on the wire as a tool_result block.
	if !strings.Contains(requestBodies[1], "tool_result") {
		t.Errorf("second request did not carry a tool_result block: %s", requestBodies[1])
	}
	if !strings.Contains(requestBodies[1], "hello from disk") {
		t.Errorf("second request did not carry the file contents: %s", requestBodies[1])
	}
	if !strings.Contains(requestBodies[1], "toolu_1") {
		t.Errorf("second request did not reference the tool call id: %s", requestBodies[1])
	}
}

func TestEndToEndProviderErrorSurfaces(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"type":"authentication_error","message":"invalid key"}}`)
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "bad-key")
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	_, err = loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "go"}})
	if err == nil {
		t.Fatal("expected an HTTP 401 to surface as a run failure")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
}
