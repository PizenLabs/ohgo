package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

type surfaceRun struct {
	turns    []agent.Turn
	reports  []agent.ProjectionReport
	requests int64
	err      error
}

// runSurface drives one scripted run with an explicit projection. The script
// supplies one assistant message per provider request and fails if the runtime
// asks past it, so a test observes the exact number of model turns.
func runSurface(t *testing.T, tools []agent.Tool, script []ai.AssistantMessage, project func(agent.ProjectionRequest) agent.ProjectionResult, maxTurns int) surfaceRun {
	t.Helper()
	var requests atomic.Int64
	var reports []agent.ProjectionReport
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Tools:    tools,
		MaxTurns: maxTurns,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script; the runtime continued after terminal", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Project:   project,
		Authorize: allowRead{},
		OnProjection: func(report agent.ProjectionReport) {
			reports = append(reports, report)
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	turns, runErr := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "check this project"}})
	return surfaceRun{turns: turns, reports: reports, requests: requests.Load(), err: runErr}
}

func surfaceCall(id, name string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
		StopReason: ai.StopToolUse,
	}
}

func surfaceAnswer(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

func closedProjection() agent.ProjectionResult {
	return agent.ProjectionResult{
		Tools:  []agent.Tool{},
		Report: agent.ProjectionReport{AnswerEligible: true},
	}
}

// A projector's AnswerEligible flag is the control plane's semantic closure.
// The loop must withdraw the tools itself instead of trusting the projector to
// also return an empty slice, or a model could execute a call on a surface the
// runtime already declared closed.
func TestClosedSurfaceRefusesCallsEvenWhenAProjectorLeavesTools(t *testing.T) {
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			surfaceCall("stale", "ls", map[string]any{"path": ".ohgo"}),
			surfaceAnswer("the project is a controlled coding agent"),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools:  []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{AnswerEligible: true},
			}
		}, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the closed request plus one answer", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	first := result.turns[0]
	if len(first.Results) != 1 || !first.Results[0].Refused() {
		t.Fatalf("first outcomes = %+v, want one refusal", first.Results)
	}
	if !strings.Contains(first.Results[0].Result.Text, "No tool is available") {
		t.Fatalf("refusal text = %q, want the closed-surface reason", first.Results[0].Result.Text)
	}
	if got := result.turns[1].Assistant.Text(); got == "" {
		t.Fatal("the answer after the refusal was lost")
	}
}

// A projection may withhold the one tool a model keeps proposing while still
// offering others. The refusal is a control decision, so a run of
// nothing-but-refusals must reach a semantic verdict instead of the turn cap.
func TestPartialSurfaceRepeatedRefusalIsBounded(t *testing.T) {
	script := make([]ai.AssistantMessage, 0, 10)
	for i := 0; i < 10; i++ {
		script = append(script, surfaceCall("stale", "ls", map[string]any{"path": ".ohgo"}))
	}
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		script,
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools:  []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{Sufficiency: "INSUFFICIENT", Stage: "explore"},
			}
		}, 10)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked, not a resource stop", result.err)
	}
	if errors.Is(result.err, agent.ErrTurnLimit) {
		t.Fatal("the run ended at the turn cap instead of a semantic verdict")
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want one refusal plus one recovery", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	for i, turn := range result.turns {
		if len(turn.Results) != 1 || !turn.Results[0].Refused() {
			t.Fatalf("turn %d outcomes = %+v, want one refusal", i, turn.Results)
		}
	}
}

// A closed surface that produces a stale proposal and no answer gets exactly
// one recovery request; a second stale proposal is terminal. The run must not
// walk the turn grant.
func TestClosedSurfaceStaleProposalWithoutAnswerIsTerminal(t *testing.T) {
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			surfaceCall("stale-1", "ls", map[string]any{"path": ".ohgo"}),
			surfaceCall("stale-2", "ls", map[string]any{"path": ".ohgo"}),
			surfaceCall("stale-3", "ls", map[string]any{"path": ".ohgo"}),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult { return closedProjection() },
		10)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the bounded recovery (2)", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	for i, turn := range result.turns {
		if len(turn.Results) != 1 || !turn.Results[0].Refused() {
			t.Fatalf("turn %d outcomes = %+v, want one refusal", i, turn.Results)
		}
	}
}

