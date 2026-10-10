package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// ignorer resolves .gitignore rules for a tree.
//
// The original delegates to a native Rust matcher. This implements the subset
// that matters in practice: nested ignore files, directory-scoped patterns,
// negation, anchoring, and comments. Patterns it cannot interpret are ignored
// rather than treated as literal text, which avoids a false exclusion.
type ignorer struct {
	// root bounds which ignore files are read.
	root string
	// disabled turns every rule off, for the `gitignore: false` case.
	disabled bool
	// rules holds every loaded rule, outermost first, so a deeper file wins.
	rules []ignoreRule
}

// ignoreRule is one parsed pattern from one .gitignore.
type ignoreRule struct {
	// base is the directory the pattern is relative to, Root-relative.
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

// newIgnorer loads every .gitignore under root.
//
// A missing or unreadable file is not an error: a repository without ignore
// rules simply excludes nothing.
func newIgnorer(root string, enabled bool) *ignorer {
	ig := &ignorer{root: root, disabled: !enabled}
	if ig.disabled {
		return ig
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
			if path != root && (name == ".git" || isHidden(name)) {
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
		ig.appendFile(base, string(data))
		return nil
	})
	return ig
}

// appendFile parses one .gitignore's contents.
func (ig *ignorer) appendFile(base, contents string) {
	for _, raw := range strings.Split(contents, "\n") {
		line := strings.TrimRight(raw, "\r")
		// A trailing space not escaped with a backslash is stripped by git.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		rule := ignoreRule{base: base, pattern: trimmed}
		if strings.HasPrefix(rule.pattern, "!") {
			rule.negated = true
			rule.pattern = strings.TrimPrefix(rule.pattern, "!")
		}
		// A leading slash anchors the pattern to the ignore file's directory.
		// It must be removed before the path-segment logic runs, or the first
		// segment would never match.
		if strings.HasPrefix(rule.pattern, "/") {
			rule.anchored = true
			rule.pattern = strings.TrimPrefix(rule.pattern, "/")
		}
		if strings.HasSuffix(rule.pattern, "/") {
			rule.dirOnly = true
			rule.pattern = strings.TrimSuffix(rule.pattern, "/")
		}
		// A slash anywhere else also anchors the pattern.
		if idx := strings.IndexByte(rule.pattern, '/'); idx >= 0 {
			rule.anchored = true
		}
		if rule.pattern == "" {
			continue
		}
		ig.rules = append(ig.rules, rule)
	}
}

// ignored reports whether a Root-relative path is excluded.
//
// Later rules win, which is what makes a negation in a deeper .gitignore
// override an exclusion set by an outer one.
func (ig *ignorer) ignored(rel string, isDir bool) bool {
	if ig.disabled || len(ig.rules) == 0 {
		return false
	}
	rel = filepath.ToSlash(rel)

	ignored := false
	for _, rule := range ig.rules {
		if rule.dirOnly && !isDir {
			continue
		}
		if !rule.appliesTo(rel) {
			continue
		}
		ignored = !rule.negated
	}
	return ignored
}

// appliesTo reports whether the rule's pattern matches the path.
func (r ignoreRule) appliesTo(rel string) bool {
	if r.base != "" {
		// The rule only governs its own subtree.
		if rel != r.base && !strings.HasPrefix(rel, r.base+"/") {
			return false
		}
		rel = strings.TrimPrefix(rel, r.base+"/")
	}
	if r.anchored {
		return matchPath(r.pattern, rel)
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
