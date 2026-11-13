package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// reasoningOnlyStream is an Anthropic response that closed the turn with
// reasoning and no visible text and no tool call. It is the transport shape
// that produced the persisted reasoning-only turn in the reported session.
const reasoningOnlyStream = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the project served 200 and the check passed"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}

`

// conversationServer replies with one scripted SSE body per request and
// rejects any outbound message that violates the canonical content contract,
// exactly as the reporting providers did.
type conversationServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	bodies   []string
	invalid  []string
}

func newConversationServer(t *testing.T, bodies ...string) *conversationServer {
	t.Helper()
	srv := &conversationServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		invalid := malformedContentParts(raw)
		srv.mu.Lock()
		srv.invalid = append(srv.invalid, invalid...)
		srv.bodies = append(srv.bodies, string(raw))
		index := srv.requests
		srv.requests++
		srv.mu.Unlock()
		if len(invalid) > 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid message content"}}`)
			return
		}
		body := ""
		if index < len(bodies) {
			body = bodies[index]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// malformedContentParts reproduces the provider-side rule: a content part of
// type text must carry a non-empty string, and a message's content list must
// not be an empty array.
func malformedContentParts(body []byte) []string {
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return []string{"body is not JSON: " + err.Error()}
	}
	var problems []string
	for index, message := range payload.Messages {
		raw := strings.TrimSpace(string(message.Content))
		if raw == "" || raw == "null" {
			continue
		}
		if !strings.HasPrefix(raw, "[") {
			continue
		}
		var parts []map[string]any
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			continue
		}
		if len(parts) == 0 {
			problems = append(problems, "messages["+strconv.Itoa(index)+"].content is an empty array")
			continue
		}
		for _, part := range parts {
			kind, _ := part["type"].(string)
			if kind != "text" {
				continue
			}
			text, ok := part["text"].(string)
			if !ok || text == "" {
				problems = append(problems,
					"messages["+strconv.Itoa(index)+"].content text part has no text")
			}
		}
	}
	return problems
}

// The production REPL is the end-to-end path: the real loop, provider adapter,
// rebase, and transcript persistence. A reasoning-only answer must not poison
// the next user request.
func TestReplContinuesAfterAReasoningOnlyTurn(t *testing.T) {
	server := newConversationServer(t, reasoningOnlyStream, ackStream)
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
	in := strings.NewReader("run this project help me\nimplement the second change\n/quit\n")
	var out strings.Builder
	if err := repl(&harness{loop: loop}, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	t.Logf("repl output:\n%s", out.String())

	if server.requests != 2 {
		t.Fatalf("provider requests = %d, want 2", server.requests)
	}
	if len(server.invalid) > 0 {
		t.Fatalf("provider received malformed content: %v", server.invalid)
	}

	// The second request carries the first user turn plus the new one, and no
	// assistant message produced by the reasoning-only turn.
	var second struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(server.bodies[1]), &second); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	if len(second.Messages) != 2 {
		t.Fatalf("second request messages = %d, want two user turns: %s", len(second.Messages), server.bodies[1])
	}
	for index, message := range second.Messages {
		if message.Role != "user" {
			t.Fatalf("message %d role = %q, want user: %s", index, message.Role, server.bodies[1])
		}
	}
}
