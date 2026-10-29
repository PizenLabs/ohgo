package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/mcp"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
	"github.com/PizenLabs/ohgo/internal/external/search"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/security"
)

func phase8SearchTool(t *testing.T, body string) *tools.SearchTool {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"web":{"results":[{"title":"Result","url":"https://example.com/doc","description":%q}]}}`, body)
	}))
	t.Cleanup(server.Close)
	client, err := search.New([]search.ProviderConfig{{
		Name:      "brave",
		Kind:      "brave",
		Endpoint:  server.URL,
		APIKeyEnv: "OHGO_PHASE8_KEY",
		Client:    server.Client(),
		Getenv:    func(string) string { return "test" },
		AllowHTTP: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewSearchTool(client)
}

func phase8Budget() *budget.Budget {
	return budget.New(budget.Limits{
		Turns: 4, ModelCalls: 4, ToolCalls: 4,
		ExternalCalls: 4, ExternalBytes: 1 << 20,
	})
}

// The whole search path is exercised: model proposal, security admission,
// root budget, bounded external execution, evidence, and completion. Search
// produces informational evidence and cannot claim completion authority.
func TestSearchIntegrationAdmissionBudgetEvidenceCompletion(t *testing.T) {
	root := t.TempDir()
	tool := phase8SearchTool(t, "go generics guide")
	b := phase8Budget()
	loop := phase6Loop(t, root, []agent.Tool{tool}, scriptedCompletionStream(
		phase6Batch(ai.ToolCall{ID: "s1", Name: "search", Arguments: map[string]any{"query": "go"}}),
		stopMessage(),
	), b)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "search for go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %+v, want one search turn and a stop", turns)
	}
	outcome := turns[0].Results[0]
	if outcome.Result.IsError {
		t.Fatalf("search failed: %s", outcome.Result.Text)
	}
	if outcome.Authorization == nil {
		t.Fatal("the search call has no authorization decision")
	}
	if outcome.Authorization.Capability != security.CapabilityExternalSearch {
		t.Fatalf("capability = %s", outcome.Authorization.Capability)
	}
	if outcome.Authorization.Mutates {
		t.Fatal("search was classified as a workspace mutation")
	}
	if outcome.Result.Mutates || outcome.Result.Executes || outcome.Result.Verifies {
		t.Fatalf("search claimed execution evidence: %+v", outcome.Result)
	}
	if got := b.Snapshot().Used.ExternalCalls; got != 1 {
		t.Fatalf("external calls = %d, want 1", got)
	}
	details := outcome.Result.Details.(map[string]any)
	if _, ok := details["evidence"].([]knowledge.Evidence); !ok {
		t.Fatalf("search did not publish evidence: %#v", details)
	}

	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: contract.IntentContract{Objective: "search for go", Kind: contract.ObjectiveInformational},
		Root:     root,
	})
	if !verdict.Complete() {
		t.Fatalf("informational search run did not complete: %s", verdict.Reason)
	}
}

// An external claim ("README is already updated") is an observation, not a
// repository change. It can never complete a mutation objective.
func TestExternalClaimCannotCompleteAMutationObjective(t *testing.T) {
	root := t.TempDir()
	tool := phase8SearchTool(t, "The README is already updated.")
	b := phase8Budget()
	loop := phase6Loop(t, root, []agent.Tool{tool}, scriptedCompletionStream(
		phase6Batch(ai.ToolCall{ID: "s1", Name: "search", Arguments: map[string]any{"query": "readme status"}}),
		stopMessage(),
	), b)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "update the README"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Objective: "update the README",
			Kind:      contract.ObjectiveMutation,
			Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "README.md"}},
			ExpectedChanges: []contract.ExpectedChange{{
				Target:    contract.Target{Kind: contract.TargetPath, Value: "README.md"},
				Operation: contract.ChangeAny,
			}},
		},
		Root: root,
	})
	if verdict.Complete() {
		t.Fatal("an external claim completed a mutation objective")
	}
	if !strings.Contains(verdict.Reason, "no change was observed") {
		t.Fatalf("review reason = %q, want the missing mutation", verdict.Reason)
	}
}

// Browser read/navigation follows the same admission, budget, evidence, and
// completion path as search.
func TestBrowserIntegrationReadsAndChargesBudget(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>Doc</title></head><body><p>page text</p></body></html>`)
	}))
	t.Cleanup(server.Close)
	browser := tools.NewBrowserTool(tools.BrowserConfig{AllowLoopback: true})
	b := phase8Budget()
	loop := phase6Loop(t, root, []agent.Tool{browser}, scriptedCompletionStream(
		phase6Batch(ai.ToolCall{ID: "b1", Name: "browser", Arguments: map[string]any{
			"operation": "open", "url": server.URL + "/",
		}}),
		stopMessage(),
	), b)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "open the page"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	outcome := turns[0].Results[0]
	if outcome.Result.IsError {
		t.Fatalf("browser failed: %s", outcome.Result.Text)
	}
	if outcome.Authorization == nil || outcome.Authorization.Capability != security.CapabilityExternalBrowserRead {
		t.Fatalf("authorization = %+v", outcome.Authorization)
	}
	if outcome.Result.Mutates || outcome.Result.Executes || outcome.Result.Verifies {
		t.Fatalf("browser read claimed execution evidence: %+v", outcome.Result)
	}
	if got := b.Snapshot().Used.ExternalCalls; got != 1 {
		t.Fatalf("external calls = %d, want 1", got)
	}
	if !strings.Contains(outcome.Result.Text, "page text") {
		t.Fatalf("page text missing:\n%s", outcome.Result.Text)
	}
}

