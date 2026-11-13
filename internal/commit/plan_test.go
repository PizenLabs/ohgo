package commit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestLogicalGroupingJoinsCrossDirectoryFeature pins the central grouping
// contract: one feature spread across components, hooks, api, and tests is one
// logical change, not five file commits.
func TestLogicalGroupingJoinsCrossDirectoryFeature(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "src/client/components/Checkout.tsx",
		"export function Checkout() { return <div>checkout</div> }\n")
	writeTestFile(t, repo, "src/client/components/CheckoutSummary.tsx",
		"export function CheckoutSummary() { return <div>summary</div> }\n")
	writeTestFile(t, repo, "src/client/hooks/useCheckout.ts",
		"export function useCheckout() { return 'checkout' }\n")
	writeTestFile(t, repo, "src/client/api/checkout.ts",
		"export function loadCheckout() { return 'checkout' }\n")
	writeTestFile(t, repo, "src/client/tests/Checkout.test.tsx",
		"export function testCheckout() { return <div>checkout</div> }\n")
	writeTestFile(t, repo, "src/auth/Login.tsx",
		"export function Login() { return <div>login</div> }\n")
	writeTestFile(t, repo, "src/auth/useLogin.ts",
		"export function useLogin() { return 'login' }\n")
	writeTestFile(t, repo, "README.md", "# project\n")
	writeTestFile(t, repo, ".env", "TOKEN=secret\n")
	writeTestFile(t, repo, ".ohgo/internal-state", "local\n")

	plan, err := (&Service{Root: repo}).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Total != 9 {
		t.Errorf("total changed paths = %d, want 9", plan.Total)
	}
	if len(plan.Protected) != 1 || plan.Protected[0].Path != ".env" {
		t.Errorf("protected = %s, want .env", renderChanges(plan.Protected))
	}
	if len(plan.Internal) != 1 || plan.Internal[0].Path != ".ohgo/internal-state" {
		t.Errorf("internal = %s, want the .ohgo state", renderChanges(plan.Internal))
	}

	checkout, auth, docs := -1, -1, -1
	for i, group := range plan.Groups {
		switch group.Scope {
		case "checkout":
			checkout = i
		case "login":
			auth = i
		case "docs", "readme":
			docs = i
		}
	}
	if checkout < 0 || auth < 0 || docs < 0 {
		t.Fatalf("groups = %s, want checkout, authentication, and documentation groups",
			FormatGroupSummary(plan.Groups))
	}
	if len(plan.Groups[checkout].Files) != 5 {
		t.Errorf("checkout group = %s, want all five feature files",
			renderChanges(plan.Groups[checkout].Files))
	}
	if len(plan.Groups[auth].Files) != 2 {
		t.Errorf("auth group = %s, want both authentication files",
			renderChanges(plan.Groups[auth].Files))
	}
	if len(plan.Groups[docs].Files) != 1 {
		t.Errorf("docs group = %s, want the README", renderChanges(plan.Groups[docs].Files))
	}
	if err := plan.validateAccounting(); err != nil {
		t.Errorf("plan accounting: %v", err)
	}

	// Normal /commit selects the dominant logical change, not each file and
	// not every change in the tree.
	request, err := testService(repo, "feat(checkout): refine checkout flow").Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(request.Snapshot.Scope) != 5 {
		t.Errorf("normal commit scope = %s, want the five-file checkout change",
			renderChanges(request.Snapshot.Scope))
	}
	if containsPath(request.Snapshot.Scope, "src/auth/Login.tsx") ||
		containsPath(request.Snapshot.Scope, "README.md") {
		t.Errorf("normal commit absorbed another logical change: %s",
			renderChanges(request.Snapshot.Scope))
	}
}

// TestScriptImportsJoinGenericFileNames proves a multi-file TypeScript change
// does not fragment when its file names carry no subsystem token.
func TestScriptImportsJoinGenericFileNames(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "src/feature/A.tsx", "export function A() { return 1 }\n")
	writeTestFile(t, repo, "src/feature/B.ts",
		"import { A } from \"./A\"\nexport function B() { return A() }\n")
	writeTestFile(t, repo, "src/feature/C.ts",
		"import { B } from \"./B\"\nexport function C() { return B() }\n")
	writeTestFile(t, repo, "src/feature/D.test.ts",
		"import { C } from \"./C\"\nexport function testD() { return C() }\n")

	plan, err := (&Service{Root: repo}).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 1 || len(plan.Groups[0].Files) != 4 {
		t.Fatalf("groups = %s, want the four imported files as one logical change",
			FormatGroupSummary(plan.Groups))
	}
}

