package investigate

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// StructuralRouter composes the native structural engine with an optional
// external structural-intelligence provider.
//
// The router is the capability boundary, not a provider of its own: both
// implementations answer the same structural step, and the choice is driven by
// the information the step requires. Role is not an input here; the planner has
// already decided that structural evidence is needed.
//
// The external provider is strictly optional. When it is absent, disabled, or
// fails, the native engine answers and the external outcome is preserved as an
// issue. An external absence never becomes "no structural evidence exists".
type StructuralRouter struct {
	// Native is the in-process structural engine. It is the fallback and the
	// preferred path for cheap orientation work.
	Native Provider
	// External is an optional bounded structural-intelligence adapter.
	External Provider
}

var _ Provider = (*StructuralRouter)(nil)

// Name implements Provider.
func (r *StructuralRouter) Name() planner.Provider { return planner.ProviderStructural }

// Available implements Provider. It reports a usable structural capability, not
// whether the external provider is configured: an unavailable external provider
// must not make the native capability look absent.
func (r *StructuralRouter) Available(ctx context.Context) bool {
	if r == nil {
		return false
	}
	if r.Native != nil && r.Native.Available(ctx) {
		return true
	}
	return r.External != nil && r.External.Available(ctx)
}

// Observe implements Provider.
func (r *StructuralRouter) Observe(ctx context.Context, req Request, step planner.Step, meter *Meter) (Observation, error) {
	if r == nil {
		return Observation{}, errors.New("structural router is not configured")
	}
	external := r.externalFor(ctx, req, step)
	if external == nil {
		if r.Native == nil {
			return Observation{
				Outcome: OutcomeUnavailable,
				Issues:  []string{"no structural intelligence provider is configured"},
			}, nil
		}
		return r.Native.Observe(ctx, req, step, meter)
	}

	if err := meter.ChargeExternal(); err != nil {
		if r.Native == nil {
			return Observation{Outcome: OutcomeUnavailable, Issues: []string{err.Error()}}, nil
		}
		native, nativeErr := r.Native.Observe(ctx, req, step, meter)
		if nativeErr != nil {
			return Observation{}, nativeErr
		}
		native.Issues = append(append([]string(nil), native.Issues...),
			err.Error()+"; native structural analysis used")
		return native, nil
	}

	externalObservation, externalErr := external.Observe(ctx, req, step, meter)
	if externalObservation.Outcome == "" && externalErr == nil {
		externalObservation.Outcome = OutcomeSuccess
	}
	if externalErr == nil && usableOutcome(externalObservation.Outcome) {
		if externalObservation.Source == "" {
			externalObservation.Source = "external"
		}
		return externalObservation, nil
	}

	if r.Native == nil {
		if externalErr != nil {
			return Observation{}, fmt.Errorf("external structural provider: %w", externalErr)
		}
		return externalObservation, nil
	}
	native, nativeErr := r.Native.Observe(ctx, req, step, meter)
	if nativeErr != nil {
		if externalErr != nil {
			return Observation{}, fmt.Errorf("external structural provider: %v; native structural provider: %w",
				externalErr, nativeErr)
		}
		return Observation{}, nativeErr
	}

	// The external provider being absent or not indexed is a configuration
	// state, not a limitation of the native answer. Qualifying every native
	// result with it would make an optional capability look like a failure.
	if externalErr == nil &&
		(externalObservation.Outcome == OutcomeUnavailable || externalObservation.Outcome == OutcomeNotIndexed) {
		native.Source = "native"
		return native, nil
	}

	native.Issues = append(fallbackIssues(externalObservation, externalErr), native.Issues...)
	// Preserve which mechanism produced each fact when the native and external
	// observations are combined. The step-level source is "native" because the
	// native answer is being returned, but a gry fact is still gry evidence.
	for i := range externalObservation.Facts {
		if externalObservation.Facts[i].Source == "" {
			externalObservation.Facts[i].Source = externalObservation.Source
		}
	}
	for i := range native.Facts {
		if native.Facts[i].Source == "" {
			native.Facts[i].Source = "native"
		}
	}
	native.Facts = append(append([]Fact(nil), externalObservation.Facts...), native.Facts...)
	native.Records = append(append([]evidence.Record(nil), externalObservation.Records...), native.Records...)
	native.Source = "native"
	switch {
	case len(native.Facts) > 0:
		native.Outcome = OutcomePartial
	case externalErr == nil && externalObservation.Outcome != "":
		native.Outcome = externalObservation.Outcome
	default:
		native.Outcome = OutcomeFailed
	}
	if native.Confidence <= 0 {
		native.Confidence = externalObservation.Confidence
	}
	if native.Coverage < externalObservation.Coverage {
		native.Coverage = externalObservation.Coverage
	}
	return native, nil
}

// externalFor returns the external provider when this step's information need
// is better served by it, and it can actually run.
func (r *StructuralRouter) externalFor(ctx context.Context, req Request, step planner.Step) Provider {
	if r.External == nil {
		return nil
	}
	if !prefersExternal(req, step) {
		return nil
	}
	if !r.External.Available(ctx) {
		return nil
	}
	return r.External
}

// prefersExternal is the information-need policy.
//
// It is deliberately about evidence shape, not role:
//   - orientation asks for topology, which the native metadata-first analysis
//     already answers without parsing or an external process;
//   - a target-shaped question (locate/flow/debug) needs symbol resolution and
//     relationship traversal, which is the external provider's strength;
//   - architecture, impact, and verification need dependency/test traversal
//     that the native model does not own at that resolution.
//
// When the external provider is unavailable, the native path still answers; the
// policy never makes correctness depend on the external capability.
func prefersExternal(req Request, step planner.Step) bool {
	if step.Strategy == "filesystem-hierarchy" {
		return false
	}
	switch req.Intent {
	case planner.IntentProject:
		return false
	case planner.IntentArchitecture, planner.IntentImpact, planner.IntentVerify:
		return true
	case planner.IntentLocate, planner.IntentFlow, planner.IntentDebug:
		return req.Focus != ""
	default:
		return false
	}
}

func usableOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeSuccess, OutcomePartial, OutcomeStale:
		return true
	default:
		return false
	}
}

// fallbackIssues records why the external evidence was not used. An external
// failure is never silently dropped: the native answer is qualified by it.
func fallbackIssues(observation Observation, err error) []string {
	if err != nil {
		return []string{
			"external structural provider failed: " + err.Error() +
				"; native structural analysis used; no fact was inferred from the failure",
		}
	}
	label := string(observation.Outcome)
	if label == "" {
		label = "no outcome"
	}
	issues := []string{
		"external structural provider reported " + label +
			"; native structural analysis used, and the external outcome is preserved rather than resolved",
	}
	return append(issues, observation.Issues...)
}
