package commit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/vcs"
)

func TestProposalSubjectRequiresEveryField(t *testing.T) {
	for _, proposal := range []Proposal{
		{Scope: "s", Summary: "x"},
		{Type: "fix", Summary: "x"},
		{Type: "fix", Scope: "s"},
	} {
		if got := proposal.Subject(); got != "" {
			t.Errorf("Subject(%+v) = %q, want empty for an incomplete proposal", proposal, got)
		}
		if got := proposal.Message(); got != "" {
			t.Errorf("Message(%+v) = %q, want empty for an incomplete proposal", proposal, got)
		}
	}
}

func TestParseRejectsEmptyAndOversizedReplies(t *testing.T) {
	if _, err := Parse(" \n\t ", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("empty reply: %v, want ErrInvalidProposal", err)
	}
	var lines []string
	for i := 0; i <= MaxBullets; i++ {
		lines = append(lines, "- bullet")
	}
	text := "fix(scope): subject\n\n" + strings.Join(lines, "\n")
	if _, err := Parse(text, nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("oversized body: %v, want ErrInvalidProposal", err)
	}
}

func TestValidateRejectsInconsistentProposals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		proposal Proposal
	}{
		{"empty", Proposal{}},
		{"scope with space", Proposal{Type: "fix", Scope: "bad scope", Summary: "x"}},
		{"padded summary", Proposal{Type: "fix", Scope: "s", Summary: " padded "}},
		{"control in subject", Proposal{Type: "fix", Scope: "s", Summary: "a\tb"}},
		{"body without summary", Proposal{Type: "fix", Scope: "s", Summary: "x", Body: []string{"-  "}}},
		{"control in body", Proposal{Type: "fix", Scope: "s", Summary: "x", Body: []string{"- a\tb"}}},
		{"too many bullets", Proposal{Type: "fix", Scope: "s", Summary: "x", Body: []string{
			"- a", "- b", "- c", "- d", "- e", "- f",
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.proposal.Validate(nil); !errors.Is(err, ErrInvalidProposal) {
				t.Fatalf("Validate = %v, want ErrInvalidProposal", err)
			}
		})
	}
}

func TestParseRejectsAnUnterminatedFenceAndInvalidUTF8(t *testing.T) {
	if _, err := Parse("```\nfix(scope): fenced", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("unterminated fence: %v, want ErrInvalidProposal", err)
	}
	if _, err := Parse("fix(scope): \xff", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("invalid UTF-8: %v, want ErrInvalidProposal", err)
	}
}

func TestValidationTextReturnsNilAndForeignErrorsAsIs(t *testing.T) {
	if got := ValidationText(nil); got != "" {
		t.Errorf("ValidationText(nil) = %q, want empty", got)
	}
	if got := ValidationText(errors.New("boom")); got != "boom" {
		t.Errorf("ValidationText = %q, want the original error text", got)
	}
	if got := ValidationText(fmt.Errorf("%w: rule", ErrInvalidProposal)); got != "rule" {
		t.Errorf("ValidationText = %q, want the rule without the sentinel", got)
	}
}

func TestRepairRejectsTextItCannotDeriveAScopeFor(t *testing.T) {
	if _, ok := repairProposal("", nil, "scope"); ok {
		t.Error("repair accepted empty model output")
	}
	if _, ok := repairProposal("fix: add a thing", nil, ""); ok {
		t.Error("repair invented a scope when no usable fallback existed")
	}
	longScope := strings.Repeat("a", 40)
	if _, ok := repairProposal("fix(): add a thing", nil, longScope); ok {
		t.Error("repair accepted a fallback that leaves no room for a summary")
	}
}

func TestFitSummaryKeepsTextInsideTheBudget(t *testing.T) {
	if got := fitSummary("short", 10); got != "short" {
		t.Errorf("fitSummary = %q, want the text unchanged", got)
	}
}

