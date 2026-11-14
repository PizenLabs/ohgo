package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// TestRecallArchiveIsRunScoped proves the current recall boundary: ids resolve
// exactly within the run that created them, and a fresh process reports an
// explicit not-found instead of fabricating content. This is the observed
// limitation for recall-after-restart; it is not a silent success.
func TestRecallArchiveIsRunScoped(t *testing.T) {
	before := agent.NewRecallStore()
	id := before.Archive("read", "call-42", "the exact observed source window")
	if id == "" {
		t.Fatal("archive refused a valid observation")
	}
	toolBefore := tools.NewRecallTool(before)
	got := toolBefore.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"id": id})
	if got.IsError || got.Text != "the exact observed source window" {
		t.Fatalf("within-run recall = %+v, want the exact text", got)
	}
	if got.Mutates || len(got.AffectedPaths) != 0 {
		t.Fatalf("recall reported a mutation: %+v", got)
	}
	if toolBefore.Approval().Tier != agent.TierRead {
		t.Fatalf("recall tier = %s, want read", toolBefore.Approval().Tier)
	}

	// A new process builds a fresh archive. The id from the previous run must
	// not resolve to invented content.
	after := agent.NewRecallStore()
	toolAfter := tools.NewRecallTool(after)
	missing := toolAfter.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"id": id})
	if !missing.IsError || !strings.Contains(missing.Text, "no archived observation") {
		t.Fatalf("post-restart recall = %+v, want an explicit not-found", missing)
	}
}

// TestSummarizerConsumesTheExistingModelCallBudget proves compaction cannot
// become an unmetered provider-call side channel.
func TestSummarizerConsumesTheExistingModelCallBudget(t *testing.T) {
	holder := budget.NewHolder(budget.New(budget.Limits{ModelCalls: 1}))
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "summary"}},
		}), nil
	}
	summarize := newSummarizer(stream, catalog.MustResolve("anthropic/claude-sonnet-4-5"), holder)
	if _, err := summarize(context.Background(), []agent.Message{agent.UserMessage{Text: "first"}}); err != nil {
		t.Fatalf("first summarizer call: %v", err)
	}
	_, err := summarize(context.Background(), []agent.Message{agent.UserMessage{Text: "second"}})
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("second summarizer call err = %v, want budget.ErrExhausted", err)
	}
}
