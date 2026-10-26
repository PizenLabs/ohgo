package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/security"
)

func toolOutcome(name string, result Result) ToolOutcome {
	return ToolOutcome{ToolName: name, Result: result}
}

func cleanStop() Turn { return Turn{Assistant: ai.AssistantMessage{}} }

// answerTurn is a clean stop that carries visible assistant text. Informational
// completion requires an answer, not just a stopped model.
func answerTurn(text string) Turn {
	return Turn{Assistant: ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}}
}

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

func TestEvaluateCompletionRejectsUnexpectedMutation(t *testing.T) {
	// A read-only or execution-only run that changed the repository is not
	// complete even if the model produced a clean final answer. The workspace
	// may have authorized the call; the objective did not.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		answerTurn("done"),
	}
	if verdict := EvaluateCompletion(turns, false); verdict.Complete() {
		t.Fatal("a read-only run that mutated was reported complete")
	}
}

func TestInformationalPathTargetDoesNotRequireVerification(t *testing.T) {
	// Naming a Go file in a question does not make the answer a checked
	// change. Before this rule, "what package is declared in tui/x.go?"
	// required a build or test that the task never asked for.
	contract := contract.IntentContract{
		Objective: "What package is declared in tui/approval.go?",
		Kind:      contract.ObjectiveInformational,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "tui/approval.go"}},
	}
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{
			Text:    "1\tpackage tui",
			Details: map[string]any{"path": "tui/approval.go", "totalLines": 1, "offset": 1, "limit": 2000},
		})}},
		answerTurn("tui"),
	}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: contract})
	if !verdict.Complete() {
		t.Fatalf("informational completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

func TestEvidenceReviewVetoesAnAnswerWithoutEvidence(t *testing.T) {
	// A model can always emit a plausible sentence. The cognitive layer's
	// sufficiency verdict is the runtime's statement that the sentence was
	// produced without the evidence the task required.
	contract := contract.IntentContract{
		Objective: "Explain this project.",
		Kind:      contract.ObjectiveInformational,
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:          []Turn{answerTurn("It is a Go project.")},
		Contract:       contract,
		EvidenceReview: "runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage explore",
	})
	if verdict.Complete() {
		t.Fatal("an answer with an insufficient evidence review was reported complete")
	}
}

func TestBudgetCompletionKeepsProvenEvidenceAuthoritative(t *testing.T) {
	// A resource stop removes the closing model turn, but not the proof. The
	// cognitive review from the pre-verification request must not undo a
	// definite change followed by a successful check.
	contract := contract.IntentContract{
		Objective:           "Change note.txt and verify it.",
		Kind:                contract.ObjectiveMutation,
		BehavioralObjective: true,
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyAny,
		}},
	}
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
	}
	verdict := EvaluateBudgetCompletion(CompletionInput{
		Turns:          turns,
		Contract:       contract,
		EvidenceReview: "runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage verify",
	}, true)
	if !verdict.Complete() {
		t.Fatalf("proven budget completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
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
	completion := EvaluateCompletion(turns, false)
	if completion.Complete() {
		t.Fatal("a failed tool call followed by a clean stop was reported complete")
	}
	if !strings.Contains(completion.Reason, "edit") {
		t.Fatalf("review reason does not name the failing tool: %q", completion.Reason)
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
