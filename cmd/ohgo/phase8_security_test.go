package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

func externalTestCall(id, name string, args map[string]any) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Arguments: args}
}

// Search is an external read: medium risk, external scope, no workspace
// mutation flag.
func TestSecurityRequestClassifiesSearch(t *testing.T) {
	root := t.TempDir()
	req := securityRequest(root, tools.NewSearchTool(nil),
		map[string]any{"query": "go", "provider": "brave"}, "s1", "", "")
	if req.Capability != security.CapabilityExternalSearch {
		t.Fatalf("capability = %s", req.Capability)
	}
	if req.Scope.Class != security.ScopeExternal || req.Scope.Path != "search:brave" {
		t.Fatalf("scope = %+v", req.Scope)
	}
	if req.Risk != security.RiskMedium || req.Mutates {
		t.Fatalf("risk/mutates = %s/%v", req.Risk, req.Mutates)
	}
}

// MCP mutation is a distinct external capability and requires BUILD plus
// approval. It is not reported as a repository mutation.
func TestSecurityRequestClassifiesMCPMutationAsExternal(t *testing.T) {
	root := t.TempDir()
	callTool := tools.NewMCPCallTool(nil)
	req := securityRequest(root, callTool,
		map[string]any{"server": "alpha", "tool": "send"}, "m1", "", "")
	if req.Capability != security.CapabilityExternalMCPMutate {
		t.Fatalf("capability = %s", req.Capability)
	}
	if req.Scope.Class != security.ScopeExternal || req.Scope.Path != "mcp:alpha" {
		t.Fatalf("scope = %+v", req.Scope)
	}
	if req.Risk != security.RiskHigh || !req.Mutates {
		t.Fatalf("risk/mutates = %s/%v, want HIGH/true", req.Risk, req.Mutates)
	}

	decision := phase6Gate(root, workspace.Build).Evaluate(
		externalTestCall("m1", "mcp_call", map[string]any{"server": "alpha", "tool": "send"}), callTool)
	if decision.Outcome != security.OutcomeApprove {
		t.Fatalf("outcome = %s (%s), want APPROVE", decision.Outcome, decision.Reason)
	}
	gate := phase6Gate(root, workspace.Build)
	decision = gate.Evaluate(externalTestCall("m1", "mcp_call", map[string]any{"server": "alpha", "tool": "send"}), callTool)
	if _, ok := gate.Approve(externalTestCall("m1", "mcp_call", map[string]any{"server": "alpha", "tool": "send"}), decision); !ok {
		t.Fatal("approval was refused")
	}
	decision = gate.Evaluate(externalTestCall("m1", "mcp_call", map[string]any{"server": "alpha", "tool": "send"}), callTool)
	if decision.Outcome != security.OutcomeAuto {
		t.Fatalf("confirmed outcome = %s (%s), want AUTO", decision.Outcome, decision.Reason)
	}
	if decision.Mutates {
		t.Fatal("external MCP mutation was counted as a workspace mutation")
	}
	if !decision.ExternalMutation {
		t.Fatal("external MCP mutation was not marked external")
	}

	ask := phase6Gate(root, workspace.Ask).Evaluate(
		externalTestCall("m1", "mcp_call", map[string]any{"server": "alpha", "tool": "send"}), callTool)
	if ask.Outcome != security.OutcomeDeny {
		t.Fatalf("ASK outcome = %s, want DENY", ask.Outcome)
	}
}

func TestSecurityRequestClassifiesBrowserRead(t *testing.T) {
	root := t.TempDir()
	browser := tools.NewBrowserTool(tools.BrowserConfig{})
	req := securityRequest(root, browser,
		map[string]any{"operation": "open", "url": "https://example.com/a"}, "b1", "", "")
	if req.Capability != security.CapabilityExternalBrowserRead {
		t.Fatalf("capability = %s", req.Capability)
	}
	if req.Scope.Class != security.ScopeExternal || req.Scope.Path != "example.com" {
		t.Fatalf("scope = %+v", req.Scope)
	}
	if req.Risk != security.RiskMedium || req.Mutates {
		t.Fatalf("risk/mutates = %s/%v", req.Risk, req.Mutates)
	}
}