func TestFitBulletsLeavesAnUnfittableLongLineAlone(t *testing.T) {
	line := "- " + strings.Repeat(" ", 60)
	if got := fitBullets(line); got != line {
		t.Errorf("fitBullets = %q, want the unfixable body preserved for Parse to reject", got)
	}
}

// --- service contracts -------------------------------------------------------

func TestProposalErrorHandlesNilAndMissingCause(t *testing.T) {
	var nilErr *ProposalError
	if got := nilErr.Error(); got != "" {
		t.Errorf("nil Error = %q, want empty", got)
	}
	if got := nilErr.Unwrap(); got != nil {
		t.Errorf("nil Unwrap = %v, want nil", got)
	}
	plain := &ProposalError{Message: "boom"}
	if got := plain.Error(); got != "boom" {
		t.Errorf("Error = %q, want the message", got)
	}
	if _, ok := DiagnosticsOf(errors.New("plain")); ok {
		t.Error("DiagnosticsOf claimed an unclassified error carried diagnostics")
	}
}

func TestProposeRejectsAMissingRepositoryOrModel(t *testing.T) {
	notRepo := t.TempDir()
	service := &Service{Root: notRepo}
	if _, err := service.Propose(context.Background()); err == nil || !strings.Contains(err.Error(), "not inside a Git repository") {
		t.Fatalf("Propose outside a repo = %v, want the repository refusal", err)
	}

	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	service = &Service{Root: repo, Model: ai.Model{Provider: "test", ID: "commit-model"}}
	if _, err := service.Propose(context.Background()); err == nil || !strings.Contains(err.Error(), "transport is unavailable") {
		t.Fatalf("Propose without a stream = %v, want the transport refusal", err)
	}
	service.Stream = staticStream("fix(scope): x")
	service.Model = ai.Model{}
	if _, err := service.Propose(context.Background()); err == nil || !strings.Contains(err.Error(), "model is not configured") {
		t.Fatalf("Propose without a model = %v, want the configuration refusal", err)
	}
}

func TestModelErrorClassifiesEveryCancellationLayer(t *testing.T) {
	userCtx, cancelUser := context.WithCancelCause(context.Background())
	cancelUser(ErrCancelledByUser)
	commitDeadlineCtx, cancelCommitDeadline := context.WithCancelCause(context.Background())
	cancelCommitDeadline(ErrProposalTimeout)
	sessionCtx, cancelSession := context.WithCancelCause(context.Background())
	cancelSession(ErrSessionClosed)
	supersededCtx, cancelSuperseded := context.WithCancelCause(context.Background())
	cancelSuperseded(ErrSuperseded)
	deadlineCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		kind   error
		reason string
	}{
		{"user", userCtx, errors.New("boom"), ErrModelCancelled, "user"},
		{"session", sessionCtx, errors.New("boom"), ErrModelCancelled, "session"},
		{"superseded", supersededCtx, errors.New("boom"), ErrModelCancelled, "superseded"},
		{"commit deadline", commitDeadlineCtx, errors.New("boom"), ErrModelTimeout, "timeout"},
		{"context deadline", deadlineCtx, errors.New("boom"), ErrModelTimeout, "context-deadline"},
		{"context cancelled", cancelledCtx, errors.New("boom"), ErrModelCancelled, "cancelled"},
		{"transport cancelled", context.Background(), context.Canceled, ErrModelCancelled, "cancelled"},
		{"transport deadline", context.Background(), context.DeadlineExceeded, ErrModelTimeout, "provider-timeout"},
		{"provider timeout", context.Background(), timeoutError{message: "stalled"}, ErrModelProviderTimeout, "provider-timeout"},
		{"provider failure", context.Background(), errors.New("boom"), ErrModel, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diag ProposalDiagnostics
			err := modelError(tc.ctx, tc.err, &diag)
			if !errors.Is(err, tc.kind) {
				t.Fatalf("modelError = %v, want %v", err, tc.kind)
			}
			if diag.CancellationReason != tc.reason {
				t.Errorf("reason = %q, want %q", diag.CancellationReason, tc.reason)
			}
		})
	}
}

