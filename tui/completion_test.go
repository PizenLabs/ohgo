package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/workspace"
)

func stopTurn() agent.Turn { return agent.Turn{Assistant: ai.AssistantMessage{}} }

func TestTurnTaskOutcomeHonorsEvidenceReview(t *testing.T) {
	turns := []agent.Turn{{Assistant: ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "It is a Go project."}},
		StopReason: ai.StopEndTurn,
	}}}
	action, reason := turnTaskOutcomeWithReview(
		nil, turns, workspace.BuildIntentContract("Explain this project."), nil, nil,
		"runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage explore",
	)
	if action != taskActionReview {
		t.Fatalf("action = %v, want review (reason %q)", action, reason)
	}
	if reason == "" {
		t.Fatal("review has no reason")
	}
}

// A failed tool call followed by a clean stop is reviewable, never complete.
func TestTurnTaskOutcomeRejectsFailedToolCall(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "edit",
			Result:   agent.Result{Text: "no match found", IsError: true},
		}}},
		stopTurn(),
	}
	action, reason := turnTaskOutcome(nil, turns, workspace.BuildIntentContract("Update note.txt."), nil, nil)
	if action != taskActionReview {
		t.Fatalf("action = %v, want review (reason %q)", action, reason)
	}
	if reason == "" {
		t.Fatal("review has no evidence reason")
	}
}

// A mutation request that produced no observed change is not complete, even
// when the model says it is done and the todo list is empty.
func TestTurnTaskOutcomeRejectsUnobservedMutation(t *testing.T) {
	action, reason := turnTaskOutcome(nil, []agent.Turn{stopTurn()}, workspace.BuildIntentContract("Update note.txt."), nil, nil)
	if action != taskActionReview {
		t.Fatalf("action = %v, want review (reason %q)", action, reason)
	}
}

// A verified change completes only when the contract's evidence is satisfied.
// This case is bounded by the behavioural oracle, not by a target path.
func TestTurnTaskOutcomeAcceptsVerifiedMutation(t *testing.T) {
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
	action, reason := turnTaskOutcome(nil, turns, workspace.BuildIntentContract("Fix the failing tests."), nil, nil)
	if action != taskActionComplete {
		t.Fatalf("action = %v, want complete (reason %q)", action, reason)
	}
}

// An unbounded mutation objective with a successful test run is still not
// enough evidence that the requested change happened.
func TestTurnTaskOutcomeRejectsAmbiguousMutation(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "write",
			Result:   agent.Result{Text: "wrote", Mutates: true},
		}}},
		{Results: []agent.ToolOutcome{{
			ToolName: "bash",
			Result:   agent.Result{Text: "ok", Verifies: true},
		}}},
		stopTurn(),
	}
	action, reason := turnTaskOutcome(nil, turns, workspace.BuildIntentContract("Update the code to be more robust."), nil, nil)
	if action != taskActionReview {
		t.Fatalf("action = %v, want review (reason %q)", action, reason)
	}
}

// Cancellation is a recoverable pause, never completion and never failure.
func TestTurnTaskOutcomePausesOnCancellation(t *testing.T) {
	action, reason := turnTaskOutcome(context.Canceled, []agent.Turn{stopTurn()}, workspace.BuildIntentContract("Explain this."), nil, nil)
	if action != taskActionPause || reason != "interrupted" {
		t.Fatalf("action/reason = %v/%q, want pause/interrupted", action, reason)
	}
}

// A turn-cap stop is recoverable, not a failure of the work.
func TestTurnTaskOutcomePausesOnTurnLimit(t *testing.T) {
	action, reason := turnTaskOutcome(agent.ErrTurnLimit, []agent.Turn{stopTurn()}, workspace.BuildIntentContract("Explain this."), nil, nil)
	if action != taskActionPause || reason != "turn limit reached" {
		t.Fatalf("action/reason = %v/%q, want pause/turn limit reached", action, reason)
	}
}

// A real execution error is a failure, not a review.
func TestTurnTaskOutcomeFailsOnExecutionError(t *testing.T) {
	action, reason := turnTaskOutcome(errors.New("provider unavailable"), nil, workspace.BuildIntentContract("Explain this."), nil, nil)
	if action != taskActionFail || reason != "provider unavailable" {
		t.Fatalf("action/reason = %v/%q, want fail/provider unavailable", action, reason)
	}
}
