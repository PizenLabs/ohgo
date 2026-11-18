package ai

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bedrockFrame(eventType, messageType string, payload []byte) []byte {
	headers := map[string]string{
		":message-type": messageType,
		":content-type": "application/json",
	}
	if messageType == "exception" {
		headers[":exception-type"] = eventType
	} else {
		headers[":event-type"] = eventType
	}
	var hb []byte
	for name, value := range headers {
		hb = append(hb, byte(len(name)))
		hb = append(hb, name...)
		hb = append(hb, 7) // string
		hb = append(hb, byte(len(value)>>8), byte(len(value)))
		hb = append(hb, value...)
	}
	total := 12 + len(hb) + len(payload) + 4
	msg := make([]byte, total)
	binary.BigEndian.PutUint32(msg[0:4], uint32(total))
	binary.BigEndian.PutUint32(msg[4:8], uint32(len(hb)))
	binary.BigEndian.PutUint32(msg[8:12], crc32.ChecksumIEEE(msg[:8]))
	copy(msg[12:], hb)
	copy(msg[12+len(hb):], payload)
	binary.BigEndian.PutUint32(msg[total-4:], crc32.ChecksumIEEE(msg[:total-4]))
	return msg
}

func bedrockEventFrame(t *testing.T, eventType string, payload any) []byte {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return bedrockFrame(eventType, "event", encoded)
}

func bedrockTestServer(t *testing.T, frames ...[]byte) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var captured http.Request
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = *r
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			_, _ = w.Write(frame)
		}
	}))
	t.Cleanup(server.Close)
	return server, &captured, &body
}

func bedrockTestModel(baseURL string) Model {
	return Model{
		ID:              "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Name:            "Claude Sonnet 4.5 (Bedrock)",
		Provider:        "bedrock",
		API:             APIBedrockConverse,
		BaseURL:         baseURL,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		InputModalities: []string{"text", "image"},
	}
}

