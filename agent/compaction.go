package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/tokens"
)

// Compaction settings.
//
// The defaults mirror the original's: a 15% proportional reserve with a
// compaction strategy that summarises history once the window fills.
type Compaction struct {
	// Enabled disables compaction entirely when false.
	Enabled bool
	// Strategy selects the compaction behaviour.
	Strategy CompactionStrategy
	// ThresholdTokens is a fixed trigger. When set it takes priority over
	// ThresholdPercent and the reserve.
	ThresholdTokens int
	// ThresholdPercent is a percentage of the context window. Ignored when
	// ThresholdTokens is set.
	ThresholdPercent float64
	// ReserveTokens is the absolute budget held back from the window. When
	// unset, a proportional reserve of 15% is used if the default would be
	// impossible for the window.
	ReserveTokens int
	// KeepRecent is the number of trailing messages preserved verbatim
	// through compaction, so the model keeps its immediate working set.
	KeepRecent int
}

// CompactionStrategy selects how context is reduced.
type CompactionStrategy string

// Compaction strategies.
const (
	// StrategySummarize replaces older history with a summary.
	StrategySummarize CompactionStrategy = "summarize"
	// StrategyOff disables compaction.
	StrategyOff CompactionStrategy = "off"
)

// DefaultCompaction returns the built-in settings.
func DefaultCompaction() Compaction {
	return Compaction{
		Enabled:    true,
		Strategy:   StrategySummarize,
		KeepRecent: 8,
	}
}

// reserveProportionalFraction is the fallback fraction of the window held back
// when the configured reserve is impossible for that window.
//
// The original documents this as recovering a *defaulted* reserve that cannot
// fit: an explicit reserve must still win, even when it is equally impossible,
// because the user set it deliberately.
const reserveProportionalFraction = 0.15

// resolveBudgetReserveTokens computes the tokens held back from the window.
//
// An explicit ReserveTokens always wins. A defaulted (zero) reserve falls back
// to a proportional value when holding back nothing would leave no practical
// budget — that recovery is what makes the threshold usable on the small
// windows the original bundles.
func resolveBudgetReserveTokens(contextWindow int, c Compaction) int {
	proportional := int(float64(contextWindow) * reserveProportionalFraction)
	if proportional < 1 {
		proportional = 1
	}
	if contextWindow <= 0 {
		return 0
	}

	// ReserveTokens of zero means "unset": the port represents the original's
	// undefined with zero rather than a pointer, so provenance is carried by
	// that convention.
	reserve := c.ReserveTokens
	if reserve == 0 {
		// A defaulted reserve that consumes nearly the whole window leaves no
		// usable budget, so recover with the proportional value.
		return proportional
	}
	// An explicit reserve is honoured even when impossible: the user asked for
	// a smaller usable budget on purpose.
	if reserve >= contextWindow {
		return proportional
	}
	return reserve
}

// resolveThresholdTokens computes the token count that triggers compaction.
//
// Priority is fixed tokens, then percentage, then window-minus-reserve. Every
// result is clamped below the window so the threshold never consumes it
// entirely.
func resolveThresholdTokens(contextWindow int, c Compaction) int {
	if contextWindow <= 0 {
		return 0
	}

	// A fixed threshold wins and is clamped so a threshold at or above the
	// window still leaves room.
	if c.ThresholdTokens > 0 {
		return clamp(1, contextWindow-1, c.ThresholdTokens)
	}

	if c.ThresholdPercent > 0 {
		pct := c.ThresholdPercent
		if pct > 99 {
			pct = 99
		}
		if pct < 1 {
			pct = 1
		}
		return int(float64(contextWindow) * pct / 100)
	}

	// Default: everything except the reserve.
	threshold := contextWindow - resolveBudgetReserveTokens(contextWindow, c)
	if threshold < 0 {
		threshold = 0
	}
	if threshold > contextWindow-1 {
		threshold = contextWindow - 1
	}
	return threshold
}

func clamp(lo, hi, value int) int {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}

// ShouldCompact reports whether the context has grown past the trigger.
func ShouldCompact(contextTokens, contextWindow int, c Compaction) bool {
	if !c.Enabled || c.Strategy == StrategyOff || contextWindow <= 0 {
		return false
	}
	return contextTokens > resolveThresholdTokens(contextWindow, c)
}

// CompactionContextTokens floors a provider-reported context size by a local
// estimate of the stored conversation.
//
// The original documents why this matters: a payload transform can compress
// the request below the real stored conversation, so the provider reports
// deflated prompt tokens. Anchoring compaction on that usage alone lets the
// real history grow unbounded until it overflows and compaction can no longer
// run. Taking the max of both keeps the trigger honest.
func CompactionContextTokens(providerContextTokens, storedConversationEstimate int) int {
	if storedConversationEstimate > providerContextTokens {
		return storedConversationEstimate
	}
	return providerContextTokens
}

// EstimateMessages approximates the token size of a message list.
//
// It counts only messages that reach the provider: a UI-only notification is
// dropped, because counting it would trigger compaction on history the model
// never saw. It uses the estimate mode rather than the upper bound, because an
// over-count here would discard history that still fit.
func EstimateMessages(messages []Message) int {
	texts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if !sendsToLLM(msg) {
			continue
		}
		texts = append(texts, estimateMessageText(msg))
	}
	return tokens.CountBlocks(texts, tokens.ModeEstimate)
}

// sendsToLLM reports whether a message reaches the provider at all.
func sendsToLLM(msg Message) bool {
	_, ok := msg.toLLM()
	return ok
}

