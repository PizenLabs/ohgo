package commit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/vcs"
)

// Plan is the deterministic logical commit plan for the whole change universe.
//
// It is a proposal, not repository truth: Git remains authoritative, so every
// group is revalidated against a fresh fingerprint before it is proposed and
// the plan is rebuilt after every commit.
type Plan struct {
	// Root, Branch, Head, and Upstream are the repository context.
	Root     string
	Branch   string
	Head     string
	Upstream string
	// Fingerprint is the complete repository state the plan was built from.
	Fingerprint string
	// Objective is the task objective used as grouping evidence only.
	Objective string
	// Groups are the committable logical changes in deterministic order.
	Groups []ChangeGroup
	// Protected, Blocked, Unrelated, Ambiguous, and Insufficient account for
	// every changed path that is not in a group. Ignored and Internal are the
	// repository facts that never become candidates.
	Protected    []FileChange
	Blocked      []FileChange
	Unrelated    []FileChange
	Ambiguous    []FileChange
	Insufficient []FileChange
	Ignored      []FileChange
	Internal     []FileChange
	// Changes is every relevant changed path in the plan's universe.
	Changes []FileChange
	// Total is len(Changes).
	Total int
	// StructuralEvidence names the grouping evidence actually consulted.
	StructuralEvidence string
	// Recent and Style are the bounded convention sample.
	Recent []CommitSummary
	Style  CommitStyle
}

// CommitPlan is the descriptive alias for the whole logical commit plan.
type CommitPlan = Plan

// Group returns the planned group with the given key.
func (p *Plan) Group(key string) (ChangeGroup, bool) {
	if p == nil {
		return ChangeGroup{}, false
	}
	for _, group := range p.Groups {
		if group.Key == key {
			return group, true
		}
	}
	return ChangeGroup{}, false
}

// Plan inspects the whole repository and builds a complete logical commit plan.
// It is read-only: no path is staged and the model is never called.
func (s *Service) Plan(ctx context.Context) (*Plan, error) {
	repo, err := vcs.Discover(s.Root)
	if err != nil {
		return nil, &PhaseError{Phase: PhaseInspection,
			Cause: fmt.Errorf("%w: %w", ErrRepository, err)}
	}
	if repo == nil {
		return nil, &PhaseError{Phase: PhaseInspection,
			Cause: fmt.Errorf("%w: %s is not inside a Git repository", ErrRepository, s.Root)}
	}
	snapshot, err := buildSnapshot(ctx, repo, snapshotRequest{
		Objective:  s.Objective,
		Selection:  SelectionPlan,
		Structural: s.Structural,
	})
	if err != nil {
		return nil, &PhaseError{Phase: phaseForSnapshotError(err), Cause: err}
	}
	context := snapshot.Context
	if context == nil {
		return nil, &PhaseError{Phase: PhaseClassification, Cause: ErrGrouping}
	}

	plan := &Plan{
		Root:               repo.Root,
		Branch:             snapshot.Branch,
		Head:               snapshot.Head,
		Upstream:           context.Repository.Upstream,
		Fingerprint:        snapshot.Fingerprint,
		Objective:          context.Objective,
		Protected:          append([]FileChange(nil), context.Protected...),
		Blocked:            append([]FileChange(nil), context.Blocked...),
		Unrelated:          unrelatedChanges(context.Excluded),
		Ambiguous:          append([]FileChange(nil), context.Ambiguous...),
		Insufficient:       append([]FileChange(nil), context.Insufficient...),
		Ignored:            append([]FileChange(nil), context.Ignored...),
		Internal:           append([]FileChange(nil), context.Internal...),
		Changes:            append([]FileChange(nil), context.Changes...),
		Total:              len(context.Changes),
		StructuralEvidence: context.StructuralEvidence,
		Recent:             append([]CommitSummary(nil), context.Recent...),
		Style:              context.Style,
	}
	for _, group := range context.Groups {
		// Non-committable groups are already represented in the ambiguous or
		// insufficient slices; the accounting slices remain the single source
		// of truth.
		if group.Confidence.Committable() {
			plan.Groups = append(plan.Groups, group)
		}
	}
	sort.Slice(plan.Groups, func(i, j int) bool {
		if plan.Groups[i].Order != plan.Groups[j].Order {
			return plan.Groups[i].Order < plan.Groups[j].Order
		}
		return plan.Groups[i].Key < plan.Groups[j].Key
	})
	// Re-number after filtering so progress is 1..N even when an ambiguous
	// group was removed from the executable plan.
	for i := range plan.Groups {
		plan.Groups[i].Order = i + 1
		plan.Groups[i].Selected = false
	}

	if err := plan.validateAccounting(); err != nil {
		return nil, &PhaseError{Phase: PhaseClassification, Cause: err}
	}
	if plan.Total == 0 {
		return nil, &PhaseError{Phase: PhaseClassification, Cause: ErrNoChanges}
	}
	if err := plan.validateStagedState(snapshot); err != nil {
		return nil, &PhaseError{Phase: PhaseClassification, Cause: err}
	}
	return plan, nil
}

