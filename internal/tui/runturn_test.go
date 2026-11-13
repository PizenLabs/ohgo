package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

type orderReadTool struct{ calls int }

func (*orderReadTool) Name() string                   { return "read" }
func (*orderReadTool) Description() string            { return "read a file" }
func (*orderReadTool) Parameters() *jsonschema.Schema { return nil }
func (*orderReadTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierRead} }
func (*orderReadTool) Exclusive(map[string]any) bool  { return false }
func (t *orderReadTool) Execute(agent.Context, map[string]any) agent.Result {
	t.calls++
	return agent.Result{Text: "content"}
}

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

// A reused observation produces a tool outcome without reaching Execute. Its
// start/finish pair must publish in its own turn; deferring it to the
// post-run pass replays an early read after the final answer.
func TestRunTurnPublishesReusedOutcomeBeforeFinalAnswer(t *testing.T) {
	tool := &orderReadTool{}
	index := 0
	script := []ai.AssistantMessage{
		{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "r1", Name: "read", Arguments: map[string]any{"path": "README.md"}}},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "r2", Name: "read", Arguments: map[string]any{"path": "README.md"}}},
			StopReason: ai.StopToolUse,
		},
	}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			if index == len(script) {
				index++
				// A live provider streams the answer before the run ends. A
				// completed stream with no text deltas cannot expose the
				// post-answer replay this test guards against.
				return ai.NewTextStream("the answer"), nil
			}
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
		Tools: []agent.Tool{tool},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("analyze")
	}()

	reusedFinished := false
	answerSeen := false
	answerBeforeReused := false
	process := func(event Event) {
		switch e := event.(type) {
		case ToolFinished:
			if e.ID == "r2" {
				reusedFinished = true
			}
		case AssistantDelta:
			if strings.Contains(e.Text, "the answer") {
				answerSeen = true
				answerBeforeReused = answerBeforeReused || !reusedFinished
			}
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			process(event)
		case <-done:
			// runTurn publishes synchronously before returning, so any event
			// still in the buffer belongs to this turn. Drain it before
			// asserting; selecting on done must not skip a buffered outcome.
			for {
				select {
				case event := <-p.events:
					process(event)
				default:
					if !answerSeen {
						t.Fatal("the final answer was never published")
					}
					if !reusedFinished {
						t.Fatal("the reused outcome was never published")
					}
					if answerBeforeReused {
						t.Fatal("the reused outcome was published after the final answer")
					}
					if tool.calls != 1 {
						t.Fatalf("read executed %d times, want the first call only", tool.calls)
					}
					return
				}
			}
		case <-timeout:
			t.Fatal("runTurn hung")
		}
	}
}