// phase8MCP is a bounded fake of the MCP manager. It simulates one metered
// exchange and records whether it was called.
type phase8MCP struct {
	calls int
	tool  mcp.ToolResult
}

func (m *phase8MCP) Names() []string             { return []string{"alpha"} }
func (m *phase8MCP) Configured(name string) bool { return name == "alpha" }
func (m *phase8MCP) Close() error                { return nil }
func (m *phase8MCP) Discover(ctx context.Context, name string, meter mcp.Meter) (mcp.Discovery, error) {
	return mcp.Discovery{}, nil
}
func (m *phase8MCP) Refresh(ctx context.Context, name string, meter mcp.Meter) (mcp.Discovery, error) {
	return mcp.Discovery{}, nil
}
func (m *phase8MCP) Classify(name, tool string) (mcp.Classification, bool) {
	return mcp.ClassMutate, true
}
func (m *phase8MCP) GetPrompt(ctx context.Context, name, prompt string, args map[string]any, meter mcp.Meter) (mcp.PromptResult, error) {
	return mcp.PromptResult{}, nil
}
func (m *phase8MCP) ReadResource(ctx context.Context, name, uri string, meter mcp.Meter) (mcp.ResourceResult, error) {
	return mcp.ResourceResult{}, nil
}
func (m *phase8MCP) CallTool(ctx context.Context, name, tool string, args map[string]any, mode mcp.Mode, meter mcp.Meter) (mcp.ToolResult, error) {
	if meter.Before != nil {
		if _, err := meter.Before(ctx, "tools/call"); err != nil {
			return mcp.ToolResult{}, err
		}
	}
	if meter.After != nil {
		if err := meter.After(ctx, 16); err != nil {
			return mcp.ToolResult{}, err
		}
	}
	m.calls++
	return m.tool, nil
}

