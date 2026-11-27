package tui

import (
	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// ReasoningSelector resolves the reasoning setting for one provider request.
//
// The policy belongs to the control plane and is supplied by the host; the
// program only applies the returned value to the request. Returning
// (false, "") omits reasoning configuration rather than inventing a level.
// text is the user's request; facts carries the measured request shape and the
// run's remaining budget so the host can lower or raise effort between model
// calls.
type ReasoningSelector func(intent workspace.IntentContract, text string, facts agent.ReasoningFacts) (bool, string)

// SetReasoningSelector installs the host reasoning policy. The program applies
// it at the start of a run and again before every provider request. Nil keeps
// the role's configured reasoning exactly as before.
func (p *Program) SetReasoningSelector(selectReasoning ReasoningSelector) {
	p.reasoningSelect = selectReasoning
}

// applyReasoning installs the host's per-request policy and applies its
// turn-level result immediately. The policy is consulted again before every
// provider request, so the initial value is only a starting point; the loop's
// budget and context facts drive later changes.
func (p *Program) applyReasoning(intent workspace.IntentContract, text string, request []agent.Message) {
	if p.reasoningSelect == nil || p.loop == nil {
		return
	}
	facts := p.loop.RequestFacts(request)
	enabled, level := p.reasoningSelect(intent, text, facts)
	p.loop.SetReasoning(enabled, level)
	p.publishEffortSelection(enabled, level)
	p.loop.SetReasoningPolicy(func(facts agent.ReasoningFacts) (bool, string) {
		enabled, level := p.reasoningSelect(intent, text, facts)
		p.publishEffortLive(enabled, level)
		return enabled, level
	})
}

// publishEffortSelection records the turn-level starting badge. The live
// per-request path uses publishEffortLive because it runs on the request
// goroutine.
func (p *Program) publishEffortSelection(enabled bool, level string) {
	if !enabled {
		level = ""
	}
	p.PublishEffort(level)
}

// publishEffortLive projects a per-request selection through the event channel.
// It is safe from the request goroutine and never writes UI state directly.
func (p *Program) publishEffortLive(enabled bool, level string) {
	if !enabled {
		level = ""
	}
	p.publish(ModelEffortUpdated{Effort: level})
}
