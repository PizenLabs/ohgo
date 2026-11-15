package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// An answer that calls a path missing while the run listed it must be reported
// as a contradiction. The observed session said "no portfolio file exists
// anywhere under app/" after `ls app` returned app/portfolio.tsx: reporting
// "absence unproven" instead left the reader unable to see which claim was
// wrong, and the review named the bare noun "file" rather than the file.
func TestNegativeClaimIsContradictedByAnObservedPath(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		{
			CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "app"},
			Result: Result{
				Text: "app\n   15086  favicon.ico\n      97  page.tsx\n    6457  portfolio.tsx\n",
				Details: map[string]any{
					"path": "app", "entries": 3, "depth": 0, "truncated": false,
					"paths": []string{"app/favicon.ico", "app/page.tsx", "app/portfolio.tsx"},
				},
			},
		},
	}}}
	answer := "Entry point: app/page.tsx renders <Portfolio />. " +
		"Notable finding: no portfolio file exists anywhere under app/, so the component file is missing."

	assessment := AssessGrounding(claimContractFor("analyze what this project is about"), turns, answer)
	var detail string
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_negative_conclusion" {
			detail = issue.Detail
		}
	}
	if detail == "" {
		t.Fatalf("issues = %+v, want unsupported_negative_conclusion", assessment.Issues)
	}
	if !strings.Contains(detail, "app/portfolio.tsx") {
		t.Fatalf("detail = %q, want the contradicting observation named", detail)
	}
	if !strings.Contains(detail, "portfolio") {
		t.Fatalf("detail = %q, want the specific subject, not the bare noun", detail)
	}
}

// A claim the run cannot refute keeps the coverage wording: the audit must not
// invent an observation to make its message stronger.
func TestNegativeClaimWithoutContradictionKeepsCoverageWording(t *testing.T) {
	// A run that never listed the tree cannot refute anything; the wording
	// must stay the coverage verdict rather than inventing an observation.
	var turns []Turn
	answer := "There is no payment module anywhere in this project, so nothing charges a card."
	assessment := AssessGrounding(claimContractFor("analyze what this project is about"), turns, answer)
	var detail string
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_negative_conclusion" {
			detail = issue.Detail
		}
	}
	if detail == "" {
		t.Fatalf("issues = %+v, want unsupported_negative_conclusion", assessment.Issues)
	}
	if strings.Contains(detail, "observed") {
		t.Fatalf("detail = %q, want no invented observation", detail)
	}
}

// claimContractFor builds the contract a plain informational request produces,
// so a test states only the request and the answer it judges.
func claimContractFor(text string) contract.IntentContract {
	return informationalContract(&contract.IntentGrounding{Objective: text})
}

// The observed session-361 answer declined to read the component it had just
// listed: "the ./portfolio module ... was not in the inspected file set (the
// scope is partial — at least one source file was excluded), so I couldn't read
// its contents". The excluded file was the generated next-env.d.ts; the module
// was listed twice and `read` could open it. The review must name the path it
// declines to read, because "reports an inability to find evidence" points the
// reader at the repository when the missing call is the answer's.
func TestUnreadObservedPathIsNamedInTheReview(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		{
			CallID: "ls-2", ToolName: "ls", Args: map[string]any{"path": "app"},
			Result: Result{
				Text: "app\n   15086  favicon.ico\n      25  globals.css\n     755  layout.tsx\n      97  page.tsx\n    6457  portfolio.tsx\n",
				Details: map[string]any{
					"path": "app", "entries": 5,
					"paths": []string{
						"app/favicon.ico", "app/globals.css", "app/layout.tsx",
						"app/page.tsx", "app/portfolio.tsx",
					},
				},
			},
		},
	}}}
	answer := "This is a Next.js 16.4.6 project (React 19, TypeScript). The root page renders a single component. " +
		"However, the ./portfolio module it imports was not in the inspected file set (the scope is partial), " +
		"so I couldn't read its contents to describe the actual portfolio sections."

	assessment := AssessGrounding(claimContractFor("check this project and analyze what this project is about?"), turns, answer)
	var detail string
	for _, issue := range assessment.Issues {
		if issue.Code == "unverified_negative_result" {
			detail = issue.Detail
		}
	}
	if detail == "" {
		t.Fatalf("issues = %+v, want unverified_negative_result", assessment.Issues)
	}
	if !strings.Contains(detail, "app/portfolio.tsx") {
		t.Fatalf("detail = %q, want the path the answer declines to read", detail)
	}
}
