package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/security"
)

// scriptedAuthorizer returns a fixed sequence of decisions and records what it
// was asked. It stands in for the real security gate so the loop's sequence can
// be asserted in isolation.
type scriptedAuthorizer struct {
	mu            sync.Mutex
	decisions     []security.Decision
	evaluated     []ai.ToolCall
	approved      []ai.ToolCall
	approveResult bool
}

func (a *scriptedAuthorizer) Evaluate(call ai.ToolCall, _ Tool) security.Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evaluated = append(a.evaluated, call)
	if len(a.decisions) == 0 {
		return security.Decision{Outcome: security.OutcomeAuto}
	}
	next := a.decisions[0]
	a.decisions = a.decisions[1:]
	return next
}

func (a *scriptedAuthorizer) Approve(call ai.ToolCall, _ security.Decision) (security.Grant, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approved = append(a.approved, call)
	if a.approveResult {
		return security.Grant{Capability: security.CapabilityRead, Provenance: "test"}, true
	}
	return security.Grant{}, false
}

func (a *scriptedAuthorizer) counts() (evaluated, approved int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.evaluated), len(a.approved)
}

// oneCallStream scripts exactly one tool call followed by a stop.
func oneCallStream(name string, args map[string]any) ai.StreamFn {
	streamFn, _ := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: name, Arguments: args}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	)
	return streamFn
}

func runWithAuthorizer(t *testing.T, tool Tool, authorizer Authorizer) ([]Turn, *scriptedTool) {
	t.Helper()
	scripted, _ := tool.(*scriptedTool)
	loop, err := New(Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    oneCallStream(tool.Name(), map[string]any{"value": "x"}),
		Tools:     []Tool{tool},
		Authorize: authorizer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return turns, scripted
}

func TestAuthorizeAutoExecutes(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{Outcome: security.OutcomeAuto}}}
	turns, scripted := runWithAuthorizer(t, tool, authorizer)

	if got := len(scripted.invoked()); got != 1 {
		t.Fatalf("tool invocations = %d, want 1", got)
	}
	if len(turns) == 0 || len(turns[0].Results) == 0 {
		t.Fatal("no outcome recorded")
	}
	outcome := turns[0].Results[0]
	if outcome.Authorization == nil || outcome.Authorization.Outcome != security.OutcomeAuto {
		t.Errorf("outcome authorization = %+v, want AUTO", outcome.Authorization)
	}
	if outcome.Result.IsError {
		t.Errorf("an authorized call was an error: %q", outcome.Result.Text)
	}
}

// The read-only bit is part of the authorization decision, not a tool
// convention. The loop must hand it to the tool so an execution boundary can
// enforce it.
func TestAuthorizeReadOnlyReachesTheTool(t *testing.T) {
	tool := newScriptedTool("bash", "ran")
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{
		Outcome: security.OutcomeAuto, ReadOnly: true,
	}}}
	runWithAuthorizer(t, tool, authorizer)

	modes := tool.readOnlyModes()
	if len(modes) != 1 || !modes[0] {
		t.Fatalf("tool read-only modes = %v, want [true]", modes)
	}
}

func TestAuthorizeDenyNeverExecutes(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{
		Outcome: security.OutcomeDeny, Rule: security.RuleOutsideWorkspace, Reason: "outside the workspace",
	}}}
	turns, scripted := runWithAuthorizer(t, tool, authorizer)

	if got := len(scripted.invoked()); got != 0 {
		t.Fatalf("a denied tool executed %d times", got)
	}
	text := turns[0].Results[0].Result.Text
	if !strings.Contains(text, "authorization denied") {
		t.Errorf("refusal = %q, want an authorization denial", text)
	}
	if !strings.Contains(text, security.RuleOutsideWorkspace) {
		t.Errorf("refusal = %q, want the policy rule", text)
	}
	if !turns[0].Results[0].Result.IsError {
		t.Error("a denied call must be an error result")
	}
	if _, approved := authorizer.counts(); approved != 0 {
		t.Error("a denied call reached the approval step")
	}
}

func TestAuthorizeApproveThenGrantExecutes(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	authorizer := &scriptedAuthorizer{
		decisions:     []security.Decision{{Outcome: security.OutcomeApprove}, {Outcome: security.OutcomeAuto}},
		approveResult: true,
	}
	_, scripted := runWithAuthorizer(t, tool, authorizer)

	if got := len(scripted.invoked()); got != 1 {
		t.Fatalf("an approved tool ran %d times, want 1", got)
	}
	evaluated, approved := authorizer.counts()
	if evaluated != 2 {
		t.Errorf("evaluations = %d, want 2 (policy, then confirmation)", evaluated)
	}
	if approved != 1 {
		t.Errorf("approvals = %d, want 1", approved)
	}
}

