package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/security"
)

// phase6Fixture writes a small module with one dependency edge.
func phase6Fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":   "module example.com/app\n\ngo 1.21\n",
		"a/a.go":   "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go":   "package b\n",
		"c/c.go":   "package c\n",
		"note.txt": "old\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func phase6GraphCall(id string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        id,
			Name:      "graph",
			Arguments: args,
		}},
		StopReason: ai.StopToolUse,
	}
}

func phase6Batch(calls ...ai.ToolCall) ai.AssistantMessage {
	content := make([]ai.ContentBlock, 0, len(calls))
	for _, call := range calls {
		content = append(content, call)
	}
	return ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
}

func phase6Gate(root string, ws workspace.Workspace) *securityGate {
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(ws)
	return opts.newSecurityGate(nil)
}

func phase6Loop(t *testing.T, root string, toolSet []agent.Tool, stream ai.StreamFn, b *budget.Budget) *agent.Loop {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    stream,
		Tools:     toolSet,
		Budget:    b,
		MaxTurns:  10,
		Authorize: phase6Gate(root, workspace.Build),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop
}

func phase6GraphTool(t *testing.T, toolSet []agent.Tool) *tools.GraphTool {
	t.Helper()
	for _, tool := range toolSet {
		if graph, ok := tool.(*tools.GraphTool); ok {
			return graph
		}
	}
	return nil
}

func TestGraphToolIsAdmittedAsReadOnlyInEveryWorkspace(t *testing.T) {
	// The relationship surface is informational. ASK, FUZZ, and BUILD must
	// all admit it as a read, and the security decision must never claim a
	// mutation capability.
	root := phase6Fixture(t)
	graph := phase6GraphTool(t, buildTools(testOptions(t, root, 1), ai.NewRegistry()))
	if graph == nil {
		t.Fatal("graph is not registered")
	}
	if graph.Approval().Tier != agent.TierRead {
		t.Fatalf("graph tier = %q, want read", graph.Approval().Tier)
	}

	call := ai.ToolCall{ID: "g1", Name: "graph", Arguments: map[string]any{"package": "a"}}
	for _, ws := range []workspace.Workspace{workspace.Ask, workspace.Fuzz, workspace.Build} {
		if err := (workspace.Authority{Workspace: ws}).Admit(graph, call.Arguments); err != nil {
			t.Errorf("%s refused a read-only graph query: %v", ws, err)
		}
		decision := phase6Gate(root, ws).Evaluate(call, graph)
		if decision.Outcome != security.OutcomeAuto {
			t.Errorf("%s decision = %s, want AUTO", ws, decision.Outcome)
		}
		if decision.Capability != security.CapabilityRead {
			t.Errorf("%s capability = %s, want read", ws, decision.Capability)
		}
		if decision.Mutates {
			t.Errorf("%s decision claims a mutation", ws)
		}
	}
}

func TestGraphQueriesConsumeTheToolBudget(t *testing.T) {
	// A graph query is a tool execution. It must be admitted exactly once by
	// the existing tool-call grant and must not execute when the grant is
	// exhausted.
	root := phase6Fixture(t)
	graph := tools.NewGraphTool(tools.NewStructuralSource(root))

	t.Run("one query", func(t *testing.T) {
		b := budget.New(budget.Limits{Turns: 4, ModelCalls: 4, ToolCalls: 1})
		loop := phase6Loop(t, root, []agent.Tool{graph}, scriptedCompletionStream(
			phase6GraphCall("g1", map[string]any{"package": "a"}),
			stopMessage(),
		), b)
		turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "show a's dependencies"}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(turns) != 2 || len(turns[0].Results) != 1 {
			t.Fatalf("turns = %+v, want one graph turn and a stop", turns)
		}
		if turns[0].Results[0].Result.IsError {
			t.Fatalf("graph query failed: %s", turns[0].Results[0].Result.Text)
		}
		if used := b.Snapshot().Used.ToolCalls; used != 1 {
			t.Errorf("tool calls used = %d, want 1", used)
		}
	})

	t.Run("exhausted grant", func(t *testing.T) {
		b := budget.New(budget.Limits{Turns: 4, ModelCalls: 4, ToolCalls: 1})
		calls := phase6Batch(
			ai.ToolCall{ID: "g1", Name: "graph", Arguments: map[string]any{"package": "a"}},
			ai.ToolCall{ID: "g2", Name: "graph", Arguments: map[string]any{"package": "b"}},
		)
		loop := phase6Loop(t, root, []agent.Tool{graph}, scriptedCompletionStream(calls, stopMessage()), b)
		turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "show both dependencies"}})
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