// TestNormalCommitDoesNotAbsorbUnrelatedDocumentation proves a README change is
// not silently included just because the tree has one implementation change.
func TestNormalCommitDoesNotAbsorbUnrelatedDocumentation(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/commit/a.go", "package commit\n")
	writeTestFile(t, repo, "internal/commit/a.go", "package commit\n\nfunc A() {}\n")
	writeTestFile(t, repo, "README.md", "# project\n")

	request, err := testService(repo, "feat(commit): add function A").Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if !containsPath(request.Snapshot.Scope, "internal/commit/a.go") ||
		containsPath(request.Snapshot.Scope, "README.md") {
		t.Errorf("scope = %s, want the implementation change without the README",
			renderChanges(request.Snapshot.Scope))
	}
}

// TestScopeCannotNameAnotherLogicalChange proves the message scope is checked
// against the selected group in a multi-commit plan.
func TestScopeCannotNameAnotherLogicalChange(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	initialCommit(t, repo, "internal/alpha/a.go", "package alpha\n")
	initialCommit(t, repo, "internal/beta/b.go", "package beta\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	writeTestFile(t, repo, "internal/beta/b.go", "package beta\n\nfunc Beta() {}\n")

	snapshot := snapshotOf(t, repo)
	proposal := Proposal{Type: "feat", Scope: "beta", Summary: "update alpha"}
	err := snapshot.ValidateProposal(proposal)
	if err == nil || ValidationCode(err) != CodeInvalidScope {
		t.Fatalf("scope beta for the alpha group = %v, want %s", err, CodeInvalidScope)
	}
}

// TestPlanKeepsSameDirectoryIndependentChangesApart proves a shared directory
// is not a commit boundary.
func TestPlanKeepsSameDirectoryIndependentChangesApart(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "src/components/Checkout.tsx",
		"export function Checkout() { return <div>checkout</div> }\n")
	writeTestFile(t, repo, "src/components/Header.tsx",
		"export function Header() { return <header>navigation</header> }\n")

	plan, err := (&Service{Root: repo}).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("groups = %s, want two independent component changes",
			FormatGroupSummary(plan.Groups))
	}
	for _, group := range plan.Groups {
		if len(group.Files) != 1 {
			t.Errorf("group = %s, want a single independent change", renderChanges(group.Files))
		}
	}
}

// TestPlanDoesNotMergeDependentsOfASharedUtility proves that both changes
// importing the same unchanged utility is not cohesion evidence.
func TestPlanDoesNotMergeDependentsOfASharedUtility(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "go.mod", "module example.com/test\n\ngo 1.27\n")
	initialCommit(t, repo, "internal/util/util.go", "package util\n\nfunc Format() {}\n")
	writeTestFile(t, repo, "internal/a/a.go",
		"package a\n\nimport _ \"example.com/test/internal/util\"\n\nfunc A() {}\n")
	writeTestFile(t, repo, "internal/b/b.go",
		"package b\n\nimport _ \"example.com/test/internal/util\"\n\nfunc B() {}\n")

	plan, err := (&Service{Root: repo}).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("groups = %s, want the two dependents kept apart",
			FormatGroupSummary(plan.Groups))
	}
}

// TestPlanSurfacesInsufficientEvidenceInsteadOfInventingACommit proves an
// opaque change is never turned into a one-file commit.
func TestPlanSurfacesInsufficientEvidenceInsteadOfInventingACommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/empty/empty.go", "")

	plan, err := (&Service{Root: repo}).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 0 {
		t.Fatalf("groups = %s, want no invented commit", FormatGroupSummary(plan.Groups))
	}
	if len(plan.Insufficient) != 1 || plan.Insufficient[0].Path != "internal/empty/empty.go" {
		t.Errorf("insufficient = %s, want the empty file surfaced explicitly",
			renderChanges(plan.Insufficient))
	}
	if len(plan.Unrelated) != 0 {
		t.Errorf("unrelated = %s, uncertainty must not become unrelated",
			renderChanges(plan.Unrelated))
	}
}