// TestBedrockStreamAssemblesTextUsageAndStopReason is the basic wire contract:
// binary frames become the same canonical events as every other transport.
func TestBedrockStreamAssemblesTextUsageAndStopReason(t *testing.T) {
	server, request, body := bedrockTestServer(t,
		bedrockEventFrame(t, "messageStart", map[string]any{"role": "assistant"}),
		bedrockEventFrame(t, "contentBlockDelta", map[string]any{
			"contentBlockIndex": 0, "delta": map[string]any{"text": "Hello "},
		}),
		bedrockEventFrame(t, "contentBlockDelta", map[string]any{
			"contentBlockIndex": 0, "delta": map[string]any{"text": "world"},
		}),
		bedrockEventFrame(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		bedrockEventFrame(t, "messageStop", map[string]any{"stopReason": "end_turn"}),
		bedrockEventFrame(t, "metadata", map[string]any{
			"usage": map[string]any{
				"inputTokens": 12, "outputTokens": 5, "totalTokens": 17,
				"cacheReadInputTokens": 3, "cacheWriteInputTokens": 2,
			},
		}),
	)

	provider := &BedrockProvider{APIKey: "bedrock-key", BaseURL: server.URL}
	stream, err := provider.Stream(context.Background(), Request{
		Model:     bedrockTestModel(server.URL),
		System:    []SystemPromptBlock{{Text: "be terse"}},
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var deltas []string
	for event := range stream.Events {
		if delta, ok := event.(EventTextDelta); ok {
			deltas = append(deltas, delta.Delta)
		}
	}
	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "Hello world" {
		t.Errorf("deltas = %q, want Hello world", got)
	}
	if message.Text() != "Hello world" {
		t.Errorf("message text = %q", message.Text())
	}
	if message.StopReason != StopEndTurn {
		t.Errorf("stop reason = %q, want end_turn", message.StopReason)
	}
	if message.Usage != (Usage{Input: 12, Output: 5, Total: 17, CacheRead: 3, CacheWrite: 2}) {
		t.Errorf("usage = %+v", message.Usage)
	}

	if request.URL.Path != "/model/global.anthropic.claude-sonnet-4-5-20250929-v1:0/converse-stream" {
		t.Errorf("path = %q", request.URL.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer bedrock-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "application/vnd.amazon.eventstream" {
		t.Errorf("Accept = %q", got)
	}
	var sent bedrockRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if len(sent.System) != 1 || sent.System[0].Text == nil || *sent.System[0].Text != "be terse" {
		t.Errorf("system = %+v", sent.System)
	}
	if sent.Inference == nil || sent.Inference.MaxTokens != 100 {
		t.Errorf("inferenceConfig = %+v", sent.Inference)
	}
}

// TestBedrockStreamReassemblesToolCallFromPartialJSON proves the tool contract
// survives the binary protocol: start, fragments, stop, decoded arguments.
func TestBedrockStreamReassemblesToolCallFromPartialJSON(t *testing.T) {
	server, _, _ := bedrockTestServer(t,
		bedrockEventFrame(t, "messageStart", map[string]any{"role": "assistant"}),
		bedrockEventFrame(t, "contentBlockStart", map[string]any{
			"contentBlockIndex": 0,
			"start": map[string]any{"toolUse": map[string]any{
				"toolUseId": "tool-1", "name": "read",
			}},
		}),
		bedrockEventFrame(t, "contentBlockDelta", map[string]any{
			"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `{"path":`}},
		}),
		bedrockEventFrame(t, "contentBlockDelta", map[string]any{
			"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `"a.go"}`}},
		}),
		bedrockEventFrame(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		bedrockEventFrame(t, "messageStop", map[string]any{"stopReason": "tool_use"}),
	)

	stream, err := (&BedrockProvider{APIKey: "k", BaseURL: server.URL}).Stream(context.Background(), Request{
		Model:    bedrockTestModel(server.URL),
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "read it"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var started, ended bool
	for event := range stream.Events {
		switch typed := event.(type) {
		case EventToolCallStart:
			started = typed.ID == "tool-1" && typed.Name == "read"
		case EventToolCallEnd:
			ended = typed.ID == "tool-1" && typed.Name == "read" && typed.Arguments["path"] == "a.go"
		}
	}
	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !started || !ended {
		t.Fatalf("tool events: start=%v end=%v", started, ended)
	}
	calls := message.ToolCalls()
	if len(calls) != 1 || calls[0].Arguments["path"] != "a.go" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if message.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want tool_use", message.StopReason)
	}
}

// TestBedrockStreamPreservesSignedReasoning proves the replayable signature
// survives a reasoning block rather than being dropped at the boundary.
func TestBedrockStreamPreservesSignedReasoning(t *testing.T) {
	server, _, _ := bedrockTestServer(t,
		bedrockEventFrame(t, "contentBlockDelta", map[string]any{
			"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"text": "think "}},
		}),
		bedrockEventFrame(t, "contentBlockDelta", map[string]any{
			"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"signature": "sig-1"}},
		}),
		bedrockEventFrame(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		bedrockEventFrame(t, "messageStop", map[string]any{"stopReason": "end_turn"}),
	)
	stream, err := (&BedrockProvider{APIKey: "k", BaseURL: server.URL}).Stream(context.Background(), Request{
		Model:    bedrockTestModel(server.URL),
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "why"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var thinking []string
	for event := range stream.Events {
		if delta, ok := event.(EventThinkingDelta); ok {
			thinking = append(thinking, delta.Delta)
		}
	}
	message, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(thinking, ""); got != "think " {
		t.Errorf("thinking deltas = %q", got)
	}
	var block ThinkingContent
	for _, content := range message.Content {
		if typed, ok := content.(ThinkingContent); ok {
			block = typed
		}
	}
	if block.Thinking != "think " || block.Signature != "sig-1" {
		t.Errorf("thinking block = %+v", block)
	}
}

// TestBedrockStreamSurfacesModeledException keeps a provider-side rejection a
// stream error rather than a silently empty answer.
func TestBedrockStreamSurfacesModeledException(t *testing.T) {
	server, _, _ := bedrockTestServer(t,
		bedrockFrame("validationException", "exception", []byte(`{"message":"bad tool schema"}`)),
	)
	stream, err := (&BedrockProvider{APIKey: "k", BaseURL: server.URL}).Stream(context.Background(), Request{
		Model:    bedrockTestModel(server.URL),
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events {
	}
	if _, err := stream.Result(); err == nil || !strings.Contains(err.Error(), "bad tool schema") {
		t.Fatalf("Result error = %v, want the modeled message", err)
	}
}

// TestBedrockRejectsACorruptedFrame proves the checksum is enforced rather
// than decoded as plausible bytes.
func TestBedrockRejectsACorruptedFrame(t *testing.T) {
	frame := bedrockEventFrame(t, "messageStop", map[string]any{"stopReason": "end_turn"})
	frame[len(frame)-6] ^= 0xff
	server, _, _ := bedrockTestServer(t, frame)
	stream, err := (&BedrockProvider{APIKey: "k", BaseURL: server.URL}).Stream(context.Background(), Request{
		Model:    bedrockTestModel(server.URL),
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events {
	}
	if _, err := stream.Result(); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Result error = %v, want a checksum failure", err)
	}
}

// TestBedrockMessagesMergeSameRoleTurns guards a real Converse constraint:
// consecutive tool results are separate canonical messages but one user turn.
func TestBedrockMessagesMergeSameRoleTurns(t *testing.T) {
	messages, err := toBedrockMessages([]Message{
		AssistantMessage{Content: []ContentBlock{
			ToolCall{ID: "a", Name: "read", Arguments: map[string]any{"path": "a"}},
			ToolCall{ID: "b", Name: "read", Arguments: map[string]any{"path": "b"}},
		}},
		ToolResultMessage{ToolCallID: "a", ToolName: "read", Content: []ContentBlock{TextContent{Text: "A"}}},
		ToolResultMessage{ToolCallID: "b", ToolName: "read", Content: []ContentBlock{TextContent{Text: "B"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want one assistant and one merged user turn", len(messages))
	}
	if messages[0].Role != "assistant" || len(messages[0].Content) != 2 {
		t.Errorf("assistant turn = %+v", messages[0])
	}
	if messages[1].Role != "user" || len(messages[1].Content) != 2 {
		t.Errorf("user turn = %+v", messages[1])
	}
	if messages[1].Content[0].ToolResult.ToolUseID != "a" || messages[1].Content[1].ToolResult.ToolUseID != "b" {
		t.Errorf("tool results not in order: %+v", messages[1].Content)
	}
}

// TestBedrockRegistryFollowsAWSRegion proves the runtime hostname is built from
// the same region resolution a user expects from the AWS CLI.
func TestBedrockRegistryFollowsAWSRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_DEFAULT_REGION", "")
	registry := NewRegistry()
	registry.SetAPIKey("bedrock", "bedrock-key")
	provider, err := registry.ProviderFor(bedrockTestModel(BedrockDefaultBaseURL))
	if err != nil {
		t.Fatal(err)
	}
	bedrock := provider.(*BedrockProvider)
	if bedrock.BaseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("base URL = %q", bedrock.BaseURL)
	}
}

// TestBedrockExplicitEndpointIsNotRewritten keeps a gateway or test server
// exactly where the operator put it, regardless of AWS_REGION.
func TestBedrockExplicitEndpointIsNotRewritten(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-1")
	registry := NewRegistry()
	registry.SetAPIKey("bedrock", "bedrock-key")
	registry.SetBaseURL("bedrock", "http://127.0.0.1:9999")
	provider, err := registry.ProviderFor(bedrockTestModel(BedrockDefaultBaseURL))
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.(*BedrockProvider).BaseURL; got != "http://127.0.0.1:9999" {
		t.Errorf("base URL = %q, want the explicit override", got)
	}
}

// TestBedrockProbeUsesAModelListForCredentialChecking proves the probe does
// not invent a runtime /models route: an explicit gateway is checked at its own
// models endpoint.
func TestBedrockProbeUsesAModelListForCredentialChecking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("probe path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer bedrock-key" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"}]}`)
	}))
	defer server.Close()
	result := ProbeProvider(context.Background(), nil, APIBedrockConverse, server.URL, "bedrock-key")
	if !result.Reachable || !result.Authenticated || result.Models != 2 {
		t.Fatalf("probe = %+v", result)
	}
}

// TestBedrockReasoningEffortBecomesAThinkingBudget proves the effort ladder is
// carried to the wire rather than displayed and ignored.
func TestBedrockReasoningEffortBecomesAThinkingBudget(t *testing.T) {
	server, _, body := bedrockTestServer(t,
		bedrockEventFrame(t, "messageStop", map[string]any{"stopReason": "end_turn"}),
	)
	model := bedrockTestModel(server.URL)
	model.Reasoning = true
	stream, err := (&BedrockProvider{APIKey: "k", BaseURL: server.URL}).Stream(context.Background(), Request{
		Model:           model,
		Messages:        []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "think"}}}},
		MaxTokens:       8000,
		Reasoning:       true,
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Events {
	}
	var sent struct {
		Additional map[string]any `json:"additionalModelRequestFields"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	thinking, _ := sent.Additional["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] == nil {
		t.Fatalf("thinking = %+v, want an enabled budget", thinking)
	}
}
