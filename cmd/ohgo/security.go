package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

// workspaceAuthority adapts the workspace model to security.Authority.
//
// It delegates to workspace.Permits, so there is exactly one permission table
// in the runtime: the security kernel asks the authority, it does not restate
// its rules.
type workspaceAuthority struct {
	ws workspace.Workspace
}

// Name implements security.Authority.
func (a workspaceAuthority) Name() string { return a.ws.String() }

// Permits implements security.Authority.
func (a workspaceAuthority) Permits(cap security.Capability) bool {
	op, ok := operationForCapability(cap)
	if !ok {
		return false
	}
	return a.ws.Permits(op)
}

// operationForCapability maps a security capability to the workspace operation
// it exercises.
//
// Network is classified as a read because that is the authority it consumes
// today (a fetch observes a remote resource); when a workspace gains a distinct
// egress policy this is the one place to change.
func operationForCapability(cap security.Capability) (workspace.Operation, bool) {
	switch cap {
	case security.CapabilityRead:
		return workspace.OpRead, true
	case security.CapabilityWrite:
		return workspace.OpWrite, true
	case security.CapabilityExec:
		return workspace.OpExec, true
	case security.CapabilityNetwork:
		return workspace.OpRead, true
	case security.CapabilityExternalSearch, security.CapabilityExternalMCPRead,
		security.CapabilityExternalBrowserRead:
		// Reading external knowledge observes a remote resource. It is not a
		// repository read, but it consumes the same read authority today.
		return workspace.OpRead, true
	case security.CapabilityExternalMCPExecute:
		// Execution outside the workspace is execution, not a repository
		// write. FUZZ and BUILD may run it after approval; ASK may not.
		return workspace.OpExec, true
	case security.CapabilityExternalMCPMutate, security.CapabilityExternalBrowserMutate:
		// External mutation requires the strongest authority. It is a distinct
		// capability (and a distinct approval), never a silent extension of a
		// read.
		return workspace.OpWrite, true
	}
	return "", false
}

// securityGate is the runtime's agent.Authorizer: the one boundary every tool
// call crosses before execution.
//
// It derives a request from trusted runtime classification, evaluates the
// policy, and — only for an APPROVE — obtains a trusted human approval and
// records a narrow grant. The model never supplies a capability, a risk, or an
// authorization flag; those are computed here.
type securityGate struct {
	root   string
	policy security.Policy
	grants *security.GrantStore

	mu sync.Mutex
	// authority resolves the execution authority at call time, so an explicit
	// workspace switch moves the gate without rebuilding the loop.
	authority func() workspace.Workspace
	// fallback is the authority used when no live gate resolves one. It is
	// what a bare harness (no authorityGate) updates on a switch.
	fallback workspace.Workspace
	// ask is the trusted human interaction behind APPROVE. Nil means "no way
	// to approve", which fails closed.
	ask func(ai.ToolCall, agent.Approval) (bool, string)
	// autoApprove records a user decision to skip approval prompts (--yes). It
	// never bypasses DENY: policy is still evaluated first.
	autoApprove bool
	settings    *config.Settings
	// taskID reports the current task identity, when the task layer knows one.
	taskID func() string
	// sessionID binds grants to the session.
	sessionID string
}

// SetAsk installs the trusted human approval interaction after construction.
//
// The UI owns this callback and is built after the loop, so the gate exposes a
// setter rather than taking it at construction. It must be set before any call
// can reach an APPROVE decision.
func (g *securityGate) SetAsk(ask func(ai.ToolCall, agent.Approval) (bool, string)) {
	g.mu.Lock()
	g.ask = ask
	g.mu.Unlock()
}

// SetSession binds grants to a session identity.
func (g *securityGate) SetSession(id string) {
	g.mu.Lock()
	g.sessionID = id
	g.mu.Unlock()
}

// SetFallback moves the authority used when no live gate resolves one.
func (g *securityGate) SetFallback(ws workspace.Workspace) {
	g.mu.Lock()
	g.fallback = ws
	g.mu.Unlock()
}

// SetTaskSource installs the task-identity source used to bind grants.
func (g *securityGate) SetTaskSource(source func() string) {
	g.mu.Lock()
	g.taskID = source
	g.mu.Unlock()
}