// TestPlanAccountsForEveryPath proves the coverage invariant across a
// committable group, an ambiguous blob, and an insufficient-evidence file.
func TestPlanAccountsForEveryPath(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	writeTestFile(t, repo, "asset.bin", "binary\x00payload")
	writeTestFile(t, repo, "internal/empty/empty.go", "")

	plan, err := (&Service{Root: repo}).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 1 || len(plan.Ambiguous) != 1 || len(plan.Insufficient) != 1 {
		t.Fatalf("groups/ambiguous/insufficient = %d/%d/%d, want 1/1/1",
			len(plan.Groups), len(plan.Ambiguous), len(plan.Insufficient))
	}
	if plan.Total != 3 {
		t.Errorf("total = %d, want 3 relevant paths", plan.Total)
	}
	if err := plan.validateAccounting(); err != nil {
		t.Errorf("accounting: %v", err)
	}
}

// TestPlanRefusesStagedChangesSpanningLogicalChanges proves the index is not
// silently absorbed by an automatic plan.
func TestPlanRefusesStagedChangesSpanningLogicalChanges(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	writeTestFile(t, repo, "internal/beta/b.go", "package beta\n\nfunc Beta() {}\n")
	stageTestFile(t, repo, "internal/alpha/a.go")
	stageTestFile(t, repo, "internal/beta/b.go")

	_, err := (&Service{Root: repo}).Plan(context.Background())
	if !errors.Is(err, ErrNeedsDecision) {
		t.Fatalf("Plan = %v, want ErrNeedsDecision for a multi-change index", err)
	}
	if status := runTestGit(t, repo, "status", "--porcelain"); !strings.Contains(status, "A  internal/alpha/a.go") {
		t.Errorf("status = %q, the plan mutated the index", status)
	}
}

// TestPlanRefusesStagedUncertainGroup proves an uncertain staged path still
// counts toward the index-wide decision, even when it is not a commit group.
func TestPlanRefusesStagedUncertainGroup(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	writeTestFile(t, repo, "internal/empty/empty.go", "")
	stageTestFile(t, repo, "internal/alpha/a.go")
	stageTestFile(t, repo, "internal/empty/empty.go")

	_, err := (&Service{Root: repo}).Plan(context.Background())
	if !errors.Is(err, ErrNeedsDecision) {
		t.Fatalf("Plan = %v, want ErrNeedsDecision for an uncertain staged path", err)
	}
}

// TestPlanRefusesPartiallyStagedPath proves a staged/worktree split is a human
// decision, not an automatic commit scope.
func TestPlanRefusesPartiallyStagedPath(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "internal/alpha/a.go", "package alpha\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	stageTestFile(t, repo, "internal/alpha/a.go")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc AlphaAgain() {}\n")

	_, err := (&Service{Root: repo}).Plan(context.Background())
	if !errors.Is(err, ErrNeedsDecision) {
		t.Fatalf("Plan = %v, want ErrNeedsDecision for a partially staged path", err)
	}
}

// TestPlanEmptyWorktreeIsClean proves /commit -a exits without a proposal when
// there is nothing to plan.
func TestPlanEmptyWorktreeIsClean(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")

	if _, err := (&Service{Root: repo}).Plan(context.Background()); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("Plan = %v, want ErrNoChanges", err)
	}
}

