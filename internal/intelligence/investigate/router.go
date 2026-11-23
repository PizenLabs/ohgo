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

	// Native first. The task contract gives native structural analysis
	// precedence and makes the external provider an escalation for a specific
	// evidence gap, not a replacement for every target-shaped question. An
	// explicit external preference is the one exception: honoring it outranks
	// the cheaper native answer.
	external, externalDisclosure := r.externalFor(ctx, req, step)
	var native Observation
	haveNative := r.Native != nil
	var nativeErr error
	if haveNative {
		native, nativeErr = r.Native.Observe(ctx, req, step, meter)
		if nativeErr == nil && native.Sufficient &&
			native.Outcome != OutcomeStale && native.Outcome != OutcomeFailed &&
			(external == nil || !req.PreferExternal) {
			// A sufficient answer can still carry a global coverage limit
			// (unsupported SQL or templates). That limit does not turn a
			// complete focus answer into an evidence gap that justifies an
			// external request. A request that explicitly asked for the
			// external provider skips this shortcut: the preference must be
			// honored, not silently downgraded to a sufficient native answer.
			native.Source = "native"
			if externalDisclosure != "" {
				native.Issues = append(native.Issues, externalDisclosure)
			}
			return native, nil
		}
	}

	if external == nil {
		if !haveNative {
			return Observation{
				Outcome: OutcomeUnavailable,
				Issues:  []string{"no structural intelligence provider is configured"},
			}, nil
		}
		if nativeErr != nil {
			return Observation{}, nativeErr
		}
		native.Source = "native"
		if externalDisclosure != "" {
			native.Issues = append(native.Issues, externalDisclosure)
		}
		return native, nil
	}

	if err := meter.ChargeExternal(); err != nil {
		if !haveNative {
			return Observation{Outcome: OutcomeUnavailable, Issues: []string{err.Error()}}, nil
		}
		if nativeErr != nil {
			return Observation{}, nativeErr
		}
		native.Source = "native"
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
		// A native gap is what justified the escalation. Keep the native
		// evidence too: for impact it owns references, importers, and test
		// associations that the external call graph does not carry.
		if haveNative && nativeErr == nil {
			return mergeStructural(native, externalObservation), nil
		}
		return externalObservation, nil
	}

	if !haveNative {
		if externalErr != nil {
			return Observation{}, fmt.Errorf("external structural provider: %w", externalErr)
		}
		return externalObservation, nil
	}
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
	// A request that explicitly asked for the external provider is the
	// exception: it is owed the explanation, not silence.
	if externalErr == nil &&
		(externalObservation.Outcome == OutcomeUnavailable || externalObservation.Outcome == OutcomeNotIndexed) {
		native.Source = "native"
		if req.PreferExternal {
			native.Issues = append(native.Issues,
				"requested external structural provider reported "+string(externalObservation.Outcome)+
					"; native structural analysis served this step")
		}
		return native, nil
	}

	native.Issues = append(fallbackIssues(externalObservation, externalErr), native.Issues...)
	// A fallback still preserves the external facts that explain why it was
	// not usable, such as ambiguity candidates or a stale generation. Dropping
	// them would hide the very uncertainty the fallback disclosed.
	externalSource := externalObservation.Source
	if externalSource == "" {
		externalSource = "external"
	}
	for i := range externalObservation.Facts {
		if externalObservation.Facts[i].Source == "" {
			externalObservation.Facts[i].Source = externalSource
		}
	}
	native.Facts = append(native.Facts, externalObservation.Facts...)
	native.Records = append(native.Records, externalObservation.Records...)
	if native.Outcome == OutcomeSuccess {
		native.Outcome = OutcomePartial
	}
	native.Source = "native"
	return native, nil
}

// mergeStructural combines a native observation with an escalating external
// one. Source attribution stays per fact and per record; the step-level label
// names both mechanisms.
func mergeStructural(native, external Observation) Observation {
	for i := range native.Facts {
		if native.Facts[i].Source == "" {
			native.Facts[i].Source = "native"
		}
	}
	extSource := external.Source
	if extSource == "" {
		extSource = "external"
	}
	for i := range external.Facts {
		if external.Facts[i].Source == "" {
			external.Facts[i].Source = extSource
		}
	}
	merged := native
	merged.Facts = append(append([]Fact(nil), native.Facts...), external.Facts...)
	merged.Records = append(append([]evidence.Record(nil), native.Records...), external.Records...)
	merged.Issues = append(append([]string(nil), native.Issues...), external.Issues...)
	merged.Source = "native+" + extSource
	merged.Sufficient = true
	if external.Confidence > merged.Confidence {
		merged.Confidence = external.Confidence
	}
	if external.Coverage > merged.Coverage {
		merged.Coverage = external.Coverage
	}
	// Combined evidence is additive, not a claim that either provider alone
	// completed the step. Stale external evidence downgrades the combined
	// observation rather than being silently averaged with current native
	// facts.
	switch {
	case external.Outcome == OutcomeStale:
		merged.Outcome = OutcomeStale
		merged.Issues = append(merged.Issues,
			"external structural evidence is stale; native structural analysis is current")
	case merged.Outcome == OutcomeSuccess:
		merged.Outcome = OutcomePartial
	}
	if merged.Summary == "" {
		merged.Summary = external.Summary
	} else if external.Summary != "" {
		merged.Summary += "; " + external.Summary
	}
	return merged
}

// externalFor returns the external provider when this step's information need
// is better served by it, and it can actually run. The second return value is
// the disclosure owed when an explicit external preference cannot be met: it is
// non-empty only when the request asked for the external provider and none was
// returned, so an auto request stays exactly as quiet as before.
func (r *StructuralRouter) externalFor(ctx context.Context, req Request, step planner.Step) (Provider, string) {
	if r.External == nil {
		if req.PreferExternal {
			return nil, "requested external structural provider is not configured; native structural analysis served this step"
		}
		return nil, ""
	}
	if !prefersExternal(req, step) {
		if req.PreferExternal {
			// Only native-only orientation steps reach here under an explicit
			// preference; every other strategy is widened by prefersExternal.
			return nil, "requested external structural provider was not used: this orientation step is native-only by design; native structural analysis served it"
		}
		return nil, ""
	}
	if !r.External.Available(ctx) {
		if req.PreferExternal {
			return nil, "requested external structural provider is unavailable; native structural analysis served this step"
		}
		return nil, ""
	}
	return r.External, ""
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
	if req.PreferExternal {
		// An explicit request widens the policy to every step the external
		// provider can serve. Orientation steps stay native-only by design.
		return true
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
