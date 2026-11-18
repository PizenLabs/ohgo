package main

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func reasoningTestHarness(t *testing.T, model ai.Model, limits budget.Limits) *harness {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model:  model,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) { return nil, errors.New("unused") },
		Tools:  []agent.Tool{&spyTool{name: "read", tier: agent.TierRead}},
		Budget: budget.New(limits),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return &harness{
		loop: loop, settings: &config.Settings{}, model: model,
		autoReasoning: true, workspace: workspace.Build.Label(),
	}
}

// The AUTO path selects the demand from the work, not the role or a fixed
// per-role default, and maps it onto the model's own ladder.
func TestAutoReasoningSelectsFromTheWork(t *testing.T) {
	model := ai.Model{
		Provider: "test", ID: "reasoning", Reasoning: true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
	h := reasoningTestHarness(t, model, budget.Limits{Turns: 50, ModelCalls: 50})

	simple := workspace.BuildIntentContract("Rename this variable.")
	enabled, level := h.reasoningForWork(simple, workspace.AnalyzeRequest("Rename this variable."), nil, nil)
	if !enabled || level != "low" {
		t.Fatalf("simple rename = %v/%q, want the lowest native rung", enabled, level)
	}

	complex := contract.IntentContract{
		Objective: "Refactor the task lifecycle across the continuity and control owners without breaking resume, evidence, or role handoffs.",
		Kind:      contract.ObjectiveMutation,
		Targets: []contract.Target{
			{Kind: contract.TargetPackage, Value: "task"},
			{Kind: contract.TargetPackage, Value: "workspace"},
		},
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyScope, Scope: "./...", Check: contract.CheckTest}},
		Grounding: &contract.IntentGrounding{
			Actions:     []contract.GroundingAction{contract.GroundingRead, contract.GroundingInvestigate, contract.GroundingMutate},
			Entities:    []contract.Entity{{Value: "task"}, {Value: "workspace"}, {Value: "resume"}, {Value: "evidence"}},
			Ambiguities: []contract.Ambiguity{{Kind: "scope", Material: true}},
		},
	}
	enabled, level = h.reasoningForWork(complex, workspace.Requirement{Capability: workspace.CapabilityInvestigate}, nil, nil)
	if !enabled || level != "high" {
		t.Fatalf("complex refactor = %v/%q, want the highest native rung", enabled, level)
	}
}

// A nearly exhausted run does not spend its last request thinking. The budget
// clamp lowers the demand before the provider is called.
func TestAutoReasoningRespectsTheRunBudget(t *testing.T) {
	model := ai.Model{
		Provider: "test", ID: "reasoning", Reasoning: true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
	h := reasoningTestHarness(t, model, budget.Limits{Turns: 1, ModelCalls: 1})
	complex := contract.IntentContract{
		Objective: "Refactor the task lifecycle across the continuity and control owners without breaking resume, evidence, or role handoffs.",
		Targets: []contract.Target{
			{Kind: contract.TargetPackage, Value: "task"},
			{Kind: contract.TargetPackage, Value: "workspace"},
		},
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	enabled, level := h.reasoningForWork(complex, workspace.Requirement{Capability: workspace.CapabilityMutate}, nil, nil)
	if !enabled || level != "low" {
		t.Fatalf("last-request demand = %v/%q, want the lowest native rung", enabled, level)
	}
}

// An explicit human effort is an override, never replaced by AUTO. A model with
// no published ladder omits reasoning rather than inventing a level.
func TestAutoReasoningPreservesOverridesAndOmissions(t *testing.T) {
	model := ai.Model{
		Provider: "test", ID: "reasoning", Reasoning: true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
	h := reasoningTestHarness(t, model, budget.Limits{Turns: 50, ModelCalls: 50})
	settings := config.Default()
	settings.AssignRole("build", "test/reasoning", "high")
	h.settings = &settings

	simple := workspace.BuildIntentContract("Rename this variable.")
	enabled, level := h.reasoningForWork(simple, workspace.AnalyzeRequest("Rename this variable."), nil, nil)
	if !enabled || level != "high" {
		t.Fatalf("explicit override = %v/%q, want the human's high effort", enabled, level)
	}

	unknown := ai.Model{Provider: "test", ID: "unknown", Reasoning: true}
	h.model = unknown
	h.settings = &config.Settings{}
	enabled, level = h.reasoningForWork(simple, workspace.AnalyzeRequest("Rename this variable."), nil, nil)
	if enabled || level != "" {
		t.Fatalf("unknown ladder = %v/%q, want omission", enabled, level)
	}

	off := config.Default()
	off.ReasoningEffort = "off"
	h.model = model
	h.settings = &off
	enabled, level = h.reasoningForWork(simple, workspace.AnalyzeRequest("Rename this variable."), nil, nil)
	if enabled || level != "" {
		t.Fatalf("global off = %v/%q, want reasoning disabled", enabled, level)
	}

	plain := ai.Model{Provider: "test", ID: "plain"}
	h.model = plain
	h.settings = &config.Settings{}
	enabled, level = h.reasoningForWork(simple, workspace.AnalyzeRequest("Rename this variable."), nil, nil)
	if enabled || level != "" {
		t.Fatalf("non-reasoning model = %v/%q, want omission", enabled, level)
	}
}
