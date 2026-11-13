package commit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/vcs"
)

// Errors that stop /commit before it asks the model or mutates anything.
var (
	// ErrNoChanges means the repository has nothing ohgo may commit.
	ErrNoChanges = errors.New("commit: no changes to commit")
	// ErrStagedExcluded means the index already contains a path ohgo treats
	// as internal or ignored. Committing the index would include it, so the
	// only safe action is to stop and let the human decide.
	ErrStagedExcluded = errors.New("commit: staged changes include excluded paths")
	// ErrTooManyChanges keeps one reviewable proposal bounded.
	ErrTooManyChanges = errors.New("commit: too many changed paths")
	// ErrRepository means repository inspection itself failed. It is distinct
	// from a classification refusal and from a model failure.
	ErrRepository = errors.New("commit: repository inspection failed")
	// ErrBlockedChanges means the working tree is in a state /commit cannot
	// safely translate into a commit (an unresolved conflict, for example).
	// The human must resolve it; the system must not guess.
	ErrBlockedChanges = errors.New("commit: changes require a decision before committing")
	// ErrGrouping means the control plane failed to assign a committable
	// change to a logical group. It is an invariant failure, not a model
	// failure, and fails closed rather than merging the path silently.
	ErrGrouping = errors.New("commit: change grouping failed")
	// ErrNeedsDecision means no change could be safely classified as part of
	// the commit. Ambiguity is surfaced, never converted into inclusion.
	ErrNeedsDecision = errors.New("commit: no change could be safely classified")
)

// Bounds on the evidence sent to the commit model and on one proposal.
const (
	maxChangedPaths = 2000
	// maxDiffBytes bounds the model-visible diff. The deterministic diffstat
	// still names every changed path, so a truncated diff does not hide what
	// changed; it only stops the prompt from growing with the patch.
	maxDiffBytes      = 24 * 1024
	maxDiffStatBytes  = 8 * 1024
	maxUntrackedBytes = 4 * 1024
	// maxScopeLines bounds the readable path list. The diffstat remains the
	// complete per-path summary when the list is elided.
	maxScopeLines        = 100
	internalProjectDir   = ".ohgo"
	untrackedPreviewLine = 120
)

// Mode is how the exact commit scope was selected.
type Mode string

const (
	// ModeStaged commits the existing index only. It is chosen whenever the
	// index already has a change, because replacing a staged version with the
	// worktree version would silently discard the user's staging intent.
	ModeStaged Mode = "staged"
	// ModeWorktree stages every tracked modification and untracked file when
	// the index is clean.
	ModeWorktree Mode = "worktree"
)

// FileChange is one path in the repository state.
type FileChange struct {
	// Path is the current repository-relative path.
	Path string
	// OrigPath is the source path of a staged rename, or "".
	OrigPath string
	// Index and Worktree are the porcelain status bytes.
	Index byte
	// Worktree is the worktree status byte.
	Worktree byte
	// Untracked marks the "??" record.
	Untracked bool
	// Ignored marks the "!!" record.
	Ignored bool
	// Internal marks ohgo's own project state.
	Internal bool
	// Category is the control-plane commitability decision.
	Category ChangeCategory
	// Kind is the most specific observable change fact.
	Kind ChangeKind
	// Reason explains a non-committable category. It is control-plane text,
	// never model output.
	Reason string
	// Group is the logical change group this path belongs to.
	Group string
	// Role is the semantic path role used for type inference and grouping.
	Role kindRole
	// Sensitive reports credential material whose content must never be sent
	// to a provider. A deletion can be committed without exposing the bytes.
	Sensitive bool
	// Binary reports a non-text blob. It is separate from Kind so a renamed
	// or deleted binary keeps its observable operation.
	Binary bool
	// Added and Removed are diff churn, nil when unknown or binary.
	Added   *int
	Removed *int
}

// Code renders the status a reviewer should read for this entry.
func (c FileChange) Code() string {
	switch {
	case c.Untracked:
		return "??"
	case c.Ignored:
		return "!!"
	case c.Index != ' ' && c.Index != '?':
		return string(c.Index)
	case c.Worktree != ' ' && c.Worktree != '?':
		return string(c.Worktree)
	default:
		return " "
	}
}

// Display renders one proposal line, e.g. "M agent/loop.go".
func (c FileChange) Display() string {
	if c.OrigPath != "" {
		return c.Code() + " " + safePath(c.OrigPath) + " → " + safePath(c.Path)
	}
	return c.Code() + " " + safePath(c.Path)
}

