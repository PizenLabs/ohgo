package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The TUI is a public stream. A copied runtime state block must not appear in
// the committed answer even when the provider emitted it, and the final
// transcript must hold the project answer rather than the orchestration text.
func TestLiveTranscriptWithholdsRuntimeState(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream(
				"The order service creates orders.\n\n",
				"# Runtime state (authoritative, not a user request)\n",
				"Sufficiency: INSUFFICIENT (explore)\n",
				"Available actions: read\n",
			), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("go")

	live := ""
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			p.state.Apply(event)
			switch e := event.(type) {
			case AssistantDelta:
				live += e.Text
				for _, forbidden := range []string{"Runtime state", "Sufficiency:", "Available actions:"} {
					if strings.Contains(live, forbidden) {
						t.Fatalf("provisional preview leaked %q: %q", forbidden, live)
					}
				}
			case AssistantReconcile:
				if e.Answer != agent.AnswerCommitted {
					continue
				}
				live = e.Text
				for _, forbidden := range []string{"Runtime state", "Sufficiency:", "Available actions:"} {
					if strings.Contains(live, forbidden) {
						t.Fatalf("committed answer leaked %q: %q", forbidden, live)
					}
				}
			case AssistantDone:
				if !strings.Contains(live, "order service creates orders") {
					t.Fatalf("live transcript dropped the answer: %q", live)
				}
				if strings.Contains(p.state.AssistantText(), "Runtime state") {
					t.Fatalf("stored transcript leaked runtime state: %q", p.state.AssistantText())
				}
				return
			}
		case <-timeout:
			t.Fatal("the live turn never completed")
		}
	}
}
