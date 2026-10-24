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
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/vcs"
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
)

// Bounds on the evidence sent to the commit model and on one proposal.
const (
	maxChangedPaths = 2000
	// maxDiffBytes bounds the model-visible diff. The deterministic diffstat
	// still names every changed path, so a truncated diff does not hide what
	// changed; it only stops the prompt from growing with the patch.
	maxDiffBytes      = 24 * 1024
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
		return c.Code() + " " + c.OrigPath + " → " + c.Path
	}
	return c.Code() + " " + c.Path
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
	// Scope is the exact set of paths the human is approving.
	Scope []FileChange
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
	if repo == nil {
		return nil, fmt.Errorf("commit: not a Git repository")
	}
	entries, err := repo.StatusEntries(ctx, vcs.StatusOptions{Untracked: "all", Ignored: true})
	if err != nil {
		return nil, err
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
	}
	if snapshot.Head, err = repo.HeadSHA(ctx); err != nil {
		return nil, err
	}
	if snapshot.Branch, err = repo.CurrentBranch(ctx); err != nil {
		return nil, err
	}
	hasStaged := false
	for _, entry := range entries {
		change := classify(entry)
		switch {
		case change.Internal:
			snapshot.Internal = append(snapshot.Internal, change)
		case change.Ignored:
			snapshot.Ignored = append(snapshot.Ignored, change)
		default:
			if change.Staged() {
				snapshot.Staged = append(snapshot.Staged, change)
				hasStaged = true
			}
			if change.Unstaged() {
				snapshot.Unstaged = append(snapshot.Unstaged, change)
			}
			if change.Untracked {
				snapshot.Untracked = append(snapshot.Untracked, change)
			}
		}
	}

	if hasStaged {
		snapshot.Mode = ModeStaged
	} else {
		snapshot.Mode = ModeWorktree
	}

	// A staged internal or ignored path is fatal, not something to reset
	// behind the user's back: committing the index would include it, and
	// unstaging is a mutation the human did not approve.
	var excluded []FileChange
	for _, change := range snapshot.Internal {
		if change.Staged() {
			excluded = append(excluded, change)
		}
	}
	for _, change := range snapshot.Ignored {
		if change.Staged() {
			excluded = append(excluded, change)
		}
	}
	if len(excluded) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrStagedExcluded, joinDisplays(excluded))
	}

	switch snapshot.Mode {
	case ModeStaged:
		snapshot.Scope = append(snapshot.Scope, snapshot.Staged...)
	case ModeWorktree:
		snapshot.Scope = append(snapshot.Scope, snapshot.Unstaged...)
		snapshot.Scope = append(snapshot.Scope, snapshot.Untracked...)
	}
	snapshot.Scope = dedupeChanges(snapshot.Scope)
	if len(snapshot.Scope) == 0 {
		return nil, ErrNoChanges
	}
	if len(snapshot.Scope) > maxChangedPaths {
		return nil, fmt.Errorf("%w: %d scoped paths exceed the %d-path limit",
			ErrTooManyChanges, len(snapshot.Scope), maxChangedPaths)
	}

	if err := snapshot.captureExpected(ctx, repo); err != nil {
		return nil, err
	}
	if snapshot.Fingerprint, err = snapshot.fingerprint(ctx, repo, entries, true); err != nil {
		return nil, err
	}
	if snapshot.WorktreeFingerprint, err = snapshot.fingerprint(ctx, repo, entries, false); err != nil {
		return nil, err
	}
	snapshot.captureEvidence(ctx, repo)
	return snapshot, nil
}

// classify turns a porcelain record into a FileChange.
func classify(entry vcs.StatusEntry) FileChange {
	path := entry.Path
	return FileChange{
		Path:      path,
		OrigPath:  entry.OrigPath,
		Index:     entry.Index,
		Worktree:  entry.Worktree,
		Untracked: entry.Untracked(),
		Ignored:   entry.Ignored(),
		Internal:  isInternal(path),
	}
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
		if change.OrigPath != "" {
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
		hash, _, err := repo.HashPath(ctx, change.Path)
		if err != nil {
			return "", err
		}
		row := change.Path + "\x00" + change.OrigPath + "\x00" + hash
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

// captureEvidence builds the bounded text the commit model sees.
//
// The model needs the approved paths, their per-file line counts, and the
// change itself. Branch, HEAD, staged/unstaged/untracked duplicates, recent
// commit style, and excluded internal paths do not change the message and are
// deliberately absent: the approval surface already shows them to the human.
func (s *Snapshot) captureEvidence(ctx context.Context, repo *vcs.RepoInfo) {
	var diffOptions vcs.DiffOptions
	if s.Mode == ModeStaged {
		diffOptions.Cached = true
	}
	diff, err := repo.Diff(ctx, diffOptions)
	if err != nil {
		diff = ""
	}
	diff = strings.TrimRight(diff, "\n")
	if s.Mode == ModeWorktree {
		if preview := untrackedPreview(repo.Root, s.Untracked); preview != "" {
			if diff != "" {
				diff += "\n\n"
			}
			diff += "new files:\n" + preview
		}
	}
	if diff != "" {
		s.Diff = truncateUTF8(diff, maxDiffBytes)
	}
	if numstat, err := repo.Numstat(ctx, diffOptions); err == nil {
		s.DiffStat = renderNumstat(numstat)
	}

	records := []string{
		"repository: " + valueOr(filepath.Base(s.Root), s.Root),
		"",
		"approved scope:",
		renderChangesBounded(s.Scope, maxScopeLines),
	}
	if s.DiffStat != "" {
		records = append(records, "", "change summary:", s.DiffStat)
	}
	if s.Diff != "" {
		records = append(records, "", "relevant diff:", s.Diff)
	}
	evidence := strings.Join(records, "\n")
	s.evidence = evidence
	for _, token := range referencePattern.FindAllString(evidence, -1) {
		s.Refs[token] = true
	}
}

// Evidence renders the bounded repository evidence for the prompt.
func (s *Snapshot) Evidence() string {
	return s.evidence
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
			fmt.Fprintf(&sb, "%s (binary)", entry.Path)
		default:
			fmt.Fprintf(&sb, "%s +%d -%d", entry.Path, *entry.Added, *entry.Removed)
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
