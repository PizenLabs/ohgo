package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// gateFor builds a security gate under a workspace authority.
func gateFor(t *testing.T, ws workspace.Workspace, ask func(ai.ToolCall, agent.Approval) (bool, string)) (*securityGate, string) {
	t.Helper()
	root := t.TempDir()
	opts := options{root: root, settings: config.Default()}
	opts.setAuthority(ws)
	return opts.newSecurityGate(ask), root
}

func secReadCall(path string) ai.ToolCall {
	return ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": path}}
}

func secWriteCall(path string) ai.ToolCall {
	return ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{"path": path, "content": "x"}}
}

func secBashCall(command string) ai.ToolCall {
	return ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"command": command}}
}

func TestSecurityRequestClassifiesRealTools(t *testing.T) {
	root := t.TempDir()
	gate, _ := gateFor(t, workspace.Build, nil)
	_ = gate

	cases := []struct {
		name    string
		tool    agent.Tool
		call    ai.ToolCall
		cap     security.Capability
		risk    security.Risk
		mutates bool
		class   security.ScopeClass
	}{
		{"read", tools.NewReadTool(root), secReadCall("a.go"), security.CapabilityRead, security.RiskLow, false, security.ScopeInside},
		{"write", tools.NewWriteTool(root), secWriteCall("a.go"), security.CapabilityWrite, security.RiskMedium, true, security.ScopeInside},
		{"bash ordinary", tools.NewBashTool(root), secBashCall("go test ./..."), security.CapabilityExec, security.RiskMedium, false, security.ScopeInside},
		{"bash mutating", tools.NewBashTool(root), secBashCall("rm -rf internal"), security.CapabilityExec, security.RiskHigh, true, security.ScopeInside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := securityRequest(root, tc.tool, tc.call.Arguments, tc.call.ID, "", "")
			if req.Capability != tc.cap {
				t.Errorf("capability = %s, want %s", req.Capability, tc.cap)
			}
			if req.Risk != tc.risk {
				t.Errorf("risk = %s, want %s", req.Risk, tc.risk)
			}
			if req.Mutates != tc.mutates {
				t.Errorf("mutates = %v, want %v", req.Mutates, tc.mutates)
			}
			if req.Scope.Class != tc.class {
				t.Errorf("scope class = %s, want %s", req.Scope.Class, tc.class)
			}
		})
	}
}

func TestSecurityRequestIgnoresModelSuppliedSecurityFields(t *testing.T) {
	// A model that supplies "approved", "risk", or "capability" in its
	// arguments changes nothing: classification is derived from the tool and
	// the operation, never from model-authored fields.
	root := t.TempDir()
	call := ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{
		"command":    "rm -rf internal",
		"approved":   true,
		"risk":       "LOW",
		"capability": "filesystem.read",
		"scope":      "workspace",
	}}
	req := securityRequest(root, tools.NewBashTool(root), call.Arguments, call.ID, "", "")
	if req.Capability != security.CapabilityExec {
		t.Errorf("capability = %s, want exec (model field ignored)", req.Capability)
	}
	if req.Risk != security.RiskHigh {
		t.Errorf("risk = %s, want HIGH (model field ignored)", req.Risk)
	}
	if !req.Mutates {
		t.Error("mutates = false, want true (model field ignored)")
	}
	if req.Scope.Class != security.ScopeInside {
		t.Errorf("scope = %s, want workspace (model field ignored)", req.Scope.Class)
	}
}

func TestSecurityRequestClassifiesNetwork(t *testing.T) {
	root := t.TempDir()
	req := securityRequest(root, tools.NewWebFetchTool(),
		map[string]any{"url": "https://example.com/page"}, "c1", "", "")
	if req.Capability != security.CapabilityNetwork {
		t.Fatalf("capability = %s, want network", req.Capability)
	}
	if req.Scope.Class != security.ScopeExternal || req.Scope.Path != "example.com" {
		t.Errorf("scope = %+v, want external example.com", req.Scope)
	}
	if req.Risk != security.RiskMedium {
		t.Errorf("risk = %s, want MEDIUM", req.Risk)
	}
}

func TestGateAutoForLowRiskRead(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, nil)
	d := gate.Evaluate(secReadCall("a.go"), tools.NewReadTool(root))
	if d.Outcome != security.OutcomeAuto {
		t.Fatalf("outcome = %s (%s), want AUTO", d.Outcome, d.Reason)
	}
}

func TestGateApproveForWorkspaceWrite(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, nil)
	d := gate.Evaluate(secWriteCall("a.go"), tools.NewWriteTool(root))
	if d.Outcome != security.OutcomeApprove {
		t.Fatalf("outcome = %s, want APPROVE", d.Outcome)
	}
	if d.Risk != security.RiskMedium {
		t.Errorf("risk = %s, want MEDIUM", d.Risk)
	}
}

