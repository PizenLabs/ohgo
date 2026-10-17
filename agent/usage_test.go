package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
)

// The usage ledger must be safe for concurrent turns and honest about gaps.

func TestUsageLogAccumulatesTurns(t *testing.T) {
	log := &UsageLog{}
	log.Add([]Turn{
		{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 100, Output: 50, Total: 150}}},
		{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 10, Output: 5, Total: 15}}},
	})
	turns, input, output := log.Snapshot()
	if turns != 2 || input != 110 || output != 55 {
		t.Errorf("snapshot = %d/%d/%d, want 2/110/55", turns, input, output)
	}
}

func TestUsageLogSkipsUnmeasuredTurns(t *testing.T) {
	log := &UsageLog{}
	log.Add([]Turn{{Assistant: ai.AssistantMessage{}}})
	if turns, _, _ := log.Snapshot(); turns != 0 {
		t.Errorf("turns = %d, want 0: a turn with no measurement is not a zero turn", turns)
	}
}

func TestUsageLogValuesAtRecordedSchedule(t *testing.T) {
	log := &UsageLog{}
	log.SetCost(ai.Cost{Input: 3, Output: 15})
	log.Add([]Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 1_000_000, Output: 1_000_000, Total: 2_000_000}}}})
	if got := log.Cost(); got != 18 {
		t.Errorf("cost = %v, want 18", got)
	}
	if report := log.Report(false); !strings.Contains(report, "$18.0000") || !strings.Contains(report, "measured") {
		t.Errorf("report = %q, want measured dollars", report)
	}
}

func TestUsageLogLabelsEstimates(t *testing.T) {
	log := &UsageLog{}
	log.SetCost(ai.Cost{})
	log.Add([]Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 4, Output: 2, Total: 6}}}})
	if report := log.Report(true); !strings.Contains(report, "estimated") {
		t.Errorf("report = %q, want the estimate labeled", report)
	}
}

func TestUsageLogEmptyReportsNoTurns(t *testing.T) {
	if report := (&UsageLog{}).Report(false); !strings.Contains(report, "no turns yet") {
		t.Errorf("report = %q, want the empty state, not zeros", report)
	}
}

// The ledger keeps the most recent measured request separately from the
// running totals, so an inspector can report last-request cache reads and
// writes without re-deriving them from the transcript.
func TestUsageLogKeepsLastRequestAndCacheTotals(t *testing.T) {
	log := &UsageLog{}
	log.Add([]Turn{
		{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 10, Output: 5, CacheRead: 2, CacheWrite: 1, Total: 17}}},
		{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 20, Output: 8, CacheRead: 4, CacheWrite: 3, Total: 32}}},
	})
	turns, input, output, cacheRead, cacheWrite := log.Totals()
	if turns != 2 || input != 30 || output != 13 || cacheRead != 6 || cacheWrite != 4 {
		t.Errorf("totals = %d/%d/%d/%d/%d, want 2/30/13/6/4", turns, input, output, cacheRead, cacheWrite)
	}
	last, ok := log.Last()
	if !ok || last.Input != 20 || last.CacheRead != 4 || last.Total != 32 {
		t.Errorf("last = (%+v, %v), want the newest measured request", last, ok)
	}
}

// An unmeasured turn never becomes the "last request": a silent provider is
// not a zero measurement.
func TestUsageLogSkipsUnmeasuredLastRequest(t *testing.T) {
	log := &UsageLog{}
	log.Add([]Turn{{Assistant: ai.AssistantMessage{}}})
	if _, ok := log.Last(); ok {
		t.Error("a silent turn became the last request")
	}
}

// CostOf prices arbitrary counters against the recorded schedule, letting the
// live session's cost share the ledger's price authority.
func TestUsageLogCostOf(t *testing.T) {
	log := &UsageLog{}
	log.SetCost(ai.Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75})
	if !log.CostKnown() {
		t.Error("a recorded schedule reported unknown")
	}
	want := (1_000_000*3.0 + 1_000_000*15.0 + 1_000_000*0.3 + 1_000_000*3.75) / 1_000_000
	if got := log.CostOf(1_000_000, 1_000_000, 1_000_000, 1_000_000); got != want {
		t.Errorf("CostOf = %v, want %v", got, want)
	}
	if (&UsageLog{}).CostKnown() {
		t.Error("an empty schedule reported known")
	}
}

func TestLastUserTextFindsTheLatestPrompt(t *testing.T) {
	history := []Message{
		UserMessage{Text: "first"},
		AssistantMessage{Message: ai.AssistantMessage{}},
		UserMessage{Text: "second"},
	}
	text, ok := LastUserText(history)
	if !ok || text != "second" {
		t.Errorf("got (%q, %v), want the latest prompt", text, ok)
	}
}

func TestLastUserTextSkipsBlanks(t *testing.T) {
	history := []Message{UserMessage{Text: "  "}}
	if _, ok := LastUserText(history); ok {
		t.Error("a blank prompt is not retryable")
	}
	if _, ok := LastUserText(nil); ok {
		t.Error("empty history is not retryable")
	}
}
