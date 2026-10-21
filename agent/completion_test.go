package agent

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/security"
)

func toolOutcome(name string, result Result) ToolOutcome {
	return ToolOutcome{ToolName: name, Result: result}
}

func cleanStop() Turn { return Turn{Assistant: ai.AssistantMessage{}} }

func TestEvaluateCompletionRejectsMutationWithoutVerification(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("edit", Result{Text: "edited", Mutates: true})}},
		cleanStop(),
	}
	verdict := EvaluateCompletion(turns, true)
	if verdict.Complete() {
		t.Fatal("a mutation with no verification was reported complete")
	}
	if verdict.Reason == "" {
		t.Fatal("a NEEDS_REVIEW verdict has no evidence reason")
	}
}

func TestEvaluateCompletionRejectsMissingMutation(t *testing.T) {
	// The model says it is done and the todo list is empty, but the request
	// asked for a change and the runtime observed none.
	verdict := EvaluateCompletion([]Turn{cleanStop()}, true)
	if verdict.Complete() {
		t.Fatal("a mutation objective with no observed change was reported complete")
	}
}

func TestEvaluateCompletionRejectsFailedVerification(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "FAIL", IsError: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a failed verification was reported complete")
	}
}

func TestEvaluateCompletionAcceptsMutationThenVerification(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a verified mutation was not reported complete")
	}
}

func TestEvaluateCompletionRejectsVerificationBeforeMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a verification that preceded the change was accepted")
	}
}

func TestEvaluateCompletionRejectsUnrecoveredToolFailure(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("edit", Result{Text: "no match", IsError: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a failed tool call followed by a clean stop was reported complete")
	}
}

func TestEvaluateCompletionAcceptsRetriedTool(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "missing", IsError: true})}},
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "contents"})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a failure recovered by a successful retry was not reported complete")
	}
}

func TestEvaluateCompletionRejectsCancellation(t *testing.T) {
	turns := []Turn{{Err: context.Canceled}}
	if EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a cancelled run was reported complete")
	}

	// A tool-level cancellation is an error outcome, not a clean stop.
	turns = []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "command cancelled", IsError: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a cancelled tool followed by a clean stop was reported complete")
	}
}

func TestEvaluateCompletionRejectsOutstandingToolCalls(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a run stopped mid-tool-batch was reported complete")
	}
}

func TestEvaluateCompletionAcceptsReadOnlyCleanStop(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "contents"})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a clean read-only run was not reported complete")
	}
}

func TestEvaluateCompletionRequiresVerificationAfterTheLastMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		{Results: []ToolOutcome{toolOutcome("edit", Result{Text: "edited", Mutates: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a change after the last verification was reported complete")
	}
}

func TestEvaluateCompletionRequiresVerificationAfterPotentialMutation(t *testing.T) {
	// An arbitrary shell command in a mutation objective may have changed the
	// repository even when the classifier did not prove it. A later read-only
	// exec is not verification, so the task is reviewable.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ran", Executes: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a potential mutation after the last verification was reported complete")
	}
}

func TestEvaluateCompletionRejectsMutationTaskThatOnlyRanTests(t *testing.T) {
	// Running tests is verification, not a change. A mutation objective with
	// no definite change is still unmet, however clean the test run was.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Executes: true, Verifies: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a mutation objective with no observed change was reported complete")
	}
}

func TestEvaluateCompletionAcceptsReadOnlyExecution(t *testing.T) {
	// A read-only task that runs a process is not forced to verify a change it
	// did not request.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "output", Executes: true})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a clean read-only execution was not reported complete")
	}
}

func TestCompletionEvidenceCarriesDelegatedMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		cleanStop(),
	}
	mutated, verified := CompletionEvidence(turns, true)
	if !mutated || !verified {
		t.Fatalf("evidence = (%v, %v), want (true, true)", mutated, verified)
	}

	turns[1].Results = nil
	mutated, verified = CompletionEvidence(turns, true)
	if !mutated || verified {
		t.Fatalf("unverified evidence = (%v, %v), want (true, false)", mutated, verified)
	}
}

func TestCompletionEvidenceCountsAuthorizedMutation(t *testing.T) {
	// A shell command classified as mutating carries the authorization flag,
	// even when the tool result itself does not set Mutates.
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName:      "bash",
			Result:        Result{Text: "ok"},
			Authorization: &security.Decision{Mutates: true},
		}}},
		cleanStop(),
	}
	mutated, _ := CompletionEvidence(turns, false)
	if !mutated {
		t.Fatal("an authorized mutation was not counted as evidence")
	}
}
