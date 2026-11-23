package cognition

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// EvidenceFacet is one kind of evidence a task's intent requires.
//
// A facet is not a claim class. A claim class says what an answer sentence may
// assert; a facet says which observations the control plane requires before
// the task may reach an answer boundary. The distinction is what keeps a
// relationship question from closing after a lexical search that merely
// mentions both endpoints.
type EvidenceFacet string

// Evidence facets.
const (
	// FacetTarget reports that the entity or module under investigation was
	// identified in a current structural observation.
	FacetTarget EvidenceFacet = "target"
	// FacetRelationship reports a structural edge or reference binding two
	// endpoints. Lexical co-occurrence and source prose are not edges.
	FacetRelationship EvidenceFacet = "relationship"
	// FacetInterfaceBoundary reports an observed interface/implementation or
	// module boundary.
	FacetInterfaceBoundary EvidenceFacet = "interface_boundary"
	// FacetImplementation reports observed implementation source or a concrete
	// IMPLEMENTS/OVERRIDES edge.
	FacetImplementation EvidenceFacet = "implementation"
	// FacetFlow reports an observed call/reference chain from the target.
	FacetFlow EvidenceFacet = "flow"
)

// Facet state.
const (
	// FacetSatisfied means the required observation exists and is current.
	FacetSatisfied = "SATISFIED"
	// FacetMissing means the observation the facet requires has not been made.
	FacetMissing = "MISSING"
)

// FacetRequirement is one required evidence facet.
type FacetRequirement struct {
	// Facet is the evidence kind.
	Facet EvidenceFacet `json:"facet"`
	// Key is the stable requirement key, "facet:<facet>".
	Key string `json:"key"`
	// Reason explains why the intent needs it.
	Reason string `json:"reason,omitempty"`
}

// FacetStatus is the control-plane projection of one required facet.
type FacetStatus struct {
	// Facet is the evidence kind.
	Facet EvidenceFacet `json:"facet"`
	// Key is the requirement key.
	Key string `json:"key"`
	// State is SATISFIED or MISSING.
	State string `json:"state"`
	// Detail names the missing observation.
	Detail string `json:"detail,omitempty"`
	// Evidence is a bounded, auditable list of the observations that satisfy
	// the facet. It is provenance for the control plane, not answer text.
	Evidence []string `json:"evidence,omitempty"`
}

// FacetKey returns the requirement key for a facet.
func FacetKey(facet EvidenceFacet) string { return "facet:" + string(facet) }

// deriveRequiredFacets is the deterministic investigation-requirements policy.
//
// It reads the already-derived task shape, never a model proposal. The policy
// is deliberately narrow: only a relationship or code-navigation request
// acquires structural facets. A conversational or pure documentation request
// does not become a structural task merely because the tool exists.
func deriveRequiredFacets(shape TaskShape, intent contract.IntentContract) []FacetRequirement {
	if shape.Kind != contract.ObjectiveInformational {
		return nil
	}
	normalized := " " + normalizeWords(shape.Objective) + " "
	if !explicitRelationshipIntent(normalized) {
		return nil
	}
	// A definition question such as "what is an interface?" is general
	// knowledge, not a request to traverse this repository. A repository
	// subject or an explicit target keeps a definition-shaped request local.
	if definitionQuestion(normalized) && !shape.RepositoryScope && len(intent.Targets) == 0 {
		return nil
	}
	var out []FacetRequirement
	add := func(facet EvidenceFacet, reason string) {
		for _, existing := range out {
			if existing.Facet == facet {
				return
			}
		}
		out = append(out, FacetRequirement{Facet: facet, Key: FacetKey(facet), Reason: reason})
	}
	add(FacetTarget, "a relationship question must identify the entity or module whose edges are being traced")
	add(FacetRelationship, "a dependency, call, import, reference, or implementation claim needs an observed structural edge; lexical co-occurrence is not a relationship")
	if containsAnyPhrase(normalized, []string{
		" interface ", " interfaces ", " implement ", " implements ", " implemented ",
		" implementation of ", " adapter ", " adapters ", " boundary ", " boundaries ",
	}) {
		add(FacetInterfaceBoundary, "an interface or implementation claim needs an observed interface/implementation or module boundary")
		add(FacetImplementation, "an interface or implementation claim needs observed implementation source or a concrete IMPLEMENTS/OVERRIDES edge")
	}
	if containsAnyPhrase(normalized, []string{
		" request flow ", " data flow ", " data flows ", " call chain ", " call graph ",
		" reaches ", " reach ", " end-to-end ", " end to end ",
	}) {
		add(FacetFlow, "a flow claim needs an observed call/reference chain, not only a file name or documentation statement")
	}
	return out
}

