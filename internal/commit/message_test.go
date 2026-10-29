package commit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func TestParseAcceptsTheContract(t *testing.T) {
	body := []string{"- capture workspace before mutation", "- add session-scoped undo and redo"}
	text := "feat(recovery): safe recovery\n\n" + strings.Join(body, "\n")
	proposal, err := Parse(text, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if proposal.Subject() != "feat(recovery): safe recovery" {
		t.Errorf("subject = %q", proposal.Subject())
	}
	if len(proposal.Body) != 2 || proposal.Body[0] != body[0] {
		t.Errorf("body = %#v", proposal.Body)
	}
	if proposal.Message() != text {
		t.Errorf("message = %q, want %q", proposal.Message(), text)
	}
}

func TestParseAcceptsEmptyBody(t *testing.T) {
	proposal, err := Parse("docs(runtime): clarify authority", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if proposal.Message() != "docs(runtime): clarify authority" {
		t.Errorf("message = %q", proposal.Message())
	}
}

func TestSubjectLimitIsExactlyThirtySixRunes(t *testing.T) {
	// "feat(commit): " is 14 runes, so 22 more reaches the limit exactly.
	atLimit := "feat(commit): " + strings.Repeat("a", 22)
	if got := len([]rune(atLimit)); got != MaxSubjectRunes {
		t.Fatalf("fixture is %d runes, want %d", got, MaxSubjectRunes)
	}
	if _, err := Parse(atLimit, nil); err != nil {
		t.Errorf("36-rune subject rejected: %v", err)
	}
	over := atLimit + "a"
	if _, err := Parse(over, nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("37-rune subject accepted or wrong error: %v", err)
	}
}

func TestSubjectLimitCountsRunesNotBytes(t *testing.T) {
	// 14 ASCII prefix runes plus 22 CJK runes is 36 runes but 80 bytes.
	atLimit := "feat(commit): " + strings.Repeat("界", 22)
	if len(atLimit) <= MaxSubjectRunes {
		t.Fatalf("fixture is only %d bytes; it must exceed the rune limit in bytes", len(atLimit))
	}
	if _, err := Parse(atLimit, nil); err != nil {
		t.Errorf("36-rune multibyte subject rejected: %v", err)
	}
	if _, err := Parse(atLimit+"界", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("37-rune multibyte subject accepted or wrong error: %v", err)
	}
}

func TestBulletLimitIsExactlyFiftyRunesIncludingPrefix(t *testing.T) {
	atLimit := "- " + strings.Repeat("a", 48)
	if got := len([]rune(atLimit)); got != MaxBulletRunes {
		t.Fatalf("fixture is %d runes, want %d", got, MaxBulletRunes)
	}
	text := "fix(scope): bound lines\n\n" + atLimit
	if _, err := Parse(text, nil); err != nil {
		t.Errorf("50-rune bullet rejected: %v", err)
	}
	if _, err := Parse(text+"a", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("51-rune bullet accepted or wrong error: %v", err)
	}

	multibyte := "- " + strings.Repeat("界", 48)
	if _, err := Parse("fix(scope): bound lines\n\n"+multibyte, nil); err != nil {
		t.Errorf("50-rune multibyte bullet rejected: %v", err)
	}
	if _, err := Parse("fix(scope): bound lines\n\n"+multibyte+"界", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("51-rune multibyte bullet accepted or wrong error: %v", err)
	}
}

func TestParseRejectsMalformedSubjects(t *testing.T) {
	for _, text := range []string{
		"feat: missing scope",
		"Feat(scope): uppercase type",
		"feat(scope):missing space",
		"feat(Scope): uppercase scope",
		"feat(): empty scope",
		"feat(scope): ",
		"hello world",
	} {
		if _, err := Parse(text, nil); !errors.Is(err, ErrInvalidProposal) {
			t.Errorf("%q accepted or wrong error: %v", text, err)
		}
	}
}

func TestParseRequiresOneBlankLineBeforeBody(t *testing.T) {
	for _, text := range []string{
		"fix(scope): x\n- no blank line",
		"fix(scope): x\n\n\n- too many blanks",
		"fix(scope): x\n\ndangling separator",
		"fix(scope): x\n\n- ok\n\n- blank inside",
	} {
		if _, err := Parse(text, nil); !errors.Is(err, ErrInvalidProposal) {
			t.Errorf("%q accepted or wrong error: %v", text, err)
		}
	}
}

func TestParseRejectsInvalidBullets(t *testing.T) {
	for _, line := range []string{"* star", "-missing space", "- ", "-  "} {
		text := "fix(scope): x\n\n" + line
		if _, err := Parse(text, nil); !errors.Is(err, ErrInvalidProposal) {
			t.Errorf("%q accepted or wrong error: %v", line, err)
		}
	}
}

func TestParseRejectsInventedReferences(t *testing.T) {
	if _, err := Parse("fix(scope): close #999", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("invented issue reference accepted or wrong error: %v", err)
	}
	if _, err := Parse("fix(scope): close #999", map[string]bool{"#999": true}); err != nil {
		t.Errorf("evidence-backed reference rejected: %v", err)
	}
	if _, err := Parse("fix(scope): see https://e.co", nil); !errors.Is(err, ErrInvalidProposal) {
		t.Errorf("invented URL accepted or wrong error: %v", err)
	}
	refs := map[string]bool{"https://e.co": true}
	if _, err := Parse("fix(scope): see https://e.co", refs); err != nil {
		t.Errorf("evidence-backed URL rejected: %v", err)
	}
}

func TestRepairFillsAMissingScopeFromTheApprovedPaths(t *testing.T) {
	text := "docs: explain commit flow\n\n- keep the reviewed body"
	proposal, ok := repairProposal(text, nil, "commit")
	if !ok {
		t.Fatal("a scope-less but otherwise valid message was not repaired")
	}
	if got, want := proposal.Subject(), "docs(commit): explain commit flow"; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
	if len(proposal.Body) != 1 || proposal.Body[0] != "- keep the reviewed body" {
		t.Errorf("body = %#v, want the reviewed body preserved", proposal.Body)
	}
}

func TestRepairReplacesAMalformedScope(t *testing.T) {
	for _, subject := range []string{
		"feat(.ohgo): add hello file",
		"feat(): add hello file",
		"feat(ohgo repo): add hello file",
		"feat(Ohgo): add hello file",
	} {
		proposal, ok := repairProposal(subject, nil, "hana")
		if !ok {
			t.Fatalf("%q was not repaired", subject)
		}
		if got, want := proposal.Subject(), "feat(hana): add hello file"; got != want {
			t.Errorf("%q repaired to %q, want the approved-path scope %q", subject, got, want)
		}
	}
	if _, ok := repairProposal("feat(.ohgo): add hello file", nil, "Bad Scope"); ok {
		t.Error("repair used an unusable fallback scope")
	}
}

func TestRepairFitsOverlongSubjectAndBullets(t *testing.T) {
	long := "docs(readme): update the readme with a much longer introduction"
	bullet := "- " + strings.Repeat("a", 60)
	proposal, ok := repairProposal(long+"\n\n"+bullet, nil, "readme")
	if !ok {
		t.Fatal("a structurally valid but overlong message was not repaired")
	}
	if got, want := proposal.Subject(), "docs(readme): update the readme"; got != want {
		t.Errorf("subject = %q, want the phrase fitted at a word boundary %q", got, want)
	}
	if len(proposal.Body) != 1 {
		t.Fatalf("body = %#v, want one bullet", proposal.Body)
	}
	if got := len([]rune(proposal.Body[0])); got > MaxBulletRunes {
		t.Errorf("bullet is %d runes, want at most %d", got, MaxBulletRunes)
	}
	if want := "- " + strings.Repeat("a", 48); proposal.Body[0] != want {
		t.Errorf("bullet = %q, want %q", proposal.Body[0], want)
	}
}

func TestRepairInsertsMissingBodySeparatorAndNormalizesMarkers(t *testing.T) {
	proposal, ok := repairProposal("docs: explain commit flow\n* first item\n* second item", nil, "commit")
	if !ok {
		t.Fatal("a message with a missing separator and star bullets was not repaired")
	}
	if got, want := proposal.Message(), "docs(commit): explain commit flow\n\n- first item\n- second item"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestRepairCapsTheBodyAtFiveBullets(t *testing.T) {
	bullets := make([]string, 0, MaxBullets+2)
	for i := 0; i < MaxBullets+2; i++ {
		bullets = append(bullets, "- bullet "+strconv.Itoa(i))
	}
	proposal, ok := repairProposal("docs: explain\n\n"+strings.Join(bullets, "\n"), nil, "commit")
	if !ok {
		t.Fatal("a body with too many bullets was not repaired")
	}
	if len(proposal.Body) != MaxBullets {
		t.Fatalf("body has %d bullets, want the first %d", len(proposal.Body), MaxBullets)
	}
	if got, want := proposal.Body[0], bullets[0]; got != want {
		t.Errorf("first bullet = %q, want %q", got, want)
	}
}

func TestRepairDropsATrailingStopWord(t *testing.T) {
	proposal, ok := repairProposal("docs: add commit docs and more details", nil, "commit")
	if !ok {
		t.Fatal("a structurally valid message was not repaired")
	}
	if got, want := proposal.Subject(), "docs(commit): add commit docs"; got != want {
		t.Errorf("subject = %q, want the trailing conjunction dropped: %q", got, want)
	}
}

func TestRepairNeverBypassesValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"invented reference", "docs: close #999"},
		{"invented body reference", "docs: explain\n\n- close #999"},
		{"invalid bullet", "docs: explain\n\n+ plus"},
		{"missing type", "Update: explain"},
		{"nothing but prose", "please write a commit message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if proposal, ok := repairProposal(tc.text, nil, "commit"); ok {
				t.Fatalf("repair accepted invalid text as %q", proposal.Message())
			}
		})
	}
	refs := map[string]bool{"#999": true}
	if _, ok := repairProposal("docs: explain\n\n- close #999", refs, "commit"); !ok {
		t.Error("repair rejected a reference present in the evidence")
	}
}

func TestSuggestedScopeIsDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		root  string
		paths []string
		want  string
	}{
		{"most common directory", "/tmp/ohgo", []string{"commit/a.go", "commit/b.go", "docs/x.md"}, "commit"},
		{"tie is lexical", "/tmp/ohgo", []string{"docs/x.md", "commit/a.go"}, "commit"},
		{"root files use repository name", "/tmp/ohgo", []string{"README.md", "main.go"}, "ohgo"},
		{"repository name is sanitized", "/tmp/My Project!", []string{"README.md"}, "myproject"},
		{"long directory falls back", "/tmp/ohgo", []string{"very-long-directory-name/a.go"}, "ohgo"},
		{"no usable name", "/", []string{"README.md"}, "repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &Snapshot{Root: tc.root}
			for _, path := range tc.paths {
				snapshot.Scope = append(snapshot.Scope, FileChange{Path: path})
			}
			if got := snapshot.SuggestedScope(); got != tc.want {
				t.Errorf("scope = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseAcceptsOneOuterCodeFence(t *testing.T) {
	proposal, err := Parse("```\nfix(scope): fenced\n\n- ok\n```", nil)
	if err != nil {
		t.Fatalf("fenced message rejected: %v", err)
	}
	if proposal.Subject() != "fix(scope): fenced" {
		t.Errorf("subject = %q", proposal.Subject())
	}
}

func TestExecuteRejectsATamperedProposal(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	stageTestFile(t, repo, "a.txt")

	request := inspectTestRepo(t, repo)
	request.Proposal = Proposal{Type: "fix", Scope: "scope", Summary: "x", Body: []string{"- " + strings.Repeat("a", 60)}}
	service := testService(repo, "unused")
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("tampered proposal executed or wrong error: %v", err)
	}
	if count := commitCount(t, repo); count != 1 {
		t.Errorf("commits = %d, want the initial commit only", count)
	}
}

func TestAbortTextNamesTheRequiredOutcomes(t *testing.T) {
	if got := AbortText(ErrNoChanges); got != "No changes to commit." {
		t.Errorf("no changes = %q", got)
	}
	if got := AbortText(ErrStateChanged); got != "Commit aborted: repository state changed after proposal." {
		t.Errorf("state changed = %q", got)
	}
}

func TestFormatProposalShowsTheMessageAndExactScope(t *testing.T) {
	request := &Request{
		Proposal: Proposal{
			Type:    "feat",
			Scope:   "commit",
			Summary: "safe proposal",
			Body:    []string{"- one change"},
		},
		Snapshot: &Snapshot{
			Mode: ModeWorktree,
			Scope: []FileChange{
				{Path: "a.go", Worktree: 'M', Index: ' '},
				{Path: "b.go", Untracked: true, Index: '?', Worktree: '?'},
			},
			Internal: []FileChange{{Path: ".ohgo/project.yml", Internal: true, Index: '?', Worktree: '?'}},
		},
	}
	got := FormatProposal(request)
	for _, want := range []string{
		"Commit proposal",
		"feat(commit): safe proposal",
		"- one change",
		"Files:",
		"M a.go",
		"?? b.go",
		"Tracked and untracked changes will be staged explicitly.",
		".ohgo/ internal state is excluded.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("proposal = %q, want %q", got, want)
		}
	}
}

// --- repository test helpers -------------------------------------------------

func newTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	dir := t.TempDir()
	runTestGit(t, dir, "init", "--quiet")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test")
	runTestGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stageTestFile(t *testing.T, dir, name string) {
	t.Helper()
	runTestGit(t, dir, "add", "--", name)
}

func initialCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	writeTestFile(t, dir, name, content)
	stageTestFile(t, dir, name)
	runTestGit(t, dir, "commit", "-qm", "initial")
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	out := strings.TrimSpace(runTestGit(t, dir, "rev-list", "--count", "HEAD"))
	count, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("parse commit count %q: %v", out, err)
	}
	return count
}

func testService(root, text string) *Service {
	return &Service{
		Root:   root,
		Stream: staticStream(text),
		Model:  ai.Model{Provider: "test", ID: "commit-model", API: "test"},
	}
}

func staticStream(text string) ai.StreamFn {
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: text}},
		}), nil
	}
}

func inspectTestRepo(t *testing.T, root string) *Request {
	t.Helper()
	service := testService(root, "feat(scope): approved\n\n- one approved change")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return request
}
