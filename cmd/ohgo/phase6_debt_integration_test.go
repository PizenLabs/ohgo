package main

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/security"
)

func phase6RefsCall(id string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        id,
			Name:      "refs",
			Arguments: args,
		}},
		StopReason: ai.StopToolUse,
	}
}

func phase6RefsTool(t *testing.T, toolSet []agent.Tool) *tools.RefsTool {
	t.Helper()
	for _, tool := range toolSet {
		if refs, ok := tool.(*tools.RefsTool); ok {
			return refs
		}
	}
	return nil
}

func TestBuildToolsWiresRefsToTheOneStructuralSource(t *testing.T) {
	// The reference surface must read the same snapshot as symbol coordinates
	// and package relationships; a second source would allow the three
	// answers to disagree.
	toolSet := buildTools(testOptions(t, phase6Fixture(t), 1), ai.NewRegistry())
	symbols := phase6SymbolsTool(t, toolSet)
	graph := phase6GraphTool(t, toolSet)
	refs := phase6RefsTool(t, toolSet)
	if symbols == nil || graph == nil || refs == nil {
		t.Fatalf("structural tools missing: symbols=%v graph=%v refs=%v", symbols != nil, graph != nil, refs != nil)
	}
	if symbols.Source() == nil || graph.Source() == nil || refs.Source() == nil {
		t.Fatal("a structural tool has no source")
	}
	if symbols.Source() != graph.Source() || graph.Source() != refs.Source() {
		t.Fatal("symbols, graph, and refs do not share one structural source")
	}
}

func phase6SymbolsTool(t *testing.T, toolSet []agent.Tool) *tools.SymbolsTool {
	t.Helper()
	for _, tool := range toolSet {
		if symbols, ok := tool.(*tools.SymbolsTool); ok {
			return symbols
		}
	}
	return nil
}

func TestRefsToolIsAdmittedAsReadOnlyInEveryWorkspace(t *testing.T) {
	// The reference surface is informational. ASK, FUZZ, and BUILD must all
	// admit it as a read, and the decision must never claim a mutation.
	root := phase6Fixture(t)
	refs := phase6RefsTool(t, buildTools(testOptions(t, root, 1), ai.NewRegistry()))
	if refs == nil {
		t.Fatal("refs is not registered")
	}
	if refs.Approval().Tier != agent.TierRead {
		t.Fatalf("refs tier = %q, want read", refs.Approval().Tier)
	}

	call := ai.ToolCall{ID: "r1", Name: "refs", Arguments: map[string]any{"symbol": "a.A"}}
	for _, ws := range []workspace.Workspace{workspace.Ask, workspace.Fuzz, workspace.Build} {
		if err := (workspace.Authority{Workspace: ws}).Admit(refs, call.Arguments); err != nil {
			t.Errorf("%s refused a read-only refs query: %v", ws, err)
		}
		decision := phase6Gate(root, ws).Evaluate(call, refs)
		if decision.Outcome != security.OutcomeAuto {
			t.Errorf("%s decision = %s, want AUTO", ws, decision.Outcome)
		}
		if decision.Capability != security.CapabilityRead {
			t.Errorf("%s capability = %s, want read", ws, decision.Capability)
		}
		if decision.Risk != security.RiskLow {
			t.Errorf("%s risk = %s, want low", ws, decision.Risk)
		}
		if decision.Mutates {
			t.Errorf("%s decision claims a mutation", ws)
		}
	}
}

func TestRefsQueriesConsumeTheToolBudget(t *testing.T) {
	root := phase6Fixture(t)
	refs := tools.NewRefsTool(tools.NewStructuralSource(root))

	t.Run("one query", func(t *testing.T) {
		b := budget.New(budget.Limits{Turns: 4, ModelCalls: 4, ToolCalls: 1})
		loop := phase6Loop(t, root, []agent.Tool{refs}, scriptedCompletionStream(
			phase6RefsCall("r1", map[string]any{"symbol": "a.A"}),
			stopMessage(),
		), b)
		turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "where is A referenced?"}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(turns) != 2 || len(turns[0].Results) != 1 {
			t.Fatalf("turns = %+v, want one refs turn and a stop", turns)
		}
		if turns[0].Results[0].Result.IsError {
			t.Fatalf("refs query failed: %s", turns[0].Results[0].Result.Text)
		}
		if used := b.Snapshot().Used.ToolCalls; used != 1 {
			t.Errorf("tool calls used = %d, want 1", used)
		}
	})

	t.Run("exhausted grant", func(t *testing.T) {
		b := budget.New(budget.Limits{Turns: 4, ModelCalls: 4, ToolCalls: 1})
		calls := phase6Batch(
			ai.ToolCall{ID: "r1", Name: "refs", Arguments: map[string]any{"symbol": "a.A"}},
			ai.ToolCall{ID: "r2", Name: "refs", Arguments: map[string]any{"symbol": "b.B"}},
		)
		loop := phase6Loop(t, root, []agent.Tool{refs}, scriptedCompletionStream(calls, stopMessage()), b)
		turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "query both"}})
		if !errors.Is(err, budget.ErrExhausted) {
			t.Fatalf("Run error = %v, want budget exhaustion", err)
		}
		if len(turns) != 1 || len(turns[0].Results) != 2 {
			t.Fatalf("turns = %+v, want one batch", turns)
		}
		executed, refused := 0, 0
		for _, outcome := range turns[0].Results {
			if outcome.Result.IsError {
				refused++
			} else {
				executed++
			}
		}
		if executed != 1 || refused != 1 {
			t.Errorf("executed/refused = %d/%d, want 1/1", executed, refused)
		}
		if used := b.Snapshot().Used.ToolCalls; used != 1 {
			t.Errorf("tool calls used = %d, want 1", used)
		}
	})
}

