package main

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// reasoningForWork resolves the reasoning setting for one turn.
//
// It is the composition boundary, not a second policy: cognition owns the
// demand assessment and the native mapping, and the loop owns the request.
// This function only supplies the runtime facts and preserves human overrides.
//
// A nil task is a standalone turn (one-shot, plain REPL): the request's own
// capabilities and the contract are still enough to estimate demand.
func (h *harness) reasoningForWork(intent contract.IntentContract, requirement workspace.Requirement, current *task.Task, facts agent.ReasoningFacts) (bool, string) {
	if h == nil || h.loop == nil {
		return false, ""
	}
	// A human effort on the active role is the most specific preference and
	// always wins. It is the reason /effort exists.
	role := h.sessionRole()
	if explicit := h.roleEffortFor(role); explicit != "" {
		h.clearLastReasoning()
		return h.reasoningFor(explicit)
	}
	// Demand is the default per role. It is disabled only by a role Auto
	// choice overridden by an invocation flag, or by a global/invocation
	// policy. The session's starting role no longer has a vote.
	if !h.demandActive(role) {
		h.clearLastReasoning()
		return h.nonDemandReasoning()
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
	input.RemainingTurns = facts.RemainingTurns
	input.RemainingModelCalls = facts.RemainingModelCalls
	input.ContextWindow = facts.ContextWindow
	input.ContextUsed = facts.ContextUsed

	demand := cognition.ClampReasoningDemand(cognition.AssessReasoningDemand(input), input)
	enabled, level := cognition.ReasoningSetting(h.model, demand)
	h.recordReasoningSelection(enabled, level)
	return enabled, level
}

// demandActive reports whether this role's effort comes from the work rather
// than a fixed policy.
//
// Precedence is explicit role level, role Auto, invocation flags, global
// policy, then demand. Auto is role-scoped: choosing it for BUILD cannot make
// ASK shallow, and clearing a BUILD override cannot disable ASK.
func (h *harness) demandActive(role string) bool {
	if h.roleEffortFor(role) != "" {
		return false
	}
	forced := h.autoForcedFor(role)
	if h.settingsOrDefault().RoleAuto(role) {
		// The human selected Auto for this role. A persisted Auto yields to an
		// invocation flag, but a runtime /effort auto is the later human act
		// and wins for the rest of the session.
		if forced {
			return true
		}
		return !h.hardReasoning()
	}
	if h.hardReasoning() {
		return false
	}
	settings := h.settingsOrDefault()
	if settings.Reasoning {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(settings.ReasoningEffort), "off")
}

// hardReasoning reports an invocation-level reasoning override. These are
// flags, not durable configuration, so they are separate from settings.
func (h *harness) hardReasoning() bool {
	return h.reasoningFlag || strings.TrimSpace(h.effortFlag) != ""
}

// nonDemandReasoning resolves the fixed setting after demand has been ruled
// out. An invocation flag wins over global configuration.
func (h *harness) nonDemandReasoning() (bool, string) {
	if flag := strings.TrimSpace(h.effortFlag); flag != "" {
		return h.reasoningFor(flag)
	}
	if h.reasoningFlag {
		settings := h.settingsOrDefault()
		level := strings.TrimSpace(settings.ReasoningEffort)
		if strings.EqualFold(level, "off") {
			return false, ""
		}
		return true, level
	}
	enabled, level := h.reasoningFor("")
	if !enabled || strings.EqualFold(strings.TrimSpace(level), "off") {
		return false, ""
	}
	return enabled, level
}

// autoForcedFor reports a runtime Auto selection for one role.
func (h *harness) autoForcedFor(role string) bool {
	h.reasoningMu.RLock()
	defer h.reasoningMu.RUnlock()
	return h.autoForced[strings.ToLower(strings.TrimSpace(role))]
}

// recordReasoningSelection stores the last demand-selected level for the
// header badge. A native disable value is not a level: reporting it as one
// would claim the model is thinking at a named effort when it was told to stop.
func (h *harness) recordReasoningSelection(enabled bool, level string) {
	last := ""
	if enabled && level != "" && level != h.model.ReasoningDisable {
		last = level
	}
	h.reasoningMu.Lock()
	h.lastReasoningEffort = last
	h.reasoningMu.Unlock()
}

// clearLastReasoning drops a level selected for different work or a different
// model. The next demand calculation will record a fresh one.
func (h *harness) clearLastReasoning() {
	h.reasoningMu.Lock()
	h.lastReasoningEffort = ""
	h.reasoningMu.Unlock()
}

// lastReasoningLevel reads the last demand-selected level for display.
func (h *harness) lastReasoningLevel() string {
	h.reasoningMu.RLock()
	defer h.reasoningMu.RUnlock()
	return h.lastReasoningEffort
}

// effortMode names the authority that currently owns this role's effort. It is
// what the /effort card and report show, so "auto" is never used for a global
// or invocation policy that merely has no role override.
func (h *harness) effortMode(role string) string {
	if level := h.roleEffortFor(role); level != "" {
		if level == tui.EffortOff {
			return tui.EffortModeOff
		}
		return tui.EffortModeExplicit
	}
	if h.demandActive(role) {
		return tui.EffortModeAuto
	}
	if enabled, _ := h.nonDemandReasoning(); !enabled {
		return tui.EffortModeOff
	}
	if h.hardReasoning() {
		return tui.EffortModeInvocation
	}
	return tui.EffortModeGlobal
}
