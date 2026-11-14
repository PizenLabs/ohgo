package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// TestMeasureRequestAccountsForEveryBilledComponent defends the predictive
// trigger: a conversation that looks small can overflow once the tool surface
// and completion budget are added.
func TestMeasureRequestAccountsForEveryBilledComponent(t *testing.T) {
	c := DefaultCompaction()
	measure := MeasureRequest(MeasureInput{
		ContextTokens:        900,
		SystemTokens:         100,
		ToolTokens:           100,
		ExpectedOutputTokens: 128,
		Window:               1000,
	}, c)
	if measure.Pressure != PressureCritical {
		t.Fatalf("pressure = %s, want CRITICAL (projected %d of %d)", measure.Pressure, measure.Projected, measure.Window)
	}
	if measure.Projected <= measure.ContextTokens {
		t.Fatalf("projected %d did not include non-context overhead over context %d", measure.Projected, measure.ContextTokens)
	}
	if measure.Headroom != 0 {
		t.Fatalf("headroom = %d, want 0 for an overflowing projection", measure.Headroom)
	}
}

// TestMeasureRequestHonoursLowExplicitTrigger proves a small explicit trigger
// still means "conversation size", not "any request with tools and an output
// budget". Otherwise a 1.5k trigger on a 100k window would compact forever.
func TestMeasureRequestHonoursLowExplicitTrigger(t *testing.T) {
	c := Compaction{Enabled: true, Strategy: StrategySummarize, ThresholdTokens: 1500}
	normal := MeasureRequest(MeasureInput{
		ContextTokens: 100, ExpectedOutputTokens: 8192, Window: 100000,
	}, c)
	if normal.Pressure != PressureNormal {
		t.Fatalf("pressure = %s, want NORMAL for context 100 under a 1500 trigger", normal.Pressure)
	}
	compact := MeasureRequest(MeasureInput{ContextTokens: 2000, Window: 100000}, c)
	if compact.Pressure != PressureCompact {
		t.Fatalf("pressure = %s, want COMPACT for context 2000 over a 1500 trigger", compact.Pressure)
	}
}

// TestTrimMessagesDeduplicatesIdenticalObservations proves deterministic
// trimming replaces an older identical observation without dropping the
// newest, and archives the original text for recall.
func TestTrimMessagesDeduplicatesIdenticalObservations(t *testing.T) {
	recall := NewRecallStore()
	var messages []Message
	for i := range 4 {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages, AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{ID: id, Name: "read", Arguments: map[string]any{"path": "main.go"}}},
		}})
		messages = append(messages, ToolResultMessage{
			ToolCallID: id, ToolName: "read",
			Content: ai.TextBlocks("package main\n" + strings.Repeat("x", 400)),
		})
	}
	c := Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 8, KeepRecentToolResults: 2}
	trimmed, report := TrimMessages(messages, c, recall)
	if report.ToolResultsTrimmed != 2 {
		t.Fatalf("trimmed = %d, want 2 older duplicates", report.ToolResultsTrimmed)
	}
	if recall.Len() != 2 {
		t.Fatalf("archived = %d, want 2 originals", recall.Len())
	}
	for i := range 2 {
		result := trimmed[i*2+1].(ToolResultMessage)
		if !strings.Contains(result.Text(), "trimmed") {
			t.Fatalf("older result %d was not replaced: %q", i, result.Text())
		}
	}
	newest := trimmed[len(trimmed)-1].(ToolResultMessage)
	if strings.Contains(newest.Text(), "trimmed") {
		t.Fatal("the newest observation was trimmed")
	}
}

// TestBoundToolResultsPreservesHeadAndArchives proves size-based trimming is
// truthful: the model keeps the head, the omission is stated, and the full
// payload remains addressable.
func TestBoundToolResultsPreservesHeadAndArchives(t *testing.T) {
	recall := NewRecallStore()
	original := "HEAD-" + strings.Repeat("z", 5000)
	messages := []Message{
		UserMessage{Text: "start"},
		AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"command": "cat big"}},
		}}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "bash", Content: ai.TextBlocks(original)},
	}
	out, report := BoundToolResults(messages, recall, 512)
	if report.ToolResultsTrimmed != 1 {
		t.Fatalf("trimmed = %d, want 1", report.ToolResultsTrimmed)
	}
	text := out[2].(ToolResultMessage).Text()
	if !strings.HasPrefix(text, "[tool output bounded") || !strings.Contains(text, "HEAD-") {
		t.Fatalf("bounded text lost its marker or head: %q", text[:min(120, len(text))])
	}
	item, ok := recall.Get("E1")
	if !ok || item.Text != original {
		t.Fatal("the original payload was not archived intact")
	}
}

