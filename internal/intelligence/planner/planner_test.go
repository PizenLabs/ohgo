package planner

import (
	"errors"
	"reflect"
	"testing"
)

func TestProjectPlanAvoidsRuntimeAndParsing(t *testing.T) {
	plan, err := Build(IntentProject, Availability{Structural: true, Retrieval: true, Runtime: true}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	providers := selected(plan)
	if !providers[ProviderStructural] || !providers[ProviderRetrieval] {
		t.Fatalf("project plan = %+v", plan)
	}
	if providers[ProviderRuntime] {
		t.Fatal("runtime was planned for a project overview")
	}
	if plan.Stop != StopConfidenceReached {
		t.Fatalf("stop = %s, want confidence reached", plan.Stop)
	}
}

func TestLocatePlansSemanticOnlyWhenAvailable(t *testing.T) {
	plan, err := Build(IntentLocate, Availability{Structural: true, Retrieval: true}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if selected(plan)[ProviderSemantic] {
		t.Fatal("semantic step planned without an available server")
	}
	if !hasSkip(plan, ProviderSemantic, "unavailable") {
		t.Fatalf("missing semantic skip reason: %+v", plan.Skips)
	}

	plan, err = Build(IntentLocate, Availability{Structural: true, Retrieval: true, Semantic: true}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !selected(plan)[ProviderSemantic] {
		t.Fatalf("semantic step missing with an available server: %+v", plan.Steps)
	}
}

func TestDebugRequiresRuntimeAvailability(t *testing.T) {
	plan, err := Build(IntentDebug, Availability{Structural: true, Retrieval: true, Semantic: true}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if selected(plan)[ProviderRuntime] {
		t.Fatal("runtime planned without an authorized session")
	}
	if !hasSkip(plan, ProviderRuntime, "authorized runtime") {
		t.Fatalf("runtime skip does not explain authorization: %+v", plan.Skips)
	}

	plan, err = Build(IntentDebug, Availability{Structural: true, Runtime: true}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !selected(plan)[ProviderRuntime] {
		t.Fatalf("runtime step missing when available: %+v", plan.Steps)
	}
	for _, step := range plan.Steps {
		if step.Provider == ProviderRuntime && step.Cost.Band != "high" {
			t.Fatalf("runtime cost band = %q, want high", step.Cost.Band)
		}
	}
}

func TestBudgetStopsSelection(t *testing.T) {
	plan, err := Build(IntentArchitecture,
		Availability{Structural: true, Retrieval: true, Semantic: true},
		Budget{MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %+v, want exactly the budget", plan.Steps)
	}
	if plan.Stop != StopBudgetExhausted {
		t.Fatalf("stop = %s, want budget exhausted", plan.Stop)
	}
	if !hasSkip(plan, ProviderRetrieval, "budget") && !hasSkip(plan, ProviderSemantic, "budget") {
		t.Fatalf("skips do not explain the budget: %+v", plan.Skips)
	}
}

func TestNoProviderIsAnError(t *testing.T) {
	if _, err := Build(IntentImpact, Availability{}, Budget{}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("error = %v, want ErrNoProvider", err)
	}
	if _, err := Build(Intent("nonsense"), Availability{Structural: true}, Budget{}); !errors.Is(err, ErrUnknownIntent) {
		t.Fatalf("error = %v, want ErrUnknownIntent", err)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	availability := Availability{Structural: true, Retrieval: true, Semantic: true, Runtime: true}
	first, err := Build(IntentFlow, availability, Budget{MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(IntentFlow, availability, Budget{MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan is not deterministic:\n%+v\n%+v", first, second)
	}
	if first.Confidence > 1 || first.Confidence < 0 {
		t.Fatalf("confidence out of range: %v", first.Confidence)
	}
	for _, step := range first.Steps {
		if step.EstimatedConfidence > first.Confidence+1e-9 {
			t.Fatalf("step confidence exceeds plan confidence: %+v", step)
		}
		if step.Reason == "" {
			t.Fatalf("step %s has no reason", step.Provider)
		}
	}
}

func TestVerifyPlanNeedsStructuralFoundation(t *testing.T) {
	plan, err := Build(IntentVerify, Availability{Semantic: true, Retrieval: true}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stop != StopPartial {
		t.Fatalf("stop = %s, want partial", plan.Stop)
	}
	if !hasSkip(plan, ProviderStructural, "mandatory") {
		t.Fatalf("missing structural skip: %+v", plan.Skips)
	}
}

func selected(plan Plan) map[Provider]bool {
	out := make(map[Provider]bool, len(plan.Steps))
	for _, step := range plan.Steps {
		out[step.Provider] = true
	}
	return out
}

func hasSkip(plan Plan, provider Provider, substring string) bool {
	for _, skip := range plan.Skips {
		if skip.Provider != provider {
			continue
		}
		if substring == "" {
			return true
		}
		if contains(skip.Reason, substring) {
			return true
		}
	}
	return false
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
