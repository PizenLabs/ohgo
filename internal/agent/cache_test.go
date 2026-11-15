package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A cached-token drop is only a cache problem when the request did not change.
// The verdict names the cause instead of blaming the provider: a role switch, a
// tool change, or a compaction rebuilds the prefix by design.
func TestClassifyCacheSeparatesExpectedFromSuspicious(t *testing.T) {
	previous := CacheSample{Input: 320, CacheRead: 31296, Shape: RequestShape{
		SystemStable: true, ToolsStable: true, ModelStable: true, Messages: 8, StableMessages: 8,
	}}
	cases := []struct {
		name     string
		current  CacheSample
		want     string
		wantText string
	}{
		{
			name:     "appended history keeps the cache",
			current:  CacheSample{Input: 104, CacheRead: 31296, Shape: RequestShape{SystemStable: true, ToolsStable: true, ModelStable: true, Messages: 10, StableMessages: 8}},
			want:     CacheStable,
			wantText: "",
		},
		{
			name: "a role change rebuilds the system prefix",
			current: CacheSample{Input: 30000, CacheRead: 800, Shape: RequestShape{
				SystemStable: false, ToolsStable: true, ModelStable: true, Messages: 10, StableMessages: 8,
			}},
			want:     CacheExpected,
			wantText: "system",
		},
		{
			name: "a tool-set change rebuilds the request",
			current: CacheSample{Input: 30000, CacheRead: 800, Shape: RequestShape{
				SystemStable: true, ToolsStable: false, ModelStable: true, Messages: 10, StableMessages: 8,
			}},
			want:     CacheExpected,
			wantText: "tools",
		},
		{
			name: "a compaction rewrite is a deliberate rewind",
			current: CacheSample{Input: 6000, CacheRead: 1200, Shape: RequestShape{
				SystemStable: true, ToolsStable: true, ModelStable: true, Messages: 3, Rewound: true,
			}},
			want:     CacheExpected,
			wantText: "rewind",
		},
		{
			name: "the same request reusing less is suspicious",
			current: CacheSample{Input: 30000, CacheRead: 1200, Shape: RequestShape{
				SystemStable: true, ToolsStable: true, ModelStable: true, Messages: 10, StableMessages: 8,
			}},
			want:     CacheSuspicious,
			wantText: "repeated the previous prefix",
		},
		{
			name: "a suppressed rewrite is a client defect",
			current: CacheSample{Input: 30000, CacheRead: 1200, Shape: RequestShape{
				SystemStable: true, ToolsStable: true, ModelStable: true, Messages: 10, StableMessages: 4, Rewrites: 3,
			}},
			want:     CacheSuspicious,
			wantText: "rewrote already-sent history",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			regression, ok := ClassifyCache(previous, tc.current)
			if !ok {
				t.Fatal("no verdict for two measured requests")
			}
			if regression.Class != tc.want {
				t.Fatalf("class = %s, want %s (%s)", regression.Class, tc.want, regression.Detail)
			}
			if !strings.Contains(regression.Detail, tc.wantText) {
				t.Fatalf("detail = %q, want it to name %q", regression.Detail, tc.wantText)
			}
			if regression.Delta != tc.current.CacheRead-previous.CacheRead {
				t.Fatalf("delta = %d, want %d", regression.Delta, tc.current.CacheRead-previous.CacheRead)
			}
		})
	}
}

// A request whose prefix did not change must never be flagged: growth is the
// normal case and it never invalidates a cached prefix.
func TestClassifyCacheIgnoresAppendedHistory(t *testing.T) {
	shape := RequestShape{SystemStable: true, ToolsStable: true, ModelStable: true, Messages: 4, StableMessages: 4}
	if causes := shape.Causes(); len(causes) != 0 {
		t.Fatalf("causes = %v, want none for an append-only request", causes)
	}
	if _, ok := ClassifyCache(CacheSample{}, CacheSample{Input: 10, CacheRead: 5}); ok {
		t.Fatal("an unmeasured previous request produced a verdict")
	}
}

