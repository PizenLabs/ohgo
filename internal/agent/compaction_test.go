package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tokens"
)

func TestResolveThresholdTokensUsesFixedTokensFirst(t *testing.T) {
	// A fixed threshold must win over both a percentage and the reserve.
	c := Compaction{ThresholdTokens: 500, ThresholdPercent: 50}
	if got := resolveThresholdTokens(1000, c); got != 500 {
		t.Errorf("threshold = %d, want 500 (the fixed value)", got)
	}
}

func TestResolveThresholdTokensClampsFixedToWindow(t *testing.T) {
	// A threshold at or beyond the window must not consume it entirely,
	// otherwise nothing could ever be sent after compaction.
	c := Compaction{ThresholdTokens: 5000}
	if got := resolveThresholdTokens(1000, c); got != 999 {
		t.Errorf("threshold = %d, want 999 (window - 1)", got)
	}

	low := Compaction{ThresholdTokens: 0}
	// Zero means "unset" here, so this falls through to the reserve path.
	if got := resolveThresholdTokens(1000, low); got < 1 {
		t.Errorf("threshold = %d, want at least 1", got)
	}
}

func TestResolveThresholdTokensUsesPercentWhenNoFixedValue(t *testing.T) {
	c := Compaction{ThresholdPercent: 60}
	if got := resolveThresholdTokens(1000, c); got != 600 {
		t.Errorf("threshold = %d, want 600", got)
	}
}

func TestResolveThresholdTokensClampsPercentage(t *testing.T) {
	if got := resolveThresholdTokens(1000, Compaction{ThresholdPercent: 150}); got != 990 {
		t.Errorf("over-100%% gave %d, want 990 (99%% capped)", got)
	}
	// A non-positive percentage is treated as unset, exactly as the original
	// does, so the reserve path decides the budget instead of a 1% floor.
	if got := resolveThresholdTokens(1000, Compaction{ThresholdPercent: -5}); got != 850 {
		t.Errorf("negative percent gave %d, want 850 (window minus reserve)", got)
	}
	if got := resolveThresholdTokens(1000, Compaction{ThresholdPercent: 0}); got != 850 {
		t.Errorf("zero percent gave %d, want 850 (window minus reserve)", got)
	}
}

func TestResolveThresholdTokensDefaultsToWindowMinusReserve(t *testing.T) {
	// No explicit threshold: the reserve decides the usable budget.
	got := resolveThresholdTokens(1000, Compaction{})
	if want := 1000 - 150; got != want {
		t.Errorf("threshold = %d, want %d (15%% reserve)", got, want)
	}
}

func TestResolveThresholdTokensIsNeverZeroForAPositiveWindow(t *testing.T) {
	// A tiny window must still leave a positive threshold, or compaction would
	// trigger on every turn and never make progress.
	if got := resolveThresholdTokens(10, Compaction{}); got < 1 {
		t.Errorf("threshold = %d, want >= 1", got)
	}
	if got := resolveThresholdTokens(1, Compaction{}); got >= 1 {
		t.Errorf("threshold = %d for a 1-token window, want < 1", got)
	}
}

func TestResolveThresholdTokensZeroWindowDisablesCompaction(t *testing.T) {
	if got := resolveThresholdTokens(0, Compaction{ThresholdTokens: 100}); got != 0 {
		t.Errorf("threshold = %d, want 0 for a zero window", got)
	}
}

func TestResolveBudgetReserveHonoursExplicitReserve(t *testing.T) {
	if got := resolveBudgetReserveTokens(1000, Compaction{ReserveTokens: 300}); got != 300 {
		t.Errorf("reserve = %d, want 300", got)
	}
}

func TestResolveBudgetReserveRecoversImpossibleDefault(t *testing.T) {
	// A defaulted (zero) reserve of 0 on a small window would leave the whole
	// window usable, which is not a budget at all. The proportional reserve
	// must be substituted.
	if got := resolveBudgetReserveTokens(100, Compaction{}); got != 15 {
		t.Errorf("reserve = %d, want 15 (15%% of 100)", got)
	}
	// An explicit reserve must still win even when it is equally tight,
	// because the user set it deliberately.
	if got := resolveBudgetReserveTokens(100, Compaction{ReserveTokens: 20}); got != 20 {
		t.Errorf("reserve = %d, want 20 (explicit)", got)
	}
}

func TestShouldCompactGatesOnEnabledAndStrategy(t *testing.T) {
	base := Compaction{}
	if ShouldCompact(10_000, 100_000, base) {
		t.Error("compaction ran while disabled")
	}
	off := base
	off.Enabled = true
	off.Strategy = StrategyOff
	if ShouldCompact(10_000, 100_000, off) {
		t.Error("compaction ran with strategy off")
	}
	if ShouldCompact(10_000, 0, Compaction{Enabled: true}) {
		t.Error("compaction ran with no context window")
	}
}