// definitionQuestion reports a general-knowledge definition shape. It is used
// only to keep a relationship word such as "interface" from turning a
// definitional question into repository work.
func definitionQuestion(normalized string) bool {
	trimmed := strings.TrimSpace(normalized)
	return strings.HasPrefix(trimmed, "what is ") || strings.HasPrefix(trimmed, "what are ") ||
		strings.Contains(trimmed, " what is ") || strings.Contains(trimmed, " what are ")
}

// explicitRelationshipIntent is the boundary between a broad account and a
// relationship claim. "Inspect the architecture" is an overview; "trace the
// architecture dependencies" and "who calls parseConfig" are structural
// questions. The vocabulary is intentionally explicit so a broad overview does
// not acquire a mandatory edge merely because it says "architecture".
func explicitRelationshipIntent(normalized string) bool {
	return containsAnyPhrase(normalized, []string{
		" dependency ", " dependencies ", " depends ", " depending ", " depend ",
		" caller ", " callers ", " callee ", " callees ", " call graph ",
		" call chain ", " who calls ", " calls ", " called ", " calling ",
		" calls into ", " called by ",
		" import ", " imports ", " imported ",
		" reference ", " references ", " referenced ",
		" implements ", " implemented by ", " implementation of ",
		" interface ", " interfaces ", " adapter ", " adapters ",
		" impact ", " affected ",
		" request flow ", " data flow ", " end-to-end ", " end to end ",
		" reaches ", " reach ",
	})
}

// facetSatisfied reports whether one facet has a current observation.
func (s *EvidenceState) facetSatisfied(facet EvidenceFacet) bool {
	return len(s.facetEvidence(facet)) > 0
}

