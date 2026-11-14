package retrieval

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// MaxQueryChars bounds one query string before tokenization.
const MaxQueryChars = 512

// Query is one bounded retrieval request.
type Query struct {
	// Text is the natural-language or identifier query.
	Text string
	// Limit is the maximum number of hits. Zero uses 10.
	Limit int
	// IncludeTests includes test source in the candidate set.
	IncludeTests bool
	// IncludeDocs includes documentation files in the candidate set.
	IncludeDocs bool
	// Scope restricts candidates to one repository-relative directory or file.
	Scope string
	// Proximity is an optional graph-proximity signal in [0,1] keyed by path.
	// It is a mild reranking signal, never a substitute for query relevance:
	// the boost is capped below the exact-identifier boost.
	Proximity func(path string) float64
}

// Hit is one ranked document.
type Hit struct {
	// Path is the repository-relative identity.
	Path string
	// Score is the final hybrid score.
	Score float64
	// BM25 is the field-aware Okapi BM25 score before hybrid boosts.
	BM25 float64
	// Matches are the query terms observed in this document, sorted.
	Matches []string
	// Language and Kind describe the observed file.
	Language string
	Kind     string
	// Test and Generated report classification signals used by reranking.
	Test      bool
	Generated bool
	// Truncated reports that only the top terms of the document were indexed.
	Truncated bool
	// Exact reports that a whole query identifier matched an indexed
	// identifier term.
	Exact bool
	// PathMatch reports that a whole query identifier matched a path token.
	PathMatch bool
	// Proximity is the supplied graph signal that was applied.
	Proximity float64
}

// Result is one bounded page of ranked documents.
type Result struct {
	Hits []Hit
	// QueryTerms are the normalized terms the ranking used, sorted.
	QueryTerms []string
	// Documents is the number of indexed documents searched.
	Documents int
	// Candidates is the number of documents that matched at least one term.
	Candidates int
	// Truncated reports that more candidates matched than the page returned.
	Truncated bool
}

