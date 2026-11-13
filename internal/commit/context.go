package commit

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/vcs"
)

// ChangeCategory is the control-plane decision for one changed path.
//
// It is not a model suggestion. The COMMIT model sees the selected scope and
// the exclusion summary; it never chooses categories.
type ChangeCategory string

// Commitability categories.
const (
	// CategoryCommittable means the change belongs to the selected logical
	// commit and is safe to stage.
	CategoryCommittable ChangeCategory = "committable"
	// CategoryProtected means the path must not be committed automatically:
	// credentials, local machine state, nested repository content, ...
	CategoryProtected ChangeCategory = "protected"
	// CategoryUnrelated means the change is real repository work that belongs
	// to a different logical change set.
	CategoryUnrelated ChangeCategory = "unrelated"
	// CategoryAmbiguous means the control plane cannot prove the change
	// belongs. Uncertainty is never converted into inclusion.
	CategoryAmbiguous ChangeCategory = "ambiguous"
	// CategoryBlocked means the repository state cannot be committed through
	// this flow at all (an unresolved conflict, for example).
	CategoryBlocked ChangeCategory = "blocked"
	// CategoryInsufficientEvidence means the control plane could not establish
	// that the path is an independent logical change. It is never committable
	// automatically; a human or a richer structural provider must decide.
	CategoryInsufficientEvidence ChangeCategory = "insufficient_evidence"
)

// ChangeKind is the observable state of one path. A path can carry more than
// one fact (a staged rename is also a tracked modification), so the most
// specific fact is recorded and the raw porcelain codes remain available.
type ChangeKind string

// Observable change kinds.
const (
	KindModified         ChangeKind = "modified"
	KindDeleted          ChangeKind = "deleted"
	KindRenamed          ChangeKind = "renamed"
	KindAdded            ChangeKind = "added"
	KindUntracked        ChangeKind = "untracked"
	KindPartiallyStaged  ChangeKind = "partially_staged"
	KindConflicted       ChangeKind = "conflicted"
	KindGenerated        ChangeKind = "generated"
	KindSecret           ChangeKind = "secret"
	KindBinary           ChangeKind = "binary"
	KindSubmodule        ChangeKind = "submodule"
	KindNestedRepository ChangeKind = "nested_repository"
	KindSymlink          ChangeKind = "symlink"
	KindIgnored          ChangeKind = "ignored"
	KindInternal         ChangeKind = "internal"
	KindNonRegular       ChangeKind = "non_regular_file"
)

// RepositoryState is the branch and history context for one proposal.
type RepositoryState struct {
	// Root is the checkout root.
	Root string
	// Branch is the checked-out branch, or "" when detached.
	Branch string
	// Head is the commit HEAD pointed at when the snapshot was built.
	Head string
	// Upstream is the configured upstream ref, or "" when there is none.
	Upstream string
}

// CommitConstraints is the bounded message contract for this repository.
//
// The values are facts assembled by the control plane. The model receives them
// as context; Probe.Validate enforces them deterministically.
type CommitConstraints struct {
	// Types is the repository's conventional-commit vocabulary.
	Types []string
	// Scopes is the bounded vocabulary derived from the selected paths and
	// recent history. It is guidance, not a closed grammar: a repository can
	// legitimately introduce a new subsystem scope.
	Scopes []string
	// MaxSubjectRunes, MaxBullets, and MaxBulletRunes are the hard limits.
	MaxSubjectRunes int
	MaxBullets      int
	MaxBulletRunes  int
	// BodyExpected reports that recent commits usually carry a body.
	BodyExpected bool
}

// CommitSummary is one recent commit as style evidence. The raw body is
// deliberately not retained: /commit learns whether bodies are used, not what
// previous authors claimed.
type CommitSummary struct {
	// SHA is the commit id.
	SHA string
	// Subject is the first line, bounded for evidence rendering.
	Subject string
	// Type and Scope are the parsed conventional fields when the subject is
	// conventional; empty otherwise.
	Type  string
	Scope string
	// HasBody reports that the commit message had more than one paragraph.
	HasBody bool
}

// CommitStyle is the bounded convention sample learned from recent history.
type CommitStyle struct {
	// Commits is the number of samples read.
	Commits int
	// BodyCommits is how many samples carried a body.
	BodyCommits int
	// Types and Scopes count observed conventional fields, most frequent
	// first. History informs guidance; it never widens validation.
	Types  []string
	Scopes []string
}

