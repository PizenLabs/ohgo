package task

import (
	"testing"

	"github.com/PizenLabs/ohgo/contract"
)

// The grounding contract is durable task state: a resumed task must remember
// the entities and ambiguities the user stated, or a later turn can silently
// replace them.
func TestControllerPersistsIntentGrounding(t *testing.T) {
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
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
			Ambiguities: []contract.Ambiguity{{
				Kind: "unknown_action", Material: true,
			}},
			Confidence: contract.GroundingConfidenceLow,
		},
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatal(err)
	}

	// A write to the caller's slice must not reach the persisted contract.
	intent.Grounding.Entities[0].Value = "mutated"
	current, ok := controller.Current()
	if !ok || current.Contract == nil || current.Contract.Grounding == nil {
		t.Fatal("grounding was not persisted")
	}
	if got := current.Contract.Grounding.EntityValues(); len(got) != 2 || got[0] != "k8s" {
		t.Fatalf("entities = %v, want the caller's later mutation isolated", got)
	}

	reopened := NewController(NewStore(root), nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("restore: found=%v err=%v", found, err)
	}
	if restored.Contract == nil || restored.Contract.Grounding == nil {
		t.Fatal("restored task lost its grounding")
	}
	if got := restored.Contract.Grounding.EntityValues(); len(got) != 2 ||
		got[0] != "k8s" || got[1] != "opencode" {
		t.Fatalf("restored entities = %v", got)
	}
	if !restored.Contract.Grounding.HasMaterialAmbiguity() {
		t.Fatal("restored task lost the material ambiguity")
	}
}