func TestModelErrorKeepsTheCommitDeadlineDistinct(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrProposalTimeout)
	var diag ProposalDiagnostics
	err := modelError(ctx, errors.New("boom"), &diag)
	if !errors.Is(err, ErrModelTimeout) || diag.CancellationReason != "timeout" {
		t.Errorf("modelError = %v reason %q, want the COMMIT timeout", err, diag.CancellationReason)
	}
}

type statusError struct{ status int }

func (e statusError) Error() string   { return "provider rejected the request" }
func (e statusError) StatusCode() int { return e.status }

func TestModelErrorRecordsTheProviderStatus(t *testing.T) {
	var diag ProposalDiagnostics
	_ = modelError(context.Background(), statusError{status: 429}, &diag)
	if diag.ProviderStatus != 429 {
		t.Errorf("provider status = %d, want 429", diag.ProviderStatus)
	}
}

func TestInvalidCauseHandlesMissingErrors(t *testing.T) {
	if got := invalidCause(nil); got != "the proposal was empty" {
		t.Errorf("invalidCause(nil) = %q", got)
	}
}

func TestExecuteRejectsAMissingRequestAndRepository(t *testing.T) {
	service := &Service{Root: t.TempDir()}
	if _, err := service.Execute(context.Background(), nil); err == nil {
		t.Fatal("Execute accepted a nil request")
	}
	request := &Request{
		Proposal: Proposal{Type: "fix", Scope: "s", Summary: "x"},
		Snapshot: &Snapshot{Root: t.TempDir()},
	}
	if _, err := service.Execute(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "not inside a Git repository") {
		t.Fatalf("Execute outside a repo = %v, want the repository refusal", err)
	}
}

func TestExecuteFallsBackToTheServiceRoot(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	service := testService(repo, "fix(scope): use the service root")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	request.Snapshot.Root = ""
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute with an empty snapshot root: %v", err)
	}
	if got := commitCount(t, repo); got != 2 {
		t.Errorf("commits = %d, want the initial commit plus one", got)
	}
}

func TestExecutionStateErrorMapsAVanishedScope(t *testing.T) {
	if got := executionStateError(ErrNoChanges); !errors.Is(got, ErrStateChanged) {
		t.Errorf("executionStateError(ErrNoChanges) = %v, want ErrStateChanged", got)
	}
	other := errors.New("boom")
	if got := executionStateError(other); got != other {
		t.Errorf("executionStateError(other) = %v, want the original error", got)
	}
}

func TestVerifyStagedRejectsAHashMismatch(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	stageTestFile(t, repo, "a.txt")

	snapshot := &Snapshot{
		Scope:    []FileChange{{Path: "a.txt", Index: 'M'}},
		Expected: map[string]string{"a.txt": "deadbeef"},
	}
	err := verifyStaged(context.Background(), discoverRepo(t, repo), snapshot)
	if !errors.Is(err, ErrStagingMismatch) {
		t.Fatalf("verifyStaged = %v, want ErrStagingMismatch", err)
	}
}