// The one recovery request is legitimate: a model that answers after a refused
// stale proposal completes the run.
func TestClosedSurfaceStaleProposalThenAnswerIsAccepted(t *testing.T) {
	answer := "The project is a coding agent with a controlled runtime."
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			surfaceCall("stale", "ls", map[string]any{"path": ".ohgo"}),
			surfaceAnswer(answer),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult { return closedProjection() },
		6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want stale proposal then answer (2)", result.requests)
	}
	last := result.turns[len(result.turns)-1]
	if last.Assistant.Text() != answer {
		t.Fatalf("final answer = %q, want the recovered answer", last.Assistant.Text())
	}
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    result.turns,
		Contract: contract.IntentContract{Kind: contract.ObjectiveInformational, Objective: "analyze this project"},
	})
	if !got.Complete() {
		t.Fatalf("recovered answer = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A terminal projection is terminal at the first stale proposal: the runtime
// has already established that no action can make progress.
func TestTerminalSurfaceStaleProposalIsTerminalImmediately(t *testing.T) {
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			surfaceCall("stale", "ls", map[string]any{"path": ".ohgo"}),
			surfaceAnswer("should never be requested"),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools: []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{
					Terminal:   true,
					StopReason: "required capability failed",
				},
			}
		}, 6)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if !strings.Contains(result.err.Error(), "required capability failed") {
		t.Fatalf("block reason = %q, want the projection's terminal reason", result.err)
	}
	if result.requests != 1 {
		t.Fatalf("model requests = %d, want exactly one", result.requests)
	}
	if len(result.turns) != 1 || len(result.turns[0].Results) != 1 || !result.turns[0].Results[0].Refused() {
		t.Fatalf("outcomes = %+v, want one refusal", result.turns)
	}
}

// A terminal surface with no proposal and no answer is still blocked. Without
// this, an empty assistant message looked like a clean model stop and lost the
// terminal reason.
func TestTerminalSurfaceEmptyResponseKeepsTheBlockedReason(t *testing.T) {
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}},
		[]ai.AssistantMessage{{StopReason: ai.StopEndTurn}},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{Report: agent.ProjectionReport{
				Terminal:   true,
				StopReason: "required capability failed",
			}}
		}, 6)

	if !errors.Is(result.err, agent.ErrTaskBlocked) {
		t.Fatalf("Run error = %v, want ErrTaskBlocked", result.err)
	}
	if !strings.Contains(result.err.Error(), "required capability failed") {
		t.Fatalf("block reason = %q, want the projection's terminal reason", result.err)
	}
	if result.requests != 1 {
		t.Fatalf("model requests = %d, want exactly one", result.requests)
	}
}

// A refused proposal with visible text is not a completion event. The runtime
// withheld the action and asked the model to choose another; the text that
// accompanied it has not been seen with the refusal yet. The model gets one
// recovery request, and its answer is what ends the run. The text here only
// announces an action, so it is not a terminal answer; a substantive answer
// with only withheld observation calls is handled by the terminal transition.
func TestRefusedProposalWithTextGetsOneRecoveryRequest(t *testing.T) {
	const answer = "I cannot inspect .ohgo in this task stage; here is the analysis from the evidence."
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "Let me inspect .ohgo before answering."},
					ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": ".ohgo"}},
				},
				StopReason: ai.StopToolUse,
			},
			{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
		},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools:  []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{Sufficiency: "INSUFFICIENT", Stage: "explore"},
			}
		}, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the refusal plus one recovery answer (2)", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want the refusal and the answer", len(result.turns))
	}
	first := result.turns[0]
	if len(first.Results) != 1 || !first.Results[0].Refused() {
		t.Fatalf("outcomes = %+v, want one refusal", first.Results)
	}
	last := result.turns[len(result.turns)-1]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer = %q, want the model's recovery answer", last.Assistant.Text())
	}
}