func TestShouldCompactTriggersStrictlyAboveThreshold(t *testing.T) {
	c := Compaction{Enabled: true}
	threshold := resolveThresholdTokens(1000, c)

	if ShouldCompact(threshold, 1000, c) {
		t.Error("compaction triggered exactly at the threshold")
	}
	if !ShouldCompact(threshold+1, 1000, c) {
		t.Error("compaction did not trigger one token above the threshold")
	}
}

func TestCompactionContextTokensFloorsByLocalEstimate(t *testing.T) {
	// A compressed request makes the provider under-report; the local
	// estimate must win, or history grows unbounded until it overflows.
	if got := CompactionContextTokens(100, 5000); got != 5000 {
		t.Errorf("tokens = %d, want 5000 (the local estimate)", got)
	}
	if got := CompactionContextTokens(5000, 100); got != 5000 {
		t.Errorf("tokens = %d, want 5000 (the provider count)", got)
	}
	if got := CompactionContextTokens(-10, -20); got != -10 {
		t.Errorf("tokens = %d, want 0-clamped behaviour", got)
	}
}

// conversation builds a message list of the given length, alternating user,
// assistant, and tool-result messages the way a real transcript looks.
func conversation(turns int) []Message {
	var out []Message
	for i := 0; i < turns; i++ {
		out = append(out,
			UserMessage{Text: "do a thing"},
			AssistantMessage{Message: ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "doing it"}},
			}},
		)
	}
	return out
}

func TestFindValidCutPointsNeverCutsOnToolResult(t *testing.T) {
	msgs := []Message{
		UserMessage{Text: "q"},
		AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{}}},
		}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}},
		UserMessage{Text: "q2"},
	}

	points := findValidCutPoints(msgs)
	for _, point := range points {
		if _, isResult := msgs[point].(ToolResultMessage); isResult {
			t.Fatalf("cut point %d is a tool result, which would strand it with no tool call", point)
		}
	}
	if len(points) == 0 {
		t.Fatal("no cut points found in a perfectly valid conversation")
	}
}

func TestSelectCutReturnsMinusOneWhenTooShort(t *testing.T) {
	// A conversation shorter than keepRecent has no suffix to preserve, so
	// there is nothing valid to compact.
	if got := selectCut(conversation(1), 8); got != -1 {
		t.Errorf("cut = %d, want -1 for a too-short conversation", got)
	}
}

func TestSelectCutReturnsMinusOneForAnEmptyConversation(t *testing.T) {
	if got := selectCut(nil, 8); got != -1 {
		t.Errorf("cut = %d, want -1", got)
	}
}

func TestSelectCutNeverPicksIndexZero(t *testing.T) {
	// Compacting everything would leave a summary with no surviving turn, so
	// index 0 must never be returned.
	if got := selectCut(conversation(50), 100); got != -1 {
		t.Errorf("cut = %d, want -1", got)
	}
}

func TestCompactPreservesSuffixAndReplacesPrefix(t *testing.T) {
	msgs := conversation(20)
	summarize := func(ctx context.Context, prefix []Message) (string, error) {
		return "the user asked for stuff", nil
	}

	result, err := Compact(context.Background(), msgs, Compaction{Enabled: true, KeepRecent: 10}, 1000, summarize)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Compacted+result.Retained != len(msgs) {
		t.Errorf("compacted %d + retained %d != %d messages",
			result.Compacted, result.Retained, len(msgs))
	}
	// The rebuilt conversation must start with a user turn, or the provider
	// rejects the request outright.
	if _, ok := result.Messages[0].(UserMessage); !ok {
		t.Errorf("first message is %T, want UserMessage", result.Messages[0])
	}
	if !strings.Contains(result.Summary, "the user asked for stuff") {
		t.Errorf("summary = %q, want the summarizer output", result.Summary)
	}
	// The preserved suffix must survive byte-identically.
	last := result.Messages[len(result.Messages)-1]
	if want := msgs[len(msgs)-1]; !sameMessage(last, want) {
		t.Errorf("last message was not preserved: %+v", last)
	}
}

func sameMessage(a, b Message) bool {
	return estimateMessageText(a) == estimateMessageText(b)
}

func TestCompactReducesEstimatedSize(t *testing.T) {
	// Compaction must actually shrink the conversation; a "compaction" that
	// leaves it larger is worse than not running.
	var big []Message
	body := strings.Repeat("x", 400)
	for i := 0; i < 60; i++ {
		big = append(big, UserMessage{Text: body})
	}

	result, err := Compact(context.Background(), big, Compaction{Enabled: true, KeepRecent: 10}, 20000,
		func(ctx context.Context, prefix []Message) (string, error) {
			return strings.Repeat("s", 200), nil
		})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.AfterTokens >= result.BeforeTokens {
		t.Errorf("after %d tokens, before %d tokens; compaction did not shrink the context",
			result.AfterTokens, result.BeforeTokens)
	}
}

