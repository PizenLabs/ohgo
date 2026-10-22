package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
)

func stopTurn() agent.Turn { return agent.Turn{Assistant: ai.AssistantMessage{}} }

// A failed tool call followed by a clean stop is reviewable, never complete.
// Before the fix it was recorded COMPLETED.
func TestTurnTaskOutcomeRejectsFailedToolCall(t *testing.T) {
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{{
			ToolName: "edit",
			Result:   agent.Result{Text: "no match found", IsError: true},
		}}},
		stopTurn(),
	}
	action, reason := turnTaskOutcome(nil, turns, false)
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
	action, reason := turnTaskOutcome(nil, []agent.Turn{stopTurn()}, true)
	if action != taskActionReview {
		t.Fatalf("action = %v, want review (reason %q)", action, reason)
	}
}

// A verified mutation may complete.
func TestTurnTaskOutcomeAcceptsVerifiedMutation(t *testing.T) {
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
	action, reason := turnTaskOutcome(nil, turns, true)
	if action != taskActionComplete {
		t.Fatalf("action = %v, want complete (reason %q)", action, reason)
	}
}

// Cancellation is a recoverable pause, never completion and never failure.
func TestTurnTaskOutcomePausesOnCancellation(t *testing.T) {
	action, reason := turnTaskOutcome(context.Canceled, []agent.Turn{stopTurn()}, false)
	if action != taskActionPause || reason != "interrupted" {
		t.Fatalf("action/reason = %v/%q, want pause/interrupted", action, reason)
	}
}

// A turn-cap stop is recoverable, not a failure of the work.
func TestTurnTaskOutcomePausesOnTurnLimit(t *testing.T) {
	action, reason := turnTaskOutcome(agent.ErrTurnLimit, []agent.Turn{stopTurn()}, false)
	if action != taskActionPause || reason != "turn limit reached" {
		t.Fatalf("action/reason = %v/%q, want pause/turn limit reached", action, reason)
	}
}

// A real execution error is a failure, not a review.
func TestTurnTaskOutcomeFailsOnExecutionError(t *testing.T) {
	action, reason := turnTaskOutcome(errors.New("provider unavailable"), nil, false)
	if action != taskActionFail || reason != "provider unavailable" {
		t.Fatalf("action/reason = %v/%q, want fail/provider unavailable", action, reason)
	}
}
