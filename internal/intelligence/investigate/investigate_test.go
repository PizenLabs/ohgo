package investigate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// fakeProvider is a scripted capability. It records every call so the test can
// prove the orchestrator executed the plan, not a fixed pipeline.
type fakeProvider struct {
	name      planner.Provider
	available bool
	obs       Observation
	err       error
	calls     []planner.Step
}

func (f *fakeProvider) Name() planner.Provider { return f.name }

func (f *fakeProvider) Available(context.Context) bool { return f.available }

func (f *fakeProvider) Observe(_ context.Context, _ Request, step planner.Step, _ *Meter) (Observation, error) {
	f.calls = append(f.calls, step)
	if f.err != nil {
		return Observation{}, f.err
	}
	return f.obs, nil
}

func record(provider evidence.Provider, subject, relation, object string) evidence.Record {
	return evidence.Record{
		Kind: evidence.KindObservation, Provider: provider, Source: evidence.SourceAST,
		Subject: subject, Relation: relation, Object: object,
		State: evidence.Observed, Confidence: 0.8, Freshness: evidence.FreshnessCurrent,
	}
}

// TestLocateRunsCheapestSufficientPath proves the live loop: retrieval runs,
// structural optionally follows, DAP is never selected for a locate question,
// and the planner's confidence estimate does not replace observed evidence.
func TestLocateRunsCheapestSufficientPath(t *testing.T) {
	retrievalProvider := &fakeProvider{
		name: planner.ProviderRetrieval, available: true,
		obs: Observation{
			State: evidence.Observed, Confidence: 0.55, Coverage: 0.5,
			Summary: "2 hits",
			Records: []evidence.Record{record(evidence.ProviderRetrieval, "file:auth.go", "ranked_for", "auth")},
			Facts:   []Fact{{Provider: "retrieval", Subject: "auth.go", Detail: "score 2.1"}},
		},
	}
	structuralProvider := &fakeProvider{
		name: planner.ProviderStructural, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.2, Coverage: 0.3,
			Records: []evidence.Record{record(evidence.ProviderStructural, "file:auth.go", "contains", "Authenticator")}},
	}
	semanticProvider := &fakeProvider{name: planner.ProviderSemantic, available: true}
	runtimeProvider := &fakeProvider{name: planner.ProviderRuntime, available: false}

	engine := New(retrievalProvider, structuralProvider, semanticProvider, runtimeProvider)
	result, err := engine.Run(context.Background(), Request{
		Question: "where is authentication implemented?", Intent: planner.IntentLocate,
		Budget: Budget{MaxSteps: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(retrievalProvider.calls) != 1 {
		t.Fatalf("retrieval calls = %d, want 1", len(retrievalProvider.calls))
	}
	if len(runtimeProvider.calls) != 0 {
		t.Fatalf("runtime ran for a locate question: %d calls", len(runtimeProvider.calls))
	}
	if result.Status != StatusSufficient && result.Status != StatusPartial {
		t.Fatalf("status = %s", result.Status)
	}
	foundRuntimeSkip := false
	for _, skip := range result.Skips {
		if skip.Provider == planner.ProviderRuntime {
			foundRuntimeSkip = true
		}
	}
	if !foundRuntimeSkip {
		t.Fatalf("runtime skip was not recorded: %+v", result.Skips)
	}
	if result.Records == 0 || result.Confidence <= 0 {
		t.Fatalf("shared evidence was not recorded: records=%d confidence=%.2f", result.Records, result.Confidence)
	}
	if len(result.Steps) == 0 || result.Steps[0].Provider != planner.ProviderRetrieval {
		t.Fatalf("first executed step = %+v, want retrieval", result.Steps)
	}
}

// TestObservedEvidenceStopsExpansion proves re-planning uses observed
// confidence: a high-confidence first step ends the run before optional work.
func TestObservedEvidenceStopsExpansion(t *testing.T) {
	retrievalProvider := &fakeProvider{
		name: planner.ProviderRetrieval, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.99, Coverage: 0.9,
			Records: []evidence.Record{record(evidence.ProviderRetrieval, "file:a.go", "ranked_for", "q")}},
	}
	structuralProvider := &fakeProvider{name: planner.ProviderStructural, available: true}
	engine := New(retrievalProvider, structuralProvider)
	result, err := engine.Run(context.Background(), Request{
		Question: "where is auth?", Intent: planner.IntentLocate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 1 || result.Steps[0].Provider != planner.ProviderRetrieval {
		t.Fatalf("steps = %+v, want only retrieval", result.Steps)
	}
	if len(structuralProvider.calls) != 0 {
		t.Fatalf("structural ran after sufficiency: %d calls", len(structuralProvider.calls))
	}
	if result.Confidence < 0.9 {
		t.Fatalf("observed confidence = %.2f, want >= 0.9", result.Confidence)
	}
}

// TestContradictionsArePreserved proves a disagreement is retained with every
// provider's observation instead of being resolved to the most convenient one.
func TestContradictionsArePreserved(t *testing.T) {
	structuralProvider := &fakeProvider{
		name: planner.ProviderStructural, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.4, Coverage: 0.4,
			Records: []evidence.Record{record(evidence.ProviderStructural, "symbol:Charge", "calls", "A")}},
	}
	retrievalProvider := &fakeProvider{
		name: planner.ProviderRetrieval, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.4, Coverage: 0.4,
			Records: []evidence.Record{record(evidence.ProviderRetrieval, "symbol:Charge", "calls", "B")}},
	}
	semanticProvider := &fakeProvider{
		name: planner.ProviderSemantic, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.4, Coverage: 0.4,
			Records: []evidence.Record{record(evidence.ProviderSemantic, "symbol:Charge", "calls", "C")}},
	}
	engine := New(structuralProvider, retrievalProvider, semanticProvider)
	result, err := engine.Run(context.Background(), Request{
		Question: "how does checkout charge a card?", Intent: planner.IntentFlow,
		Budget: Budget{MaxSteps: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contradictions) != 1 {
		t.Fatalf("contradictions = %d, want 1: %+v", len(result.Contradictions), result.Contradictions)
	}
	contradiction := result.Contradictions[0]
	if len(contradiction.Objects) != 3 {
		t.Fatalf("objects = %v, want three preserved observations", contradiction.Objects)
	}
	if len(contradiction.Records) != 3 {
		t.Fatalf("records = %d, want 3", len(contradiction.Records))
	}
}

// TestProviderFailureIsExplicit proves a failed provider is reported and the
// loop continues with the next provider, without inventing a fact.
func TestProviderFailureIsExplicit(t *testing.T) {
	failing := &fakeProvider{name: planner.ProviderRetrieval, available: true, err: errors.New("index unavailable")}
	structuralProvider := &fakeProvider{
		name: planner.ProviderStructural, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.5, Coverage: 0.5,
			Records: []evidence.Record{record(evidence.ProviderStructural, "repository", "architecture", "layered")}},
	}
	engine := New(failing, structuralProvider)
	result, err := engine.Run(context.Background(), Request{Question: "locate auth", Intent: planner.IntentLocate})
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, step := range result.Steps {
		if step.Provider == planner.ProviderRetrieval {
			failed = step.Failed && step.State == evidence.Unknown
		}
	}
	if !failed {
		t.Fatalf("retrieval failure was not explicit: %+v", result.Steps)
	}
	joined := strings.Join(result.Issues, "\n")
	if !strings.Contains(joined, "index unavailable") || !strings.Contains(joined, "no fact was inferred") {
		t.Fatalf("issues do not name the failure: %s", joined)
	}
	if len(structuralProvider.calls) != 1 {
		t.Fatalf("structural did not run after the failure: %d calls", len(structuralProvider.calls))
	}
}

// TestBudgetBoundsStepsAndRequests proves the shared meter stops work rather
// than silently continuing.
func TestBudgetBoundsStepsAndRequests(t *testing.T) {
	provider := &fakeProvider{
		name: planner.ProviderRetrieval, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.2,
			Records: []evidence.Record{record(evidence.ProviderRetrieval, "file:a.go", "ranked_for", "q")}},
	}
	engine := New(provider)
	result, err := engine.Run(context.Background(), Request{
		Question: "project overview", Intent: planner.IntentProject,
		Budget: Budget{MaxSteps: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(result.Steps))
	}
	meter := newMeter(Budget{MaxLSPRequests: 1})
	if err := meter.ChargeLSP(); err != nil {
		t.Fatal(err)
	}
	if err := meter.ChargeLSP(); !errors.Is(err, ErrBudget) {
		t.Fatalf("second LSP charge = %v, want ErrBudget", err)
	}
}

// TestInferIntentIsDeterministic keeps the heuristic from changing between
// runs and from routing architecture questions to an expensive debug row.
func TestInferIntentIsDeterministic(t *testing.T) {
	cases := map[string]planner.Intent{
		"explain the architecture":             planner.IntentArchitecture,
		"what depends on internal/agent?":      planner.IntentImpact,
		"why does Serve panic at runtime?":     planner.IntentDebug,
		"where is authentication implemented?": planner.IntentLocate,
		"how does checkout charge a card?":     planner.IntentFlow,
		"verify the change is complete":        planner.IntentVerify,
		"what is this project?":                planner.IntentProject,
	}
	for question, want := range cases {
		for run := 0; run < 3; run++ {
			if got := InferIntent(question); got != want {
				t.Fatalf("InferIntent(%q) = %s, want %s", question, got, want)
			}
		}
	}
}

// TestCancellationStopsTheLoop proves a cancelled context ends the run with a
// cancellation status instead of continuing provider work.
func TestCancellationStopsTheLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &fakeProvider{
		name: planner.ProviderStructural, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.5},
	}
	result, err := New(provider).Run(ctx, Request{Question: "overview", Intent: planner.IntentProject})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %s, want cancelled", result.Status)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider ran after cancellation: %d calls", len(provider.calls))
	}
}

func plannerStep() planner.Step {
	return planner.Step{Provider: planner.ProviderSemantic, Strategy: "definition-references"}
}
