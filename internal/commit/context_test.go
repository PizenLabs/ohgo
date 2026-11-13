package commit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// TestClassificationSeparatesProtectedAmbiguousAndUnrelated pins the central
// safety invariant: the working tree may contain several kinds of change, and
// only the classified logical change set may enter the proposal.
func TestClassificationSeparatesProtectedAmbiguousAndUnrelated(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "src/a.go", "package src\n")
	initialCommit(t, repo, "src/a_test.go", "package src\n")
	initialCommit(t, repo, "tui/view.go", "package tui\n")

	writeTestFile(t, repo, "src/a.go", "package src\n\nfunc A() {}\n")
	writeTestFile(t, repo, "src/a_test.go", "package src\n\nfunc TestA(t *testing.T) {}\n")
	writeTestFile(t, repo, "tui/view.go", "package tui\n\nfunc View() {}\n")
	writeTestFile(t, repo, ".env", "SECRET_TOKEN=must-not-be-sent\n")
	writeTestFile(t, repo, "src/generated.log", "local noise\n")
	writeTestFile(t, repo, "asset.bin", "binary\x00payload")

	service := testService(repo, "feat(src): add a testable view\n\n- add src/a_test.go")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	commitContext := request.Snapshot.Context
	if commitContext == nil {
		t.Fatal("snapshot carries no commit context")
	}
	for _, path := range []string{"src/a.go", "src/a_test.go"} {
		if !containsPath(request.Snapshot.Scope, path) {
			t.Errorf("scope = %s, want %s", renderChanges(request.Snapshot.Scope), path)
		}
	}
	if containsPath(request.Snapshot.Scope, "tui/view.go") {
		t.Errorf("unrelated group entered the scope: %s", renderChanges(request.Snapshot.Scope))
	}
	if !containsPath(commitContext.Excluded, "tui/view.go") {
		t.Errorf("excluded = %s, want the tui group", renderChanges(commitContext.Excluded))
	}
	if !containsPath(commitContext.Protected, ".env") || !containsPath(commitContext.Protected, "src/generated.log") {
		t.Errorf("protected = %s, want .env and generated.log", renderChanges(commitContext.Protected))
	}
	if !containsPath(commitContext.Ambiguous, "asset.bin") {
		t.Errorf("ambiguous = %s, want the untracked binary", renderChanges(commitContext.Ambiguous))
	}
	if strings.Contains(request.Snapshot.Evidence(), "SECRET_TOKEN") {
		t.Error("protected credential content reached the model evidence")
	}
	if !strings.Contains(request.Snapshot.Evidence(), "excluded changes (never include):") {
		t.Error("evidence does not state the exclusions")
	}
	if len(commitContext.Groups) != 2 {
		t.Fatalf("groups = %#v, want src and tui", commitContext.Groups)
	}
	if commitContext.Groups[0].Key != "src" {
		t.Errorf("first group = %q, want src", commitContext.Groups[0].Key)
	}

	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	committed := strings.TrimSpace(runTestGit(t, repo, "diff", "--name-only", "HEAD~1", "HEAD"))
	if committed != "src/a.go\nsrc/a_test.go" {
		t.Errorf("committed = %q, want exactly the selected source group", committed)
	}
	if _, err := os.Stat(filepath.Join(repo, ".env")); err != nil {
		t.Errorf("protected .env was removed by the commit: %v", err)
	}
	if status := runTestGit(t, repo, "status", "--porcelain"); !strings.Contains(status, "?? .env") {
		t.Errorf("status = %q, want the protected file left uncommitted", status)
	}
}

// TestGroupingKeepsImplementationAndTestsTogether proves a test file in the
// same package directory is not its own logical change.
func TestGroupingKeepsImplementationAndTestsTogether(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "pkg/a.go", "package pkg\n")
	writeTestFile(t, repo, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	writeTestFile(t, repo, "pkg/a_test.go", "package pkg\n")

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Context.Groups) != 1 {
		t.Fatalf("groups = %s, want one", FormatGroupSummary(snapshot.Context.Groups))
	}
	group := snapshot.Context.Groups[0]
	if len(group.Files) != 2 {
		t.Errorf("group files = %s, want implementation and test", renderChanges(group.Files))
	}
	if len(snapshot.Scope) != 2 {
		t.Errorf("scope = %s, want both files", renderChanges(snapshot.Scope))
	}
}

