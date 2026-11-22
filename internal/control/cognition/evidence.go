package cognition

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

// EvidenceStatus is what the runtime currently knows about one fact.
type EvidenceStatus string

// Evidence statuses.
const (
	// StatusKnown means the fact was observed directly.
	StatusKnown EvidenceStatus = "KNOWN"
	// StatusUnknown means the fact is not known yet.
	StatusUnknown EvidenceStatus = "UNKNOWN"
	// StatusConflicting means two observations disagree.
	StatusConflicting EvidenceStatus = "CONFLICTING"
	// StatusDerived means the fact was inferred from other evidence.
	StatusDerived EvidenceStatus = "DERIVED"
	// StatusRequired means the fact is needed but has not been acquired.
	StatusRequired EvidenceStatus = "REQUIRED"
)

// Retention is how much of an observation must survive projection.
//
// The vocabulary is deliberately small and semantic. A diagnostic and a bug
// window are retained exactly; a directory listing or a search hit is
// structural; a large unrelated file read is eligible for a bounded excerpt;
// external content keeps its provenance and untrusted envelope; transient
// exploration output may be compacted to a line.
type Retention string

// Retention classes.
const (
	RetentionExact      Retention = "exact"
	RetentionStructural Retention = "structural"
	RetentionDerived    Retention = "derived"
	RetentionDiagnostic Retention = "diagnostic"
	RetentionExternal   Retention = "external"
	RetentionTransient  Retention = "transient"
)

// Provenance is what a repository observation actually observes.
//
// It keeps a README's description of the project distinct from a source read:
// both can be exact content, but only the source read establishes what the
// implementation does. The classification is derived from the path and the
// producing capability, never from model text.
type Provenance string

// Provenance classes.
const (
	// ProvenanceObservation is source code or a structural query: evidence of
	// what the implementation contains.
	ProvenanceObservation Provenance = "observation"
	// ProvenanceConfiguration is declared runtime or deployment configuration:
	// it states intended topology, not observed execution.
	ProvenanceConfiguration Provenance = "configuration"
	// ProvenanceDocumentation is README, docs, or another prose artifact: it
	// is evidence of what the project says about itself.
	ProvenanceDocumentation Provenance = "documentation"
	// ProvenanceManifest is a module or package manifest. It states dependency
	// or language facts, not architecture.
	ProvenanceManifest Provenance = "manifest"
	// ProvenanceGenerated is vendored or generated content. It is retained as
	// evidence but does not prove the project's own implementation.
	ProvenanceGenerated Provenance = "generated"
	// ProvenanceExternal is untrusted content from outside the workspace.
	ProvenanceExternal Provenance = "external"
	// ProvenanceDiagnostic is an error, check, or command result: a control
	// observation rather than a description of the implementation.
	ProvenanceDiagnostic Provenance = "diagnostic"
)

// Evidence bounds.
const (
	// MaxEvidenceValueBytes bounds one retained payload.
	MaxEvidenceValueBytes = 16 * 1024
	// MaxDiagnosticValueBytes bounds an exact error payload.
	MaxDiagnosticValueBytes = 8 * 1024
	// MaxEvidenceSummaryBytes bounds the model-facing one-line summary.
	MaxEvidenceSummaryBytes = 400
	// MaxEvidenceItems bounds the in-memory and persisted state. Trimming
	// removes low-value transient evidence first; conflicting, content-bearing,
	// and external observations are retained longest because they satisfy the
	// request's declared needs. Diagnostic command logs are retained only while
	// the bound allows.
	MaxEvidenceItems = 256
	// MaxEvidenceBytes bounds the retained payload total, so a checkpoint
	// cannot grow into a second transcript. Values are bounded per item and
	// the lowest-value items are evicted until the total fits.
	MaxEvidenceBytes = 64 * 1024
	// EvidenceVersion is the persisted schema version. Version 2 added
	// optional snapshot provenance to Source; version 3 adds language,
	// adapter, resolution, completeness, and truncation; version 4 adds
	// project-scope completeness; version 5 adds bounded structural counts
	// for claim support; version 6 adds inventory completeness, scope gaps,
	// and the producing structural view; version 7 adds a conclusion's
	// explicit unresolved code coordinates; version 8 adds structural evidence
	// facets. Older records stay readable because every new field is optional
	// and additive.
	EvidenceVersion = 8
)

// Source records where one observation came from.
type Source struct {
	// Tool is the tool that produced it.
	Tool string `json:"tool,omitempty"`
	// CallID is the tool call identity, for replay and audit.
	CallID string `json:"callId,omitempty"`
	// Path is a repository-relative file or directory, when one applies.
	Path string `json:"path,omitempty"`
	// StartLine and EndLine bound an exact window.
	StartLine int `json:"startLine,omitempty"`
	EndLine   int `json:"endLine,omitempty"`
	// URL, Provider, and Trust describe external evidence.
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider,omitempty"`
	Trust    string `json:"trust,omitempty"`
	// Check names a verification kind, when the observation is a check.
	Check string `json:"check,omitempty"`
	// Scope is the repository-relative coverage a check reported, "*" for
	// every package. It lets the durable completion boundary verify that a
	// check covers what the contract asked for without re-deriving it.
	Scope string `json:"scope,omitempty"`
	// SnapshotID, GraphState, and Generation bind a structural observation to
	// the published snapshot that produced it. They are additive: a legacy
	// item without them is not treated as current.
	SnapshotID string `json:"snapshotId,omitempty"`
	GraphState string `json:"graphState,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	// Language, Adapter, and Resolution identify the syntax provenance of a
	// structural coordinate. They are empty for non-structural observations.
	Language   string `json:"language,omitempty"`
	Adapter    string `json:"adapter,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	// Completeness is COMPLETE, PARTIAL, UNPARSED, or UNSUPPORTED. It is
	// independent of GraphState: a CURRENT snapshot can be structurally
	// partial.
	Completeness string `json:"completeness,omitempty"`
	// ScopeCompleteness is COMPLETE or PARTIAL for a structural snapshot's
	// project scope. Empty means the observation predates scope provenance or
	// is not structural.
	ScopeCompleteness string `json:"scopeCompleteness,omitempty"`
	// Truncated reports that the producing query hit its own bound.
	Truncated bool `json:"truncated,omitempty"`
	// InventoryCompleteness is COMPLETE or PARTIAL for a structural
	// snapshot's file inventory. It is independent of scope and structural
	// completeness: a complete file list can still contain unparsed source.
	InventoryCompleteness string `json:"inventoryCompleteness,omitempty"`
	// ExcludedRoots and ExcludedSourceFiles are the scope gaps a structural
	// observation disclosed. A nonzero count means an inventory is not
	// repository-wide even when its own result is untruncated.
	ExcludedRoots       int `json:"excludedRoots,omitempty"`
	ExcludedSourceFiles int `json:"excludedSourceFiles,omitempty"`
	// UnsupportedSources counts files whose language has no adapter. They are
	// inventoried but their content is not observed structurally.
	UnsupportedSources int `json:"unsupportedSources,omitempty"`
}

// EvidenceItem is one durable fact.
type EvidenceItem struct {
	// ID is a stable identity, e.g. "file:README.md".
	ID string `json:"id"`
	// Status is what the runtime knows.
	Status EvidenceStatus `json:"status"`
	// Retention is how the evidence survives projection.
	Retention Retention `json:"retention"`
	// Provenance is what the observation actually observes: implementation,
	// documentation, manifest, generated/vendored content, external content,
	// or a diagnostic.
	Provenance Provenance `json:"provenance,omitempty"`
	// Summary is a bounded model-facing description.
	Summary string `json:"summary"`
	// Value is the bounded retained payload. It is not sent whole by default;
	// projection chooses a view.
	Value string `json:"value,omitempty"`
	// Digest identifies the observed payload without duplicating it.
	Digest string `json:"digest,omitempty"`
	// Source records provenance and coordinates.
	Source Source `json:"source,omitempty"`
	// Counts retains the bounded numeric facts a structural result reported,
	// such as test_files and total. They are additive and optional; a claim
	// boundary must not have to parse a rendered line to know whether a
	// complete inventory observed zero test files.
	Counts map[string]int `json:"counts,omitempty"`
	// Complete reports that a listing or search result was not truncated.
	Complete bool `json:"complete,omitempty"`
	// Seq is the observation order, used to detect stale file state.
	Seq int `json:"seq,omitempty"`
	// Conflict explains a status of CONFLICTING.
	Conflict string `json:"conflict,omitempty"`
	// Executed reports that the observation came from a process that ran to a
	// successful terminal state. It is what separates a successful command,
	// which can support an execution objective, from a failed one, which is a
	// diagnostic. It is additive and optional: a legacy item without it is
	// treated as unknown, never as success.
	Executed bool `json:"executed,omitempty"`
	// Runtime reports that the observation is a live project runtime: a
	// service reached readiness, a check ran against it while it was alive,
	// and it did not exit. It is a stronger observation than Executed and is
	// the only one a runtime execution objective accepts. Additive and
	// optional: legacy items are not runtime observations.
	Runtime bool `json:"runtime,omitempty"`
	// View names the producing structural query (overview, files, tests, …)
	// when one applies. It lets the claim projection distinguish an inventory
	// from a targeted source observation without parsing the rendered line.
	View string `json:"view,omitempty"`
	// Facets names the evidence facets this observation establishes. It is
	// written from structured result fields at ingestion, never inferred from
	// the model's prose. Additive and optional: a legacy item without it uses
	// the facet fallbacks in facets.go.
	Facets []EvidenceFacet `json:"facets,omitempty"`
	// Unresolved names the concrete code coordinates a conclusion still
	// depends on. A conclusion is recorded for audit even when one of these is
	// unresolved, but it is not admissible until the runtime observes the
	// coordinate or a bounded attempt classifies the conclusion as UNRESOLVED.
	Unresolved []string `json:"unresolved,omitempty"`
}

// EvidenceClass is the claim-facing class of one observation.
//
// It is derived from the existing provenance, identity, and payload; it is not
// a second persisted model. The distinctions are load-bearing:
//
//	DOCUMENTATION   what the project says about itself
//	CONFIGURATION   declared topology or deployment
//	MANIFEST        declared modules and dependencies
//	INVENTORY       files, directories, or structural entities exist
//	SOURCE_PATH     a source coordinate exists; its content was not observed
//	SOURCE_CONTENT  source content was observed
//	SOURCE_IMPORT   observed source imports a dependency
//	SOURCE_USE      observed source performs an operation with a dependency
//	RUNTIME         behavior was observed while the project ran
//	STRUCTURAL      a structural relationship or declaration was observed
//	DIAGNOSTIC      the observation is a control or failure signal
//
// SOURCE_PATH and INVENTORY never support an implementation statement;
// SOURCE_IMPORT is not SOURCE_USE; and STRUCTURAL is not source content.
type EvidenceClass string

