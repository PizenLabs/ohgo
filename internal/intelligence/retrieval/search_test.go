package retrieval

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func goDoc(rel, source string) Document {
	return DocumentFromSource(rel, "go", "go_source", false, int64(len(source)), 1, []byte(source), DefaultOptions())
}

func indexOf(t *testing.T, docs ...Document) *Index {
	t.Helper()
	index := NewIndex(DefaultOptions())
	if err := index.Apply(docs, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return index
}

func search(t *testing.T, index *Index, query Query) Result {
	t.Helper()
	result, err := index.Search(query)
	if err != nil {
		t.Fatalf("Search(%q): %v", query.Text, err)
	}
	return result
}

// TestSearchSaturatesTermFrequency pins the BM25 saturation property: three
// occurrences beat one, but not by a factor of three, because repeated terms
// carry diminishing evidence.
func TestSearchSaturatesTermFrequency(t *testing.T) {
	index := indexOf(t,
		goDoc("a/x.go", "package a\nfunc checkout() { checkout(); checkout() }\n"),
		goDoc("b/y.go", "package b\nfunc checkout() {}\n"),
	)
	result := search(t, index, Query{Text: "checkout"})
	if len(result.Hits) != 2 || result.Hits[0].Path != "a/x.go" {
		t.Fatalf("hits = %+v, want a/x.go first", result.Hits)
	}
	ratio := result.Hits[0].Score / result.Hits[1].Score
	if ratio <= 1.1 || ratio >= 3 {
		t.Fatalf("tf ratio = %.2f, want saturation between 1.1 and 3", ratio)
	}
}

// TestSearchNormalizesDocumentLength pins that a match in a short file beats
// the same match buried in a long one.
func TestSearchNormalizesDocumentLength(t *testing.T) {
	long := strings.Builder{}
	long.WriteString("package a\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&long, "func filler%d() {}\n", i)
	}
	long.WriteString("func checkout() {}\n")
	index := indexOf(t,
		goDoc("a/short.go", "package a\nfunc checkout() {}\n"),
		goDoc("b/long.go", long.String()),
	)
	result := search(t, index, Query{Text: "checkout"})
	if len(result.Hits) != 2 || result.Hits[0].Path != "a/short.go" {
		t.Fatalf("hits = %+v, want the short file first", result.Hits)
	}
}

// TestSearchIDFPrefersRareTerms pins that a term present in one document
// contributes more than a term present everywhere.
func TestSearchIDFPrefersRareTerms(t *testing.T) {
	docs := []Document{
		goDoc("a/handler.go", "package a\nfunc Handler() { Authentication() }\n"),
	}
	for i := 0; i < 4; i++ {
		docs = append(docs, goDoc(fmt.Sprintf("b/handler%d.go", i), "package b\nfunc Handler() {}\n"))
	}
	result := search(t, indexOf(t, docs...), Query{Text: "authentication handler"})
	if len(result.Hits) == 0 || result.Hits[0].Path != "a/handler.go" {
		t.Fatalf("hits = %+v, want the rare-term document first", result.Hits)
	}
	if result.Hits[0].BM25 <= result.Hits[1].BM25 {
		t.Fatalf("rare-term score %.3f did not beat common-term score %.3f", result.Hits[0].BM25, result.Hits[1].BM25)
	}
}

// TestSearchExactIdentifierBeatsSplitParts pins the hybrid exact signal: a
// compound identifier match outranks the same words as separate identifiers.
func TestSearchExactIdentifierBeatsSplitParts(t *testing.T) {
	index := indexOf(t,
		goDoc("a/exact.go", "package a\nfunc HandleRequest() {}\n"),
		goDoc("b/parts.go", "package b\nvar handle int\nvar request int\n"),
	)
	result := search(t, index, Query{Text: "HandleRequest"})
	if len(result.Hits) != 2 {
		t.Fatalf("hits = %+v", result.Hits)
	}
	if result.Hits[0].Path != "a/exact.go" || !result.Hits[0].Exact {
		t.Fatalf("first hit = %+v, want the exact identifier marked", result.Hits[0])
	}
	if result.Hits[1].Exact {
		t.Errorf("split-part document reported an exact identifier match")
	}
}

// TestSearchPathMatchIsReported pins that a path-only match is retrievable and
// marked, so evidence can explain why a file surfaced.
func TestSearchPathMatchIsReported(t *testing.T) {
	index := indexOf(t,
		goDoc("billing/service.go", "package service\nfunc Run() {}\n"),
		goDoc("other/x.go", "package other\nfunc Billing() {}\n"),
	)
	result := search(t, index, Query{Text: "billing"})
	if len(result.Hits) != 2 {
		t.Fatalf("hits = %+v", result.Hits)
	}
	if result.Hits[0].Path != "billing/service.go" || !result.Hits[0].PathMatch {
		t.Fatalf("first hit = %+v, want the path match first", result.Hits[0])
	}
}

// TestSearchClassificationFilters pins that tests and documentation are
// opt-in, so an implementation query is not buried in test fixtures.
func TestSearchClassificationFilters(t *testing.T) {
	tests := DocumentFromSource("a/auth_test.go", "go", "go_test", true, 1, 1,
		[]byte("package a\nfunc TestAuthentication() {}\n"), DefaultOptions())
	docs := DocumentFromSource("README.md", "markdown", "documentation", false, 1, 1,
		[]byte("# Authentication design\n"), DefaultOptions())
	impl := goDoc("auth/service.go", "package auth\nfunc Authenticate() {}\n")
	index := indexOf(t, tests, docs, impl)

	defaultResult := search(t, index, Query{Text: "authentication"})
	for _, hit := range defaultResult.Hits {
		if hit.Path == "a/auth_test.go" || hit.Path == "README.md" {
			t.Fatalf("default search returned %q", hit.Path)
		}
	}
	withTests := search(t, index, Query{Text: "authentication", IncludeTests: true})
	if !hasPath(withTests.Hits, "a/auth_test.go") {
		t.Fatal("include-tests search omitted the test file")
	}
	withDocs := search(t, index, Query{Text: "authentication", IncludeDocs: true})
	if !hasPath(withDocs.Hits, "README.md") {
		t.Fatal("include-docs search omitted the documentation")
	}
}

// TestSearchIsDeterministic pins that insertion order cannot change scores or
// tie order.
func TestSearchIsDeterministic(t *testing.T) {
	docs := []Document{
		goDoc("a/one.go", "package a\nfunc Payment() {}\n"),
		goDoc("a/two.go", "package a\nfunc Payment() {}\n"),
		goDoc("b/three.go", "package b\nfunc Payment() {}\n"),
	}
	first := search(t, indexOf(t, docs...), Query{Text: "payment"})
	reversed := []Document{docs[2], docs[1], docs[0]}
	second := search(t, indexOf(t, reversed...), Query{Text: "payment"})
	if len(first.Hits) != len(second.Hits) {
		t.Fatalf("hit counts differ: %d vs %d", len(first.Hits), len(second.Hits))
	}
	for i := range first.Hits {
		if first.Hits[i].Path != second.Hits[i].Path || first.Hits[i].Score != second.Hits[i].Score {
			t.Fatalf("hit %d differs: %+v vs %+v", i, first.Hits[i], second.Hits[i])
		}
	}
}

// TestSearchRemoveAndReplaceInvalidatePostings pins that a deleted document
// leaves no posting behind and a rewritten document stops matching old terms.
func TestSearchRemoveAndReplaceInvalidatePostings(t *testing.T) {
	index := indexOf(t,
		goDoc("a/remove.go", "package a\nfunc ObsoleteMarker() {}\n"),
		goDoc("a/keep.go", "package a\nfunc KeepOld() {}\n"),
	)
	if got := search(t, index, Query{Text: "obsolete"}); len(got.Hits) != 1 {
		t.Fatalf("pre-remove hits = %+v", got.Hits)
	}
	if err := index.Remove("a/remove.go"); err != nil {
		t.Fatal(err)
	}
	if got := search(t, index, Query{Text: "obsolete"}); len(got.Hits) != 0 {
		t.Fatalf("removed document still matched: %+v", got.Hits)
	}
	if err := index.Update(goDoc("a/keep.go", "package a\nfunc KeepNew() {}\n")); err != nil {
		t.Fatal(err)
	}
	if got := search(t, index, Query{Text: "keepold"}); len(got.Hits) != 0 {
		t.Fatalf("stale identifier still matched: %+v", got.Hits)
	}
	if got := search(t, index, Query{Text: "keepnew"}); len(got.Hits) != 1 || got.Hits[0].Path != "a/keep.go" {
		t.Fatalf("rewritten document hits = %+v", got.Hits)
	}
}

func TestSearchRejectsEmptyQueryAndAcceptsUnknownTerms(t *testing.T) {
	index := indexOf(t, goDoc("a/x.go", "package a\nfunc X() {}\n"))
	if _, err := index.Search(Query{Text: "   "}); err == nil {
		t.Fatal("empty query was accepted")
	}
	result := search(t, index, Query{Text: "nonexistentterm"})
	if len(result.Hits) != 0 || result.Candidates != 0 {
		t.Fatalf("unknown term result = %+v", result)
	}
}

func TestSearchLimitScopeAndTruncation(t *testing.T) {
	index := indexOf(t,
		goDoc("a/one.go", "package a\nfunc Payment() {}\n"),
		goDoc("a/two.go", "package a\nfunc Payment() {}\n"),
		goDoc("b/three.go", "package b\nfunc Payment() {}\n"),
	)
	limited := search(t, index, Query{Text: "payment", Limit: 2})
	if len(limited.Hits) != 2 || !limited.Truncated {
		t.Fatalf("limited result = %+v, want two truncated hits", limited)
	}
	scoped := search(t, index, Query{Text: "payment", Scope: "b"})
	if len(scoped.Hits) != 1 || scoped.Hits[0].Path != "b/three.go" {
		t.Fatalf("scoped result = %+v", scoped.Hits)
	}
}

func TestSearchProximityIsBoundedAndApplied(t *testing.T) {
	index := indexOf(t,
		goDoc("a/near.go", "package a\nfunc Payment() {}\n"),
		goDoc("b/far.go", "package b\nfunc Payment() {}\n"),
	)
	result := search(t, index, Query{
		Text: "payment",
		Proximity: func(path string) float64 {
			if path == "a/near.go" {
				return 4 // clamped to 1
			}
			return 0
		},
	})
	if result.Hits[0].Path != "a/near.go" || result.Hits[0].Proximity != 1 {
		t.Fatalf("proximity result = %+v", result.Hits)
	}
	if math.Abs(result.Hits[0].Proximity-1) > 1e-9 {
		t.Fatal("proximity was not clamped")
	}
}

func hasPath(hits []Hit, path string) bool {
	for _, hit := range hits {
		if hit.Path == path {
			return true
		}
	}
	return false
}