// safePath keeps a repository path on one display line. Git permits newlines
// and tabs in a path; rendering them raw would let a filename forge evidence or
// a UI row.
func safePath(path string) string {
	if !strings.ContainsFunc(path, unicode.IsControl) {
		return path
	}
	var sb strings.Builder
	for _, r := range path {
		if unicode.IsControl(r) {
			fmt.Fprintf(&sb, `\x%02x`, r)
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Snapshot is the exact repository state a proposal was built from.
//
// It is also the concurrency guard: Fingerprint and WorktreeFingerprint must
// still match immediately before execution, or the commit is aborted rather
// than silently absorbing a change nobody approved.
type Snapshot struct {
	// Root is the repository checkout root.
	Root string
	// Branch is the checked-out branch, or "" when detached.
	Branch string
	// Head is the commit HEAD pointed at when the proposal was built.
	Head string
	// Mode is how Scope was selected.
	Mode Mode
	// Staged, Unstaged, and Untracked classify every relevant path.
	Staged    []FileChange
	Unstaged  []FileChange
	Untracked []FileChange
	// Ignored and Internal are shown but never committed.
	Ignored  []FileChange
	Internal []FileChange
	// Context is the authoritative classification, grouping, and convention
	// projection. Scope is a projection of its selected group(s).
	Context *CommitContext
	// Scope is the exact set of paths the human is approving.
	Scope []FileChange
	// Auto reports that this snapshot is one proposal inside an automatic
	// plan. Other logical changes are deferred, not unrelated.
	Auto bool
	// Expected maps every approved path to its expected index or worktree
	// blob hash. A missing key is a staged deletion.
	Expected map[string]string
	// Fingerprint covers HEAD, branch, status codes, and worktree content.
	Fingerprint string
	// WorktreeFingerprint covers HEAD, paths, and worktree content only. It
	// is the comparison after staging, when index codes legitimately moved.
	WorktreeFingerprint string
	// DiffStat and Diff are bounded evidence.
	DiffStat string
	Diff     string
	// Refs is every issue/PR/URL token present in the evidence.
	Refs map[string]bool
	// evidence is the rendered bounded text handed to the model.
	evidence string
}

// BuildSnapshot inspects the repository and freezes the candidate scope.
//
// It is read-only: status, log, diff, and hash-object only. Nothing is staged
// and no ref moves.
func BuildSnapshot(ctx context.Context, repo *vcs.RepoInfo) (*Snapshot, error) {
	return BuildSnapshotWithObjective(ctx, repo, "")
}

// SnapshotSelection is how a snapshot chooses the scope projected from the
// complete classification.
type SnapshotSelection int

const (
	// SelectionDominant is the normal /commit policy: one dominant logical
	// change is selected and the rest are excluded.
	SelectionDominant SnapshotSelection = iota
	// SelectionPlan classifies and groups every candidate without selecting a
	// scope. It is used to build the complete logical commit plan.
	SelectionPlan
	// SelectionGroup selects exactly one planned logical change. The plan
	// fingerprint and path set are verified before any evidence is built.
	SelectionGroup
)

// snapshotRequest is the internal input shared by normal, plan, and scoped
// proposal snapshots.
type snapshotRequest struct {
	Objective       string
	Selection       SnapshotSelection
	GroupKey        string
	ExpectedPaths   []string
	PlanFingerprint string
	Structural      StructuralEvidenceProvider
}

// BuildSnapshotWithObjective is BuildSnapshot plus the authoritative task
// objective, which is evidence for grouping but never authority over the
// working tree.
func BuildSnapshotWithObjective(ctx context.Context, repo *vcs.RepoInfo, objective string) (*Snapshot, error) {
	return buildSnapshot(ctx, repo, snapshotRequest{
		Objective:  objective,
		Selection:  SelectionDominant,
		Structural: DefaultStructuralEvidence,
	})
}

// buildSnapshot inspects the repository, classifies every path, groups logical
// changes, and projects the requested selection.
func buildSnapshot(ctx context.Context, repo *vcs.RepoInfo, req snapshotRequest) (*Snapshot, error) {
	if repo == nil {
		return nil, fmt.Errorf("%w: not a Git repository", ErrRepository)
	}
	if req.Structural == nil {
		req.Structural = DefaultStructuralEvidence
	}
	// Task context is evidence, not a second repository transcript. Bound it
	// before tokenizing or rendering.
	objective := truncateUTF8(strings.TrimSpace(req.Objective), 1000)
	if operation, err := repo.OperationInProgress(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	} else if operation != "" {
		return nil, fmt.Errorf("%w: a %s operation is in progress; finish or abort it before /commit",
			ErrBlockedChanges, operation)
	}
	entries, err := repo.StatusEntries(ctx, vcs.StatusOptions{Untracked: "all", Ignored: true})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}
	if len(entries) > 0 {
		relevant := 0
		for _, entry := range entries {
			if entry.Ignored() || isInternal(entry.Path) {
				continue
			}
			relevant++
		}
		if relevant > maxChangedPaths {
			return nil, fmt.Errorf("%w: %d changed paths exceed the %d-path limit",
				ErrTooManyChanges, relevant, maxChangedPaths)
		}
	}

	snapshot := &Snapshot{
		Root:     repo.Root,
		Expected: make(map[string]string),
		Refs:     make(map[string]bool),
		Auto:     req.Selection == SelectionGroup,
	}
	if snapshot.Head, err = repo.HeadSHA(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}
	if snapshot.Branch, err = repo.CurrentBranch(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}
	upstream, err := repo.Upstream(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}

	var all []FileChange
	hasStaged := false
	for _, entry := range entries {
		change := classify(entry)
		switch {
		case change.Internal:
			snapshot.Internal = append(snapshot.Internal, change)
			continue
		case change.Ignored:
			snapshot.Ignored = append(snapshot.Ignored, change)
			continue
		}
		classifyOne(repo.Root, &change)
		all = append(all, change)
		if change.Staged() {
			hasStaged = true
		}
	}

	if hasStaged {
		snapshot.Mode = ModeStaged
	} else {
		snapshot.Mode = ModeWorktree
	}

	// Churn facts improve type evidence and group weight. They are read-only
	// and must not decide inclusion on their own.
	applyChurn(ctx, repo, snapshot.Mode, all)
	for i := range all {
		// An untracked binary was ambiguous before churn; a tracked binary is
		// still a legitimate commit candidate.
		if all[i].Untracked && all[i].Kind == KindBinary && all[i].Category == CategoryCommittable {
			all[i].Category = CategoryAmbiguous
			all[i].Reason = "untracked binary; the change set cannot establish its purpose"
		}
	}

	// An unresolved conflict makes the index uncommittable. Check it before
	// the staged-exclusion path so the failure names the real cause.
	var conflicts []FileChange
	for _, change := range all {
		if change.Kind == KindConflicted {
			conflicts = append(conflicts, change)
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrBlockedChanges, joinDisplays(conflicts))
	}

	// A staged internal or ignored path is fatal, not something to reset
	// behind the user's back: committing the index would include it, and
	// unstaging is a mutation the human did not approve. A staged protected or
	// blocked path is the same class of refusal.
	var excludedStaged []FileChange
	for _, change := range snapshot.Internal {
		if change.Staged() {
			excludedStaged = append(excludedStaged, change)
		}
	}
	for _, change := range snapshot.Ignored {
		if change.Staged() {
			excludedStaged = append(excludedStaged, change)
		}
	}
	for _, change := range all {
		if !change.Staged() {
			continue
		}
		if change.Category == CategoryProtected || change.Category == CategoryBlocked ||
			change.Category == CategoryAmbiguous {
			excludedStaged = append(excludedStaged, change)
		}
	}
	if len(excludedStaged) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrStagedExcluded, joinDisplaysDetailed(excludedStaged))
	}

	// A nested repository or submodule is a separate boundary: it was
	// classified blocked and will be excluded and reported below, but it does
	// not make an otherwise valid parent commit impossible.
	var candidate []FileChange
	for _, change := range all {
		if snapshot.Mode == ModeStaged && !change.Staged() {
			continue
		}
		if change.Category != CategoryCommittable {
			continue
		}
		candidate = append(candidate, change)
	}

	diff := groupingDiff(ctx, repo, snapshot.Mode, candidate)
	groups, structural := buildGroups(ctx, groupingInput{
		Root:       repo.Root,
		Mode:       snapshot.Mode,
		Changes:    candidate,
		Diff:       diff,
		Structural: req.Structural,
		Objective:  objective,
	})
	switch req.Selection {
	case SelectionDominant:
		groups = selectGroups(groups, objective, snapshot.Mode == ModeStaged)
	case SelectionGroup:
		chosen := -1
		for i := range groups {
			groups[i].Selected = false
			if groups[i].Key == req.GroupKey {
				chosen = i
			}
		}
		if chosen < 0 {
			return nil, ErrPlanStale
		}
		if len(req.ExpectedPaths) > 0 && !sameStringSet(groupPaths(groups[chosen]), req.ExpectedPaths) {
			return nil, ErrPlanStale
		}
		groups[chosen].Selected = true
	}

	context := &CommitContext{
		Repository: RepositoryState{
			Root:     repo.Root,
			Branch:   snapshot.Branch,
			Head:     snapshot.Head,
			Upstream: upstream,
		},
		Changes:            all,
		Groups:             groups,
		Ignored:            snapshot.Ignored,
		Internal:           snapshot.Internal,
		Objective:          objective,
		StructuralEvidence: structural,
	}
	context.Recent, context.Style = recentStyle(ctx, repo)
	context.Constraints = commitConstraints(context, snapshot)

	groupOf := make(map[string]string, len(candidate))
	groupByKey := make(map[string]*ChangeGroup, len(groups))
	for i := range groups {
		groupByKey[groups[i].Key] = &groups[i]
		for _, file := range groups[i].Files {
			groupOf[file.Path] = groups[i].Key
		}
	}
	for i := range all {
		if all[i].Category != CategoryCommittable {
			continue
		}
		// In staged mode the human selected the index explicitly. An
		// unstaged change is not part of that selection.
		if snapshot.Mode == ModeStaged && !all[i].Staged() {
			all[i].Category = CategoryUnrelated
			all[i].Reason = "unstaged change is not part of the staged commit"
			continue
		}
		key, ok := groupOf[all[i].Path]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrGrouping, all[i].Display())
		}
		all[i].Group = key
		group := groupByKey[key]
		if group == nil {
			return nil, fmt.Errorf("%w: %s", ErrGrouping, all[i].Display())
		}
		if req.Selection == SelectionPlan && group.Confidence.Committable() {
			// The plan enumerates every committable logical change. No scope
			// is selected yet, but these files are not unrelated.
			continue
		}
		if group.Selected {
			continue
		}
		if !group.Confidence.Committable() {
			if group.Confidence == GroupAmbiguous {
				all[i].Category = CategoryAmbiguous
				all[i].Reason = "the change is consistent with more than one logical change"
			} else {
				all[i].Category = CategoryInsufficientEvidence
				all[i].Reason = "no semantic evidence establishes this as a logical change"
			}
			continue
		}
		all[i].Category = CategoryUnrelated
		if req.Selection == SelectionGroup {
			all[i].Reason = "planned for a later logical commit"
		} else {
			all[i].Reason = "belongs to a different logical change set"
		}
	}

	context.Changes = all
	snapshot.Context = context

	// Committing a staged scope commits the whole index. Any staged path the
	// final classification did not select would therefore be absorbed
	// silently, so it is a refusal rather than an exclusion.
	if snapshot.Mode == ModeStaged && req.Selection != SelectionPlan {
		var unselectedStaged []FileChange
		for _, change := range all {
			if change.Staged() && change.Category != CategoryCommittable {
				unselectedStaged = append(unselectedStaged, change)
			}
		}
		if len(unselectedStaged) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrStagedExcluded, joinDisplaysDetailed(unselectedStaged))
		}
	}

	// Rebuild the projections from the final categories so every renderer
	// reads the same decision.
	snapshot.Staged = nil
	snapshot.Unstaged = nil
	snapshot.Untracked = nil
	for _, change := range all {
		if change.Staged() {
			snapshot.Staged = append(snapshot.Staged, change)
		}
		if change.Unstaged() {
			snapshot.Unstaged = append(snapshot.Unstaged, change)
		}
		if change.Untracked {
			snapshot.Untracked = append(snapshot.Untracked, change)
		}
		switch change.Category {
		case CategoryCommittable:
			snapshot.Scope = append(snapshot.Scope, change)
		case CategoryProtected:
			context.Protected = append(context.Protected, change)
		case CategoryAmbiguous:
			context.Ambiguous = append(context.Ambiguous, change)
		case CategoryBlocked:
			context.Blocked = append(context.Blocked, change)
			context.Excluded = append(context.Excluded, change)
		case CategoryInsufficientEvidence:
			context.Insufficient = append(context.Insufficient, change)
			context.Excluded = append(context.Excluded, change)
		default:
			context.Excluded = append(context.Excluded, change)
		}
	}
	snapshot.Scope = dedupeChanges(snapshot.Scope)
	if req.Selection != SelectionPlan {
		if len(snapshot.Scope) == 0 {
			if len(context.Ambiguous) > 0 || len(context.Protected) > 0 ||
				len(context.Insufficient) > 0 || len(context.Blocked) > 0 {
				return nil, fmt.Errorf("%w: %s", ErrNeedsDecision, summarizeExcluded(context))
			}
			return nil, ErrNoChanges
		}
		if len(snapshot.Scope) > maxChangedPaths {
			return nil, fmt.Errorf("%w: %d scoped paths exceed the %d-path limit",
				ErrTooManyChanges, len(snapshot.Scope), maxChangedPaths)
		}
	}

	if snapshot.Fingerprint, err = snapshot.fingerprint(ctx, repo, entries, true); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}
	// A planned group is only valid while the repository is the one the plan
	// described. The fingerprint covers HEAD, the index, and worktree content.
	if req.Selection == SelectionGroup && req.PlanFingerprint != "" &&
		snapshot.Fingerprint != req.PlanFingerprint {
		return nil, ErrPlanStale
	}
	if snapshot.WorktreeFingerprint, err = snapshot.fingerprint(ctx, repo, entries, false); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}
	if req.Selection == SelectionPlan {
		return snapshot, nil
	}

	if err := snapshot.captureExpected(ctx, repo); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRepository, err)
	}
	if err := snapshot.captureEvidence(ctx, repo, context); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// groupPaths returns a group's paths in stable order.
