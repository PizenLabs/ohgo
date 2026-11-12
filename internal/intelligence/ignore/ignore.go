// Package ignore resolves .gitignore rules for a filesystem tree.
//
// It exists as a shared package because more than one subsystem must agree on
// which paths belong to a repository: the search tools must not report an
// ignored file, and workspace recovery must not checkpoint or restore one. Two
// matchers would be two answers to "is this file part of the workspace?", and a
// disagreement there is a silent correctness bug rather than a cosmetic one.
//
// The original delegates to a native Rust matcher. This implements the subset
// that matters in practice: nested ignore files, directory-scoped patterns,
// negation, anchoring, and comments. Patterns it cannot interpret are ignored
// rather than treated as literal text, which avoids a false exclusion.
package ignore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Matcher resolves the ignore rules loaded from one tree.
//
// The zero value is not usable; call New or NewRules. A nil *Matcher is safe
// and matches nothing, so a caller that has no rules can skip a check instead
// of branching around it.
type Matcher struct {
	// root bounds which ignore files are read.
	root string
	// disabled turns every rule off, for the `gitignore: false` case.
	disabled bool
	// rules holds every loaded rule, outermost first, so a deeper file wins.
	rules []rule
	// contents retains the exact rule text that produced rules. It exists so
	// a structural index can fingerprint repository scope without re-reading
	// or re-implementing .gitignore semantics.
	contents strings.Builder
}

// rule is one parsed pattern from one .gitignore.
type rule struct {
	// base is the directory the pattern is relative to, root-relative.
	base string
	// pattern is the glob body.
	pattern string
	// negated is true for "!" rules.
	negated bool
	// dirOnly is true when the pattern ended with "/".
	dirOnly bool
	// anchored is true when the pattern contained a non-trailing "/".
	anchored bool
	// source is the ignore file that contributed the rule, root-relative.
	source string
	// line is the 1-based line in source that carried the rule.
	line int
	// raw is the trimmed rule text, retained so a scope exclusion can name the
	// exact decision instead of presenting an unexplained disappearance.
	raw string
}

// New loads every .gitignore under root.
//
// A missing or unreadable file is not an error: a repository without ignore
// rules simply excludes nothing.
func New(root string, enabled bool) *Matcher {
	m := &Matcher{root: root, disabled: !enabled}
	if m.disabled {
		return m
	}

	// Load outermost-first so a deeper file's rules are appended later and
	// therefore evaluated last.
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			// Descending into .git itself is both wasteful and wrong for
			// ignore semantics.
			if path != root && (name == ".git" || IsHidden(name)) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != ".gitignore" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return nil
		}
		base := filepath.ToSlash(rel)
		if base == "." {
			base = ""
		}
		m.Append(base, string(data))
		return nil
	})
	return m
}

// NewRules returns a disabled matcher with an empty rule set, for a caller that
// parses rule text directly rather than loading files.
func NewRules() *Matcher { return &Matcher{disabled: false} }

// Append parses one .gitignore's contents, relative to base.
func (m *Matcher) Append(base, contents string) {
	if m == nil {
		return
	}
	m.contents.WriteString(base)
	m.contents.WriteByte(0)
	m.contents.WriteString(contents)
	m.contents.WriteByte(0)
	source := ".gitignore"
	if base != "" {
		source = base + "/.gitignore"
	}
	for index, raw := range strings.Split(contents, "\n") {
		line := strings.TrimRight(raw, "\r")
		// A trailing space not escaped with a backslash is stripped by git.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		r := rule{base: base, pattern: trimmed, source: source, line: index + 1, raw: trimmed}
		if strings.HasPrefix(r.pattern, "!") {
			r.negated = true
			r.pattern = strings.TrimPrefix(r.pattern, "!")
		}
		// A leading slash anchors the pattern to the ignore file's directory.
		// It must be removed before the path-segment logic runs, or the first
		// segment would never match.
		if strings.HasPrefix(r.pattern, "/") {
			r.anchored = true
			r.pattern = strings.TrimPrefix(r.pattern, "/")
		}
		if strings.HasSuffix(r.pattern, "/") {
			r.dirOnly = true
			r.pattern = strings.TrimSuffix(r.pattern, "/")
		}
		// A slash anywhere else also anchors the pattern.
		if idx := strings.IndexByte(r.pattern, '/'); idx >= 0 {
			r.anchored = true
		}
		if r.pattern == "" {
			continue
		}
		m.rules = append(m.rules, r)
	}
}

// Disabled reports whether the matcher excludes nothing.
func (m *Matcher) Disabled() bool { return m == nil || m.disabled }

