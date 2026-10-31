package cognition

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// requirementKeys returns the evidence keys a shape declares.
func requirementKeys(needs []KnowledgeNeed) map[string]bool {
	keys := map[string]bool{}
	for _, need := range needs {
		keys[need.Key] = true
	}
	return keys
}

// A question about how this repository behaves cannot be answered from model
// knowledge: the answer lives in a file. The shape must declare that need, and
// sufficiency must stay INSUFFICIENT until something content-bearing is read.
// A directory listing is structure, not an answer, so it must not satisfy it.
func TestRepositoryBehaviourQuestionRequiresAnObservation(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Does this workflow run automatically?",
	}, ShapeParams{Authority: "ask"})
	if !requirementKeys(shape.RequiredKnowledge)["repository_overview"] {
		t.Fatalf("required knowledge = %+v, want repository_overview", shape.RequiredKnowledge)
	}

	evidence := NewEvidenceState()
	evidence.ensureRequirements(shape)
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Insufficient {
		t.Fatalf("no evidence: sufficiency = %s/%s, want INSUFFICIENT", suff.State, suff.Stage)
	}

	// A listing explains which names exist, not what the workflow does.
	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{Text: "README.md\ncalc.go", Details: map[string]any{"path": ".", "entries": 2}},
	})
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Insufficient {
		t.Fatalf("listing only: sufficiency = %s/%s, want INSUFFICIENT", suff.State, suff.Stage)
	}

	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read",
		Args: map[string]any{"path": ".github/workflows/telemetry.yml"},
		Result: agent.Result{
			Text:    "1\ton:\n2\t  schedule:\n3\t    - cron: \"17 3 * * *\"",
			Details: map[string]any{"path": ".github/workflows/telemetry.yml", "totalLines": 3, "offset": 1, "limit": 2000},
		},
	})
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("after read: sufficiency = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
}

// The repository requirement is not a blanket tax on questions. A general
// knowledge question has no repository coordinate to establish and completes
// with an answer.
func TestGeneralKnowledgeQuestionNeedsNoRepositoryEvidence(t *testing.T) {
	for _, objective := range []string{
		"What is a cron expression?",
		"What is the difference between a commit and a push?",
		"Which shell does a Dockerfile use?",
	} {
		shape := BuildShape(contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: objective,
		}, ShapeParams{Authority: "ask"})
		if requirementKeys(shape.RequiredKnowledge)["repository_overview"] {
			t.Errorf("%q declared a repository observation requirement", objective)
		}
		if suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false); suff.State != Sufficient {
			t.Errorf("%q: sufficiency = %s, want SUFFICIENT", objective, suff.State)
		}
	}
}

// The same question phrased without a determiner-bound artifact still points at
// this repository: "this" plus a hosting surface is a deictic reference, and the
// answer is in the configuration. A general question that names the same
// technology does not need repository evidence.
func TestRepositoryScopeRecognisesDeicticHostingQuestions(t *testing.T) {
	cases := []struct {
		objective string
		want      bool
	}{
		{"Does this automatically update GitHub, or do I have to run it manually?", true},
		{"Does it update the workflow automatically?", true},
		{"Is our pipeline green?", true},
		{"What is GitHub Actions?", false},
		{"What is the best CI for a Go project?", false},
		{"How does a cron expression work?", false},
	}
	for _, tc := range cases {
		shape := BuildShape(contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: tc.objective,
		}, ShapeParams{Authority: "ask"})
		got := requirementKeys(shape.RequiredKnowledge)["repository_overview"]
		if got != tc.want {
			t.Errorf("repository observation for %q = %v, want %v", tc.objective, got, tc.want)
		}
	}
}
