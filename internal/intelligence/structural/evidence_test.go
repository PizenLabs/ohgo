package structural

import "testing"

// TestObservedNeverBecomesVerifiedThroughCombination pins the central evidence
// invariant: combining observations cannot manufacture a proof. A model that
// states an implementation is verified must be citing source, not an aggregate.
func TestObservedNeverBecomesVerifiedThroughCombination(t *testing.T) {
	first := ObservedEvidence(KindAdapterFact, EvidenceRef{Kind: KindAdapterFact, Path: "a.go"})
	second := ObservedEvidence(KindInventory, EvidenceRef{Kind: KindInventory, Path: "b"})
	combined := CombineEvidence(first, second)
	if combined.State == StateVerified {
		t.Fatal("combining observations produced VERIFIED evidence")
	}
	if combined.State != StateObserved {
		t.Fatalf("state = %s, want OBSERVED", combined.State)
	}
	if combined.Confidence >= first.Confidence {
		t.Errorf("multi-witness confidence %.2f was not capped below %.2f",
			combined.Confidence, first.Confidence)
	}
}

// TestVerifiedRequiresEveryConjunct pins that one proven fact cannot carry an
// unproven one.
func TestVerifiedRequiresEveryConjunct(t *testing.T) {
	verified := VerifiedEvidence(KindAdapterFact, EvidenceRef{Path: "a.go"})
	observed := ObservedEvidence(KindInventory, EvidenceRef{Path: "b"})
	if got := CombineEvidence(verified, observed).State; got != StateObserved {
		t.Fatalf("verified+observed = %s, want OBSERVED", got)
	}
	if got := CombineEvidence(verified, verified).State; got != StateVerified {
		t.Fatalf("verified+verified = %s, want VERIFIED", got)
	}
}

// TestUnknownInConjunctionStaysUnknown pins that a missing check is not a
// negative fact.
func TestUnknownInConjunctionStaysUnknown(t *testing.T) {
	observed := ObservedEvidence(KindInventory, EvidenceRef{Path: "."})
	unknown := UnknownEvidence("parser unavailable")
	combined := CombineEvidence(observed, unknown)
	if combined.State != StateUnknown {
		t.Fatalf("state = %s, want UNKNOWN", combined.State)
	}
	if combined.Reason != "parser unavailable" {
		t.Errorf("reason = %q", combined.Reason)
	}
}

// TestContainmentDegradesToPartiallyObserved pins the aggregate rule: a
// container with one inferred member is PARTIALLY_OBSERVED, never OBSERVED.
func TestContainmentDegradesToPartiallyObserved(t *testing.T) {
	observed := ObservedEvidence(KindInventory, EvidenceRef{Path: "a"})
	inferred := InferredEvidence("path convention", "b/x.md")
	got := AggregateContainment(observed, inferred)
	if got.State != StatePartiallyObserved {
		t.Fatalf("state = %s, want PARTIALLY_OBSERVED", got.State)
	}
	if got.Confidence > inferred.Confidence {
		t.Errorf("aggregate confidence %.2f exceeds its weakest member %.2f",
			got.Confidence, inferred.Confidence)
	}
	allObserved := AggregateContainment(observed,
		ObservedEvidence(KindInventory, EvidenceRef{Path: "c"}))
	if allObserved.State != StateObserved {
		t.Fatalf("all-observed aggregate = %s, want OBSERVED", allObserved.State)
	}
	if AggregateContainment(observed).State == StateVerified {
		t.Error("a single observation aggregated into VERIFIED")
	}
}

// TestInferredEvidenceNamesItsRule pins that a convention-based claim is
// auditable: the rule is carried, not implied.
func TestInferredEvidenceNamesItsRule(t *testing.T) {
	evidence := InferredEvidence("vendor directory: vendor", "vendor/x/y.go")
	if evidence.State != StateInferred {
		t.Fatalf("state = %s", evidence.State)
	}
	if len(evidence.Refs) != 1 || evidence.Refs[0].Rule != "vendor directory: vendor" {
		t.Fatalf("refs = %+v", evidence.Refs)
	}
	if evidence.Confidence >= confidenceInventory {
		t.Errorf("inferred confidence %.2f is not below an inventory observation", evidence.Confidence)
	}
}