// TestSeparateTestsDirectoryJoinsTheImplementationCommit proves test grouping
// is not limited to co-located files.
func TestSeparateTestsDirectoryJoinsTheImplementationCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/foo/a.go", "package foo\n")
	writeTestFile(t, repo, "internal/foo/a.go", "package foo\n\nfunc A() {}\n")
	writeTestFile(t, repo, "tests/foo/a_test.go", "package foo_test\n")

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Context.Groups) != 1 {
		t.Fatalf("groups = %s, want tests joined to the implementation",
			FormatGroupSummary(snapshot.Context.Groups))
	}
	if len(snapshot.Scope) != 2 {
		t.Errorf("scope = %s, want implementation and test", renderChanges(snapshot.Scope))
	}
}

// TestUnrelatedTestGroupIsNotAbsorbed proves the test merge rule still requires
// evidence that the tests describe the implementation group.
func TestUnrelatedTestGroupIsNotAbsorbed(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/ast/a.go", "package ast\n")
	writeTestFile(t, repo, "internal/ast/a.go", "package ast\n\nfunc A() {}\n")
	writeTestFile(t, repo, "tests/tui/tui_test.go", "package tui_test\n")

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Context.Groups) != 2 {
		t.Fatalf("groups = %s, want the unrelated tests kept separate",
			FormatGroupSummary(snapshot.Context.Groups))
	}
	if len(snapshot.Scope) != 1 || snapshot.Scope[0].Path != "internal/ast/a.go" {
		t.Errorf("scope = %s, want only the implementation group", renderChanges(snapshot.Scope))
	}
}

// TestGroupingSplitsIndependentSourceChanges proves a single /commit does not
// silently absorb unrelated packages.
func TestGroupingSplitsIndependentSourceChanges(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/ast/graph.go", "package ast\n")
	initialCommit(t, repo, "internal/tui/view.go", "package tui\n")
	writeTestFile(t, repo, "internal/ast/graph.go", "package ast\n\nfunc Graph() {}\n")
	writeTestFile(t, repo, "internal/tui/view.go", "package tui\n\nfunc View() {}\n")
	writeTestFile(t, repo, ".gitignore", "*.log\n")

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Context.Groups) != 3 {
		t.Fatalf("groups = %s, want two source groups and the root support file",
			FormatGroupSummary(snapshot.Context.Groups))
	}
	if containsPath(snapshot.Scope, ".gitignore") {
		t.Errorf("root support file won the selection: %s", renderChanges(snapshot.Scope))
	}
	if snapshot.Context.StructuralEvidence != "paths only" {
		t.Errorf("structural evidence = %q, want the explicit degradation", snapshot.Context.StructuralEvidence)
	}
	selected := 0
	for _, group := range snapshot.Context.Groups {
		if group.Selected {
			selected++
		}
	}
	if selected != 1 {
		t.Errorf("selected groups = %d, want exactly one", selected)
	}
	if len(snapshot.Scope) != 1 {
		t.Errorf("scope = %s, want one logical group", renderChanges(snapshot.Scope))
	}
}

// TestImportEvidenceMergesRelatedPackages proves grouping uses a structural
// relationship that a filename heuristic cannot see.
func TestImportEvidenceMergesRelatedPackages(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "go.mod", "module example.com/test\n\ngo 1.27\n")
	initialCommit(t, repo, "internal/a/a.go", "package a\n")
	initialCommit(t, repo, "internal/b/b.go", "package b\n")
	writeTestFile(t, repo, "internal/a/a.go",
		"package a\n\nimport _ \"example.com/test/internal/b\"\n\nfunc A() {}\n")
	writeTestFile(t, repo, "internal/b/b.go", "package b\n\nfunc B() {}\n")

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Context.Groups) != 1 {
		t.Fatalf("groups = %s, want the import-connected packages merged",
			FormatGroupSummary(snapshot.Context.Groups))
	}
	if !strings.Contains(snapshot.Context.StructuralEvidence, "import") {
		t.Errorf("structural evidence = %q, want the proven import relationship",
			snapshot.Context.StructuralEvidence)
	}
	evidence := snapshot.Context.Groups[0].Evidence
	joined := strings.Join(evidence, "; ")
	if !strings.Contains(joined, "imports") {
		t.Errorf("group evidence = %v, want the import edge", evidence)
	}
}