// TestProposeGroupUsesExactlyThePlannedScope proves each model invocation sees
// one logical change and its own evidence, never the whole repository diff.
func TestProposeGroupUsesExactlyThePlannedScope(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	initialCommit(t, repo, "internal/alpha/a.go", "package alpha\n")
	initialCommit(t, repo, "internal/beta/b.go", "package beta\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc AlphaSecret() {}\n")
	writeTestFile(t, repo, "internal/beta/b.go", "package beta\n\nfunc BetaSecret() {}\n")

	service := testService(repo, "feat(alpha): update alpha")
	plan, err := service.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("groups = %s, want two", FormatGroupSummary(plan.Groups))
	}
	group := plan.Groups[0]
	request, err := service.ProposeGroup(context.Background(), plan, group.Key)
	if err != nil {
		t.Fatalf("ProposeGroup: %v", err)
	}
	if !sameStringSet(request.Snapshot.StagePaths(), groupPaths(group)) {
		t.Errorf("scope = %s, want the planned group %s",
			renderChanges(request.Snapshot.Scope), renderChanges(group.Files))
	}
	if !request.Snapshot.Auto {
		t.Error("scoped proposal is not marked automatic")
	}
	if request.Progress == nil || request.Progress.Position != 1 || request.Progress.Total != 2 {
		t.Errorf("progress = %+v, want 1/2", request.Progress)
	}
	for _, other := range plan.Groups[1:] {
		for _, file := range other.Files {
			if containsPath(request.Snapshot.Scope, file.Path) {
				t.Errorf("scope contains another logical change: %s", file.Path)
			}
		}
	}
}

// TestProposeGroupRejectsAStalePlan proves a plan is never executed against a
// repository that moved after planning.
func TestProposeGroupRejectsAStalePlan(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")

	service := testService(repo, "feat(alpha): update alpha")
	plan, err := service.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc AlphaChanged() {}\n")
	if _, err := service.ProposeGroup(context.Background(), plan, plan.Groups[0].Key); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("ProposeGroup = %v, want ErrPlanStale", err)
	}
}

// TestPlannedCommitStagesOnlyItsGroup proves /commit -a never degrades to
// `git add -A`: the other logical change stays untouched.
func TestPlannedCommitStagesOnlyItsGroup(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	writeTestFile(t, repo, "internal/beta/b.go", "package beta\n\nfunc Beta() {}\n")

	service := testService(repo, "feat(scope): update files")
	plan, err := service.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	group := plan.Groups[0]
	request, err := service.ProposeGroup(context.Background(), plan, group.Key)
	if err != nil {
		t.Fatalf("ProposeGroup: %v", err)
	}
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	committed := strings.TrimSpace(runTestGit(t, repo, "show", "--pretty=format:", "--name-only", "HEAD"))
	if committed != group.Files[0].Path {
		t.Errorf("committed files = %q, want only %q", committed, group.Files[0].Path)
	}
	status := runTestGit(t, repo, "status", "--porcelain")
	for _, other := range plan.Groups[1:] {
		for _, file := range other.Files {
			dir := file.Path
			if i := strings.LastIndexByte(dir, '/'); i >= 0 {
				dir = dir[:i+1]
			}
			if !strings.Contains(status, file.Path) && !strings.Contains(status, dir) {
				t.Errorf("status = %q, want the uncommitted %s", status, file.Path)
			}
		}
	}
}

// TestAutomaticPlanWalksGroupsWithSeparateProposals proves the service half of
// the /commit -a sequence: one proposal and one commit per logical group, with
// a fresh plan after each commit.
func TestAutomaticPlanWalksGroupsWithSeparateProposals(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "internal/alpha/a.go", "package alpha\n\nfunc Alpha() {}\n")
	writeTestFile(t, repo, "internal/beta/b.go", "package beta\n\nfunc Beta() {}\n")
	writeTestFile(t, repo, "internal/gamma/c.go", "package gamma\n\nfunc Gamma() {}\n")

	service := testService(repo, "feat(scope): update files")
	created := 0
	for {
		plan, err := service.Plan(context.Background())
		if errors.Is(err, ErrNoChanges) {
			break
		}
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		// Each pass proposes exactly one group; the remaining groups stay in
		// the plan until their own approval.
		group := plan.Groups[0]
		request, err := service.ProposeGroup(context.Background(), plan, group.Key)
		if err != nil {
			t.Fatalf("ProposeGroup: %v", err)
		}
		result, err := service.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.SHA == "" {
			t.Fatal("Execute returned no commit")
		}
		created++
		if created > 5 {
			t.Fatal("automatic plan did not terminate")
		}
	}
	if created != 3 {
		t.Errorf("created = %d commits, want one per logical change", created)
	}
	if got := commitCount(t, repo); got != 4 {
		t.Errorf("commits = %d, want the initial commit plus three logical commits", got)
	}
	if status := strings.TrimSpace(runTestGit(t, repo, "status", "--porcelain")); status != "" {
		t.Errorf("status = %q, want a clean tree", status)
	}
}