// Fingerprint is a deterministic identity of the loaded rule text.
//
// A structural index records it with every snapshot. When a .gitignore rule
// changes, the fingerprint changes even if no source-file signature did, so the
// snapshot is refreshed and file membership is recomputed. A disabled matcher
// has its own stable identity rather than the empty-rule hash, so enabling or
// disabling repository scoping is observable.
func (m *Matcher) Fingerprint() string {
	if m == nil || m.disabled {
		return "disabled"
	}
	sum := sha256.Sum256([]byte(m.contents.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// Ignored reports whether a root-relative path is excluded.
//
// Later rules win, which is what makes a negation in a deeper .gitignore
// override an exclusion set by an outer one.
func (m *Matcher) Ignored(rel string, isDir bool) bool {
	ignored, _ := m.Match(rel, isDir)
	return ignored
}

// IgnoredFrom applies only the rules rooted at or below base.
//
// It is the nested-project boundary: a repository's own ignore files govern
// its contents, while the enclosing repository's rules stop at the boundary.
// A path outside base is evaluated normally, so a caller can use this
// unconditionally after choosing the nearest boundary.
func (m *Matcher) IgnoredFrom(base, rel string, isDir bool) bool {
	ignored, _ := m.MatchFrom(base, rel, isDir)
	return ignored
}

// Match reports whether rel is excluded and, when it is, the rule that decided
// it in "source:line: pattern" form.
func (m *Matcher) Match(rel string, isDir bool) (bool, string) {
	return m.MatchFrom("", rel, isDir)
}

// MatchFrom is Match limited to the rules rooted at or below base. An empty
// base evaluates the complete rule set, which is the repository-root scope.
func (m *Matcher) MatchFrom(base, rel string, isDir bool) (bool, string) {
	return m.matchWhere(rel, isDir, func(r rule) bool {
		return base == "" || r.base == base || strings.HasPrefix(r.base, base+"/")
	})
}

// MatchOutside is Match limited to the rules outside base. It answers why an
// enclosing repository would have excluded a nested project root without
// treating the nested project's own rules as the cause.
func (m *Matcher) MatchOutside(base, rel string, isDir bool) (bool, string) {
	return m.matchWhere(rel, isDir, func(r rule) bool {
		return r.base != base && !strings.HasPrefix(r.base, base+"/")
	})
}

func (m *Matcher) matchWhere(rel string, isDir bool, keep func(rule) bool) (bool, string) {
	if m == nil || m.disabled || len(m.rules) == 0 {
		return false, ""
	}
	rel = filepath.ToSlash(rel)

	ignored := false
	reason := ""
	for _, r := range m.rules {
		if !keep(r) {
			continue
		}
		if r.dirOnly && !isDir {
			continue
		}
		if !r.appliesTo(rel) {
			continue
		}
		ignored = !r.negated
		reason = r.source + ":" + strconv.Itoa(r.line) + ": " + r.raw
	}
	if !ignored {
		// A negation may have rescued the path after an exclusion. The reason
		// only describes an actual exclusion.
		return false, ""
	}
	return true, reason
}

// appliesTo reports whether the rule's pattern matches the path.
func (r rule) appliesTo(rel string) bool {
	if r.base != "" {
		// The rule only governs its own subtree.
		if rel != r.base && !strings.HasPrefix(rel, r.base+"/") {
			return false
		}
		rel = strings.TrimPrefix(rel, r.base+"/")
	}
	if r.anchored {
		// Anchoring is relative to the ignore file's base. Match the pattern
		// against that many leading segments, so "/ohgo" excludes the root
		// binary but not "cmd/ohgo". Matching every segment here made the
		// anchor meaningless and silently hid an entire source subtree.
		patternSegments := strings.Split(r.pattern, "/")
		pathSegments := strings.Split(rel, "/")
		if hasDoubleStar(patternSegments) {
			// "**" deliberately spans directories, so a prefix slice would
			// truncate the very segments it is meant to match.
			return matchSegments(patternSegments, pathSegments)
		}
		if len(pathSegments) < len(patternSegments) {
			return false
		}
		return matchSegments(patternSegments, pathSegments[:len(patternSegments)])
	}
	// An unanchored pattern matches at any depth, so walk the path's
	// suffixes rather than requiring an exact hit.
	for {
		if matchPath(r.pattern, rel) {
			return true
		}
		idx := strings.IndexByte(rel, '/')
		if idx < 0 {
			return false
		}
		rel = rel[idx+1:]
	}
}

// matchPath matches a gitignore pattern against a relative path.
//
// A pattern with "/" matches the whole path segment-wise; a bare pattern
// matches any single segment, which is how git treats "*.log" and
// "node_modules" alike.
func matchPath(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") {
		for _, segment := range strings.Split(rel, "/") {
			if ok, err := filepath.Match(pattern, segment); err == nil && ok {
				return true
			}
		}
		return false
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

// hasDoubleStar reports whether a pattern segment list contains "**".
func hasDoubleStar(pattern []string) bool {
	for _, segment := range pattern {
		if segment == "**" {
			return true
		}
	}
	return false
}

// matchSegments matches pattern segments against path segments, honouring "**"
// as "any number of segments".
func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchSegments(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if ok, err := filepath.Match(pattern[0], path[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], path[1:])
}

// IsHidden reports whether a name is a dotfile or dot-directory.
func IsHidden(name string) bool {
	return len(name) > 1 && name[0] == '.' && name != ".."
}