// TestGroupingIsIndependentOfChangeOrder pins the grouping determinism: an
// import edge is symmetric evidence, so visiting the imported package first
// must not split the component or change the plan order.
func TestGroupingIsIndependentOfChangeOrder(t *testing.T) {
	provider := staticEdgeProvider{edges: []EvidenceEdge{{
		From: "internal/a/a.go", To: "internal/b/b.go",
		Kind: EvidenceImport, Detail: "imports internal/b",
	}}}
	forward := []FileChange{{Path: "internal/a/a.go"}, {Path: "internal/b/b.go"}}
	reverse := []FileChange{{Path: "internal/b/b.go"}, {Path: "internal/a/a.go"}}
	for i := 0; i < 100; i++ {
		left, _ := buildGroups(context.Background(), groupingInput{
			Root: t.TempDir(), Changes: forward, Structural: provider,
		})
		right, _ := buildGroups(context.Background(), groupingInput{
			Root: t.TempDir(), Changes: reverse, Structural: provider,
		})
		if len(left) != 1 || len(right) != 1 {
			t.Fatalf("iteration %d produced %d/%d groups; an import edge was lost", i, len(left), len(right))
		}
		if left[0].Key != right[0].Key || len(left[0].Files) != 2 {
			t.Fatalf("iteration %d groups differ: %+v vs %+v", i, left[0], right[0])
		}
	}
}

// staticEdgeProvider is a test-only structural provider.
type staticEdgeProvider struct {
	edges []EvidenceEdge
}

func (p staticEdgeProvider) Edges(context.Context, string, []FileChange) []EvidenceEdge {
	return append([]EvidenceEdge(nil), p.edges...)
}

// TestBuildOnlyChangeRequiresAnInfrastructureType proves CI/build work is not
// labeled as a feature just because it changes repository files.
func TestBuildOnlyChangeRequiresAnInfrastructureType(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "go.mod", "module example.com/test\n\ngo 1.27\n")
	writeTestFile(t, repo, "go.mod", "module example.com/test\n\ngo 1.27\n\nrequire example.com/x v1.0.0\n")
	snapshot := snapshotOf(t, repo)

	if err := snapshot.ValidateProposal(Proposal{Type: "feat", Scope: "test", Summary: "update deps"}); err == nil ||
		ValidationCode(err) != CodeInvalidType {
		t.Fatalf("feat for a build-only change = %v, want %s", err, CodeInvalidType)
	}
	if err := snapshot.ValidateProposal(Proposal{Type: "build", Scope: "test", Summary: "update deps"}); err != nil {
		t.Errorf("build for a build-only change: %v", err)
	}
}

// TestObjectiveSelectsTheNamedGroup proves task context is evidence for
// grouping without overriding the working tree.
func TestObjectiveSelectsTheNamedGroup(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/ast/graph.go", "package ast\n")
	initialCommit(t, repo, "internal/tui/view.go", "package tui\n")
	writeTestFile(t, repo, "internal/ast/graph.go", "package ast\n\nfunc Graph() {}\n")
	writeTestFile(t, repo, "internal/tui/view.go", "package tui\n\nfunc View() {}\n")

	snapshot, err := BuildSnapshotWithObjective(context.Background(), discoverRepo(t, repo), "polish the tui view")
	if err != nil {
		t.Fatalf("BuildSnapshotWithObjective: %v", err)
	}
	if len(snapshot.Scope) != 1 || snapshot.Scope[0].Path != "internal/tui/view.go" {
		t.Errorf("scope = %s, want the group the objective names", renderChanges(snapshot.Scope))
	}
	if snapshot.Context.Objective != "polish the tui view" {
		t.Errorf("objective = %q", snapshot.Context.Objective)
	}
}

// TestGroupingMergesDocumentationForTheSelectedSubsystem proves docs that
// describe the changed subsystem stay in its commit.
func TestGroupingMergesDocumentationForTheSelectedSubsystem(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/commit/service.go", "package commit\n")
	initialCommit(t, repo, "docs/commit.md", "# commit\n")
	writeTestFile(t, repo, "internal/commit/service.go", "package commit\n\nfunc Service() {}\n")
	writeTestFile(t, repo, "docs/commit.md", "# commit\n\nupdated\n")

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Context.Groups) != 1 {
		t.Fatalf("groups = %s, want docs merged into commit", FormatGroupSummary(snapshot.Context.Groups))
	}
	if len(snapshot.Scope) != 2 {
		t.Errorf("scope = %s, want implementation and docs", renderChanges(snapshot.Scope))
	}
}

