package main

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/security"
	"github.com/PizenLabs/ohgo/mcp"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// authorityMCP is a manager whose classification is configuration, not model
// input. It is enough to exercise the security seam without a subprocess.
type authorityMCP struct {
	classes map[string]mcp.Classification
}

func (m authorityMCP) Names() []string             { return []string{"alpha"} }
func (m authorityMCP) Configured(name string) bool { return name == "alpha" }
func (m authorityMCP) Classify(name, tool string) (mcp.Classification, bool) {
	class, ok := m.classes[tool]
	if !ok {
		return mcp.ClassUnknown, true
	}
	return class, true
}
func (m authorityMCP) Discover(context.Context, string, mcp.Meter) (mcp.Discovery, error) {
	return mcp.Discovery{}, nil
}
func (m authorityMCP) Refresh(context.Context, string, mcp.Meter) (mcp.Discovery, error) {
	return mcp.Discovery{}, nil
}
func (m authorityMCP) ReadResource(context.Context, string, string, mcp.Meter) (mcp.ResourceResult, error) {
	return mcp.ResourceResult{}, nil
}
func (m authorityMCP) GetPrompt(context.Context, string, string, map[string]any, mcp.Meter) (mcp.PromptResult, error) {
	return mcp.PromptResult{}, nil
}
func (m authorityMCP) CallTool(context.Context, string, string, map[string]any, mcp.Mode, mcp.Meter) (mcp.ToolResult, error) {
	return mcp.ToolResult{}, nil
}
func (m authorityMCP) Close() error { return nil }

// Execute-class MCP tools consume execution authority, not repository write
// authority. FUZZ may execute after approval; ASK may not.
func TestMCPExecuteUsesExecutionAuthority(t *testing.T) {
	execute := security.CapabilityExternalMCPExecute
	mutate := security.CapabilityExternalMCPMutate
	if !(workspaceAuthority{workspace.Fuzz}).Permits(execute) {
		t.Error("FUZZ was refused execute-class MCP authority")
	}
	if (workspaceAuthority{workspace.Fuzz}).Permits(mutate) {
		t.Error("FUZZ was granted MCP mutation authority")
	}
	if (workspaceAuthority{workspace.Ask}).Permits(execute) {
		t.Error("ASK was granted execute-class MCP authority")
	}
	if !(workspaceAuthority{workspace.Ask}).Permits(security.CapabilityExternalMCPRead) {
		t.Error("ASK was refused MCP read authority")
	}
	if !(workspaceAuthority{workspace.Build}).Permits(mutate) {
		t.Error("BUILD was refused MCP mutation authority")
	}
}

// The security request carries the configured class, so an execute tool is not
// misclassified as a workspace mutation.
func TestMCPExecuteSecurityRequestIsNotWorkspaceMutation(t *testing.T) {
	manager := authorityMCP{classes: map[string]mcp.Classification{
		"run_query":    mcp.ClassExecute,
		"create_issue": mcp.ClassMutate,
	}}
	tool := tools.NewMCPCallTool(manager)
	root := t.TempDir()

	execute := securityRequest(root, tool, map[string]any{
		"server": "alpha", "tool": "run_query",
	}, "op-exec", "", "session")
	if execute.Capability != security.CapabilityExternalMCPExecute {
		t.Fatalf("capability = %s, want external.mcp.execute", execute.Capability)
	}
	if execute.Mutates {
		t.Fatal("execute-class MCP was classified as repository mutation")
	}
	decision := (security.Policy{}).Evaluate(execute, workspaceAuthority{workspace.Fuzz}, nil)
	if decision.Outcome == security.OutcomeDeny {
		t.Fatalf("FUZZ execute decision = %+v, want approval", decision)
	}

	mutation := securityRequest(root, tool, map[string]any{
		"server": "alpha", "tool": "create_issue",
	}, "op-mutate", "", "session")
	if mutation.Capability != security.CapabilityExternalMCPMutate {
		t.Fatalf("capability = %s, want external.mcp.mutate", mutation.Capability)
	}
	decision = (security.Policy{}).Evaluate(mutation, workspaceAuthority{workspace.Fuzz}, nil)
	if decision.Outcome != security.OutcomeDeny {
		t.Fatalf("FUZZ mutation decision = %+v, want DENY", decision)
	}
}

// An unresolved tool class stays at the mutation boundary.
func TestMCPUnknownClassFailsClosedToMutation(t *testing.T) {
	manager := authorityMCP{classes: map[string]mcp.Classification{}}
	tool := tools.NewMCPCallTool(manager)
	capability := tool.SecurityCapabilityFor(map[string]any{"server": "alpha", "tool": "mystery"})
	if capability != security.CapabilityExternalMCPMutate {
		t.Fatalf("unknown class capability = %s, want the mutation boundary", capability)
	}
	if tier := tool.Capability(map[string]any{"server": "alpha", "tool": "mystery"}); tier != agent.TierWrite {
		t.Fatalf("unknown class tier = %s, want write", tier)
	}
}
