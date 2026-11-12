package cognition

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func TestStructuralDepthDerivesFromTaskShape(t *testing.T) {
	cases := []struct {
		name      string
		objective string
		facts     RepositoryFacts
		want      StructuralDepth
	}{
		{
			name:      "simple conversational",
			objective: "say hello",
			want:      StructuralNone,
		},
		{
			name:      "broad repository account",
			objective: "check this project and analyze what this project is about?",
			facts:     RepositoryFacts{TypeScript: true, JavaScript: true},
			want:      StructuralOverview,
		},
		{
			name:      "targeted symbol",
			objective: "find the parseConfig function",
			want:      StructuralTargeted,
		},
		{
			name:      "relationship question",
			objective: "who calls parseConfig?",
			want:      StructuralImpact,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape := BuildShape(contract.IntentContract{
				Kind:      contract.ObjectiveInformational,
				Objective: tc.objective,
			}, ShapeParams{Authority: "ask", Intent: "read", Facts: tc.facts})
			if shape.StructuralDepth != tc.want {
				t.Fatalf("StructuralDepth(%q) = %s, want %s (needsCode=%v relationship=%v broad=%v)",
					tc.objective, shape.StructuralDepth, tc.want,
					shape.NeedsCodeNavigation, shape.NeedsRelationshipNavigation,
					shape.BroadRepositoryAnalysis)
			}
		})
	}
}

func TestStructuralToolIsWithheldAtNoneAndOfferedAtOverview(t *testing.T) {
	tools := []agent.Tool{
		testTool{name: "structure", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}},
	}
	none := BuildShape(contract.IntentContract{
		Kind: contract.ObjectiveInformational, Objective: "say hello",
	}, ShapeParams{Authority: "ask", Intent: "read"})
	set := Candidates(none, NewEvidenceState(), ExecutionState{}, tools, none.RepositoryFacts,
		EvaluateSufficiency(none, NewEvidenceState(), ExecutionState{}, false))
	if len(set.Tools) != 0 {
		t.Fatalf("NONE activation offered structural tools: %+v", set.Tools)
	}

	facts := RepositoryFacts{TypeScript: true, JavaScript: true}
	overview := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask", Intent: "read", Facts: facts})
	if overview.StructuralDepth != StructuralOverview {
		t.Fatalf("broad analysis depth = %s", overview.StructuralDepth)
	}
	set = Candidates(overview, NewEvidenceState(), ExecutionState{}, tools, facts,
		EvaluateSufficiency(overview, NewEvidenceState(), ExecutionState{}, false))
	if len(set.Tools) != 1 || set.Tools[0].Name() != "structure" {
		t.Fatalf("OVERVIEW activation withheld structure: tools=%+v excluded=%+v", set.Tools, set.Excluded)
	}
}
