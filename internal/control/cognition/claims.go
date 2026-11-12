package cognition

import "strings"

// ClaimSummary is what the evidence state can truthfully support on the answer
// side. It is derived from the same observations the completion gate uses, so
// the model's pre-answer briefing and the final review cannot disagree.
//
// The classes are evidence classes, not confidence levels: a configured port
// and an implemented listener are different observations, and an absence claim
// needs a complete filtered inventory, not a zero in a metadata overview.
type ClaimSummary struct {
	// HasImplementation reports source content (a source read or source-scoped
	// grep), not a manifest, configuration, documentation, or inventory.
	HasImplementation bool
	// HasConfiguration reports a configuration or manifest observation. It
	// declares topology or dependencies; it does not prove execution.
	HasConfiguration bool
	// HasDocumentation reports a prose observation.
	HasDocumentation bool
	// HasRuntime reports a live runtime observation.
	HasRuntime bool
	// StructuralScope is COMPLETE, PARTIAL, or "" when no structural
	// observation exists.
	StructuralScope string
	// TestInventory is COMPLETE, PARTIAL, or NOT_QUERIED. Only COMPLETE with
	// zero test files can support a test-file absence claim.
	TestInventory string
	// TestFiles is the snapshot test-file count when the inventory reported it.
	TestFiles int
	// TestFilesKnown reports that TestFiles is an observation rather than a
	// zero-value default.
	TestFilesKnown bool
	// TestTotal is the test-symbol count the filtered inventory returned.
	TestTotal int
	// TestTotalKnown reports that TestTotal is an observation.
	TestTotalKnown bool
	// ImplementationPaths, ConfigurationPaths, and DocumentationPaths are
	// bounded coordinates the model can cite. They are evidence, not authority.
	ImplementationPaths []string
	ConfigurationPaths  []string
	DocumentationPaths  []string
}

// claimPathLimit bounds each model-facing path list. The evidence block already
// carries the coordinates; this list is a reminder of the observed class.
const claimPathLimit = 3

// ClaimSummary derives the answer-side claim classes from the evidence state.
func (s *EvidenceState) ClaimSummary() ClaimSummary {
	summary := ClaimSummary{TestInventory: "NOT_QUERIED"}
	if s == nil {
		return summary
	}
	scopeSeq := -1
	for _, item := range s.Items {
		if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
			continue
		}
		if item.Runtime {
			summary.HasRuntime = true
		}
		// A later current structural query describes the snapshot. A stale
		// observation is evidence of what was true, not proof of what is true
		// now, so it cannot support an absence claim.
		current := item.Source.GraphState == "CURRENT"
		if current && item.Source.ScopeCompleteness != "" && item.Seq >= scopeSeq {
			scopeSeq = item.Seq
			summary.StructuralScope = item.Source.ScopeCompleteness
		}
		if current && item.ID == "structure:tests" {
			summary.TestInventory = testInventoryState(item)
			if value, ok := item.Counts["test_files"]; ok {
				summary.TestFiles = value
				summary.TestFilesKnown = true
			}
			if value, ok := item.Counts["total"]; ok {
				summary.TestTotal = value
				summary.TestTotalKnown = true
			}
		}
		switch item.Provenance {
		case ProvenanceDocumentation:
			if !claimFileContent(item) {
				continue
			}
			summary.HasDocumentation = true
			summary.DocumentationPaths = appendClaimPath(summary.DocumentationPaths, item.Source.Path)
		case ProvenanceConfiguration, ProvenanceManifest:
			if !claimFileContent(item) {
				continue
			}
			summary.HasConfiguration = true
			summary.ConfigurationPaths = appendClaimPath(summary.ConfigurationPaths, item.Source.Path)
		case ProvenanceObservation:
			if !isImplementationObservation(item) {
				continue
			}
			summary.HasImplementation = true
			summary.ImplementationPaths = appendClaimPath(summary.ImplementationPaths, item.Source.Path)
		}
	}
	return summary
}

// claimFileContent reports a direct file read that carried content. A search
// hit, structural coordinate, or missing-file observation does not establish
// what the file says.
func claimFileContent(item EvidenceItem) bool {
	return strings.HasPrefix(item.ID, "file:") && strings.TrimSpace(item.Value) != ""
}

// testInventoryState reports whether one structure tests observation proves
// absence. It requires scope, inventory, and structural completeness plus an
// untruncated result; the completion layer applies the same axes.
func testInventoryState(item EvidenceItem) string {
	if item.Complete &&
		item.Source.ScopeCompleteness == "COMPLETE" &&
		item.Source.Completeness == "COMPLETE" &&
		!item.Source.Truncated {
		return "COMPLETE"
	}
	return "PARTIAL"
}

// appendClaimPath retains a bounded, duplicate-free coordinate list. A
// directory listing has no source coordinate and is skipped; it is structure,
// not implementation.
func appendClaimPath(paths []string, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || path == "./" {
		return paths
	}
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	if len(paths) >= claimPathLimit {
		return paths
	}
	return append(paths, path)
}