// TestSemanticTypeAlignment validates the whole-set semantics rather than the
// extension of one file.
func TestSemanticTypeAlignment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, repo string)
		typ     string
		wantErr bool
	}{
		{
			name: "docs only",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "README.md", "# project\n")
			},
			typ: "docs",
		},
		{
			name: "docs typed as feature",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "README.md", "# project\n")
			},
			typ:     "feat",
			wantErr: true,
		},
		{
			name: "tests only",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "pkg/a_test.go", "package pkg\n")
			},
			typ: "test",
		},
		{
			name: "tests typed as fix",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "pkg/a_test.go", "package pkg\n")
			},
			typ:     "fix",
			wantErr: true,
		},
		{
			name: "implementation plus tests is not test-only",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "pkg/a.go", "package pkg\n")
				writeTestFile(t, repo, "pkg/a_test.go", "package pkg\n")
			},
			typ:     "test",
			wantErr: true,
		},
		{
			name: "implementation typed as docs",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "pkg/a.go", "package pkg\n")
			},
			typ:     "docs",
			wantErr: true,
		},
		{
			name: "unknown type",
			setup: func(t *testing.T, repo string) {
				writeTestFile(t, repo, "pkg/a.go", "package pkg\n")
			},
			typ:     "wip",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			initialCommit(t, repo, "base.txt", "base\n")
			tc.setup(t, repo)
			snapshot := snapshotOf(t, repo)
			proposal := Proposal{Type: tc.typ, Scope: "pkg", Summary: "change"}
			if tc.typ == "docs" {
				proposal.Summary = "update docs"
			}
			err := snapshot.ValidateProposal(proposal)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateProposal accepted %s for %s", tc.typ, tc.name)
				}
				if !errors.Is(err, ErrInvalidProposal) {
					t.Errorf("error = %v, want ErrInvalidProposal", err)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateProposal rejected %s: %v", tc.typ, err)
			}
		})
	}
}

// TestCanonicalTypeVocabularyIsAccepted proves the validator permits the
// repository's conventional types for an implementation change.
func TestCanonicalTypeVocabularyIsAccepted(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	snapshot := snapshotOf(t, repo)

	for _, typ := range []string{"feat", "fix", "refactor", "perf", "chore", "revert", "style"} {
		if err := snapshot.ValidateProposal(Proposal{Type: typ, Scope: "pkg", Summary: "update pkg"}); err != nil {
			t.Errorf("type %q rejected: %v", typ, err)
		}
	}
}

// TestSemanticClaimsAreGroundedInTheChangeSet covers invention and process
// narration, the commit analogue of the evidence ledger.
func TestSemanticClaimsAreGroundedInTheChangeSet(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "pkg/new.go", "package pkg\n")
	snapshot := snapshotOf(t, repo)

	valid := Proposal{Type: "feat", Scope: "pkg", Summary: "add a new file"}
	if err := snapshot.ValidateProposal(valid); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	withPeriod := valid
	withPeriod.Body = []string{"- add pkg/new.go."}
	if err := snapshot.ValidateProposal(withPeriod); err != nil {
		t.Errorf("a sentence-ending period made a real path claim fail: %v", err)
	}
	ratio := valid
	ratio.Body = []string{"- keep the parser available 24/7"}
	if err := snapshot.ValidateProposal(ratio); err != nil {
		t.Errorf("a ratio was treated as a repository path: %v", err)
	}
	for _, body := range []string{
		"- read config from the environment",
		"- staged rollout of the new graph",
		"- checked values remain compatible",
	} {
		benign := valid
		benign.Body = []string{body}
		if err := snapshot.ValidateProposal(benign); err != nil {
			t.Errorf("benign change description %q rejected: %v", body, err)
		}
	}
	for _, tc := range []struct {
		name string
		body []string
		code string
	}{
		{"invented removal", []string{"- remove legacy authentication"}, CodeUnsupportedClaim},
		{"invented file", []string{"- update missing.go"}, CodeUnsupportedClaim},
		{"process narration", []string{"- inspected git status"}, CodeProcessNarration},
		{"git command narration", []string{"- ran git diff and staged files"}, CodeProcessNarration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proposal := valid
			proposal.Body = tc.body
			err := snapshot.ValidateProposal(proposal)
			if err == nil {
				t.Fatalf("ValidateProposal accepted %q", tc.body[0])
			}
			if got := ValidationCode(err); got != tc.code {
				t.Errorf("code = %q, want %q (%v)", got, tc.code, err)
			}
		})
	}
}