func TestGateDeniesPathOutsideWorkspace(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, nil)
	d := gate.Evaluate(secWriteCall("../escape.go"), tools.NewWriteTool(root))
	if d.Outcome != security.OutcomeDeny || d.Rule != security.RuleOutsideWorkspace {
		t.Fatalf("decision = %+v, want DENY/%s", d, security.RuleOutsideWorkspace)
	}
}

func TestGateDeniesMutationOutsideAuthority(t *testing.T) {
	// FUZZ may execute but not change the repository. A mutating shell command
	// is exec by capability and must still be refused before approval.
	gate, root := gateFor(t, workspace.Fuzz, nil)
	d := gate.Evaluate(secBashCall("rm -rf internal"), tools.NewBashTool(root))
	if d.Outcome != security.OutcomeDeny || d.Rule != security.RuleMutationNotPermitted {
		t.Fatalf("decision = %+v, want DENY/%s", d, security.RuleMutationNotPermitted)
	}
}

// An execution the classifier does not recognise as mutating is still marked
// read-only under FUZZ. The mark is what the bash tool turns into an OS
// sandbox, so the boundary does not depend on the classifier being right.
func TestGateMarksFuzzExecReadOnly(t *testing.T) {
	gate, root := gateFor(t, workspace.Fuzz, nil)
	d := gate.Evaluate(secBashCall(`python3 -c "open('f','w')"`), tools.NewBashTool(root))
	if d.Outcome != security.OutcomeApprove {
		t.Fatalf("outcome = %s, want APPROVE", d.Outcome)
	}
	if !d.ReadOnly {
		t.Fatalf("FUZZ exec was not marked read-only: %+v", d)
	}

	build, buildRoot := gateFor(t, workspace.Build, nil)
	d = build.Evaluate(secBashCall("go test ./..."), tools.NewBashTool(buildRoot))
	if d.ReadOnly {
		t.Fatalf("BUILD exec was marked read-only: %+v", d)
	}
}

// The human shell escape crosses the same decision, so a read-only authority
// also sandboxes it.
func TestGateMarksFuzzEscapeReadOnly(t *testing.T) {
	fuzz, _ := gateFor(t, workspace.Fuzz, nil)
	if d := fuzz.EvaluateCommand(`python3 -c "open('f','w')"`); !d.ReadOnly {
		t.Fatalf("FUZZ escape was not marked read-only: %+v", d)
	}
	build, _ := gateFor(t, workspace.Build, nil)
	if d := build.EvaluateCommand("go test ./..."); d.ReadOnly {
		t.Fatalf("BUILD escape was marked read-only: %+v", d)
	}
}

func TestGateApproveRecordsANarrowGrant(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, func(ai.ToolCall, agent.Approval) (bool, string) {
		return true, ""
	})
	call := secWriteCall("a.go")
	tool := tools.NewWriteTool(root)

	d := gate.Evaluate(call, tool)
	if d.Outcome != security.OutcomeApprove {
		t.Fatalf("first outcome = %s, want APPROVE", d.Outcome)
	}
	if _, ok := gate.Approve(call, d); !ok {
		t.Fatal("approval was refused")
	}
	confirmed := gate.Evaluate(call, tool)
	if confirmed.Outcome != security.OutcomeAuto || confirmed.Rule != security.RuleGrant {
		t.Fatalf("confirmed = %+v, want AUTO/grant", confirmed)
	}
	// The grant was one-shot: a different path is evaluated afresh.
	other := gate.Evaluate(secWriteCall("b.go"), tool)
	if other.Outcome != security.OutcomeApprove {
		t.Errorf("a grant leaked to another path: %+v", other)
	}
}

func TestGateApproveRejectedLeavesNoGrant(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, func(ai.ToolCall, agent.Approval) (bool, string) {
		return false, "denied by user"
	})
	call := secWriteCall("a.go")
	tool := tools.NewWriteTool(root)
	d := gate.Evaluate(call, tool)
	if _, ok := gate.Approve(call, d); ok {
		t.Fatal("a rejected approval produced a grant")
	}
	if again := gate.Evaluate(call, tool); again.Outcome != security.OutcomeApprove {
		t.Errorf("outcome after rejection = %s, want APPROVE (no grant)", again.Outcome)
	}
}

func TestGateWithNoApproverFailsClosed(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, nil)
	call := secWriteCall("a.go")
	d := gate.Evaluate(call, tools.NewWriteTool(root))
	if _, ok := gate.Approve(call, d); ok {
		t.Fatal("an APPROVE with no human approver was granted")
	}
}

func TestGateSettingsCanDenyAndAllow(t *testing.T) {
	root := t.TempDir()
	opts := options{root: root, settings: config.Default()}
	opts.setAuthority(workspace.Build)
	opts.settings.Approvals = map[string]string{"write": "deny"}
	gate := opts.newSecurityGate(nil)
	if d := gate.Evaluate(secWriteCall("a.go"), tools.NewWriteTool(root)); d.Outcome != security.OutcomeDeny {
		t.Fatalf("settings deny outcome = %s, want DENY", d.Outcome)
	}

	opts.settings.Approvals = map[string]string{"write": "allow"}
	gate = opts.newSecurityGate(nil)
	if d := gate.Evaluate(secWriteCall("a.go"), tools.NewWriteTool(root)); d.Outcome != security.OutcomeAuto {
		t.Fatalf("settings allow outcome = %s, want AUTO", d.Outcome)
	}
}

