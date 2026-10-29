package main

import (
	"net/url"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// benchmarkGate is the Phase 10 live-benchmark authorizer.
//
// It is deliberately a thin adapter over the production policy kernel and the
// production workspace permission table. It exists because the harness runs
// outside cmd/ohgo; it is not a second production control plane. Approval is
// auto-granted to model `--yes` headless operation, while DENY remains a hard
// refusal. Both benchmark arms install the same gate with the same authority,
// so a CONTROL/COGNITIVE difference cannot be explained by admission.
type benchmarkGate struct {
	root    string
	ws      workspace.Workspace
	policy  security.Policy
	grants  *security.GrantStore
	approve bool
}

// newBenchmarkGate returns an auto-approving gate for one workspace.
func newBenchmarkGate(root string, ws workspace.Workspace) *benchmarkGate {
	return &benchmarkGate{
		root:    root,
		ws:      ws,
		grants:  security.NewGrantStore(),
		approve: true,
	}
}

// Evaluate implements agent.Authorizer.
func (g *benchmarkGate) Evaluate(call ai.ToolCall, tool agent.Tool) security.Decision {
	req := benchmarkRequest(g.root, tool, call.Arguments, call.ID)
	if grant, ok := g.grants.Consume(req, time.Now()); ok {
		return g.policy.Evaluate(req, benchmarkAuthority{ws: g.ws}, &grant)
	}
	return g.policy.Evaluate(req, benchmarkAuthority{ws: g.ws}, nil)
}

// Approve implements agent.Authorizer. The benchmark represents `--yes`: the
// human decision is fixed, but it is still a real grant with the operation's
// capability and scope, and the loop still confirms it.
func (g *benchmarkGate) Approve(call ai.ToolCall, decision security.Decision) (security.Grant, bool) {
	if !g.approve {
		return security.Grant{}, false
	}
	grant := security.Grant{
		Capability:  decision.Capability,
		Scope:       decision.Scope,
		OperationID: call.ID,
		Provenance:  "benchmark auto-approval",
	}
	g.grants.Add(grant)
	return grant, true
}

// benchmarkAuthority adapts a workspace to security.Authority.
type benchmarkAuthority struct{ ws workspace.Workspace }

// Name implements security.Authority.
func (a benchmarkAuthority) Name() string { return a.ws.String() }

// Permits implements security.Authority.
func (a benchmarkAuthority) Permits(cap security.Capability) bool {
	op, ok := benchmarkOperation(cap)
	if !ok {
		return false
	}
	return a.ws.Permits(op)
}

// benchmarkOperation maps a security capability to the workspace operation it
// exercises. It mirrors cmd/ohgo's one production mapping; keeping it local
// avoids importing package main from a benchmark.
func benchmarkOperation(cap security.Capability) (workspace.Operation, bool) {
	switch cap {
	case security.CapabilityRead:
		return workspace.OpRead, true
	case security.CapabilityWrite:
		return workspace.OpWrite, true
	case security.CapabilityExec:
		return workspace.OpExec, true
	case security.CapabilityNetwork, security.CapabilityExternalSearch,
		security.CapabilityExternalMCPRead, security.CapabilityExternalBrowserRead:
		return workspace.OpRead, true
	case security.CapabilityExternalMCPExecute:
		return workspace.OpExec, true
	case security.CapabilityExternalMCPMutate, security.CapabilityExternalBrowserMutate:
		return workspace.OpWrite, true
	}
	return "", false
}

// benchmarkRequest derives the trusted policy request for one call.
func benchmarkRequest(root string, tool agent.Tool, args map[string]any, callID string) security.Request {
	cap := benchmarkCapability(tool, args)
	op := workspace.OperationForTool(tool, args)
	scope := benchmarkScope(root, cap, args)
	destructive := cap == security.CapabilityExec && tools.CommandDestructive(benchmarkString(args, "command"))
	risk := security.ClassifyRisk(cap, scope, op.Mutates(), destructive)
	return security.Request{
		Capability:  cap,
		Scope:       scope,
		Risk:        risk,
		Mutates:     op.Mutates(),
		ToolName:    tool.Name(),
		OperationID: callID,
		Summary:     benchmarkSummary(cap, args),
	}
}

// benchmarkCapability resolves the per-call capability exactly as production
// does: the declared tier is the default, a tool may refine it per invocation,
// and a capability namer wins.
func benchmarkCapability(tool agent.Tool, args map[string]any) security.Capability {
	// Mirror the production request builder: the declared tier (or an explicit
	// security capability) owns the capability taxonomy. CapabilityReporter
	// refines the operation/mutation flag, not the capability class.
	tier := tool.Approval().Tier
	cap := security.Capability("")
	switch tier {
	case agent.TierRead:
		cap = security.CapabilityRead
	case agent.TierWrite:
		cap = security.CapabilityWrite
	case agent.TierExec:
		cap = security.CapabilityExec
	}
	if resolved, ok := agent.SecurityCapabilityFor(tool, args); ok {
		cap = resolved
	}
	return cap
}

// benchmarkScope normalizes the target the policy evaluates.
func benchmarkScope(root string, cap security.Capability, args map[string]any) security.Scope {
	switch cap {
	case security.CapabilityRead, security.CapabilityWrite:
		return security.NormalizeScope(root, benchmarkString(args, "path"))
	case security.CapabilityExec:
		return security.NormalizeScope(root, ".")
	case security.CapabilityNetwork:
		raw := benchmarkString(args, "url")
		host := ""
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil {
			host = parsed.Host
		}
		return security.ExternalScope(raw, host)
	default:
		return security.Scope{Class: security.ScopeUnspecified}
	}
}

// benchmarkSummary names the operation for the decision record.
func benchmarkSummary(cap security.Capability, args map[string]any) string {
	switch cap {
	case security.CapabilityExec:
		return benchmarkString(args, "command")
	case security.CapabilityRead, security.CapabilityWrite:
		return benchmarkString(args, "path")
	case security.CapabilityNetwork:
		return benchmarkString(args, "url")
	}
	return ""
}

func benchmarkString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}
