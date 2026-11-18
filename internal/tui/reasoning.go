package tui

import (
	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// ReasoningSelector resolves the reasoning setting for one turn.
//
// The policy belongs to the control plane and is supplied by the host; the
// program only applies the returned value to the loop before the request.
// Returning (false, "") omits reasoning configuration rather than inventing a
// level. text is the user's request and request is the turn's assembled
// messages, so a host can classify the work and measure the context it is about
// to send.
type ReasoningSelector func(intent workspace.IntentContract, text string, request []agent.Message) (bool, string)

// SetReasoningSelector installs the per-turn reasoning policy. Nil keeps the
// role's configured reasoning exactly as before.
func (p *Program) SetReasoningSelector(selectReasoning ReasoningSelector) {
	p.reasoningSelect = selectReasoning
}

// applyReasoning asks the host for this turn's reasoning setting and applies it
// to the live loop. It is called after the task contract is resolved, so an
// adopted objective and a persisted task both participate in the demand.
func (p *Program) applyReasoning(intent workspace.IntentContract, text string, request []agent.Message) {
	if p.reasoningSelect == nil || p.loop == nil {
		return
	}
	enabled, level := p.reasoningSelect(intent, text, request)
	p.loop.SetReasoning(enabled, level)
	if enabled {
		p.PublishEffort(level)
		return
	}
	p.PublishEffort("")
}
