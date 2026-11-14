// Package evidence is the unified evidence vocabulary and correlation engine.
//
// Providers observe; this package preserves what they observed. It never
// upgrades one provider's observation into another's, never flattens two
// domains into one confidence number, and never lets a ranking score stand in
// for proof. A VERIFIED record requires an explicit proof constructor, so a
// provider that merely returns successfully cannot claim verification.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// State is how strongly one claim is supported.
//
// The states are ordered and not interchangeable. A consumer must never
// upgrade one into another: an OBSERVED containment is not a VERIFIED
// implementation, and an INFERRED relationship is not observed.
type State string

// Evidence states, weakest to strongest.
const (
	// Unknown means the claim could not be checked at all.
	Unknown State = "UNKNOWN"
	// Inferred means a convention or name suggests the claim.
	Inferred State = "INFERRED"
	// PartiallyObserved means part of the claim is observed and part is not.
	PartiallyObserved State = "PARTIALLY_OBSERVED"
	// Observed means the provider directly observed the fact.
	Observed State = "OBSERVED"
	// Verified means an explicit proof establishes the fact. It cannot be set
	// by building a Record literal; only Prove can produce it.
	Verified State = "VERIFIED"
)

// Rank orders states by strength.
func (s State) Rank() int {
	switch s {
	case Verified:
		return 4
	case Observed:
		return 3
	case PartiallyObserved:
		return 2
	case Inferred:
		return 1
	default:
		return 0
	}
}

// Weakest returns the least-supported state. An empty set is Unknown, which is
// the conservative answer.
func Weakest(states ...State) State {
	if len(states) == 0 {
		return Unknown
	}
	weakest := states[0]
	for _, state := range states[1:] {
		if state.Rank() < weakest.Rank() {
			weakest = state
		}
	}
	return weakest
}

// Source names the observation mechanism.
type Source string

// Evidence sources.
const (
	SourceFilesystem Source = "FILESYSTEM"
	SourceManifest   Source = "MANIFEST"
	SourceAST        Source = "AST"
	SourceGraph      Source = "GRAPH"
	SourceBM25       Source = "BM25"
	SourceLSP        Source = "LSP"
	SourceDAP        Source = "DAP"
	SourceTest       Source = "TEST"
	SourceBuild      Source = "BUILD"
	SourceCommand    Source = "COMMAND"
)

// Provider names the intelligence capability that produced a record.
type Provider string

// Known providers. A new capability may use another identity; the field is not
// a closed switch.
const (
	ProviderStructural Provider = "structural"
	ProviderRetrieval  Provider = "retrieval"
	ProviderSemantic   Provider = "semantic"
	ProviderRuntime    Provider = "runtime"
)

// Kind classifies what the record is about.
type Kind string

// Evidence kinds.
const (
	KindObservation  Kind = "observation"
	KindRelationship Kind = "relationship"
	KindDiagnostic   Kind = "diagnostic"
	KindRuntime      Kind = "runtime"
	KindRetrieval    Kind = "retrieval"
)

// Freshness is how current an observation is relative to the consumer's
// snapshot. It is not confidence: a fresh observation can still be weak.
type Freshness string

// Freshness values.
const (
	FreshnessCurrent Freshness = "CURRENT"
	FreshnessStale   Freshness = "STALE"
	FreshnessUnknown Freshness = "UNKNOWN"
)

// Ref is one provenance coordinate.
type Ref struct {
	// Path is repository-relative when the provider can express it.
	Path string
	// URI is the provider's own locator when it has one (file://, lsp).
	URI string
	// Line and Column are 1-based when known; zero means unset.
	Line   int
	Column int
}

// Scope is the domain an observation applies to.
type Scope struct {
	Workspace string
	Path      string
}

