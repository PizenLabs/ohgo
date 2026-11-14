package retrieval

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// labeledCorpus is a small, hand-labeled retrieval fixture. It is deliberately
// synthetic: every query has one file that a competent engineer would expect
// first, so Recall@K and MRR are ground truth rather than model opinion.
func labeledCorpus() ([]Document, []struct {
	query string
	path  string
}) {
	sources := map[string]string{
		"auth/service.go": `package auth
// Authentication validates credentials and starts a session.
func Authenticate(user string) (string, error) { return startSession(user) }
`,
		"auth/middleware.go":   "package auth\n// RequireAuth rejects requests without a session.\nfunc RequireAuth() {}\n",
		"billing/checkout.go":  "package billing\nfunc Checkout(cart Cart) error { return Charge(cart) }\n",
		"billing/refund.go":    "package billing\n// Refund reverses a charge for a customer.\nfunc Refund(payment Payment) error {}\n",
		"search/index.go":      "package search\n// BM25 scores a query against the inverted index.\nfunc ScoreBM25(query Query) float64 {}\n",
		"search/query.go":      "package search\nfunc ParseQuery(text string) Query {}\n",
		"worker/queue.go":      "package worker\n// Enqueue adds a job to the queue.\nfunc Enqueue(job Job) {}\n",
		"worker/retry.go":      "package worker\n// RetryPolicy decides how many times a job is retried.\nfunc RetryPolicy() {}\n",
		"web/checkout.tsx":     "export function CheckoutPage() { return <div>Checkout</div> }\n",
		"util/strings.go":      "package util\n// SplitIdentifier splits camelCase and snake_case identifiers.\nfunc SplitIdentifier(value string) []string {}\n",
		"docs/architecture.md": "# Architecture\n\nThe service uses a layered architecture with a domain core.\n",
		"auth/service_test.go": "package auth\nfunc TestAuthenticate(t *testing.T) { Authenticate(\"user\") }\n",
	}
	docs := make([]Document, 0, len(sources))
	for rel, content := range sources {
		docs = append(docs, goDoc(rel, content))
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	queries := []struct {
		query string
		path  string
	}{
		{"where is authentication implemented", "auth/service.go"},
		{"refund payment", "billing/refund.go"},
		{"bm25 scoring", "search/index.go"},
		{"retry policy worker", "worker/retry.go"},
		{"react checkout page", "web/checkout.tsx"},
		{"split identifier camel case", "util/strings.go"},
		{"enqueue job queue", "worker/queue.go"},
	}
	return docs, queries
}

// TestRetrievalQualityOnLabeledCorpus pins relevance, not just mechanics: the
// hybrid ranker must put the intended file first for most queries and in the
// top five for all of them. It also compares against a naive path-substring
// baseline, which is the honest "did BM25 add value" question.
func TestRetrievalQualityOnLabeledCorpus(t *testing.T) {
	docs, queries := labeledCorpus()
	index := indexOf(t, docs...)

	var reciprocal float64
	recall := 0
	for _, item := range queries {
		result := search(t, index, Query{Text: item.query, Limit: 5})
		rank := 0
		for position, hit := range result.Hits {
			if hit.Path == item.path {
				rank = position + 1
				break
			}
		}
		t.Logf("query %q: rank=%d top=%s", item.query, rank, topPaths(result.Hits))
		if rank > 0 {
			recall++
			reciprocal += 1 / float64(rank)
		}
	}
	mrr := reciprocal / float64(len(queries))
	if mrr < 0.8 {
		t.Fatalf("hybrid MRR = %.2f, want at least 0.80", mrr)
	}
	if recall != len(queries) {
		t.Fatalf("Recall@5 = %d/%d, want all labeled documents", recall, len(queries))
	}

	baseline := pathBaselineMRR(docs, queries)
	if mrr <= baseline {
		t.Fatalf("hybrid MRR %.2f did not beat the path baseline %.2f", mrr, baseline)
	}
}

// TestRetrievalFindsExactCompoundIdentifier pins the symbol-shaped query case:
// an exact identifier must be enough on its own.
func TestRetrievalFindsExactCompoundIdentifier(t *testing.T) {
	docs := []Document{
		goDoc("a/one.go", "package a\nfunc RotateSessionSecret() {}\n"),
		goDoc("b/two.go", "package b\nfunc Rotate() {}\nfunc Session() {}\nfunc Secret() {}\n"),
	}
	result := search(t, indexOf(t, docs...), Query{Text: "RotateSessionSecret"})
	if len(result.Hits) == 0 || result.Hits[0].Path != "a/one.go" || !result.Hits[0].Exact {
		t.Fatalf("exact query result = %+v", result.Hits)
	}
}

// pathBaselineMRR is the cheapest possible retrieval: rank documents by how
// many query words appear in the path. It exists to make the BM25 comparison
// falsifiable rather than rhetorical.
func pathBaselineMRR(docs []Document, queries []struct {
	query string
	path  string
}) float64 {
	total := 0.0
	for _, item := range queries {
		words := strings.Fields(strings.ToLower(item.query))
		type scored struct {
			path  string
			score int
		}
		ranked := make([]scored, 0, len(docs))
		for _, doc := range docs {
			score := 0
			lower := strings.ToLower(doc.Path)
			for _, word := range words {
				if strings.Contains(lower, word) {
					score++
				}
			}
			ranked = append(ranked, scored{path: doc.Path, score: score})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].path < ranked[j].path
		})
		for position, entry := range ranked {
			if entry.path == item.path {
				if entry.score > 0 {
					total += 1 / float64(position+1)
				}
				break
			}
		}
	}
	return total / float64(len(queries))
}

// BenchmarkBuildDocument measures tokenization and term counting for one
// source file.
func BenchmarkBuildDocument(b *testing.B) {
	source := []byte(strings.Repeat("func ParseRequest(ctx context.Context) (Response, error) {\n\treturn handle(ctx, request)\n}\n", 40))
	for b.Loop() {
		_ = DocumentFromSource("internal/server/handler.go", "go", "go_source", false, int64(len(source)), 1, source, DefaultOptions())
	}
}

// BenchmarkSearch measures query-time scoring over a thousand documents.
func BenchmarkSearch(b *testing.B) {
	index := NewIndex(DefaultOptions())
	docs := make([]Document, 0, 1000)
	for i := 0; i < 1000; i++ {
		source := fmt.Sprintf("package p%d\n// handler for request %d\nfunc HandleRequest%d() {}\n", i, i, i)
		docs = append(docs, DocumentFromSource(
			fmt.Sprintf("pkg%02d/file%d.go", i%20, i), "go", "go_source", false,
			int64(len(source)), int64(i+1), []byte(source), DefaultOptions()))
	}
	if err := index.Apply(docs, nil); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := index.Search(Query{Text: "handle request", Limit: 10}); err != nil {
			b.Fatal(err)
		}
	}
}

func topPaths(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Path)
	}
	return out
}