func TestAuthorizeApproveRejectedNeverExecutes(t *testing.T) {
	tool := newScriptedTool("write", "wrote")
	authorizer := &scriptedAuthorizer{
		decisions:     []security.Decision{{Outcome: security.OutcomeApprove}},
		approveResult: false,
	}
	turns, scripted := runWithAuthorizer(t, tool, authorizer)

	if got := len(scripted.invoked()); got != 0 {
		t.Fatalf("a rejected tool executed %d times", got)
	}
	if !strings.Contains(turns[0].Results[0].Result.Text, "approval rejected") {
		t.Errorf("refusal = %q, want an approval rejection", turns[0].Results[0].Result.Text)
	}
}

func TestAuthorizeChangedOperationAfterApprovalNeverExecutes(t *testing.T) {
	// The operation changed between approval and the confirmation evaluation:
	// the approval no longer covers it, so it must not execute.
	tool := newScriptedTool("write", "wrote")
	authorizer := &scriptedAuthorizer{
		decisions:     []security.Decision{{Outcome: security.OutcomeApprove}, {Outcome: security.OutcomeDeny, Rule: security.RuleOutsideWorkspace}},
		approveResult: true,
	}
	turns, scripted := runWithAuthorizer(t, tool, authorizer)

	if got := len(scripted.invoked()); got != 0 {
		t.Fatalf("a changed operation executed %d times", got)
	}
	if !strings.Contains(turns[0].Results[0].Result.Text, "authorization changed after approval") {
		t.Errorf("refusal = %q, want a changed-authorization refusal", turns[0].Results[0].Result.Text)
	}
}

func TestAuthorizeSupersedesLegacyHooks(t *testing.T) {
	// When an authorizer is installed it is the only admission path: the
	// legacy admit/approve hooks are not consulted, so a stale hook cannot
	// grant or refuse independently.
	tool := newScriptedTool("read", "contents")
	admitCalled := false
	approveCalled := false
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{Outcome: security.OutcomeAuto}}}

	loop, err := New(Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    oneCallStream("read", map[string]any{"value": "x"}),
		Tools:     []Tool{tool},
		Authorize: authorizer,
		Admit: func(ai.ToolCall, Tool) (bool, string) {
			admitCalled = true
			return false, "legacy deny"
		},
		Approve: func(ai.ToolCall, Approval) (bool, string) {
			approveCalled = true
			return false, "legacy deny"
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if admitCalled || approveCalled {
		t.Error("legacy hooks ran while an authorizer was installed")
	}
	if got := len(tool.invoked()); got != 1 {
		t.Errorf("authorized tool invocations = %d, want 1", got)
	}
}

func TestSubagentInheritsLegacyAdmit(t *testing.T) {
	// Before Phase 2 a child lost the parent's admission hook, which was a real
	// path around the workspace boundary. The child must inherit it.
	childTool := newScriptedTool("write", "wrote")
	parent := Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: oneCallStream("write", map[string]any{"value": "x"}),
		Tools:  []Tool{childTool},
		Admit:  func(ai.ToolCall, Tool) (bool, string) { return false, "capability denied" },
	}
	if _, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "do a thing",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{childTool},
	}); err != nil {
		t.Logf("RunSubagent: %v", err)
	}
	if got := len(childTool.invoked()); got != 0 {
		t.Fatalf("a child bypassed the inherited admission hook (%d invocations)", got)
	}
}

func TestSubagentInheritsTheAuthorizer(t *testing.T) {
	// A delegated child must cross the same boundary as its parent. A child
	// that lost the authorizer would be an execution path around policy.
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{Outcome: security.OutcomeDeny, Rule: security.RuleCapabilityNotPermitted}}}
	childTool := newScriptedTool("bash", "ran")

	parent := Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    oneCallStream("bash", map[string]any{"value": "x"}),
		Tools:     []Tool{childTool},
		Authorize: authorizer,
	}
	if _, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "do a thing",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{childTool},
	}); err != nil {
		// The child may fail for producing no text; the authorization is what
		// this test observes.
		t.Logf("RunSubagent: %v", err)
	}
	if got := len(childTool.invoked()); got != 0 {
		t.Fatalf("a denied child tool executed %d times", got)
	}
	if evaluated, _ := authorizer.counts(); evaluated == 0 {
		t.Fatal("the child never consulted the inherited authorizer")
	}
}
