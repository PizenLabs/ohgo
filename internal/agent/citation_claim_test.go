package agent

import (
	"strings"
	"testing"
)

// Session 295: a metadata survey read a manifest, a README-shaped requirement
// doc, two route files, and directory listings, then attributed a build-time
// search index to lib/search.ts and lib/content.ts. Neither file was opened;
// the citations made an inference look inspected. The answer was marked
// proven.
func TestCitedPathRequiresSourceObservation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "app/\ncomponents/\ncontent/\nlib/\nstore/\npackage.json\n"),
		claimRead("package.json", `{"dependencies":{"flexsearch":"^0.8.212","next":"16.2.4"}}`, 92),
		claimRead("docs/requirement.md", "Final Tech Stack\n\nCore Framework\n\n* Next.js\n", 222),
		claimRead("app/page.tsx", "export default function HomePage() { return null }\n", 14),
	}}}
	answer := strings.Join([]string{
		"andev0x is a personal blog built as a Next.js 16 static site.",
		"State: Zustand (store/ui.ts) for UI mode and selection.",
		"Search: FlexSearch with a build-time prebuilt index (lib/search.ts, lib/content.ts).",
	}, "\n\n")

	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("citations of unopened source files were accepted as evidence")
	}
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_source_citation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want unsupported_source_citation", assessment.Issues)
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"lib/search.ts", "never opened"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}
	if verdict := claimVerdict(turns, answer); verdict.Complete() {
		t.Fatal("an answer citing unopened files completed")
	}
}

// The same sentence is supported once the cited file is opened: the audit
// gates the citation, not the topic.
func TestCitedPathWithSourceReadCompletes(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "lib/\npackage.json\n"),
		claimRead("package.json", `{"dependencies":{"flexsearch":"^0.8.212"}}`, 92),
		claimRead("lib/search.ts",
			"\"use client\"\n\nlet index = null\n\nexport function initSearchIndex(docs) { index = new FlexSearch.Document({}) }\n",
			60),
	}}}
	answer := "Search uses FlexSearch, with the index built in the browser (lib/search.ts)."
	if issues := AssessGrounding(claimContract(), turns, answer).Issues; len(issues) != 0 {
		t.Fatalf("a citation backed by the file's source was reviewed: %+v", issues)
	}
	if verdict := claimVerdict(turns, answer); !verdict.Complete() {
		t.Fatalf("a supported citation was reviewed: %s", verdict.Reason)
	}
}

// Only an explicit citation form is audited. A route table, a directory, or a
// path mentioned in prose stays out of the review: those are inventory
// statements, and forcing them to be read would turn every survey answer into
// a review.
func TestProsePathMentionIsNotACitation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "app/\ncomponents/\nlib/\npackage.json\n"),
		claimRead("package.json", `{"dependencies":{"next":"16.2.4"}}`, 92),
		claimRead("app/page.tsx", "export default function Page() { return null }\n", 14),
	}}}
	answer := strings.Join([]string{
		"Routes: / (app/page.tsx), /posts/[slug], and /info.",
		"Search lives in lib/search.ts and state in store/ui.ts.",
	}, "\n\n")
	if issues := AssessGrounding(claimContract(), turns, answer).Issues; len(issues) != 0 {
		t.Fatalf("prose mentions were treated as citations: %+v", issues)
	}
}

// A source file paged through a shell content reader was opened. Treating the
// `sed` observation as a listing forced a false review even though the cited
// path's contents were in the run.
func TestBashSourceReadSupportsCitation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{{
		ToolName: "bash",
		Args:     map[string]any{"command": "cd backend && sed -n 200,400p checkout/app/checkout.go"},
		Result:   Result{Text: "package app\n\nfunc Place() {}\n"},
	}}}}
	if issue := citationClaimIssue("Place calls Reserve before Save (checkout/app/checkout.go).", turns); issue != nil {
		t.Fatalf("a shell source read was treated as an unopened citation: %+v", issue)
	}
	// A command that only lists the path does not count.
	listing := []Turn{{Results: []ToolOutcome{{
		ToolName: "bash",
		Args:     map[string]any{"command": "ls checkout/app/checkout.go"},
		Result:   Result{Text: "checkout/app/checkout.go\n"},
	}}}}
	if issue := citationClaimIssue("Place calls Reserve before Save (checkout/app/checkout.go).", listing); issue == nil {
		t.Fatal("a listing was treated as a source observation")
	}
}

// A grep hit is a source observation of the matched lines. The model traced an
// event through `grep -rn "OrderPaid"`, cited the file with the line range the
// search reported, and was told the file was never opened. When the cited
// window contains an observed match, the citation is supported; a listing, a
// match outside the window, or a bare path still is not.
func TestGrepLineMatchSupportsCitation(t *testing.T) {
	grep := []Turn{{Results: []ToolOutcome{{
		ToolName: "bash",
		Args:     map[string]any{"command": "cd backend && grep -rn \"OrderPaid\" --include=*.go ."},
		Result: Result{
			Text: "./fulfillment/app/service.go:490:\t\t\ts.publisher.Publish(ctx, integration.OrderShipped{\n" +
				"./fulfillment/app/service.go:497:\t\t\ts.publisher.Publish(ctx, integration.OrderDelivered{\n",
			Executes: true, Finished: true,
		},
	}}}}
	sentence := "Fulfillment publishes OrderShipped and OrderDelivered (`fulfillment/app/service.go:490-502`)."
	if issue := citationClaimIssue(sentence, grep); issue != nil {
		t.Fatalf("an observed grep window was treated as an unopened citation: %+v", issue)
	}

	// A listing names the path but observes no line.
	listing := []Turn{{Results: []ToolOutcome{{
		ToolName: "bash",
		Args:     map[string]any{"command": "find backend -name service.go"},
		Result:   Result{Text: "backend/fulfillment/app/service.go\n", Executes: true, Finished: true},
	}}}}
	if issue := citationClaimIssue(sentence, listing); issue == nil {
		t.Fatal("a listing was accepted as a source observation")
	}

	// The observed match is outside the cited window.
	outside := "Fulfillment publishes events (`fulfillment/app/service.go:600-610`)."
	if issue := citationClaimIssue(outside, grep); issue == nil {
		t.Fatal("a match outside the cited line window supported the citation")
	}
}

// A path named to say it was not opened is a limitation, not a citation. The
// corrected deep answer listed the next files to inspect and was reviewed as if
// it had presented them as evidence.
func TestUnopenedPathIsNotACitation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{claimListing("lib", "app.ts\n")}}}
	limitation := "The orchestrator (`lib/app.ts`) was not opened either."
	if issue := citationClaimIssue(limitation, turns); issue != nil {
		t.Fatalf("an explicit limitation was treated as a citation: %+v", issue)
	}
	claim := "Search lives in lib/app.ts (lib/app.ts)."
	if issue := citationClaimIssue(claim, turns); issue == nil {
		t.Fatal("a real citation of an unopened path was accepted")
	}
}
