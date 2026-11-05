package workspace

import "testing"

// PhaseContract decides whether the current authority can perform the persisted
// objective or only an informational phase of it. A wrong answer either marks a
// permitted change as unfinished or pretends an authority can perform work its
// gate will refuse.
func TestPhaseContractDefersObjectivesTheAuthorityCannotPerform(t *testing.T) {
	mutation := IntentContract{
		Objective:           "fix the checkout flow",
		Kind:                ObjectiveMutation,
		Ambiguous:           true,
		Issues:              []string{"no target"},
		BehavioralObjective: true,
		Verification:        []VerificationRequirement{{Kind: VerifyAny}},
		Targets:             []Target{{Kind: TargetPath, Value: "order/checkout"}},
	}
	execution := IntentContract{
		Objective:    "run the checkout tests",
		Kind:         ObjectiveExecution,
		Verification: []VerificationRequirement{{Kind: VerifyScope, Scope: "order"}},
	}
	informational := IntentContract{
		Objective: "explain the checkout flow",
		Kind:      ObjectiveInformational,
	}

	tests := []struct {
		name     string
		intent   IntentContract
		active   Workspace
		deferred bool
	}{
		{name: "mutation under build", intent: mutation, active: Build},
		{name: "mutation under fuzz", intent: mutation, active: Fuzz, deferred: true},
		{name: "mutation under ask", intent: mutation, active: Ask, deferred: true},
		{name: "mutation without authority", intent: mutation, active: "", deferred: true},
		{name: "execution under build", intent: execution, active: Build},
		{name: "execution under fuzz", intent: execution, active: Fuzz},
		{name: "execution under ask", intent: execution, active: Ask, deferred: true},
		{name: "informational under ask", intent: informational, active: Ask},
		{name: "unknown contract", intent: IntentContract{Objective: "corrupt", Kind: "unknown"}, active: Build},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			phase, deferred := PhaseContract(tc.intent, tc.active)
			if deferred != tc.deferred {
				t.Fatalf("deferred = %v, want %v", deferred, tc.deferred)
			}
			if !tc.deferred {
				if phase.Kind != tc.intent.Kind || phase.Objective != tc.intent.Objective {
					t.Fatalf("phase contract changed a performable objective: %+v", phase)
				}
				return
			}
			if phase.Kind != ObjectiveInformational {
				t.Fatalf("phase kind = %s, want informational", phase.Kind)
			}
			if phase.Ambiguous || len(phase.Issues) > 0 || phase.BehavioralObjective ||
				len(phase.Verification) > 0 {
				t.Fatalf("mutation requirements survived into the phase: %+v", phase)
			}
			if phase.Objective != tc.intent.Objective || len(phase.Targets) != len(tc.intent.Targets) {
				t.Fatalf("phase contract lost the task identity or targets: %+v", phase)
			}
		})
	}
}