// TestCompactWithReconstructsFromDurableState proves the preferred path: the
// rebuilt context is authoritative state plus a bounded recent window, with no
// model call and no summary of a previous summary.
func TestCompactWithReconstructsFromDurableState(t *testing.T) {
	messages := []Message{
		UserMessage{Text: "fix the sequence collision"},
		AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "store.go"}},
		}}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: ai.TextBlocks(strings.Repeat("line\n", 200))},
		UserMessage{Text: "continue"},
		AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c2", Name: "read", Arguments: map[string]any{"path": "store_test.go"}},
		}}},
		ToolResultMessage{ToolCallID: "c2", ToolName: "read", Content: ai.TextBlocks("test body")},
	}
	projection := ContextProjection{
		Authority:  "BUILD · mutation permitted",
		Objective:  "fix the sequence collision",
		Completion: "NEEDS_REVIEW",
		Blocks: []string{
			"Verified facts: the allocator starts at 0",
			"Hypotheses: SQLite may be causing the collision",
			"Next action: run the store package test",
		},
		Counts: PreservationCounts{Evidence: 1, Verifications: 0, Constraints: 1},
	}
	result, err := CompactWith(context.Background(), messages, CompactOptions{
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 2},
		Window:     1000,
		Summarize: func(context.Context, []Message) (string, error) {
			t.Fatal("state-first reconstruction must not call the summarizer")
			return "", nil
		},
		Projection: func() (ContextProjection, bool) { return projection, true },
	})
	if err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	if result.Mode != ModeReconstructed {
		t.Fatalf("mode = %q, want %q", result.Mode, ModeReconstructed)
	}
	if result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("no reduction: %d -> %d", result.BeforeTokens, result.AfterTokens)
	}
	header := result.Messages[0].(UserMessage).Text
	for _, want := range []string{"BUILD", "NEEDS_REVIEW", "fix the sequence collision", "SQLite may be causing the collision"} {
		if !strings.Contains(header, want) {
			t.Fatalf("reconstructed header lost %q:\n%s", want, header)
		}
	}
	if err := ValidateCompactedMessages(result.Messages); err != nil {
		t.Fatalf("rebuilt context is structurally invalid: %v", err)
	}
}

// TestCompactWithRejectsAnInvalidProjection proves a malformed candidate is
// refused rather than replacing the valid context.
func TestCompactWithRejectsAnInvalidProjection(t *testing.T) {
	messages := []Message{UserMessage{Text: "one"}, UserMessage{Text: "two"}}
	_, err := CompactWith(context.Background(), messages, CompactOptions{
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 1},
		Window:     1000,
		Summarize:  func(context.Context, []Message) (string, error) { return "s", nil },
		Projection: func() (ContextProjection, bool) {
			return ContextProjection{Authority: "", Objective: "one", Blocks: []string{"x"}}, true
		},
	})
	if !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("err = %v, want ErrInvalidProjection", err)
	}
}

// TestRepeatedCompactionIsStable proves a second compaction rebuilds the same
// authoritative state instead of recursively summarising the first summary.
func TestRepeatedCompactionIsStable(t *testing.T) {
	projection := ContextProjection{
		Authority: "ASK · read-only",
		Objective: "explain the allocator",
		Blocks:    []string{"Verified facts: allocation is central", "Next action: answer"},
	}
	opts := CompactOptions{
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 1},
		Window:     2000,
		Summarize: func(context.Context, []Message) (string, error) {
			t.Fatal("projection path must not summarize")
			return "", nil
		},
		Projection: func() (ContextProjection, bool) { return projection, true },
	}
	first, err := CompactWith(context.Background(), []Message{
		UserMessage{Text: "explain"}, UserMessage{Text: "more"}, UserMessage{Text: "and more"},
	}, opts)
	if err != nil {
		t.Fatalf("first compaction: %v", err)
	}
	second, err := CompactWith(context.Background(), first.Messages, opts)
	if err != nil {
		t.Fatalf("second compaction: %v", err)
	}
	if second.Messages[0].(UserMessage).Text != first.Messages[0].(UserMessage).Text {
		t.Fatal("repeated compaction changed the authoritative reconstruction")
	}
	if second.Metrics.StateItemsPreserved != first.Metrics.StateItemsPreserved {
		t.Fatal("repeated compaction changed the preserved state count")
	}
}

