package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The grounding contract is projected into the runtime state block, so the
// model sees the entities and ambiguity it must not silently replace. This is
// the context half of entity preservation.
func TestRenderStateCarriesGrounding(t *testing.T) {
	intent := contract.IntentContract{
		Objective: "Additional projects are k8s and opencode.",
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{
			Objective: "Additional projects are k8s and opencode.",
			Action:    contract.GroundingUnknown,
			Entities: []contract.Entity{
				{Value: "k8s", Kind: contract.EntityProject},
				{Value: "opencode", Kind: contract.EntityProject},
			},
			Scope: contract.GroundingScope{
				Kind:   contract.ScopeNamed,
				Values: []string{"k8s", "opencode"},
			},
			Ambiguities: []contract.Ambiguity{{Kind: "unknown_action", Material: true}},
			Confidence:  contract.GroundingConfidenceLow,
		},
	}
	shape := BuildShape(intent, ShapeParams{Authority: "ask", Intent: "read"})
	if len(shape.NamedEntities) != 2 {
		t.Fatalf("shape entities = %v", shape.NamedEntities)
	}
	if shape.RequestedAction != string(contract.GroundingUnknown) {
		t.Fatalf("shape action = %q", shape.RequestedAction)
	}
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": "README.md"},
		Result:   agent.Result{Text: "package main"},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil), Sufficiency{State: Sufficient, Stage: StageAnswer},
		CandidateSet{}, 0, 0)
	for _, want := range []string{"Named entities", "k8s", "opencode", "Requested action: unknown", "unknown_action"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("state block missing %q:\n%s", want, rendered)
		}
	}
}
