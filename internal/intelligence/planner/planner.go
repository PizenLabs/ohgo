// Package planner selects the cheapest sufficient set of intelligence
// providers for one task.
//
// It is a pure decision layer: it starts no server, reads no file, and grants
// no authority. Availability is reported by the caller, and runtime steps are
// only planned when a provider reports that an authorized session exists. The
// plan records every selected and skipped provider with a reason, so the
// decision can be audited and benchmarked.
package planner

import (
	"errors"
	"fmt"
	"sort"
)

// Provider is one intelligence capability.
type Provider string

// Providers.
const (
	ProviderStructural Provider = "structural"
	ProviderRetrieval  Provider = "retrieval"
	ProviderSemantic   Provider = "semantic"
	ProviderRuntime    Provider = "runtime"
)

// Intent is the task class.
type Intent string

// Intents.
const (
	IntentProject      Intent = "project"
	IntentArchitecture Intent = "architecture"
	IntentLocate       Intent = "locate"
	IntentFlow         Intent = "flow"
	IntentImpact       Intent = "impact"
	IntentDebug        Intent = "debug"
	IntentVerify       Intent = "verify"
)

// Availability reports which providers can be used right now. Runtime being
// true means an authorized debug session can be established; this package
// never derives that from intent.
type Availability struct {
	Structural bool
	Retrieval  bool
	Semantic   bool
	Runtime    bool
}

// Budget bounds the plan.
type Budget struct {
	// MaxSteps is the maximum selected providers. Zero uses the intent default.
	MaxSteps int
}

// Cost is a relative estimate, not a duration.
type Cost struct {
	// Units are relative work units; 1 is one cheap metadata observation.
	Units float64
	// Band is low, medium, or high, and is the only user-facing form.
	Band string
}

// Step is one selected provider.
type Step struct {
	Provider Provider
	Strategy string
	Reason   string
	Cost     Cost
	// ExpectedGain is the estimated confidence contribution in [0,1].
	ExpectedGain float64
	// EstimatedConfidence is the plan's confidence after this step.
	EstimatedConfidence float64
}

// Skip is one provider that was not selected, with the reason.
type Skip struct {
	Provider Provider
	Reason   string
}

// StopReason explains why planning ended.
type StopReason string

// Stop reasons.
const (
	StopConfidenceReached StopReason = "estimated_confidence_reached"
	StopBudgetExhausted   StopReason = "step_budget_exhausted"
	StopPartial           StopReason = "partial_provider_availability"
)

// Plan is one deterministic provider plan.
type Plan struct {
	Intent     Intent
	Steps      []Step
	Skips      []Skip
	Stop       StopReason
	Confidence float64
	// Target is the intent's confidence threshold.
	Target float64
}

// Errors.
var (
	// ErrUnknownIntent reports an unregistered intent.
	ErrUnknownIntent = errors.New("planner: unknown intent")
	// ErrNoProvider reports a plan with no usable provider.
	ErrNoProvider = errors.New("planner: no provider is available for the intent")
)

// candidate describes one possible step.
type candidate struct {
	provider Provider
	strategy string
	reason   string
	cost     Cost
	gain     float64
	// mandatory steps are selected whenever the provider is available.
	mandatory bool
}