// TestModelSwitchPreservesContinuity proves the same durable state survives a
// change of model window: only the retained suffix may differ.
func TestModelSwitchPreservesContinuity(t *testing.T) {
	projection := ContextProjection{
		Authority: "FUZZ · read-only",
		Objective: "reproduce the failure",
		Blocks:    []string{"Verified facts: the failure reproduces"},
	}
	messages := []Message{
		UserMessage{Text: "reproduce"},
		AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"command": "go test"}},
		}}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "bash", Content: ai.TextBlocks("FAIL")},
	}
	for _, window := range []int{1200, 8000} {
		result, err := CompactWith(context.Background(), messages, CompactOptions{
			Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 1},
			Window:     window,
			Summarize:  func(context.Context, []Message) (string, error) { return "s", nil },
			Projection: func() (ContextProjection, bool) { return projection, true },
		})
		if err != nil {
			t.Fatalf("window %d: %v", window, err)
		}
		header := result.Messages[0].(UserMessage).Text
		for _, want := range []string{"FUZZ", "reproduce the failure", "the failure reproduces"} {
			if !strings.Contains(header, want) {
				t.Fatalf("window %d lost %q", window, want)
			}
		}
	}
}

// TestLoopReportsTruthfulOverflowInsteadOfSendingAnImpossibleRequest proves
// the loop bound: repeated compaction cannot loop silently past the window.
func TestLoopReportsTruthfulOverflowInsteadOfSendingAnImpossibleRequest(t *testing.T) {
	var turns int
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		turns++
		if turns > 20 {
			t.Fatal("the loop did not report overflow")
		}
		big := strings.Repeat("x", 4000)
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: fmt.Sprintf("c%d", turns), Name: "read", Arguments: map[string]any{"value": big}}},
			StopReason: ai.StopToolUse,
		}), nil
	}
	loop, err := New(Config{
		Model:     ai.Model{ID: "m", Provider: "p", ContextWindow: 500},
		Stream:    stream,
		Tools:     []Tool{&countingTool{name: "read"}},
		MaxTokens: 64,
		MaxTurns:  20,
		Compaction: Compaction{
			Enabled: true, Strategy: StrategySummarize, ThresholdTokens: 200,
			KeepRecent: 1, MaxAttempts: 2,
		},
		Summarize: func(context.Context, []Message) (string, error) {
			return strings.Repeat("summary ", 100), nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runErr := loop.Run(context.Background(), []Message{UserMessage{Text: "start"}})
	if !errors.Is(runErr, ErrContextOverflow) {
		t.Fatalf("err = %v, want ErrContextOverflow", runErr)
	}
}

// TestRecallStoreIsConcurrencySafe is a race contract: the loop archives from
// the turn goroutine while a recall tool may read from a dispatched call.
func TestRecallStoreIsConcurrencySafe(t *testing.T) {
	store := NewRecallStore()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := store.Archive("read", fmt.Sprintf("c%d", i), strings.Repeat("x", 100))
			if _, ok := store.Get(id); !ok {
				t.Errorf("archived id %q was not readable", id)
			}
			_ = store.Len()
		}(i)
	}
	wg.Wait()
}

// TestCompactionPreservesCompletionUncertaintyAndVerification defends the
// truth invariants across every completion state: compaction is a projection
// of authoritative state, so the state it carries comes back byte-identical.
func TestCompactionPreservesCompletionUncertaintyAndVerification(t *testing.T) {
	for _, completion := range []string{"IN_PROGRESS", "NEEDS_REVIEW", "PROVEN"} {
		t.Run(completion, func(t *testing.T) {
			projection := ContextProjection{
				Authority:  "BUILD · mutation permitted",
				Objective:  "fix the collision",
				Completion: completion,
				Blocks: []string{
					"Hypotheses: SQLite may be causing the collision",
					"Verification: FAILED go test ./internal/store",
					"Next action: reproduce the failure",
				},
			}
			result, err := CompactWith(context.Background(), []Message{
				UserMessage{Text: "fix"}, UserMessage{Text: "continue"},
			}, CompactOptions{
				Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 1},
				Window:     2000,
				Summarize:  func(context.Context, []Message) (string, error) { return "s", nil },
				Projection: func() (ContextProjection, bool) { return projection, true },
			})
			if err != nil {
				t.Fatalf("CompactWith: %v", err)
			}
			header := result.Messages[0].(UserMessage).Text
			for _, want := range []string{completion, "may be causing", "FAILED go test", "NEEDS_REVIEW", "PROVEN"} {
				if want == "NEEDS_REVIEW" && completion != "NEEDS_REVIEW" || want == "PROVEN" && completion != "PROVEN" {
					continue
				}
				if !strings.Contains(header, want) {
					t.Fatalf("completion %s lost %q:\n%s", completion, want, header)
				}
			}
		})
	}
}