// BodyRatio returns the share of sampled commits with a body.
func (s CommitStyle) BodyRatio() float64 {
	if s.Commits == 0 {
		return 0
	}
	return float64(s.BodyCommits) / float64(s.Commits)
}

// ChangeGroup is one logical change set identified from repository evidence.
type ChangeGroup struct {
	// Key is the stable grouping key (usually the package directory).
	Key string
	// Label is the human-readable group name.
	Label string
	// Scope is the conventional-commit scope suggested for this group.
	Scope string
	// Files are the changes in the group.
	Files []FileChange
	// Evidence records the facts that put these files together.
	Evidence []string
	// Selected reports that this group is in the proposed commit.
	Selected bool
	// Confidence reports how strongly the grouping is evidenced.
	Confidence GroupConfidence
	// Order is the deterministic plan position assigned to this group.
	Order int
}

// CommitContext is the authoritative control-plane projection of repository
// state for one /commit proposal.
//
// It is built once, handed to the model as bounded evidence, validated against
// the model's proposal, and frozen into the Request that execution verifies.
// The model never owns any field here.
type CommitContext struct {
	// Repository is the branch and history context.
	Repository RepositoryState
	// Changes is every relevant path with its category.
	Changes []FileChange
	// Groups are the logical change sets found in the committable changes.
	Groups []ChangeGroup
	// Protected, Excluded, and Ambiguous are the non-selected changes.
	// Excluded is the union of unrelated, blocked, and insufficient-evidence
	// changes; Blocked and Insufficient are the explicit subsets so a caller
	// never has to infer the state from a reason string.
	Protected    []FileChange
	Excluded     []FileChange
	Blocked      []FileChange
	Ambiguous    []FileChange
	Insufficient []FileChange
	// Ignored and Internal are repository facts that never become candidates.
	Ignored  []FileChange
	Internal []FileChange
	// Recent is the bounded history sample.
	Recent []CommitSummary
	// Style is the learned convention from Recent.
	Style CommitStyle
	// Constraints is the message contract.
	Constraints CommitConstraints
	// Objective is the authoritative task objective, when one is available.
	Objective string
	// StructuralEvidence describes how grouping used structural facts
	// ("imports", "package", or "paths only").
	StructuralEvidence string
}

// kindRole classifies a path for semantic type validation and grouping.
type kindRole int

const (
	roleOther kindRole = iota
	roleSource
	roleTest
	roleDocs
	roleBuild
	roleCI
	roleConfig
)

// roleOf reports the semantic role of one path. It is deliberately about the
// path and repository convention, not the model's wording.
func roleOf(path string) kindRole {
	clean := strings.TrimPrefix(path, "./")
	lower := strings.ToLower(clean)
	base := strings.ToLower(pathBase(clean))
	ext := strings.ToLower(pathExt(base))

	// Test data wins over a documentation extension: a golden file under
	// testdata describes a test, not the project. A docs subtree named tests
	// remains documentation.
	if !strings.HasPrefix(lower, "docs/") &&
		(strings.HasSuffix(base, "_test.go") || strings.Contains(lower, "/test/") ||
			strings.HasPrefix(lower, "test/") || strings.Contains(lower, "/tests/") ||
			strings.HasPrefix(lower, "tests/") || strings.Contains(lower, "/testdata/") ||
			strings.HasPrefix(lower, "testdata/") || strings.Contains(lower, ".test.") ||
			strings.Contains(lower, ".spec.") || strings.HasSuffix(lower, "_spec.go") ||
			strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".spec")) {
		return roleTest
	}
	switch base {
	case "readme", "changelog", "license", "licence", "copying", "notice",
		"authors", "contributors":
		return roleDocs
	}
	if strings.HasPrefix(lower, "docs/") || ext == ".md" || ext == ".mdx" ||
		ext == ".rst" || ext == ".adoc" {
		return roleDocs
	}
	if strings.HasPrefix(lower, ".github/") || strings.Contains(lower, "/.github/") {
		return roleCI
	}
	switch base {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "makefile", "gnumakefile", "dockerfile",
		".golangci.yml", ".golangci.yaml", "cargo.toml", "cargo.lock",
		"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock",
		"tsconfig.json", "build.gradle", "settings.gradle", "pom.xml",
		"cmakelists.txt", "meson.build":
		return roleBuild
	}
	if strings.HasSuffix(base, ".mk") || strings.HasPrefix(base, "dockerfile") ||
		strings.HasPrefix(base, "makefile.") {
		return roleBuild
	}
	switch ext {
	case ".go", ".rs", ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp",
		".java", ".kt", ".kts", ".cs", ".rb", ".py", ".js", ".jsx",
		".ts", ".tsx", ".mjs", ".cjs", ".swift", ".scala", ".php",
		".lua", ".sh", ".bash", ".zsh", ".fish", ".ps1", ".vue",
		".svelte", ".html", ".css", ".scss", ".sass", ".less":
		return roleSource
	}
	switch ext {
	case ".yml", ".yaml", ".toml", ".json", ".ini", ".conf", ".cfg",
		".properties", ".xml", ".plist", ".env":
		return roleConfig
	}
	return roleOther
}