func TestVerifyCommitRejectsEveryMismatchedFact(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	first := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	writeTestFile(t, repo, "a.txt", "two\n")
	stageTestFile(t, repo, "a.txt")
	const message = "fix(scope): second\n\n- update a"
	runTestGit(t, repo, "commit", "-qm", message)
	second := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD^{tree}"))

	proposal, err := Parse(message, nil)
	if err != nil {
		t.Fatalf("Parse fixture: %v", err)
	}
	repoInfo := discoverRepo(t, repo)
	valid := &Request{
		Proposal: proposal,
		Snapshot: &Snapshot{Head: first},
	}
	if err := verifyCommit(context.Background(), repoInfo, valid, second, tree); err != nil {
		t.Fatalf("verifyCommit rejected the exact commit: %v", err)
	}

	for _, tc := range []struct {
		name    string
		request *Request
		sha     string
		tree    string
	}{
		{"head moved", valid, first, tree},
		{"tree differs", valid, second, "deadbeef"},
		{"message differs", &Request{
			Proposal: Proposal{Type: "fix", Scope: "scope", Summary: "other"},
			Snapshot: valid.Snapshot,
		}, second, tree},
		{"parent differs", &Request{
			Proposal: proposal,
			Snapshot: &Snapshot{Head: ""},
		}, second, tree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyCommit(context.Background(), repoInfo, tc.request, tc.sha, tc.tree)
			if !errors.Is(err, ErrCommitVerification) {
				t.Fatalf("verifyCommit = %v, want ErrCommitVerification", err)
			}
		})
	}
}

func TestSameStringsAndUniqueSortedIgnoreOrderAndDuplicates(t *testing.T) {
	if sameStrings([]string{"a"}, []string{"a", "b"}) {
		t.Error("sameStrings accepted different set sizes")
	}
	if !sameStrings([]string{"b", "a", "a"}, []string{"a", "b"}) {
		t.Error("sameStrings rejected an equivalent set")
	}
	if got := uniqueSorted([]string{"b", "a", "b"}); strings.Join(got, ",") != "a,b" {
		t.Errorf("uniqueSorted = %v, want a,b", got)
	}
}

func TestDisplayListBoundsLongScopes(t *testing.T) {
	if got := displayList(nil); got != "(none)" {
		t.Errorf("displayList(nil) = %q", got)
	}
	values := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	got := displayList(values)
	if !strings.Contains(got, "1, 2, 3, 4, 5, 6, 7, 8") || !strings.Contains(got, "(1 more)") {
		t.Errorf("displayList = %q, want the bounded first eight and a remainder", got)
	}
}

func TestAbortTextHandlesEveryTerminalClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"invalid without rule", ErrInvalidProposal,
			"Commit proposal failed.\nThe COMMIT model did not produce a valid proposal."},
		{"staged excluded", fmt.Errorf("%w: .ohgo", ErrStagedExcluded), "commit: staged changes include excluded paths: .ohgo"},
		{"too many", fmt.Errorf("%w: 2001", ErrTooManyChanges), "commit: too many changed paths: 2001"},
		{"unclassified", errors.New("plain failure"), "plain failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AbortText(tc.err); got != tc.want {
				t.Errorf("AbortText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatElapsedDescribesSubSecondWaits(t *testing.T) {
	if got := formatElapsed(500 * time.Millisecond); got != "less than 1s" {
		t.Errorf("formatElapsed = %q", got)
	}
	if got := formatElapsed(90 * time.Second); got != "1m30s" {
		t.Errorf("formatElapsed = %q", got)
	}
}

func TestFormatResultStatesTheTruthAboutRemainingChanges(t *testing.T) {
	clean := FormatResult(Result{
		SHA: "abcdef123456", Subject: "fix(scope): x", Files: []FileChange{{Path: "a.go"}}, Clean: true,
	})
	if !strings.HasPrefix(clean, "Committed abcdef1 fix(scope): x\n1 file changed") ||
		!strings.HasSuffix(clean, "\nWorking tree is clean.") {
		t.Errorf("clean result = %q", clean)
	}
	dirty := FormatResult(Result{
		SHA: "abcdef123456", Subject: "fix(scope): x",
		Files:     []FileChange{{Path: "a.go"}, {Path: "b.go"}},
		Remaining: []FileChange{{Path: "notes.txt", Untracked: true, Index: '?', Worktree: '?'}},
	})
	for _, want := range []string{"2 files changed", "Changes remain outside this commit:", "?? notes.txt"} {
		if !strings.Contains(dirty, want) {
			t.Errorf("dirty result = %q, want %q", dirty, want)
		}
	}
	if strings.Contains(dirty, "Working tree is clean.") {
		t.Errorf("dirty result = %q, claimed a clean tree", dirty)
	}
}

func TestScopeNoteNamesExactlyWhatIsExcluded(t *testing.T) {
	if got := ScopeNote(nil); got != "" {
		t.Errorf("ScopeNote(nil) = %q, want empty", got)
	}
	staged := ScopeNote(&Snapshot{
		Mode:     ModeStaged,
		Unstaged: []FileChange{{Path: "a.go"}},
		Internal: []FileChange{{Path: ".ohgo/project.yml"}},
		Ignored:  []FileChange{{Path: "debug.log"}},
	})
	for _, want := range []string{
		"Only the staged changes will be committed.",
		"Unstaged and untracked changes are not included.",
		".ohgo/ internal state is excluded.",
		"Ignored files are excluded.",
	} {
		if !strings.Contains(staged, want) {
			t.Errorf("staged note = %q, want %q", staged, want)
		}
	}
	worktree := ScopeNote(&Snapshot{Mode: ModeWorktree})
	if worktree != "Tracked and untracked changes will be staged explicitly." {
		t.Errorf("worktree note = %q", worktree)
	}
}

func TestPluralSelectsTheSingularOnlyForOne(t *testing.T) {
	if got := plural("file", 1); got != "file" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural("file", 0); got != "files" {
		t.Errorf("plural(0) = %q", got)
	}
	if got := plural("file", 2); got != "files" {
		t.Errorf("plural(2) = %q", got)
	}
}

func TestWorkspaceHelpers(t *testing.T) {
	if got := valueOr("", "fallback"); got != "fallback" {
		t.Errorf("valueOr empty = %q", got)
	}
	if got := valueOr("value", "fallback"); got != "value" {
		t.Errorf("valueOr value = %q", got)
	}
	// A cut inside a multibyte rune must back up to a valid boundary.
	got := truncateUTF8("éééé", 3)
	if !strings.HasPrefix(got, "é") || !strings.HasSuffix(got, "(truncated)") {
		t.Errorf("truncateUTF8 = %q, want a valid prefix and an explicit marker", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateUTF8 produced invalid UTF-8: %q", got)
	}
}

func TestFileChangeCodeAndDisplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change FileChange
		code   string
	}{
		{"untracked", FileChange{Index: '?', Worktree: '?', Untracked: true}, "??"},
		{"ignored", FileChange{Index: '!', Worktree: '!', Ignored: true}, "!!"},
		{"index", FileChange{Index: 'M', Worktree: ' '}, "M"},
		{"worktree", FileChange{Index: ' ', Worktree: 'M'}, "M"},
		{"clean", FileChange{Index: ' ', Worktree: ' '}, " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.change.Code(); got != tc.code {
				t.Errorf("Code = %q, want %q", got, tc.code)
			}
		})
	}
	rename := FileChange{Path: "b.go", OrigPath: "a.go", Index: 'R'}
	if got := rename.Display(); got != "R a.go → b.go" {
		t.Errorf("Display = %q, want the rename source", got)
	}
	if got := (FileChange{Path: "a.go", Index: ' ', Worktree: 'M'}).Display(); got != "M a.go" {
		t.Errorf("Display = %q", got)
	}
}

func TestDedupeChangesKeepsTheFirstRecord(t *testing.T) {
	got := dedupeChanges([]FileChange{
		{Path: "a.go", Index: 'M'},
		{Path: "a.go", Index: 'A'},
		{Path: "b.go"},
	})
	if len(got) != 2 || got[0].Index != 'M' || got[1].Path != "b.go" {
		t.Errorf("dedupeChanges = %+v, want the first a.go and b.go", got)
	}
}

