package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

const terminalBlogSource = `
interface TerminalBlogProps {
  publishedAt: string;
  className?: string;
}

export function TerminalBlog({ publishedAt, className }: TerminalBlogProps) {
  return <article className={className}>{publishedAt}</article>
}
`

func readOutcome(path, text string) ToolOutcome {
	return ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result:   Result{Text: text},
	}
}

// The observed failure: the model invented a prop that is absent from the
// component's declared props. The declaration is in hand, so the contradiction
// is deterministic and must veto completion.
func TestCodeClaimRejectsInventedPropAgainstObservedDeclaration(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("components/TerminalBlog.tsx", terminalBlogSource),
	}}}
	answer := "`TerminalBlog` accepts a `showDetails` prop that expands the section."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_claim" {
		t.Fatalf("issues = %+v, want an unsupported_code_claim", issues)
	}
	if !strings.Contains(issues[0].Detail, "showDetails") {
		t.Fatalf("detail = %q, want the invented identifier named", issues[0].Detail)
	}
}

// A member that is present in the observed declaration is supported. The
// contract must not punish a correct prop claim.
func TestCodeClaimAcceptsObservedDeclaredProp(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("components/TerminalBlog.tsx", terminalBlogSource),
	}}}
	answer := "`TerminalBlog` accepts a `publishedAt` prop and renders it."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("a declared prop was reviewed: %+v", issues)
	}
}

// Without any source observation this run cannot refute a code claim. A
// resumed answer may rely on earlier evidence, so absence of a current read is
// not a contradiction.
func TestCodeClaimSkipsWhenNoSourceWasObserved(t *testing.T) {
	answer := "`TerminalBlog` accepts a `showDetails` prop that expands the section."
	if issues := claimScopeIssues(answer, nil); len(issues) != 0 {
		t.Fatalf("a claim was reviewed without source evidence: %+v", issues)
	}
}

// A claim about an identifier the run never saw, while it did read source, is
// unsupported even when no declaration block was captured.
func TestCodeClaimRejectsUnobservedIdentifierWithSourceContext(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("lib/blog-data.ts", "export function loadPosts() { return [] }\n"),
	}}}
	answer := "The loader accepts a `includeDrafts` parameter."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_claim" {
		t.Fatalf("issues = %+v, want an unsupported_code_claim", issues)
	}
}

// The code-claim audit is an answer check, not a replacement for grounding. It
// must reach the completion path through AssessGrounding unchanged.
func TestCodeClaimReachesGroundingAssessment(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("components/TerminalBlog.tsx", terminalBlogSource),
	}}}
	answer := "`TerminalBlog` accepts a `showDetails` prop that expands the section."
	assessment := AssessGrounding(intent, turns, answer)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "showDetails") {
		t.Fatalf("grounding = %+v, want a code-claim review", assessment)
	}
}
