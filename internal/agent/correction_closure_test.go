package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// countedRepairRead counts the calls that cross the execution boundary so the
// test can distinguish a closed repair window from one that merely narrates a
// closure.
type countedRepairRead struct {
	evidenceReadTool
	calls *atomic.Int64
}

func (tool countedRepairRead) Execute(ctx agent.Context, args map[string]any) agent.Result {
	tool.calls.Add(1)
	return tool.evidenceReadTool.Execute(ctx, args)
}

// A projector that has spent the repair allowance closes the action surface.
// The loop must honour that closure instead of restoring tools because the
// turn is still classified as a correction. Otherwise a model that ignores the
// closed-window notice gets more evidence than the lifecycle authorized, which
// is the failure mode in the observed investigation transcript.
func TestSpentRepairWindowDoesNotReopenTheToolSurface(t *testing.T) {
	var calls atomic.Int64
	read := countedRepairRead{calls: &calls}
	var toolCounts []int

	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Unsupported claim."}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{
			ID: "repair-read", Name: "read", Arguments: map[string]any{"path": "README.md"},
		}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{
			ai.TextContent{Text: "Supported final answer."},
			ai.ToolCall{ID: "stale-read", Name: "read", Arguments: map[string]any{"path": "go.mod"}},
		}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "Supported final answer."}}, StopReason: ai.StopEndTurn},
	}

	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			toolCounts = append(toolCounts, len(req.Tools))
			index := len(toolCounts) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:                   []agent.Tool{read},
		MaxTurns:                8,
		CorrectionToolCalls:     1,
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		AnswerReview: func(_ []agent.Turn, answer string) string {
			if strings.Contains(answer, "Unsupported") {
				return "the answer contains an unsupported claim"
			}
			return ""
		},
		Project: func(req agent.ProjectionRequest) agent.ProjectionResult {
			if req.Correcting && req.CorrectionBudget == 0 {
				return agent.ProjectionResult{
					Tools:   []agent.Tool{},
					Runtime: []string{"The repair window is closed."},
					Report: agent.ProjectionReport{
						AnswerEligible: true,
						Stage:          "answer",
					},
				}
			}
			return agent.ProjectionResult{Tools: []agent.Tool{read}}
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "analyze this project"}})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if len(toolCounts) != 3 {
		t.Fatalf("model requests = %d, want candidate, one repair read, final answer (3)", len(toolCounts))
	}
	if got := toolCounts[2]; got != 0 {
		t.Fatalf("closed-window request offered %d tools, want 0", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("executed calls = %d, want only the call admitted before the window closed", got)
	}
	if len(turns) != 3 || turns[2].Answer != agent.AnswerCommitted {
		t.Fatalf("turns = %+v, want the final answer committed after the stale call was dropped", turns)
	}
}
