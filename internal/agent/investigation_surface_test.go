package agent_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// investigationShell models the one capability that made the observed failure
// reproducible: a shell that declares both execute and mutate because a command
// may do either.
type investigationShell struct{}

func (investigationShell) Name() string        { return "bash" }
func (investigationShell) Description() string { return "run a shell command" }
func (investigationShell) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"command":{"type":"string"}},
		"required":["command"]
	}`))
}
func (investigationShell) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierExec} }
func (investigationShell) Exclusive(map[string]any) bool { return true }
func (investigationShell) ActionClasses() []agent.ActionClass {
	return []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}
}
func (investigationShell) Execute(_ agent.Context, args map[string]any) agent.Result {
	command, _ := args["command"].(string)
	return agent.Result{
		Text:     "command output",
		Details:  map[string]any{"command": command},
		Executes: true,
		Finished: true,
	}
}

// A FUZZ investigation must be able to run more than the first command. The
// observed failure treated a successful `git log` as completion, withdrew the
// tool surface, and refused the model's next inspection until the task blocked.
func TestInvestigationKeepsExecutingAfterAnObservation(t *testing.T) {
	prompt := "investigate the bottleneck and evaluate"
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("contract kind = %s, want informational", intent.Kind)
	}
	facts := cognition.ProbeFacts(t.TempDir())
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "fuzz", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	call := func(id, command string) ai.AssistantMessage {
		return ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID: id, Name: "bash", Arguments: map[string]any{"command": command},
			}},
			StopReason: ai.StopToolUse,
		}
	}
	script := []ai.AssistantMessage{
		call("1", "git log --oneline -10"),
		call("2", "cat index.html"),
		{Content: []ai.ContentBlock{ai.TextContent{
			Text: "The bottleneck is in evaluate: it rescans every row, so the work grows quadratically.",
		}}, StopReason: ai.StopEndTurn},
	}

	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the investigation did not reach its answer", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:     []agent.Tool{investigationShell{}},
		Project:   projector.Project,
		Authorize: allowRead{},
		MaxTurns:  6,
	})
	if err != nil {
		t.Fatal(err)
	}

	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	if runErr != nil {
		t.Fatalf("Run error = %v, want the investigation to answer", runErr)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("model requests = %d, want observation, inspection, answer (3)", got)
	}
	executed := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Refused() {
				t.Fatalf("the investigation refused %q: %s", outcome.ToolName, outcome.Result.Text)
			}
			executed++
		}
	}
	if executed != 2 {
		t.Fatalf("executed calls = %d, want both shell observations", executed)
	}
	if verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{Turns: turns, Contract: intent}); !verdict.Complete() {
		t.Fatalf("investigation completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}