// TypeEvidence is the deterministic semantic shape of the selected change set.
type TypeEvidence struct {
	// DocsOnly means every selected change is documentation.
	DocsOnly bool
	// TestsOnly means every selected change is a test or test data file.
	TestsOnly bool
	// BuildOnly means every selected change is build or CI infrastructure.
	BuildOnly bool
	// HasSource reports at least one source change.
	HasSource bool
	// HasAdditions and HasDeletions report churn evidence.
	HasAdditions bool
	HasDeletions bool
	// HasTests reports at least one test change, even when implementation is
	// also present. Tests alone never force the test type.
	HasTests bool
}

// typeEvidenceOf derives TypeEvidence from the selected changes.
func typeEvidenceOf(changes []FileChange) TypeEvidence {
	var evidence TypeEvidence
	if len(changes) == 0 {
		return evidence
	}
	allDocs, allTests, allBuild := true, true, true
	for _, change := range changes {
		role := change.Role
		if role == roleOther && change.Path != "" {
			role = roleOf(change.Path)
		}
		switch role {
		case roleDocs:
			allTests = false
			allBuild = false
		case roleTest:
			evidence.HasTests = true
			allDocs = false
			allBuild = false
		case roleBuild, roleCI:
			allDocs = false
			allTests = false
		case roleSource:
			evidence.HasSource = true
			allDocs = false
			allTests = false
			allBuild = false
		case roleConfig:
			allDocs = false
			allTests = false
			allBuild = false
		default:
			allDocs = false
			allTests = false
			allBuild = false
		}
		if change.Added != nil && *change.Added > 0 {
			evidence.HasAdditions = true
		}
		if change.Untracked || change.Kind == KindAdded || change.Kind == KindUntracked {
			evidence.HasAdditions = true
		}
		if change.Removed != nil && *change.Removed > 0 {
			evidence.HasDeletions = true
		}
		if change.Kind == KindDeleted {
			evidence.HasDeletions = true
		}
		if change.Kind == KindRenamed && change.OrigPath != "" {
			// A rename is an addition at the new path and a removal at the
			// old one, even when the blob is unchanged.
			evidence.HasAdditions = true
			evidence.HasDeletions = true
		}
		if change.Binary || change.Kind == KindBinary {
			// Binary churn is not line-oriented, so neither an add nor a
			// remove claim can be disproved from numstat.
			evidence.HasAdditions = true
			evidence.HasDeletions = true
		}
	}
	evidence.DocsOnly = allDocs
	evidence.TestsOnly = allTests
	evidence.BuildOnly = allBuild
	return evidence
}

// defaultTypes is the conventional-commit vocabulary OHGO validates against.
//
// It is a package-level contract rather than model output: a bad historical
// commit cannot widen the allowed set. The prompt recommends the same words.
var defaultTypes = []string{
	"feat", "fix", "refactor", "perf", "docs", "test",
	"build", "ci", "chore", "revert", "style",
}

// allowedTypes returns the canonical vocabulary in stable order.
func allowedTypes() []string { return append([]string(nil), defaultTypes...) }

// containsType reports membership in the canonical vocabulary.
func containsType(value string) bool { return containsTypeIn(defaultTypes, value) }

