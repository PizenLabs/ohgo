package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A terminal run must publish its verdict after the outcomes that led to it.
// Publishing the error first made the transcript look as though refused tools
// were still executing after the runtime had closed the task.
func TestRunTurnPublishesTerminalVerdictAfterRefusedOutcomes(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		MaxTurns: 5,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "stale", Name: "ls", Arguments: map[string]any{"path": ".ohgo"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	// A durable evidence state the real projector closes on: two content
	// observations satisfy the repository survey, so the next request is for
	// the answer and every tool is withheld.
	evidence := cognition.NewEvidenceState()
	for _, path := range []string{"README.md", "main.go"} {
		evidence.ObserveOutcome(agent.ToolOutcome{
			CallID: "read-" + path, ToolName: "read", Args: map[string]any{"path": path},
			Result: agent.Result{
				Text: "1\tcontent of " + path,
				Details: map[string]any{
					"path": path, "totalLines": 1, "offset": 1, "limit": 2000,
				},
			},
		})
	}
	p.SetTaskHooks(TaskHooks{
		Evidence: func() *cognition.EvidenceState { return evidence },
	})

	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("check this project and analyze what this project is about") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn hung")
	}

	var events []Event
	for {
		select {
		case event := <-p.events:
			events = append(events, event)
			continue
		default:
		}
		break
	}
	verdict := -1
	lastOutcome := -1
	lastEvent := -1
	for i, event := range events {
		switch typed := event.(type) {
		case Notice:
			if strings.HasPrefix(typed.Text, "error:") && strings.Contains(typed.Text, "task blocked") {
				verdict = i
			}
		case ToolStarted, ToolFinished:
			lastOutcome = i
		case AssistantDone:
			lastEvent = i
		}
	}
	if verdict < 0 {
		t.Fatalf("no terminal verdict reached the transcript: %+v", events)
	}
	if lastOutcome < 0 {
		t.Fatal("the refused outcome never reached the transcript")
	}
	if verdict < lastOutcome {
		t.Fatalf("terminal verdict at %d preceded outcome at %d: %+v", verdict, lastOutcome, events)
	}
	if lastEvent < verdict {
		t.Fatalf("turn was marked done before its verdict: done=%d verdict=%d", lastEvent, verdict)
	}
}