// Evaluate implements agent.Authorizer.
func (g *securityGate) Evaluate(call ai.ToolCall, tool agent.Tool) security.Decision {
	taskID, sessionID := g.identity()
	req := securityRequest(g.root, tool, call.Arguments, call.ID, taskID, sessionID)

	// Settings are trusted policy: a user who denied a tool is never asked
	// again, and an allowed one skips the prompt. Authority is still enforced
	// first, so a settings entry can never widen the workspace boundary.
	if g.settings != nil {
		if policy, ok := g.settings.ApprovalFor(call.Name); ok {
			base := g.policy.Evaluate(req, g.authorityInForce(), nil)
			if base.Outcome == security.OutcomeDeny {
				return base
			}
			switch policy {
			case "deny":
				base.Outcome = security.OutcomeDeny
				base.Rule = "settings_deny"
				base.Reason = "denied by configuration"
			case "allow":
				base.Outcome = security.OutcomeAuto
				base.Rule = "settings_allow"
				base.Reason = "allowed by configuration"
			}
			return base
		}
	}

	grant, ok := g.grants.Consume(req, time.Now())
	if !ok {
		return g.policy.Evaluate(req, g.authorityInForce(), nil)
	}
	return g.policy.Evaluate(req, g.authorityInForce(), &grant)
}

// Approve implements agent.Authorizer.
func (g *securityGate) Approve(call ai.ToolCall, decision security.Decision) (security.Grant, bool) {
	g.mu.Lock()
	auto := g.autoApprove
	ask := g.ask
	g.mu.Unlock()

	// Generate the mutation preview from the trusted execution layer before
	// any human interaction. A mutation that cannot be previewed fails closed
	// rather than being approved on metadata alone.
	preview, applicable, err := tools.MutationPreviewForTool(call.Name, g.root, call.Arguments)
	if applicable && err != nil {
		return security.Grant{}, false
	}
	if !applicable {
		// Non-file mutations (for example a shell command) have no textual
		// diff to show; record the command as the preview so the prompt is
		// never empty for a mutating operation.
		if decision.Mutates {
			summary := ""
			if cmd, ok := call.Arguments["command"].(string); ok {
				summary = cmd
			}
			if summary == "" {
				summary = call.Name
			}
			preview = agent.MutationPreview{
				Path:         call.Name,
				Operation:    agent.MutationModify,
				MutationType: agent.MutationModify,
				Type:         agent.MutationModify,
				Op:           agent.MutationModify,
				Diff:         summary,
				Delta:        summary,
				Content:      summary,
				Text:         summary,
				Unsupported:  true,
			}
		}
	}

	if !auto {
		if ask == nil {
			return security.Grant{}, false
		}
		approved, _ := ask(call, approvalFor(decision, preview))
		if !approved {
			return security.Grant{}, false
		}
	}
	grant := g.grantFor(decision, call.ID)
	g.grants.Add(grant)
	return grant, true
}

// authorityInForce resolves the authority for this decision.
func (g *securityGate) authorityInForce() security.Authority {
	g.mu.Lock()
	fn := g.authority
	fallback := g.fallback
	g.mu.Unlock()
	if fn != nil {
		if ws := fn(); ws.Valid() {
			return workspaceAuthority{ws}
		}
	}
	if fallback.Valid() {
		return workspaceAuthority{fallback}
	}
	// An unresolved authority permits nothing.
	return workspaceAuthority{ws: ""}
}

// identity returns the task and session bindings for a request.
func (g *securityGate) identity() (taskID, sessionID string) {
	g.mu.Lock()
	source := g.taskID
	sessionID = g.sessionID
	g.mu.Unlock()
	if source != nil {
		taskID = strings.TrimSpace(source())
	}
	return taskID, sessionID
}

// grantFor records the authority one approval established.
func (g *securityGate) grantFor(decision security.Decision, operationID string) security.Grant {
	taskID, sessionID := g.identity()
	return security.Grant{
		Capability:  decision.Capability,
		Scope:       decision.Scope,
		TaskID:      taskID,
		SessionID:   sessionID,
		OperationID: operationID,
		Provenance:  "human approval",
	}
}

// escapeAuthority is the authority for a human-initiated shell escape.
//
// A human typing "!command" is acting directly, so execution is permitted in
// every workspace; mutation is not, because a read-only workspace means the
// repository is not changed by anyone. It is a distinct authority on purpose:
// the agent's authority refuses exec in ASK, which a human escape must allow.
type escapeAuthority struct {
	ws workspace.Workspace
}

// Name implements security.Authority.
func (a escapeAuthority) Name() string { return a.ws.String() + "+escape" }

// Permits implements security.Authority.
func (a escapeAuthority) Permits(cap security.Capability) bool {
	if !a.ws.Valid() {
		return false
	}
	switch cap {
	case security.CapabilityRead, security.CapabilityExec, security.CapabilityNetwork:
		return true
	case security.CapabilityWrite:
		return a.ws == workspace.Build
	}
	return false
}

