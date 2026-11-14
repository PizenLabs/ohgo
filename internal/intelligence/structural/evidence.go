// Package structural is the adaptive structural intelligence engine behind the
// structure capability.
//
// It owns the reusable analysis layer: a typed multi-resolution model, evidence
// and confidence semantics, a strategy portfolio, an intent-aware planner, and
// a versioned result cache. It does not own the repository index (that is
// intelligence/ast), the authorization decision (control plane), or the
// model-facing text (execution/tools). Those boundaries are what let a future
// capability consume the same analysis instead of re-deriving it.
//
// The operating rule is "analyse the minimum that answers the task correctly":
// the planner starts from inventory metadata, selects the cheapest sufficient
// strategies, and stops when the remaining uncertainty is below the intent's
// threshold. No function here mutates repository source.
package structural

import (
	"sort"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
)

// EvidenceState is the shared evidence vocabulary. Structural analysis uses
// the same states as semantic, runtime, and retrieval observations so a
// consumer can compare them without translating.
//
// The states are ordered and not interchangeable. A consumer must never upgrade
// one into another: an OBSERVED containment is not a VERIFIED implementation,
// and an INFERRED relationship is not an observed one.
type EvidenceState = evidence.State

// Evidence states, weakest to strongest.
const (
	// StateUnknown means the claim could not be checked at all.
	StateUnknown = evidence.Unknown
	// StateInferred means a convention or name suggests the claim.
	StateInferred = evidence.Inferred
	// StatePartiallyObserved means part of the claim is observed and part is
	// not: a subtree with unparsed files, an aggregate with an unknown edge.
	StatePartiallyObserved = evidence.PartiallyObserved
	// StateObserved means the snapshot directly contains the fact.
	StateObserved = evidence.Observed
	// StateVerified means the fact has an explicit, adapter-level proof. It is
	// reserved for facts the producer can prove, never inferred upward.
	StateVerified = evidence.Verified
)

// EvidenceKind names the mechanism that produced a fact. Confidence is derived
// from the kind, not from model intuition.
type EvidenceKind string

// Evidence mechanisms, strongest to weakest.
const (
	// KindAdapterFact is a fact a language adapter parsed from source.
	KindAdapterFact EvidenceKind = "adapter_fact"
	// KindManifestFact is a fact read from a build manifest (go.mod, go.work,
	// package.json, Cargo.toml, ...).
	KindManifestFact EvidenceKind = "manifest_fact"
	// KindImportEdge is an aggregated dependency edge proved by an import
	// declaration.
	KindImportEdge EvidenceKind = "import_edge"
	// KindInventory is a filesystem fact observed during the scan.
	KindInventory EvidenceKind = "inventory"
	// KindPathConvention is a classification implied by a path or filename.
	KindPathConvention EvidenceKind = "path_convention"
	// KindUnavailable means no observation was possible.
	KindUnavailable EvidenceKind = "unavailable"
)

// Confidence constants for each mechanism.
//
// They are deliberately conservative and fixed so two runs over the same
// snapshot produce identical numbers. A path convention can suggest a source
// class; it can never reach the confidence of a parsed fact.
const (
	confidenceAdapterFact  = 0.95
	confidenceManifestFact = 0.90
	confidenceImportEdge   = 0.85
	confidenceInventory    = 0.80
	confidencePathRule     = 0.45
	confidenceAggregateCap = 0.90
)

// EvidenceRef is one witness behind a claim. It is a coordinate, not source
// text: a consumer that needs the bytes reads the path and line range.
type EvidenceRef struct {
	Kind    EvidenceKind `json:"kind"`
	Path    string       `json:"path,omitempty"`
	Line    int          `json:"line,omitempty"`
	Adapter string       `json:"adapter,omitempty"`
	// Rule names the exact convention that produced an inferred fact.
	Rule string `json:"rule,omitempty"`
}

// Evidence is the provenance of one structural fact.
//
// Reason explains an UNKNOWN or PARTIALLY_OBSERVED state in prose-free,
// machine-readable form. It is required for both: a partial claim without a
// named gap cannot be qualified by the consumer.
type Evidence struct {
	State      EvidenceState `json:"state"`
	Confidence float64       `json:"confidence"`
	Reason     string        `json:"reason,omitempty"`
	Refs       []EvidenceRef `json:"refs,omitempty"`
}

// Band labels a confidence numerically for display, not for reasoning.
func (e Evidence) Band() string {
	switch {
	case e.State == StateUnknown:
		return "unknown"
	case e.Confidence >= 0.85:
		return "high"
	case e.Confidence >= 0.6:
		return "medium"
	default:
		return "low"
	}
}

// WithRef returns a copy carrying one more witness.
func (e Evidence) WithRef(ref EvidenceRef) Evidence {
	refs := make([]EvidenceRef, 0, len(e.Refs)+1)
	refs = append(refs, e.Refs...)
	refs = append(refs, ref)
	e.Refs = refs
	return e
}

