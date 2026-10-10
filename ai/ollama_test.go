package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ollamaHarness serves scripted NDJSON and records the request.
type ollamaHarness struct {
	server  *httptest.Server
	status  int
	lines   []string
	gotAuth string
	gotBody string
	gotPath string
}

func newOllamaHarness(t *testing.T, status int, lines []string) *ollamaHarness {
	t.Helper()
	h := &ollamaHarness{status: status, lines: lines}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.gotAuth = r.Header.Get("Authorization")
		h.gotPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		h.gotBody = string(buf)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(status)
		for _, line := range lines {
			_, _ = fmt.Fprintln(w, line)
		}
	}))
	t.Cleanup(h.server.Close)
	return h
}

func ollamaTestModel(baseURL string) Model {
	return Model{ID: "llama3.1", Provider: "ollama", API: APIOllamaChat, BaseURL: baseURL, ContextWindow: 128000, MaxOutputTokens: 8192}
}

func runOllama(t *testing.T, h *ollamaHarness, model Model) (AssistantMessage, error) {
	t.Helper()
	provider := NewOllamaProvider("")
	provider.Client = h.server.Client()
	// The transport reads the provider's BaseURL (the registry sets it from
	// the model in production); the model copy travels with the message.
	provider.BaseURL = h.server.URL
	model.BaseURL = h.server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := provider.Stream(ctx, Request{
		Model:     model,
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 64,
	})
	if err != nil {
		return AssistantMessage{}, err
	}
	return stream.Result()
}

// TestOllamaStreamsFragmentedText proves chunks split mid-word reassemble
// into one block: appending per chunk would corrupt the text with newlines.
func TestOllamaStreamsFragmentedText(t *testing.T) {
	h := newOllamaHarness(t, 200, []string{
		`{"message":{"role":"assistant","content":"hel"},"done":false}`,
		`{"message":{"role":"assistant","content":"lo wo"},"done":false}`,
		`{"message":{"role":"assistant","content":"rld"},"done":false}`,
		`{"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`,
	})
	msg, err := runOllama(t, h, ollamaTestModel(""))
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if msg.Text() != "hello world" {
		t.Errorf("text = %q, want reassembled fragments", msg.Text())
	}
	if msg.StopReason != StopEndTurn {
		t.Errorf("stop = %q, want end_turn", msg.StopReason)
	}
	if msg.Usage.Input != 10 || msg.Usage.Output != 5 || msg.Usage.Total != 15 {
		t.Errorf("usage = %+v, want 10/5/15 from eval counts", msg.Usage)
	}
	if h.gotPath != "/api/chat" {
		t.Errorf("path = %q, want the native chat endpoint", h.gotPath)
	}
}

