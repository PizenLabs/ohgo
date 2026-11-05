package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func inventoryContract() contract.IntentContract {
	return contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about",
	}
}

// A broad analysis cannot claim "only one Go file" when its own glob listed
// three. The counts are both visible to the user; completing would endorse a
// false statement.
func TestInventoryCountClaimContradictedByObservedGlob(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName: "glob",
			Result: Result{Text: strings.Join([]string{
				"backend/cmd/order-service/main.go",
				"backend/cmd/cart-service/main.go",
				"backend/cmd/product-service/main.go",
			}, "\n")},
		}}},
		answerTurn("The repository is largely scaffold. Only one real Go file exists."),
	}

	got := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: inventoryContract(),
		Root:     "/repo",
		Provenance: EvidenceProvenance{
			RequireAttribution: true,
		},
	})
	if got.Complete() {
		t.Fatal("an exclusive file-count claim contradicted by the run's own glob completed")
	}
	if !strings.Contains(got.Reason, "only 1 Go file") || !strings.Contains(got.Reason, "observed 3") {
		t.Fatalf("reason = %q, want the claimed and observed counts", got.Reason)
	}
}

// A scoped count is checked against that scope, not the whole repository.
func TestInventoryCountClaimWithScopeMatchesObservation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName: "glob",
			Result: Result{Text: strings.Join([]string{
				"backend/cmd/order-service/main.go",
				"backend/cmd/cart-service/main.go",
				"backend/cmd/product-service/main.go",
			}, "\n")},
		}}},
		answerTurn("Only one Go file exists in backend/cmd/order-service."),
	}

	got := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: inventoryContract(),
		Root:     "/repo",
		Provenance: EvidenceProvenance{
			RequireAttribution: true,
		},
	})
	if !got.Complete() {
		t.Fatalf("a scoped count matching the observation = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// The inventory check is part of broad-account truthfulness. A narrow question
// is not forced through it.
func TestInventoryCountClaimIgnoredWithoutBroadAnalysis(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName: "glob",
			Result: Result{Text: strings.Join([]string{
				"a/main.go", "b/main.go", "c/main.go",
			}, "\n")},
		}}},
		answerTurn("Only one Go file exists."),
	}

	got := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: inventoryContract(),
		Root:     "/repo",
		Provenance: EvidenceProvenance{
			RequireAttribution: false,
		},
	})
	if !got.Complete() {
		t.Fatalf("a narrow answer = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// An absolute read and a relative glob of the same file must count once.
func TestInventoryNormalizesAbsoluteAndRelativePaths(t *testing.T) {
	files := observedFileInventory("/repo", []Turn{{Results: []ToolOutcome{
		{ToolName: "read", Args: map[string]any{"path": "/repo/backend/cmd/order-service/main.go"},
			Result: Result{Text: "package main"}},
		{ToolName: "glob", Result: Result{Text: "backend/cmd/order-service/main.go"}},
	}}})
	if len(files) != 1 {
		t.Fatalf("inventory = %v, want one normalized file", files)
	}
}

// A qualified name identifies that basename, not every file with the same
// extension. "Only one main.go" is contradicted by two main.go files, while
// "only one other.go" is not contradicted by unrelated Go files.
func TestInventoryBasenameClaimDoesNotBecomeAnExtensionClaim(t *testing.T) {
	glob := Turn{Results: []ToolOutcome{{
		ToolName: "glob",
		Result: Result{Text: strings.Join([]string{
			"a/main.go", "b/main.go", "c/other.go",
		}, "\n")},
	}}}
	input := CompletionInput{
		Contract:   inventoryContract(),
		Root:       "/repo",
		Provenance: EvidenceProvenance{RequireAttribution: true},
	}

	input.Turns = []Turn{glob, answerTurn("Only one main.go file exists.")}
	if got := EvaluateTaskCompletion(input); got.Complete() {
		t.Fatal("two observed main.go files did not contradict the basename claim")
	}

	input.Turns = []Turn{glob, answerTurn("Only one other.go file exists.")}
	if got := EvaluateTaskCompletion(input); !got.Complete() {
		t.Fatalf("a true basename count = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}
