package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/project"
)

// writeFile creates a file, making its parent directories first.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProjectContextReadsBothInstructionFiles pins the hierarchy: the root
// AGENTS.md is the primary source and the optional .ohgo/AGENTS.md is
// supplementary, so an OHGO-local file must never displace the root one or be
// dropped when both exist.
func TestProjectContextReadsBothInstructionFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, project.DirName, "AGENTS.md"), "ohgo rules")

	got := ProjectContext(root)
	rootAt := strings.Index(got, "root rules")
	localAt := strings.Index(got, "ohgo rules")
	if rootAt < 0 {
		t.Errorf("root AGENTS.md was discarded when .ohgo/AGENTS.md exists: %q", got)
	}
	if localAt < 0 {
		t.Errorf(".ohgo/AGENTS.md was not discovered: %q", got)
	}
	if rootAt >= 0 && localAt >= 0 && rootAt > localAt {
		t.Errorf("root AGENTS.md must be read before .ohgo/AGENTS.md, got:\n%s", got)
	}
}

// TestProjectContextReadsOhgoLocalAlone covers the supplementary file standing
// on its own: a project that keeps its instructions only under .ohgo/ still
// contributes them.
func TestProjectContextReadsOhgoLocalAlone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, project.DirName, "AGENTS.md"), "ohgo only")

	if got := ProjectContext(root); !strings.Contains(got, "ohgo only") {
		t.Errorf(".ohgo/AGENTS.md alone was ignored: %q", got)
	}
}

// TestProjectContextReadsRootAlone covers the expected common case: a project
// with only the root AGENTS.md behaves as before.
func TestProjectContextReadsRootAlone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root only")

	got := ProjectContext(root)
	if !strings.Contains(got, "root only") {
		t.Errorf("root AGENTS.md was ignored: %q", got)
	}
	if strings.Contains(got, "OHGO project instructions") {
		t.Errorf("a supplementary section appeared with no .ohgo/AGENTS.md: %q", got)
	}
}

// TestProjectContextEmptyWhenNoInstructions keeps an unconfigured project from
// emitting an empty instructions block the model would have to interpret.
func TestProjectContextEmptyWhenNoInstructions(t *testing.T) {
	if got := ProjectContext(t.TempDir()); got != "" {
		t.Errorf("no instruction files should yield no context, got %q", got)
	}
}

// TestProjectContextAnchorsOhgoLocalAtProjectRoot pins the project boundary: a
// working directory nested under a Git worktree still resolves the
// supplementary file at the worktree root, and the primary file by nearest
// match. A nested AGENTS.md stays the primary source for its directory, but
// must not hide the project's OHGO-local instructions.
func TestProjectContextAnchorsOhgoLocalAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, project.DirName, "AGENTS.md"), "ohgo rules")
	nested := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nested, "AGENTS.md"), "nested rules")

	got := ProjectContext(nested)
	if !strings.Contains(got, "nested rules") {
		t.Errorf("nearest AGENTS.md should be the primary source, got:\n%s", got)
	}
	if !strings.Contains(got, "ohgo rules") {
		t.Errorf(".ohgo/AGENTS.md at the project root was not discovered from a nested directory:\n%s", got)
	}
	if strings.Index(got, "nested rules") > strings.Index(got, "ohgo rules") {
		t.Errorf("the primary instructions must precede the supplementary ones:\n%s", got)
	}
}

// A referenced file is rendered with its path and its exact bytes, so the model
// can cite the source and reason over the real content. No files yields no
// block rather than an empty header.
func TestMentionContextRendersPathAndContent(t *testing.T) {
	if got := MentionContext(nil); got != "" {
		t.Fatalf("MentionContext(nil) = %q, want empty", got)
	}
	got := MentionContext([]ReferencedFile{{Path: "tui/app.go", Content: "package tui"}})
	if !strings.Contains(got, `path="tui/app.go"`) {
		t.Errorf("context = %q, want the path", got)
	}
	if !strings.Contains(got, "package tui") {
		t.Errorf("context = %q, want the content verbatim", got)
	}
}

// The identity block is the contract that keeps the first turn from turning
// into a brochure: a plain greeting must not produce a self-introduction, a
// tool roster, or a capability list. Each assertion pins one clause the model
// would otherwise be free to ignore.
func TestIdentityForbidsSelfIntroduction(t *testing.T) {
	lower := strings.ToLower(Identity)
	for _, want := range []string{
		"task execution engine",
		"do not introduce yourself",
		"never open with a self-introduction",
		"do not list tools",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("identity does not forbid boilerplate (%q):\n%s", want, Identity)
		}
	}
}

// TestProposalDecisionRendersHumanProvenance proves a recorded decision
// reaches the model as control-plane-authored text: the human's choice, the
// question it answered, and its provenance, never bare wording the model could
// mistake for its own inference.
func TestProposalDecisionRendersHumanProvenance(t *testing.T) {
	text, ok := ProposalDecision(
		"One file",
		`Keep "portfolio site" in one self-contained file.`,
		`How should I organize "portfolio site"?`,
		"portfolio site",
	)
	if !ok {
		t.Fatal("a structural choice produced no directive")
	}
	for _, want := range []string{
		"decided by the human",
		"Requested work: portfolio site",
		`Decision: How should I organize "portfolio site"?`,
		"Choice: One file.",
		"one self-contained file",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("decision block lost %q: %q", want, text)
		}
	}
}

// TestProposalDecisionDelegationAddsNoConstraint proves a delegated choice
// constrains nothing: explicit delegation grants the authority rather than
// directing it.
func TestProposalDecisionDelegationAddsNoConstraint(t *testing.T) {
	if text, ok := ProposalDecision("Let the model decide", "", "Which approach?", "the work"); ok || text != "" {
		t.Errorf("delegation constrained execution with %q", text)
	}
}
