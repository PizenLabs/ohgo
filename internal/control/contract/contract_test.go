package contract

import "testing"

// OperationFor is the only bridge between the target scope list and the
// per-target direction. A missing mapping must be ChangeAny, never a guessed
// delete/create.
func TestOperationForUsesExpectedChangeOrDefaultsToAny(t *testing.T) {
	target := Target{Kind: TargetPath, Value: "note.txt"}
	contract := IntentContract{
		Targets: []Target{target},
		ExpectedChanges: []ExpectedChange{{
			Target:    target,
			Operation: ChangeModify,
		}},
	}
	if got := contract.OperationFor(target); got != ChangeModify {
		t.Errorf("OperationFor(%+v) = %s, want modify", target, got)
	}
	if got := contract.OperationFor(Target{Kind: TargetPath, Value: "other.txt"}); got != ChangeAny {
		t.Errorf("OperationFor(unknown) = %s, want any", got)
	}
}

// An unset check field is the permissive generic check; a typed requirement is
// never silently widened or narrowed by a zero value.
func TestVerificationRequirementEffectiveCheck(t *testing.T) {
	if got := (VerificationRequirement{}).EffectiveCheck(); got != CheckAny {
		t.Fatalf("zero check = %s, want any", got)
	}
	if got := (VerificationRequirement{Check: CheckTest}).EffectiveCheck(); got != CheckTest {
		t.Fatalf("test check = %s, want test", got)
	}
}

// A whole-change prohibition is the same limit whether it is written as a
// contraction or in full. A prohibition on one named object is not a
// no-repository-change request and must stay available to a mutation contract.
func TestNoMutationRequestedRecognizesWholeChangeLimits(t *testing.T) {
	limited := []string{
		"Actually don't change anything. Explain the current architecture.",
		"Do not change anything; just answer.",
		"Do not modify anything. Describe the parser.",
		"Leave the code alone and describe it.",
		"Without modifying anything, explain the parser.",
	}
	for _, text := range limited {
		if !NoMutationRequested(text) {
			t.Errorf("NoMutationRequested(%q) = false, want true", text)
		}
	}
	scoped := []string{
		"Do not change the API; add a new endpoint.",
		"Do not modify styles.css; update script.js.",
		"Don't change anything else; only the palette is in scope.",
		"Fix it without changing the tests.",
		"",
	}
	for _, text := range scoped {
		if NoMutationRequested(text) {
			t.Errorf("NoMutationRequested(%q) = true, want a scoped instruction", text)
		}
	}
}
