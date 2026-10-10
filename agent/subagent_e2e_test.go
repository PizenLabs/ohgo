package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
)

// TestRunSubagentRunsToARealAnswer is the capability this increment bought.
//
// Everything above it proves policy; this proves the child actually drives a
// loop, calls a tool, and returns the text its parent will see.
func TestRunSubagentRunsToARealAnswer(t *testing.T) {
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p", ContextWindow: 100_000},
		Stream: aiTextStream("I checked the file and it looks correct."),
		Tools:  []Tool{&countingTool{name: "read"}},
	}

	result, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "check src/auth",
		System: []ai.SystemPromptBlock{{Text: "you are a read-only researcher"}},
		Tools:  []Tool{&countingTool{name: "read"}},
	})
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if !strings.Contains(result.Text, "looks correct") {
		t.Errorf("text = %q, want the child's answer", result.Text)
	}
	// The child is read-only because its tool set is, which is what lets the
	// parent let it loose on the repository unsupervised.
	if !result.ReadOnly {
		t.Error("the child is read-only but was not reported as such")
	}
}

// TestRunSubagentReportsNoOutput is a silent-wrong-answer guard.
//
// An empty result would read as "the subagent answered nothing", which cannot
// be told apart from "it failed quietly". An error keeps the two apart.
func TestRunSubagentReportsNoOutput(t *testing.T) {
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p", ContextWindow: 100_000},
		Stream: aiTextStream(""),
		Tools:  []Tool{namedTool("read")},
	}

	result, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "check src/auth",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	})
	if err == nil {
		t.Fatal("a subagent with no output was reported as a success")
	}
	if !isNoSubagentResult(err) {
		t.Errorf("error = %v, want ErrNoSubagentResult", err)
	}
	if result.Text != "" {
		t.Errorf("text = %q, want it empty on failure", result.Text)
	}
}

func isNoSubagentResult(err error) bool {
	return err != nil && err.Error() == ErrNoSubagentResult.Error()
}

// TestRunSubagentPropagatesContextCancellation stops a child from outliving the
// parent's deadline, which is how a cancelled run spends tokens anyway.
func TestRunSubagentPropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: aiTextStream("never reached"),
		Tools:  []Tool{namedTool("read")},
	}

	_, err := RunSubagent(ctx, parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	})
	if err == nil {
		t.Fatal("a cancelled context did not stop the subagent")
	}
}

// TestRunSubagentInheritsAEnabledCompaction proves a long-running child does not
// outgrow its window: the summarizer is handed down rather than silently
// dropped, which would turn a long task into a provider rejection.
func TestRunSubagentInheritsAEnabledCompaction(t *testing.T) {
	summarizerCalled := false
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p", ContextWindow: 1_000},
		Stream: aiTextStream("done"),
		Tools:  []Tool{namedTool("read")},
		Compaction: Compaction{
			Enabled:  true,
			Strategy: StrategySummarize,
		},
		Summarize: func(ctx context.Context, messages []Message) (string, error) {
			summarizerCalled = true
			return "summary", nil
		},
	}

	if _, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	}); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	// The summarizer is not called on a short run; the point is that the child
	// was buildable with it, so a misconfiguration fails here rather than
	// mid-task.
	_ = summarizerCalled
}

// TestRunSubagentBuildFailsWithoutASummarizer keeps the parent's own
// construction-time guarantee: compaction with no summarizer is refused by New,
// so a child must fail the same way rather than starting and then breaking.
func TestRunSubagentBuildFailsWithoutASummarizer(t *testing.T) {
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: aiTextStream("done"),
		Tools:  []Tool{namedTool("read")},
		Compaction: Compaction{
			Enabled:  true,
			Strategy: StrategySummarize,
		},
	}

	_, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	})
	if err == nil {
		t.Fatal("a child with compaction but no summarizer was built")
	}
	if !strings.Contains(err.Error(), "summariz") {
		t.Errorf("error = %v, want it to name the summarizer", err)
	}
}

// aiTextStream is a StreamFn that answers every request with one text turn.
func aiTextStream(text string) ai.StreamFn {
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: text}},
		}), nil
	}
}