// ObservedEvidence builds an observed fact with the mechanism's fixed
// confidence. A mechanism without a table entry cannot produce OBSERVED
// evidence; it is a programming error, so the fact degrades to UNKNOWN instead
// of inventing a number.
func ObservedEvidence(kind EvidenceKind, refs ...EvidenceRef) Evidence {
	var base float64
	switch kind {
	case KindAdapterFact:
		base = confidenceAdapterFact
	case KindManifestFact:
		base = confidenceManifestFact
	case KindImportEdge:
		base = confidenceImportEdge
	case KindInventory:
		base = confidenceInventory
	default:
		return UnknownEvidence("no confidence table entry for evidence kind " + string(kind))
	}
	return Evidence{State: StateObserved, Confidence: base, Refs: refs}
}

// VerifiedEvidence is the explicit proof constructor. Nothing derives a
// VERIFIED state from another state; only a producer that holds a proof calls
// this.
func VerifiedEvidence(kind EvidenceKind, refs ...EvidenceRef) Evidence {
	evidence := ObservedEvidence(kind, refs...)
	if evidence.State == StateUnknown {
		return evidence
	}
	evidence.State = StateVerified
	return evidence
}

// InferredEvidence builds a convention-based fact. It can never be promoted by
// combining it with anything weaker than an observation, and even then the
// aggregate stays PARTIALLY_OBSERVED.
func InferredEvidence(rule, path string) Evidence {
	return Evidence{
		State:      StateInferred,
		Confidence: confidencePathRule,
		Refs:       []EvidenceRef{{Kind: KindPathConvention, Path: path, Rule: rule}},
	}
}

// UnknownEvidence builds an explicit gap. It is the only correct answer when a
// check could not run.
func UnknownEvidence(reason string) Evidence {
	return Evidence{State: StateUnknown, Confidence: 0, Reason: reason}
}

// CombineEvidence is the conjunction operator.
//
// The result is the weakest link, both in state and confidence, and the
// confidence is capped below any single strong mechanism when more than one
// witness is involved: agreement between independent observations is not
// stronger than the weaker observation. UNKNOWN anywhere makes the whole claim
// UNKNOWN, which is what keeps a missing check from reading as a negative fact.
func CombineEvidence(parts ...Evidence) Evidence {
	if len(parts) == 0 {
		return UnknownEvidence("no evidence supplied")
	}
	result := parts[0]
	distinct := map[string]bool{}
	for _, part := range parts[1:] {
		result.State = evidence.Weakest(result.State, part.State)
		if part.Confidence < result.Confidence {
			result.Confidence = part.Confidence
		}
		if part.Reason != "" && result.Reason == "" {
			result.Reason = part.Reason
		}
	}
	for _, part := range parts {
		for _, ref := range part.Refs {
			if ref.Path != "" {
				distinct[ref.Path] = true
			}
		}
	}
	if len(distinct) > 1 && result.Confidence > confidenceAggregateCap {
		result.Confidence = confidenceAggregateCap
	}
	result.Refs = boundedRefs(result.Refs, 4)
	return result
}

// AggregateContainment is the aggregation operator for a container claim
// ("this module contains 12 files, 3 unparsed").
//
// It differs from conjunction deliberately: one unobserved child does not make
// the container UNKNOWN, because the container is still observed. It makes it
// PARTIALLY_OBSERVED, which is exactly the claim a consumer must qualify. A
// child whose state is INFERRED demotes the aggregate the same way; a container
// is not more certain than its members.
func AggregateContainment(parts ...Evidence) Evidence {
	if len(parts) == 0 {
		return UnknownEvidence("container has no observed members")
	}
	result := parts[0]
	weakest := parts[0].State
	degraded := parts[0].State != StateVerified && parts[0].State != StateObserved
	for _, part := range parts[1:] {
		if part.State.Rank() < weakest.Rank() {
			weakest = part.State
		}
		if part.State != StateVerified && part.State != StateObserved {
			degraded = true
		}
		if part.Confidence < result.Confidence {
			result.Confidence = part.Confidence
		}
		if part.Reason != "" && result.Reason == "" {
			result.Reason = part.Reason
		}
		result.Refs = append(result.Refs, part.Refs...)
	}
	switch {
	case weakest.Rank() >= StateObserved.Rank():
		result.State = weakest
	case degraded && weakest.Rank() >= StateInferred.Rank():
		result.State = StatePartiallyObserved
	default:
		result.State = weakest
	}
	if result.State != StateVerified && result.Confidence > confidenceAggregateCap {
		result.Confidence = confidenceAggregateCap
	}
	result.Refs = boundedRefs(result.Refs, 4)
	return result
}

// boundedRefs keeps a deterministic, small witness set. Evidence is for
// citation, not for reproducing the snapshot.
func boundedRefs(refs []EvidenceRef, limit int) []EvidenceRef {
	if len(refs) <= limit {
		return refs
	}
	out := append([]EvidenceRef(nil), refs[:limit]...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}
