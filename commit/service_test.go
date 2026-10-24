package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/vcs"
)

func TestSnapshotUsesTheIndexWhenAnythingIsStaged(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "b.txt", "b\n")
	stageTestFile(t, repo, "b.txt")
	runTestGit(t, repo, "commit", "-qm", "add b")

	writeTestFile(t, repo, "a.txt", "changed\n")
	writeTestFile(t, repo, "b.txt", "changed too\n")
	stageTestFile(t, repo, "a.txt")

	snapshot := snapshotOf(t, repo)
	if snapshot.Mode != ModeStaged {
		t.Fatalf("mode = %q, want staged", snapshot.Mode)
	}
	if len(snapshot.Scope) != 1 || snapshot.Scope[0].Path != "a.txt" {
		t.Fatalf("scope = %s, want only the staged a.txt", renderChanges(snapshot.Scope))
	}
	if !containsPath(snapshot.Unstaged, "b.txt") {
		t.Errorf("unstaged changes = %s, want b.txt", renderChanges(snapshot.Unstaged))
	}
}

func TestSnapshotUsesTheWorktreeWhenTheIndexIsClean(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	writeTestFile(t, repo, "b.txt", "new\n")

	snapshot := snapshotOf(t, repo)
	if snapshot.Mode != ModeWorktree {
		t.Fatalf("mode = %q, want worktree", snapshot.Mode)
	}
	if !containsPath(snapshot.Scope, "a.txt") || !containsPath(snapshot.Scope, "b.txt") {
		t.Fatalf("scope = %s, want the tracked edit and the untracked file", renderChanges(snapshot.Scope))
	}
}

func TestSnapshotMixedChangeKeepsUnstagedFilesOutOfScope(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	initialCommit(t, repo, "b.txt", "two\n")
	writeTestFile(t, repo, "a.txt", "staged a\n")
	writeTestFile(t, repo, "b.txt", "unstaged b\n")
	stageTestFile(t, repo, "a.txt")

	snapshot := snapshotOf(t, repo)
	if snapshot.Mode != ModeStaged {
		t.Fatalf("mode = %q, want staged", snapshot.Mode)
	}
	if len(snapshot.Scope) != 1 || snapshot.Scope[0].Path != "a.txt" {
		t.Fatalf("scope = %s, want only a.txt", renderChanges(snapshot.Scope))
	}
}

func TestSnapshotExcludesIgnoredFiles(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, ".gitignore", "*.log\n")
	writeTestFile(t, repo, "debug.log", "noise\n")

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("ignored-only repository: %v, want ErrNoChanges", err)
	}

	writeTestFile(t, repo, "a.txt", "real\n")
	snapshot := snapshotOf(t, repo)
	if containsPath(snapshot.Scope, "debug.log") {
		t.Errorf("ignored file entered the scope: %s", renderChanges(snapshot.Scope))
	}
	if !containsPath(snapshot.Ignored, "debug.log") {
		t.Errorf("ignored file not reported: %s", renderChanges(snapshot.Ignored))
	}
}

func TestSnapshotExcludesOhgoInternalState(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, ".ohgo/project.yml", "id: fixture\n")

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrNoChanges) {
		t.Fatalf(".ohgo-only repository: %v, want ErrNoChanges", err)
	}

	writeTestFile(t, repo, "a.txt", "two\n")
	snapshot := snapshotOf(t, repo)
	if containsPath(snapshot.Scope, ".ohgo/project.yml") {
		t.Errorf(".ohgo entered the scope: %s", renderChanges(snapshot.Scope))
	}
	if !containsPath(snapshot.Internal, ".ohgo/project.yml") {
		t.Errorf(".ohgo not reported as internal: %s", renderChanges(snapshot.Internal))
	}
}

func TestSnapshotRefusesPreStagedInternalState(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, ".ohgo/project.yml", "id: fixture\n")
	runTestGit(t, repo, "add", "-f", "--", ".ohgo/project.yml")

	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrStagedExcluded) {
		t.Fatalf("staged .ohgo: %v, want ErrStagedExcluded", err)
	}
}

func TestNoChangesInAnEmptyRepository(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := BuildSnapshot(context.Background(), discoverRepo(t, repo)); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("empty repository: %v, want ErrNoChanges", err)
	}
}

func TestProposeDoesNotMutateTheIndexOrHead(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	headBefore := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))

	service := testService(repo, "fix(scope): approved\n\n- change a file")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if request == nil {
		t.Fatal("Propose returned no request")
	}
	if staged := strings.TrimSpace(runTestGit(t, repo, "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("Propose staged files: %q", staged)
	}
	if head := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD")); head != headBefore {
		t.Errorf("HEAD moved during Propose: %s -> %s", headBefore, head)
	}
}

func TestNoChangesDoesNotInvokeTheModel(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")

	var calls atomic.Int32
	service := &Service{
		Root: repo,
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			calls.Add(1)
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		},
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
	}
	if _, err := service.Propose(context.Background()); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("Propose: %v, want ErrNoChanges", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("model called %d times for a clean tree", got)
	}
}

