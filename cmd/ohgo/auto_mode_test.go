package main

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

func autoTestModel() ai.Model {
	return ai.Model{Provider: "test", ID: "reasoning", Reasoning: true, ReasoningEfforts: []string{"low", "medium", "high"}}
}

func attachAutoLoop(t *testing.T, h *harness, model ai.Model) {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model:  model,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) { return nil, errors.New("unused") },
		Tools:  []agent.Tool{&spyTool{name: "read", tier: agent.TierRead}},
		Budget: budget.New(budget.Limits{Turns: 50, ModelCalls: 50}),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	h.loop = loop
}

func autoComplexIntent() (contract.IntentContract, workspace.Requirement) {
	intent := contract.IntentContract{
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
	return intent, workspace.Requirement{Capability: workspace.CapabilityInvestigate}
}

// /effort auto is a real switch back to demand. It must work after the
// session started with an explicit role level, which was the prior failure:
// clearing a level left AUTO disabled for the whole invocation.
func TestEffortAutoReenablesDemandAfterRoleOverride(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.AssignRole("build", "", "high")
	h.model = autoTestModel()
	attachAutoLoop(t, h, h.model)
	intent, requirement := autoComplexIntent()

	if enabled, _ := h.reasoningForWork(intent, requirement, nil, unmeteredFacts()); !enabled {
		t.Fatal("explicit role level did not enable reasoning")
	}
	applied, _, err := h.SetEffort("build", "auto")
	if err != nil {
		t.Fatalf("SetEffort(auto): %v", err)
	}
	if applied.Effort != "" || !applied.Auto {
		t.Fatalf("applied = %+v, want Auto with no fixed level", applied)
	}
	enabled, level := h.reasoningForWork(intent, requirement, nil, unmeteredFacts())
	if !enabled || level != "high" {
		t.Fatalf("after /effort auto = %v/%q, want demand-driven high", enabled, level)
	}
	profile := h.settings.Roles["build"]
	if profile.Effort != "" || !profile.Auto {
		t.Fatalf("profile = %+v, want a persisted Auto mode", profile)
	}
}

// Auto belongs to the active role. A saved level on BUILD must not make ASK
// shallow after a workspace switch.
func TestDemandModeIsPerRoleAfterWorkspaceSwitch(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.AssignRole("build", "", "high")
	h.model = autoTestModel()
	attachAutoLoop(t, h, h.model)
	h.workspace = workspace.Ask.Label()
	intent, requirement := autoComplexIntent()

	enabled, level := h.reasoningForWork(intent, requirement, nil, unmeteredFacts())
	if !enabled || level != "high" {
		t.Fatalf("ASK with no role level = %v/%q, want demand-driven high", enabled, level)
	}
}

// A runtime Auto choice is later than an invocation flag for this session. It
// must not leave the flag's fixed policy in control.
func TestEffortAutoWinsOverInvocationFlagForThisSession(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.Reasoning = false
	h.reasoningFlag = true
	h.model = autoTestModel()
	attachAutoLoop(t, h, h.model)
	attachAutoLoop(t, h, h.model)
	intent, requirement := autoComplexIntent()

	if enabled, level := h.reasoningForWork(intent, requirement, nil, unmeteredFacts()); !enabled || level != "medium" {
		t.Fatalf("flag policy = %v/%q, want the global medium level", enabled, level)
	}
	if _, _, err := h.SetEffort("build", "auto"); err != nil {
		t.Fatalf("SetEffort(auto): %v", err)
	}
	enabled, level := h.reasoningForWork(intent, requirement, nil, unmeteredFacts())
	if !enabled || level != "high" {
		t.Fatalf("Auto after flag = %v/%q, want demand-driven high", enabled, level)
	}
}

// The picker target is the live model, not only the persisted role profile.
// A --model/session override must not offer levels the runtime will reject.
func TestEffortAssignmentUsesTheLiveModelLadder(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.AssignRole("build", "anthropic/claude-haiku-4-5", "")
	h.model = catalog.MustResolve("anthropic/claude-opus-4-6")

	assignment := h.EffortAssignment("build")
	if assignment.Model != h.model.Ref() {
		t.Fatalf("assignment model = %q, want the live %q", assignment.Model, h.model.Ref())
	}
	for _, level := range assignment.Efforts {
		if level == "minimal" || level == "xhigh" {
			t.Errorf("assignment offers %q from the configured model, not the live ladder", level)
		}
	}
}

// A model switch must not leave a demand-selected level claiming the new
// model when that model does not publish it.
func TestCurrentEffortDropsAStaleLevelOnModelSwitch(t *testing.T) {
	h, _ := modelControlHarness(t)
	h.model = autoTestModel()
	attachAutoLoop(t, h, h.model)
	intent, requirement := autoComplexIntent()
	if enabled, level := h.reasoningForWork(intent, requirement, nil, unmeteredFacts()); !enabled || level != "high" {
		t.Fatalf("setup = %v/%q, want high", enabled, level)
	}

	h.applyModel(ai.Model{Provider: "test", ID: "limited", Reasoning: true, ReasoningEfforts: []string{"low"}})
	if got := h.currentEffort(); got == "high" {
		t.Fatal("currentEffort kept high after switching to a model that only publishes low")
	}
}

// Changing the model through /model preserves a persisted Auto mode; changing
// the effort explicitly clears it.
func TestModelSwitchPreservesAutoAndExplicitLevelClearsIt(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.AssignAuto("build", "")
	h.workspace = workspace.Build.Label()
	if _, err := h.SetModel(catalog.DefaultRef); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	profile := h.settings.Roles["build"]
	if !profile.Auto || profile.Effort != "" {
		t.Fatalf("profile after model switch = %+v, want Auto preserved", profile)
	}
	if _, _, err := h.SetEffort("build", "high"); err != nil {
		t.Fatalf("SetEffort(high): %v", err)
	}
	profile = h.settings.Roles["build"]
	if profile.Auto || profile.Effort != "high" {
		t.Fatalf("profile after explicit level = %+v, want explicit high", profile)
	}
}

// The card must mark Auto active from the effective mode, not merely from an
// empty stored effort.
func TestEffortModeMarksDemandAndGlobalDistinctly(t *testing.T) {
	h, settings := modelControlHarness(t)
	if h.effortMode("build") != "auto" || !h.demandActive("build") {
		t.Fatalf("default mode = %q, demand=%v, want auto", h.effortMode("build"), h.demandActive("build"))
	}
	settings.Reasoning = true
	if h.effortMode("build") != "global" || h.demandActive("build") {
		t.Fatalf("global mode = %q, demand=%v, want global", h.effortMode("build"), h.demandActive("build"))
	}
	if row := h.EffortAssignment("build"); row.Auto || row.EffortMode != "global" {
		t.Fatalf("row = %+v, want global and Auto inactive", row)
	}

	settings.Reasoning = false
	settings.ReasoningEffort = "off"
	if h.effortMode("build") != "off" || h.demandActive("build") {
		t.Fatalf("global-off mode = %q, demand=%v, want off", h.effortMode("build"), h.demandActive("build"))
	}
	if row := h.EffortAssignment("build"); row.Auto || row.EffortMode != "off" {
		t.Fatalf("row = %+v, want Off mode", row)
	}
}