// TestOllamaReassemblesToolCalls covers both argument shapes: a JSON string
// and an already-decoded object.
func TestOllamaReassemblesToolCalls(t *testing.T) {
	h := newOllamaHarness(t, 200, []string{
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]},"done":false}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"grep","arguments":{"pattern":"x"}}}]},"done":false}`,
		`{"done":true,"done_reason":"tool_calls"}`,
	})
	msg, err := runOllama(t, h, ollamaTestModel(""))
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if msg.StopReason != StopToolUse {
		t.Errorf("stop = %q, want tool_use", msg.StopReason)
	}
	calls := toolCallsOf(msg)
	if len(calls) != 2 || calls[0].Name != "read" || calls[1].Name != "grep" {
		t.Fatalf("calls = %+v, want read then grep", calls)
	}
	if calls[0].Arguments["path"] != "a.go" || calls[1].Arguments["pattern"] != "x" {
		t.Errorf("arguments = %v, want decoded maps from both shapes", calls)
	}
}

func toolCallsOf(msg AssistantMessage) []ToolCall {
	var out []ToolCall
	for _, block := range msg.Content {
		if call, ok := block.(ToolCall); ok {
			out = append(out, call)
		}
	}
	return out
}

// TestOllamaMapsStopReasons pins the done_reason contract, including the
// load trap: a load is an error, never a clean empty stop.
func TestOllamaMapsStopReasons(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		extra  string
		want   StopReason
	}{
		{"length", "length", "", StopMaxTokens},
		{"tool_calls", "tool_calls", "", StopToolUse},
		{"load is an error", "load", "", StopError},
		{"missing with calls", "", `,"tool_calls":[{"function":{"name":"x","arguments":{}}}]`, StopToolUse},
		{"missing without calls", "", "", StopEndTurn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := []string{
				`{"message":{"role":"assistant","content":"t"` + tc.extra + `},"done":false}`,
				`{"done":true` + doneReasonJSON(tc.reason) + `}`,
			}
			h := newOllamaHarness(t, 200, lines)
			msg, err := runOllama(t, h, ollamaTestModel(""))
			// A load stop is terminal but erroneous: Result carries it.
			if tc.want == StopError {
				if err == nil {
					t.Fatal("a load stop returned no error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Result: %v", err)
			}
			if msg.StopReason != tc.want {
				t.Errorf("stop = %q, want %q", msg.StopReason, tc.want)
			}
		})
	}
}

func doneReasonJSON(reason string) string {
	if reason == "" {
		return ""
	}
	return `,"done_reason":"` + reason + `"`
}

// TestOllamaOmitsAuthWithoutAKey proves a local server gets no auth header:
// an empty bearer could trip a strict proxy, and local Ollama needs none.
func TestOllamaOmitsAuthWithoutAKey(t *testing.T) {
	h := newOllamaHarness(t, 200, []string{`{"done":true,"done_reason":"stop"}`})
	if _, err := runOllama(t, h, ollamaTestModel("")); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if h.gotAuth != "" {
		t.Errorf("Authorization = %q, want it absent for a keyless server", h.gotAuth)
	}
}

// TestOllamaSendsBearerWithAKey covers the cloud path.
func TestOllamaSendsBearerWithAKey(t *testing.T) {
	h := newOllamaHarness(t, 200, []string{`{"done":true,"done_reason":"stop"}`})
	provider := NewOllamaProvider("cloud-key")
	provider.Client = h.server.Client()
	provider.BaseURL = h.server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := provider.Stream(ctx, Request{
		Model:    ollamaTestModel(h.server.URL),
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if h.gotAuth != "Bearer cloud-key" {
		t.Errorf("Authorization = %q, want the bearer key", h.gotAuth)
	}
}

// TestOllamaClassifiesAuthFailures keeps a rejected key from reading as a
// generic error: the fix is a different key, not a retry.
func TestOllamaClassifiesAuthFailures(t *testing.T) {
	h := newOllamaHarness(t, 401, []string{`{"error":"invalid api key"}`})
	_, err := runOllama(t, h, ollamaTestModel(""))
	if err == nil || !strings.Contains(err.Error(), "rejected the credential") {
		t.Errorf("err = %v, want the auth classification", err)
	}
}

// TestOllamaUnreachableLocalNamesTheFix is the first-run failure: Ollama not
// running must say to start it, not dump a dial error.
func TestOllamaUnreachableLocalNamesTheFix(t *testing.T) {
	provider := NewOllamaProvider("")
	model := ollamaTestModel("http://127.0.0.1:9")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := provider.Stream(ctx, Request{
		Model:    model,
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("err = %v, want it to name `ollama serve`", err)
	}
}

// TestOllamaSystemOnlySurfacesLoadError proves a request with no user turn
// fails loudly: the static prefix stays on system (demoting it would break
// prefix caching), so the server reports done_reason load and the transport
// surfaces it as an error rather than a clean empty stop.
func TestOllamaSystemOnlySurfacesLoadError(t *testing.T) {
	h := newOllamaHarness(t, 200, []string{`{"done":true,"done_reason":"load"}`})
	provider := NewOllamaProvider("")
	provider.Client = h.server.Client()
	provider.BaseURL = h.server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := provider.Stream(ctx, Request{
		Model:  ollamaTestModel(h.server.URL),
		System: []SystemPromptBlock{{Text: "prefix"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Result(); err == nil || !strings.Contains(err.Error(), "done_reason: load") {
		t.Errorf("err = %v, want the load condition named", err)
	}
}

// TestOllamaSendsNumPredict bounds the completion through the native option.
func TestOllamaSendsNumPredict(t *testing.T) {
	h := newOllamaHarness(t, 200, []string{`{"done":true,"done_reason":"stop"}`})
	if _, err := runOllama(t, h, ollamaTestModel("")); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !strings.Contains(h.gotBody, `"num_predict":64`) {
		t.Errorf("body = %s, want the output bound sent", h.gotBody)
	}
}