// validateAccounting enforces the plan invariant: every relevant changed path
// is in exactly one group or explicitly non-committable state.
func (p *Plan) validateAccounting() error {
	grouped := 0
	seen := make(map[string]bool)
	for _, group := range p.Groups {
		for _, file := range group.Files {
			if seen[file.Path] {
				return fmt.Errorf("%w: %s appears in more than one logical change",
					ErrGrouping, safePath(file.Path))
			}
			seen[file.Path] = true
			grouped++
		}
	}
	for _, list := range [][]FileChange{
		p.Protected, p.Blocked, p.Unrelated, p.Ambiguous, p.Insufficient,
	} {
		for _, file := range list {
			if seen[file.Path] {
				return fmt.Errorf("%w: %s appears in more than one state",
					ErrGrouping, safePath(file.Path))
			}
			seen[file.Path] = true
		}
	}
	accounted := len(seen)
	if accounted != p.Total {
		return fmt.Errorf("%w: %d of %d changed paths were accounted for",
			ErrGrouping, accounted, p.Total)
	}
	return nil
}

// validateStagedState keeps /commit -a safe when the index already contains a
// human staging decision. The staged-index path commits the whole index, so a
// staged set that spans logical changes is a decision, not something to absorb.
func (p *Plan) validateStagedState(snapshot *Snapshot) error {
	if snapshot == nil || snapshot.Context == nil || snapshot.Mode != ModeStaged {
		return nil
	}
	for _, change := range p.Changes {
		if change.Kind == KindPartiallyStaged {
			return fmt.Errorf("%w: %s is partially staged; commit or unstage it before /commit -a",
				ErrNeedsDecision, change.Display())
		}
	}
	logical := 0
	for _, group := range snapshot.Context.Groups {
		for _, file := range group.Files {
			if file.Staged() {
				logical++
				break
			}
		}
	}
	if logical > 1 {
		return fmt.Errorf("%w: the index contains %d logical changes; commit the staged index with /commit or unstage it before /commit -a",
			ErrNeedsDecision, logical)
	}
	return nil
}

// unrelatedChanges returns only the explicitly unrelated projection. Excluded
// also carries blocked and insufficient paths, which have their own slices.
func unrelatedChanges(changes []FileChange) []FileChange {
	var out []FileChange
	for _, change := range changes {
		if change.Category == CategoryUnrelated {
			out = append(out, change)
		}
	}
	return out
}

