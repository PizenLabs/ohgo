package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func mutationContracts() contract.IntentContract {
	return contract.IntentContract{
		Kind:         contract.ObjectiveMutation,
		Objective:    "Add the project cards.",
		Ambiguous:    true,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
}

func mutationTurn() Turn {
	return Turn{Results: []ToolOutcome{toolOutcome("edit", Result{
		Text: "edited", Mutates: true, AffectedPaths: []string{"index.html"},
	})}}
}

func checkNotRunTurn() Turn {
	return Turn{Results: []ToolOutcome{{
		ToolName: "check",
		Args:     map[string]any{"mode": "run"},
		Result: Result{
			Text:    "checks: NOT_RUN (no supported command was discovered)",
			Details: map[string]any{"status": "NOT_RUN", "mode": "run"},
			Useless: true,
		},
	}}}
}

func checkFailedTurn() Turn {
	return Turn{Results: []ToolOutcome{{
		ToolName: "check",
		Result: Result{
			Text:         "checks: FAIL",
			IsError:      true,
			FailureClass: ToolFailureVerification,
			Details:      map[string]any{"status": "FAIL", "mode": "run"},
		},
	}}}
}

// An objective whose observed change is followed only by the news that the
// workspace has no check command is complete with a stated limitation. It is
// not a review, because no later turn can produce the missing observation.
func TestTaskCompletionReportsLimitedVerification(t *testing.T) {
	turns := []Turn{mutationTurn(), checkNotRunTurn(), answerTurn("Added the project cards.")}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: mutationContracts()})
	if verdict.State != CompletionCompletedLimited {
		t.Fatalf("state = %q (%s), want %q", verdict.State, verdict.Reason, CompletionCompletedLimited)
	}
	if !verdict.Complete() {
		t.Fatal("a limited verification was not treated as a terminal completion")
	}
	if verdict.Limitation == "" {
		t.Fatal("the completion carried no limitation")
	}

	runVerdict, reason, _ := ClassifyRun(nil, CompletionInput{Turns: turns, Contract: mutationContracts()}, false)
	if runVerdict != RunCompletedLimited {
		t.Fatalf("run verdict = %s (%s), want %s", runVerdict, reason, RunCompletedLimited)
	}
	if reason == "" {
		t.Fatal("the run verdict dropped the limitation reason")
	}
}

// The limitation must be evidence, not a host preference: the same run without
// a check that established unavailability is still a review.
func TestTaskCompletionWithoutCheckStaysReview(t *testing.T) {
	turns := []Turn{mutationTurn(), answerTurn("Added the project cards.")}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: mutationContracts()})
	if verdict.Complete() {
		t.Fatalf("an unchecked change was reported complete: %+v", verdict)
	}
	if verdict.State != CompletionNeedsReview {
		t.Fatalf("state = %q, want review", verdict.State)
	}
}

// A failing check is an observed failure, not an unavailable one. It must not
// be reported as a limitation.
func TestTaskCompletionDoesNotTurnFailureIntoLimitation(t *testing.T) {
	turns := []Turn{mutationTurn(), checkFailedTurn(), answerTurn("Added the cards.")}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: mutationContracts()})
	if verdict.Complete() {
		t.Fatalf("a failed verification was reported complete: %+v", verdict)
	}
	if verdict.State != CompletionNeedsReview {
		t.Fatalf("state = %q, want review", verdict.State)
	}
}

// A symbol the run looked for and established absent stops being a target
// requirement at completion too. Otherwise the projection and the evaluator
// disagree and the task loops between them.
func TestTaskCompletionIgnoresTargetTheRepositoryDoesNotContain(t *testing.T) {
	intent := mutationContracts()
	intent.Targets = []contract.Target{{Kind: contract.TargetSymbol, Value: "OpenCode"}}
	miss := Turn{Results: []ToolOutcome{{
		ToolName: "symbols",
		Args:     map[string]any{"symbol": "OpenCode"},
		Result: Result{
			Text:    "status: not_found\nsymbol: OpenCode",
			Details: map[string]any{"status": "not_found", "symbol": "OpenCode"},
			Useless: true,
		},
	}}}

	unresolvedTurns := []Turn{miss, mutationTurn(), checkNotRunTurn(), answerTurn("Added the cards.")}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: unresolvedTurns, Contract: intent})
	if verdict.State != CompletionCompletedLimited {
		t.Fatalf("state = %q (%s), want limited completion",
			verdict.State, firstNonEmptyString(verdict.Reason, verdict.Limitation))
	}

	// A target named but never looked for is still unresolved evidence, not a
	// free pass.
	notLookedUp := []Turn{mutationTurn(), checkNotRunTurn(), answerTurn("Added the cards.")}
	if verdict := EvaluateTaskCompletion(CompletionInput{Turns: notLookedUp, Contract: intent}); verdict.Complete() {
		t.Fatalf("an unlooked-up target was treated as absent: %+v", verdict)
	}
}

// The host may supply the projection's own verdict, which must survive a
// resumed task whose failing lookups happened in an earlier run.
func TestTaskCompletionAcceptsHostReportedLimitation(t *testing.T) {
	intent := mutationContracts()
	intent.Targets = []contract.Target{{Kind: contract.TargetSymbol, Value: "OpenCode"}}
	turns := []Turn{mutationTurn(), checkNotRunTurn(), answerTurn("Added the cards.")}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:                  turns,
		Contract:               intent,
		UnresolvedTargets:      []string{"OpenCode"},
		VerificationLimitation: "no supported verification command was discovered for this workspace",
	})
	if verdict.State != CompletionCompletedLimited {
		t.Fatalf("state = %q (%s), want limited completion",
			verdict.State, firstNonEmptyString(verdict.Reason, verdict.Limitation))
	}
	if !strings.Contains(verdict.Limitation, "no supported verification") {
		t.Fatalf("limitation = %q", verdict.Limitation)
	}

	// The live producer reports the requirement with its reason, not a bare
	// name. A capability-limited run has no miss turn from which to derive the
	// bare name, so the evaluator must accept the shape the projection emits.
	projected := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		UnresolvedTargets: []string{
			`target_coordinates:OpenCode: "OpenCode" cannot be looked up: no available structural capability supports this repository's source languages`,
		},
		VerificationLimitation: "no supported verification command was discovered for this workspace",
	})
	if projected.State != CompletionCompletedLimited {
		t.Fatalf("requirement-shaped unresolved target: state = %q (%s), want limited completion",
			projected.State, firstNonEmptyString(projected.Reason, projected.Limitation))
	}
}
