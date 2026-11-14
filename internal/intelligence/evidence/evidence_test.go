package evidence

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func record(source Source, subject, relation, object string, state State) Record {
	return Record{
		Kind: KindRelationship, Provider: ProviderStructural, Source: source,
		Subject: subject, Relation: relation, Object: object,
		State: state, Confidence: 0.9, SnapshotID: "snap-1",
		ObservedAt: time.Unix(0, 0).UTC(),
	}
}

func TestStateOrderingAndWeakest(t *testing.T) {
	order := []State{Unknown, Inferred, PartiallyObserved, Observed, Verified}
	for index := 1; index < len(order); index++ {
		if order[index].Rank() <= order[index-1].Rank() {
			t.Fatalf("%s did not outrank %s", order[index], order[index-1])
		}
	}
	if got := Weakest(Observed, Inferred, Verified); got != Inferred {
		t.Fatalf("Weakest = %s, want INFERRED", got)
	}
	if got := Weakest(); got != Unknown {
		t.Fatalf("Weakest() = %s, want UNKNOWN", got)
	}
}

func TestEngineDeduplicatesIdenticalObservationsOnly(t *testing.T) {
	engine := New()
	first, err := engine.Add(record(SourceAST, "Checkout", "calls", "Charge", Observed))
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Add(record(SourceAST, "Checkout", "calls", "Charge", Observed))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || engine.Len() != 1 {
		t.Fatalf("identical observations were not deduplicated: %s %s len=%d", first.ID, second.ID, engine.Len())
	}
	if _, err := engine.Add(record(SourceLSP, "Checkout", "calls", "Charge", Observed)); err != nil {
		t.Fatal(err)
	}
	if engine.Len() != 2 {
		t.Fatalf("different sources collapsed into one record: len=%d", engine.Len())
	}
	if first.ID == "" {
		t.Fatal("identity was not assigned")
	}
}

func TestVerifiedRequiresExplicitProof(t *testing.T) {
	engine := New()
	claimed := record(SourceAST, "Checkout", "calls", "Charge", Verified)
	if _, err := engine.Add(claimed); !errors.Is(err, ErrUnverified) {
		t.Fatalf("hand-built VERIFIED error = %v, want ErrUnverified", err)
	}
	proven := Prove(checkedRecord())
	stored, err := engine.Add(proven)
	if err != nil {
		t.Fatalf("proven record rejected: %v", err)
	}
	if stored.State != Verified {
		t.Fatalf("stored state = %s", stored.State)
	}
}

func checkedRecord() Record {
	value := record(SourceTest, "Checkout", "passes", "TestCheckout", Observed)
	value.Kind = KindObservation
	return value
}

func TestCorrelationPreservesAgreementAndContradiction(t *testing.T) {
	engine := New()
	add := func(record Record) {
		if _, err := engine.Add(record); err != nil {
			t.Fatal(err)
		}
	}
	add(record(SourceAST, "CheckoutService", "calls", "PaymentService.Charge", Observed))
	add(record(SourceLSP, "CheckoutService", "calls", "PaymentService.Charge", Observed))
	runtimeObservation := record(SourceDAP, "CheckoutService", "calls", "PaymentService.Charge", Observed)
	runtimeObservation.Provider = ProviderRuntime
	runtimeObservation.Kind = KindRuntime
	add(runtimeObservation)

	correlation := engine.Correlate()
	if len(correlation.Claims) != 1 {
		t.Fatalf("claims = %+v", correlation.Claims)
	}
	claim := correlation.Claims[0]
	if len(claim.Records) != 3 || len(claim.Sources) != 3 {
		t.Fatalf("agreement was flattened: %+v", claim)
	}
	if claim.State != Observed {
		t.Fatalf("claim state = %s", claim.State)
	}
	if len(correlation.Contradictions) != 0 {
		t.Fatalf("agreement reported as contradiction: %+v", correlation.Contradictions)
	}

	conflict := record(SourceAST, "CheckoutService", "calls", "LegacyPayment.Charge", Observed)
	add(conflict)
	correlation = engine.Correlate()
	if len(correlation.Contradictions) != 1 {
		t.Fatalf("contradictions = %+v", correlation.Contradictions)
	}
	contradiction := correlation.Contradictions[0]
	if len(contradiction.Records) != 4 || len(contradiction.Objects) != 2 {
		t.Fatalf("contradiction dropped evidence: %+v", contradiction)
	}
	if contradiction.Note == "" {
		t.Fatal("contradiction has no interpretation guidance")
	}
	for _, claim := range correlation.Claims {
		if claim.Object == "PaymentService.Charge" && len(claim.Records) != 3 {
			t.Fatalf("claim lost its records: %+v", claim)
		}
	}
}

func TestClaimStateNeverExceedsWeakestRecord(t *testing.T) {
	engine := New()
	if _, err := engine.Add(record(SourceAST, "A", "imports", "B", Observed)); err != nil {
		t.Fatal(err)
	}
	inferred := record(SourceGraph, "A", "imports", "B", Inferred)
	inferred.Provider = ProviderStructural
	if _, err := engine.Add(inferred); err != nil {
		t.Fatal(err)
	}
	claims := engine.Correlate().Claims
	if len(claims) != 1 || claims[0].State != Inferred {
		t.Fatalf("claims = %+v, want the weakest state", claims)
	}
}

func TestAddValidatesAndBounds(t *testing.T) {
	engine := New()
	if _, err := engine.Add(Record{Provider: ProviderStructural}); !errors.Is(err, ErrMissingSubject) {
		t.Fatalf("missing subject error = %v", err)
	}
	if _, err := engine.Add(Record{Subject: "x"}); !errors.Is(err, ErrMissingProvider) {
		t.Fatalf("missing provider error = %v", err)
	}
	value := record(SourceBM25, "x", "retrieved_for", "query", Observed)
	value.Confidence = 2
	refs := make([]Ref, 40)
	for index := range refs {
		refs[index] = Ref{Path: "a.go", Line: index + 1}
	}
	value.Refs = refs
	stored, err := engine.Add(value)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Confidence != 1 || len(stored.Refs) != 32 {
		t.Fatalf("bounds not applied: confidence=%v refs=%d", stored.Confidence, len(stored.Refs))
	}
	value = record(SourceBM25, "x", "retrieved_for", "nan", Observed)
	value.Confidence = math.NaN()
	stored, err = engine.Add(value)
	if err != nil || stored.Confidence != 0 {
		t.Fatalf("NaN confidence = %v, %v", stored.Confidence, err)
	}
}

func TestCorrelationOrderingIsDeterministic(t *testing.T) {
	build := func() Correlation {
		engine := New()
		for _, value := range []Record{
			record(SourceLSP, "Zed", "calls", "Two", Observed),
			record(SourceAST, "Alpha", "calls", "One", Observed),
			record(SourceAST, "Alpha", "calls", "Other", Observed),
		} {
			if _, err := engine.Add(value); err != nil {
				t.Fatal(err)
			}
		}
		return engine.Correlate()
	}
	first, second := build(), build()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("correlation is not deterministic:\n%+v\n%+v", first, second)
	}
	if first.Contradictions[0].Subject != "Alpha" || first.Contradictions[0].Objects[0] != "One" {
		t.Fatalf("contradiction order = %+v", first.Contradictions[0])
	}
}