// Search ranks documents for q using BM25F plus bounded hybrid signals.
//
// The ranking is deterministic: query terms are scored in sorted order,
// posting lists are path-sorted, and ties break on the path. A graph proximity
// signal can reorder relevant documents around each other, but its maximum
// contribution is below the exact-identifier signal, so a globally central
// file cannot outrank the exact answer.
func (idx *Index) Search(q Query) (Result, error) {
	queryWeights, exactTerms := queryTerms(q.Text, idx.opts.MaxQueryTerms)
	if len(queryWeights) == 0 {
		return Result{}, ErrEmptyQuery
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > idx.opts.MaxResults {
		limit = idx.opts.MaxResults
	}
	scope := cleanScope(q.Scope)

	terms := make([]string, 0, len(queryWeights))
	for term := range queryWeights {
		terms = append(terms, term)
	}
	sort.Strings(terms)

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	documents := len(idx.docs)
	if documents == 0 {
		return Result{QueryTerms: terms}, nil
	}
	var average [FieldCount]float64
	for field := Field(0); field < FieldCount; field++ {
		average[field] = float64(idx.total[field]) / float64(documents)
		if average[field] < 1 {
			average[field] = 1
		}
	}

	type accumulation struct {
		bm25    float64
		matches []string
	}
	acc := make(map[string]*accumulation, 64)
	for _, term := range terms {
		list, ok := idx.postings[term]
		if !ok {
			continue
		}
		frequency := float64(idx.df[term])
		inverse := math.Log(1 + (float64(documents)-frequency+0.5)/(frequency+0.5))
		if inverse < 0 {
			inverse = 0
		}
		weight := float64(queryWeights[term])
		for _, entry := range list {
			doc := idx.docs[entry.id]
			if !candidate(doc, q, scope) {
				continue
			}
			combined := 0.0
			for field := Field(0); field < FieldCount; field++ {
				count := float64(entry.tf[field])
				if count == 0 {
					continue
				}
				normalized := count / (1 - idx.opts.B + idx.opts.B*float64(doc.Length[field])/average[field])
				combined += idx.opts.Weights[field] * normalized
			}
			if combined <= 0 {
				continue
			}
			termScore := inverse * combined * (idx.opts.K1 + 1) / (combined + idx.opts.K1)
			current, ok := acc[entry.id]
			if !ok {
				current = &accumulation{}
				acc[entry.id] = current
			}
			current.bm25 += termScore * weight
			current.matches = append(current.matches, term)
		}
	}

	hits := make([]Hit, 0, len(acc))
	for rel, current := range acc {
		doc := idx.docs[rel]
		hit := Hit{
			Path:      doc.Path,
			BM25:      current.bm25,
			Matches:   current.matches,
			Language:  doc.Language,
			Kind:      doc.Kind,
			Test:      doc.Test,
			Generated: doc.Generated,
			Truncated: doc.Truncated,
		}
		for _, term := range exactTerms {
			if doc.Terms[term][FieldIdentifier] > 0 {
				hit.Exact = true
			}
			if doc.Terms[term][FieldPath] > 0 {
				hit.PathMatch = true
			}
		}
		if q.Proximity != nil {
			hit.Proximity = clamp01(q.Proximity(rel))
		}
		multiplier := 1.0
		if hit.Exact {
			multiplier += 0.35
		}
		if hit.PathMatch {
			multiplier += 0.20
		}
		multiplier += 0.20 * hit.Proximity
		if hit.Test {
			multiplier *= 0.85
		}
		if hit.Generated {
			multiplier *= 0.50
		}
		hit.Score = current.bm25 * multiplier
		sort.Strings(hit.Matches)
		hits = append(hits, hit)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	candidates := len(hits)
	truncated := candidates > limit
	if truncated {
		hits = hits[:limit]
	}
	return Result{
		Hits:       hits,
		QueryTerms: terms,
		Documents:  documents,
		Candidates: candidates,
		Truncated:  truncated,
	}, nil
}

// candidate applies classification and scope filters.
func candidate(doc Document, q Query, scope string) bool {
	if doc.Test && !q.IncludeTests {
		return false
	}
	if doc.Kind == "documentation" && !q.IncludeDocs {
		return false
	}
	if scope == "" {
		return true
	}
	return doc.Path == scope || strings.HasPrefix(doc.Path, scope+"/")
}

// cleanScope normalizes a directory or file selector.
func cleanScope(scope string) string {
	scope = strings.TrimSpace(strings.ReplaceAll(scope, "\\", "/"))
	scope = strings.TrimPrefix(scope, "./")
	scope = strings.TrimSuffix(scope, "/")
	if scope == "." {
		return ""
	}
	return scope
}

// isQuerySeparator reports a rune that ends a query word.
func isQuerySeparator(r rune) bool {
	switch r {
	case '_', '-', '.', ':', '/':
		return false
	}
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	if math.IsNaN(value) {
		return 0
	}
	return value
}

// queryStopwords are English function words that carry no retrieval signal in
// a natural-language question. They are dropped from the query only; the index
// keeps whatever occurrences the source contains, because removing terms from
// documents would change the corpus statistics.
var queryStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"by": {}, "can": {}, "do": {}, "does": {}, "for": {}, "from": {}, "how": {},
	"i": {}, "if": {}, "in": {}, "into": {}, "is": {}, "it": {}, "its": {},
	"me": {}, "my": {}, "of": {}, "on": {}, "or": {}, "our": {}, "should": {},
	"so": {}, "that": {}, "the": {}, "their": {}, "them": {}, "then": {},
	"there": {}, "these": {}, "they": {}, "this": {}, "to": {}, "was": {},
	"we": {}, "were": {}, "what": {}, "when": {}, "where": {}, "which": {},
	"who": {}, "why": {}, "will": {}, "with": {}, "would": {}, "you": {},
}

// queryTerms normalizes a query into weighted terms and the exact full words
// used by hybrid boosts.
//
// Full identifier words are emitted alongside their parts, so a query for
// HandleRequest scores both the compound term (exact-signal) and handle and
// request. Duplicate query terms are capped at three repetitions, which keeps
// "auth auth auth auth" from becoming an unbounded weight.
func queryTerms(text string, max int) (map[string]int, []string) {
	if len(text) > MaxQueryChars*4 {
		text = text[:MaxQueryChars*4]
	}
	words := strings.FieldsFunc(text, isQuerySeparator)
	weights := make(map[string]int, 16)
	exact := make([]string, 0, 8)
	seenExact := make(map[string]struct{}, 8)
	add := func(token string) {
		token = normalize(token)
		if token == "" || len(token) > MaxTokenLength || !hasLetter(token) {
			return
		}
		if _, stop := queryStopwords[token]; stop {
			return
		}
		if len(weights) >= max {
			if _, ok := weights[token]; !ok {
				return
			}
		}
		if weights[token] < 3 {
			weights[token]++
		}
	}
	for _, word := range words {
		trimmed := strings.Trim(word, "-.:")
		if trimmed == "" {
			continue
		}
		parts := SplitIdentifier(trimmed)
		if len(parts) == 0 {
			add(trimmed)
			continue
		}
		for _, part := range parts {
			add(part)
		}
		whole := normalize(trimmed)
		if len(whole) <= MaxTokenLength {
			add(whole)
			if _, ok := seenExact[whole]; !ok && len(whole) >= 3 {
				seenExact[whole] = struct{}{}
				exact = append(exact, whole)
			}
		}
	}
	if len(exact) > 4 {
		exact = exact[:4]
	}
	return weights, exact
}