// TestNegatedClaimsDoNotRequireTheOppositeChurn proves a body may truthfully
// say "no tests added" on a deletion-only change.
func TestNegatedClaimsDoNotRequireTheOppositeChurn(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "pkg/a.go", "package pkg\n\nfunc A() {}\n")
	writeTestFile(t, repo, "pkg/a.go", "package pkg\n")
	snapshot := snapshotOf(t, repo)

	proposal := Proposal{
		Type:    "refactor",
		Scope:   "pkg",
		Summary: "remove function A",
		Body:    []string{"- no tests added"},
	}
	if err := snapshot.ValidateProposal(proposal); err != nil {
		t.Errorf("a negated claim was treated as an unsupported addition: %v", err)
	}
}

// TestProposeRepairsAMisalignedProposalInOneBoundedTurn proves the repair loop
// sends only the concrete rule, not the repository, and caps the turns.
func TestProposeRepairsAMisalignedProposalInOneBoundedTurn(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "pkg/new.go", "package pkg\n")

	var calls int
	var second ai.Request
	var diagnostics ProposalDiagnostics
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		OnDiagnostics: func(d ProposalDiagnostics) {
			diagnostics = d
		},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			calls++
			if calls == 1 {
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: "docs(pkg): add a new file"}},
				}), nil
			}
			second = req
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "feat(pkg): add a new file"}},
			}), nil
		},
	}
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if calls != 2 {
		t.Errorf("model calls = %d, want the proposal plus one repair", calls)
	}
	if request.Proposal.Type != "feat" {
		t.Errorf("type = %q, want the repaired proposal", request.Proposal.Type)
	}
	if !strings.Contains(second.Messages[0].(ai.UserMessage).Text(), CodeInvalidType) {
		t.Errorf("repair request does not name the rule: %q", second.Messages[0].(ai.UserMessage).Text())
	}
	if len(diagnostics.RetryReasons) != 1 || !strings.Contains(diagnostics.RetryReasons[0], CodeInvalidType) {
		t.Errorf("retry reasons = %v, want %s", diagnostics.RetryReasons, CodeInvalidType)
	}
	if diagnostics.Phase != PhaseRepair {
		t.Errorf("phase = %q, want repair", diagnostics.Phase)
	}
}

// TestTruncationRetryDisablesReasoning proves the budget recovery changes how
// the budget is spent and never accepts a partial message.
func TestTruncationRetryDisablesReasoning(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "pkg/new.go", "package pkg\n")

	var calls int
	var second ai.Request
	service := &Service{
		Root:   repo,
		Model:  ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Effort: "high",
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			calls++
			if calls == 1 {
				if !req.Reasoning {
					t.Error("the first attempt did not use the configured reasoning")
				}
				return ai.NewCompletedStream(ai.AssistantMessage{
					StopReason: ai.StopMaxTokens,
				}), nil
			}
			second = req
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "feat(pkg): add a new file"}},
			}), nil
		},
	}
	if _, err := service.Propose(context.Background()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want one bounded truncation retry", calls)
	}
	if second.Reasoning || second.ReasoningEffort != "" {
		t.Errorf("retry reasoning = %v/%q, want the explicit disable", second.Reasoning, second.ReasoningEffort)
	}
}

// TestFingerprintDetectsAModeOnlyChange proves the stale-proposal guard does
// not rely on content alone: a chmod would otherwise be absorbed silently.
func TestFingerprintDetectsAModeOnlyChange(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	before := snapshotOf(t, repo).Fingerprint
	if err := os.Chmod(filepath.Join(repo, "a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := snapshotOf(t, repo).Fingerprint
	if before == after {
		t.Error("fingerprint did not change after a mode-only edit")
	}
}

// TestConflictBlocksTheCommit proves an unresolved index is never guessed at.
func TestConflictBlocksTheCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	runTestGit(t, repo, "checkout", "-b", "side")
	writeTestFile(t, repo, "a.txt", "side\n")
	stageTestFile(t, repo, "a.txt")
	runTestGit(t, repo, "commit", "-qm", "side")
	runTestGit(t, repo, "checkout", "-")
	writeTestFile(t, repo, "a.txt", "main\n")
	stageTestFile(t, repo, "a.txt")
	runTestGit(t, repo, "commit", "-qm", "main")
	merge := exec.Command("git", "merge", "side")
	merge.Dir = repo
	_ = merge.Run()

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrBlockedChanges) {
		t.Fatalf("BuildSnapshot = %v, want ErrBlockedChanges", err)
	}
}

