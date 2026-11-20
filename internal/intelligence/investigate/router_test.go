package investigate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

type fakeStructuralProvider struct {
	name        planner.Provider
	available   bool
	observation Observation
	err         error

	calls int
}

func (p *fakeStructuralProvider) Name() planner.Provider { return p.name }

func (p *fakeStructuralProvider) Available(context.Context) bool { return p.available }

func (p *fakeStructuralProvider) Observe(context.Context, Request, planner.Step, *Meter) (Observation, error) {
	p.calls++
	return p.observation, p.err
}

func nativeObservation() Observation {
	return Observation{
		Outcome: OutcomeSuccess, Source: "native", State: evidence.Observed,
		Confidence: 0.5, Coverage: 0.5, Summary: "native",
		Facts: []Fact{{Provider: "structural", Subject: "native", Detail: "observed"}},
	}
}

func TestPrefersExternalIsDrivenByInformationNeed(t *testing.T) {
	tests := []struct {
		name   string
		req    Request
		step   planner.Step
		expect bool
	}{
		{"orientation stays native", Request{Intent: planner.IntentProject}, planner.Step{Strategy: "filesystem-hierarchy"}, false},
		{"architecture needs graph shape", Request{Intent: planner.IntentArchitecture}, planner.Step{Strategy: "dependency-graph"}, true},
		{"locate without target stays native", Request{Intent: planner.IntentLocate}, planner.Step{Strategy: "module-discovery"}, false},
		{"locate with target needs resolution", Request{Intent: planner.IntentLocate, Focus: "auth.Serve"}, planner.Step{Strategy: "module-discovery"}, true},
		{"flow without target stays native", Request{Intent: planner.IntentFlow}, planner.Step{Strategy: "dependency-graph"}, false},
		{"flow with target needs traversal", Request{Intent: planner.IntentFlow, Focus: "auth.Serve"}, planner.Step{Strategy: "dependency-graph"}, true},
		{"impact is a reverse-edge question", Request{Intent: planner.IntentImpact}, planner.Step{Strategy: "reverse-dependencies"}, true},
		{"impact with target is a reverse-edge question", Request{Intent: planner.IntentImpact, Focus: "auth.Serve"}, planner.Step{Strategy: "reverse-dependencies"}, true},
		{"verify needs test impact", Request{Intent: planner.IntentVerify}, planner.Step{Strategy: "expected-change-shape"}, true},
		{"debug without target stays native", Request{Intent: planner.IntentDebug}, planner.Step{Strategy: "focused-region"}, false},
		{"debug with target narrows structurally", Request{Intent: planner.IntentDebug, Focus: "auth.Serve"}, planner.Step{Strategy: "focused-region"}, true},
		{"hierarchy step never escalates", Request{Intent: planner.IntentArchitecture}, planner.Step{Strategy: "filesystem-hierarchy"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := prefersExternal(test.req, test.step); got != test.expect {
				t.Fatalf("prefersExternal = %t, want %t", got, test.expect)
			}
		})
	}
}

func TestRouterReusesNativeForOrientationAndDoesNotCallExternal(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{Outcome: OutcomeSuccess, Source: "external"},
	}
	router := &StructuralRouter{Native: native, External: external}
	meter := NewMeter(Budget{})

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentProject}, planner.Step{Strategy: "filesystem-hierarchy"}, meter)
	if err != nil {
		t.Fatal(err)
	}
	if external.calls != 0 {
		t.Fatalf("external provider was called %d time(s) for orientation", external.calls)
	}
	if native.calls != 1 || result.Facts[0].Subject != "native" {
		t.Fatalf("native path was not used: calls=%d result=%+v", native.calls, result)
	}
	if meter.External() != 0 {
		t.Fatalf("external budget was charged for a native step: %d", meter.External())
	}
}