// EvaluateCommand evaluates a human-initiated shell escape.
//
// The command is classified exactly as a bash tool call would be — exec with
// the command's mutation and destructive flags — so a denied escape and a
// denied tool call cannot disagree. The only difference is the authority: a
// human may execute in ASK.
func (g *securityGate) EvaluateCommand(command string) security.Decision {
	scope := security.NormalizeScope(g.root, ".")
	mutates := tools.CommandMutates(command)
	req := security.Request{
		Capability: security.CapabilityExec,
		Scope:      scope,
		Risk:       security.ClassifyRisk(security.CapabilityExec, scope, mutates, tools.CommandDestructive(command)),
		Mutates:    mutates,
		ToolName:   "shell",
		Summary:    command,
	}
	req.TaskID, req.SessionID = g.identity()
	ws := g.authorityInForce()
	return g.policy.Evaluate(req, escapeAuthority{ws: workspaceOf(ws)}, nil)
}

// workspaceOf recovers the workspace from an authority produced by this
// package, so the escape authority can reuse the same live value.
func workspaceOf(a security.Authority) workspace.Workspace {
	if w, ok := a.(workspaceAuthority); ok {
		return w.ws
	}
	return ""
}

// approvalFor projects a security decision into the UI's approval vocabulary.
//
// The UI is a projection of the decision, never the policy: it receives the
// capability, scope, risk, and reason the runtime computed, so a human approves
// what the runtime will actually do.
func approvalFor(decision security.Decision, preview agent.MutationPreview) agent.Approval {
	tier := agent.TierRead
	switch decision.Capability {
	case security.CapabilityWrite:
		tier = agent.TierWrite
	case security.CapabilityExec:
		tier = agent.TierExec
	case security.CapabilityExternalMCPExecute:
		tier = agent.TierExec
	case security.CapabilityExternalMCPMutate, security.CapabilityExternalBrowserMutate:
		tier = agent.TierWrite
	}
	if decision.Mutates {
		tier = agent.TierWrite
	}
	var b strings.Builder
	fmt.Fprintf(&b, "capability: %s\n", decision.Capability)
	fmt.Fprintf(&b, "scope: %s\n", scopeLabel(decision.Scope))
	fmt.Fprintf(&b, "risk: %s\n", decision.Risk)
	if decision.ExternalMutation {
		b.WriteString("mutation: external (not a repository change)\n")
	}
	fmt.Fprintf(&b, "reason: %s", decision.Reason)
	// Normalize alias fields on the preview.
	preview.MutationType = preview.Operation
	preview.Type = preview.Operation
	preview.Op = preview.Operation
	preview.Delta = preview.Diff
	preview.Content = preview.Diff
	preview.Text = preview.Diff
	return agent.Approval{
		Tier:            tier,
		Reason:          b.String(),
		Policy:          agent.PolicyPrompt,
		Preview:         preview.Diff,
		Diff:            preview.Diff,
		MutationPreview: preview,
		PreviewMutation: preview,
	}
}

// scopeLabel renders a scope for the approval prompt.
func scopeLabel(scope security.Scope) string {
	switch scope.Class {
	case security.ScopeInside:
		return "workspace"
	case security.ScopeOutside:
		return "outside workspace: " + scope.Path
	case security.ScopeExternal:
		return "external: " + scope.Path
	default:
		return "workspace"
	}
}

// securityRequest builds the trusted request for one tool call.
//
// Everything policy evaluates is derived here: the capability from the tool's
// declared interface, the mutation flag from the per-call operation, the
// normalized scope from the arguments, and the risk from all of them. The
// model's arguments influence the operation but never the classification.
func securityRequest(root string, tool agent.Tool, args map[string]any, operationID, taskID, sessionID string) security.Request {
	cap := capabilityForTier(tool.Approval().Tier)
	// Capability is the declared authority class; a per-call CapabilityReporter
	// refines the operation (and therefore mutation risk), not the process/
	// filesystem taxonomy. A shell that writes is still process execution.
	// An invocation-aware namer may pick a different security capability from
	// trusted configuration, for example MCP execute versus MCP mutate.
	if resolved, ok := agent.SecurityCapabilityFor(tool, args); ok {
		cap = resolved
	}
	op := workspace.OperationForTool(tool, args)
	mutates := op.Mutates()

	scope := scopeFor(cap, root, args)
	destructive := cap == security.CapabilityExec && tools.CommandDestructive(argString(args, "command"))
	risk := security.ClassifyRisk(cap, scope, mutates, destructive)

	return security.Request{
		Capability:  cap,
		Scope:       scope,
		Risk:        risk,
		Mutates:     mutates,
		ToolName:    tool.Name(),
		OperationID: operationID,
		Summary:     summarize(cap, args),
		TaskID:      taskID,
		SessionID:   sessionID,
	}
}

// capabilityForTier maps a declared approval tier to a capability. An unknown
// tier maps to an empty capability, which policy denies.
func capabilityForTier(tier agent.Tier) security.Capability {
	switch tier {
	case agent.TierRead:
		return security.CapabilityRead
	case agent.TierWrite:
		return security.CapabilityWrite
	case agent.TierExec:
		return security.CapabilityExec
	}
	return ""
}

