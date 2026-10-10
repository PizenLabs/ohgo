package agent

import (
	"context"
	"errors"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"strings"
	"sync/atomic"
	"testing"
)

// TestLoopCompactsWhenContextGrowsPastTheTrigger proves the wiring, not just
// the primitives.
//
// A model that keeps producing tool calls grows the context past the trigger.
// Without the loop integration the conversation would keep growing until the
// provider rejected it, so the observable contract is that the context the
// second request sees has been replaced by a summary.
func TestLoopCompactsWhenContextGrowsPastTheTrigger(t *testing.T) {
	// A tiny window plus a low threshold makes the trigger fire quickly.
	cfg := Config{
		Model:      ai.Model{ID: "test", Provider: "anthropic", API: ai.APIAnthropicMessages, ContextWindow: 1000},
		Tools:      []Tool{&countingTool{name: "read"}},
		MaxTokens:  128,
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 2},
		Summarize: func(ctx context.Context, messages []Message) (string, error) {
			return "SUMMARY-OF-HISTORY", nil
		},
	}

	var compactions atomic.Int64

	// Each turn emits one tool call with a large argument, which is what grows
	// the context past the trigger.
	var turns atomic.Int64
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		// A turn cap makes a missing compaction fail loudly instead of
		// spinning forever: the mock never voluntarily stops once the context
		// is compacted, so without the cap the only thing standing between a
		// broken loop and a hang is the compaction itself.
		if turns.Add(1) > 25 {
			return nil, errTestLoopRanAway
		}
		big := strings.Repeat("x", 2000)
		call := ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": big}}},
			StopReason: ai.StopToolUse,
			Usage:      ai.Usage{Total: 10},
		}
		// Once the summary appears the context has been compacted, so stop.
		if strings.Contains(strings.Join(messageTexts(req.Messages), ""), "SUMMARY-OF-HISTORY") {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		return ai.NewCompletedStream(call), nil
	}

	cfg.Stream = streamFn
	loop, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loop.cfg.OnCompact = func(result CompactResult) {
		compactions.Add(1)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "start"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := compactions.Load(); got == 0 {
		t.Fatal("no compaction ran despite the context outgrowing the window")
	}
}

// errTestLoopRanAway marks a mock provider that was called too many times,
// which means the loop never compacted.
var errTestLoopRanAway = errors.New("agent: test loop ran away; compaction never fired")

// messageTexts flattens a request's messages, for matching on content.
func messageTexts(messages []ai.Message) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			out = append(out, m.Text())
		case ai.AssistantMessage:
			out = append(out, m.Text())
		case ai.ToolResultMessage:
			out = append(out, m.Text())
		}
	}
	return out
}

func TestLoopRejectsCompactionWithoutSummarizer(t *testing.T) {
	// Compaction enabled with no summarizer is a misconfiguration that would
	// otherwise surface only when the window fills.
	_, err := New(Config{
		Model:      catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:     completedStreamFn(ai.AssistantMessage{}),
		Tools:      []Tool{&countingTool{name: "read"}},
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize},
	})
	if err == nil {
		t.Fatal("expected compaction without a summarizer to be rejected")
	}
}

func TestLoopAcceptsDisabledCompactionWithoutSummarizer(t *testing.T) {
	// The default configuration must not demand a summarizer.
	_, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: completedStreamFn(ai.AssistantMessage{}),
		Tools:  []Tool{&countingTool{name: "read"}},
	})
	if err != nil {
		t.Fatalf("New with default settings: %v", err)
	}
}
