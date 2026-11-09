package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

type denyEveryCall struct{}

func (denyEveryCall) Evaluate(ai.ToolCall, agent.Tool) security.Decision {
	return security.Decision{
		Outcome: security.OutcomeDeny,
		Rule:    "test_deny",
		Reason:  "not allowed in this test",
	}
}

func (denyEveryCall) Approve(ai.ToolCall, security.Decision) (security.Grant, bool) {
	return security.Grant{}, false
}

// A call refused before execution never reaches the progress hook, but it is
// still a real outcome. The live transcript must show it, or a denied action
// would silently vanish from the view while remaining in persisted history.
func TestDeniedToolCallStillAppearsInLiveTranscript(t *testing.T) {
	turn := 0
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		turn++
		if turn == 1 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "denied-1", Name: "bash", Arguments: map[string]any{"command": "rm -rf /"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "stopped"}},
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:     ai.Model{ID: "m", Provider: "p"},
		Stream:    stream,
		Tools:     []agent.Tool{newBlockingTool("bash")},
		Authorize: denyEveryCall{},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("run it")
	drainProgram(p)
	p.state.Apply(FrameTick{At: time.Now().Add(toolMinDwell)})

	var denied *Entry
	for i := range p.state.Entries {
		entry := &p.state.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.ID == "denied-1" {
			denied = entry
		}
	}
	if denied == nil {
		t.Fatalf("entries = %+v, want the denied call visible", p.state.Entries)
	}
	if denied.Tool.Status != ToolError {
		t.Errorf("status = %s, want an error block", denied.Tool.Status)
	}
	if !strings.Contains(denied.Tool.Text, "not allowed") {
		t.Errorf("text = %q, want the authorization reason", denied.Tool.Text)
	}
	if !strings.Contains(Render(p.state), "not allowed") {
		t.Errorf("rendered transcript hides the refusal:\n%s", Render(p.state))
	}
}
