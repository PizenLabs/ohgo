package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// verifiedMutationTurns is the minimum evidence that can justify completion:
// one definite change followed by a successful verification.
func verifiedMutationTurns() []Turn {
	return []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Executes: true, Verifies: true})}},
		cleanStop(),
	}
}

func mutationInput(turns []Turn) CompletionInput {
	return CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Objective:           "Change the file and verify it.",
			Kind:                contract.ObjectiveMutation,
			BehavioralObjective: true,
			Verification: []contract.VerificationRequirement{{
				Kind: contract.VerifyAny,
			}},
		},
	}
}

// The two shells that report a run must agree on why it stopped. Cancellation
// outranks evidence: verified work is not "completed" after the user stopped the
// process, because that would make the interruption invisible in the verdict.
func TestClassifyRunCancellationOutranksVerifiedEvidence(t *testing.T) {
	verdict, reason := ClassifyRun(context.Canceled, mutationInput(verifiedMutationTurns()), false)
	if verdict != RunCancelled {
		t.Fatalf("verdict = %v, want RunCancelled even with verified evidence", verdict)
	}
	if reason != "interrupted" {
		t.Fatalf("reason = %q, want %q", reason, "interrupted")
	}
}

// A resource stop is not success by itself, but it must not erase evidence that
// was already conclusive before the grant ran out.
func TestClassifyRunResourceStopDependsOnEvidence(t *testing.T) {
	exhausted := &budget.ExhaustedError{Dimension: budget.DimensionToolCalls, Limit: 1, Used: 1}

	verdict, reason := ClassifyRun(exhausted, mutationInput(verifiedMutationTurns()), false)
	if verdict != RunCompleted {
		t.Fatalf("verified work on exhaustion = (%v, %q), want RunCompleted", verdict, reason)
	}

	unverified := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
	}
	verdict, reason = ClassifyRun(exhausted, mutationInput(unverified), false)
	if verdict != RunPaused {
		t.Fatalf("unverified exhaustion = (%v, %q), want RunPaused", verdict, reason)
	}
	if !strings.Contains(reason, "tool_calls") && !strings.Contains(reason, "budget") {
		t.Fatalf("reason = %q, want the exhausted dimension or the generic budget reason", reason)
	}
}

// A deadline the loop reports as a nil error is still authoritative: it must
// reach the same resource rule as context.DeadlineExceeded.
func TestClassifyRunSilentDeadlineIsAResourceStop(t *testing.T) {
	verdict, reason := ClassifyRun(nil, mutationInput(verifiedMutationTurns()), true)
	if verdict != RunCompleted {
		t.Fatalf("silent deadline over verified work = (%v, %q), want RunCompleted", verdict, reason)
	}

	verdict, reason = ClassifyRun(nil, mutationInput(nil), true)
	if verdict != RunPaused || reason != "deadline reached" {
		t.Fatalf("silent deadline without evidence = (%v, %q), want RunPaused/\"deadline reached\"", verdict, reason)
	}
}

// A turn limit is a pause, never a failure: the task is resumable.
func TestClassifyRunTurnLimitPauses(t *testing.T) {
	verdict, reason := ClassifyRun(ErrTurnLimit, mutationInput(nil), false)
	if verdict != RunPaused || reason != "turn limit reached" {
		t.Fatalf("= (%v, %q), want RunPaused/\"turn limit reached\"", verdict, reason)
	}
}

// A real error is a failure and keeps its own message, so the operator sees the
// provider or execution cause rather than a generic verdict.
func TestClassifyRunKeepsTheFailureCause(t *testing.T) {
	cause := errors.New("provider unavailable")
	verdict, reason := ClassifyRun(cause, mutationInput(nil), false)
	if verdict != RunFailed {
		t.Fatalf("verdict = %v, want RunFailed", verdict)
	}
	if reason != cause.Error() {
		t.Fatalf("reason = %q, want %q", reason, cause)
	}
}

// Absent a stop, the evidence decides, and an evidence gap is review — not
// failure and not completion.
func TestClassifyRunEvidenceDecidesWithoutAStop(t *testing.T) {
	verdict, reason := ClassifyRun(nil, mutationInput(verifiedMutationTurns()), false)
	if verdict != RunCompleted || reason != "" {
		t.Fatalf("verified run = (%v, %q), want RunCompleted with no reason", verdict, reason)
	}

	verdict, reason = ClassifyRun(nil, mutationInput(nil), false)
	if verdict != RunNeedsReview {
		t.Fatalf("unproven run = %v, want RunNeedsReview", verdict)
	}
	if reason == "" {
		t.Fatal("a review verdict must carry the evidence reason")
	}
}