// Record is one preserved observation.
type Record struct {
	// ID is stable and content-derived when not supplied.
	ID       string
	Kind     Kind
	Provider Provider
	Source   Source
	// Subject is the entity observed; Relation names the assertion; Object is
	// the related entity for relationship records.
	Subject  string
	Relation string
	Object   string
	State    State
	// Confidence is the provider's confidence in the observation itself. It is
	// not a relevance score and not a probability that the code is correct.
	Confidence float64
	Freshness  Freshness
	Scope      Scope
	Refs       []Ref
	SnapshotID string
	ObservedAt time.Time
	Note       string
	proof      bool
}

// Errors returned by the engine.
var (
	// ErrMissingSubject reports a record that observes nothing.
	ErrMissingSubject = errors.New("evidence: record has no subject")
	// ErrMissingProvider reports a record with no producing capability.
	ErrMissingProvider = errors.New("evidence: record has no provider")
	// ErrUnverified reports a hand-built VERIFIED state. Verification needs an
	// explicit proof, not a string.
	ErrUnverified = errors.New("evidence: VERIFIED requires the proof constructor")
)

// Prove returns a verified copy of record.
//
// This is the only path to State Verified. It is a deliberate speed bump: a
// provider calling it has to name verification in code, and a consumer can
// still see the source that established it.
func Prove(record Record) Record {
	record.State = Verified
	record.proof = true
	return record
}

// Engine stores and correlates records. It is safe for concurrent use.
type Engine struct {
	mu      sync.Mutex
	records []Record
	byID    map[string]int
}

// New returns an empty engine.
func New() *Engine {
	return &Engine{byID: make(map[string]int)}
}

// Add validates and stores one record, returning the stored copy.
//
// An identical record (same deterministic identity) is deduplicated rather than
// duplicated, so two providers reporting the same fact do not inflate counts.
// Records from different sources remain distinct even when they agree.
func (e *Engine) Add(record Record) (Record, error) {
	if strings.TrimSpace(record.Subject) == "" {
		return Record{}, ErrMissingSubject
	}
	if strings.TrimSpace(string(record.Provider)) == "" {
		return Record{}, ErrMissingProvider
	}
	if record.State == Verified && !record.proof {
		return Record{}, ErrUnverified
	}
	if record.Kind == "" {
		record.Kind = KindObservation
	}
	if record.Freshness == "" {
		record.Freshness = FreshnessUnknown
	}
	record.Confidence = clampConfidence(record.Confidence)
	record.Refs = append([]Ref(nil), record.Refs...)
	if len(record.Refs) > 32 {
		record.Refs = record.Refs[:32]
	}
	if record.ObservedAt.IsZero() {
		record.ObservedAt = time.Now().UTC()
	}
	if record.ID == "" {
		record.ID = identity(record)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if index, ok := e.byID[record.ID]; ok {
		return e.records[index], nil
	}
	e.byID[record.ID] = len(e.records)
	e.records = append(e.records, record)
	return record, nil
}

// Len reports the number of distinct records.
func (e *Engine) Len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.records)
}

// Records returns a copy in insertion order.
func (e *Engine) Records() []Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Record(nil), e.records...)
}

// Claim is one subject/relation/object assertion with all supporting records.
type Claim struct {
	Subject  string
	Relation string
	Object   string
	Records  []Record
	// State is the weakest supporting state, never stronger than any record.
	State State
	// Confidence is the minimum supporting confidence.
	Confidence float64
	// Sources are the distinct observation mechanisms, sorted.
	Sources []Source
	// Providers are the distinct producing capabilities, sorted.
	Providers []Provider
}

// Contradiction is one subject/relation asserted with different objects.
type Contradiction struct {
	Subject  string
	Relation string
	Objects  []string
	Records  []Record
	Note     string
}

// Correlation is the grouped view of every stored record.
type Correlation struct {
	Claims         []Claim
	Contradictions []Contradiction
}