func TestRouterEscalatesToExternalWhenNativeIsInsufficient(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{
			Outcome: OutcomeSuccess, Source: "gry", State: evidence.Observed,
			Confidence: 0.7, Coverage: 0.5,
			Facts: []Fact{{Provider: "structural", Subject: "caller", Detail: "CALLS target"}},
			Records: []evidence.Record{{
				Provider: evidence.ProviderStructural, Source: evidence.SourceGRY,
				Subject: "symbol:target", Relation: "CALLS", Object: "symbol:caller",
				State: evidence.Observed, Confidence: 0.8,
			}},
		},
	}
	router := &StructuralRouter{Native: native, External: external}
	meter := NewMeter(Budget{})

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentFlow, Focus: "target"},
		planner.Step{Strategy: "dependency-graph"}, meter)
	if err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 || external.calls != 1 {
		t.Fatalf("routing calls: native=%d external=%d, want native first then escalation", native.calls, external.calls)
	}
	if result.Source != "native+gry" || result.Outcome != OutcomePartial {
		t.Fatalf("combined observation = %+v", result)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("native evidence was lost in the merge: %+v", result.Facts)
	}
	if meter.External() != 1 {
		t.Fatalf("external request was not charged: %d", meter.External())
	}
}

// A sufficient native answer must not spend an external request merely
// because one is configured.
func TestRouterKeepsSufficientNativeWithoutEscalation(t *testing.T) {
	nativeObs := nativeObservation()
	nativeObs.Sufficient = true
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObs}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{Outcome: OutcomeSuccess, Source: "gry"},
	}
	router := &StructuralRouter{Native: native, External: external}
	meter := NewMeter(Budget{})

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentImpact, Focus: "target"},
		planner.Step{Strategy: "reverse-dependencies"}, meter)
	if err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 || external.calls != 0 {
		t.Fatalf("routing calls: native=%d external=%d, want native only", native.calls, external.calls)
	}
	if result.Source != "native" || meter.External() != 0 {
		t.Fatalf("result = %+v external=%d", result, meter.External())
	}
}

func TestRouterKeepsSufficientNativeEvenWhenGlobalCoverageIsPartial(t *testing.T) {
	nativeObs := nativeObservation()
	nativeObs.Outcome = OutcomePartial
	nativeObs.Sufficient = true
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObs}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{Outcome: OutcomeSuccess, Source: "gry"},
	}
	router := &StructuralRouter{Native: native, External: external}
	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentImpact, Focus: "target"},
		planner.Step{Strategy: "reverse-dependencies"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if external.calls != 0 || result.Source != "native" {
		t.Fatalf("global partial coverage escalated unnecessarily: %+v external=%d", result, external.calls)
	}
}

func TestRouterFallsBackWhenExternalIsUnavailable(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{name: planner.ProviderStructural, available: false}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentFlow, Focus: "target"},
		planner.Step{Strategy: "dependency-graph"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if external.calls != 0 || native.calls != 1 {
		t.Fatalf("calls: external=%d native=%d", external.calls, native.calls)
	}
	if result.Source != "native" || len(result.Issues) != 0 {
		t.Fatalf("an absent optional provider must not qualify the native answer: %+v", result)
	}
}

func TestRouterFallsBackAndPreservesExternalFailure(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{
			Outcome: OutcomeFailed, State: evidence.Unknown,
			Issues: []string{"gry command_failed: index is locked"},
		},
	}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentImpact}, planner.Step{Strategy: "reverse-dependencies"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "native" || result.Outcome != OutcomePartial {
		t.Fatalf("fallback outcome = %s/%s, want native/PARTIAL", result.Source, result.Outcome)
	}
	joined := strings.Join(result.Issues, "\n")
	for _, want := range []string{"external structural provider reported FAILED", "index is locked"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fallback did not preserve %q: %s", want, joined)
		}
	}
}

func TestRouterFallsBackAndPreservesAmbiguity(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{
			Outcome: OutcomeAmbiguous, State: evidence.PartiallyObserved,
			Facts:  []Fact{{Provider: "structural", Subject: "candidate:a", Detail: "a.go:1"}, {Provider: "structural", Subject: "candidate:b", Detail: "b.go:2"}},
			Issues: []string{"gry trace reported an ambiguous target for \"Run\"; no candidate was selected"},
		},
	}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentFlow, Focus: "Run"},
		planner.Step{Strategy: "dependency-graph"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	subjects := make(map[string]bool)
	for _, fact := range result.Facts {
		subjects[fact.Subject] = true
	}
	if !subjects["candidate:a"] || !subjects["candidate:b"] {
		t.Fatalf("ambiguity candidates were dropped: %+v", result.Facts)
	}
	if result.Outcome != OutcomePartial {
		t.Fatalf("outcome = %s, want PARTIAL so the ambiguity is not hidden", result.Outcome)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "no candidate was selected") {
		t.Fatalf("ambiguity issue was not preserved: %+v", result.Issues)
	}
}