func TestExecuteCreatesExactlyOneCommitWithTheApprovedMessage(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	writeTestFile(t, repo, "new.txt", "new\n")
	writeTestFile(t, repo, ".ohgo/project.yml", "id: fixture\n")

	service := testService(repo, "feat(scope): add the change\n\n- update a\n- add new")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	oldHead := request.Snapshot.Head
	before := commitCount(t, repo)

	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.SHA == "" {
		t.Fatal("Execute returned no commit SHA")
	}
	if got := commitCount(t, repo); got != before+1 {
		t.Fatalf("commits = %d, want exactly one new commit", got)
	}
	details, err := discoverRepo(t, repo).CommitDetails(context.Background(), result.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if details.Message != request.Proposal.Message() {
		t.Errorf("committed message = %q, want %q", details.Message, request.Proposal.Message())
	}
	if len(details.Parents) != 1 || details.Parents[0] != oldHead {
		t.Errorf("parents = %v, want exactly the previous HEAD %s", details.Parents, oldHead)
	}
	headTree := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD^{tree}"))
	if result.Tree == "" || headTree != result.Tree {
		t.Errorf("tree = %q, HEAD tree = %q; the commit is not the verified index", result.Tree, headTree)
	}
	tree := runTestGit(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, ".ohgo/") {
		t.Errorf("commit contains .ohgo state:\n%s", tree)
	}
	if result.Clean {
		t.Error("result claimed a clean tree while untracked .ohgo state remains")
	}
	if !containsPath(result.Remaining, ".ohgo/project.yml") {
		t.Errorf("remaining = %s, want the excluded .ohgo path stated explicitly", renderChanges(result.Remaining))
	}
}

func TestResultClaimsCleanOnlyWhenStatusIsClean(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	service := testService(repo, "fix(scope): clean result")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Clean || len(result.Remaining) != 0 {
		t.Errorf("result = clean %v remaining %s, want a proven clean tree",
			result.Clean, renderChanges(result.Remaining))
	}
}

func TestSecondExecuteCannotCreateAnotherCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	service := testService(repo, "fix(scope): one commit")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	before := commitCount(t, repo)
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := service.Execute(context.Background(), request); err == nil {
		t.Fatal("second Execute on the same approval succeeded")
	}
	if got := commitCount(t, repo); got != before+1 {
		t.Fatalf("commits = %d, want exactly one new commit", got)
	}
}

func TestWorkspaceChangeAfterProposalAborts(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	service := testService(repo, "fix(scope): abort on change")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	before := commitCount(t, repo)
	writeTestFile(t, repo, "a.txt", "three, after approval\n")

	if _, err := service.Execute(context.Background(), request); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("Execute: %v, want ErrStateChanged", err)
	}
	if got := commitCount(t, repo); got != before {
		t.Fatalf("commits = %d, want none after an aborted commit", got)
	}
	if staged := strings.TrimSpace(runTestGit(t, repo, "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("aborted commit left staged paths: %q", staged)
	}
}

func TestStagingMismatchAbortsBeforeCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	initialCommit(t, repo, "b.txt", "two\n")
	writeTestFile(t, repo, "a.txt", "staged a\n")
	writeTestFile(t, repo, "b.txt", "unstaged b\n")
	stageTestFile(t, repo, "a.txt") // staged scope is a.txt only

	service := testService(repo, "fix(scope): staging check")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if request.Snapshot.Mode != ModeStaged {
		t.Fatalf("mode = %q, want staged", request.Snapshot.Mode)
	}
	before := commitCount(t, repo)
	// Stage a path outside the approved scope after approval. The verifier
	// must see the index no longer matches and abort.
	service.OnStaged = func() {
		runTestGit(t, repo, "add", "--", "b.txt")
	}

	if _, err := service.Execute(context.Background(), request); !errors.Is(err, ErrStagingMismatch) {
		t.Fatalf("Execute: %v, want ErrStagingMismatch", err)
	}
	if got := commitCount(t, repo); got != before {
		t.Fatalf("commits = %d, want none after a staging mismatch", got)
	}
}

func TestFailedCommitDoesNotTriggerAnotherCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	service := testService(repo, "fix(scope): fail once")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	before := commitCount(t, repo)
	if _, err := service.Execute(context.Background(), request); err == nil {
		t.Fatal("Execute succeeded despite a rejecting pre-commit hook")
	}
	if got := commitCount(t, repo); got != before {
		t.Fatalf("commits = %d, want no corrective commit after a failure", got)
	}
}

func TestInvalidOutputIsRetriedThenStops(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var calls atomic.Int32
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			calls.Add(1)
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "not a commit message"}},
			}), nil
		},
	}
	if _, err := service.Propose(context.Background()); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("Propose: %v, want ErrInvalidProposal", err)
	}
	if got := calls.Load(); got != defaultAttempts {
		t.Errorf("attempts = %d, want the bounded %d", got, defaultAttempts)
	}
	if staged := strings.TrimSpace(runTestGit(t, repo, "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("invalid proposal staged files: %q", staged)
	}
}