func groupPaths(group ChangeGroup) []string {
	paths := make([]string, 0, len(group.Files))
	for _, file := range group.Files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	return paths
}

// applyChurn records per-path diff churn, bounded to the inspected state.
func applyChurn(ctx context.Context, repo *vcs.RepoInfo, mode Mode, changes []FileChange) {
	if len(changes) == 0 {
		return
	}
	options := vcs.DiffOptions{}
	if mode == ModeStaged {
		options.Cached = true
	}
	entries, err := repo.Numstat(ctx, options)
	if err != nil {
		return
	}
	byPath := make(map[string]vcs.NumstatEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for i := range changes {
		if entry, ok := byPath[changes[i].Path]; ok {
			applyBinaryFacts(&changes[i], entry, true)
		}
	}
}

// recentStyle reads a bounded history sample. History is guidance; a failed or
// empty sample simply leaves the constraints with no learned convention.
func recentStyle(ctx context.Context, repo *vcs.RepoInfo) ([]CommitSummary, CommitStyle) {
	records, err := repo.RecentCommits(ctx, 8)
	if err != nil {
		return nil, CommitStyle{}
	}
	return buildStyle(records)
}

// commitConstraints assembles the message contract for one context.
func commitConstraints(context *CommitContext, snapshot *Snapshot) CommitConstraints {
	suggested := snapshot.SuggestedScope()
	for _, group := range context.Groups {
		if group.Selected && group.Scope != "" {
			suggested = group.Scope
			break
		}
	}
	return CommitConstraints{
		Types:           allowedTypes(),
		Scopes:          scopeSuggestions(suggested, context.Groups, context.Style),
		MaxSubjectRunes: MaxSubjectRunes,
		MaxBullets:      MaxBullets,
		MaxBulletRunes:  MaxBulletRunes,
		BodyExpected:    context.Style.BodyRatio() >= 0.5,
	}
}

// summarizeExcluded renders the non-selected changes for a decision notice.
func summarizeExcluded(context *CommitContext) string {
	var parts []string
	excluded := append([]FileChange{}, context.Protected...)
	excluded = append(excluded, context.Ambiguous...)
	excluded = append(excluded, context.Excluded...)
	for _, change := range excluded {
		parts = append(parts, change.Display()+" ("+change.Reason+")")
	}
	if len(parts) == 0 {
		return "no change could be safely classified"
	}
	if len(parts) > 8 {
		parts = append(parts[:8], fmt.Sprintf("… %d more", len(parts)-8))
	}
	return strings.Join(parts, ", ")
}

// classify turns a porcelain record into a FileChange.
func classify(entry vcs.StatusEntry) FileChange {
	path := entry.Path
	change := FileChange{
		Path:      path,
		OrigPath:  entry.OrigPath,
		Index:     entry.Index,
		Worktree:  entry.Worktree,
		Untracked: entry.Untracked(),
		Ignored:   entry.Ignored(),
		Internal:  isInternal(path),
	}
	switch {
	case change.Internal:
		change.Kind = KindInternal
		change.Category = CategoryProtected
		change.Reason = "ohgo project state"
	case change.Ignored:
		change.Kind = KindIgnored
		change.Category = CategoryProtected
		change.Reason = "ignored by repository rules"
	}
	return change
}

// isInternal reports whether a path belongs to ohgo's project state. The
// check is root-relative and deterministic; .ohgo is never a commit candidate.
func isInternal(path string) bool {
	clean := strings.TrimPrefix(path, "./")
	return clean == internalProjectDir || strings.HasPrefix(clean, internalProjectDir+"/")
}

// Staged reports whether this change is in the index.
func (c FileChange) Staged() bool {
	return c.Index != ' ' && c.Index != '?' && c.Index != '!'
}

// Unstaged reports whether this change is in the worktree.
func (c FileChange) Unstaged() bool {
	return c.Worktree != ' ' && c.Worktree != '?' && c.Worktree != '!'
}

// dedupeChanges keeps the first record per path in status order.
func dedupeChanges(changes []FileChange) []FileChange {
	seen := make(map[string]bool, len(changes))
	out := make([]FileChange, 0, len(changes))
	for _, change := range changes {
		if seen[change.Path] {
			continue
		}
		seen[change.Path] = true
		out = append(out, change)
	}
	return out
}

// maxSuggestedScopeRunes keeps a derived scope from consuming the subject
// budget. It leaves room for a short type, the parentheses, and a summary.
const maxSuggestedScopeRunes = 16

// SuggestedScope derives a conventional-commit scope from the approved paths.
//
// It is used only when the model omitted the scope entirely. The most common
// top-level directory wins; a repository with only root-level changes uses a
// sanitized repository name, and "repo" is the final honest fallback. A name too
// long to leave room for a summary is skipped for the same fallback. The value
// is deterministic for one snapshot, so two identical proposals cannot disagree.
func (s *Snapshot) SuggestedScope() string {
	// The selected group already carries the path-derived scope. Prefer it so
	// the deterministic repair and the evidence agree.
	if s.Context != nil {
		for _, group := range s.Context.Groups {
			if group.Selected && fitsScopeBudget(group.Scope) {
				return group.Scope
			}
		}
	}
	counts := make(map[string]int)
	for _, change := range s.Scope {
		dir, _, nested := strings.Cut(change.Path, "/")
		if !nested {
			continue
		}
		dir = strings.ToLower(dir)
		if scopePattern.MatchString(dir) {
			counts[dir]++
		}
	}
	best := ""
	for dir, n := range counts {
		switch {
		case n > counts[best]:
			best = dir
		case n == counts[best] && (best == "" || dir < best):
			best = dir
		}
	}
	if fitsScopeBudget(best) {
		return best
	}
	if name := scopeToken(filepath.Base(s.Root)); fitsScopeBudget(name) {
		return name
	}
	return "repo"
}

// fitsScopeBudget reports a non-empty derived scope that leaves room for a
// type and a summary.
func fitsScopeBudget(scope string) bool {
	return scope != "" && utf8.RuneCountInString(scope) <= maxSuggestedScopeRunes
}

// scopeToken turns a repository name into a lowercase scope token, dropping
// characters the scope grammar does not allow. An unusable name returns "".
func scopeToken(value string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '/':
			sb.WriteRune(r)
		}
	}
	token := strings.Trim(sb.String(), "-._/")
	if !scopePattern.MatchString(token) {
		return ""
	}
	return token
}

