package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAIStreamAccumulatesTextAndUsage(t *testing.T) {
	body := `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"content":", world"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":8}}}

data: [DONE]

`
	server, _ := sseServer(t, body, 0)
	provider := &OpenAIProvider{APIKey: "sk-test", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testOpenAIModel(),
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
	// total_tokens is authoritative when present: providers compute it over
	// buckets that Input+Output alone would under-count.
	if msg.Usage.Total != 16 || msg.Usage.Input != 11 || msg.Usage.Output != 5 {
		t.Errorf("usage = %+v, want total 16 input 11 output 5", msg.Usage)
	}
	if msg.Usage.CacheRead != 8 {
		t.Errorf("cache read = %d, want 8", msg.Usage.CacheRead)
	}
}

func TestOpenAIStreamReassemblesToolCallArguments(t *testing.T) {
	// OpenAI has no content_block_stop, so arguments must be decoded once
	// the stream drains rather than at a terminator event.
	body := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"/tmp/a.go\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	server, _ := sseServer(t, body, 0)
	provider := &OpenAIProvider{APIKey: "sk-test", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testOpenAIModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "read a"}}}},
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
	if calls[0].ID != "call_1" || calls[0].Name != "read" {
		t.Errorf("call = %+v, want id call_1 name read", calls[0])
	}
	if got := calls[0].Arguments["path"]; got != "/tmp/a.go" {
		t.Errorf("argument path = %v, want /tmp/a.go", got)
	}
	if msg.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want %q", msg.StopReason, StopToolUse)
	}
}

func TestOpenAIStreamKeepsInterleavedToolCallsInDistinctBlocks(t *testing.T) {
	// Two tool calls with text between them exercise the content-index
	// tracking: a positional write would clobber the first call.
	//
	// Chunks are marshalled from structs so the fixture cannot be malformed by
	// hand-edited JSON.
	type fn struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}
	type tc struct {
		Index    int    `json:"index"`
		ID       string `json:"id,omitempty"`
		Function fn     `json:"function"`
	}
	type delta struct {
		Content   string `json:"content,omitempty"`
		ToolCalls []tc   `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int     `json:"index"`
		Delta        delta   `json:"delta"`
		FinishReason *string `json:"finish_reason,omitempty"`
	}
	type chunk struct {
		Choices []choice `json:"choices"`
	}

	var body strings.Builder
	for _, c := range []chunk{
		{Choices: []choice{{Delta: delta{ToolCalls: []tc{{Index: 0, ID: "c0", Function: fn{Name: "read", Arguments: `{"path":"a"}`}}}}}}},
		{Choices: []choice{{Delta: delta{Content: "middle"}}}},
		{Choices: []choice{{Delta: delta{ToolCalls: []tc{{Index: 1, ID: "c1", Function: fn{Name: "read", Arguments: `{"path":"b"}`}}}}}}},
		{Choices: []choice{{FinishReason: strptr("tool_calls")}}},
	} {
		encoded, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}
		body.WriteString("data: " + string(encoded) + "\n\n")
	}
	body.WriteString("data: [DONE]\n\n")

	server, _ := sseServer(t, body.String(), 0)
	provider := &OpenAIProvider{APIKey: "sk-test", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testOpenAIModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "read a and b"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	msg, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if msg.Text() != "middle" {
		t.Errorf("text = %q, want %q", msg.Text(), "middle")
	}
	calls := msg.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(calls))
	}
	if calls[0].Arguments["path"] != "a" {
		t.Errorf("first call path = %v, want a", calls[0].Arguments["path"])
	}
	if calls[1].Arguments["path"] != "b" {
		t.Errorf("second call path = %v, want b", calls[1].Arguments["path"])
	}
	if calls[0].ID != "c0" || calls[1].ID != "c1" {
		t.Errorf("call ids = %q,%q want c0,c1", calls[0].ID, calls[1].ID)
	}
}

func strptr(s string) *string { return &s }

func TestOpenAIStreamSurfacesHTTPError(t *testing.T) {
	server, _ := sseServer(t, "", http.StatusUnauthorized)
	provider := &OpenAIProvider{APIKey: "bad", BaseURL: server.URL}

	_, err := provider.Stream(context.Background(), Request{
		Model:     testOpenAIModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	var httpErr *httpError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want *httpError", err)
	}
	if httpErr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", httpErr.Status)
	}
}

func TestOpenAIStreamSurfacesChunkError(t *testing.T) {
	body := `data: {"error":{"type":"rate_limit_error","message":"slow down"}}

`
	server, _ := sseServer(t, body, 0)
	provider := &OpenAIProvider{APIKey: "sk-test", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testOpenAIModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Result(); err == nil {
		t.Fatal("expected the chunk error to surface as a failure")
	}
}

func TestOpenAIStreamEmitsToolCallStartBeforeDeltas(t *testing.T) {
	// A consumer that tracks in-flight calls by id relies on the start event;
	// emitting only deltas would leave it with no call to attach them to.
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"read","arguments":"{\"path\""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"a\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	server, _ := sseServer(t, body, 0)
	provider := &OpenAIProvider{APIKey: "sk-test", BaseURL: server.URL}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     testOpenAIModel(),
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "read a"}}}},
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var starts, dels, ends int
	for event := range stream.Events {
		switch e := event.(type) {
		case EventToolCallStart:
			starts++
			if e.ID != "c0" || e.Name != "read" {
				t.Errorf("start = %+v, want id c0 name read", e)
			}
		case EventToolCallDelta:
			dels++
		case EventToolCallEnd:
			ends++
		}
	}
	if starts != 1 {
		t.Errorf("tool call starts = %d, want 1", starts)
	}
	if dels != 2 {
		t.Errorf("tool call deltas = %d, want 2", dels)
	}
	if ends != 1 {
		t.Errorf("tool call ends = %d, want 1", ends)
	}
}

func TestOpenAIConvertsToolResultsToToolRole(t *testing.T) {
	messages := []Message{
		UserMessage{Content: []ContentBlock{TextContent{Text: "read a"}}},
		AssistantMessage{Content: []ContentBlock{ToolCall{ID: "c0", Name: "read", Arguments: map[string]any{"path": "a"}}}},
		ToolResultMessage{ToolCallID: "c0", ToolName: "read", Content: []ContentBlock{TextContent{Text: "file a"}}, IsError: true},
	}
	converted, err := toOpenAIMessages(messages, nil)
	if err != nil {
		t.Fatalf("toOpenAIMessages: %v", err)
	}
	if len(converted) != 3 {
		t.Fatalf("messages = %d, want 3", len(converted))
	}
	tool := converted[2]
	if tool.Role != "tool" || tool.ToolCallID != "c0" {
		t.Errorf("tool message = %+v, want role tool id c0", tool)
	}
	if !strings.Contains(tool.Content[0].Text, "file a") {
		t.Errorf("tool content = %q, want the result text", tool.Content[0].Text)
	}
	assistant := converted[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "read" {
		t.Errorf("assistant tool calls = %+v, want one named read", assistant.ToolCalls)
	}
}
