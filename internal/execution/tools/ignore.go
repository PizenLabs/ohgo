package tools

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/ignore"
)

// ignorer is the tools package's view of the shared ignore matcher.
//
// A production ignorer delegates to ignoreScope, the same evaluator the
// structural index uses. That evaluator owns the nested-repository boundary:
// an enclosing .gitignore stops at a nested repository, whose own rules govern
// its contents. Lexical discovery and structural overview therefore answer
// "is this file in the repository?" identically instead of implementing two
// subtly different ancestor rules.
type ignorer struct {
	// enabled is false for the explicit `gitignore: false` override. Keeping
	// the field (rather than a nil ignorer) lets every call site apply the
	// switch without a second branch.
	enabled bool
	// scope is the shared evaluator. Tests may construct a matcher directly.
	scope *ignoreScope
	// matcher is the literal-rule seam used by unit tests that do not touch
	// the filesystem. Production never sets both.
	matcher *ignore.Matcher
}

// newIgnorer builds an evaluator rooted at root.
func newIgnorer(root string, enabled bool) *ignorer {
	if !enabled {
		return &ignorer{}
	}
	return &ignorer{enabled: true, scope: &ignoreScope{root: root}}
}

// ignored reports whether a root-relative path is excluded.
func (ig *ignorer) ignored(rel string, isDir bool) bool {
	if ig == nil || !ig.enabled {
		return false
	}
	if ig.scope != nil {
		return ig.scope.Ignored(rel, isDir)
	}
	return ig.matcher.Ignored(rel, isDir)
}

// explain names the rule that excluded rel, empty when no rule decided it.
func (ig *ignorer) explain(rel string, isDir bool) string {
	if ig == nil || !ig.enabled {
		return ""
	}
	if ig.scope != nil {
		return ig.scope.Explain(rel, isDir)
	}
	excluded, reason := ig.matcher.Match(rel, isDir)
	if !excluded {
		return ""
	}
	return reason
}

// exclusion reports whether rel or an ignored ancestor is excluded, with the
// deciding rule. It is the targeted-path lookup: a walk would have pruned the
// ancestor before seeing rel.
func (ig *ignorer) exclusion(rel string, isDir bool) (bool, string) {
	if ig == nil || !ig.enabled {
		return false, ""
	}
	if ig.scope != nil {
		return ig.scope.Exclusion(rel, isDir)
	}
	return ig.matcher.MatchExcluded(rel, isDir)
}

// isHidden reports whether a name is a dotfile or dot-directory.
func isHidden(name string) bool { return ignore.IsHidden(name) }

// maxExclusionExamples bounds the named paths in one result. The count stays
// exact; examples are evidence a reader can act on, not a directory listing.
const maxExclusionExamples = 6

// exclusionExample is one skipped path and the rule that decided it.
type exclusionExample struct {
	Path string
	Rule string
}

// scanExclusions accumulates the scope decisions of one tree walk.
//
// It exists so grep, glob, and a recursive ls all report exclusion the same
// way: an exact count plus a bounded set of named paths and rules. A count
// alone is opaque, and the path alone does not tell the model which rule to
// change or override.
type scanExclusions struct {
	ignored  int
	builtin  int
	examples []exclusionExample
}

// addIgnored records one path pruned by .gitignore.
func (x *scanExclusions) addIgnored(path, rule string) {
	if x == nil {
		return
	}
	x.ignored++
	if len(x.examples) < maxExclusionExamples {
		x.examples = append(x.examples, exclusionExample{Path: path, Rule: rule})
	}
}

// addBuiltin records one path pruned by the built-in dependency/build list.
func (x *scanExclusions) addBuiltin() {
	if x == nil {
		return
	}
	x.builtin++
}

// ignoredNote renders the .gitignore exclusion as one actionable sentence.
func (x *scanExclusions) ignoredNote() string {
	if x == nil || x.ignored == 0 {
		return ""
	}
	noun := "paths were"
	if x.ignored == 1 {
		noun = "path was"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s excluded by .gitignore", x.ignored, noun)
	if len(x.examples) > 0 {
		parts := make([]string, 0, len(x.examples))
		for _, example := range x.examples {
			if example.Rule == "" {
				parts = append(parts, example.Path)
				continue
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", example.Path, example.Rule))
		}
		b.WriteString(": ")
		b.WriteString(strings.Join(parts, ", "))
		if x.ignored > len(x.examples) {
			fmt.Fprintf(&b, ", and %d more", x.ignored-len(x.examples))
		}
	}
	b.WriteString(`; pass "gitignore": false to include ignored paths.`)
	return b.String()
}

// ignoredDetails publishes the same evidence structurally.
func (x *scanExclusions) ignoredDetails() []map[string]any {
	if x == nil || len(x.examples) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(x.examples))
	for _, example := range x.examples {
		item := map[string]any{"path": example.Path}
		if example.Rule != "" {
			item["rule"] = example.Rule
		}
		out = append(out, item)
	}
	return out
}