func TestGateSettingsCannotWidenAuthority(t *testing.T) {
	// A settings "allow" skips the prompt; it can never grant a capability the
	// workspace forbids.
	root := t.TempDir()
	opts := options{root: root, settings: config.Default()}
	opts.setAuthority(workspace.Fuzz)
	opts.settings.Approvals = map[string]string{"bash": "allow"}
	gate := opts.newSecurityGate(nil)
	d := gate.Evaluate(secBashCall("rm -rf internal"), tools.NewBashTool(root))
	if d.Outcome != security.OutcomeDeny {
		t.Fatalf("settings allow widened authority: %+v", d)
	}
}

func TestGateGrantIsBoundToTask(t *testing.T) {
	gate, root := gateFor(t, workspace.Build, func(ai.ToolCall, agent.Approval) (bool, string) {
		return true, ""
	})
	task := "task-1"
	gate.SetTaskSource(func() string { return task })

	call := secWriteCall("a.go")
	tool := tools.NewWriteTool(root)
	d := gate.Evaluate(call, tool)
	if _, ok := gate.Approve(call, d); !ok {
		t.Fatal("approval refused")
	}
	// A different task must not be covered by the first task's grant.
	task = "task-2"
	if again := gate.Evaluate(call, tool); again.Outcome != security.OutcomeApprove {
		t.Errorf("a grant crossed tasks: %+v", again)
	}
}

func TestGateAutoApproveStillDenies(t *testing.T) {
	// --yes skips the prompt, never the policy: a forbidden operation is still
	// refused.
	root := t.TempDir()
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Fuzz)
	gate := opts.newSecurityGate(nil)
	if d := gate.Evaluate(secBashCall("rm -rf internal"), tools.NewBashTool(root)); d.Outcome != security.OutcomeDeny {
		t.Fatalf("auto-approve bypassed policy: %+v", d)
	}

	opts.setAuthority(workspace.Build)
	gate = opts.newSecurityGate(nil)
	call := secWriteCall("a.go")
	d := gate.Evaluate(call, tools.NewWriteTool(root))
	if _, ok := gate.Approve(call, d); !ok {
		t.Fatal("auto-approve did not grant an approved operation")
	}
}

func TestGateEscapeCommandFollowsPolicy(t *testing.T) {
	gate, _ := gateFor(t, workspace.Build, nil)
	if d := gate.EvaluateCommand("rm -rf /tmp/x"); d.Outcome != security.OutcomeApprove {
		t.Errorf("destructive escape outcome = %s, want APPROVE", d.Outcome)
	}

	fuzz, _ := gateFor(t, workspace.Fuzz, nil)
	if d := fuzz.EvaluateCommand("rm -rf internal"); d.Outcome != security.OutcomeDeny {
		t.Errorf("mutating escape in FUZZ outcome = %s, want DENY", d.Outcome)
	}
	if d := fuzz.EvaluateCommand("go test ./..."); d.Outcome == security.OutcomeDeny {
		t.Errorf("read-only escape in FUZZ was denied: %+v", d)
	}

	ask, _ := gateFor(t, workspace.Ask, nil)
	if d := ask.EvaluateCommand("ls -la"); d.Outcome == security.OutcomeDeny {
		t.Errorf("read-only escape in ASK was denied: %+v", d)
	}
	if d := ask.EvaluateCommand("rm -rf internal"); d.Outcome != security.OutcomeDeny {
		t.Errorf("mutating escape in ASK outcome = %s, want DENY", d.Outcome)
	}
}

func TestGateUnresolvedAuthorityDeniesEverything(t *testing.T) {
	// A gate with no resolvable authority permits nothing, so a misconfigured
	// session fails closed rather than executing unguarded.
	root := t.TempDir()
	gate := &securityGate{root: root, grants: security.NewGrantStore()}
	if d := gate.Evaluate(secReadCall("a.go"), tools.NewReadTool(root)); d.Outcome != security.OutcomeDeny {
		t.Fatalf("unresolved authority outcome = %s, want DENY", d.Outcome)
	}
	if d := gate.EvaluateCommand("ls"); d.Outcome != security.OutcomeDeny {
		t.Fatalf("unresolved authority escape outcome = %s, want DENY", d.Outcome)
	}
}

func TestGateDefaultsToBuildWithoutAuthority(t *testing.T) {
	// options.authority() defaults an unset workspace to BUILD, preserving the
	// pre-workspace behavior for a caller that never chose one.
	root := t.TempDir()
	opts := options{root: root, settings: config.Default()}
	gate := opts.newSecurityGate(nil)
	if d := gate.Evaluate(secReadCall("a.go"), tools.NewReadTool(root)); d.Outcome != security.OutcomeAuto {
		t.Fatalf("defaulted authority outcome = %s, want AUTO", d.Outcome)
	}
}
