package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer replies to every POST with the given SSE body. It captures the
// request body so tests can assert on the wire shape.
func sseServer(t *testing.T, body string, status int) (*httptest.Server, *string) {
	t.Helper()
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			if _, err := r.Body.Read(buf); err != nil && err.Error() != "EOF" {
				t.Errorf("read request body: %v", err)
			}
		}
		captured = string(buf)
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"bad request"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

const anthropicTextStream = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":120,"output_tokens":1,"cache_read_input_tokens":40,"cache_creation_input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`

func TestAnthropicStreamAccumulatesTextUsageAndStopReason(t *testing.T) {
	server, _ := sseServer(t, anthropicTextStream, 0)
	provider := &AnthropicProvider{APIKey: "test-key", BaseURL: server.URL, Version: AnthropicVersion}

	model := testModel()
	stream, err := provider.Stream(context.Background(), Request{
		Model:     model,
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	msg, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if msg.Text() != "Hello, world" {
		t.Errorf("text = %q, want %q", msg.Text(), "Hello, world")
	}
	if msg.StopReason != StopEndTurn {
		t.Errorf("stop reason = %q, want %q", msg.StopReason, StopEndTurn)
	}
	if msg.Usage.Input != 120 || msg.Usage.Output != 7 {
		t.Errorf("usage = %+v, want input 120 output 7", msg.Usage)
	}
	if msg.Usage.CacheRead != 40 || msg.Usage.CacheWrite != 10 {
		t.Errorf("cache usage = %+v, want read 40 write 10", msg.Usage)
	}
	// Total must include cache reads: they are billed tokens the model saw.
	if want := 120 + 7 + 40; msg.Usage.Total != want {
		t.Errorf("usage total = %d, want %d", msg.Usage.Total, want)
	}
}

func TestAnthropicStreamReassemblesToolCallFromPartialJSON(t *testing.T) {
	// Arguments arrive as independently unparseable fragments, so a decoder
	// that tries to parse each delta would fail here.
	body := `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"/tmp/x.go\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

`
	server, _ := sseServer(t, body, 0)
	provider := &AnthropicProvider{APIKey: "test-key", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "read x"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	msg, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	calls := msg.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].ID != "toolu_1" || calls[0].Name != "read" {
		t.Errorf("call = %+v, want id toolu_1 name read", calls[0])
	}
	if got := calls[0].Arguments["path"]; got != "/tmp/x.go" {
		t.Errorf("argument path = %v, want /tmp/x.go", got)
	}
	if msg.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", msg.StopReason, StopToolUse)
	}
}

func TestAnthropicStreamEmitsEventsInOrder(t *testing.T) {
	server, _ := sseServer(t, anthropicTextStream, 0)
	provider := &AnthropicProvider{APIKey: "test-key", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var deltas []string
	var sawStart, sawDone bool
	for event := range stream.Events {
		switch e := event.(type) {
		case EventStart:
			sawStart = true
		case EventTextDelta:
			deltas = append(deltas, e.Delta)
		case EventDone:
			sawDone = true
		}
	}
	if !sawStart {
		t.Error("no start event emitted")
	}
	if !sawDone {
		t.Error("no done event emitted")
	}
	if got := len(deltas); got != 2 {
		t.Fatalf("text deltas = %d, want 2", got)
	}
	if deltas[0] != "Hello" || deltas[1] != ", world" {
		t.Errorf("deltas = %q, want [Hello \", world\"]", deltas)
	}
}

func TestAnthropicStreamSurfacesHTTPError(t *testing.T) {
	server, _ := sseServer(t, "", http.StatusBadRequest)
	provider := &AnthropicProvider{APIKey: "test-key", BaseURL: server.URL}

	_, err := provider.Stream(context.Background(), Request{
		Model:     testModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	var httpErr *httpError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want *httpError", err)
	}
	if httpErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", httpErr.Status)
	}
}

func TestAnthropicStreamSurfacesStreamErrorEvent(t *testing.T) {
	body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n"
	server, _ := sseServer(t, body, 0)
	provider := &AnthropicProvider{APIKey: "test-key", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Result(); err == nil {
		t.Fatal("expected the error event to surface as a failure")
	}
}

func TestAnthropicStreamRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &AnthropicProvider{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"}

	_, err := provider.Stream(ctx, Request{
		Model:     testModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestAnthropicSendsAuthAndCacheControl(t *testing.T) {
	var (
		gotKey     string
		gotVersion string
		captured   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		captured = string(buf)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, anthropicTextStream)
	}))
	t.Cleanup(server.Close)
	provider := &AnthropicProvider{APIKey: "sk-test", BaseURL: server.URL}

	_, err := provider.Stream(context.Background(), Request{
		Model: testModel(),
		System: []SystemPromptBlock{
			{Text: "You are ohgo."},
			{Text: "Cacheable tail.", Cache: true},
		},
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotKey != "sk-test" {
		t.Errorf("x-api-key = %q, want sk-test", gotKey)
	}
	if gotVersion != AnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, AnthropicVersion)
	}
	// Only the final system block may carry cache_control: marking one block
	// is enough to cache the longest matching prefix.
	if got := strings.Count(captured, `"cache_control"`); got != 1 {
		t.Errorf("cache_control occurrences = %d, want 1; body %s", got, captured)
	}
}
