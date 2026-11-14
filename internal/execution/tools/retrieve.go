package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/retrieval"
)

// Retrieval page and output bounds. They are runtime constants, never
// model-controlled.
const (
	// RetrieveDefaultLimit is the page size when the caller does not choose one.
	RetrieveDefaultLimit = 10
	// RetrieveMaxLimit is the hard ceiling on one page.
	RetrieveMaxLimit = 20
	// RetrieveMaxOutputChars bounds the rendered answer.
	RetrieveMaxOutputChars = 8000
)

// RetrieveTool ranks repository files by relevance to a task query.
//
// It is the lexical retrieval capability: Okapi BM25F over paths, identifiers,
// and comments, with exact-identifier reranking. It is read-only and never
// claims that a high score is proof of implementation; a consumer still reads
// the file or resolves it semantically.
type RetrieveTool struct {
	Base

	// Root bounds every path selector.
	Root string
	// source is the shared repository snapshot the index is reconciled with.
	source *StructuralSource
	// workspace is the shared live index: the investigation orchestrator
	// reads the same state instead of building a second corpus.
	workspace *retrieval.Workspace
}

// NewRetrieveTool returns the retrieval capability over source.
//
// cacheDir is machine-scoped persistence supplied by composition; empty keeps
// the index in memory only.
func NewRetrieveTool(source *StructuralSource, cacheDir string) *RetrieveTool {
	root := ""
	if source != nil {
		root = source.Root()
	}
	tool := &RetrieveTool{
		Root:      root,
		source:    source,
		workspace: retrieval.NewWorkspace(root, cacheDir, retrieval.DefaultOptions()),
	}
	tool.Base = NewBase("retrieve",
		"Rank repository files by relevance to a task query before reading them. "+
			"Uses Okapi BM25F over path, identifier, and comment terms with an "+
			"exact-identifier rerank. Returns bounded file:path candidates with "+
			"score, matched terms, and whether the match was an exact identifier "+
			"or a path match. Tests and documentation are opt-in. This is lexical "+
			"relevance evidence, not proof of behaviour: no hit does not prove a "+
			"concept is absent, and a high score does not prove implementation.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":      "string",
					"minLength": 1,
					"maxLength": float64(retrieval.MaxQueryChars),
					"description": "Natural-language question, exact identifier, or " +
						"architecture term. Examples: \"authentication middleware\", " +
						"\"RotateSessionSecret\", \"how checkout charges a card\".",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     float64(RetrieveMaxLimit),
					"description": "Maximum ranked files to return. Default 10.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional repository-relative directory or file to restrict candidates.",
				},
				"include_tests": map[string]any{
					"type":        "boolean",
					"description": "Include test source in the candidate set. Default false.",
				},
				"include_docs": map[string]any{
					"type":        "boolean",
					"description": "Include documentation files in the candidate set. Default false.",
				},
			},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "rank repository files by relevance"},
	)
	tool.SetActionClasses(agent.ActionObserve, agent.ActionStructure)
	return tool
}

// Workspace returns the shared live index, so the investigation orchestrator
// can rank against the same corpus instead of building a second one.
func (t *RetrieveTool) Workspace() *retrieval.Workspace {
	if t == nil {
		return nil
	}
	return t.workspace
}

// Execute implements agent.Tool.
func (t *RetrieveTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	query, err := strArg(args, "query", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"query\" is required; nothing executed. Pass the task terms to rank files by.")
	}
	if utf8.RuneCountInString(query) > retrieval.MaxQueryChars {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"query\" exceeds %d characters; nothing executed. Shorten the query to its distinctive terms.",
			retrieval.MaxQueryChars)
	}
	limit, err := intArg(args, "limit", RetrieveDefaultLimit)
	if err != nil || limit < 1 || limit > RetrieveMaxLimit {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"limit\" must be between 1 and %d; nothing executed.", RetrieveMaxLimit)
	}
	scope, err := strArg(args, "path", "")
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	scope = strings.TrimSpace(scope)
	if !validRetrievalScope(scope) {
		return requestError(agent.ToolFailureInvalidArgument,
			"argument \"path\" must be a repository-relative directory or file, not an absolute or escaping path; nothing executed.")
	}
	includeTests, err := boolArg(args, "include_tests", false)
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	includeDocs, err := boolArg(args, "include_docs", false)
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing executed.", err)
	}
	if t.source == nil {
		return agent.ErrorResult("retrieval has no repository source; nothing executed.")
	}

	index, err := t.source.Index()
	if err != nil {
		return agent.ErrorResult("retrieval could not open the repository snapshot: %v", err)
	}
	result, stats, err := t.workspace.Search(ctx.Ctx, index, retrieval.Query{
		Text:         query,
		Limit:        limit,
		IncludeTests: includeTests,
		IncludeDocs:  includeDocs,
		Scope:        scope,
	})
	if err != nil {
		return requestError(agent.ToolFailureInvalidArgument, "%v; nothing ranked.", err)
	}

	details := map[string]any{
		"query":        query,
		"terms":        result.QueryTerms,
		"documents":    result.Documents,
		"candidates":   result.Candidates,
		"truncated":    result.Truncated,
		"cache":        string(stats.Cache),
		"cache_reason": stats.CacheReason,
		"bytes_read":   stats.BytesRead,
		"added":        stats.Added,
		"updated":      stats.Updated,
		"removed":      stats.Removed,
		"reused":       stats.Reused,
		"index_issues": stats.Issues,
		"hits":         retrievalHitDetails(result.Hits),
		"evidence":     retrievalEvidence(result, stats),
	}
	if len(result.Hits) == 0 {
		return agent.Result{
			Text: fmt.Sprintf("No indexed document matched %s (%d documents searched, %s cache). "+
				"An empty page is a valid lexical answer, not proof the concept is absent; "+
				"broaden the query or use symbols/graph for structural facts.",
				strings.Join(result.QueryTerms, ", "), result.Documents, stats.Cache),
			Details: details,
			Useless: true,
		}
	}

	text := formatRetrieval(result, stats)
	return agent.Result{Text: text, Details: details}
}

