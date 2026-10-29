// Package tokens estimates token counts for prompt budgeting.
//
// The original implementation counts exactly by delegating to a native
// tokenizer per model family, and falls back to a heuristic when the addon is
// absent or the encoding unknown. This package provides that fallback with the
// same documented semantics, plus an interface a real byte-pair encoder can
// satisfy without changing any caller.
//
// An estimate is never presented as exact. `Exact()` reporting false is what
// stops a caller treating the number as ground truth.
package tokens

import (
	"strings"
	"unicode/utf8"
)

// Mode selects the counting strategy.
type Mode string

// Counting modes, matching the original's documented behaviour.
const (
	// ModeEstimate is a bytes-per-token heuristic. It is the cheap default.
	ModeEstimate Mode = "estimate"
	// ModeUpperBound is the raw byte length, which never undercounts. Use it
	// where an undercount causes a provider 400 rather than an early
	// compaction.
	ModeUpperBound Mode = "upperbound"
)

// Count returns the token count for text under the given mode.
//
// The estimate rounds up rather than down, so it is not optimistic by
// construction.
func Count(text string, mode Mode) int {
	switch mode {
	case ModeUpperBound:
		return len(text)
	default:
		return (len(text) + 3) / 4
	}
}

// Tokenizer counts tokens for one model family.
//
// A nil Tokenizer is valid and falls back to the heuristic.
type Tokenizer interface {
	// Count returns the token count for text.
	Count(text string) int
	// Exact reports whether the count is exact rather than estimated.
	Exact() bool
}

// HeuristicTokenizer counts with the bytes-per-token estimate.
//
// It is the default because no provider publishes a tokenizer this binary can
// run, and pretending otherwise would make budgeting silently wrong.
type HeuristicTokenizer struct {
	// bytesPerToken scales the estimate. 4 is the original's value and is
	// roughly right for English; it undercounts dense source code, which is
	// why it is used only as a floor and never as a guarantee.
	bytesPerToken int
}

// NewHeuristicTokenizer returns the default estimator.
func NewHeuristicTokenizer() *HeuristicTokenizer {
	return &HeuristicTokenizer{bytesPerToken: 4}
}

// Count implements Tokenizer.
func (t *HeuristicTokenizer) Count(text string) int {
	per := t.bytesPerToken
	if per <= 0 {
		per = 4
	}
	return (len(text) + per - 1) / per
}

// Exact implements Tokenizer.
func (t *HeuristicTokenizer) Exact() bool { return false }

// DefaultTokenizer is the estimator used when none is configured.
var DefaultTokenizer = NewHeuristicTokenizer()

// CountWith counts using tokenizer, falling back to the heuristic when nil.
func CountWith(tokenizer Tokenizer, text string) int {
	if tokenizer == nil {
		return DefaultTokenizer.Count(text)
	}
	return tokenizer.Count(text)
}

// CountBlocks counts a set of text blocks and adds wire framing.
//
// The count covers everything a provider would bill: content text plus the
// per-message and per-block wrappers. Under-counting here defers compaction
// until the provider rejects the request, which is the failure mode this
// function exists to prevent.
func CountBlocks(texts []string, mode Mode) int {
	const (
		perMessage = 4
		perBlock   = 3
	)
	total := perMessage
	for _, text := range texts {
		total += perBlock + Count(text, mode)
	}
	return total
}

// Percent renders a count as a percentage of the window.
//
// A window of zero renders 0 rather than dividing by zero: a model with no
// published window must not break a status line.
func Percent(count, window int) int {
	if window <= 0 {
		return 0
	}
	return count * 100 / window
}

// TruncateToBudget cuts text so it fits a token budget.
//
// It cuts on a rune boundary so the result is never invalid UTF-8, and marks
// the cut so a consumer can tell the text was truncated.
func TruncateToBudget(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if Count(text, ModeEstimate) <= budget {
		return text
	}
	limit := budget * 4
	if limit > len(text) {
		limit = len(text)
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return strings.TrimSpace(text[:limit]) + "…"
}
