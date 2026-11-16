package main

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// reasoningForWork resolves the reasoning setting for one turn.
//
// It is the composition boundary, not a second policy: cognition owns the
// demand assessment and the native mapping, and the loop owns the request.
// This function only supplies the runtime facts and preserves human overrides.
//
// A nil task is a standalone turn (one-shot, plain REPL): the request's own
// capabilities and the contract are still enough to estimate demand.
func (h *harness) reasoningForWork(intent contract.IntentContract, requirement workspace.Requirement, current *task.Task, request []agent.Message) (bool, string) {
	if h == nil || h.loop == nil {
		return false, ""
	}
	// A human effort on the active role, or an explicitly enabled global
	// reasoning policy, is an override. The runtime must not replace a
	// preference the user stated with its own estimate.
	role := h.sessionRole()
	if explicit := h.roleEffortFor(role); explicit != "" {
		return h.reasoningFor(explicit)
	}
	if h.settings != nil && h.settings.Reasoning {
		return h.reasoningFor("")
	}
	if h.settings != nil && strings.EqualFold(strings.TrimSpace(h.settings.ReasoningEffort), "off") {
		return false, ""
	}
	if !h.autoReasoning {
		return h.reasoningFor("")
	}

	input := cognition.ReasoningInput{
		Intent:      &intent,
		Investigate: requirement.Capability == workspace.CapabilityInvestigate,
	}
	// A persisted objective can require investigation after the current phase.
	// Counting that need is part of the work's demand, not a grant to run it.
	for _, need := range intent.RequiredCapabilities() {
		if need.Capability == workspace.CapabilityInvestigate {
			input.Investigate = true
		}
	}
	if current != nil {
		for _, todo := range current.Todos {
			if todo.Status != task.TodoDone {
				input.OpenTodos++
			}
		}
		for _, hypothesis := range current.Context.Hypotheses {
			if hypothesis.Status == task.HypothesisActive {
				input.OpenHypotheses++
			}
		}
		for _, question := range current.Context.Questions {
			if question.Status == task.QuestionOpen {
				input.UnresolvedQuestions++
			}
		}
	}
	input.RemainingTurns, input.RemainingModelCalls = -1, -1
	if b := h.loop.Budget(); b != nil {
		snapshot := b.Snapshot()
		input.RemainingTurns = snapshot.Remaining.Turns
		input.RemainingModelCalls = snapshot.Remaining.ModelCalls
	}
	input.ContextWindow = h.model.ContextWindow
	input.ContextUsed = agent.EstimateMessages(request)
	// The loop knows the system prompt and tool block that will actually be
	// sent, not only the conversation. Using its measurement keeps a large
	// tool surface from being treated as free context.
	if measured := h.loop.MeasureCompaction(request, agent.ContextProjection{}, false); measured.Window > 0 {
		input.ContextWindow = measured.Window
		input.ContextUsed = measured.ContextTokens + measured.SystemTokens +
			measured.ToolTokens + measured.TaskStateTokens
	}

	demand := cognition.ClampReasoningDemand(cognition.AssessReasoningDemand(input), input)
	enabled, level := cognition.ReasoningSetting(h.model, demand)
	h.lastReasoningEffort = ""
	if enabled && level != "" && level != h.model.ReasoningDisable {
		h.lastReasoningEffort = level
	}
	return enabled, level
}