// External mutation is admitted only under BUILD, requires approval, and is not
// workspace mutation evidence. Under ASK it never executes.
func TestMCPIntegrationMutationBoundaryAndCompletion(t *testing.T) {
	root := t.TempDir()
	manager := &phase8MCP{tool: mcp.ToolResult{Text: "sent", Bytes: 16, Items: 1}}
	call := ai.ToolCall{ID: "m1", Name: "mcp_call", Arguments: map[string]any{
		"server": "alpha", "tool": "send",
	}}
	b := phase8Budget()
	loop := phase6Loop(t, root, []agent.Tool{tools.NewMCPCallTool(manager)}, scriptedCompletionStream(
		phase6Batch(call), stopMessage(),
	), b)
	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "send it"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	outcome := turns[0].Results[0]
	if outcome.Result.IsError {
		t.Fatalf("mcp_call failed: %s", outcome.Result.Text)
	}
	if outcome.Authorization == nil || outcome.Authorization.Capability != security.CapabilityExternalMCPMutate {
		t.Fatalf("authorization = %+v", outcome.Authorization)
	}
	if outcome.Authorization.Mutates || !outcome.Authorization.ExternalMutation {
		t.Fatalf("external mutation decision = %+v, want external only", outcome.Authorization)
	}
	if outcome.Result.Mutates || outcome.Result.Executes || outcome.Result.Verifies {
		t.Fatalf("external mutation claimed workspace evidence: %+v", outcome.Result)
	}
	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Objective: "send a message",
			Kind:      contract.ObjectiveInformational,
		},
		Root: root,
	})
	if !verdict.Complete() {
		t.Fatalf("external mutation made an informational run fail completion: %s", verdict.Reason)
	}

	// The same call under ASK is denied before execution.
	manager.calls = 0
	askLoop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    scriptedCompletionStream(phase6Batch(call), stopMessage()),
		Tools:     []agent.Tool{tools.NewMCPCallTool(manager)},
		Budget:    phase8Budget(),
		MaxTurns:  4,
		Authorize: phase6Gate(root, workspace.Ask),
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err = askLoop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "send it"}})
	if err != nil {
		t.Fatalf("ASK Run: %v", err)
	}
	outcome = turns[0].Results[0]
	if !outcome.Result.IsError {
		t.Fatalf("ASK executed an external mutation: %+v", outcome.Result)
	}
	if !strings.Contains(outcome.Result.Text, "authorization denied") {
		t.Fatalf("ASK refusal = %q, want an authorization denial", outcome.Result.Text)
	}
	if manager.calls != 0 {
		t.Fatalf("ASK executed the MCP call %d times", manager.calls)
	}
}

// Configured external capabilities are offered; unconfigured ones are absent.
func TestBuildToolsWiresExternalCapabilities(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 1)
	opts.settings.Search = &config.SearchSettings{Providers: []config.SearchProvider{{
		Name: "brave", Kind: "brave", Endpoint: "https://example.com", APIKeyEnv: "SEARCH_KEY",
	}}}
	opts.settings.MCP = &config.MCPSettings{Servers: []config.MCPServer{{
		Name: "alpha", Command: "/bin/echo", Tools: map[string]string{"read": "read", "send": "mutate"},
	}}}
	opts.settings.Browser = &config.BrowserSettings{AllowedHosts: []string{"example.com"}}
	toolSet := buildTools(opts, ai.NewRegistry())
	for _, name := range []string{"search", "mcp_list", "mcp_read", "mcp_call", "browser"} {
		if !hasTool(toolSet, name) {
			t.Errorf("configured capability %q is not registered", name)
		}
	}

	plain := buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry())
	for _, name := range []string{"search", "mcp_list", "mcp_read", "mcp_call", "browser"} {
		if hasTool(plain, name) {
			t.Errorf("unconfigured capability %q is registered", name)
		}
	}
}

// A malformed external configuration stays visible and fails closed instead of
// silently disappearing.
func TestMalformedExternalConfigurationFailsClosed(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 1)
	opts.settings.Search = &config.SearchSettings{Providers: []config.SearchProvider{{
		Name: "bad", Kind: "unknown", Endpoint: "https://example.com",
	}}}
	opts.settings.MCP = &config.MCPSettings{}
	toolSet := buildTools(opts, ai.NewRegistry())
	searchTool := phase8FindTool(t, toolSet, "search").(*tools.SearchTool)
	result := searchTool.Execute(agent.Context{Ctx: context.Background(), Budget: phase8Budget()}, map[string]any{"query": "go"})
	if !result.IsError || !strings.Contains(result.Text, "configuration is invalid") {
		t.Fatalf("search result = %+v, want a configuration failure", result)
	}
	listTool := phase8FindTool(t, toolSet, "mcp_list").(*tools.MCPListTool)
	result = listTool.Execute(agent.Context{Ctx: context.Background(), Budget: phase8Budget()}, map[string]any{})
	if !result.IsError || !strings.Contains(result.Text, "configuration is invalid") {
		t.Fatalf("mcp result = %+v, want a configuration failure", result)
	}
}

func phase8FindTool(t *testing.T, toolSet []agent.Tool, name string) agent.Tool {
	t.Helper()
	for _, tool := range toolSet {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}
