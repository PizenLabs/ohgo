package cognition

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func reasoningModel(ladder ...string) ai.Model {
	return ai.Model{Provider: "test", ID: "reasoning", Reasoning: true, ReasoningEfforts: ladder}
}

// Demand follows the work's structure, not the role. The same objective must
// produce the same demand whether ASK or BUILD performs it; only the evidence
// in the input can raise it.
func TestAssessReasoningDemandIsRoleIndependent(t *testing.T) {
	simple := contract.IntentContract{
		Objective: "Rename this variable.",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetSymbol, Value: "x"}},
	}
	if got := AssessReasoningDemand(ReasoningInput{Intent: &simple}); got != ReasoningMinimal {
		t.Fatalf("simple rename demand = %s, want minimal", got.Label())
	}

	moderate := simple
	moderate.BehavioralObjective = true
	moderate.Verification = []contract.VerificationRequirement{{Kind: contract.VerifyAny}}
	if got := AssessReasoningDemand(ReasoningInput{Intent: &moderate}); got != ReasoningModerate {
		t.Fatalf("verified rename demand = %s, want moderate", got.Label())
	}

	complex := contract.IntentContract{
		Objective: "Refactor the task lifecycle across the continuity and control owners without breaking resume, evidence, or role handoffs.",
		Kind:      contract.ObjectiveMutation,
		Targets: []contract.Target{
			{Kind: contract.TargetPackage, Value: "task"},
			{Kind: contract.TargetPackage, Value: "workspace"},
		},
		ExpectedChanges: []contract.ExpectedChange{
			{Target: contract.Target{Kind: contract.TargetPackage, Value: "task"}, Operation: contract.ChangeModify},
			{Target: contract.Target{Kind: contract.TargetPackage, Value: "workspace"}, Operation: contract.ChangeModify},
		},
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyScope, Scope: "./...", Check: contract.CheckTest}},
		Grounding: &contract.IntentGrounding{
			Actions:     []contract.GroundingAction{contract.GroundingRead, contract.GroundingInvestigate, contract.GroundingMutate},
			Entities:    []contract.Entity{{Value: "task"}, {Value: "workspace"}, {Value: "resume"}, {Value: "evidence"}},
			Ambiguities: []contract.Ambiguity{{Kind: "scope", Material: true}},
		},
	}
	if got := AssessReasoningDemand(ReasoningInput{Intent: &complex}); got != ReasoningHigh {
		t.Fatalf("complex refactor demand = %s, want high", got.Label())
	}
}

// The run's remaining capacity lowers the demand; it never raises it. Spending
// the last request thinking instead of answering is the failure this prevents.
func TestClampReasoningDemandUsesBudgetAndContext(t *testing.T) {
	cases := []struct {
		name  string
		in    ReasoningInput
		want  ReasoningDemand
		input ReasoningDemand
	}{
		{"last turn", ReasoningInput{RemainingTurns: 1}, ReasoningMinimal, ReasoningHigh},
		{"last call", ReasoningInput{RemainingModelCalls: 1}, ReasoningMinimal, ReasoningHigh},
		{"short run", ReasoningInput{RemainingTurns: 3}, ReasoningModerate, ReasoningHigh},
		{"unmetered", ReasoningInput{RemainingTurns: -1, RemainingModelCalls: -1}, ReasoningHigh, ReasoningHigh},
		{"context pressure", ReasoningInput{ContextWindow: 100, ContextUsed: 80}, ReasoningModerate, ReasoningHigh},
		{"context limit", ReasoningInput{ContextWindow: 100, ContextUsed: 95}, ReasoningMinimal, ReasoningHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampReasoningDemand(tc.input, tc.in); got != tc.want {
				t.Fatalf("clamp(%s) = %s, want %s", tc.input.Label(), got.Label(), tc.want.Label())
			}
		})
	}
}

// The mapping is over the model's own ladder. An empty or unknown ladder omits
// the setting rather than inventing the provider's middle value.
func TestReasoningSettingUsesTheModelsNativeLadder(t *testing.T) {
	model := reasoningModel("low", "medium", "high")

	enabled, level := ReasoningSetting(model, ReasoningMinimal)
	if !enabled || level != "low" {
		t.Fatalf("minimal = %v/%q, want the lowest native rung", enabled, level)
	}
	enabled, level = ReasoningSetting(model, ReasoningModerate)
	if !enabled || level != "medium" {
		t.Fatalf("moderate = %v/%q, want the middle native rung", enabled, level)
	}
	enabled, level = ReasoningSetting(model, ReasoningHigh)
	if !enabled || level != "high" {
		t.Fatalf("high = %v/%q, want the highest native rung", enabled, level)
	}

	if enabled, level := ReasoningSetting(ai.Model{Provider: "test", ID: "plain"}, ReasoningHigh); enabled || level != "" {
		t.Fatalf("non-reasoning model = %v/%q, want omission", enabled, level)
	}
	if enabled, level := ReasoningSetting(reasoningModel(), ReasoningHigh); enabled || level != "" {
		t.Fatalf("unknown ladder = %v/%q, want omission, not an invented level", enabled, level)
	}
	if enabled, level := ReasoningSetting(reasoningModel("low", "high"), ReasoningDemand(99)); enabled || level != "" {
		t.Fatalf("unknown demand = %v/%q, want omission", enabled, level)
	}
}

// A model that reasons by default is never reported as disabled. An explicit
// disable value is used only when the catalog publishes one.
func TestReasoningSettingRespectsMandatoryAndDisable(t *testing.T) {
	mandatory := reasoningModel("low", "high")
	mandatory.ReasoningMandatory = true
	if enabled, level := ReasoningSetting(mandatory, ReasoningMinimal); enabled || level != "" {
		t.Fatalf("mandatory model minimal = %v/%q, want omission", enabled, level)
	}

	disable := reasoningModel("low", "high")
	disable.ReasoningDisable = "none"
	enabled, level := ReasoningSetting(disable, ReasoningMinimal)
	if !enabled || level != "none" {
		t.Fatalf("disable model minimal = %v/%q, want the native disable value", enabled, level)
	}
}
