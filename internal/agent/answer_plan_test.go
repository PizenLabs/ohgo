package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	exectools "github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The answer boundary requires a pre-prose plan. A refused plan reopens the
// evidence surface; the next accepted plan unlocks the answer, which is then
// committed. No answer prose is generated before the plan passes.
func TestAnswerPlanGateRunsBeforeProse(t *testing.T) {
	script := []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:   "plan-1",
				Name: agent.AnswerPlanToolName,
				Arguments: map[string]any{
					"claims": []any{"The project is a Go coding agent."},
				},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:        "read-1",
				Name:      "read",
				Arguments: map[string]any{"path": "README.md"},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:   "plan-2",
				Name: agent.AnswerPlanToolName,
				Arguments: map[string]any{
					"claims": []any{"The project is a Go coding agent."},
				},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "The project is a Go coding agent."}},
			StopReason: ai.StopEndTurn,
		},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			if index == 1 {
				for _, tool := range req.Tools {
					if tool.Name == agent.AnswerPlanToolName {
						t.Fatalf("refused plan reopened with the plan tool still offered")
					}
				}
				if req.ToolChoice.Type != "any" {
					t.Fatalf("evidence request tool choice = %q, want any", req.ToolChoice.Type)
				}
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools: []agent.Tool{
			evidenceReadTool{},
			exectools.NewPrepareAnswerTool(),
		},
		Project: func(agent.ProjectionRequest) agent.ProjectionResult {
			// The evidence frontier is already closed, so every request starts
			// at the answer boundary. The plan gate is the only reason a tool
			// is offered on the first and third requests.
			return agent.ProjectionResult{Report: agent.ProjectionReport{
				AnswerEligible: true,
				Stage:          "answer",
			}}
		},
		Authorize:                 allowRead{},
		MaxTurns:                  8,
		AnswerPlanRequest:         "Call prepare_answer before writing the answer.",
		AnswerPlanEvidenceRequest: "Read the missing evidence.",
		CompletionReviewRequest:   "Runtime completion review: {{reason}}.",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	// The audit is a test seam: the first plan has no source observation, the
	// second follows the README read. The real claim audit is covered by the
	// plan unit tests and the existing answer-review suite.
	loop.SetAnswerPlanReview(func(turns []agent.Turn, plan agent.AnswerPlan) []contract.GroundingIssue {
		if len(plan.Claims) != 1 {
			t.Fatalf("plan claims = %+v, want one", plan.Claims)
		}
		for _, turn := range turns {
			for _, outcome := range turn.Results {
				if outcome.ToolName == "read" && !outcome.Result.IsError {
					return nil
				}
			}
		}
		return []contract.GroundingIssue{{
			Code:        "unsupported_implementation_claim",
			Detail:      "the project status has no source observation",
			Claim:       plan.Claims[0],
			Category:    "implementation",
			Requirement: "source content for the named component",
			Action:      "read the component or narrow the status claim",
		}}
	})
	loop.SetAnswerReview(func([]agent.Turn, string) string { return "" })

	turns, err := loop.Run(context.Background(), []agent.Message{
		agent.UserMessage{Text: "what is this project?"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if requests.Load() != 4 {
		t.Fatalf("provider requests = %d, want plan, read, plan, answer (4)", requests.Load())
	}
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want 4", len(turns))
	}
	if !turns[0].Results[0].Result.IsError {
		t.Fatalf("first plan was accepted without evidence: %+v", turns[0].Results[0].Result)
	}
	if !strings.Contains(turns[0].Results[0].Result.Text, "answer plan refused") {
		t.Fatalf("first plan refusal = %q", turns[0].Results[0].Result.Text)
	}
	if turns[1].Results[0].ToolName != "read" || turns[1].Results[0].Result.IsError {
		t.Fatalf("evidence turn = %+v, want a successful read", turns[1].Results[0])
	}
	if turns[2].Results[0].Result.IsError {
		t.Fatalf("second plan was refused after the read: %+v", turns[2].Results[0].Result)
	}
	if turns[3].Answer != agent.AnswerCommitted {
		t.Fatalf("final disposition = %s, want committed", turns[3].Answer)
	}
	if got := turns[3].Assistant.Text(); got != "The project is a Go coding agent." {
		t.Fatalf("final answer = %q", got)
	}
}

// A refused plan leaves an evidence debt. The next request offers evidence
// tools only; prose without an observation is withheld as narration and the
// debt survives, so the model cannot re-plan the same claims or publish prose
// before reading the subjects the refusal named.
func TestRefusedAnswerPlanKeepsEvidenceSurfaceUntilRead(t *testing.T) {
	script := []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:   "plan-1",
				Name: agent.AnswerPlanToolName,
				Arguments: map[string]any{
					"claims": []any{"The project is a Go coding agent."},
				},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "The project is a Go coding agent."}},
			StopReason: ai.StopEndTurn,
		},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:        "ls-1",
				Name:      "ls",
				Arguments: map[string]any{"path": "."},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:        "read-1",
				Name:      "read",
				Arguments: map[string]any{"path": "README.md"},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:   "plan-2",
				Name: agent.AnswerPlanToolName,
				Arguments: map[string]any{
					"claims": []any{"The project is a Go coding agent."},
				},
			}},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "The project is a Go coding agent."}},
			StopReason: ai.StopEndTurn,
		},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			if index == 1 || index == 2 || index == 3 {
				for _, tool := range req.Tools {
					if tool.Name == agent.AnswerPlanToolName {
						t.Fatalf("request %d offered the plan tool while evidence was owed", index+1)
					}
				}
				if req.ToolChoice.Type != "any" {
					t.Fatalf("request %d tool choice = %q, want any while evidence was owed", index+1, req.ToolChoice.Type)
				}
			}
			if index == 4 {
				found := false
				for _, tool := range req.Tools {
					found = found || tool.Name == agent.AnswerPlanToolName
				}
				if !found {
					t.Fatalf("plan tool did not return after the evidence read")
				}
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools: []agent.Tool{
			evidenceReadTool{},
			evidenceListTool{},
			exectools.NewPrepareAnswerTool(),
		},
		Project: func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{Report: agent.ProjectionReport{
				AnswerEligible: true,
				Stage:          "answer",
			}}
		},
		Authorize:                 allowRead{},
		MaxTurns:                  8,
		AnswerPlanRequest:         "Call prepare_answer before writing the answer.",
		AnswerPlanEvidenceRequest: "Read the missing evidence.",
		CompletionReviewRequest:   "Runtime completion review: {{reason}}.",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	loop.SetAnswerPlanReview(func(turns []agent.Turn, plan agent.AnswerPlan) []contract.GroundingIssue {
		for _, turn := range turns {
			for _, outcome := range turn.Results {
				if outcome.ToolName == "read" && !outcome.Result.IsError {
					return nil
				}
			}
		}
		return []contract.GroundingIssue{{
			Code:        "unsupported_implementation_claim",
			Detail:      "the project status has no source observation",
			Claim:       plan.Claims[0],
			Category:    "implementation",
			Requirement: "source content for the named component",
			Action:      "read the component or narrow the status claim",
		}}
	})
	loop.SetAnswerReview(func([]agent.Turn, string) string { return "" })

	turns, err := loop.Run(context.Background(), []agent.Message{
		agent.UserMessage{Text: "what is this project?"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if requests.Load() != 6 {
		t.Fatalf("provider requests = %d, want plan, prose, ls, read, plan, answer (6)", requests.Load())
	}
	if len(turns) != 6 {
		t.Fatalf("turns = %d, want 6", len(turns))
	}
	if turns[1].Answer != agent.AnswerNarration {
		t.Fatalf("pre-evidence prose disposition = %s, want narration", turns[1].Answer)
	}
	if turns[1].AnswerText() != "" {
		t.Fatalf("pre-evidence prose entered the answer projection: %q", turns[1].AnswerText())
	}
	if turns[2].Results[0].ToolName != "ls" || turns[2].Results[0].Result.IsError {
		t.Fatalf("listing observation = %+v, want a successful ls", turns[2].Results[0])
	}
	if turns[3].Results[0].ToolName != "read" || turns[3].Results[0].Result.IsError {
		t.Fatalf("read observation = %+v, want a successful read", turns[3].Results[0])
	}
	if turns[4].Results[0].Result.IsError {
		t.Fatalf("plan after evidence was refused: %+v", turns[4].Results[0].Result)
	}
	if turns[5].Answer != agent.AnswerCommitted {
		t.Fatalf("final disposition = %s, want committed", turns[5].Answer)
	}
}

// A model that writes prose without the required plan does not publish it. The
// turn is withheld as narration and the next request asks for the plan again;
// no candidate text reaches the host before the plan passes.
func TestAnswerPlanGateWithholdsProseBeforePlan(t *testing.T) {
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "The project is a Go coding agent."}}},
		{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID:   "plan-1",
				Name: agent.AnswerPlanToolName,
				Arguments: map[string]any{
					"claims": []any{"The project is a Go coding agent."},
				},
			}},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "The project is a Go coding agent."}}},
	}
	var requests atomic.Int64
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			index := int(requests.Add(1)) - 1
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			return ai.NewCompletedStream(script[index]), nil
		},
		Tools: []agent.Tool{
			evidenceReadTool{},
			exectools.NewPrepareAnswerTool(),
		},
		Project: func(agent.ProjectionRequest) agent.ProjectionResult {
			return agent.ProjectionResult{Report: agent.ProjectionReport{AnswerEligible: true}}
		},
		Authorize:               allowRead{},
		MaxTurns:                8,
		AnswerPlanRequest:       "Call prepare_answer before writing the answer.",
		CompletionReviewRequest: "Runtime completion review: {{reason}}.",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	loop.SetAnswerPlanReview(func([]agent.Turn, agent.AnswerPlan) []contract.GroundingIssue { return nil })
	loop.SetAnswerReview(func([]agent.Turn, string) string { return "" })

	turns, err := loop.Run(context.Background(), []agent.Message{
		agent.UserMessage{Text: "what is this project?"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want withheld prose, plan, answer", len(turns))
	}
	if turns[0].Answer != agent.AnswerNarration {
		t.Fatalf("pre-plan prose disposition = %s, want narration", turns[0].Answer)
	}
	if turns[0].AnswerText() != "" {
		t.Fatalf("pre-plan prose entered the answer projection: %q", turns[0].AnswerText())
	}
	if turns[2].Answer != agent.AnswerCommitted {
		t.Fatalf("final disposition = %s, want committed", turns[2].Answer)
	}
}
