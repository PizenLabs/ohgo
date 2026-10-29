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
