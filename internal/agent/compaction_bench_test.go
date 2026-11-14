package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// BenchmarkCompactionLongExploration measures the deterministic state-first
// path on a long exploration. Correctness is asserted by the accompanying
// tests; this benchmark reports the cost and the reduction so the claim can be
// checked against a real measurement instead of an estimate.
func BenchmarkCompactionLongExploration(b *testing.B) {
	var messages []Message
	messages = append(messages, UserMessage{Text: "understand the store package"})
	for i := range 200 {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages, AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{ID: id, Name: "ls", Arguments: map[string]any{"path": fmt.Sprintf("dir%d", i)}}},
		}})
		messages = append(messages, ToolResultMessage{
			ToolCallID: id, ToolName: "ls",
			Content: ai.TextBlocks(strings.Repeat("entry\n", 120)),
		})
	}
	projection := ContextProjection{
		Authority: "ASK · read-only",
		Objective: "understand the store package",
		Blocks:    []string{"Verified facts: the package owns allocation", "Next action: answer"},
	}
	opts := CompactOptions{
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 4, KeepRecentToolResults: 2},
		Window:     32000,
		Summarize:  func(context.Context, []Message) (string, error) { return "s", nil },
		Projection: func() (ContextProjection, bool) { return projection, true },
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, err := CompactWith(context.Background(), messages, opts)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(result.AfterTokens)/float64(result.BeforeTokens), "after/before")
	}
}