// Evidence classes.
const (
	EvidenceDocumentation EvidenceClass = "DOCUMENTATION"
	EvidenceConfiguration EvidenceClass = "CONFIGURATION"
	EvidenceManifest      EvidenceClass = "MANIFEST"
	EvidenceInventory     EvidenceClass = "INVENTORY"
	EvidenceSourcePath    EvidenceClass = "SOURCE_PATH"
	EvidenceSourceContent EvidenceClass = "SOURCE_CONTENT"
	EvidenceSourceImport  EvidenceClass = "SOURCE_IMPORT"
	EvidenceSourceUse     EvidenceClass = "SOURCE_USE"
	EvidenceRuntime       EvidenceClass = "RUNTIME"
	EvidenceStructural    EvidenceClass = "STRUCTURAL"
	EvidenceDiagnostic    EvidenceClass = "DIAGNOSTIC"
	EvidenceExternal      EvidenceClass = "EXTERNAL"
	EvidenceGenerated     EvidenceClass = "GENERATED"
)

// EvidenceClass derives the claim-facing class of an observation.
func (i EvidenceItem) EvidenceClass() EvidenceClass {
	if i.Status == StatusConflicting || i.Status == StatusUnknown || i.Status == StatusRequired {
		return EvidenceDiagnostic
	}
	if i.Runtime {
		return EvidenceRuntime
	}
	switch i.Provenance {
	case ProvenanceDocumentation:
		return EvidenceDocumentation
	case ProvenanceConfiguration:
		return EvidenceConfiguration
	case ProvenanceManifest:
		return EvidenceManifest
	case ProvenanceExternal:
		return EvidenceExternal
	case ProvenanceGenerated:
		return EvidenceGenerated
	case ProvenanceDiagnostic:
		return EvidenceDiagnostic
	}
	switch {
	case strings.HasPrefix(i.ID, "dir:"):
		return EvidenceInventory
	case strings.HasPrefix(i.ID, "search:glob:"):
		if len(globPaths(i.Value)) > 0 {
			return EvidenceSourcePath
		}
		return EvidenceDiagnostic
	case strings.HasPrefix(i.ID, "search:grep:"):
		if strings.TrimSpace(i.Value) == "" {
			return EvidenceDiagnostic
		}
		if matches, known := i.Counts["matches"]; (known && matches == 0) ||
			searchResultHasNoMatches(i.Value) {
			// A no-match search is not observed source content. Its text is a
			// diagnostic notice, not a matched line from the component. The
			// text check keeps a legacy checkpoint without Counts honest.
			return EvidenceDiagnostic
		}
		if !sourcePathIsImplementation(i.Source.Path) {
			return EvidenceStructural
		}
		return sourceContentClass(i.Value)
	case i.View != "" && strings.TrimSpace(i.Value) == "":
		// A structural row is a coordinate, not source content.
		return EvidenceStructural
	case strings.HasPrefix(i.ID, "file:"):
		if strings.TrimSpace(i.Value) == "" {
			return EvidenceSourcePath
		}
		return sourceContentClass(i.Value)
	case strings.HasPrefix(i.ID, "command:"):
		return EvidenceDiagnostic
	case strings.HasPrefix(i.ID, "verification:"):
		return EvidenceDiagnostic
	case strings.HasPrefix(i.ID, "external:"):
		return EvidenceExternal
	case strings.HasPrefix(i.ID, "error:"):
		return EvidenceDiagnostic
	default:
		return EvidenceStructural
	}
}