// validRetrievalScope keeps the selector inside the workspace.
func validRetrievalScope(scope string) bool {
	if scope == "" {
		return true
	}
	clean := strings.TrimPrefix(strings.ReplaceAll(scope, "\\", "/"), "./")
	if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return false
	}
	return true
}

// formatRetrieval renders the bounded evidence page.
func formatRetrieval(result retrieval.Result, stats retrieval.SyncStats) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "ranked %d of %d candidates (%d documents, %s cache",
		len(result.Hits), result.Candidates, result.Documents, stats.Cache)
	if stats.BytesRead > 0 {
		fmt.Fprintf(&builder, ", %d bytes read", stats.BytesRead)
	}
	builder.WriteString(")\n")
	for index, hit := range result.Hits {
		fmt.Fprintf(&builder, "%d. %s  score %.3f", index+1, hit.Path, hit.Score)
		if hit.BM25 != hit.Score {
			fmt.Fprintf(&builder, " (bm25 %.3f)", hit.BM25)
		}
		builder.WriteString("  terms: ")
		builder.WriteString(strings.Join(hit.Matches, ", "))
		var signals []string
		if hit.Exact {
			signals = append(signals, "exact identifier")
		}
		if hit.PathMatch {
			signals = append(signals, "path match")
		}
		if hit.Test {
			signals = append(signals, "test")
		}
		if hit.Generated {
			signals = append(signals, "generated")
		}
		if len(signals) > 0 {
			builder.WriteString("  [")
			builder.WriteString(strings.Join(signals, ", "))
			builder.WriteString("]")
		}
		builder.WriteString("\n")
	}
	if result.Truncated {
		builder.WriteString("more candidates matched than this page; narrow the query or scope it with path.\n")
	}
	if len(stats.Issues) > 0 {
		builder.WriteString("index issues: ")
		builder.WriteString(strings.Join(stats.Issues, "; "))
		builder.WriteString("\n")
	}
	return capRetrievalOutput(builder.String())
}

// capRetrievalOutput trims at a rune boundary.
func capRetrievalOutput(text string) string {
	if len(text) <= RetrieveMaxOutputChars {
		return text
	}
	cut := RetrieveMaxOutputChars
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… output truncated; lower the limit or narrow the query.\n"
}

func retrievalHitDetails(hits []retrieval.Hit) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		out = append(out, map[string]any{
			"path":       hit.Path,
			"score":      hit.Score,
			"bm25":       hit.BM25,
			"terms":      append([]string(nil), hit.Matches...),
			"language":   hit.Language,
			"kind":       hit.Kind,
			"test":       hit.Test,
			"generated":  hit.Generated,
			"exact":      hit.Exact,
			"path_match": hit.PathMatch,
		})
	}
	return out
}

// retrievalEvidence publishes the observation contract: what was searched, how
// fresh the index was, and what was not claimed.
func retrievalEvidence(result retrieval.Result, stats retrieval.SyncStats) map[string]any {
	state := "OBSERVED"
	note := "BM25 lexical relevance over the current snapshot; not a semantic or proof claim"
	if result.Documents == 0 {
		state = "UNKNOWN"
		note = "the retrieval index is empty; absence is not evidence"
	}
	return map[string]any{
		"state":      state,
		"provider":   "retrieval",
		"snapshot":   "current at reconcile time",
		"cache":      string(stats.Cache),
		"documents":  result.Documents,
		"candidates": result.Candidates,
		"note":       note,
	}
}