// ExpectedStagedPaths is the index path set the approved commit must have.
//
// A staged rename contributes both its new path and its old path: with rename
// detection disabled, the staged diff names the deletion and the addition.
func (s *Snapshot) ExpectedStagedPaths() []string {
	set := make(map[string]bool, len(s.Scope))
	for _, change := range s.Scope {
		set[change.Path] = true
		// A staged rename removes the old path, so it is part of the staged
		// diff set. A copy leaves the old path unchanged; including it would
		// make the verified index differ from the approved set. Worktree mode
		// stages only the named paths, so it never adds the source side.
		if s.Mode == ModeStaged && change.OrigPath != "" &&
			(change.Index == 'R' || change.Worktree == 'R') {
			set[change.OrigPath] = true
		}
	}
	return sortedKeys(set)
}

// StagePaths is the explicit path list passed to `git add`, never a wildcard.
func (s *Snapshot) StagePaths() []string {
	set := make(map[string]bool, len(s.Scope))
	for _, change := range s.Scope {
		set[change.Path] = true
	}
	return sortedKeys(set)
}

// captureExpected records the content each approved path must have.
func (s *Snapshot) captureExpected(ctx context.Context, repo *vcs.RepoInfo) error {
	switch s.Mode {
	case ModeStaged:
		hashes, err := repo.IndexHashes(ctx, s.ExpectedStagedPaths())
		if err != nil {
			return err
		}
		for _, path := range s.ExpectedStagedPaths() {
			s.Expected[path] = hashes[path]
		}
	case ModeWorktree:
		for _, change := range s.Scope {
			hash, found, err := repo.HashPath(ctx, change.Path)
			if err != nil {
				return err
			}
			if found {
				s.Expected[change.Path] = hash
			} else {
				s.Expected[change.Path] = ""
			}
		}
	}
	return nil
}