// intents is the decision table. It is data, not a chain of conditionals, so a
// new capability adds a row instead of editing orchestration branches.
var intents = map[Intent]struct {
	target float64
	steps  []candidate
}{
	IntentProject: {target: 0.55, steps: []candidate{
		{ProviderStructural, "filesystem-hierarchy", "orientation needs topology, not parsing", Cost{1, "low"}, 0.45, true},
		{ProviderRetrieval, "representative-source", "rank representative files for the overview", Cost{2, "low"}, 0.20, false},
	}},
	IntentArchitecture: {target: 0.70, steps: []candidate{
		{ProviderStructural, "dependency-graph", "architecture claims require observed edges", Cost{4, "medium"}, 0.50, true},
		{ProviderRetrieval, "representative-source", "cite representative source per module", Cost{2, "low"}, 0.15, false},
		{ProviderSemantic, "symbol-resolution", "resolve declarations the graph only names", Cost{3, "medium"}, 0.15, false},
	}},
	IntentLocate: {target: 0.75, steps: []candidate{
		{ProviderRetrieval, "bm25", "lexical ranking finds candidates before parsing", Cost{2, "low"}, 0.40, true},
		{ProviderStructural, "module-discovery", "place candidates in the module topology", Cost{3, "medium"}, 0.20, false},
		{ProviderSemantic, "definition-references", "resolve candidates semantically when a server is available", Cost{3, "medium"}, 0.25, false},
	}},
	IntentFlow: {target: 0.80, steps: []candidate{
		{ProviderRetrieval, "bm25", "find the flow's entry candidates", Cost{2, "low"}, 0.30, true},
		{ProviderStructural, "dependency-graph", "establish module direction and cycles", Cost{4, "medium"}, 0.25, true},
		{ProviderSemantic, "call-hierarchy", "follow resolved calls, not name matches", Cost{3, "medium"}, 0.30, false},
		{ProviderRuntime, "observe-execution", "confirm the path actually executes", Cost{8, "high"}, 0.35, false},
	}},
	IntentImpact: {target: 0.80, steps: []candidate{
		{ProviderStructural, "reverse-dependencies", "impact is a reverse-edge question", Cost{4, "medium"}, 0.45, true},
		{ProviderSemantic, "references", "resolve references the syntax layer cannot", Cost{3, "medium"}, 0.30, false},
		{ProviderRetrieval, "tests-and-callers", "find likely tests and callers", Cost{2, "low"}, 0.15, false},
	}},
	IntentDebug: {target: 0.85, steps: []candidate{
		{ProviderStructural, "focused-region", "narrow the dependency region before executing", Cost{4, "medium"}, 0.30, true},
		{ProviderRetrieval, "bm25", "rank likely failure sites", Cost{2, "low"}, 0.15, false},
		{ProviderSemantic, "diagnostics", "collect language-service diagnostics", Cost{2, "low"}, 0.15, false},
		{ProviderRuntime, "observe-execution", "runtime state is the only proof of actual behaviour", Cost{8, "high"}, 0.40, false},
	}},
	IntentVerify: {target: 0.90, steps: []candidate{
		{ProviderStructural, "expected-change-shape", "confirm the intended components and dependencies", Cost{4, "medium"}, 0.35, true},
		{ProviderSemantic, "reference-resolution", "confirm references and interfaces", Cost{3, "medium"}, 0.25, false},
		{ProviderRetrieval, "test-coverage-candidates", "find relevant tests", Cost{2, "low"}, 0.15, false},
	}},
}

// DefaultMaxSteps bounds providers per plan.
const DefaultMaxSteps = 5

// Progress is what an investigation has already established.
//
// It is the closed-loop input: the caller reports observed confidence, not the
// plan's estimate, so a provider that returned little usable evidence does not
// silently count as a completed step.
type Progress struct {
	// Executed names providers that already ran in this investigation.
	Executed map[Provider]bool
	// Confidence is the observed confidence in [0,1].
	Confidence float64
	// Coverage is the observed task coverage in [0,1].
	Coverage float64
}

// Reassess returns the remaining plan after observed progress.
//
// Executed providers are never selected again. The plan starts from the
// observed confidence, so the same step budget means "additional work", not
// cumulative work. A plan with no remaining steps is returned with the stop
// reason rather than as an error.
func Reassess(intent Intent, availability Availability, budget Budget, progress Progress) (Plan, error) {
	if progress.Confidence < 0 {
		progress.Confidence = 0
	}
	if progress.Confidence > 1 {
		progress.Confidence = 1
	}
	return build(intent, availability, budget, &progress)
}

// Build returns a deterministic plan for intent.
//
// Mandatory steps run in table order when available. Optional steps are ranked
// by gain per cost unit and added until the target confidence is met or the
// step budget is exhausted; ties break on provider name.
func Build(intent Intent, availability Availability, budget Budget) (Plan, error) {
	return build(intent, availability, budget, nil)
}

