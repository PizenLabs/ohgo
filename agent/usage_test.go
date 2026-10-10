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
