package tui

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
)

// A completed turn must leave its answer in the transcript: the tool hook
// carries tool output, but the model's own words only exist on the turns, so
// runTurn publishes each turn's text. Without this the screen shows the
// user's prompts with blank space where the answers belong.
func TestRunTurnPublishesAssistantText(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "hello golang"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("what is golang") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn hung")
	}
	timeout := time.After(2 * time.Second)
	found := false
	for !found {
		select {
		case event := <-p.events:
			if delta, ok := event.(AssistantDelta); ok && delta.Text == "hello golang" {
				found = true
			}
			p.state.Apply(event)
		case <-timeout:
			t.Fatal("no AssistantDelta published: the UI would look idle after a success")
		}
	}
	if got := p.state.AssistantText(); got != "hello golang" {
		t.Fatalf("AssistantText = %q, want %q", got, "hello golang")
	}
}