// fingerprint hashes HEAD, path set, and worktree content.
//
// includeIndex also hashes the porcelain code pair, which is what catches a
// concurrent stage/unstage before execution. After staging, index codes have
// legitimately changed, so the worktree form is compared instead.
func (s *Snapshot) fingerprint(ctx context.Context, repo *vcs.RepoInfo, entries []vcs.StatusEntry, includeIndex bool) (string, error) {
	// The porcelain code pair alone cannot prove the staged content is the
	// same: re-staging a different version of the same path keeps the "M"
	// code. The index blob hash is part of the state the human approved.
	var stagedPaths []string
	if includeIndex {
		for _, entry := range entries {
			change := classify(entry)
			if change.Internal || change.Ignored || !change.Staged() {
				continue
			}
			stagedPaths = append(stagedPaths, change.Path)
			if change.OrigPath != "" {
				stagedPaths = append(stagedPaths, change.OrigPath)
			}
		}
	}
	indexHashes, err := repo.IndexHashes(ctx, stagedPaths)
	if err != nil {
		return "", err
	}

	var rows []string
	rows = append(rows, "head="+s.Head, "branch="+s.Branch)
	for _, entry := range entries {
		change := classify(entry)
		if change.Internal || change.Ignored {
			continue
		}
		hash, err := contentFingerprint(ctx, repo, change.Path)
		if err != nil {
			return "", err
		}
		row := change.Path + "\x00" + change.OrigPath + "\x00" + hash +
			"\x00" + fileMode(repo.Root, change.Path)
		if includeIndex {
			row += "\x00" + string(change.Index) + string(change.Worktree) +
				"\x00" + indexHashes[change.Path]
		}
		rows = append(rows, row)
	}
	// Entry order is whatever git printed; sorting makes the fingerprint a
	// function of state rather than of traversal order.
	sort.Strings(rows[1:])
	return strings.Join(rows, "\x01"), nil
}

