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
	"os"
	"path/filepath"
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
	for _, raw := range strings.Split(contents, "\n") {
		line := strings.TrimRight(raw, "\r")
		// A trailing space not escaped with a backslash is stripped by git.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		r := rule{base: base, pattern: trimmed}
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

// Ignored reports whether a root-relative path is excluded.
//
// Later rules win, which is what makes a negation in a deeper .gitignore
// override an exclusion set by an outer one.
func (m *Matcher) Ignored(rel string, isDir bool) bool {
	if m == nil || m.disabled || len(m.rules) == 0 {
		return false
	}
	rel = filepath.ToSlash(rel)

	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if !r.appliesTo(rel) {
			continue
		}
		ignored = !r.negated
	}
	return ignored
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