// searchResultHasNoMatches reports the producer's no-result notice. It is a
// bounded text check for legacy evidence items that predate Counts.
func searchResultHasNoMatches(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"no matches", "no files matched", "0 matches", "0 files",
		"no results", "nothing matched",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// sourcePathIsImplementation duplicates the path classification used by
// isImplementationObservation, keeping one decision in agent.ClassifyPath.
func sourcePathIsImplementation(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || path == "./" {
		return false
	}
	switch agent.ClassifyPath(path) {
	case agent.PathDocumentation, agent.PathManifest, agent.PathConfiguration, agent.PathGenerated:
		return false
	default:
		return true
	}
}

// sourceContentClass distinguishes an import declaration from an actual
// dependency operation. A read that only imports a package is not use.
func sourceContentClass(text string) EvidenceClass {
	lower := strings.ToLower(text)
	if dependencyImportMarker(lower) {
		if dependencyUseMarker(lower) {
			return EvidenceSourceUse
		}
		return EvidenceSourceImport
	}
	// An operation-shaped call without an import/initialization is ordinary
	// source content. Calling os.Open is not evidence that the project uses an
	// external dependency.
	return EvidenceSourceContent
}

// dependencyImportMarker reports an import/require statement. The vocabulary
// is finite and language-shaped; it does not decide whether a dependency is
// used.
func dependencyImportMarker(text string) bool {
	for _, marker := range []string{
		"\nimport ", "\nimport(", "\nimport (", "\nfrom ", "\nrequire(",
		"\nrequire \"", "#include", "\nuse ", "\nusing ", "import \"",
		"import '", "from \"", "from '", "require(\"", "require('",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// dependencyUseMarker reports a concrete operation shape in source. It is a
// bounded signal for the state projection; the claim audit binds it to a
// dependency and component before accepting a claim.
func dependencyUseMarker(text string) bool {
	for _, marker := range []string{
		".set(", ".get(", ".hset(", ".hget(", ".lpush(", ".rpush(",
		".publish(", ".subscribe(", ".consume(", ".produce(", ".send(",
		".query(", ".queryrow(", ".exec(", ".insert(", ".update(",
		".delete(", ".find(", ".sign(", ".verify(", ".parse(", ".open(",
		".dial(", ".connect(", "newclient(", "amqp.dial(", "jwt.new(",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Requirement is one evidence need declared by a task shape.
type Requirement struct {
	// Key is the stable identity the evidence must satisfy.
	Key string `json:"key"`
	// Reason explains why it matters.
	Reason string `json:"reason,omitempty"`
}

// EvidenceState is the durable knowledge the runtime has acquired.
//
// It is a projection of observations, never a transcript. Two tool calls that
// observe the same fact collapse to one item; a later observation of the same
// path either updates it (after a recorded mutation) or marks it conflicting
// (a change the runtime did not perform).
type EvidenceState struct {
	// Version is the persisted schema version.
	Version int `json:"version"`
	// Items are the facts, in observation order.
	Items []EvidenceItem `json:"items,omitempty"`
	// Requirements are the needs the task shape declared.
	Requirements []Requirement `json:"requirements,omitempty"`
	// Seq is the last observation sequence.
	Seq int `json:"seq,omitempty"`
	// ObservedCalls prevents replaying an already-observed tool outcome when a
	// projector is called more than once for the same turn.
	ObservedCalls map[string]bool `json:"observedCalls,omitempty"`
	// LastMutationSeq records the observation order of the last recorded
	// mutation per path, so a later differing read is not mistaken for a
	// conflict when the runtime itself changed the file.
	LastMutationSeq map[string]int `json:"lastMutationSeq,omitempty"`
	// Failures is the durable capability-failure ledger. It is separate from
	// Items because a failure is not a repository fact: it is a control
	// observation that decides whether the same proposal may execute again.
	Failures []FailureRecord `json:"failures,omitempty"`
	// Facts is the bounded workspace probe the requirements were derived from.
	// It lets sufficiency distinguish a documentation-only project from a code
	// project whose broad account still lacks an implementation observation.
	Facts RepositoryFacts `json:"facts,omitempty"`
	// conclusionMode is the terminal-outcome vocabulary of the current shape.
	// It is derived state, not a durable fact, so it is rebuilt by
	// ensureRequirements on every resume and is never serialized.
	conclusionMode contract.ConclusionMode
}

// NewEvidenceState returns an empty state.
func NewEvidenceState() *EvidenceState {
	return &EvidenceState{
		Version:         EvidenceVersion,
		ObservedCalls:   map[string]bool{},
		LastMutationSeq: map[string]int{},
	}
}

// ResetRequirements drops the declared needs because the completion contract
// changed.
//
// A task that is re-scoped by an approved transition keeps its observations —
// they are facts about the repository and remain true — but the previous
// objective's needs no longer describe what would satisfy this one, and holding
// them would report a permanent evidence gap for an objective nobody asked for.
func (s *EvidenceState) ResetRequirements() {
	if s == nil {
		return
	}
	s.Requirements = nil
}

// Clone returns a deep copy, so a checkpoint cannot alias the live state.
func (s *EvidenceState) Clone() *EvidenceState {
	if s == nil {
		return NewEvidenceState()
	}
	out := *s
	out.Items = make([]EvidenceItem, len(s.Items))
	copy(out.Items, s.Items)
	for i := range out.Items {
		if len(out.Items[i].Counts) == 0 {
			continue
		}
		counts := make(map[string]int, len(out.Items[i].Counts))
		for key, value := range out.Items[i].Counts {
			counts[key] = value
		}
		out.Items[i].Counts = counts
	}
	for i := range out.Items {
		out.Items[i].Facets = append([]EvidenceFacet(nil), out.Items[i].Facets...)
	}
	out.Requirements = append([]Requirement(nil), s.Requirements...)
	out.ObservedCalls = make(map[string]bool, len(s.ObservedCalls))
	for key, value := range s.ObservedCalls {
		out.ObservedCalls[key] = value
	}
	out.LastMutationSeq = make(map[string]int, len(s.LastMutationSeq))
	for key, value := range s.LastMutationSeq {
		out.LastMutationSeq[key] = value
	}
	out.Failures = append([]FailureRecord(nil), s.Failures...)
	return &out
}

// Empty reports whether the state has no observed facts.
func (s *EvidenceState) Empty() bool { return s == nil || len(s.Items) == 0 }

// Find returns the item with the given id.
func (s *EvidenceState) Find(id string) (EvidenceItem, bool) {
	if s == nil {
		return EvidenceItem{}, false
	}
	for _, item := range s.Items {
		if item.ID == id {
			return item, true
		}
	}
	return EvidenceItem{}, false
}

// Conflicts returns the items whose observations disagree.
func (s *EvidenceState) Conflicts() []EvidenceItem {
	if s == nil {
		return nil
	}
	var out []EvidenceItem
	for _, item := range s.Items {
		if item.Status == StatusConflicting {
			out = append(out, item)
		}
	}
	return out
}

// HasExternalObservation reports whether a successful external observation was
// recorded.
//
// Discovery is not an observation: it names capabilities without acquiring
// knowledge. An item is external evidence only when it carries the external
// provenance and a payload or summary to support a claim.
func (s *EvidenceState) HasExternalObservation() bool {
	if s == nil {
		return false
	}
	for _, item := range s.Items {
		if item.Provenance != ProvenanceExternal {
			continue
		}
		if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
			continue
		}
		if strings.TrimSpace(item.Value) != "" || strings.TrimSpace(item.Summary) != "" {
			return true
		}
	}
	return false
}

// Missing returns the declared requirements the state cannot satisfy.
func (s *EvidenceState) Missing(execution ExecutionState) []Requirement {
	var out []Requirement
	for _, requirement := range s.Requirements {
		if s.Satisfied(requirement.Key, execution) {
			continue
		}
		out = append(out, requirement)
	}
	return out
}

// Satisfied reports whether one requirement key has adequate evidence.
func (s *EvidenceState) Satisfied(key string, execution ExecutionState) bool {
	if s == nil {
		return false
	}
	switch {
	case strings.HasPrefix(key, "target_state:"):
		path := strings.TrimPrefix(key, "target_state:")
		if path == "answer" {
			return s.hasAnyExact()
		}
		return s.targetStateKnown(path)
	case strings.HasPrefix(key, "target_content:"):
		path := cleanPath(strings.TrimPrefix(key, "target_content:"))
		if path == "" {
			return false
		}
		if item, ok := s.targetFile(path); ok && item.Status != StatusConflicting &&
			item.Status != StatusRequired && item.Status != StatusUnknown &&
			strings.TrimSpace(item.Value) != "" {
			return true
		}
		// A directory's content is its listing. An explicitly directory-scoped
		// target ("inspect backend/") cannot be satisfied by a file read, and
		// a truncated listing cannot prove the directory's contents.
		if item, ok := s.Find("dir:" + path); ok && item.Complete &&
			item.Status != StatusConflicting && item.Status != StatusRequired &&
			item.Status != StatusUnknown {
			return true
		}
		// A structural query naming the exact path is content-bearing
		// coordinate evidence for a question about a declaration or package.
		for _, item := range s.Items {
			if item.Source.Path != path {
				continue
			}
			if strings.HasPrefix(item.ID, "symbol:") || strings.HasPrefix(item.ID, "symbols:") {
				return item.Status != StatusConflicting && item.Status != StatusRequired && item.Status != StatusUnknown
			}
		}
		return false
	case strings.HasPrefix(key, "target_coordinates:"):
		value := strings.TrimPrefix(key, "target_coordinates:")
		return s.containsEvidence("symbol", value)
	case key == "repository_overview":
		return s.hasRepositoryOverview()
	case key == "repository_survey":
		return s.hasRepositorySurvey(s.Facts)
	case key == "implementation_observation":
		return s.hasImplementationObservation()
	case key == "investigation_conclusion":
		// Only the latest conclusion is authoritative. An earlier complete
		// conclusion cannot mask a later one that names a new unresolved
		// coordinate; otherwise the admissibility gate is bypassed by history.
		latest, ok := s.latestConclusion()
		return ok && latest.Complete && latest.Status != StatusConflicting &&
			latest.Status != StatusRequired && latest.Status != StatusUnknown &&
			!contract.IsProvisionalConclusion(latest.Value)
	case strings.HasPrefix(key, "investigation_target:"):
		return s.investigationTargetObserved(strings.TrimPrefix(key, "investigation_target:"), execution)
	case key == "vcs_observation":
		return s.hasVCSObservation()
	case key == "external_observation":
		return s.HasExternalObservation()
	case key == "test_inventory":
		return s.ClaimSummary().TestInventory == "COMPLETE"
	case key == "verification":
		return execution.VerifiedAfterMutation || len(execution.Verifications) > 0
	case key == "execution_result":
		return execution.Executed
	default:
		if strings.HasPrefix(key, "facet:") {
			return s.facetSatisfied(EvidenceFacet(strings.TrimPrefix(key, "facet:")))
		}
		for _, item := range s.Items {
			if item.ID == key && item.Status != StatusRequired && item.Status != StatusUnknown {
				return true
			}
		}
		return false
	}
}

// hasVCSObservation reports at least one completed version-control inspection.
// A command the tool did not mark as a read, a refusal, or a failed run is not
// an observation of the repository's state.
func (s *EvidenceState) hasVCSObservation() bool {
	for _, item := range s.Items {
		if !strings.HasPrefix(item.ID, "vcs:") {
			continue
		}
		if item.Status != StatusRequired && item.Status != StatusUnknown && item.Status != StatusConflicting {
			return true
		}
	}
	return false
}

// hasAnyExact reports whether any exact observation exists.
func (s *EvidenceState) hasAnyExact() bool {
	for _, item := range s.Items {
		if item.Retention == RetentionExact && item.Status != StatusRequired && item.Status != StatusUnknown {
			return true
		}
	}
	return false
}

// hasRepositoryOverview reports whether the state has content-bearing
// evidence about the repository.
//
// A directory listing is structure, not an explanation: it tells the model
// which names exist without telling it what any of them do. Treating `ls` as
// enough for a repository overview let a model answer "explain this project"
// from filenames alone. A file read or a search that returned matching
// content is the minimum needed to support a content claim; symbols may
// establish coordinates for a structural answer.
func (s *EvidenceState) hasRepositoryOverview() bool {
	for _, item := range s.Items {
		switch item.Status {
		case StatusConflicting, StatusUnknown, StatusRequired:
			continue
		}
		switch {
		case strings.HasPrefix(item.ID, "file:"):
			// A missing-file observation is target state, not content. It has
			// no retained value, so it cannot answer what the repository does.
			if item.Retention == RetentionExact && strings.TrimSpace(item.Value) != "" {
				return true
			}
		case strings.HasPrefix(item.ID, "search:grep:"), strings.HasPrefix(item.ID, "symbols:"):
			// A search that returned no payload or a structural query with no
			// coordinates observed nothing content-bearing.
			if strings.TrimSpace(item.Value) != "" {
				return true
			}
		case strings.HasPrefix(item.ID, "structure:"), strings.HasPrefix(item.ID, "graph:"),
			strings.HasPrefix(item.ID, "refs:"):
			// A structural edge binds two code endpoints and carries content;
			// an inventory alone does not.
			if itemHasFacet(item, FacetRelationship) || relationshipTextEvidence(item) {
				return true
			}
		}
	}
	return false
}

// hasImplementationObservation reports whether at least one source-content
// observation exists. A targetless mutation needs a concrete implementation
// fact before it can choose an edit; documentation and manifests are not
// enough to place the change.
func (s *EvidenceState) hasImplementationObservation() bool {
	if s == nil {
		return false
	}
	for _, item := range s.Items {
		if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
			continue
		}
		switch {
		case strings.HasPrefix(item.ID, "file:"):
			if item.Retention == RetentionExact && strings.TrimSpace(item.Value) != "" &&
				isImplementationObservation(item) {
				return true
			}
		case strings.HasPrefix(item.ID, "search:grep:"), strings.HasPrefix(item.ID, "symbols:"):
			if strings.TrimSpace(item.Value) != "" && isImplementationObservation(item) {
				return true
			}
		}
	}
	return false
}

// hasRepositorySurvey reports whether the evidence supports a *broad* overview
// of the project rather than one targeted answer.
//
// A specific question ("does this workflow run automatically?") is answered by
// one content observation. "Analyze what this project is about" is not: purpose,
// structure, and stack need at least a second content observation or a
// structural listing. A project that has source code additionally needs at least
// one implementation observation: two documentation artifacts describe what the
// project says about itself, not what its implementation does.
func (s *EvidenceState) hasRepositorySurvey(facts RepositoryFacts) bool {
	content, implementation, structure := 0, 0, 0
	for _, item := range s.Items {
		if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
			continue
		}
		switch {
		case strings.HasPrefix(item.ID, "file:"):
			// A missing-file observation is target-state, not content.
			if item.Retention == RetentionExact && strings.TrimSpace(item.Value) != "" {
				content++
				if isImplementationObservation(item) {
					implementation++
				}
			}
		case strings.HasPrefix(item.ID, "search:grep:"), strings.HasPrefix(item.ID, "symbols:"):
			if strings.TrimSpace(item.Value) != "" {
				content++
				if isImplementationObservation(item) {
					implementation++
				}
			}
		case strings.HasPrefix(item.ID, "dir:"):
			structure++
		}
	}
	if content == 0 {
		return false
	}
	if facts.HasSourceCode() && implementation == 0 {
		return false
	}
	return content >= 2 || structure > 0
}

// containsEvidence reports whether an item kind contains value.
func (s *EvidenceState) containsEvidence(kind, value string) bool {
	for _, item := range s.Items {
		if !strings.HasPrefix(item.ID, kind+":") {
			continue
		}
		if strings.Contains(item.Summary, value) || strings.Contains(item.Value, value) {
			return true
		}
	}
	return false
}

// bareDocumentNames are the extensionless documentation names the request
// extractor treats as files. Only these may resolve to a suffixed document;
// a bare directory target such as "docs" must still be satisfied by its own
// listing.
var bareDocumentNames = map[string]bool{
	"readme": true, "license": true, "changelog": true,
}

// targetFile returns the file observation that answers a named file target.
//
// A bare document name ("README") names the repository's readme, which is
// conventionally README.md. The read observation records the real path, so
// matching only "file:README" would leave the requirement unsatisfied forever
// and keep offering read tools to a model that had already read the document.
// Only a documentation-class file can resolve a bare name; a root document
// wins over one in a subdirectory, and an ambiguous non-root set stays
// unresolved rather than guessed.
func (s *EvidenceState) targetFile(path string) (EvidenceItem, bool) {
	path = cleanPath(path)
	if path == "" {
		return EvidenceItem{}, false
	}
	if item, ok := s.Find("file:" + path); ok {
		return item, true
	}
	if strings.Contains(path, "/") || filepath.Ext(path) != "" {
		return EvidenceItem{}, false
	}
	stem := strings.ToLower(path)
	if !bareDocumentNames[stem] {
		return EvidenceItem{}, false
	}
	var root, nested *EvidenceItem
	nestedCount := 0
	for i := range s.Items {
		item := &s.Items[i]
		if !strings.HasPrefix(item.ID, "file:") {
			continue
		}
		observed := cleanPath(item.Source.Path)
		if observed == "" || agent.ClassifyPath(observed) != agent.PathDocumentation {
			continue
		}
		if documentStem(observed) != stem {
			continue
		}
		if strings.Contains(observed, "/") {
			nested = item
			nestedCount++
			continue
		}
		if root != nil {
			// Two root documents with the same stem is not a name to guess
			// between.
			return EvidenceItem{}, false
		}
		root = item
	}
	if root != nil {
		return *root, true
	}
	if nested != nil && nestedCount == 1 {
		return *nested, true
	}
	return EvidenceItem{}, false
}

// documentStem returns the name a documentation file shares with a bare
// reference: the basename without a documentation suffix.
func documentStem(path string) string {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".md", ".markdown", ".rst", ".txt", ".adoc", ".org":
		return strings.TrimSuffix(base, ext)
	}
	return base
}

// targetStateKnown reports whether a path's existence state is known from a
// read, a complete directory listing, or a runtime mutation.
func (s *EvidenceState) targetStateKnown(path string) bool {
	path = cleanPath(path)
	if path == "" {
		return false
	}
	if item, ok := s.targetFile(path); ok {
		return item.Status == StatusKnown || item.Status == StatusDerived
	}
	if item, ok := s.Find("mutation:" + path); ok {
		return item.Status == StatusKnown
	}
	// A complete listing of the target itself proves it exists. Without this,
	// "inspect order/checkout" could never establish target_state from its own
	// listing, only from a parent directory listing.
	if item, ok := s.Find("dir:" + path); ok && item.Complete {
		return true
	}
	// A structural query that names the exact path proves the path exists and
	// carries declarations. It is not a file read, but it is target-state and
	// coordinate evidence for a question about a declaration/package.
	for _, item := range s.Items {
		if item.Source.Path != path {
			continue
		}
		if strings.HasPrefix(item.ID, "symbol:") || strings.HasPrefix(item.ID, "symbols:") {
			return item.Status != StatusConflicting && item.Status != StatusRequired && item.Status != StatusUnknown
		}
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." {
		dir = "."
	}
	// A complete listing of the parent establishes both presence and absence:
	// either the entry is in the listing or the file is not there.
	if item, ok := s.Find("dir:" + dir); ok && item.Complete {
		return true
	}
	return false
}

// ObserveTurns folds completed turns into the state. It is idempotent per tool
// call, so a projector may call it on every model turn.
//
// It reports the evidence gain of the final turn: how many outcomes recorded a
// repository fact, and how many of those facts were new. A turn that observed
// the repository without adding a fact is a turn whose exploration is spent,
// which is the only signal the runtime needs to stop offering tools. The
// counters describe one turn, not the run, so a model is never stopped for the
// total number of calls it made.
func (s *EvidenceState) ObserveTurns(shape TaskShape, turns []agent.Turn) (observed, novel int) {
	if s == nil {
		return 0, 0
	}
	if s.ObservedCalls == nil {
		s.ObservedCalls = map[string]bool{}
	}
	if s.LastMutationSeq == nil {
		s.LastMutationSeq = map[string]int{}
	}
	s.ensureRequirements(shape)
	last := len(turns) - 1
	for index, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.CallID != "" && s.ObservedCalls[outcome.CallID] {
				continue
			}
			if outcome.CallID != "" {
				s.ObservedCalls[outcome.CallID] = true
			}
			fact, changed := s.observe(outcome)
			if index == last && fact {
				observed++
				if changed {
					novel++
				}
			}
		}
	}
	s.trim()
	return observed, novel
}