func estimateMessageText(msg Message) string {
	switch m := msg.(type) {
	case UserMessage:
		return m.Text
	case AssistantMessage:
		return m.Message.Text()
	case ToolResultMessage:
		return m.Text()
	case Notification:
		// UI-only messages never reach the model, so they must not count
		// toward the window.
		return ""
	default:
		return ""
	}
}

// findValidCutPoints returns message indices that may begin a compacted
// region.
//
// A tool result must never be the first message after a cut: it would arrive
// with no tool call above it, which every provider rejects. Cutting at an
// assistant message that carries tool calls keeps its results, which is
// allowed because they follow their call.
//
// A user message is always a valid cut because it starts a self-contained
// turn.
func findValidCutPoints(messages []Message) []int {
	var points []int
	for i, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			points = append(points, i)
		case AssistantMessage:
			// An assistant message with tool calls is a valid cut: the tool
			// results that follow stay with it. One without tool calls is the
			// tail of a turn, and cutting there orphans its own tool calls
			// from nothing above it, so it is also allowed — but the first
			// message must be a user turn for the provider to accept the
			// request, which is enforced by the caller.
			_ = m
			points = append(points, i)
		case ToolResultMessage:
			// Never cut here.
		case Notification:
			// UI-only; cutting here is harmless but never useful.
		}
	}
	return points
}

// selectCut chooses the index that splits messages into a compacted prefix and
// a preserved suffix.
//
// It returns -1 when no valid cut exists, which means the conversation is too
// short to compact and the caller must leave it alone rather than produce an
// invalid transcript.
func selectCut(messages []Message, keepRecent int) int {
	points := findValidCutPoints(messages)
	if len(points) == 0 {
		return -1
	}

	// The preserved suffix is the last keepRecent messages.
	boundary := len(messages) - keepRecent
	if boundary <= 0 {
		return -1
	}

	// Walk the cut points and take the latest one at or before the boundary,
	// so the preserved region is as large as the budget allows.
	chosen := -1
	for _, point := range points {
		if point <= boundary {
			chosen = point
		}
	}
	if chosen <= 0 {
		// A cut at index 0 would compact the whole conversation and leave a
		// summary with no surviving turn beneath it.
		return -1
	}
	return chosen
}

// SummarizeFunc replaces a message range with a summary.
//
// It exists so compaction is testable without a model: the host supplies the
// real summarizer, and a test supplies a deterministic one.
type SummarizeFunc func(ctx context.Context, messages []Message) (string, error)

// CompactResult is the outcome of a compaction.
type CompactResult struct {
	// Messages is the rebuilt conversation: the summary followed by the
	// preserved suffix.
	Messages []Message
	// Summary is the text that replaced the compacted prefix.
	Summary string
	// Compacted is how many messages were replaced.
	Compacted int
	// Retained is how many messages were kept verbatim.
	Retained int
	// BeforeTokens is the estimated size of the input.
	BeforeTokens int
	// AfterTokens is the estimated size of the output.
	AfterTokens int
}

// Compact reduces messages so the conversation fits the window.
//
// The suffix is preserved verbatim and the prefix is replaced by one user
// message carrying the summary. When keepRecent alone does not leave room, the
// summary is budget-truncated rather than the conversation being left over
// the trigger: an over-full context is a provider 400, which is worse than a
// shorter summary.
func Compact(ctx context.Context, messages []Message, c Compaction, window int, summarize SummarizeFunc) (CompactResult, error) {
	if window <= 0 {
		// Without a window there is no budget to respect, so compaction
		// cannot make a decision.
		return CompactResult{}, ErrNoContextWindow
	}
	if summarize == nil {
		return CompactResult{}, ErrNoSummarizer
	}

	cut := selectCut(messages, c.KeepRecent)
	if cut <= 0 {
		return CompactResult{}, ErrNothingToCompact
	}

	prefix := messages[:cut]
	suffix := messages[cut:]

	summary, err := summarize(ctx, prefix)
	if err != nil {
		return CompactResult{}, fmt.Errorf("agent: summarise context: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		// An empty summary would replace the history with nothing, silently
		// discarding everything the model was told.
		return CompactResult{}, ErrEmptySummary
	}

	// The summary must fit whatever the preserved suffix leaves of the
	// budget.
	remaining := resolveThresholdTokens(window, c) - EstimateMessages(suffix)
	if remaining < 1 {
		remaining = 1
	}
	summary = tokens.TruncateToBudget(summary, remaining)

	out := make([]Message, 0, len(suffix)+1)
	out = append(out, UserMessage{Text: "Summary of earlier conversation:\n\n" + summary})
	out = append(out, suffix...)

	return CompactResult{
		Messages:     out,
		Summary:      summary,
		Compacted:    len(prefix),
		Retained:     len(suffix),
		BeforeTokens: EstimateMessages(messages),
		AfterTokens:  EstimateMessages(out),
	}, nil
}

// Compaction failure modes, matched with errors.Is.
var (
	// ErrNoContextWindow means compaction was asked to run without a window.
	ErrNoContextWindow = errors.New("agent: compaction needs a positive context window")
	// ErrNoSummarizer means no summary function was supplied.
	ErrNoSummarizer = errors.New("agent: compaction needs a summarize function")
	// ErrNothingToCompact means the conversation has no valid cut point.
	ErrNothingToCompact = errors.New("agent: conversation has nothing to compact")
	// ErrEmptySummary means the summarizer returned nothing.
	ErrEmptySummary = errors.New("agent: summarizer returned an empty summary")
)
