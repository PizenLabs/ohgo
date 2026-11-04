package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
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

// A refused proposal with visible text is a final answer opportunity, not a
// reason to ask the model again. The refusal is a control decision and leaves
// no outstanding work.
func TestRefusedProposalWithTextEndsTheRun(t *testing.T) {
	answer := "I cannot inspect .ohgo in this task stage; here is the analysis from the evidence."
	result := runSurface(t,
		[]agent.Tool{evidenceListTool{}, evidenceReadTool{}},
		[]ai.AssistantMessage{{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: answer},
				ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": ".ohgo"}},
			},
			StopReason: ai.StopToolUse,
		}},
		func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{
				Tools:  []agent.Tool{evidenceReadTool{}},
				Report: agent.ProjectionReport{Sufficiency: "INSUFFICIENT", Stage: "explore"},
			}
		}, 6)

	if result.err != nil {
		t.Fatalf("Run: %v", result.err)
	}
	if result.requests != 1 {
		t.Fatalf("model requests = %d, want exactly one", result.requests)
	}
	last := result.turns[len(result.turns)-1]
	if last.Assistant.Text() != answer {
		t.Fatalf("answer = %q, want the model's text", last.Assistant.Text())
	}
	if len(last.Results) != 1 || !last.Results[0].Refused() {
		t.Fatalf("outcomes = %+v, want one refusal", last.Results)
	}
}
