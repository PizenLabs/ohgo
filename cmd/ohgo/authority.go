package main

import (
	"sync"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/workspace"
)

// authorityGate is the session's live execution authority: the workspace whose
// policy admits a tool call at the moment the call is made.
//
// It exists because authority is a runtime property, not a construction-time
// constant. A workspace switch moves it, and a delegated informational turn
// narrows it for that turn. Both the agent loop's admission hook and the
// subagent spawner read the gate at call time, so a delegated turn's
// narrowing applies to anything the turn delegates as well: nothing the model
// emits can widen the gate (ARCHITECTURE_CONTRACT_V1 §7, §11).
type authorityGate struct {
	mu sync.RWMutex
	ws workspace.Workspace
}

// newAuthorityGate returns a gate holding ws.
func newAuthorityGate(ws workspace.Workspace) *authorityGate {
	return &authorityGate{ws: ws}
}

// Set moves the gate's authority. Only a workspace transition (SwitchWorkspace)
// or the intent router (RouteTurn) calls it; no model output and no tool can.
func (g *authorityGate) Set(ws workspace.Workspace) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.ws = ws
	g.mu.Unlock()
}

// Workspace returns the authority in force. A nil gate reports the invalid
// workspace, which the policy denies.
func (g *authorityGate) Workspace() workspace.Workspace {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ws
}

// Admit is the agent loop's runtime gate. It is the same check as admitFor,
// bound to the live authority rather than a captured one.
func (g *authorityGate) Admit(call ai.ToolCall, tool agent.Tool) (bool, string) {
	if err := (workspace.Authority{Workspace: g.Workspace()}).Admit(tool, call.Arguments); err != nil {
		return false, err.Error()
	}
	return true, ""
}
