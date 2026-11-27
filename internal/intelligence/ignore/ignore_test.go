package ignore

import "testing"

// A nested repository's own rules govern its contents; the enclosing
// repository's rules stop at the boundary. Without this, a root "vendor/" or
// "backend/" rule hides an entire nested project from structural discovery.
func TestIgnoredFromScopesRulesToNestedProject(t *testing.T) {
	matcher := NewRules()
	matcher.Append("", "backend/\n*.log\n")
	matcher.Append("backend", "*.tmp\n!keep.tmp\n")

	if matcher.IgnoredFrom("backend", "backend", true) {
		t.Error("the nested project root was excluded by the enclosing rule")
	}
	if !matcher.IgnoredFrom("backend", "backend/drop.tmp", false) {
		t.Error("the nested project's own rule did not apply")
	}
	if matcher.IgnoredFrom("backend", "backend/keep.tmp", false) {
		t.Error("the nested project's negation did not apply")
	}
	if matcher.IgnoredFrom("backend", "backend/drop.log", false) {
		t.Error("the enclosing repository's rule leaked into the nested project")
	}
	if !matcher.IgnoredFrom("", "backend/drop.log", false) {
		t.Error("the enclosing rule stopped applying outside the nested boundary")
	}
}

// Match returns the decisive source line so a structural snapshot can explain
// its file-count drop instead of presenting it as an unexplained absence.
func TestMatchReportsTheDecisiveRule(t *testing.T) {
	matcher := NewRules()
	matcher.Append("", "# comment\n*.log\n")
	excluded, reason := matcher.Match("a.log", false)
	if !excluded {
		t.Fatal("a.log was not excluded")
	}
	if reason != ".gitignore:2: *.log" {
		t.Errorf("reason = %q, want .gitignore:2: *.log", reason)
	}
	if excluded, reason := matcher.Match("a.go", false); excluded || reason != "" {
		t.Errorf("Match(a.go) = %v %q, want included with no reason", excluded, reason)
	}
}

// A file beneath an ignored directory is outside the boundary even when no
// rule names the file. A walk prunes the directory, but a caller holding the
// path directly (a captured entry, a changed path) must get the same answer.
func TestExcludedIncludesAncestorDirectories(t *testing.T) {
	matcher := NewRules()
	matcher.Append("", "assets/\n")

	if !matcher.Excluded("assets/logo.png", false) {
		t.Error("a file under an ignored directory was not excluded")
	}
	if !matcher.Excluded("assets", true) {
		t.Error("the ignored directory itself was not excluded")
	}
	if matcher.Excluded("src/assets.go", false) {
		t.Error("an unrelated path was excluded")
	}
}

// MatchExcluded names the ancestor rule that a walk would have pruned. A bare
// boolean cannot tell a targeted-path skip from an empty result.
func TestMatchExcludedNamesTheAncestorRule(t *testing.T) {
	matcher := NewRules()
	matcher.Append("", "assets/\n")

	excluded, reason := matcher.MatchExcluded("assets/img/logo.png", false)
	if !excluded {
		t.Fatal("a file under an ignored directory was not excluded")
	}
	if reason != ".gitignore:1: assets/" {
		t.Errorf("reason = %q, want the ignored ancestor's rule", reason)
	}
	if excluded, reason := matcher.MatchExcluded("src/assets.go", false); excluded || reason != "" {
		t.Errorf("MatchExcluded(src/assets.go) = %v %q, want included with no reason", excluded, reason)
	}
}
