package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

// evidenceReadTool returns the same attributable observation the real read
// tool returns for a complete file window.
type evidenceReadTool struct{}

func (evidenceReadTool) Name() string        { return "read" }
func (evidenceReadTool) Description() string { return "read a file" }
func (evidenceReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{"path":{"type":"string"}},
		"required":["path"]
	}`))
}
func (evidenceReadTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (evidenceReadTool) Exclusive(map[string]any) bool { return false }
func (evidenceReadTool) Execute(_ agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	if path == "" {
		path = "README.md"
	}
	return agent.Result{
		Text: "1\t# ohgo\n2\tA coding agent with a controlled runtime.",
		Details: map[string]any{
			"path": path, "offset": 1, "limit": 2000, "totalLines": 2,
		},
	}
}

// allowRead authorizes a read-only test capability.
type allowRead struct{}

func (allowRead) Evaluate(ai.ToolCall, agent.Tool) security.Decision {
	return security.Decision{Outcome: security.OutcomeAuto, Rule: "test_allow"}
}
func (allowRead) Approve(ai.ToolCall, security.Decision) (security.Grant, bool) {
	return security.Grant{}, true
}

type informationalResult struct {
	turns    []agent.Turn
	intent   contract.IntentContract
	report   agent.ProjectionReport
	requests int64
	err      error
}

// runInformational executes one informational turn against a scripted model.
// The script is consumed one message per provider request; running past it is a
// failure, so the test observes exactly how many requests the runtime made.
func runInformational(t *testing.T, script []ai.AssistantMessage) informationalResult {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/ohgo\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# ohgo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := "check this project and analyze what this project is about?"
	intent := workspace.BuildIntentContract(prompt)
	if intent.Kind != contract.ObjectiveInformational {
		t.Fatalf("intent kind = %s, want informational", intent.Kind)
	}
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "ask", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	var requests atomic.Int64
	var lastReport agent.ProjectionReport
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the runtime continued after the answer", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools:     []agent.Tool{evidenceReadTool{}},
		Project:   projector.Project,
		Authorize: allowRead{},
		MaxTurns:  6,
		OnProjection: func(report agent.ProjectionReport) {
			lastReport = report
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: prompt}})
	return informationalResult{turns: turns, intent: intent, report: lastReport, requests: requests.Load(), err: err}
}

func readCall(id string) []ai.ContentBlock {
	return []ai.ContentBlock{ai.ToolCall{
		ID: id, Name: "read", Arguments: map[string]any{"path": "README.md"},
	}}
}

// overviewCall supplies the two content observations a repository overview
// needs: a document and the module manifest.
func overviewCall(id string) []ai.ContentBlock {
	return []ai.ContentBlock{
		ai.ToolCall{ID: id + "-readme", Name: "read", Arguments: map[string]any{"path": "README.md"}},
		ai.ToolCall{ID: id + "-mod", Name: "read", Arguments: map[string]any{"path": "go.mod"}},
	}
}

// Once the required repository evidence is known, the next request is for the
// answer. The runtime must withhold every tool instead of letting the model
// observe more files merely because it can.
func TestSufficientInformationalEvidenceMakesTheNextTurnAnswerOnly(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime: explicit authority, bounded budgets, and evidence-based completion."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want 2: the runtime kept offering tools after sufficiency", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	if result.turns[1].Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the model's answer", result.turns[1].Assistant.Text())
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A model that answers and still asks for another action after the runtime
// moved to answer-only must not keep the run alive. The answer is preserved,
// the call is visibly refused without execution, and the loop stops.
func TestAnswerWithUnnecessaryCallStopsAndStaysComplete(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: answer},
				ai.ToolCall{ID: "read-after-answer", Name: "read", Arguments: map[string]any{"path": "README.md"}},
			},
			StopReason: ai.StopToolUse,
		},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want 2", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	last := result.turns[1]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer lost: %q", last.Assistant.Text())
	}
	if len(last.Results) != 1 || !last.Results[0].Result.AnswerRequired {
		t.Fatalf("last results = %+v, want one answer-required refusal", last.Results)
	}
	if !last.Results[0].Result.IsError {
		t.Fatal("a withheld call must remain visibly refused")
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED from the preserved answer", got.State, got.Reason)
	}
}

// A model that needs the empty surface repeated once before answering does so
// without executing anything after sufficiency.
func TestAnswerlessContinuationRecoversAfterARefusal(t *testing.T) {
	answer := "ohgo is a coding agent with a controlled runtime."
	result := runInformational(t, []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
		{Content: readCall("read-2"), StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	})

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 3 {
		t.Fatalf("model requests = %d, want 3 (the refusal, then the answer)", result.requests)
	}
	if len(result.turns) != 3 || result.turns[2].Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the answer after the refusal", result.turns[2].Assistant.Text())
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
		EvidenceReview: result.report.EvidenceReview(),
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A probe that establishes absence is an observation, not an unrecovered
// failure. It remains visible to the model and the user, but it cannot veto an
// otherwise correct answer.
func TestObservedAbsenceDoesNotBlockCompletion(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "read",
			Result: agent.Result{
				Text:            "stat \"README.md\": no such file or directory",
				IsError:         true,
				ObservedAbsence: true,
			},
		}}},
		{Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "There is no README yet; index.html is the entry point."}},
		}},
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "check this project",
		},
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A final message that still contains serialized tool syntax is not an
// ordinary answer, even when its text is non-empty. Completion must report
// review rather than success.
func TestProtocolResidueVetoesInformationalCompletion(t *testing.T) {
	turn := agent.Turn{Assistant: ai.AssistantMessage{
		Content:         []ai.ContentBlock{ai.TextContent{Text: `<dots_function_call><invoke name="ls"><parameter name="path">x`}},
		ProtocolResidue: true,
	}}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{turn},
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "check this project",
		},
	})
	if got.Complete() {
		t.Fatal("serialized protocol residue was reported as a completed answer")
	}
	if !strings.Contains(got.Reason, "serialized tool syntax") {
		t.Fatalf("reason = %q, want the protocol-residue reason", got.Reason)
	}
}

// A model that never answers is bounded by the turn grant. Its refusals execute
// nothing, and the run is never reported as a successful analysis.
func TestAnswerlessContinuationIsBoundedAndNotCompleted(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: overviewCall("turn-1"), StopReason: ai.StopToolUse},
	}
	for i := 0; i < 5; i++ {
		script = append(script, ai.AssistantMessage{
			Content: readCall("read-" + string(rune('a'+i))), StopReason: ai.StopToolUse,
		})
	}
	result := runInformational(t, script)

	if !errors.Is(result.err, agent.ErrTurnLimit) {
		t.Fatalf("Run error = %v, want ErrTurnLimit", result.err)
	}
	if result.requests != 6 {
		t.Fatalf("model requests = %d, want the turn grant (6)", result.requests)
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: result.turns, Contract: result.intent,
	})
	if got.Complete() {
		t.Fatal("a run with no answer was reported complete")
	}
}