// facetEvidence returns the current observations that satisfy one facet.
func (s *EvidenceState) facetEvidence(facet EvidenceFacet) []string {
	if s == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range s.Items {
		if item.Status == StatusRequired || item.Status == StatusUnknown || item.Status == StatusConflicting {
			continue
		}
		if !itemSatisfiesFacet(item, facet) {
			continue
		}
		line := facetEvidenceLine(item)
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// facetStatuses projects every required facet and its evidence.
func facetStatuses(shape TaskShape, evidence *EvidenceState) []FacetStatus {
	if len(shape.RequiredFacets) == 0 {
		return nil
	}
	out := make([]FacetStatus, 0, len(shape.RequiredFacets))
	for _, requirement := range shape.RequiredFacets {
		status := FacetStatus{
			Facet: requirement.Facet,
			Key:   requirement.Key,
			State: FacetMissing,
		}
		if evidence != nil {
			status.Evidence = evidence.facetEvidence(requirement.Facet)
			if len(status.Evidence) > 0 {
				status.State = FacetSatisfied
			}
		}
		if status.State != FacetSatisfied {
			status.Detail = facetMissingDetail(requirement.Facet)
		}
		out = append(out, status)
	}
	return out
}

// facetMissingDetail names the acquisition that would satisfy a missing facet.
func facetMissingDetail(facet EvidenceFacet) string {
	switch facet {
	case FacetTarget:
		return "no current structural observation identifies the target; query structure files, packages, symbols, relations, or analysis"
	case FacetRelationship:
		return "no current structural edge or reference binds the endpoints; source text that mentions both is lexical evidence, not a relationship"
	case FacetInterfaceBoundary:
		return "no current interface/implementation or module boundary was observed; query structure relations or analysis for IMPLEMENTS/EXTENDS or boundary evidence"
	case FacetImplementation:
		return "no implementation source or concrete IMPLEMENTS/OVERRIDES edge was observed; read the implementation or query the relevant structural relation"
	case FacetFlow:
		return "no current call/reference chain from the target was observed; query structure relations or analysis with the target and a call depth"
	default:
		return "required evidence facet has not been observed"
	}
}

// facetReportRows formats facet status for the projection audit.
func facetReportRows(statuses []FacetStatus) []string {
	if len(statuses) == 0 {
		return nil
	}
	out := make([]string, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, fmt.Sprintf("%s=%s", status.Facet, status.State))
	}
	return out
}

// facetEvidenceLine names one observation and its provenance, bounded.
func facetEvidenceLine(item EvidenceItem) string {
	source := item.Source
	line := item.ID
	if source.Path != "" {
		line += "@" + source.Path
		if source.StartLine > 0 {
			line += ":" + itoa(source.StartLine)
		}
	}
	if source.SnapshotID != "" {
		line += "#" + source.SnapshotID
	}
	return bound(line, 180)
}

// itemSatisfiesFacet is the one facet-to-observation rule.
//
// The explicit Facets field is written by the observation producer from
// structured result fields. The fallbacks keep checkpoints written before the
// facet field readable without reinterpreting their prose.
func itemSatisfiesFacet(item EvidenceItem, facet EvidenceFacet) bool {
	if itemHasFacet(item, facet) {
		return true
	}
	switch facet {
	case FacetTarget:
		return structuralTargetEvidence(item)
	case FacetRelationship:
		return relationshipTextEvidence(item)
	case FacetInterfaceBoundary:
		return boundaryTextEvidence(item)
	case FacetImplementation:
		if isImplementationObservation(item) {
			return true
		}
		class := item.EvidenceClass()
		return class == EvidenceSourceContent || class == EvidenceSourceImport || class == EvidenceSourceUse
	case FacetFlow:
		return flowTextEvidence(item)
	default:
		return false
	}
}

// itemHasFacet reports whether the producer tagged the observation with facet.
func itemHasFacet(item EvidenceItem, facet EvidenceFacet) bool {
	for _, observed := range item.Facets {
		if observed == facet {
			return true
		}
	}
	return false
}

// structuralTargetEvidence reports a structural coordinate that identifies a
// target. A path-only inventory does not identify the entity in a relationship
// question.
func structuralTargetEvidence(item EvidenceItem) bool {
	switch {
	case strings.HasPrefix(item.ID, "symbol:"),
		strings.HasPrefix(item.ID, "package:"),
		strings.HasPrefix(item.ID, "entrypoint:"),
		strings.HasPrefix(item.ID, "graph_edge:"),
		strings.HasPrefix(item.ID, "call:"),
		strings.HasPrefix(item.ID, "ref:"):
		return true
	}
	return false
}

// relationshipTextEvidence is the legacy fallback for structural observations
// that predate the Facets field. It never promotes a source read or a lexical
// search: those have neither an edge id nor a structural view.
func relationshipTextEvidence(item EvidenceItem) bool {
	if !isStructuralObservation(item) {
		return false
	}
	text := strings.ToLower(item.Value)
	return strings.Contains(text, "kind: calls") || strings.Contains(text, "kind: references") ||
		strings.Contains(text, "kind: imports") || strings.Contains(text, "kind: implements") ||
		strings.Contains(text, "kind: extends") || strings.Contains(text, "kind: depends_on")
}

// boundaryTextEvidence is the legacy fallback for interface and module
// boundaries. It is intentionally narrower than relationship evidence.
func boundaryTextEvidence(item EvidenceItem) bool {
	if !isStructuralObservation(item) {
		return false
	}
	text := strings.ToLower(item.Summary + "\n" + item.Value)
	for _, marker := range []string{"implements", "interface", "boundary", "boundaries"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// flowTextEvidence is the legacy fallback for call/reference chains.
func flowTextEvidence(item EvidenceItem) bool {
	if !isStructuralObservation(item) {
		return false
	}
	if strings.HasPrefix(item.ID, "call:") {
		return true
	}
	text := strings.ToLower(item.Value)
	return strings.Contains(text, "kind: calls") || strings.Contains(text, "kind: references") ||
		strings.Contains(text, "caller") || strings.Contains(text, "callee")
}

// isStructuralObservation reports an item produced by a structural capability.
// Facet fallbacks must not promote a source read or lexical search merely
// because its text contains a relationship word.
func isStructuralObservation(item EvidenceItem) bool {
	switch item.Source.Tool {
	case "graph", "refs", "structure":
		return true
	}
	switch item.View {
	case "overview", "scope", "languages", "files", "packages", "entrypoints",
		"relations", "impact", "analysis", "tests", "symbols":
		return true
	}
	return false
}

// structuralFacets derives facet tags from a structural tool's structured
// result. The details map is the producer boundary; prose is never parsed here.
func structuralFacets(details map[string]any) []EvidenceFacet {
	edges := coordinateMaps(details["edges"])
	edgeKinds := structuralEdgeKinds(edges)
	focus := detailsMap(details["focus_report"])
	focusCalls := coordinateMaps(focus["calls"])
	importerPaths := stringValues(focus["importer_paths"])
	coverage := detailsMap(details["coverage"])
	boundaries := coordinateMaps(details["boundaries"])
	targets := coordinateMaps(details["targets"])
	nodes := coordinateMaps(details["nodes"])

	var out []EvidenceFacet
	add := func(facet EvidenceFacet) {
		for _, existing := range out {
			if existing == facet {
				return
			}
		}
		out = append(out, facet)
	}
	target := detailBool(details, "found") || len(targets) > 0 ||
		len(nodes) > 0 || detailInt(details, "node_total") > 0
	if target {
		add(FacetTarget)
	}
	relationship := hasRelationshipEdge(edgeKinds) ||
		detailInt(details, "total_edges") > 0 || detailInt(details, "total_references") > 0 ||
		detailInt(coverage, "graph_edges") > 0 || len(importerPaths) > 0 ||
		detailInt(focus, "edges") > 0 || detailInt(focus, "references") > 0 ||
		detailInt(focus, "callers") > 0 || detailInt(focus, "callees") > 0
	if relationship {
		add(FacetRelationship)
	}
	if len(boundaries) > 0 || edgeKinds["IMPLEMENTS"] || edgeKinds["EXTENDS"] || edgeKinds["OVERRIDES"] {
		add(FacetInterfaceBoundary)
	}
	if edgeKinds["IMPLEMENTS"] || edgeKinds["OVERRIDES"] {
		add(FacetImplementation)
	}
	if len(focusCalls) > 0 || edgeKinds["CALLS"] || edgeKinds["REFERENCES"] ||
		detailInt(focus, "callers") > 0 || detailInt(focus, "callees") > 0 {
		add(FacetFlow)
	}
	return out
}

// investigationFacets derives facet tags from the bounded investigation
// result. The structural provider's own facts are the evidence; the
// investigation's derived status is not treated as a structural edge.
func investigationFacets(details map[string]any) []EvidenceFacet {
	providers := coordinateMaps(details["providers"])
	structuralRan := false
	for _, step := range providers {
		if detailString(step, "provider") != "structural" {
			continue
		}
		switch strings.ToUpper(detailString(step, "outcome")) {
		case "SUCCESS", "PARTIAL":
			structuralRan = true
		}
	}
	if !structuralRan {
		return nil
	}
	var out []EvidenceFacet
	add := func(facet EvidenceFacet) {
		for _, existing := range out {
			if existing == facet {
				return
			}
		}
		out = append(out, facet)
	}
	add(FacetTarget)
	for _, fact := range coordinateMaps(details["facts"]) {
		subject := strings.ToLower(detailString(fact, "subject"))
		detail := strings.ToLower(detailString(fact, "detail"))
		text := subject + " " + detail
		if strings.Contains(text, "boundary") || strings.Contains(text, "crossings") ||
			strings.Contains(text, "implements") || strings.Contains(text, "interface") {
			add(FacetInterfaceBoundary)
			add(FacetRelationship)
		}
		if strings.Contains(text, "caller") || strings.Contains(text, "callee") ||
			strings.Contains(text, "importer") || strings.Contains(text, " call") ||
			strings.Contains(text, "reference") {
			add(FacetRelationship)
		}
		if strings.Contains(text, "caller") || strings.Contains(text, "callee") ||
			strings.Contains(text, " call") || strings.Contains(text, "reference") {
			add(FacetFlow)
		}
		if strings.Contains(text, "implements") || strings.Contains(text, "interface") {
			add(FacetImplementation)
		}
	}
	return out
}

// hasRelationshipEdge reports whether the result contains a binding edge. A
// CONTAINS/DEFINES edge is containment, not a dependency or call relationship.
func hasRelationshipEdge(kinds map[string]bool) bool {
	for kind := range kinds {
		switch strings.ToUpper(kind) {
		case "IMPORTS", "EXPORTS", "REFERENCES", "CALLS", "EXTENDS", "IMPLEMENTS", "OVERRIDES", "DEPENDS_ON":
			return true
		}
	}
	return false
}

// coordinateFacets derives facets from one structural coordinate. It keeps
// the coordinate's path and snapshot on the facet evidence, so an audit can
// trace a relationship to the file that produced it.
func coordinateFacets(kind, symbol string) []EvidenceFacet {
	kind = strings.ToLower(strings.TrimSpace(kind))
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	var out []EvidenceFacet
	add := func(facet EvidenceFacet) {
		for _, existing := range out {
			if existing == facet {
				return
			}
		}
		out = append(out, facet)
	}
	switch kind {
	case "target", "impact_target", "package", "entrypoint", "symbol":
		add(FacetTarget)
	case "call", "caller":
		add(FacetRelationship)
		add(FacetFlow)
	case "reference", "ref":
		add(FacetRelationship)
		add(FacetFlow)
	case "graph_edge":
		add(FacetRelationship)
	}
	for _, marker := range []string{"IMPLEMENTS ", "EXTENDS ", "OVERRIDES "} {
		if strings.HasPrefix(symbol, marker) {
			add(FacetInterfaceBoundary)
			if marker == "IMPLEMENTS " || marker == "OVERRIDES " {
				add(FacetImplementation)
			}
		}
	}
	for _, marker := range []string{"CALLS ", "REFERENCES "} {
		if strings.HasPrefix(symbol, marker) {
			add(FacetFlow)
		}
	}
	return out
}

// structuralEdgeKinds returns the canonical edge kinds in a structural result.
func structuralEdgeKinds(edges []map[string]any) map[string]bool {
	out := make(map[string]bool, len(edges))
	for _, edge := range edges {
		kind := strings.ToUpper(detailString(edge, "kind"))
		if kind == "" {
			continue
		}
		out[kind] = true
	}
	return out
}

// stringValues reads a string slice from a decoded details value.
func stringValues(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, entry := range values {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// facetCoverage returns the satisfied/total counts and a stable summary.
func facetCoverage(statuses []FacetStatus) (satisfied int, total int) {
	total = len(statuses)
	for _, status := range statuses {
		if status.State == FacetSatisfied {
			satisfied++
		}
	}
	return satisfied, total
}

// missingFacetLabels names the unsatisfied facets in requirement order.
func missingFacetLabels(statuses []FacetStatus) []string {
	var out []string
	for _, status := range statuses {
		if status.State != FacetSatisfied {
			out = append(out, string(status.Facet))
		}
	}
	return out
}