// ProposeGroup asks the COMMIT model for a message for exactly one planned
// logical change. The plan is revalidated against current Git state first.
func (s *Service) ProposeGroup(ctx context.Context, plan *Plan, key string) (*Request, error) {
	if plan == nil {
		return nil, ErrPlanStale
	}
	group, ok := plan.Group(key)
	if !ok {
		return nil, ErrPlanStale
	}
	progress := &PlanProgress{
		Position: group.Order,
		Total:    len(plan.Groups),
		Key:      group.Key,
	}
	return s.propose(ctx, snapshotRequest{
		Objective:       s.Objective,
		Selection:       SelectionGroup,
		GroupKey:        group.Key,
		ExpectedPaths:   groupPaths(group),
		PlanFingerprint: plan.Fingerprint,
		Structural:      s.Structural,
	}, progress)
}

// PlanSummary is the compact accounting line shown when a plan starts.
func PlanSummary(plan *Plan) string {
	if plan == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("%d logical %s", len(plan.Groups), plural("commit", len(plan.Groups))),
		fmt.Sprintf("%d changed %s", plan.Total, plural("path", plan.Total)),
	}
	add := func(label string, n int) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add("protected", len(plan.Protected))
	add("blocked", len(plan.Blocked))
	add("ambiguous", len(plan.Ambiguous))
	add("insufficient evidence", len(plan.Insufficient))
	add("unrelated", len(plan.Unrelated))
	return strings.Join(parts, " · ")
}

// FormatPlan renders the complete plan for the transcript. It never reads file
// content: paths and evidence labels are control-plane facts.
func FormatPlan(plan *Plan) string {
	if plan == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Commit plan\n\n")
	sb.WriteString(PlanSummary(plan))
	if len(plan.Groups) > 0 {
		sb.WriteString("\n\n")
		for _, group := range plan.Groups {
			fmt.Fprintf(&sb, "%d. %s (%d %s)\n",
				group.Order, safePath(group.Label), len(group.Files), plural("file", len(group.Files)))
		}
	}
	writePlanList := func(label string, changes []FileChange) {
		if len(changes) == 0 {
			return
		}
		sb.WriteString("\n" + label + ":")
		for i, change := range changes {
			if i == 8 {
				fmt.Fprintf(&sb, "\n  … %d more", len(changes)-i)
				break
			}
			sb.WriteString("\n  " + change.Display())
		}
	}
	writePlanList("Protected", plan.Protected)
	writePlanList("Blocked", plan.Blocked)
	writePlanList("Ambiguous", plan.Ambiguous)
	writePlanList("Insufficient evidence", plan.Insufficient)
	writePlanList("Unrelated", plan.Unrelated)
	writePlanList("Internal state (excluded)", plan.Internal)
	if plan.StructuralEvidence != "" {
		sb.WriteString("\n\ngrouping evidence: " + plan.StructuralEvidence)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatPlanFrom renders a residual plan with its global commit numbers. The
// plan itself is not mutated: its orders remain the local 1..N sequence that
// ProposeGroup reports.
func FormatPlanFrom(plan *Plan, offset int) string {
	if plan == nil {
		return ""
	}
	clone := *plan
	clone.Groups = append([]ChangeGroup(nil), plan.Groups...)
	for i := range clone.Groups {
		clone.Groups[i].Order += offset
	}
	return FormatPlan(&clone)
}

// FormatProposalProgress renders the one progress line the existing proposal
// dialog needs. A normal proposal returns "".
func FormatProposalProgress(request *Request) string {
	if request == nil || request.Progress == nil {
		return ""
	}
	return fmt.Sprintf("Commit %d / %d", request.Progress.Position, request.Progress.Total)
}

// RemainingGroups returns the plans after the given key, used by hosts that
// want to report partial completion without re-reading the repository.
func RemainingGroups(plan *Plan, key string) []ChangeGroup {
	if plan == nil {
		return nil
	}
	index := -1
	for i, group := range plan.Groups {
		if group.Key == key {
			index = i
			break
		}
	}
	if index < 0 || index+1 >= len(plan.Groups) {
		return nil
	}
	return append([]ChangeGroup(nil), plan.Groups[index+1:]...)
}
