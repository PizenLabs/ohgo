package commit

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/prompt"
)

// TestCommitPromptIsOnlyAMessageContract pins the responsibility boundary:
// COMMIT receives a small messaging instruction, not the ASK/BUILD/FUZZ agent
// prompt, tool guidance, approval policy, or execution instructions.
func TestCommitPromptIsOnlyAMessageContract(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var captured ai.Request
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			captured = req
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "fix(scope): compact"}},
			}), nil
		},
	}
	if _, err := service.Propose(context.Background()); err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if len(captured.System) != 1 || captured.System[0].Text != prompt.Commit {
		t.Fatalf("system prompt = %#v, want exactly prompt.Commit", captured.System)
	}
	if len(captured.Tools) != 0 || captured.ToolChoice.Type != "" {
		t.Errorf("commit request carries tools = %d choice %q", len(captured.Tools), captured.ToolChoice.Type)
	}
	if len(prompt.Commit) > 1024 {
		t.Errorf("commit prompt is %d bytes; it is not a compact messaging contract", len(prompt.Commit))
	}
	for _, forbidden := range []string{
		"ASK", "BUILD", "FUZZ", "tool call", "approval", "git commit -m",
		"You are a coding agent", "workspace", "planning",
	} {
		if strings.Contains(prompt.Commit, forbidden) {
			t.Errorf("commit prompt contains unrelated agent instruction %q", forbidden)
		}
	}
}

// TestCommitEvidenceCarriesOnlyTheApprovedChange proves the model sees the
// exact scope, the per-file summary, and the diff, while duplicated state and
// repository metadata do not consume the request.
func TestCommitEvidenceCarriesOnlyTheApprovedChange(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")
	writeTestFile(t, repo, "new.txt", "hello\n")

	service := testService(repo, "feat(scope): bounded evidence")
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	evidence := request.Snapshot.Evidence()
	for _, want := range []string{
		"repository:", "approved scope:", "M a.txt", "?? new.txt",
		"change summary:", "a.txt +1 -1", "relevant diff:",
		"new files:", "new.txt:",
	} {
		if !strings.Contains(evidence, want) {
			t.Errorf("evidence = %q, want %q", evidence, want)
		}
	}
	for _, redundant := range []string{
		"head:", "head subject:", "branch:", "scope mode:",
		"staged:", "unstaged:", "untracked:", "recent commits:",
		"ignored (excluded):", "internal .ohgo",
	} {
		if strings.Contains(evidence, redundant) {
			t.Errorf("evidence carries redundant field %q:\n%s", redundant, evidence)
		}
	}
}

// TestOverlongSubjectUsesAFocusedRewriteAndKeepsTheBody is the quality/token
// seam for the one rule this model misses: the correction request carries only
// the previous subject, and the already-valid body is reattached unchanged.
func TestOverlongSubjectUsesAFocusedRewriteAndKeepsTheBody(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	long := "fix(scope): " + strings.Repeat("a", 30)
	body := "- keep this body"
	var calls atomic.Int32
	var first, second ai.Request
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			if calls.Add(1) == 1 {
				first = req
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: long + "\n\n" + body}},
				}), nil
			}
			second = req
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "fix(scope): short"}},
			}), nil
		},
	}
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("model calls = %d, want the full proposal plus one focused rewrite", got)
	}
	if first.System[0].Text != prompt.Commit {
		t.Error("the first request did not use the messaging prompt")
	}
	if second.System[0].Text != prompt.CommitShorten {
		t.Error("the correction did not use the focused subject prompt")
	}
	if got := second.Messages[0].(ai.UserMessage).Text(); strings.Contains(got, "repository:") {
		t.Errorf("focused rewrite re-sent the evidence: %q", got)
	}
	if got := request.Proposal.Message(); got != "fix(scope): short\n\n"+body {
		t.Errorf("message = %q, want the focused subject with the original body", got)
	}
}

// TestOverlongSubjectDetectionIsPrecise proves the focused path fires only for a
// structurally valid subject that is too long, never for a malformed one.
func TestOverlongSubjectDetectionIsPrecise(t *testing.T) {
	long := "fix(scope): " + strings.Repeat("a", 30)
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"overlong", long + "\n\n- body", true},
		{"at limit", "fix(scope): " + strings.Repeat("a", 22), false},
		{"missing scope", "fix: " + strings.Repeat("a", 30), false},
		{"invalid scope", "fix(Bad Scope): " + strings.Repeat("a", 30), false},
		{"invalid body", long + "\n\n- " + strings.Repeat("a", 60), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, got := overlongSubject(tc.text, nil); got != tc.want {
				t.Errorf("overlongSubject(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestEvidenceBoundsALargeDiffWithoutHidingFiles proves the diff is bounded
// explicitly and the scope summary still names every changed path.
func TestEvidenceBoundsALargeDiffWithoutHidingFiles(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "big.txt", "start\n")
	var body strings.Builder
	for i := 0; i < 4000; i++ {
		body.WriteString("a line of changed content for the bounded diff fixture\n")
	}
	writeTestFile(t, repo, "big.txt", body.String())

	snapshot := snapshotOf(t, repo)
	if len(snapshot.Diff) > maxDiffBytes+len("\n... (truncated)") {
		t.Errorf("diff is %d bytes, want it bounded at %d", len(snapshot.Diff), maxDiffBytes)
	}
	if !strings.Contains(snapshot.Diff, "(truncated)") {
		t.Error("a bounded diff must say that it is truncated")
	}
	if !strings.Contains(snapshot.Evidence(), "big.txt") {
		t.Error("the scope summary lost the changed path")
	}
}