// ensureRequirements records the shape's needs once.
func (s *EvidenceState) ensureRequirements(shape TaskShape) {
	s.Facts = shape.RepositoryFacts
	s.conclusionMode = contract.ConclusionModeFor(shape.Kind, shape.Objective)
	for _, need := range shape.RequiredKnowledge {
		found := false
		for _, existing := range s.Requirements {
			if existing.Key == need.Key {
				found = true
				break
			}
		}
		if !found {
			s.Requirements = append(s.Requirements, Requirement(need))
		}
	}
}

// ObserveOutcome folds one tool outcome into the state and reports whether it
// recorded a repository fact the state did not already hold.
//
// A refusal, a diagnostic, and a generic tool result are control signals, not
// repository facts: they never count as evidence gain, so a model that is
// adapting to errors is not mistaken for a model that is repeating itself.
func (s *EvidenceState) ObserveOutcome(outcome agent.ToolOutcome) bool {
	_, changed := s.observe(outcome)
	return changed
}

// observe is the one place the outcome-to-evidence switch lives. It reports
// whether the outcome carried a repository fact and whether that fact was new.
func (s *EvidenceState) observe(outcome agent.ToolOutcome) (fact, changed bool) {
	if s == nil {
		return false, false
	}
	if outcome.Refused() {
		// A withheld or denied action never ran. It is a control signal, not
		// an observation about the repository, and must not become evidence.
		// A failure refusal is still a repeated proposal, so it counts against
		// the capability's retry budget without executing.
		if outcome.Result.FailureRejected {
			s.Seq++
			s.recordFailureRefusal(outcome, s.Seq)
		}
		return false, false
	}
	if outcome.Result.Reused {
		// A reuse notice restates an observation that is already current. It
		// is not a new repository fact, and recording it as a read would make
		// the notice look like changed file content.
		return false, false
	}
	defer s.trim()
	s.Seq++
	seq := s.Seq
	details := detailsMap(outcome.Result.Details)
	path := detailString(details, "path")
	if path == "" {
		path = argString(outcome.Args, "path")
	}
	tool := outcome.ToolName

	if outcome.Result.IsError {
		// A diagnostic is not a repository observation. The one exception is a
		// missing-file error, which establishes that the named path is absent.
		// A turn made only of errors therefore never counts as stagnation: the
		// model is correcting itself, not repeating a completed observation.
		//
		// The failure ledger is separate: an absence and a resource stop are
		// successful control observations, while an execution failure is a
		// capability fact the next projection must be able to refuse.
		if !outcome.Result.ObservedAbsence && outcome.Result.Exhausted == nil {
			s.recordFailure(outcome, seq)
		}
		changed := s.addError(outcome, path, seq)
		return changed, changed
	}

	// A successful observation of the same coordinate recovers an earlier
	// failure. A definite workspace change also invalidates non-fatal failures:
	// the same command or read is a different proposal once the code it
	// observes has changed, so a failed test must be retryable after the fix.
	if outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates) {
		s.resolveNonFatalFailures()
	}
	s.resolveFailure(outcome)
	if outcome.Result.Verifies {
		// A successful check proves the final state is sound, so every earlier
		// failure is recovered even when the retry used a different capability
		// or coordinate. The live execution summary already applies this rule;
		// the durable ledger must agree or a resumed task reports a failure the
		// run already closed.
		s.resolveFailuresBefore(seq)
	}

	switch tool {
	case "read":
		// A read the runtime cannot attribute to a repository path is not a
		// repository observation. Counting it as "observed but not novel"
		// would tell the projector that exploration had stopped producing
		// facts and withhold every tool, even though nothing was learned.
		if cleanPath(path) == "" {
			s.addGeneric(outcome, seq)
			return false, false
		}
		return true, s.addRead(outcome, path, details, seq)
	case "ls":
		return true, s.addListing(outcome, path, details, seq)
	case "glob":
		return true, s.addSearch(outcome, details, seq)
	case "grep":
		return true, s.addSearch(outcome, details, seq)
	case "symbols":
		return true, s.addSymbols(outcome, details, seq)
	case "graph", "refs", "structure":
		return true, s.addStructural(outcome, details, seq)
	case "investigate":
		return true, s.addInvestigation(outcome, details, seq)
	case "conclude":
		return true, s.addConclusion(outcome, details, seq)
	case "write", "edit", "delete":
		return true, s.addMutation(outcome, path, details, seq)
	case "bash", "verify":
		return true, s.addBash(outcome, details, seq)
	case "web_fetch", "search", "browser", "mcp_list", "mcp_read", "mcp_call":
		return true, s.addExternal(outcome, details, seq)
	default:
		s.addGeneric(outcome, seq)
		return false, false
	}
}

// addError records an exact diagnostic. A missing-file error also establishes
// that the named path is absent, which is legitimate target-state evidence.
func (s *EvidenceState) addError(outcome agent.ToolOutcome, path string, seq int) bool {
	text := bound(outcome.Result.Text, MaxDiagnosticValueBytes)
	summary := firstLine(outcome.Result.Text)
	id := "error:" + outcome.ToolName + ":" + outcome.CallID
	s.upsert(EvidenceItem{
		ID:         id,
		Status:     StatusKnown,
		Retention:  RetentionDiagnostic,
		Provenance: ProvenanceDiagnostic,
		Summary:    bound("error from "+outcome.ToolName+": "+summary, MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
		Seq:        seq,
	})
	if path != "" && isMissingFileError(outcome.Result.Text) {
		return s.upsert(EvidenceItem{
			ID:         "file:" + cleanPath(path),
			Status:     StatusKnown,
			Retention:  RetentionExact,
			Provenance: classifyPathProvenance(path),
			Summary:    cleanPath(path) + ": absent (observed by " + outcome.ToolName + ")",
			Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: cleanPath(path)},
			Seq:        seq,
		})
	}
	return false
}