func build(intent Intent, availability Availability, budget Budget, progress *Progress) (Plan, error) {
	spec, ok := intents[intent]
	if !ok {
		return Plan{}, fmt.Errorf("%w: %q", ErrUnknownIntent, intent)
	}
	maxSteps := budget.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	if maxSteps > 8 {
		maxSteps = 8
	}
	plan := Plan{Intent: intent, Target: spec.target, Confidence: 0}
	if progress != nil {
		plan.Confidence = progress.Confidence
	}
	executed := func(provider Provider) bool {
		return progress != nil && progress.Executed != nil && progress.Executed[provider]
	}
	available := map[Provider]bool{
		ProviderStructural: availability.Structural,
		ProviderRetrieval:  availability.Retrieval,
		ProviderSemantic:   availability.Semantic,
		ProviderRuntime:    availability.Runtime,
	}

	for _, step := range spec.steps {
		if executed(step.provider) {
			plan.Skips = append(plan.Skips, Skip{step.provider, "already executed in this investigation"})
			continue
		}
		if step.mandatory && available[step.provider] && len(plan.Steps) < maxSteps {
			plan.Steps = append(plan.Steps, makeStep(step, plan.Confidence))
			plan.Confidence = plan.Steps[len(plan.Steps)-1].EstimatedConfidence
		}
	}
	optional := make([]candidate, 0, len(spec.steps))
	for _, step := range spec.steps {
		switch {
		case executed(step.provider):
			continue
		case step.mandatory:
			if !available[step.provider] {
				plan.Skips = append(plan.Skips, Skip{step.provider, "provider unavailable; the mandatory step cannot run"})
			}
		case !available[step.provider]:
			reason := "provider unavailable"
			if step.provider == ProviderRuntime {
				reason = "no authorized runtime session; debug adapters are not started for this intent"
			}
			plan.Skips = append(plan.Skips, Skip{step.provider, reason})
		default:
			optional = append(optional, step)
		}
	}
	sort.SliceStable(optional, func(i, j int) bool {
		left := optional[i].gain / optional[i].cost.Units
		right := optional[j].gain / optional[j].cost.Units
		if left != right {
			return left > right
		}
		return optional[i].provider < optional[j].provider
	})
	budgetHit := false
	for _, step := range optional {
		if plan.Confidence >= spec.target {
			plan.Skips = append(plan.Skips, Skip{step.provider, "estimated confidence already meets the intent target"})
			continue
		}
		if len(plan.Steps) >= maxSteps {
			budgetHit = true
			plan.Skips = append(plan.Skips, Skip{step.provider, "step budget exhausted"})
			continue
		}
		plan.Steps = append(plan.Steps, makeStep(step, plan.Confidence))
		plan.Confidence = plan.Steps[len(plan.Steps)-1].EstimatedConfidence
	}
	switch {
	case budgetHit:
		plan.Stop = StopBudgetExhausted
	case plan.Confidence >= spec.target:
		plan.Stop = StopConfidenceReached
	default:
		plan.Stop = StopPartial
	}
	// Runtime is always accounted for: an investigation must be able to state
	// that it did not debug, and why. Other providers are row-scoped.
	runtimeNamed := false
	for _, skip := range plan.Skips {
		if skip.Provider == ProviderRuntime {
			runtimeNamed = true
		}
	}
	for _, step := range plan.Steps {
		if step.Provider == ProviderRuntime {
			runtimeNamed = true
		}
	}
	if !runtimeNamed {
		reason := "no authorized runtime session; debug adapters are not started for this intent"
		if available[ProviderRuntime] {
			reason = "an authorized runtime session exists but this intent does not justify execution"
		}
		plan.Skips = append(plan.Skips, Skip{ProviderRuntime, reason})
	}
	if len(plan.Steps) == 0 && plan.Confidence < spec.target {
		return plan, fmt.Errorf("%w: %q", ErrNoProvider, intent)
	}
	sort.SliceStable(plan.Skips, func(i, j int) bool {
		return plan.Skips[i].Provider < plan.Skips[j].Provider
	})
	return plan, nil
}

// makeStep computes the confidence after one step with diminishing returns.
func makeStep(step candidate, current float64) Step {
	next := current + (1-current)*step.gain
	return Step{
		Provider: step.provider, Strategy: step.strategy, Reason: step.reason,
		Cost: step.cost, ExpectedGain: step.gain, EstimatedConfidence: next,
	}
}