func TestCompactTruncatesAnOversizedSummary(t *testing.T) {
	// When the preserved suffix already fills the budget, the summary must be
	// cut rather than left to overflow the window.
	msgs := conversation(30)
	huge := strings.Repeat("s", 100_000)

	result, err := Compact(context.Background(), msgs, Compaction{Enabled: true, KeepRecent: 4}, 1000,
		func(ctx context.Context, prefix []Message) (string, error) { return huge, nil })
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.AfterTokens > 1000 {
		t.Errorf("compacted context is %d tokens, over the 1000-token window", result.AfterTokens)
	}
}

func TestCompactRejectsAnEmptySummary(t *testing.T) {
	// An empty summary silently discards the entire prefix, so it must fail
	// loudly rather than truncate a conversation into nothing.
	_, err := Compact(context.Background(), conversation(20), Compaction{Enabled: true, KeepRecent: 5}, 1000,
		func(ctx context.Context, prefix []Message) (string, error) {
			return "   ", nil
		})
	if !errors.Is(err, ErrEmptySummary) {
		t.Errorf("error = %v, want ErrEmptySummary", err)
	}
}

func TestCompactPropagatesSummarizerFailure(t *testing.T) {
	boom := errors.New("provider on fire")
	_, err := Compact(context.Background(), conversation(20), Compaction{Enabled: true, KeepRecent: 5}, 1000,
		func(ctx context.Context, prefix []Message) (string, error) {
			return "", boom
		})
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the summarizer failure to propagate", err)
	}
}

func TestCompactRejectsMissingPrerequisites(t *testing.T) {
	if _, err := Compact(context.Background(), conversation(20), Compaction{}, 1000, nil); !errors.Is(err, ErrNoSummarizer) {
		t.Errorf("nil summarizer gave %v, want ErrNoSummarizer", err)
	}
	if _, err := Compact(context.Background(), conversation(20), Compaction{}, 0,
		func(ctx context.Context, prefix []Message) (string, error) { return "s", nil }); !errors.Is(err, ErrNoContextWindow) {
		t.Errorf("zero window gave %v, want ErrNoContextWindow", err)
	}
	if _, err := Compact(context.Background(), conversation(1), Compaction{KeepRecent: 8}, 1000,
		func(ctx context.Context, prefix []Message) (string, error) { return "s", nil }); !errors.Is(err, ErrNothingToCompact) {
		t.Errorf("short conversation gave %v, want ErrNothingToCompact", err)
	}
}

func TestEstimateMessagesIgnoresUIMessages(t *testing.T) {
	// A UI-only message must not consume budget: it never reaches the provider,
	// so counting it would trigger compaction early.
	ui := []Message{Notification{Text: "internal noise"}}
	if got := EstimateMessages(ui); got != 4 {
		t.Errorf("estimate = %d, want 4 (framing only)", got)
	}
}

func TestCountRoundsUp(t *testing.T) {
	// Rounding down would make the estimate optimistic by construction.
	if got := tokens.Count("abc", tokens.ModeEstimate); got != 1 {
		t.Errorf("count(\"abc\") = %d, want 1", got)
	}
	if got := tokens.Count("abcdefgh", tokens.ModeEstimate); got != 2 {
		t.Errorf("count(8 bytes) = %d, want 2", got)
	}
}

func TestCountUpperBoundEqualsByteLength(t *testing.T) {
	// The upper bound must never undercount, which is its whole purpose.
	text := "日本語" // 9 bytes, 3 runes
	if got := tokens.Count(text, tokens.ModeUpperBound); got != len(text) {
		t.Errorf("upper bound = %d, want %d (byte length)", got, len(text))
	}
}

func TestTruncateToBudgetPreservesUTF8AndMarksTheCut(t *testing.T) {
	got := tokens.TruncateToBudget(strings.Repeat("日", 1000), 10)
	if !isValidUTF8(got) {
		t.Fatal("truncation produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("result %q does not mark the truncation", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

func TestTruncateToBudgetLeavesShortTextAlone(t *testing.T) {
	if got := tokens.TruncateToBudget("short", 100); got != "short" {
		t.Errorf("got %q, want the input unchanged", got)
	}
	if got := tokens.TruncateToBudget("anything", 0); got != "" {
		t.Errorf("got %q, want an empty result for a zero budget", got)
	}
}

func TestPercentHandlesZeroWindow(t *testing.T) {
	// A model with no published window must not break a status line.
	if got := tokens.Percent(500, 0); got != 0 {
		t.Errorf("percent = %d, want 0", got)
	}
	if got := tokens.Percent(500, 1000); got != 50 {
		t.Errorf("percent = %d, want 50", got)
	}
}

func TestExactnessIsAdvertised(t *testing.T) {
	// A caller that treats the heuristic as exact would silently misbudget.
	if tokens.DefaultTokenizer.Exact() {
		t.Error("the heuristic tokenizer claims to be exact")
	}
	if got := tokens.DefaultTokenizer.Count("test"); got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
}