func TestRefsEvidenceCannotCompleteAMutation(t *testing.T) {
	// A reference answer is evidence about code shape, not proof that a
	// requested change happened. It must not contribute mutation or
	// verification, and must not complete a mutation objective.
	root := phase6Fixture(t)
	refs := tools.NewRefsTool(tools.NewStructuralSource(root))
	b := budget.New(budget.Limits{Turns: 4, ModelCalls: 4, ToolCalls: 2})
	loop := phase6Loop(t, root, []agent.Tool{refs}, scriptedCompletionStream(
		phase6RefsCall("r1", map[string]any{"symbol": "a.A", "relation": "callers"}),
		stopMessage(),
	), b)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "Update note.txt"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	completion := agent.EvaluateCompletion(turns, true)
	if completion.Complete() {
		t.Fatalf("refs evidence completed a mutation objective: %+v", completion)
	}
	if mutated, verified := agent.CompletionEvidence(turns, true); mutated || verified {
		t.Errorf("refs evidence reported mutation=%v verification=%v", mutated, verified)
	}
}

func TestChildRefsQuerySharesAuthorityAndBudget(t *testing.T) {
	// A delegated child must not get a second reference service, a fresh
	// budget, or a wider authority. It receives the same tool instance and the
	// root grant, and its query is charged exactly once.
	root := phase6Fixture(t)
	source := tools.NewStructuralSource(root)
	refs := tools.NewRefsTool(source)
	if !agent.IsReadOnly([]agent.Tool{refs}) {
		t.Fatal("the read-tier refs tool is not recognised as read-only")
	}
	b := budget.New(budget.Limits{Turns: 6, ModelCalls: 6, ToolCalls: 2, Children: 1})
	parent := agent.Config{
		Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: scriptedCompletionStream(
			phase6RefsCall("r1", map[string]any{"symbol": "a.A"}),
			stopMessage(),
		),
		Tools:     []agent.Tool{refs},
		Budget:    b,
		MaxTurns:  6,
		Authorize: phase6Gate(root, workspace.Ask),
	}
	result, err := agent.RunSubagent(context.Background(), parent, agent.SubagentSpec{
		Task:     "where is a.A referenced?",
		System:   []ai.SystemPromptBlock{{Text: "you are a read-only repository researcher"}},
		Tools:    []agent.Tool{refs},
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if result.ToolCalls != 1 {
		t.Errorf("child tool calls = %d, want 1", result.ToolCalls)
	}
	if len(result.Turns) == 0 || len(result.Turns[0].Results) != 1 {
		t.Fatalf("child turns = %+v, want one refs result", result.Turns)
	}
	if result.Turns[0].Results[0].ToolName != "refs" {
		t.Errorf("child tool = %q, want refs", result.Turns[0].Results[0].ToolName)
	}
	snapshot := b.Snapshot()
	if snapshot.Used.Children != 1 || snapshot.Used.ToolCalls != 1 {
		t.Errorf("root budget = %+v, want one child and one tool call", snapshot.Used)
	}
}

func TestChildRefsQuerySharesTheParentSource(t *testing.T) {
	root := phase6Fixture(t)
	opts := testOptions(t, root, 2)
	registry := ai.NewRegistry()
	parentTools := buildTools(opts, registry)
	childTools := childToolSet(opts, registry, parentTools, 0)

	parentRefs := phase6RefsTool(t, parentTools)
	childRefs := phase6RefsTool(t, childTools)
	if parentRefs == nil || childRefs == nil {
		t.Fatal("refs is missing from the parent or child tool set")
	}
	if parentRefs.Source() == nil || parentRefs.Source() != childRefs.Source() {
		t.Fatal("child refs does not share the parent structural source")
	}
	if !hasTool(childTools, "symbols") || !hasTool(childTools, "graph") || !hasTool(childTools, "refs") {
		t.Error("child lost a structural tool")
	}
}