// TestInProgressMergeBlocksTheCommit proves /commit never turns a human merge
// state into an ordinary single-parent commit.
func TestInProgressMergeBlocksTheCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	if err := os.WriteFile(filepath.Join(repo, ".git", "MERGE_HEAD"),
		[]byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrBlockedChanges) {
		t.Fatalf("BuildSnapshot = %v, want ErrBlockedChanges for an in-progress merge", err)
	}
}

// TestNestedRepositoryIsExcludedNotAbsorbed proves the parent commit never
// absorbs a nested repository's files.
func TestNestedRepositoryIsExcludedNotAbsorbed(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	nested := filepath.Join(repo, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, nested, "init", "--quiet")
	runTestGit(t, nested, "config", "user.email", "test@example.com")
	runTestGit(t, nested, "config", "user.name", "Test")
	writeTestFile(t, nested, "inner.txt", "inner\n")

	snapshot := snapshotOf(t, repo)
	if containsPath(snapshot.Scope, "nested") {
		t.Errorf("nested repository entered the scope: %s", renderChanges(snapshot.Scope))
	}
	if !containsPath(snapshot.Context.Excluded, "nested/") && !containsPath(snapshot.Context.Excluded, "nested") {
		t.Errorf("excluded = %s, want the nested repository boundary", renderChanges(snapshot.Context.Excluded))
	}
}

// TestDeletingAProtectedFileIsAllowed proves the protection is about content
// inclusion: removing a tracked secret commits no secret.
func TestDeletingAProtectedFileIsAllowed(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	initialCommit(t, repo, ".env", "TOKEN=old\n")
	if err := os.Remove(filepath.Join(repo, ".env")); err != nil {
		t.Fatal(err)
	}

	snapshot := snapshotOf(t, repo)
	if !containsPath(snapshot.Scope, ".env") {
		t.Errorf("scope = %s, want the protected-file deletion", renderChanges(snapshot.Scope))
	}
	if len(snapshot.Context.Protected) != 0 {
		t.Errorf("protected = %s, want none for a deletion", renderChanges(snapshot.Context.Protected))
	}
	if strings.Contains(snapshot.Evidence(), "TOKEN=old") {
		t.Error("the removed credential bytes reached the model evidence")
	}
	if !strings.Contains(snapshot.Evidence(), "content redacted for protected paths:") {
		t.Error("evidence does not state that the deletion content is redacted")
	}
}

// TestProtectedOnlyTreeRequiresADecision proves a tree with nothing safe to
// commit is surfaced as a human decision, not committed or reported as an empty
// repository.
func TestProtectedOnlyTreeRequiresADecision(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, ".env", "TOKEN=secret\n")

	_, err := testService(repo, "unused").Propose(context.Background())
	if !errors.Is(err, ErrNeedsDecision) {
		t.Fatalf("Propose = %v, want ErrNeedsDecision", err)
	}
	text := AbortText(err)
	if !strings.Contains(text, "Commit requires a decision.") || !strings.Contains(text, ".env") {
		t.Errorf("AbortText = %q, want the decision and the protected path", text)
	}
	if strings.Contains(text, "TOKEN=secret") {
		t.Error("AbortText leaked the protected credential")
	}
}

// TestStagedAmbiguousRenameIsRefused proves the staged-index path cannot absorb
// a change the control plane could not classify: staging commits the whole
// index, so an ambiguous staged path is a refusal rather than an exclusion.
func TestStagedAmbiguousRenameIsRefused(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	initialCommit(t, repo, ".env", "TOKEN=old\n")
	runTestGit(t, repo, "mv", ".env", "config.yaml")

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrStagedExcluded) {
		t.Fatalf("BuildSnapshot = %v, want ErrStagedExcluded", err)
	}
}

// TestStagedProtectedPathIsRefused proves a human staging action cannot make a
// credential path committable by /commit.
func TestStagedProtectedPathIsRefused(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, ".env", "TOKEN=secret\n")
	runTestGit(t, repo, "add", "-f", "--", ".env")

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrStagedExcluded) {
		t.Fatalf("BuildSnapshot = %v, want ErrStagedExcluded", err)
	}
}