// contentFingerprint hashes a worktree path without following a repository
// boundary. A submodule or nested-repository directory is represented by its
// identity, never by its contents, because its internal changes are not part of
// the parent commit.
func contentFingerprint(ctx context.Context, repo *vcs.RepoInfo, path string) (string, error) {
	abs := filepath.Join(repo.Root, filepath.FromSlash(path))
	if info, err := os.Lstat(abs); err == nil {
		if info.IsDir() {
			return "directory", nil
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			// A FIFO or device must not be opened merely to fingerprint the
			// worktree; its mode is compared by the caller.
			return "non-regular", nil
		}
	}
	hash, _, err := repo.HashPath(ctx, path)
	return hash, err
}

// fileMode is the filesystem mode part of the state guard. Content hashing
// alone misses a chmod that `git add` would stage, so the mode is compared too.
func fileMode(root, path string) string {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "missing"
	}
	return info.Mode().String()
}

// captureEvidence builds the bounded text the commit model sees.
//
// Only the selected scope reaches the diff and the untracked preview. Excluded
// paths are named with their category and reason but their content is never
// read: a protected credential must not be sent to a provider merely because it
// exists in the working tree.
func (s *Snapshot) captureEvidence(ctx context.Context, repo *vcs.RepoInfo, context *CommitContext) error {
	files, redacted := evidenceDiffPaths(s)
	diffOptions := vcs.DiffOptions{Files: files}
	if s.Mode == ModeStaged {
		diffOptions.Cached = true
	}
	diff := ""
	if len(files) > 0 {
		value, err := repo.Diff(ctx, diffOptions)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrRepository, err)
		}
		diff = value
	}
	diff = strings.TrimRight(diff, "\n")
	if s.Mode == ModeWorktree {
		selectedUntracked := make([]FileChange, 0, len(s.Scope))
		for _, change := range s.Scope {
			if change.Untracked && !change.Sensitive {
				selectedUntracked = append(selectedUntracked, change)
			}
		}
		if preview := untrackedPreview(repo.Root, selectedUntracked); preview != "" {
			if diff != "" {
				diff += "\n\n"
			}
			diff += "new files:\n" + preview
		}
	}
	if diff != "" {
		s.Diff = truncateUTF8(diff, maxDiffBytes)
	}
	if len(files) > 0 {
		if numstat, err := repo.Numstat(ctx, diffOptions); err == nil {
			s.DiffStat = truncateUTF8(renderNumstat(numstat), maxDiffStatBytes)
		}
	}

	records := []string{
		"repository: " + valueOr(filepath.Base(s.Root), s.Root),
	}
	if s.Branch != "" {
		records = append(records, "branch: "+s.Branch)
	}
	if context != nil {
		if context.Repository.Upstream != "" {
			records = append(records, "upstream: "+context.Repository.Upstream)
		}
		if context.Objective != "" {
			records = append(records, "task objective: "+safePath(truncateUTF8(context.Objective, 300)))
		}
		if len(context.Recent) > 0 {
			records = append(records, "", "recent commits (style evidence only, do not copy):")
			for _, summary := range context.Recent {
				line := safePath(summary.Subject)
				if summary.HasBody {
					line += " (body)"
				}
				records = append(records, "  "+line)
			}
		}
		if len(context.Recent) > 0 {
			records = append(records, fmt.Sprintf(
				"commit style: %d sampled commits, %d with a body",
				context.Style.Commits, context.Style.BodyCommits))
			if len(context.Style.Types) > 0 {
				records = append(records, "observed types: "+strings.Join(context.Style.Types, " "))
			}
		}
		records = append(records, "", "commit constraints:")
		records = append(records, "  types: "+strings.Join(context.Constraints.Types, " "))
		if len(context.Constraints.Scopes) > 0 {
			records = append(records, "  repository scopes: "+strings.Join(context.Constraints.Scopes, " "))
		}
		records = append(records, fmt.Sprintf(
			"  subject: at most %d characters; body: at most %d bullets, each at most %d characters",
			context.Constraints.MaxSubjectRunes, context.Constraints.MaxBullets, context.Constraints.MaxBulletRunes))
		if context.Constraints.BodyExpected {
			records = append(records, "  recent commits usually include a body")
		}
		if context.StructuralEvidence != "" {
			records = append(records, "  grouping evidence: "+context.StructuralEvidence)
		}
	}

	records = append(records, "", "approved scope:", renderChangesBounded(s.Scope, maxScopeLines))
	if context != nil && len(context.Groups) > 1 {
		records = append(records, "", "change groups:", FormatGroupSummary(context.Groups))
	}
	if context != nil {
		if excluded := evidenceExcluded(context); excluded != "" {
			records = append(records, "", "excluded changes (never include):", excluded)
		}
	}
	if s.DiffStat != "" {
		records = append(records, "", "change summary:", s.DiffStat)
	}
	if len(redacted) > 0 {
		records = append(records, "", "content redacted for protected paths:",
			renderPathList(redacted))
	}
	if s.Diff != "" {
		records = append(records, "", "relevant diff:", s.Diff)
	}
	evidence := strings.Join(records, "\n")
	s.evidence = evidence

	// References are grounded only in the selected change evidence. A #123 in
	// an old commit subject is not permission to invent it in this message.
	trusted := renderChanges(s.Scope) + "\n" + s.DiffStat + "\n" + s.Diff
	for _, token := range referencePattern.FindAllString(trusted, -1) {
		s.Refs[token] = true
	}
	return nil
}