func TestRouterNeverPresentsStaleExternalEvidenceAsCurrent(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{
			Outcome: OutcomeStale, Source: "gry", State: evidence.PartiallyObserved,
			Facts: []Fact{{Provider: "structural", Subject: "stale-node", Detail: "old generation"}},
		},
	}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentArchitecture}, planner.Step{Strategy: "dependency-graph"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 || external.calls != 1 {
		t.Fatalf("routing calls: native=%d external=%d, want native first then escalation", native.calls, external.calls)
	}
	if result.Outcome != OutcomeStale {
		t.Fatalf("stale evidence was presented as current: %+v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "stale") {
		t.Fatalf("stale limitation was not stated: %+v", result.Issues)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("native or stale facts were dropped: %+v", result.Facts)
	}
}

func TestRouterUnresolvedExternalFallsBackWithoutClaimingAbsence(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{
			Outcome: OutcomeUnresolved, State: evidence.Unknown,
			Issues: []string{"gry trace did not resolve \"x\"; this is not proof that the target does not exist"},
		},
	}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentFlow, Focus: "x"},
		planner.Step{Strategy: "dependency-graph"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 {
		t.Fatal("native fallback was not executed")
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "not proof that the target does not exist") {
		t.Fatalf("unresolved evidence became absence: %+v", result.Issues)
	}
}

func TestRouterExternalErrorFallsBackAndIsNotSilent(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		err: errors.New("transport exploded"),
	}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentVerify}, planner.Step{Strategy: "expected-change-shape"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "transport exploded") {
		t.Fatalf("external transport error was hidden: %+v", result.Issues)
	}
}

func TestRouterExternalBudgetCannotBeBypassed(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{Outcome: OutcomeSuccess, Source: "gry", Facts: []Fact{{Provider: "structural", Subject: "external", Detail: ""}}},
	}
	router := &StructuralRouter{Native: native, External: external}
	meter := NewMeter(Budget{MaxExternalRequests: 1})
	req := Request{Intent: planner.IntentImpact}
	step := planner.Step{Strategy: "reverse-dependencies"}

	if _, err := router.Observe(context.Background(), req, step, meter); err != nil {
		t.Fatal(err)
	}
	result, err := router.Observe(context.Background(), req, step, meter)
	if err != nil {
		t.Fatal(err)
	}
	if external.calls != 1 {
		t.Fatalf("external calls = %d, want 1 (budget must bound it)", external.calls)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "budget exhausted") {
		t.Fatalf("budget refusal was not explicit: %+v", result.Issues)
	}
	if result.Source != "native" {
		t.Fatalf("budget refusal did not use the native path: %+v", result)
	}
}

// An installed-but-not-indexed external provider is a configuration state, not
// a limitation of the native answer. The native result stays clean.
func TestRouterDoesNotQualifyNativeWhenExternalIsNotIndexed(t *testing.T) {
	native := &fakeStructuralProvider{name: planner.ProviderStructural, available: true, observation: nativeObservation()}
	external := &fakeStructuralProvider{
		name: planner.ProviderStructural, available: true,
		observation: Observation{
			Outcome: OutcomeNotIndexed, State: evidence.Unknown,
			Issues: []string{"gry not_indexed: no gry project is indexed for this workspace"},
		},
	}
	router := &StructuralRouter{Native: native, External: external}

	result, err := router.Observe(context.Background(),
		Request{Intent: planner.IntentImpact}, planner.Step{Strategy: "reverse-dependencies"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if native.calls != 1 || external.calls != 1 {
		t.Fatalf("calls: native=%d external=%d", native.calls, external.calls)
	}
	if result.Source != "native" || result.Outcome != OutcomeSuccess {
		t.Fatalf("optional absence qualified the native result: %+v", result)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("optional absence produced an issue: %+v", result.Issues)
	}
}
