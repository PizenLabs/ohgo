package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// mixedCompactionMessages builds a deterministic long task containing reads,
// searches, edits, checks, failures, and fixes. The compaction algorithm only
// sees messages, but the fixture keeps the workload semantically realistic.
func mixedCompactionMessages(turns int) []Message {
	messages := []Message{UserMessage{Text: "fix the allocator and verify the store package"}}
	kinds := []struct {
		tool  string
		args  map[string]any
		body  string
		error bool
	}{
		{"ls", map[string]any{"path": "internal/store"}, strings.Repeat("file.go\n", 40), false},
		{"structure", map[string]any{"path": "internal/store"}, strings.Repeat("symbol\n", 60), false},
		{"read", map[string]any{"path": "internal/store/store.go"}, strings.Repeat("source line\n", 120), false},
		{"grep", map[string]any{"pattern": "sequence"}, strings.Repeat("match\n", 50), false},
		{"edit", map[string]any{"path": "internal/store/store.go"}, "edited 12 lines", false},
		{"verify", map[string]any{"command": "go test ./internal/store"}, "FAIL TestAllocator", true},
		{"read", map[string]any{"path": "internal/store/store.go"}, strings.Repeat("fixed source line\n", 100), false},
		{"edit", map[string]any{"path": "internal/store/store.go"}, "edited 3 lines", false},
		{"verify", map[string]any{"command": "go test ./internal/store"}, "PASS TestAllocator", false},
	}
	for i := range turns {
		spec := kinds[i%len(kinds)]
		id := fmt.Sprintf("c%d", i)
		if i%3 == 0 {
			messages = append(messages, UserMessage{Text: fmt.Sprintf("continue step %d", i)})
		}
		messages = append(messages, AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{ID: id, Name: spec.tool, Arguments: spec.args}},
		}})
		messages = append(messages, ToolResultMessage{
			ToolCallID: id, ToolName: spec.tool,
			Content: ai.TextBlocks(spec.body), IsError: spec.error,
		})
	}
	return messages
}

// TestCompactionPerformanceSizes measures small, medium, and large mixed
// workloads. Token counts are local estimates, never provider-billed totals.
func TestCompactionPerformanceSizes(t *testing.T) {
	projection := ContextProjection{
		Authority: "BUILD · mutation permitted",
		Objective: "fix the allocator and verify the store package",
		Blocks:    []string{"Verified facts: the allocator is central", "Next action: run the store test"},
	}
	for _, turns := range []int{50, 200, 500} {
		messages := mixedCompactionMessages(turns)
		recall := NewRecallStore()
		result, err := CompactWith(context.Background(), messages, CompactOptions{
			Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 6, KeepRecentToolResults: 2},
			Window:     24000,
			Summarize:  func(context.Context, []Message) (string, error) { return "s", nil },
			Projection: func() (ContextProjection, bool) { return projection, true },
			Recall:     recall,
		})
		if err != nil {
			t.Fatalf("%d turns: %v", turns, err)
		}
		ratio := float64(result.AfterTokens) / float64(result.BeforeTokens)
		fallbacks := 0
		if result.Mode == ModeSummarized {
			fallbacks = 1
		}
		t.Logf("%d turns: %d -> %d estimated tokens (%.2f%% retained), mode=%s, trimmed=%d, archived=%d, model_calls=%d, fallbacks=%d",
			turns, result.BeforeTokens, result.AfterTokens, ratio*100, result.Mode, result.Trimmed,
			recall.Len(), 0, fallbacks)
	}
}

// BenchmarkCompactionLongExploration measures the deterministic state-first
// path on a long exploration. Correctness is asserted by tests; this reports
// cost and reduction.
func BenchmarkCompactionLongExploration(b *testing.B) {
	messages := mixedCompactionMessages(200)
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