// TestCompactionBenchmarkLongExploration is a deterministic benchmark-shaped
// contract: a long exploration reduces by more than half, keeps the objective
// and verified facts, and repeated compaction does not corrupt them.
func TestCompactionBenchmarkLongExploration(t *testing.T) {
	var messages []Message
	messages = append(messages, UserMessage{Text: "understand the store package"})
	for i := range 120 {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages, AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{ID: id, Name: "ls", Arguments: map[string]any{"path": fmt.Sprintf("dir%d", i)}}},
		}})
		messages = append(messages, ToolResultMessage{
			ToolCallID: id, ToolName: "ls",
			Content: ai.TextBlocks(strings.Repeat("entry\n", 120)),
		})
	}
	projection := ContextProjection{
		Authority: "ASK · read-only",
		Objective: "understand the store package",
		Blocks:    []string{"Verified facts: the package owns allocation", "Next action: answer"},
		Counts:    PreservationCounts{Evidence: 1},
	}
	opts := CompactOptions{
		Compaction: Compaction{Enabled: true, Strategy: StrategySummarize, KeepRecent: 4, KeepRecentToolResults: 2},
		Window:     16000,
		Summarize:  func(context.Context, []Message) (string, error) { return "s", nil },
		Projection: func() (ContextProjection, bool) { return projection, true },
	}
	result, err := CompactWith(context.Background(), messages, opts)
	if err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	reduction := float64(result.BeforeTokens-result.AfterTokens) / float64(result.BeforeTokens)
	t.Logf("long exploration: %d -> %d tokens (%.1f%% reduction)", result.BeforeTokens, result.AfterTokens, reduction*100)
	if reduction < 0.5 {
		t.Fatalf("reduction = %.2f, want at least 0.5 (%d -> %d)", reduction, result.BeforeTokens, result.AfterTokens)
	}
	for attempt := range 3 {
		result, err = CompactWith(context.Background(), result.Messages, opts)
		if err != nil {
			t.Fatalf("repeated compaction %d: %v", attempt+1, err)
		}
		header := result.Messages[0].(UserMessage).Text
		for _, want := range []string{"ASK · read-only", "understand the store package", "Verified facts: the package owns allocation", "Next action: answer"} {
			if !strings.Contains(header, want) {
				t.Fatalf("attempt %d lost %q:\n%s", attempt+1, want, header)
			}
		}
	}
}

// TestLoopStateFirstCompactionContinuesWithLatestDurableState proves the
// end-to-end path: turn observation updates durable state, the predictive
// trigger compacts, and the next request carries that updated state rather than
// a narrative summary.
func TestLoopStateFirstCompactionContinuesWithLatestDurableState(t *testing.T) {
	var durableTurns int
	compactions := 0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		text := strings.Join(messageStrings(req.Messages), "\n")
		if strings.Contains(text, "Durable facts: ") {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn,
			}), nil
		}
		big := strings.Repeat("observation-", 400)
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID: fmt.Sprintf("c%d", durableTurns), Name: "read",
				Arguments: map[string]any{"path": fmt.Sprintf("file%d.go", durableTurns), "value": big},
			}},
			StopReason: ai.StopToolUse,
		}), nil
	}
	loop, err := New(Config{
		Model:     ai.Model{ID: "m", Provider: "p", ContextWindow: 1200},
		Stream:    stream,
		Tools:     []Tool{&countingTool{name: "read"}},
		MaxTokens: 128,
		MaxTurns:  10,
		Compaction: Compaction{
			Enabled: true, Strategy: StrategySummarize, ThresholdTokens: 500,
			KeepRecent: 1, MaxAttempts: 2,
		},
		Summarize: func(context.Context, []Message) (string, error) {
			t.Fatal("state-first compaction must not summarize")
			return "", nil
		},
		OnTurn:    func(Turn) { durableTurns++ },
		OnCompact: func(CompactResult) { compactions++ },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loop.SetCompactionProjection(func() (ContextProjection, bool) {
		completion := "IN_PROGRESS"
		return ContextProjection{
			Authority:  "ASK · read-only",
			Objective:  "understand the files",
			Completion: completion,
			Blocks:     []string{fmt.Sprintf("Durable facts: %d observations", durableTurns)},
		}, true
	})
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "start"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactions == 0 {
		t.Fatal("no compaction ran despite the projected overflow")
	}
}

// messageStrings flattens provider messages for matching.
func messageStrings(messages []ai.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		switch m := message.(type) {
		case ai.UserMessage:
			out = append(out, m.Text())
		case ai.AssistantMessage:
			out = append(out, m.Text())
		case ai.ToolResultMessage:
			out = append(out, m.Text())
		}
	}
	return out
}

// TestClassifyRunTreatsContextOverflowAsReview proves a truthful overflow is
// not completion and not a terminal failure: the task survives with review.
func TestClassifyRunTreatsContextOverflowAsReview(t *testing.T) {
	verdict, reason := ClassifyRun(ErrContextOverflow, CompletionInput{}, false)
	if verdict != RunNeedsReview {
		t.Fatalf("verdict = %v, want RunNeedsReview", verdict)
	}
	if !strings.Contains(reason, "context") {
		t.Fatalf("reason = %q, want the context failure named", reason)
	}
}
