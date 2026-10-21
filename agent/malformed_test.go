package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
)

// A model that emits arguments the tool schema rejects must produce an
// observable error result, not a panic or a silent success.
func TestInvalidToolArgumentsAreAnObservableFailure(t *testing.T) {
	stream, _ := scriptedStream(
		ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:        "bad-call",
				Name:      "read",
				Arguments: map[string]any{"value": 42.0}, // schema wants a string
			}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "I could not call the tool."}},
			StopReason: ai.StopEndTurn,
		},
	)
	tool := newScriptedTool("read", "contents")
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{tool},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) == 0 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %+v, want one malformed call outcome", turns)
	}
	outcome := turns[0].Results[0]
	if !outcome.Result.IsError {
		t.Fatal("malformed arguments were recorded as a successful call")
	}
	if !strings.Contains(outcome.Result.Text, "Invalid arguments") {
		t.Fatalf("the failure did not name the malformed arguments: %q", outcome.Result.Text)
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("the tool executed despite invalid arguments")
	}
}