func TestGraphEvidenceCannotCompleteAMutation(t *testing.T) {
	// Relationship evidence is not mutation evidence and not verification.
	// A run that only queried the graph must remain reviewable.
	root := phase6Fixture(t)
	graph := tools.NewGraphTool(tools.NewStructuralSource(root))
	b := budget.New(budget.Limits{Turns: 4, ModelCalls: 4, ToolCalls: 2})
	loop := phase6Loop(t, root, []agent.Tool{graph}, scriptedCompletionStream(
		phase6GraphCall("g1", map[string]any{"package": "a", "depth": 2.0}),
		stopMessage(),
	), b)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "Update note.txt"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	completion := agent.EvaluateCompletion(turns, true)
	if completion.Complete() {
		t.Fatalf("graph evidence completed a mutation objective: %+v", completion)
	}
	if mutated, verified := agent.CompletionEvidence(turns, true); mutated || verified {
		t.Errorf("graph evidence reported mutation=%v verification=%v", mutated, verified)
	}
}

func TestChildStructuralToolSharesTheParentSource(t *testing.T) {
	// Delegation must not create a second repository index or a second graph
	// service. The child receives the same shared tool instance.
	root := phase6Fixture(t)
	opts := testOptions(t, root, 2)
	registry := ai.NewRegistry()
	parentTools := buildTools(opts, registry)
	childTools := childToolSet(opts, registry, parentTools, 0)

	parentGraph := phase6GraphTool(t, parentTools)
	childGraph := phase6GraphTool(t, childTools)
	if parentGraph == nil || childGraph == nil {
		t.Fatal("graph is missing from the parent or child tool set")
	}
	if parentGraph.Source() == nil || parentGraph.Source() != childGraph.Source() {
		t.Fatal("child graph does not share the parent structural source")
	}
	if !hasTool(childTools, "symbols") || !hasTool(childTools, "graph") {
		t.Error("child lost a structural tool")
	}
}

func TestChildGraphQuerySharesAuthorityAndBudget(t *testing.T) {
	// A delegated child must not get a second graph service, a fresh budget,
	// or a wider authority. It receives the same tool instance and the root
	// grant, and its query is charged exactly once.
	root := phase6Fixture(t)
	source := tools.NewStructuralSource(root)
	graph := tools.NewGraphTool(source)
	symbols := tools.NewSymbolsToolWithSource(source)
	if !agent.IsReadOnly([]agent.Tool{symbols, graph}) {
		t.Fatal("read-tier structural tools are not recognised as read-only")
	}
	b := budget.New(budget.Limits{Turns: 6, ModelCalls: 6, ToolCalls: 2, Children: 1})
	parent := agent.Config{
		Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: scriptedCompletionStream(
			phase6GraphCall("g1", map[string]any{"path": "internal/api"}),
			stopMessage(),
		),
		Tools:     []agent.Tool{graph},
		Budget:    b,
		MaxTurns:  6,
		Authorize: phase6Gate(root, workspace.Ask),
	}
	result, err := agent.RunSubagent(context.Background(), parent, agent.SubagentSpec{
		Task:     "show the dependencies of internal/api",
		System:   []ai.SystemPromptBlock{{Text: "you are a read-only repository researcher"}},
		Tools:    []agent.Tool{graph},
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if result.ToolCalls != 1 {
		t.Errorf("child tool calls = %d, want 1", result.ToolCalls)
	}
	if len(result.Turns) == 0 || len(result.Turns[0].Results) != 1 {
		t.Fatalf("child turns = %+v, want one graph result", result.Turns)
	}
	if result.Turns[0].Results[0].ToolName != "graph" {
		t.Errorf("child tool = %q, want graph", result.Turns[0].Results[0].ToolName)
	}
	snapshot := b.Snapshot()
	if snapshot.Used.Children != 1 || snapshot.Used.ToolCalls != 1 {
		t.Errorf("root budget = %+v, want one child and one tool call", snapshot.Used)
	}
}
