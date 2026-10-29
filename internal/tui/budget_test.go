package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func budgetExhausted(dimension budget.Dimension) error {
	return &budget.ExhaustedError{Dimension: dimension, Limit: 1, Used: 1}
}

// Budget exhaustion after independent verification is completion: the stop is
// a resource event, not a semantic verdict. This is Case B.
func TestTurnTaskOutcomeCompletesAfterVerifiedWorkWhenBudgetEnds(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "write",
			Result:   agent.Result{Text: "wrote", Mutates: true},
		}}},
		{Results: []agent.ToolOutcome{{
			ToolName: "bash",
			Result: agent.Result{
				Text:     "ok",
				Verifies: true,
				Verification: &agent.VerificationEvidence{
					Check:  contract.CheckTest,
					Scopes: []agent.VerificationScope{{All: true}},
				},
			},
		}}},
		stopTurn(),
	}
	action, reason := turnTaskOutcome(budgetExhausted(budget.DimensionModelCalls), turns,
		workspace.BuildIntentContract("Fix the failing tests."), nil, nil)
	if action != taskActionComplete {
		t.Fatalf("action = %v, want complete (reason %q)", action, reason)
	}
}

// Budget exhaustion before evidence is a resumable resource stop, never a
// completion. This is Case A.
func TestTurnTaskOutcomePausesOnExhaustionWithoutEvidence(t *testing.T) {
	action, reason := turnTaskOutcome(budgetExhausted(budget.DimensionToolCalls), []agent.Turn{stopTurn()},
		workspace.BuildIntentContract("Update note.txt."), nil, nil)
	if action != taskActionPause {
		t.Fatalf("action = %v, want pause (reason %q)", action, reason)
	}
	if !strings.Contains(reason, string(budget.DimensionToolCalls)) {
		t.Fatalf("reason = %q, want the exhausted dimension", reason)
	}
}

// A model that claims success after the grant ran out does not override the
// missing evidence. This is Case E.
func TestTurnTaskOutcomeDoesNotTrustModelSuccessAfterExhaustion(t *testing.T) {
	turns := []agent.Turn{{
		Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done, all fixed"}}},
	}}
	action, reason := turnTaskOutcome(budgetExhausted(budget.DimensionToolCalls), turns,
		workspace.BuildIntentContract("Update note.txt."), nil, nil)
	if action == taskActionComplete {
		t.Fatal("the model's claim completed the task after budget exhaustion")
	}
	if action != taskActionPause {
		t.Fatalf("action = %v, want pause (reason %q)", action, reason)
	}
}

// A child that stops on its budget is returned as a failed tool outcome. The
// parent evaluator must review that turn, not inherit the child's clean words.
// This is Case D.
func TestTurnTaskOutcomeRejectsChildBudgetFailureAsSuccess(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "task",
			Result: agent.Result{
				Text:    "child answer\ntask: subagent: budget: exhausted: turns (1/1 used)",
				IsError: true,
			},
		}}},
		stopTurn(),
	}
	action, reason := turnTaskOutcome(nil, turns, workspace.BuildIntentContract("Explain this."), nil, nil)
	if action != taskActionReview {
		t.Fatalf("action = %v, want review (reason %q)", action, reason)
	}
}
