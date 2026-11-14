package agent

import (
	"strings"
	"testing"
)

const nextManifest = `{"name":"andev0x","dependencies":{"next":"16.2.4","react":"^19","flexsearch":"^0.8.212","zustand":"^5.0.13","next-themes":"^0.4.6","shiki":"^4.0.2"}}`

// Session 298: a manifest-only survey wrote a stack table with "FlexSearch with
// a build-time prebuilt index". The dependency is declared, the mechanism was
// never observed, and the real index is constructed in the browser. Mechanism
// phrases carry no claim verb, so neither the dependency ladder nor the
// citation audit saw it.
func TestDeclaredDependencyMechanismClaimRequiresSource(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "app/\nlib/\npackage.json\n"),
		claimRead("package.json", nextManifest, 92),
		claimRead("docs/requirement.md", "Final Tech Stack\n\n* Next.js\n", 222),
	}}}
	answer := "Search: FlexSearch with a build-time prebuilt index."

	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("a manifest declaration was accepted as dependency wiring")
	}
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_wiring_claim" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want unsupported_wiring_claim", assessment.Issues)
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"flexsearch", "build-time"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}
	if verdict := claimVerdict(turns, answer); verdict.Complete() {
		t.Fatal("an unobserved dependency mechanism completed")
	}
}

// Reading the source that wires the dependency clears the claim.
func TestDependencyMechanismWithSourceCompletes(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("package.json", nextManifest, 92),
		claimRead("lib/search.ts",
			"\"use client\"\nimport FlexSearch from \"flexsearch\"\n\nexport function initSearchIndex(docs) { new FlexSearch.Document({}) }\n",
			60),
	}}}
	answer := "Search: FlexSearch with the index built in the browser (lib/search.ts)."
	if issues := AssessGrounding(claimContract(), turns, answer).Issues; len(issues) != 0 {
		t.Fatalf("a mechanism backed by source was reviewed: %+v", issues)
	}
	if verdict := claimVerdict(turns, answer); !verdict.Complete() {
		t.Fatalf("a supported mechanism was reviewed: %s", verdict.Reason)
	}
}

// A stack table that names versions is a declaration claim, and a property in
// the next comma-separated item does not belong to the dependency before it.
func TestVersionTableAndSeparatePropertiesAreNotWiringClaims(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("package.json", nextManifest, 92),
	}}}
	for _, answer := range []string{
		"Framework: Next.js 16 (App Router, SSG), TypeScript, deployed on Vercel.",
		"Styling: Tailwind CSS v4 + shadcn/Radix UI components, dark theme by default.",
		"Extras: next-themes, shiki highlighting, command palette.",
	} {
		if issues := AssessGrounding(claimContract(), turns, answer).Issues; len(issues) != 0 {
			t.Fatalf("a declaration-level stack row was reviewed: %q -> %+v", answer, issues)
		}
	}
}

// A hyphenated package keeps its identity: "next" is not a mention inside
// "next-themes", so the theme package cannot be attributed to Next's wiring.
func TestDependencyMentionsPreferTheLongestName(t *testing.T) {
	mentions := dependencyMentions("Extras: next-themes and next.", []string{"next-themes", "next"})
	if len(mentions) != 2 {
		t.Fatalf("mentions = %+v, want next-themes and the standalone next", mentions)
	}
	if mentions[0].name != "next-themes" {
		t.Fatalf("first mention = %q, want the hyphenated package", mentions[0].name)
	}
}
