package agent

import (
	"fmt"
	"strings"
)

// StructuralCoverage is the run's best structural observation, as the answer
// review and the TUI footer both understand it.
//
// "Best" means the observation that parsed the most source. A later
// inventory-only query must not erase a real coverage gap, and a complete
// parse clears one. It is derived from the producer's structured details, not
// from rendered prose, so the footer, the review, and the evidence ledger
// cannot disagree about whether a snapshot was partial.
type StructuralCoverage struct {
	// Seen reports at least one structural observation with coverage facts.
	Seen bool
	// Parsed, Pending, and Failed are the source-file counts the snapshot
	// reported. A missing count is zero in the aggregate, never an absence
	// claim.
	Parsed  int
	Pending int
	Failed  int
	// Truncated reports that the producing query hit its own bound.
	Truncated bool
	// StructuralSeen and StructuralComplete describe the parse state;
	// InventorySeen and InventoryComplete describe the file inventory.
	StructuralSeen     bool
	StructuralComplete bool
	InventorySeen      bool
	InventoryComplete  bool
}

// Complete reports that the observation covered the repository's source and
// inventory. An observation without coverage facts is never complete.
func (c StructuralCoverage) Complete() bool {
	if !c.Seen {
		return false
	}
	if c.Truncated || c.Failed > 0 || c.Pending > 0 {
		return false
	}
	if c.StructuralSeen && !c.StructuralComplete {
		return false
	}
	if c.InventorySeen && !c.InventoryComplete {
		return false
	}
	return true
}

// Gap returns the bounded review phrase for what the observation did not
// cover, or "" when it was complete or no coverage was reported.
func (c StructuralCoverage) Gap() string {
	if !c.Seen || c.Complete() {
		return ""
	}
	var parts []string
	if total := c.Parsed + c.Pending + c.Failed; total > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d source files parsed", c.Parsed, total))
	}
	if c.Truncated {
		parts = append(parts, "the inventory was truncated")
	} else if c.InventorySeen && !c.InventoryComplete {
		parts = append(parts, "the file inventory is partial")
	}
	if len(parts) == 0 && c.StructuralSeen && !c.StructuralComplete {
		parts = append(parts, "the structural index is incomplete")
	}
	return strings.Join(parts, "; ")
}

// ObserveStructuralCoverage folds a run's structural outcomes into the best
// coverage it observed. It reads the producer's structured details only; a
// rendered header is display text and must not decide what was observed.
func ObserveStructuralCoverage(turns []Turn) StructuralCoverage {
	best := StructuralCoverage{}
	bestParsed := -1
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if strings.ToLower(strings.TrimSpace(outcome.ToolName)) != "structure" {
				continue
			}
			details, ok := outcome.Result.Details.(map[string]any)
			if !ok {
				continue
			}
			coverage := structuralCoverageFromDetails(details)
			if !coverage.Seen {
				continue
			}
			if coverage.Complete() {
				// A complete parse is the strongest observation the run made.
				return coverage
			}
			if coverage.Parsed > bestParsed {
				best, bestParsed = coverage, coverage.Parsed
			}
		}
	}
	return best
}

// structuralCoverageFromDetails maps one structure result's provenance fields
// into coverage. A producer that reported no coverage fact is not "complete":
// it is unknown, and the review does not require a limitation the run cannot
// name.
func structuralCoverageFromDetails(details map[string]any) StructuralCoverage {
	coverage := StructuralCoverage{}
	if details == nil {
		return coverage
	}
	coverage.Parsed = detailValueInt(details, "parsed_files")
	coverage.Pending = detailValueInt(details, "pending_files")
	coverage.Failed = detailValueInt(details, "failed_files")
	coverage.Truncated = detailValueBool(details, "truncated") ||
		detailValueBool(details, "result_truncated")
	coverage.StructuralComplete, coverage.StructuralSeen = details["structural_complete"].(bool)
	coverage.InventoryComplete, coverage.InventorySeen = details["inventory_complete"].(bool)
	coverage.Seen = coverage.StructuralSeen || coverage.InventorySeen ||
		coverage.Parsed > 0 || coverage.Pending > 0 || coverage.Failed > 0 ||
		coverage.Truncated
	return coverage
}