// containsTypeIn reports membership in a supplied vocabulary. The context's
// constraints are authoritative when they are present.
func containsTypeIn(types []string, value string) bool {
	for _, typ := range types {
		if value == typ {
			return true
		}
	}
	return false
}

// buildStyle samples recent commits for convention evidence.
//
// The body is never retained. A subject is bounded so a pathological history
// cannot grow the model prompt; the raw commit stays in Git for verification.
func buildStyle(records []vcs.CommitRecord) ([]CommitSummary, CommitStyle) {
	const maxSubjectRunes = 80
	style := CommitStyle{Commits: len(records)}
	typeCounts := make(map[string]int)
	scopeCounts := make(map[string]int)
	summaries := make([]CommitSummary, 0, len(records))
	for _, record := range records {
		subject := strings.TrimSpace(record.Subject)
		if n := utf8.RuneCountInString(subject); n > maxSubjectRunes {
			subject = string([]rune(subject)[:maxSubjectRunes]) + "…"
		}
		summary := CommitSummary{
			SHA:     record.SHA,
			Subject: subject,
			HasBody: strings.TrimSpace(record.Body) != "",
		}
		if summary.HasBody {
			style.BodyCommits++
		}
		if match := subjectPattern.FindStringSubmatch(subject); match != nil {
			summary.Type = match[1]
			summary.Scope = match[2]
			// History informs scope guidance. A non-conventional type in old
			// history is not a vocabulary the model should imitate.
			if containsType(match[1]) {
				typeCounts[match[1]]++
			}
			if scopePattern.MatchString(match[2]) {
				scopeCounts[match[2]]++
			}
		}
		summaries = append(summaries, summary)
	}
	style.Types = rankedKeys(typeCounts, 8)
	style.Scopes = rankedKeys(scopeCounts, 12)
	return summaries, style
}

// rankedKeys returns keys by descending count, then lexicographic order, so the
// same history always produces the same guidance.
func rankedKeys(counts map[string]int, limit int) []string {
	type row struct {
		key   string
		count int
	}
	rows := make([]row, 0, len(counts))
	for key, count := range counts {
		rows = append(rows, row{key: key, count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].key < rows[j].key
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.key)
	}
	return out
}

// scopeSuggestions merges the deterministic path scope with scopes already used
// by the repository. It is bounded and stable so two identical snapshots cannot
// present different guidance.
func scopeSuggestions(suggested string, groups []ChangeGroup, style CommitStyle) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 16)
	add := func(scope string) {
		scope = strings.TrimSpace(scope)
		if scope == "" || !scopePattern.MatchString(scope) || seen[scope] ||
			utf8.RuneCountInString(scope) > maxSuggestedScopeRunes {
			return
		}
		seen[scope] = true
		out = append(out, scope)
	}
	// Current evidence first: history may repeat a release scope many times,
	// but it must not outrank the subsystem this change actually touches.
	add(suggested)
	for _, group := range groups {
		add(group.Scope)
	}
	for _, scope := range style.Scopes {
		add(scope)
	}
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

// FormatGroupSummary renders the bounded group plan for evidence and notes.
func FormatGroupSummary(groups []ChangeGroup) string {
	if len(groups) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, group := range groups {
		if i > 0 {
			sb.WriteByte('\n')
		}
		marker := "excluded"
		if group.Selected {
			marker = "selected"
		}
		fmt.Fprintf(&sb, "- %s (%s, %d %s)", safePath(group.Label), marker,
			len(group.Files), plural("file", len(group.Files)))
		if len(group.Evidence) > 0 {
			hints := group.Evidence
			if len(hints) > 2 {
				hints = hints[:2]
			}
			hint := safePath(strings.Join(hints, "; "))
			if n := utf8.RuneCountInString(hint); n > 120 {
				hint = string([]rune(hint)[:120]) + "…"
			}
			fmt.Fprintf(&sb, " — %s", hint)
		}
	}
	return sb.String()
}

// pathBase returns the final path element without importing filepath for a
// slash-normalized repository path.
func pathBase(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// isAlphaNumeric reports an ASCII alphanumeric rune. Repository paths and
// message tokens are ASCII-oriented; the helper is shared by grouping and
// path-claim validation.
func isAlphaNumeric(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// pathExt returns the suffix beginning at the final dot, or "".
func pathExt(base string) string {
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[i:]
	}
	return ""
}