// A final answer and tool calls can share one assistant message. On a closed
// surface those calls are continuation after the answer, not actions. The run
// must store only the answer: dispatching them would put red refusal cards
// after a finished answer and leave the stored conversation with a tool call
// that has no result.
func TestClosedSurfaceFinalAnswerDropsContinuationCalls(t *testing.T) {
	const answer = "The project is a controlled coding agent with a read-only evidence loop."
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: answer},
				ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": "."}},
			},
			StopReason: ai.StopToolUse,
		}},
		func(agent.ProjectionRequest) agent.ProjectionResult { return closedProjection() },
		6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 1 {
		t.Fatalf("model requests = %d, want exactly one", result.requests)
	}
	if len(result.turns) != 1 {
		t.Fatalf("turns = %d, want one", len(result.turns))
	}
	last := result.turns[0]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer = %q, want the model's text", last.Assistant.Text())
	}
	if len(last.Results) != 0 {
		t.Fatalf("outcomes = %+v, want no dispatched continuation", last.Results)
	}
	if calls := last.Assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("stored tool calls = %+v, want none", calls)
	}
	if last.Assistant.StopReason != ai.StopEndTurn {
		t.Fatalf("stop reason = %q, want end_turn after the answer", last.Assistant.StopReason)
	}

	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    result.turns,
		Contract: contract.IntentContract{Kind: contract.ObjectiveInformational, Objective: "analyze this project"},
	})
	if !got.Complete() {
		t.Fatalf("answer = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A task stage can withhold one observation capability while still offering
// others. When the model emits a substantive answer after a read, its batch
// repeats that read and adds the withheld listing: the whole batch is
// continuation, because neither call can add evidence. The loop must drop both
// before dispatch instead of publishing a refusal or a reuse card after the
// answer.
func TestPartialSurfaceFinalAnswerDropsContinuationBatch(t *testing.T) {
	const answer = "The project is a controlled coding agent with a read-only evidence loop."
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			surfaceCall("read", "read", map[string]any{"path": "README.md"}),
			{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: answer},
					ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": "."}},
					ai.ToolCall{ID: "read", Name: "read", Arguments: map[string]any{"path": "README.md"}},
				},
				StopReason: ai.StopToolUse,
			},
			surfaceAnswer("should never be requested"),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools:  []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{Sufficiency: "INSUFFICIENT", Stage: "explore"},
			}
		}, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the read plus the answer (2)", result.requests)
	}
	if len(result.turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(result.turns))
	}
	if len(result.turns[0].Results) != 1 {
		t.Fatalf("first outcomes = %+v, want the read", result.turns[0].Results)
	}
	last := result.turns[1]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer = %q, want the model's text", last.Assistant.Text())
	}
	if len(last.Results) != 0 {
		t.Fatalf("outcomes = %+v, want no dispatched continuation", last.Results)
	}
	if calls := last.Assistant.ToolCalls(); len(calls) != 0 {
		t.Fatalf("stored tool calls = %+v, want none", calls)
	}
	if last.Assistant.StopReason != ai.StopEndTurn {
		t.Fatalf("stop reason = %q, want end_turn after the answer", last.Assistant.StopReason)
	}
}

// A withheld policy-sensitive action is not observation continuation. Even
// when the model also emitted a substantive answer, the attempted mutation must
// stay visible to the user; the loop dispatches it and records the refusal.
func TestPartialSurfacePolicyRefusalAfterAnswerStaysVisible(t *testing.T) {
	result := runSurface(t,
		[]agent.Tool{surfaceWriteTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{
			{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "The project is a controlled coding agent."},
					ai.ToolCall{ID: "write", Name: "write", Arguments: map[string]any{"path": "x"}},
				},
				StopReason: ai.StopToolUse,
			},
			surfaceAnswer("the write was refused"),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools:  []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{Sufficiency: "INSUFFICIENT", Stage: "explore"},
			}
		}, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 2 {
		t.Fatalf("model requests = %d, want the refusal plus one recovery (2)", result.requests)
	}
	if len(result.turns) == 0 || len(result.turns[0].Results) != 1 {
		t.Fatalf("turns = %+v, want one refused write", result.turns)
	}
	outcome := result.turns[0].Results[0]
	if !outcome.Result.CandidateRejected || !outcome.Refused() {
		t.Fatalf("outcome = %+v, want a control refusal", outcome)
	}
	if outcome.Result.AnswerRequired {
		t.Fatal("a policy-sensitive mutation was classified as answer continuation")
	}
	if projected := agent.TranscriptTurn(result.turns[0]); len(projected.Results) != 1 {
		t.Fatalf("projected results = %+v, want the policy refusal preserved", projected.Results)
	}
}

type surfaceWriteTool struct{}

func (surfaceWriteTool) Name() string        { return "write" }
func (surfaceWriteTool) Description() string { return "write a file" }
func (surfaceWriteTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}}}`))
}
func (surfaceWriteTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierWrite} }
func (surfaceWriteTool) Exclusive(map[string]any) bool { return false }
func (surfaceWriteTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "should not run", Mutates: true}
}

// A mutation proposed on a closed surface is a policy refusal, not post-answer
// continuation. It stays visible in the transcript because it is an attempted
// action the user may need to review; only observation calls are projected out.
func TestClosedSurfacePolicyRefusalStaysVisible(t *testing.T) {
	result := runSurface(t,
		[]agent.Tool{surfaceWriteTool{}},
		[]ai.AssistantMessage{
			surfaceCall("write-1", "write", map[string]any{"path": "x"}),
			surfaceAnswer("the write was refused"),
		},
		func(agent.ProjectionRequest) agent.ProjectionResult { return closedProjection() },
		3)

	if len(result.turns) == 0 || len(result.turns[0].Results) != 1 {
		t.Fatalf("turns = %+v, want one refused write", result.turns)
	}
	outcome := result.turns[0].Results[0]
	if !outcome.Result.CandidateRejected || !outcome.Refused() {
		t.Fatalf("outcome = %+v, want a control refusal", outcome)
	}
	if outcome.Result.AnswerRequired {
		t.Fatal("a policy-sensitive mutation was classified as answer continuation")
	}
	if projected := agent.TranscriptTurn(result.turns[0]); len(projected.Results) != 1 {
		t.Fatalf("projected results = %+v, want the policy refusal preserved", projected.Results)
	}
}