func TestInvalidOutputIsRegeneratedWhenValidReplyArrives(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var calls atomic.Int32
	service := &Service{
		Root:      repo,
		Model:     ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		MaxTokens: 128,
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			if calls.Add(1) == 1 {
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: "bad"}},
				}), nil
			}
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "fix(scope): regenerated"}},
			}), nil
		},
	}
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if request.Proposal.Subject() != "fix(scope): regenerated" {
		t.Errorf("subject = %q", request.Proposal.Subject())
	}
}

// TestUnrulyModelTextIsRepairedInsteadOfFailing pins the reported failure: a
// small model omitted the scope, exceeded the subject budget, and wrote a long
// bullet on every attempt. The deterministic repair must preserve the change
// summary and body while producing a proposal that satisfies every rule.
func TestUnrulyModelTextIsRepairedInsteadOfFailing(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "commit/change.go", "package commit\n")

	text := "docs: update the readme with a much longer introduction\n\n- " + strings.Repeat("a", 60)
	service := testService(repo, text)
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got, want := request.Proposal.Subject(), "docs(commit): update the readme"; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
	if len(request.Proposal.Body) != 1 {
		t.Fatalf("body = %#v, want one bullet", request.Proposal.Body)
	}
	if got := len([]rune(request.Proposal.Body[0])); got > MaxBulletRunes {
		t.Errorf("bullet is %d runes, want at most %d", got, MaxBulletRunes)
	}
}

// TestMalformedModelScopeIsReplaced pins the reported `.ohgo` scope failure:
// the model invented a scope the validator cannot accept, and the deterministic
// repair substitutes the scope derived from the approved path.
func TestMalformedModelScopeIsReplaced(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "base.txt", "base\n")
	writeTestFile(t, repo, "commit/hana.txt", "hello\n")

	service := testService(repo, "feat(.ohgo): add hello file\n\n- add hana.txt")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got, want := request.Proposal.Subject(), "feat(commit): add hello file"; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
}

func TestCommitModelIsGivenNoTools(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var captured ai.Request
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			captured = req
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "fix(scope): no tools"}},
			}), nil
		},
	}
	if _, err := service.Propose(context.Background()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(captured.Tools) != 0 {
		t.Errorf("commit model received %d tools", len(captured.Tools))
	}
	if captured.ToolChoice.Type != "" {
		t.Errorf("commit model received tool choice %q", captured.ToolChoice.Type)
	}
	if captured.Model.ID != "commit-model" {
		t.Errorf("model = %q", captured.Model.ID)
	}
	if !strings.Contains(captured.Messages[0].(ai.UserMessage).Text(), "approved scope:") {
		t.Error("request did not carry the bounded repository evidence")
	}
}

// TestCommitRegressionCreatesOneCommitAndNeverAnOhgoCommit pins the observed
// failure class: a generic "git commit help me" request once produced several
// commits, including a separate .ohgo commit. The specialized executor has one
// commit call, excludes .ohgo deterministically, and refuses a second
// execution of the same approval.
func TestCommitRegressionCreatesOneCommitAndNeverAnOhgoCommit(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	writeTestFile(t, repo, ".ohgo/project.yml", "id: fixture\n")

	service := testService(repo, "fix(commit): one explicit commit\n\n- update one file")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	oldHead := request.Snapshot.Head
	before := commitCount(t, repo)

	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := commitCount(t, repo); got != before+1 {
		t.Fatalf("commits = %d, want exactly one new commit", got)
	}
	head := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	if head != result.SHA {
		t.Errorf("HEAD = %s, result SHA = %s; an amend or extra commit intervened", head, result.SHA)
	}
	details, err := discoverRepo(t, repo).CommitDetails(context.Background(), result.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Parents) != 1 || details.Parents[0] != oldHead {
		t.Errorf("parents = %v, want exactly the previous HEAD", details.Parents)
	}
	tree := runTestGit(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, ".ohgo/") {
		t.Errorf("the commit contains .ohgo state:\n%s", tree)
	}
	// A second execution of the same approval is refused and creates no
	// corrective commit.
	if _, err := service.Execute(context.Background(), request); err == nil {
		t.Fatal("the same approval executed twice")
	}
	if got := commitCount(t, repo); got != before+1 {
		t.Fatalf("commits = %d, want no corrective commit", got)
	}
}

// --- helpers ----------------------------------------------------------------

func discoverRepo(t *testing.T, root string) *vcs.RepoInfo {
	t.Helper()
	info, err := vcs.Discover(root)
	if err != nil || info == nil {
		t.Fatalf("Discover(%s): %v (%v)", root, err, info)
	}
	return info
}

func snapshotOf(t *testing.T, root string) *Snapshot {
	t.Helper()
	snapshot, err := BuildSnapshot(context.Background(), discoverRepo(t, root))
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return snapshot
}

func containsPath(changes []FileChange, path string) bool {
	for _, change := range changes {
		if change.Path == path {
			return true
		}
	}
	return false
}