func TestExpectedStagedPathsIncludesBothSidesOfARename(t *testing.T) {
	snapshot := &Snapshot{Scope: []FileChange{
		{Path: "b.go", OrigPath: "a.go", Index: 'R'},
		{Path: "c.go", Index: 'A'},
	}}
	got := strings.Join(snapshot.ExpectedStagedPaths(), ",")
	if got != "a.go,b.go,c.go" {
		t.Errorf("ExpectedStagedPaths = %q", got)
	}
}

func TestRenderChangesMarksEmptyListsAndBoundsLongOnes(t *testing.T) {
	if got := renderChanges(nil); got != "(none)" {
		t.Errorf("renderChanges(nil) = %q", got)
	}
	if got := renderChanges([]FileChange{{Path: "a.go", Index: ' ', Worktree: 'M'}}); got != "M a.go" {
		t.Errorf("renderChanges = %q", got)
	}
	if got := renderChangesBounded(nil, 3); got != "(none)" {
		t.Errorf("renderChangesBounded(nil) = %q", got)
	}
	changes := []FileChange{{Path: "a"}, {Path: "b"}, {Path: "c"}, {Path: "d"}}
	if got := renderChangesBounded(changes, 2); !strings.Contains(got, "… 2 more") {
		t.Errorf("renderChangesBounded = %q, want the overflow marker", got)
	}
}

func TestRenderNumstatMarksBinaryAndSkipsEmpty(t *testing.T) {
	if got := renderNumstat(nil); got != "" {
		t.Errorf("renderNumstat(nil) = %q", got)
	}
	added, removed := 2, 3
	got := renderNumstat([]vcs.NumstatEntry{
		{Path: "a.go", Added: &added, Removed: &removed},
		{Path: "bin"},
	})
	if !strings.Contains(got, "a.go +2 -3") || !strings.Contains(got, "bin (binary)") {
		t.Errorf("renderNumstat = %q", got)
	}
}

func TestUntrackedPreviewDescribesUnsafeAndUnreadablePaths(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "ok.txt", "hello\n")
	if err := os.Symlink("ok.txt", filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := untrackedPreview(root, []FileChange{
		{Path: "ok.txt"},
		{Path: "link"},
		{Path: "dir"},
		{Path: "missing.txt"},
	})
	for _, want := range []string{"ok.txt:\nhello", "link: (symlink)", "dir: (unreadable)", "missing.txt: (unreadable)"} {
		if !strings.Contains(got, want) {
			t.Errorf("untrackedPreview = %q, want %q", got, want)
		}
	}
}

func TestUntrackedPreviewBoundsLongFilesAndLines(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("a", 200)
	var content strings.Builder
	for i := 0; i < 25; i++ {
		content.WriteString(line)
		content.WriteString("\n")
	}
	writeTestFile(t, root, "long.txt", content.String())
	got := untrackedPreview(root, []FileChange{{Path: "long.txt"}})
	if !strings.Contains(got, "(truncated)") {
		t.Errorf("untrackedPreview = %q, want an explicit truncation marker", got)
	}
	if len(got) > maxUntrackedBytes+untrackedPreviewLine*2 {
		t.Errorf("untrackedPreview is %d bytes, want it bounded", len(got))
	}
}

// vcsNumstat is no longer needed; renderNumstat is exercised with the real
// vcs entry type above.

func TestUntrackedPreviewStopsAtTheByteBudget(t *testing.T) {
	root := t.TempDir()
	// Two large previews exceed the shared budget, so the outer loop must stop
	// before every file is rendered.
	content := strings.Repeat(strings.Repeat("x", 200)+"\n", 20)
	for i := 0; i < 8; i++ {
		writeTestFile(t, root, fmt.Sprintf("f%d.txt", i), content)
	}
	changes := make([]FileChange, 0, 8)
	for i := 0; i < 8; i++ {
		changes = append(changes, FileChange{Path: fmt.Sprintf("f%d.txt", i)})
	}
	got := untrackedPreview(root, changes)
	if !strings.Contains(got, "untracked preview truncated") {
		t.Errorf("untrackedPreview = %q, want the outer truncation marker", got)
	}
}
