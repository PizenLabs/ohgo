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
