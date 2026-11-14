package cognition

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// CompactionStateBounds bound the durable-state projection a compaction
// reconstructs from. They are deliberately larger than the per-turn runtime
// state block: after compaction this projection is the primary brief, so it
// must carry the objective, constraints, verified facts, changes, verification,
// and the next action without turning into a second transcript.
const (
	// CompactionBlockBytes bounds the reconstructed durable-state blocks.
	CompactionBlockBytes = 12 * 1024
	// CompactionEvidenceItems bounds evidence lines in the reconstruction.
	CompactionEvidenceItems = 64
)

// CompactionProjection builds the authoritative Tier-0/1 context a state-first
// compaction reconstructs from.
//
// It is deterministic: the same shape, evidence, completion state, durable
// blocks, and next action always produce the same projection. It never
// contains assistant prose or a model summary, so compaction cannot upgrade a
// hypothesis into a fact or change authority/completion.
//
// blocks is the control/continuity plane's durable orientation block list (the
// same blocks the task checkpoint renders). When nil, the projection renders a
// bounded brief from shape and evidence. completionState is the authoritative
// objective state (PROVEN, NEEDS_REVIEW, IN_PROGRESS, …) and is preserved
// verbatim.
func CompactionProjection(shape TaskShape, evidence *EvidenceState, completionState, nextAction string, blocks []string) agent.ContextProjection {
	authority := compactionAuthority(shape)
	if len(blocks) == 0 {
		blocks = compactionBlocks(shape, evidence, nextAction)
	}
	return agent.ContextProjection{
		Authority:  authority,
		Objective:  shape.Objective,
		Completion: completionState,
		Blocks:     blocks,
		Counts:     CompactionCounts(shape, evidence),
	}
}

// compactionAuthority renders the live authority as a fact.
func compactionAuthority(shape TaskShape) string {
	parts := []string{shape.Authority.Workspace}
	if shape.Authority.Workspace == "" {
		parts = []string{"unresolved"}
	}
	if shape.Authority.ReadOnly {
		parts = append(parts, "read-only")
	}
	if shape.Authority.MutationAllowed {
		parts = append(parts, "mutation permitted")
	} else {
		parts = append(parts, "mutation forbidden")
	}
	if shape.Authority.ExecutionAllowed {
		parts = append(parts, "execution permitted")
	} else {
		parts = append(parts, "execution forbidden")
	}
	return strings.Join(parts, " · ")
}

// compactionBlocks renders the durable brief from shape and evidence.
func compactionBlocks(shape TaskShape, evidence *EvidenceState, nextAction string) []string {
	var blocks []string
	var contract strings.Builder
	fmt.Fprintf(&contract, "Task shape: %s", shape.Summary())
	if len(shape.Constraints) > 0 {
		fmt.Fprintf(&contract, "\nConstraints: %s", strings.Join(shape.Constraints, "; "))
	}
	if len(shape.NamedEntities) > 0 {
		fmt.Fprintf(&contract, "\nNamed entities: %s", strings.Join(shape.NamedEntities, ", "))
	}
	if shape.RequestedAction != "" {
		fmt.Fprintf(&contract, "\nRequested action: %s", shape.RequestedAction)
	}
	if len(shape.Ambiguities) > 0 {
		fmt.Fprintf(&contract, "\nUnresolved: %s", strings.Join(shape.Ambiguities, "; "))
	}
	if len(shape.Verification) > 0 {
		fmt.Fprintf(&contract, "\nVerification required: %s", verificationSummary(shape.Verification))
	}
	if len(shape.Completion) > 0 {
		var conditions []string
		for _, condition := range shape.Completion {
			conditions = append(conditions, condition.Description)
		}
		fmt.Fprintf(&contract, "\nCompletion conditions: %s", strings.Join(conditions, "; "))
	}
	if contract.Len() > 0 {
		blocks = append(blocks, bound(contract.String(), 2400))
	}

	if evidence != nil {
		items := append([]EvidenceItem(nil), evidence.Items...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].Seq < items[j].Seq })
		var facts strings.Builder
		shown := 0
		for _, item := range items {
			if item.Status == StatusUnknown {
				continue
			}
			if shown >= CompactionEvidenceItems {
				facts.WriteString("\n…[evidence bounded]")
				break
			}
			if shown == 0 {
				facts.WriteString("Evidence (status, provenance, summary):")
			}
			fmt.Fprintf(&facts, "\n- %s [%s/%s] %s",
				bound(item.ID, 120), item.Status, compactionProvenance(item), bound(item.Summary, 360))
			shown++
		}
		if facts.Len() > 0 {
			blocks = append(blocks, bound(facts.String(), CompactionBlockBytes))
		}
	}

	if strings.TrimSpace(nextAction) != "" {
		blocks = append(blocks, "Next action: "+bound(nextAction, 600))
	}
	if len(blocks) == 0 {
		// A projection with no durable content would validate but reconstruct
		// nothing. The objective and authority headers are always present, so
		// this only guards a caller that passed an empty shape.
		blocks = append(blocks, "Task state: no durable records are available beyond the objective and authority.")
	}
	return blocks
}

// compactionProvenance names what one observation observes.
func compactionProvenance(item EvidenceItem) string {
	if item.Source.Check != "" || strings.HasPrefix(item.ID, "verification:") {
		return "verification"
	}
	if item.Provenance != "" {
		return string(item.Provenance)
	}
	return "observation"
}

// CompactionCounts reports what a projection preserves, from runtime state.
func CompactionCounts(shape TaskShape, evidence *EvidenceState) agent.PreservationCounts {
	counts := agent.PreservationCounts{
		Constraints: len(shape.Constraints),
		Completion:  len(shape.Completion),
	}
	if evidence != nil {
		for _, item := range evidence.Items {
			if item.Status == StatusUnknown {
				continue
			}
			counts.Evidence++
			if strings.HasPrefix(item.ID, "mutation:") {
				counts.Changes++
			}
			if item.Source.Check != "" || strings.HasPrefix(item.ID, "verification:") {
				counts.Verifications++
			}
		}
	}
	return counts
}

// ValidateCompactionProjection rejects a candidate that does not preserve the
// authoritative fields of the baseline. It is the control-plane trust boundary
// for compaction: a projection may add bounded evidence detail, but it may
// never change authority, objective, or completion.
func ValidateCompactionProjection(candidate, baseline agent.ContextProjection) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Authority != baseline.Authority {
		return fmt.Errorf("cognition: compaction changed authority from %q to %q", baseline.Authority, candidate.Authority)
	}
	if candidate.Objective != baseline.Objective {
		return fmt.Errorf("cognition: compaction changed the objective")
	}
	if candidate.Completion != baseline.Completion {
		return fmt.Errorf("cognition: compaction changed completion state from %q to %q", baseline.Completion, candidate.Completion)
	}
	return nil
}
