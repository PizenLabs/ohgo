package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func conditionalMutationContract() contract.IntentContract {
	return contract.IntentContract{
		Objective:           "Investigate the checkout flow; if the issue is confirmed, fix it.",
		Kind:                contract.ObjectiveMutation,
		ConditionalMutation: true,
		Targets:             []contract.Target{{Kind: contract.TargetPath, Value: "order/checkout"}},
		Verification:        []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
}

func absentCheckoutSnapshot(root string) *TargetSnapshot {
	return &TargetSnapshot{
		Root: root,
		Entries: []TargetSnapshotEntry{{
			Target:    contract.Target{Kind: contract.TargetPath, Value: "order/checkout"},
			Operation: contract.ChangeCreate,
			Resolved:  true,
			Files:     []FileState{{Path: "order/checkout", Exists: false}},
		}},
	}
}

// The investigation phase of a conditional mutation completes on an
// evidence-backed answer. It must not be reported as a missing change.
func TestConditionalMutationCompletesInvestigationWithoutAChange(t *testing.T) {
	root := t.TempDir()
	contract := conditionalMutationContract()
	turns := []Turn{answerTurn("The checkout flow is mocked; no backend order path exists in this snapshot.")}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: turns, Contract: contract, Root: root, Snapshot: absentCheckoutSnapshot(root),
	})
	if !verdict.Complete() {
		t.Fatalf("investigation phase = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
	if strings.Contains(verdict.Reason, "no change was observed") {
		t.Fatalf("the phase was judged as a failed change: %s", verdict.Reason)
	}
}

// Once the branch is entered, the normal mutation contract applies.
func TestConditionalMutationRequiresVerificationAfterTheChange(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "order"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "order", "checkout"), []byte("implemented\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contract := conditionalMutationContract()
	snapshot := absentCheckoutSnapshot(root)
	write := toolOutcome("write", Result{
		Text: "wrote", Mutates: true, Details: map[string]any{"path": "order/checkout"},
	})

	changedOnly := []Turn{
		{Results: []ToolOutcome{write}},
		answerTurn("implemented the fix"),
	}
	if verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: changedOnly, Contract: contract, Root: root, Snapshot: snapshot,
	}); verdict.Complete() {
		t.Fatal("a conditional mutation with no verification was reported complete")
	}

	verified := []Turn{
		{Results: []ToolOutcome{write}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		answerTurn("implemented and verified the fix"),
	}
	if verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: verified, Contract: contract, Root: root, Snapshot: snapshot,
	}); !verdict.Complete() {
		t.Fatalf("verified conditional mutation = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// An unconditional mutation keeps the strict evidence rule.
func TestUnconditionalMutationStillRequiresAChange(t *testing.T) {
	contract := conditionalMutationContract()
	contract.ConditionalMutation = false
	turns := []Turn{answerTurn("I think the checkout flow is probably fine.")}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: contract})
	if verdict.Complete() {
		t.Fatal("an unconditional mutation completed without a change")
	}
	if !strings.Contains(verdict.Reason, "no change was observed") {
		t.Fatalf("reason = %q, want the missing-change boundary", verdict.Reason)
	}
}

// Suppressing the change requirement is not a general loosening: a deferred
// plan is still not a phase result.
func TestConditionalMutationRejectsADeferredPlan(t *testing.T) {
	contract := conditionalMutationContract()
	turns := []Turn{answerTurn("Let me check the checkout implementation and report back.")}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: contract})
	if verdict.Complete() {
		t.Fatal("a deferred plan completed the investigation phase")
	}
	if !strings.Contains(verdict.Reason, "only announces a further action") {
		t.Fatalf("reason = %q, want the deferred-plan boundary", verdict.Reason)
	}
}

// A pure investigation objective is not conditional and does not need a
// change at all.
func TestPureInvestigationCompletesWithoutMutation(t *testing.T) {
	contract := contract.IntentContract{
		Objective: "Investigate the checkout flow.",
		Kind:      contract.ObjectiveInformational,
	}
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "checkout.go"})}},
		answerTurn("Checkout is implemented in checkout.go."),
	}
	if verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: contract}); !verdict.Complete() {
		t.Fatalf("pure investigation = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}