// addRead records an exact file observation.
func (s *EvidenceState) addRead(outcome agent.ToolOutcome, path string, details map[string]any, seq int) bool {
	path = cleanPath(path)
	if path == "" {
		s.addGeneric(outcome, seq)
		return false
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	totalLines := detailInt(details, "totalLines")
	offset := detailInt(details, "offset")
	limit := detailInt(details, "limit")
	complete := offset <= 1 && (totalLines == 0 || offset+limit-1 >= totalLines)
	summary := path
	if totalLines > 0 {
		summary += fmt.Sprintf(": %d lines", totalLines)
		if !complete {
			summary += fmt.Sprintf(" (window from line %d)", max(offset, 1))
		}
	}
	if first := firstContentLine(text); first != "" {
		summary += " — " + bound(first, 120)
	}
	item := EvidenceItem{
		ID:         "file:" + path,
		Status:     StatusKnown,
		Retention:  RetentionExact,
		Provenance: classifyPathProvenance(path),
		Summary:    bound(summary, MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Complete:   complete,
		Source:     Source{Tool: "read", CallID: outcome.CallID, Path: path, StartLine: max(offset, 1)},
		Seq:        seq,
	}
	if item.Source.StartLine > 0 && totalLines > 0 {
		item.Source.EndLine = min(item.Source.StartLine+max(limit, 1)-1, totalLines)
	}
	if previous, ok := s.Find(item.ID); ok {
		if previous.Digest != "" && previous.Digest != item.Digest {
			if mutationSeq, changed := s.LastMutationSeq[path]; changed && mutationSeq > previous.Seq {
				// The runtime changed the file; the new read is the current truth.
			} else if sameReadWindow(previous, item) {
				// Two observations of the same window disagree without a runtime
				// mutation between them. That is a real conflict.
				item.Status = StatusConflicting
				item.Conflict = "the file changed without a recorded runtime mutation"
				item.Summary = bound(path+": CONFLICTING content observed at two different times", MaxEvidenceSummaryBytes)
				if item.Value == "" {
					item.Value = previous.Value
				}
			}
		}
		// Different windows of one file are different observations, not a
		// contradiction. Keep both: the opening window carries a document's
		// identity definition, and a later page used to overwrite it, so the
		// project's own name became "documentation-only" and vetoed an answer
		// that merely repeated it. The value stays bounded and is not
		// completeness evidence.
		if !sameReadWindow(previous, item) && previous.Value != "" && item.Value != "" {
			merged := mergeReadWindows(previous, item)
			if merged != item.Value {
				item.Value = merged
				item.Complete = false
				item.Digest = digest(merged)
			}
		}
	}
	return s.upsert(item)
}

// sameReadWindow reports whether two reads cover the same source range.
//
// A complete read covers the whole file. Two partial reads are comparable only
// when they start at the same line and end at the same line; anything else
// observes a different region and may legitimately differ.
func sameReadWindow(previous, current EvidenceItem) bool {
	if previous.Complete || current.Complete {
		return previous.Complete && current.Complete
	}
	return previous.Source.StartLine == current.Source.StartLine &&
		previous.Source.EndLine == current.Source.EndLine
}

// mergeReadWindows keeps the opening window first and appends the later page,
// bounded. The opening window is what names the document; a later page alone
// does not define the project.
func mergeReadWindows(previous, current EvidenceItem) string {
	first, second := previous.Value, current.Value
	if current.Source.StartLine > 0 && current.Source.StartLine <= 1 &&
		previous.Source.StartLine > 1 {
		first, second = current.Value, previous.Value
	}
	if strings.Contains(first, second) {
		return first
	}
	if strings.Contains(second, first) {
		return second
	}
	return bound(first+"\n\n"+second, MaxEvidenceValueBytes)
}

// addListing records a directory listing as structural evidence.
func (s *EvidenceState) addListing(outcome agent.ToolOutcome, path string, details map[string]any, seq int) bool {
	path = cleanPath(path)
	if path == "" {
		path = "."
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	complete := !strings.Contains(text, "showing ") && !strings.Contains(strings.ToLower(text), "truncated")
	entries := detailInt(details, "entries")
	summary := "directory " + path
	if entries > 0 {
		summary += fmt.Sprintf(": %d entries", entries)
	}
	if first := firstContentLine(text); first != "" {
		summary += " — " + bound(first, 160)
	}
	return s.upsert(EvidenceItem{
		ID:         "dir:" + path,
		Status:     StatusKnown,
		Retention:  RetentionStructural,
		Provenance: ProvenanceObservation,
		Summary:    bound(summary, MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Complete:   complete,
		Source:     Source{Tool: "ls", CallID: outcome.CallID, Path: path},
		Seq:        seq,
	})
}

// addSearch records a bounded search result as derived structure.
func (s *EvidenceState) addSearch(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	pattern := detailString(details, "pattern")
	path := detailString(details, "path")
	if path == "" {
		path = argString(outcome.Args, "path")
	}
	id := "search:" + outcome.ToolName + ":" + pattern + ":" + path
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	summary := outcome.ToolName
	if pattern != "" {
		summary += " " + pattern
	}
	if files := detailInt(details, "files"); files > 0 {
		summary += fmt.Sprintf(": %d file(s)", files)
	}
	if matches := detailInt(details, "matches"); matches > 0 {
		summary += fmt.Sprintf(": %d match(es)", matches)
	}
	if first := firstContentLine(text); first != "" {
		summary += " — " + bound(first, 140)
	}
	// A scan that pruned .gitignore paths or built-in directories did not cover
	// the tree. It may still return useful matches, but it cannot establish
	// that something absent from the result is absent from the repository. An
	// explicit gitignore=false override is an investigative scan, not a
	// complete project inventory, and retains that distinction.
	complete := detailInt(details, "ignored") == 0 &&
		detailInt(details, "excluded") == 0 &&
		!strings.Contains(text, "pass skip=") && !strings.Contains(text, "raise \"limit\"") &&
		!strings.Contains(strings.ToLower(text), "timed out") &&
		!strings.Contains(strings.ToLower(text), "truncated") &&
		gitignoreEnabled(details)
	counts := map[string]int{}
	for _, key := range []string{"matches", "files", "ignored", "excluded"} {
		if value, ok := details[key].(int); ok {
			counts[key] = value
		}
	}
	if len(counts) == 0 {
		counts = nil
	}
	return s.upsert(EvidenceItem{
		ID:         id,
		Status:     StatusDerived,
		Retention:  RetentionStructural,
		Provenance: classifyPathProvenance(path),
		Summary:    bound(summary, MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Counts:     counts,
		Complete:   complete,
		Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
		Seq:        seq,
	})
}

// addSymbols records structural coordinates. The aggregate item is bounded;
// individual coordinates are retained up to a small cap.
func (s *EvidenceState) addSymbols(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	query := argString(outcome.Args, "symbol")
	if query == "" {
		query = argString(outcome.Args, "package")
	}
	if query == "" {
		query = argString(outcome.Args, "path")
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	complete := !detailBool(details, "truncated")
	if _, ok := details["complete"]; ok {
		complete = detailBool(details, "complete") && !detailBool(details, "partial")
	}
	changed := s.upsert(EvidenceItem{
		ID:         "symbols:" + query,
		Status:     StatusDerived,
		Retention:  RetentionStructural,
		Provenance: ProvenanceObservation,
		Summary:    bound("symbol query "+query+": "+firstLine(text), MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Complete:   complete,
		View:       "symbols",
		Source:     structuralSource(details, "symbols", outcome.CallID, "", 0, 0),
		Seq:        seq,
	})
	coords := coordinateMaps(details["coordinates"])
	for i, coord := range coords {
		if i >= 24 {
			break
		}
		path := detailString(coord, "path")
		symbol := detailString(coord, "symbol")
		if path == "" || symbol == "" {
			continue
		}
		if s.upsert(EvidenceItem{
			ID:         "symbol:" + path + ":" + symbol,
			Status:     StatusKnown,
			Retention:  RetentionStructural,
			Provenance: classifyPathProvenance(path),
			Summary: bound(symbol+" at "+path+":"+itoa(detailInt(coord, "start_line"))+
				"-"+itoa(detailInt(coord, "end_line")), MaxEvidenceSummaryBytes),
			Complete: complete,
			Source: structuralSourceWithCoordinate(details, coord, "symbols", outcome.CallID, path,
				detailInt(coord, "start_line"), detailInt(coord, "end_line")),
			Seq: seq,
		}) {
			changed = true
		}
	}
	return changed
}

// addConclusion records the model's terminal investigation verdict.
//
// It is a control observation, not a proof: the evidence and uncertainty are
// preserved verbatim for review, and completion still evaluates the claims.
// A conclusion that names a concrete unresolved code coordinate is recorded
// provisionally. The named coordinate becomes the next evidence requirement;
// another conclusion cannot close the task until that coordinate was observed
// or a bounded number of attempts explicitly classifies the result as
// UNRESOLVED. This is what keeps "I could not determine X" from becoming a
// terminal answer before X was even inspected.
func (s *EvidenceState) addConclusion(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	verdict := contract.ConclusionVerdict(strings.ToUpper(strings.TrimSpace(detailString(details, "verdict"))))
	if verdict == "" {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(detailString(details, "status")))
	if status == "" {
		status = "FINAL" // legacy observations predate the explicit status
	}
	evidence := strings.TrimSpace(detailString(details, "evidence"))
	uncertainty := strings.TrimSpace(detailString(details, "uncertainty"))
	// The control plane, not the model, owns the vocabulary. A SAFE/BUG
	// verdict on a general answer is not a closed investigation, and an
	// ANSWERED verdict on a failure hypothesis does not establish safety.
	if !s.conclusionMode.Accepts(verdict) {
		return s.addConclusionRejected(outcome, verdict, evidence, seq)
	}
	// Admission is a control decision, not a consequence of calling the tool.
	// A payload that says it is provisional, lacks supporting evidence, or
	// carries an unrecognized status remains an observation. It cannot satisfy
	// investigation_conclusion or open the answer boundary.
	if status == "IN_PROGRESS" || (status != "FINAL" && status != "") ||
		evidence == "" || contract.IsProvisionalConclusion(evidence, uncertainty) {
		return s.addConclusionProgress(outcome, string(verdict), evidence, details, seq)
	}
	declared := unresolvedConclusionTargets(uncertainty)
	targets := declared
	if verdict == contract.VerdictPartial {
		// PARTIAL is an explicit decision to stop with named gaps. Treating
		// those gaps as a pre-conclusion obligation would make the verdict
		// unusable: the answer could never be partial, only unfinished.
		targets = nil
	}
	previous, hasPrevious := s.latestConclusion()
	// A provisional conclusion's obligation cannot be erased by simply
	// omitting it from the next call. If it is not observed, it remains the
	// frontier; if it was observed, the next conclusion may close the task.
	if hasPrevious && !previous.Complete && len(previous.Unresolved) > 0 {
		targets = mergeConclusionTargets(previous.Unresolved, targets)
	}
	attempts := 1
	newEvidence := false
	if hasPrevious && !previous.Complete {
		attempts = previous.Counts["attempts"] + 1
		newEvidence = s.hasObservationAfter(previous.Seq)
	}
	resolved := len(targets) == 0
	if !resolved && hasPrevious && !previous.Complete {
		resolved = s.targetsObservedAfter(previous.Seq, targets)
	}
	// A bounded investigation may explicitly settle as UNRESOLVED after it
	// actually attempted the named gap. A repeated conclusion with no new
	// observation is not an attempt; the projection's no-progress bound closes
	// that shape instead of accepting it here.
	hardLimit := !resolved && verdict == contract.VerdictUnresolved &&
		attempts >= MaxConclusionAttempts && newEvidence
	complete := resolved || hardLimit
	unresolved := append([]string(nil), targets...)
	if verdict == contract.VerdictPartial {
		// Keep the declared gaps in the record for audit even though they are
		// not obligations. PendingConclusionTargets ignores a complete record.
		unresolved = declared
	} else if complete && resolved {
		unresolved = nil
	}
	value := "verdict: " + string(verdict) + "\nevidence: " + evidence
	if uncertainty != "" {
		value += "\nuncertainty: " + uncertainty
	}
	item := EvidenceItem{
		ID:         "conclusion:" + digest(string(verdict)+"\x00"+evidence),
		Status:     StatusKnown,
		Retention:  RetentionExact,
		Provenance: ProvenanceDiagnostic,
		Summary:    bound("conclusion "+string(verdict)+": "+evidence, MaxEvidenceSummaryBytes),
		Value:      bound(value, MaxEvidenceValueBytes),
		Digest:     digest(value),
		Complete:   complete,
		Unresolved: unresolved,
		View:       "conclusion",
		Source:     Source{Tool: "conclude", CallID: outcome.CallID},
		Seq:        seq,
		Counts:     map[string]int{"attempts": attempts},
	}
	if !complete {
		item.Conflict = "provisional conclusion: unresolved " + strings.Join(unresolved, ", ")
	} else if hardLimit {
		item.Conflict = "semantic limit reached: " + strings.Join(unresolved, ", ") + " remains unresolved"
	}
	return s.upsert(item)
}

// addConclusionProgress records an explicit progress observation. It is not a
// conclusion: it does not satisfy investigation_conclusion, does not create or
// clear a targeted obligation, and does not consume the terminal attempt
// budget. The return value is deliberately false so replaying a progress marker
// is not counted as new evidence or as task advancement.
func (s *EvidenceState) addConclusionProgress(outcome agent.ToolOutcome, verdict, evidence string, details map[string]any, seq int) bool {
	uncertainty := strings.TrimSpace(detailString(details, "uncertainty"))
	value := "verdict candidate: " + verdict + "\nevidence: " + evidence
	if uncertainty != "" {
		value += "\nuncertainty: " + uncertainty
	}
	s.upsert(EvidenceItem{
		ID:         "conclusion-progress:" + digest(verdict+"\x00"+evidence+"\x00"+uncertainty),
		Status:     StatusKnown,
		Retention:  RetentionDiagnostic,
		Provenance: ProvenanceDiagnostic,
		Summary:    bound("conclusion progress: "+evidence, MaxEvidenceSummaryBytes),
		Value:      bound(value, MaxEvidenceValueBytes),
		Digest:     digest(value),
		Complete:   false,
		View:       "conclusion_progress",
		Source:     Source{Tool: "conclude", CallID: outcome.CallID},
		Seq:        seq,
	})
	return false
}

// addConclusionRejected records a verdict from the wrong vocabulary. It is
// deliberately not a progress observation: progress implies the model is
// still gathering evidence for the same verdict, while this payload is a
// category error the projection must correct explicitly. It is never a
// completed conclusion and never satisfies investigation_conclusion.
func (s *EvidenceState) addConclusionRejected(outcome agent.ToolOutcome, verdict contract.ConclusionVerdict, evidence string, seq int) bool {
	value := "rejected verdict: " + string(verdict) +
		"\nrequired: " + s.conclusionMode.VerdictList() +
		"\nevidence: " + evidence
	s.upsert(EvidenceItem{
		ID:         "conclusion-invalid:" + digest(string(verdict)+"\x00"+evidence),
		Status:     StatusKnown,
		Retention:  RetentionDiagnostic,
		Provenance: ProvenanceDiagnostic,
		Summary:    bound("invalid conclusion verdict "+string(verdict)+"; required "+s.conclusionMode.VerdictList(), MaxEvidenceSummaryBytes),
		Value:      bound(value, MaxEvidenceValueBytes),
		Digest:     digest(value),
		Complete:   false,
		Conflict:   "invalid conclusion verdict: " + string(verdict) + " is not valid for this objective",
		View:       "conclusion_rejected",
		Source:     Source{Tool: "conclude", CallID: outcome.CallID},
		Seq:        seq,
	})
	return false
}

// latestConclusion returns the most recently recorded conclusion.
func (s *EvidenceState) latestConclusion() (EvidenceItem, bool) {
	var latest EvidenceItem
	found := false
	for _, item := range s.Items {
		if !strings.HasPrefix(item.ID, "conclusion:") {
			continue
		}
		if !found || item.Seq > latest.Seq {
			latest = item
			found = true
		}
	}
	return latest, found
}

// PendingConclusionTargets returns the named unresolved code coordinates of the
// latest conclusion. It is empty once the conclusion is admissible, including
// a bounded UNRESOLVED settlement.
func (s *EvidenceState) PendingConclusionTargets() []string {
	if s == nil {
		return nil
	}
	latest, ok := s.latestConclusion()
	if !ok || latest.Complete {
		return nil
	}
	return append([]string(nil), latest.Unresolved...)
}

// investigationTargetObserved reports whether a pending conclusion's named
// coordinate has been observed after that conclusion. Prior evidence is not
// enough: the model explicitly declared the fact unresolved, so the runtime
// requires a targeted observation rather than accepting a substring in an
// earlier survey.
func (s *EvidenceState) investigationTargetObserved(target string, _ ExecutionState) bool {
	if s == nil || strings.TrimSpace(target) == "" {
		return false
	}
	latest, ok := s.latestConclusion()
	if !ok || latest.Complete {
		return false
	}
	for _, unresolved := range latest.Unresolved {
		if unresolved == target {
			return s.targetsObservedAfter(latest.Seq, []string{target})
		}
	}
	return false
}

// isConclusionObservation reports control records produced by conclude. A
// progress record is not evidence of the repository and cannot satisfy either
// a conclusion or a named target.
func isConclusionObservation(id string) bool {
	return strings.HasPrefix(id, "conclusion:") ||
		strings.HasPrefix(id, "conclusion-progress:") ||
		strings.HasPrefix(id, "conclusion-invalid:")
}

// hasObservationAfter reports whether the state recorded a non-conclusion
// observation after seq. It is the attempt evidence for a hard UNRESOLVED
// settlement: repeating a conclusion without doing anything is not an attempt.
func (s *EvidenceState) hasObservationAfter(seq int) bool {
	for _, item := range s.Items {
		if item.Seq <= seq || isConclusionObservation(item.ID) {
			continue
		}
		if item.Status == StatusRequired || item.Status == StatusUnknown {
			continue
		}
		return true
	}
	return false
}

// targetsObservedAfter reports whether every named target appears in a
// non-conclusion observation after seq.
func (s *EvidenceState) targetsObservedAfter(seq int, targets []string) bool {
	if len(targets) == 0 {
		return true
	}
	observed := make(map[string]bool, len(targets))
	for _, item := range s.Items {
		if item.Seq <= seq || isConclusionObservation(item.ID) {
			continue
		}
		if item.Status == StatusRequired || item.Status == StatusUnknown {
			continue
		}
		haystack := item.Source.Path + "\n" + item.ID + "\n" + item.Summary + "\n" + item.Value
		for _, target := range targets {
			if conclusionTargetMatches(haystack, target) {
				observed[target] = true
			}
		}
	}
	for _, target := range targets {
		if !observed[target] {
			return false
		}
	}
	return true
}

// addInvestigation records one bounded investigation as structural evidence.
//
// The tool result is a provider-backed observation, not model narration: its
// status, stop reason, facts, and provider attribution are what the runtime
// saw. Recording it is what lets a later projection know that an investigation
// was actually attempted instead of treating the call as a generic protocol
// event. The item does not claim the answer is proven; it records the
// investigation's own conclusion and leaves completion to the evidence
// evaluator.
func (s *EvidenceState) addInvestigation(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	question := argString(outcome.Args, "question")
	if strings.TrimSpace(question) == "" {
		question = "investigation"
	}
	status := detailString(details, "status")
	stop := detailString(details, "stop_reason")
	if status == "" {
		status = "unknown"
	}
	id := "investigation:" + question
	if len(id) > 180 {
		id = "investigation:" + digest(question)
	}
	summary := "investigation " + status
	if stop != "" {
		summary += " — " + stop
	}
	value := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	return s.upsert(EvidenceItem{
		ID:         id,
		Status:     StatusDerived,
		Retention:  RetentionStructural,
		Provenance: ProvenanceObservation,
		Summary:    bound(summary, MaxEvidenceSummaryBytes),
		Value:      value,
		Digest:     digest(value),
		Complete:   status == "sufficient",
		View:       "investigate",
		Facets:     investigationFacets(details),
		Source:     Source{Tool: "investigate", CallID: outcome.CallID},
		Seq:        seq,
	})
}

// addStructural records graph, refs, and structure observations as structural
// evidence. It reuses the existing evidence model: one aggregate item per
// query plus a small bounded set of coordinate items.
func (s *EvidenceState) addStructural(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	query := argString(outcome.Args, "symbol")
	if query == "" {
		query = argString(outcome.Args, "package")
	}
	if query == "" {
		query = argString(outcome.Args, "path")
	}
	if query == "" {
		query = detailString(details, "view")
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	complete := detailBool(details, "complete") && !detailBool(details, "partial") &&
		!detailBool(details, "truncated")
	changed := s.upsert(EvidenceItem{
		ID:         outcome.ToolName + ":" + query,
		Status:     StatusDerived,
		Retention:  RetentionStructural,
		Provenance: ProvenanceObservation,
		Summary:    bound(outcome.ToolName+" "+query+": "+firstLine(text), MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Counts:     structuralCounts(details),
		Complete:   complete,
		View:       detailString(details, "view"),
		Facets:     structuralFacets(details),
		Source:     structuralSource(details, outcome.ToolName, outcome.CallID, "", 0, 0),
		Seq:        seq,
	})
	coords := coordinateMaps(details["coordinates"])
	for i, coord := range coords {
		if i >= 24 {
			break
		}
		path := detailString(coord, "path")
		if path == "" {
			continue
		}
		symbol := detailString(coord, "symbol")
		kind := detailString(coord, "kind")
		line := detailInt(coord, "line")
		endLine := detailInt(coord, "end_line")
		id := structuralCoordinateID(kind, path, line, symbol)
		if s.upsert(EvidenceItem{
			ID:         id,
			Status:     StatusKnown,
			Retention:  RetentionStructural,
			Provenance: classifyPathProvenance(path),
			Summary:    bound(symbol+" at "+path+":"+itoa(line), MaxEvidenceSummaryBytes),
			Complete:   !detailBool(details, "partial") && !detailBool(details, "truncated"),
			Facets:     coordinateFacets(kind, symbol),
			Source: structuralSourceWithCoordinate(details, coord, outcome.ToolName, outcome.CallID, path,
				line, endLine),
			Seq: seq,
		}) {
			changed = true
		}
	}
	return changed
}

// structuralCounts retains the numeric facts a structure view reports. The
// keys are a closed vocabulary shared with structuralSource; a missing key is
// unknown, never zero.
func structuralCounts(details map[string]any) map[string]int {
	keys := []string{
		"test_files", "total", "returned", "files", "packages", "symbols",
		"references", "calls", "entrypoints", "configs", "directories",
		"total_edges", "total_references",
	}
	var counts map[string]int
	for _, key := range keys {
		value, ok := details[key].(int)
		if !ok {
			continue
		}
		if counts == nil {
			counts = map[string]int{}
		}
		counts[key] = value
	}
	return counts
}

// structuralSource projects snapshot provenance into the existing Source
// shape. This is a field addition, not a second evidence model.
func structuralSource(details map[string]any, tool, callID, path string, start, end int) Source {
	completeness := detailString(details, "completeness")
	if completeness == "" {
		if complete, ok := details["structural_complete"].(bool); ok {
			if complete {
				completeness = "COMPLETE"
			} else {
				completeness = "PARTIAL"
			}
		}
	}
	scope := ""
	if complete, ok := details["scope_complete"].(bool); ok {
		if complete {
			scope = "COMPLETE"
		} else {
			scope = "PARTIAL"
		}
	}
	// An unresolved nested repository is a scope gap even when a producer
	// reports scope_complete. Its files are absent from this snapshot.
	if structuralNestedUnresolved(details) {
		scope = "PARTIAL"
	}
	inventory := ""
	if complete, ok := details["inventory_complete"].(bool); ok {
		if complete {
			inventory = "COMPLETE"
		} else {
			inventory = "PARTIAL"
		}
	}
	return Source{
		Tool:                  tool,
		CallID:                callID,
		Path:                  path,
		StartLine:             start,
		EndLine:               end,
		SnapshotID:            detailString(details, "snapshot_id"),
		GraphState:            detailString(details, "graph_state"),
		Generation:            uint64(detailInt(details, "generation")),
		Language:              detailString(details, "language"),
		Adapter:               detailString(details, "adapter"),
		Resolution:            detailString(details, "resolution"),
		Completeness:          completeness,
		ScopeCompleteness:     scope,
		Truncated:             detailBool(details, "truncated"),
		InventoryCompleteness: inventory,
		ExcludedRoots:         structuralExcludedRoots(details),
		ExcludedSourceFiles:   detailInt(details, "excluded_source_files"),
		UnsupportedSources:    structuralUnsupportedSources(details),
	}
}

// structuralNestedUnresolved reports a nested repository boundary the
// snapshot did not include. It is a scope gap independent of the producer's
// own scope boolean.
func structuralNestedUnresolved(details map[string]any) bool {
	for _, project := range coordinateMaps(details["nested_projects"]) {
		if included, ok := project["included"].(bool); !ok || !included {
			return true
		}
	}
	return false
}

// structuralExcludedRoots counts the scope gaps in a structural result.
func structuralExcludedRoots(details map[string]any) int {
	switch values := details["excluded_roots"].(type) {
	case []map[string]any:
		return len(values)
	case []any:
		return len(values)
	default:
		return detailInt(details, "excluded_roots")
	}
}

// structuralUnsupportedSources counts inventoried files whose language has no
// adapter. A structural inventory is not complete source coverage when it
// contains them.
func structuralUnsupportedSources(details map[string]any) int {
	switch values := details["unsupported_languages"].(type) {
	case []map[string]any:
		total := 0
		for _, item := range values {
			total += detailInt(item, "files")
		}
		return total
	default:
		return 0
	}
}

// gitignoreEnabled reports whether a scan honoured .gitignore. An explicit
// false override is an investigative scan; it does not establish a complete
// project inventory.
func gitignoreEnabled(details map[string]any) bool {
	if value, ok := details["gitignore"].(bool); ok {
		return value
	}
	return true
}

// structuralSourceWithCoordinate adds the per-coordinate language, adapter,
// resolution, and truncation facts that the aggregate query header cannot
// attribute to a single row.
func structuralSourceWithCoordinate(details, coord map[string]any, tool, callID, path string, start, end int) Source {
	source := structuralSource(details, tool, callID, path, start, end)
	if value := detailString(coord, "language"); value != "" {
		source.Language = value
	}
	if value := detailString(coord, "adapter"); value != "" {
		source.Adapter = value
	}
	if value := detailString(coord, "resolution"); value != "" {
		source.Resolution = value
	}
	if truncated, ok := coord["truncated"].(bool); ok {
		source.Truncated = truncated
	}
	return source
}

// structuralCoordinateID namespaces one coordinate by its graph role.
func structuralCoordinateID(kind, path string, line int, symbol string) string {
	prefix := "structure"
	switch kind {
	case "graph_edge":
		prefix = "graph_edge"
	case "package":
		prefix = "package"
	case "entrypoint":
		prefix = "entrypoint"
	case "test", "test_association":
		prefix = "test"
	case "call", "caller":
		prefix = "call"
	case "reference", "ref":
		prefix = "ref"
	case "file":
		prefix = "file"
	case "config":
		prefix = "config"
	case "target", "impact_target":
		prefix = "symbol"
	}
	if symbol == "" {
		symbol = kind
	}
	return prefix + ":" + path + ":" + itoa(line) + ":" + symbol
}

// addMutation records a definite repository change.
func (s *EvidenceState) addMutation(outcome agent.ToolOutcome, path string, details map[string]any, seq int) bool {
	path = cleanPath(path)
	value := detailString(details, "diff")
	if value == "" {
		value = outcome.Result.Text
	}
	value = bound(value, MaxEvidenceValueBytes)
	summary := "changed " + path
	if bytes := detailInt(details, "bytes"); bytes > 0 {
		summary += fmt.Sprintf(" (%d bytes)", bytes)
	}
	if path == "" {
		summary = "changed the repository"
	}
	id := "mutation:" + path
	if path == "" {
		id = "mutation:" + outcome.CallID
	}
	changed := s.upsert(EvidenceItem{
		ID:         id,
		Status:     StatusKnown,
		Retention:  RetentionExact,
		Provenance: ProvenanceObservation,
		Summary:    bound(summary, MaxEvidenceSummaryBytes),
		Value:      value,
		Digest:     digest(value),
		Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
		Seq:        seq,
	})
	if path != "" {
		s.LastMutationSeq[path] = seq
	}
	return changed
}

// ObserveJournal records the host edit journal's workspace diff as mutation
// evidence.
//
// The journal captures what the command text cannot reveal: a shell mutation
// such as create-next-app changed paths the tool never named. The position is
// the run position of the mutating batch, so a verification that followed it is
// ordered after the change. A captured journal with no changes records nothing:
// the workspace did not change, and absence of evidence is not evidence of a
// change.
func (s *EvidenceState) ObserveJournal(provenance agent.MutationProvenance, position int) {
	if s == nil || !provenance.Captured {
		return
	}
	if s.LastMutationSeq == nil {
		s.LastMutationSeq = map[string]int{}
	}
	if position < 0 {
		position = 0
	}
	if position > s.Seq {
		s.Seq = position
	}
	for _, change := range provenance.Changes {
		path := cleanPath(change.Path)
		if path == "" {
			continue
		}
		id := "mutation:journal:" + path
		s.upsert(EvidenceItem{
			ID:         id,
			Status:     StatusKnown,
			Retention:  RetentionExact,
			Provenance: ProvenanceObservation,
			Summary:    bound("changed "+path, MaxEvidenceSummaryBytes),
			Source:     Source{Tool: "edit-journal", Path: path},
			Seq:        position,
		})
		s.LastMutationSeq[path] = position
	}
	s.trim()
}

// addBash records command execution, verification, and mutation evidence.
func (s *EvidenceState) addBash(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	command := detailString(details, "command")
	if command == "" && outcome.Result.Verification != nil {
		command = outcome.Result.Verification.Command
	}
	if command == "" {
		command = argString(outcome.Args, "command")
	}
	if command == "" && outcome.ToolName == "verify" {
		if service, ok := details["service"].(map[string]any); ok {
			command = detailString(service, "command")
		}
	}
	if command == "" {
		command = outcome.ToolName + " " + outcome.CallID
	}
	// Preserve the historical bash evidence identity: checkpoints store these
	// ids, and changing them would orphan every prior observation. Any other
	// capability gets its own namespace so a command run by verify cannot
	// overwrite the same command's bash observation.
	idKey := command
	if outcome.ToolName != "bash" {
		idKey = outcome.ToolName + ":" + command
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	exit := detailInt(details, "exitCode")
	summary := "$ " + bound(command, 160)
	if outcome.ToolName == "verify" {
		summary = "verification " + bound(command, 160)
	}
	if exit != 0 {
		summary += fmt.Sprintf(" (exit %d)", exit)
	}
	executed := outcome.Result.Executes && outcome.Result.Finished && !outcome.Result.IsError
	runtime := outcome.Result.Runtime != nil
	if runtime {
		summary += fmt.Sprintf(" (runtime observed: %d check(s))", outcome.Result.Runtime.Checks)
	}
	changed := s.upsert(EvidenceItem{
		ID:         "command:" + digest(idKey),
		Status:     StatusKnown,
		Retention:  RetentionDiagnostic,
		Provenance: ProvenanceDiagnostic,
		Summary:    bound(summary, MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID},
		Seq:        seq,
		Executed:   executed,
		Runtime:    runtime,
	})
	if observed, _ := details["git_observation"].(bool); observed {
		// A completed git inspection is evidence of version-control state. It
		// is not implementation evidence, so it carries diagnostic provenance
		// and cannot stand in for a source read.
		if s.upsert(EvidenceItem{
			ID:         "vcs:" + digest(idKey),
			Status:     StatusKnown,
			Retention:  RetentionExact,
			Provenance: ProvenanceDiagnostic,
			Summary:    bound("version-control observation: "+summary, MaxEvidenceSummaryBytes),
			Value:      text,
			Digest:     digest(text),
			Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID},
			Seq:        seq,
			Executed:   true,
		}) {
			changed = true
		}
	}
	if outcome.Result.Verifies {
		check := ""
		scope := ""
		if outcome.Result.Verification != nil {
			check = string(outcome.Result.Verification.EffectiveCheck())
			for _, s := range outcome.Result.Verification.Scopes {
				if s.All {
					scope = "*"
					break
				}
				if scope == "" {
					scope = s.Path
				}
			}
		}
		id := "verification:" + digest(idKey)
		if s.upsert(EvidenceItem{
			ID:         id,
			Status:     StatusKnown,
			Retention:  RetentionExact,
			Provenance: ProvenanceObservation,
			Summary:    bound("verification "+check+" "+scope+": "+summary, MaxEvidenceSummaryBytes),
			Value:      text,
			Digest:     digest(text),
			Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID, Check: check, Scope: scope},
			Seq:        seq,
			Executed:   true,
			Runtime:    runtime,
		}) {
			changed = true
		}
	}
	if outcome.Result.Mutates {
		id := "mutation:" + digest(idKey)
		if s.upsert(EvidenceItem{
			ID:         id,
			Status:     StatusKnown,
			Retention:  RetentionExact,
			Provenance: ProvenanceObservation,
			Summary:    bound("command may have changed the workspace: "+summary, MaxEvidenceSummaryBytes),
			Value:      text,
			Digest:     digest(text),
			Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID},
			Seq:        seq,
		}) {
			changed = true
		}
	}
	if outcome.Result.Executes && !outcome.Result.Finished {
		if s.upsert(EvidenceItem{
			ID:         "background:" + outcome.CallID,
			Status:     StatusKnown,
			Retention:  RetentionTransient,
			Provenance: ProvenanceDiagnostic,
			Summary:    bound("background job started: "+summary, MaxEvidenceSummaryBytes),
			Value:      text,
			Digest:     digest(text),
			Source:     Source{Tool: "bash", CallID: outcome.CallID},
			Seq:        seq,
		}) {
			changed = true
		}
	}
	if s.addShellSourceReads(outcome, command, text, seq) {
		changed = true
	}
	return changed
}

// addShellSourceReads records the source files a shell command opened. A shell
// content read is the same observation as the read tool's: the model saw the
// bytes, so the requirement ledger must not keep asking for a source read it
// already has. A command that concatenated several files is content evidence
// but not a complete observation of any one of them.
func (s *EvidenceState) addShellSourceReads(outcome agent.ToolOutcome, command, text string, seq int) bool {
	if outcome.Result.IsError || outcome.Refused() || strings.TrimSpace(text) == "" {
		return false
	}
	reads := agent.ShellSourceReads(command)
	if len(reads) == 0 {
		return false
	}
	changed := false
	truncated := agent.ShellOutputTruncated(text)
	for _, read := range reads {
		path := cleanPath(read.Path)
		if path == "" {
			continue
		}
		summary := path + " — shell content read"
		if len(reads) == 1 {
			if first := firstContentLine(text); first != "" {
				summary += " — " + bound(first, 120)
			}
		}
		if s.upsert(EvidenceItem{
			ID:         "file:" + path,
			Status:     StatusKnown,
			Retention:  RetentionExact,
			Provenance: classifyPathProvenance(path),
			Summary:    bound(summary, MaxEvidenceSummaryBytes),
			Value:      text,
			Digest:     digest(text),
			Complete:   read.WholeFile && len(reads) == 1 && !truncated,
			Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID, Path: path},
			Seq:        seq,
		}) {
			changed = true
		}
	}
	return changed
}

// addExternal records external evidence with mandatory provenance.
func (s *EvidenceState) addExternal(outcome agent.ToolOutcome, details map[string]any, seq int) bool {
	url := detailString(details, "url")
	if url == "" {
		url = argString(outcome.Args, "url")
	}
	evidence := externalEvidence(details)
	capability := ""
	locator := url
	source := ""
	provider := ""
	trust := string(knowledge.TrustUntrusted)
	if evidence != nil {
		capability = string(evidence.Provenance.Capability)
		locator = firstNonEmpty(evidence.Provenance.Locator, locator)
		source = evidence.Provenance.Source
		provider = evidence.Provenance.Provider
		trust = string(evidence.Trust)
	}
	if capability == "" {
		capability = outcome.ToolName
	}
	if trust == "" {
		trust = string(knowledge.TrustUntrusted)
	}
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	summaryLine := firstContentLine(text)
	provenance := ProvenanceExternal
	retention := RetentionExternal
	displayCapability := capability
	if capability == string(knowledge.CapabilityMCPDiscovery) || detailBool(details, "discovery_only") {
		// Discovery names capabilities; it is not knowledge about the world.
		// Keeping it as external evidence would let an explicit "use MCP"
		// requirement look satisfied before any content was observed.
		provenance = ProvenanceDiagnostic
		retention = RetentionDiagnostic
		displayCapability = "mcp discovery"
	}
	if remote := detailString(details, "remote_error"); remote != "" {
		summaryLine = "remote_error: " + firstLine(remote)
	}
	id := "external:" + capability + ":" + firstNonEmpty(locator, digest(text))
	return s.upsert(EvidenceItem{
		ID:         id,
		Status:     StatusKnown,
		Retention:  retention,
		Provenance: provenance,
		Summary: bound("UNTRUSTED "+displayCapability+" from "+firstNonEmpty(source, url, "unknown")+
			": "+summaryLine, MaxEvidenceSummaryBytes),
		Value:  text,
		Digest: digest(text),
		Source: Source{Tool: outcome.ToolName, CallID: outcome.CallID, URL: locator,
			Provider: provider, Trust: trust},
		Seq: seq,
	})
}

// addGeneric records an unrecognised tool result conservatively.
func (s *EvidenceState) addGeneric(outcome agent.ToolOutcome, seq int) {
	text := bound(outcome.Result.Text, MaxEvidenceValueBytes)
	retention := RetentionTransient
	if len(text) > 0 && !outcome.Result.Useless {
		retention = RetentionExact
	}
	s.upsert(EvidenceItem{
		ID:         "tool:" + outcome.ToolName + ":" + outcome.CallID,
		Status:     StatusKnown,
		Retention:  retention,
		Provenance: ProvenanceDiagnostic,
		Summary:    bound(outcome.ToolName+": "+firstLine(text), MaxEvidenceSummaryBytes),
		Value:      text,
		Digest:     digest(text),
		Source:     Source{Tool: outcome.ToolName, CallID: outcome.CallID},
		Seq:        seq,
	})
}

// upsert replaces an item with the same id, preserving observation order. It
// reports whether the item is new or its observed content changed: replaying the
// same observation is not new evidence.
func (s *EvidenceState) upsert(item EvidenceItem) bool {
	for i := range s.Items {
		if s.Items[i].ID != item.ID {
			continue
		}
		changed := s.Items[i].Digest != item.Digest ||
			s.Items[i].Status != item.Status ||
			s.Items[i].Complete != item.Complete ||
			s.Items[i].Value != item.Value ||
			!equalCounts(s.Items[i].Counts, item.Counts)
		s.Items[i] = item
		return changed
	}
	s.Items = append(s.Items, item)
	return true
}

// Bytes returns the retained payload size, for checkpoints and tests.
func (s *EvidenceState) Bytes() int {
	if s == nil {
		return 0
	}
	total := 0
	for _, item := range s.Items {
		total += item.retainedBytes()
	}
	return total
}

// retainedBytes is one item's contribution to the evidence bound. Bounded
// counts are small but they are still retained state, so they are counted.
func (i EvidenceItem) retainedBytes() int {
	total := len(i.ID) + len(i.Summary) + len(i.Value) + len(i.Conflict)
	for _, target := range i.Unresolved {
		total += len(target) + 1
	}
	for key := range i.Counts {
		total += len(key) + 8
	}
	return total
}

// trim bounds the state by item count and payload bytes, dropping low-value
// evidence first.
func (s *EvidenceState) trim() {
	// Content-bearing observations are retained longest because they are what
	// the request's declared needs are satisfied by. A long run of command
	// diagnostics must not evict the file reads and listings that made the
	// repository knowable. Within one priority the oldest observation goes
	// first, so the newest content survives when the byte bound is tight.
	priority := func(item EvidenceItem) int {
		if item.Status == StatusConflicting {
			return 0
		}
		switch {
		case item.Retention == RetentionExternal:
			return 1
		case strings.HasPrefix(item.ID, "file:"), strings.HasPrefix(item.ID, "mutation:"):
			return 1
		case strings.HasPrefix(item.ID, "dir:"), strings.HasPrefix(item.ID, "search:"),
			strings.HasPrefix(item.ID, "symbols:"), strings.HasPrefix(item.ID, "symbol:"):
			return 2
		case item.Retention == RetentionDerived, item.Retention == RetentionStructural:
			return 2
		case item.Retention == RetentionDiagnostic:
			return 3
		case item.Retention == RetentionExact:
			return 4
		default:
			return 5
		}
	}
	// Low-value evidence sorts first so the eviction loop can drop it from the
	// front. Within one priority the oldest observation goes first.
	sort.SliceStable(s.Items, func(i, j int) bool {
		pi, pj := priority(s.Items[i]), priority(s.Items[j])
		if pi != pj {
			return pi > pj
		}
		return s.Items[i].Seq < s.Items[j].Seq
	})
	total := s.Bytes()
	for len(s.Items) > 1 && (len(s.Items) > MaxEvidenceItems || total > MaxEvidenceBytes) {
		total -= s.Items[0].retainedBytes()
		s.Items = s.Items[1:]
	}
	// The replay guard and mutation clock are metadata, not facts. Cap them so
	// a long session cannot grow the checkpoint through bookkeeping alone.
	for len(s.ObservedCalls) > MaxEvidenceItems*4 {
		for key := range s.ObservedCalls {
			delete(s.ObservedCalls, key)
			break
		}
	}
	if len(s.LastMutationSeq) > MaxEvidenceItems {
		type entry struct {
			key string
			seq int
		}
		entries := make([]entry, 0, len(s.LastMutationSeq))
		for key, seq := range s.LastMutationSeq {
			entries = append(entries, entry{key: key, seq: seq})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
		for _, item := range entries[:len(entries)-MaxEvidenceItems] {
			delete(s.LastMutationSeq, item.key)
		}
	}
	s.trimFailures()
}

// equalCounts reports whether two bounded count maps carry the same facts.
func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// detailsMap returns a tool result's details as a map.
func detailsMap(details any) map[string]any {
	if details == nil {
		return map[string]any{}
	}
	if values, ok := details.(map[string]any); ok {
		return values
	}
	return map[string]any{}
}

func detailString(details map[string]any, key string) string {
	value, _ := details[key].(string)
	return value
}

func detailInt(details map[string]any, key string) int {
	switch value := details[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case uint64:
		return int(value)
	case uint:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func detailBool(details map[string]any, key string) bool {
	value, _ := details[key].(bool)
	return value
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

// externalEvidence extracts the first knowledge record from a tool result.
func externalEvidence(details map[string]any) *knowledge.Evidence {
	switch value := details["evidence"].(type) {
	case []knowledge.Evidence:
		if len(value) > 0 {
			return &value[0]
		}
	case knowledge.Evidence:
		return &value
	case []any:
		for _, entry := range value {
			if evidence, ok := entry.(knowledge.Evidence); ok {
				return &evidence
			}
		}
	}
	return nil
}

func isMissingFileError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "cannot find")
}

// classifyPathProvenance decides what a path observation can support.
//
// The path classification is owned by agent.ClassifyPath, so answer claim
// scope and evidence provenance cannot drift apart. Documentation is what the
// project says about itself; a manifest states dependencies; configuration
// declares intended topology; vendored and generated files are not the
// project's own implementation. Only source content can establish behavior.
func classifyPathProvenance(path string) Provenance {
	switch agent.ClassifyPath(path) {
	case agent.PathDocumentation:
		return ProvenanceDocumentation
	case agent.PathManifest:
		return ProvenanceManifest
	case agent.PathConfiguration:
		return ProvenanceConfiguration
	case agent.PathGenerated:
		return ProvenanceGenerated
	default:
		return ProvenanceObservation
	}
}

// isImplementationObservation reports whether an evidence item observed source
// content rather than a declaration about it.
//
// A Compose file, Makefile, dependency manifest, README, directory listing, or
// structural inventory describes intended topology and identity. None of them
// establishes that the implementation executes the described behavior. Only a
// source read or a source-scoped grep carries the content a behavior claim can
// be checked against.
func isImplementationObservation(item EvidenceItem) bool {
	if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
		return false
	}
	if strings.TrimSpace(item.Value) == "" {
		return false
	}
	if item.Provenance != ProvenanceObservation {
		return false
	}
	switch {
	case strings.HasPrefix(item.ID, "file:"):
		return agent.ClassifyPath(item.Source.Path) == agent.PathSource
	case strings.HasPrefix(item.ID, "search:grep:"):
		if matches, known := item.Counts["matches"]; (known && matches == 0) ||
			searchResultHasNoMatches(item.Value) {
			// No matched source line was captured. The result is a diagnostic
			// notice, not an implementation observation.
			return false
		}
		path := strings.TrimSpace(item.Source.Path)
		if path == "" || path == "." || path == "./" {
			return false
		}
		switch agent.ClassifyPath(path) {
		case agent.PathDocumentation, agent.PathManifest, agent.PathConfiguration, agent.PathGenerated:
			return false
		default:
			return true
		}
	default:
		return false
	}
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

// stripReadLinePrefix removes the "123\t" prefix the read tool puts on every
// line of an observation. The stored evidence value is that display text, so a
// parser of the document's prose must see the line, not its gutter.
func stripReadLinePrefix(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits >= len(trimmed) || trimmed[digits] != '\t' {
		return line
	}
	return trimmed[digits+1:]
}

// stripReadLineNumbers applies stripReadLinePrefix to every line.
func stripReadLineNumbers(text string) string {
	if !strings.Contains(text, "\t") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = stripReadLinePrefix(line)
	}
	return strings.Join(lines, "\n")
}

// firstContentLine returns the first line of a read result without its line
// number prefix, so a summary names what the file is rather than "1\tpackage".
func firstContentLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(stripReadLinePrefix(line))
		if line == "" || strings.HasPrefix(line, "[showing ") {
			continue
		}
		return line
	}
	return ""
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func bound(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && (text[cut]&0xc0) == 0x80 {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func itoa(value int) string { return fmt.Sprintf("%d", value) }

// coordinateMaps accepts the slice shape the symbols tool returns and the
// generic shape a decoded payload may carry.
func coordinateMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if item, ok := entry.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
