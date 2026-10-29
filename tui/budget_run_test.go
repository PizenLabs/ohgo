package tui

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The turn boundary is the grant boundary: the program installs the host's new
// root budget before execution and records its consumption afterwards.
func TestRunTurnInstallsAndRecordsTheRunBudget(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "hello"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	var installed *budget.Budget
	var recorded budget.Snapshot
	p.SetTaskHooks(TaskHooks{
		RunBudget: func() *budget.Budget {
			installed = budget.New(budget.Limits{Turns: 1, ModelCalls: 1})
			return installed
		},
		RecordBudget: func(snapshot budget.Snapshot) { recorded = snapshot },
	})

	p.runTurn("hello")
	if loop.Budget() != installed {
		t.Fatal("the loop did not receive the run's budget instance")
	}
	if recorded.Used.Turns != 1 || recorded.Used.ModelCalls != 1 {
		t.Fatalf("recorded budget = %+v, want the one admitted turn", recorded.Used)
	}
}

// A second turn is a new grant, not a reuse of the exhausted one. The host
// decides the policy; the program must not silently carry the old instance.
func TestRunTurnGivesEachTurnItsOwnGrant(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	var grants []*budget.Budget
	p.SetTaskHooks(TaskHooks{
		RunBudget: func() *budget.Budget {
			grant := budget.New(budget.Limits{Turns: 1})
			grants = append(grants, grant)
			return grant
		},
		RecordBudget: func(budget.Snapshot) {},
	})
	p.runTurn("first")
	p.runTurn("second")
	if len(grants) != 2 || grants[0] == grants[1] {
		t.Fatalf("grants = %d (same=%v), want two distinct run grants", len(grants), len(grants) == 2 && grants[0] == grants[1])
	}
}