// scopeFor derives the normalized scope of an operation from its arguments.
func scopeFor(cap security.Capability, root string, args map[string]any) security.Scope {
	switch cap {
	case security.CapabilityRead, security.CapabilityWrite:
		return security.NormalizeScope(root, argString(args, "path"))
	case security.CapabilityExec:
		// A command runs in the workspace, so its scope is the workspace; the
		// command itself is the operation's summary and risk input.
		return security.NormalizeScope(root, ".")
	case security.CapabilityNetwork:
		raw := argString(args, "url")
		host := ""
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil {
			host = parsed.Host
		}
		return security.ExternalScope(raw, host)
	case security.CapabilityExternalSearch:
		provider := strings.TrimSpace(argString(args, "provider"))
		if provider == "" {
			provider = "default"
		}
		return security.ExternalScope("search:"+provider, "search:"+provider)
	case security.CapabilityExternalMCPRead, security.CapabilityExternalMCPMutate,
		security.CapabilityExternalMCPExecute:
		server, _ := mcpTarget(args)
		if server == "" {
			server = "any"
		}
		return security.ExternalScope("mcp:"+server, "mcp:"+server)
	case security.CapabilityExternalBrowserRead, security.CapabilityExternalBrowserMutate:
		raw := strings.TrimSpace(argString(args, "url"))
		host := ""
		if parsed, err := url.Parse(raw); err == nil {
			host = parsed.Host
		}
		if host == "" {
			session := strings.TrimSpace(argString(args, "session"))
			if session == "" {
				session = "default"
			}
			host = "session:" + session
		}
		return security.ExternalScope(raw, host)
	}
	return security.Scope{Class: security.ScopeUnspecified}
}

// summarize describes the specific operation, for the audit record.
func summarize(cap security.Capability, args map[string]any) string {
	switch cap {
	case security.CapabilityExec:
		return argString(args, "command")
	case security.CapabilityRead, security.CapabilityWrite:
		return argString(args, "path")
	case security.CapabilityNetwork:
		return argString(args, "url")
	case security.CapabilityExternalSearch:
		query := argString(args, "query")
		provider := argString(args, "provider")
		if provider == "" {
			provider = "default"
		}
		return provider + ": " + query
	case security.CapabilityExternalMCPRead, security.CapabilityExternalMCPMutate,
		security.CapabilityExternalMCPExecute:
		server, tool := mcpTarget(args)
		operation := argString(args, "operation")
		resource := argString(args, "resource")
		prompt := argString(args, "prompt")
		parts := []string{"server=" + server}
		if operation != "" {
			parts = append(parts, "operation="+operation)
		}
		if tool != "" {
			parts = append(parts, "tool="+tool)
		}
		if resource != "" {
			parts = append(parts, "resource="+resource)
		}
		if prompt != "" {
			parts = append(parts, "prompt="+prompt)
		}
		return strings.Join(parts, " ")
	case security.CapabilityExternalBrowserRead, security.CapabilityExternalBrowserMutate:
		operation := argString(args, "operation")
		url := argString(args, "url")
		session := argString(args, "session")
		parts := []string{"operation=" + operation}
		if url != "" {
			parts = append(parts, "url="+url)
		}
		if session != "" {
			parts = append(parts, "session="+session)
		}
		if index, ok := args["index"].(float64); ok {
			parts = append(parts, fmt.Sprintf("index=%d", int(index)))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// mcpTarget resolves a server and tool from either the stable identity or the
// separate arguments. It is display/scope metadata only, never authority.
func mcpTarget(args map[string]any) (string, string) {
	server := strings.TrimSpace(argString(args, "server"))
	tool := strings.TrimSpace(argString(args, "tool"))
	capability := strings.TrimSpace(argString(args, "capability"))
	if capability != "" {
		if capServer, capTool, ok := strings.Cut(capability, ":"); ok {
			if server == "" {
				server = strings.TrimSpace(capServer)
			}
			if tool == "" {
				tool = strings.TrimSpace(capTool)
			}
		}
	}
	return server, tool
}

// argString reads a string argument, tolerating any type.
func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key].(string)
	if !ok {
		return ""
	}
	return value
}

// newSecurityGate builds the gate for a session.
//
// It binds the live authority (via opts.gate), the settings overrides, and the
// session identity. ask may be nil for a caller that installs the UI callback
// later; a nil ask fails APPROVE closed until it is set.
func (o options) newSecurityGate(ask func(ai.ToolCall, agent.Approval) (bool, string)) *securityGate {
	return &securityGate{
		root:        o.root,
		grants:      security.NewGrantStore(),
		authority:   o.authority,
		fallback:    o.authority(),
		ask:         ask,
		autoApprove: o.autoApprove,
		settings:    &o.settings,
	}
}