// TestDirectoryStatusIsClassifiedAsSubmoduleOrNested covers the classification
// seam directly, since a submodule needs a real remote repository to exercise
// end to end.
func TestDirectoryStatusIsClassifiedAsSubmoduleOrNested(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	change := FileChange{Path: "pkg", Index: 'M', Worktree: 'M'}
	classifyOne(root, &change)
	if change.Category != CategoryBlocked || change.Kind != KindSubmodule {
		t.Errorf("plain directory = %s/%s, want blocked submodule", change.Category, change.Kind)
	}

	if err := os.MkdirAll(filepath.Join(root, "nested", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	change = FileChange{Path: "nested", Index: '?', Worktree: '?', Untracked: true}
	classifyOne(root, &change)
	if change.Category != CategoryBlocked || change.Kind != KindNestedRepository {
		t.Errorf("nested repository = %s/%s, want blocked nested repository", change.Category, change.Kind)
	}
}

// TestSecretAndArtifactRulesAreNarrow keeps the protected list honest: it must
// catch credentials and local state without swallowing ordinary source.
func TestSecretAndArtifactRulesAreNarrow(t *testing.T) {
	for _, path := range []string{".env", ".env.local", "deploy/signing.key", ".ssh/id_rsa", "secrets.yaml"} {
		if secretReason(path) == "" {
			t.Errorf("%q was not protected as secret material", path)
		}
	}
	for _, path := range []string{"internal/provider/creds/creds.go", "docs/security.md", "keys.go", "config.yaml", ".env.example"} {
		if reason := secretReason(path); reason != "" {
			t.Errorf("%q was protected as %q", path, reason)
		}
	}
	for _, path := range []string{".DS_Store", "coverage.out", "tmp/debug.log", "node_modules/pkg/index.js"} {
		if !isLocalArtifact(path) {
			t.Errorf("%q was not recognized as local or generated state", path)
		}
	}
	for _, path := range []string{"internal/build/plan.go", "cmd/ohgo/commit.go"} {
		if isLocalArtifact(path) {
			t.Errorf("%q was classified as local state", path)
		}
	}
}

// TestRecentHistoryTeachesStyleWithoutCopyingIt proves the model receives a
// bounded convention sample and only current evidence grants references.
func TestRecentHistoryTeachesStyleWithoutCopyingIt(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	writeTestFile(t, repo, "b.txt", "b\n")
	stageTestFile(t, repo, "b.txt")
	runTestGit(t, repo, "commit", "-qm", "feat(commit): add b\n\n- details from a previous body")
	writeTestFile(t, repo, "a.txt", "three\n")

	snapshot := snapshotOf(t, repo)
	style := snapshot.Context.Style
	if style.Commits < 2 {
		t.Fatalf("style samples = %d, want the recent history", style.Commits)
	}
	if len(style.Types) == 0 || style.Types[0] != "feat" {
		t.Errorf("types = %v, want the observed feat type", style.Types)
	}
	if style.BodyRatio() == 0 {
		t.Error("body usage was not observed")
	}
	evidence := snapshot.Evidence()
	if !strings.Contains(evidence, "feat(commit): add b") {
		t.Errorf("evidence = %q, want the bounded history sample", evidence)
	}
	if strings.Contains(evidence, "details from a previous body") {
		t.Error("a previous body was copied into the model evidence")
	}
}

// TestDiagnosticsReportClassificationFacts proves the proposal request is
// measurable without reading the UI.
func TestDiagnosticsReportClassificationFacts(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "src/a.go", "package src\n")
	initialCommit(t, repo, "tui/view.go", "package tui\n")
	writeTestFile(t, repo, "src/a.go", "package src\n\nfunc A() {}\n")
	writeTestFile(t, repo, "tui/view.go", "package tui\n\nfunc View() {}\n")
	writeTestFile(t, repo, ".env", "TOKEN=secret\n")

	var diagnostics ProposalDiagnostics
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		OnDiagnostics: func(d ProposalDiagnostics) {
			diagnostics = d
		},
		Stream: staticStream("feat(src): add an entry\n\n- add src/a.go"),
	}
	if _, err := service.Propose(context.Background()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if diagnostics.SelectedFiles != 1 || diagnostics.ExcludedFiles != 1 ||
		diagnostics.ProtectedFiles != 1 || diagnostics.Groups != 2 {
		t.Errorf("diagnostics = %+v, want selected/excluded/protected/groups 1/1/1/2", diagnostics)
	}
}