// evidenceDiffPaths returns the paths whose content may be shown to the model
// and the sensitive paths whose content is withheld. A protected-file deletion
// is committable, but the removed bytes are never evidence.
func evidenceDiffPaths(snapshot *Snapshot) (files, redacted []string) {
	seen := make(map[string]bool)
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		files = append(files, path)
	}
	for _, change := range snapshot.Scope {
		if change.Sensitive {
			redacted = append(redacted, change.Path)
			continue
		}
		add(change.Path)
		if snapshot.Mode == ModeStaged && change.OrigPath != "" &&
			(change.Index == 'R' || change.Worktree == 'R') {
			add(change.OrigPath)
		}
	}
	return uniqueSorted(files), redacted
}

// renderPathList bounds an evidence path list.
func renderPathList(paths []string) string {
	const limit = 20
	safe := make([]string, len(paths))
	for i, path := range paths {
		safe[i] = safePath(path)
	}
	if len(safe) <= limit {
		return "  " + strings.Join(safe, ", ")
	}
	return "  " + strings.Join(safe[:limit], ", ") +
		fmt.Sprintf(" … %d more", len(safe)-limit)
}

// evidenceExcluded renders the non-selected changes without their content.
func evidenceExcluded(context *CommitContext) string {
	var lines []string
	add := func(changes []FileChange) {
		for _, change := range changes {
			lines = append(lines, fmt.Sprintf("  %s - %s", change.Display(), change.Reason))
		}
	}
	add(context.Protected)
	add(context.Ambiguous)
	add(context.Excluded)
	const limit = 20
	if len(lines) > limit {
		lines = append(lines[:limit], fmt.Sprintf("  … %d more", len(lines)-limit))
	}
	return strings.Join(lines, "\n")
}