// Correlate groups records by claim and reports contradictions.
//
// Conflicting observations are preserved, not resolved: the note names the
// conditions a consumer must check (snapshot freshness, build configuration,
// conditional compilation, dynamic dispatch) before choosing one.
func (e *Engine) Correlate() Correlation {
	records := e.Records()
	type key struct{ subject, relation string }
	grouped := make(map[key][]Record, len(records))
	for _, record := range records {
		grouped[key{record.Subject, record.Relation}] = append(grouped[key{record.Subject, record.Relation}], record)
	}
	keys := make([]key, 0, len(grouped))
	for item := range grouped {
		keys = append(keys, item)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].subject != keys[j].subject {
			return keys[i].subject < keys[j].subject
		}
		return keys[i].relation < keys[j].relation
	})

	correlation := Correlation{}
	for _, item := range keys {
		group := grouped[item]
		objects := make(map[string][]Record, 2)
		for _, record := range group {
			object := record.Object
			if object == "" {
				object = record.Subject
			}
			objects[object] = append(objects[object], record)
		}
		objectNames := make([]string, 0, len(objects))
		for object := range objects {
			objectNames = append(objectNames, object)
		}
		sort.Strings(objectNames)
		if len(objectNames) > 1 {
			sort.SliceStable(group, func(i, j int) bool {
				if group[i].Source != group[j].Source {
					return group[i].Source < group[j].Source
				}
				return group[i].Object < group[j].Object
			})
			correlation.Contradictions = append(correlation.Contradictions, Contradiction{
				Subject:  item.subject,
				Relation: item.relation,
				Objects:  objectNames,
				Records:  append([]Record(nil), group...),
				Note: "observations disagree; check snapshot freshness, build configuration, " +
					"conditional compilation, generated code, and dynamic dispatch before concluding",
			})
		}
		for _, object := range objectNames {
			correlation.Claims = append(correlation.Claims, buildClaim(item.subject, item.relation, object, objects[object]))
		}
	}
	sort.SliceStable(correlation.Contradictions, func(i, j int) bool {
		if correlation.Contradictions[i].Subject != correlation.Contradictions[j].Subject {
			return correlation.Contradictions[i].Subject < correlation.Contradictions[j].Subject
		}
		return correlation.Contradictions[i].Relation < correlation.Contradictions[j].Relation
	})
	return correlation
}

func buildClaim(subject, relation, object string, records []Record) Claim {
	states := make([]State, 0, len(records))
	confidence := 1.0
	sources := make(map[Source]struct{}, len(records))
	providers := make(map[Provider]struct{}, len(records))
	for index, record := range records {
		states = append(states, record.State)
		if record.Confidence < confidence {
			confidence = record.Confidence
		}
		sources[record.Source] = struct{}{}
		providers[record.Provider] = struct{}{}
		if index == 0 {
			confidence = record.Confidence
		}
	}
	if len(records) == 0 {
		confidence = 0
	}
	claim := Claim{
		Subject: subject, Relation: relation, Object: object,
		Records:    append([]Record(nil), records...),
		State:      Weakest(states...),
		Confidence: confidence,
	}
	for source := range sources {
		claim.Sources = append(claim.Sources, source)
	}
	for provider := range providers {
		claim.Providers = append(claim.Providers, provider)
	}
	sort.Slice(claim.Sources, func(i, j int) bool { return claim.Sources[i] < claim.Sources[j] })
	sort.Slice(claim.Providers, func(i, j int) bool { return claim.Providers[i] < claim.Providers[j] })
	return claim
}

func clampConfidence(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// identity hashes the stable fields that make two observations the same one.
func identity(record Record) string {
	parts := []string{
		string(record.Provider), string(record.Source), string(record.Kind),
		record.Subject, record.Relation, record.Object, record.SnapshotID,
	}
	for _, ref := range record.Refs {
		parts = append(parts, ref.Path, ref.URI, fmt.Sprintf("%d:%d", ref.Line, ref.Column))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}
