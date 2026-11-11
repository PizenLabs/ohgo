package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A rework follow-up under ASK is a capability mismatch, not a design turn. It
// must be refused before the model is called, and it must not create or
// complete a task.
func TestRedesignUnderAskIsRefusedBeforeTheModel(t *testing.T) {
	root := t.TempDir()
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{&idleTool{}},
		[]ai.AssistantMessage{readinessAnswer("this must not run")})
	before := *r.index
	r.turn("redesign it")
	if *r.index != before {
		t.Fatal("the model was called for a mutation objective under ASK")
	}
	if _, ok := r.controller.Current(); ok {
		t.Fatal("a refused mutation objective created a task")
	}
	if notice := lastNotice(r.program); !strings.Contains(notice, "BUILD") {
		t.Fatalf("refusal notice = %q, want the required BUILD authority", notice)
	}
}

// A design turn is informational. The follow-up "redesign it" is an
// implementation objective, not a second design request: it must derive a
// mutation contract, detach the finished design task, and refuse to prove the
// new task from prose alone.
func TestRedesignFollowUpStartsANewMutationTaskAndCannotProveFromDesignWork(t *testing.T) {
	root := t.TempDir()
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })

	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{
				Text: "The screen uses a top bar, an identity rail, and animated panels.",
			}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []agent.Tool{&idleTool{}},
		MaxTurns: 8,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetCompletionContext(root, fixedResolver{})
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return "build" }})
	program.SetTaskHooks(runtimeHooks(controller))

	program.runTurn("I want to design a single-screen animation where clicking various buttons animates the information.")
	design, ok := controller.Current()
	if !ok {
		t.Fatal("the design turn recorded no task")
	}
	if design.Contract == nil || design.Contract.Kind != contract.ObjectiveInformational {
		t.Fatalf("design contract = %+v, want informational", design.Contract)
	}
	if design.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("design objective = %s (%s), want PROVEN for the answered design turn",
			design.ObjectiveState(), design.ReviewReason)
	}

	program.runTurn("redesign it")
	redesign, ok := controller.Current()
	if !ok {
		t.Fatal("the redesign turn recorded no task")
	}
	if redesign.ID == design.ID {
		t.Fatal("the redesign reused the finished design task")
	}
	if redesign.Contract == nil || redesign.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("redesign contract = %+v, want mutation", redesign.Contract)
	}
	if redesign.Proven() || redesign.Status.IsTerminal() {
		t.Fatalf("redesign was proven from design prose: status=%s objective=%s proof=%+v",
			redesign.Status, redesign.ObjectiveState(), redesign.Completion)
	}
	if redesign.ObjectiveState() != task.ObjectiveNeedsReview {
		t.Fatalf("redesign objective = %s (%s), want NEEDS_REVIEW for the missing change",
			redesign.ObjectiveState(), redesign.ReviewReason)
	}
}