// Evidence renders the bounded repository evidence for the prompt.
func (s *Snapshot) Evidence() string {
	return s.evidence
}

// RepairEvidence is the compact change summary sent with a validation repair.
//
// It deliberately omits the diff and history: the repair corrects one rule
// against the change set the human will approve, it does not re-run repository
// discovery.
func (s *Snapshot) RepairEvidence() string {
	records := []string{"approved scope:", renderChangesBounded(s.Scope, maxScopeLines)}
	if s.Context != nil && len(s.Context.Groups) > 1 {
		records = append(records, "", "change groups:", FormatGroupSummary(s.Context.Groups))
	}
	if s.Context != nil {
		records = append(records, "", "types: "+strings.Join(s.Context.Constraints.Types, " "))
		if len(s.Context.Constraints.Scopes) > 0 {
			records = append(records, "repository scopes: "+strings.Join(s.Context.Constraints.Scopes, " "))
		}
		records = append(records, fmt.Sprintf(
			"subject: at most %d characters; body: at most %d bullets, each at most %d characters",
			s.Context.Constraints.MaxSubjectRunes, s.Context.Constraints.MaxBullets, s.Context.Constraints.MaxBulletRunes))
	}
	if s.DiffStat != "" {
		records = append(records, "", "change summary:", s.DiffStat)
	}
	if s.Context != nil {
		evidence := typeEvidenceOf(s.Scope)
		switch {
		case evidence.DocsOnly:
			records = append(records, "", "change shape: documentation only")
		case evidence.TestsOnly:
			records = append(records, "", "change shape: tests only")
		case evidence.BuildOnly:
			records = append(records, "", "change shape: build or CI infrastructure")
		case evidence.HasSource:
			records = append(records, "", "change shape: implementation changes")
		}
	}
	return strings.Join(records, "\n")
}

// renderChanges formats a change list, or a short marker when empty.
func renderChanges(changes []FileChange) string {
	if len(changes) == 0 {
		return "(none)"
	}
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		lines = append(lines, change.Display())
	}
	return strings.Join(lines, "\n")
}

// renderChangesBounded renders a list for the model, which needs the shape of
// the state but not an unbounded transcript of paths.
func renderChangesBounded(changes []FileChange, limit int) string {
	if len(changes) == 0 {
		return "(none)"
	}
	lines := make([]string, 0, limit+1)
	for i, change := range changes {
		if i == limit {
			lines = append(lines, fmt.Sprintf("… %d more", len(changes)-i))
			break
		}
		lines = append(lines, change.Display())
	}
	return strings.Join(lines, "\n")
}

// renderNumstat formats per-file line counts.
func renderNumstat(entries []vcs.NumstatEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, entry := range entries {
		if i > 0 {
			sb.WriteByte('\n')
		}
		switch {
		case entry.Added == nil || entry.Removed == nil:
			fmt.Fprintf(&sb, "%s (binary)", safePath(entry.Path))
		default:
			fmt.Fprintf(&sb, "%s +%d -%d", safePath(entry.Path), *entry.Added, *entry.Removed)
		}
	}
	return sb.String()
}

// untrackedPreview shows a bounded look at new files, which `git diff` cannot
// include. It never reads the whole file.
func untrackedPreview(root string, changes []FileChange) string {
	var sb strings.Builder
	for _, change := range changes {
		if sb.Len() >= maxUntrackedBytes {
			sb.WriteString("... (untracked preview truncated)\n")
			break
		}
		path := filepath.Join(root, filepath.FromSlash(change.Path))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			fmt.Fprintf(&sb, "%s: (unreadable)\n", change.Path)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Never follow an untracked symlink out of the workspace: the
			// model sees the link, not whatever it points at.
			fmt.Fprintf(&sb, "%s: (symlink)\n", change.Path)
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(&sb, "%s: (unreadable)\n", change.Path)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxUntrackedBytes+1))
		_ = file.Close()
		if readErr != nil {
			fmt.Fprintf(&sb, "%s: (unreadable)\n", change.Path)
			continue
		}
		fmt.Fprintf(&sb, "%s:\n", change.Path)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if i >= 20 || sb.Len() >= maxUntrackedBytes {
				sb.WriteString("... (truncated)\n")
				break
			}
			fmt.Fprintf(&sb, "%s\n", truncateUTF8(line, untrackedPreviewLine))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// joinDisplays renders excluded paths for one error line.
func joinDisplays(changes []FileChange) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		parts = append(parts, change.Display())
	}
	return strings.Join(parts, ", ")
}

// joinDisplaysDetailed adds the classification reason when one exists, so a
// refused staging action says why instead of only naming the path.
func joinDisplaysDetailed(changes []FileChange) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		display := change.Display()
		if change.Reason != "" {
			display += " (" + change.Reason + ")"
		}
		parts = append(parts, display)
	}
	return strings.Join(parts, ", ")
}

// sortedKeys returns map keys in stable order.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// valueOr replaces an empty field with a display marker.
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// truncateUTF8 bounds a string by bytes without splitting a rune.
func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n... (truncated)"
}