// The loop restores the bytes a message was first sent with, even when the
// projection re-renders it later. The failure a consumer sees without this is a
// provider cache that never hits: every request rewrites a few hundred bytes in
// the middle of the conversation and invalidates everything after them.
func TestLoopKeepsAlreadySentMessagesStable(t *testing.T) {
	const original = "ORIGINAL OBSERVATION"
	const rewritten = "RE-RENDERED OBSERVATION"
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "a"}},
		}, StopReason: ai.StopToolUse},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c2", Name: "read", Arguments: map[string]any{"value": "b"}},
		}, StopReason: ai.StopToolUse},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", original)},
		// A projector that rewrites every earlier tool result on every
		// request: the behaviour the real projection used to have.
		Project: func(req ProjectionRequest) ProjectionResult {
			out := make([]Message, 0, len(req.Messages))
			for i, message := range req.Messages {
				result, ok := message.(ToolResultMessage)
				if !ok || i == len(req.Messages)-1 {
					out = append(out, message)
					continue
				}
				out = append(out, ToolResultMessage{
					ToolCallID: result.ToolCallID,
					ToolName:   result.ToolName,
					Content:    ai.TextBlocks(rewritten),
				})
			}
			return ProjectionResult{Messages: out}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := requests()
	if len(got) < 2 {
		t.Fatalf("requests = %d, want at least 2", len(got))
	}
	for _, request := range got[1:] {
		for _, message := range request.Messages {
			result, ok := message.(ai.ToolResultMessage)
			if ok && result.Text() == rewritten {
				t.Fatalf("an already-sent tool result was rewritten on a later request: %#v", request.Messages)
			}
		}
	}
}

// Restoring earlier bytes is only safe while the two lists are the same
// conversation. A host that reconstructs history with a different structure
// must get its own request, not a positionally patched one.
func TestLoopDoesNotRestoreAcrossAStructuralChange(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "first"}}, StopReason: ai.StopEndTurn},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "second"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "x")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "one"}}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// The second run reconstructs a longer history whose first message is an
	// assistant turn, not the user request the first request carried.
	if _, err := loop.Run(ctx, []Message{
		AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "reconstructed"}}}},
		UserMessage{Text: "two"},
	}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	got := requests()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	first := got[1].Messages[0]
	assistant, ok := first.(ai.AssistantMessage)
	if !ok || assistant.Text() != "reconstructed" {
		t.Fatalf("message 0 = %#v, want the reconstructed history untouched", first)
	}
}

// The ledger keeps the per-request samples the turn totals cannot express.
func TestUsageLogRecordsCacheSamples(t *testing.T) {
	var log UsageLog
	if _, ok := log.LastCache(); ok {
		t.Fatal("an empty ledger reported a sample")
	}
	if _, _, ok := log.CacheRegression(); ok {
		t.Fatal("one sample produced a regression verdict")
	}
	log.Observe(CacheSample{Input: 320, CacheRead: 31296, Shape: RequestShape{SystemStable: true, ToolsStable: true, ModelStable: true}})
	log.Observe(CacheSample{Input: 30000, CacheRead: 900, Shape: RequestShape{SystemStable: false}})
	last, ok := log.LastCache()
	if !ok || last.CacheRead != 900 {
		t.Fatalf("last cache = %+v, want the newest sample", last)
	}
	if ratio := last.CacheRatio(); ratio > 0.05 {
		t.Fatalf("cache ratio = %v, want the uncached request to read as uncached", ratio)
	}
	regression, current, ok := log.CacheRegression()
	if !ok {
		t.Fatal("two samples produced no verdict")
	}
	if current.CacheRead != 900 || regression.Class != CacheExpected || regression.Delta != -30396 {
		t.Fatalf("regression = %+v current = %+v", regression, current)
	}
}
