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

// A glob that pruned .gitignore paths is not a complete survey of the tree.
// The evidence must not mark it complete even when it returned matches, or a
// later absence claim can be accepted against a subtree that was never scanned.
func TestPrunedGlobIsNotCompleteEvidence(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "glob-1", ToolName: "glob",
		Args: map[string]any{"pattern": "**/*"},
		Result: agent.Result{
			Text: "main.go\n\n[1 path was excluded by .gitignore; " +
				`pass "gitignore": false to include ignored paths.]`,
			Details: map[string]any{"pattern": "**/*", "path": ".", "matches": 1, "ignored": 1},
		},
	})
	item, ok := state.Find("search:glob:**/*:.")
	if !ok {
		t.Fatalf("pruned glob was not recorded: %+v", state.Items)
	}
	if item.Complete {
		t.Fatal("a pruned glob was recorded as a complete scan")
	}

	state = NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "glob-2", ToolName: "glob",
		Args: map[string]any{"pattern": "**/*"},
		Result: agent.Result{
			Text:    "main.go\nnotes.md",
			Details: map[string]any{"pattern": "**/*", "path": ".", "matches": 2, "ignored": 0},
		},
	})
	item, ok = state.Find("search:glob:**/*:.")
	if !ok {
		t.Fatalf("complete glob was not recorded: %+v", state.Items)
	}
	if !item.Complete {
		t.Fatal("a glob with no exclusions was recorded as incomplete")
	}
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

// A single file is partial evidence. "I found the README" is not an overview of
// the project, so the runtime must not conclude that it can answer yet.
func TestSingleFileIsPartialRepositoryOverview(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})
	if !requirementKeys(shape.RequiredKnowledge)["repository_overview"] {
		t.Fatalf("required knowledge = %+v, want repository_overview", shape.RequiredKnowledge)
	}

	evidence := NewEvidenceState()
	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{
			Text: "1\t# ohgo", Details: map[string]any{
				"path": "README.md", "totalLines": 1, "offset": 1, "limit": 2000,
			},
		},
	})
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("one file: sufficiency = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}

	// A second content observation (the module manifest) establishes the
	// project's stack as well as its purpose.
	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-2", ToolName: "read", Args: map[string]any{"path": "go.mod"},
		Result: agent.Result{
			Text: "1\tmodule example.com/ohgo", Details: map[string]any{
				"path": "go.mod", "totalLines": 1, "offset": 1, "limit": 2000,
			},
		},
	})
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("two observations: sufficiency = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
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

// A version-control question is answered by inspecting repository metadata, not
// by surveying the source. The shape must ask for the inspection it can use,
// and a completed git read must satisfy it; otherwise a correct answer is
// vetoed as insufficient at the explore stage.
func TestVCSQuestionRequiresAGitObservationNotASourceSurvey(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Inspect the repository's git state: run git status and git tag -l.",
	}, ShapeParams{Authority: "ask"})
	keys := requirementKeys(shape.RequiredKnowledge)
	if !keys["vcs_observation"] {
		t.Fatalf("required knowledge = %+v, want vcs_observation", shape.RequiredKnowledge)
	}
	if keys["repository_overview"] || keys["repository_survey"] {
		t.Fatalf("a git-state question demanded a source survey: %+v", shape.RequiredKnowledge)
	}

	evidence := NewEvidenceState()
	evidence.ensureRequirements(shape)
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Insufficient {
		t.Fatalf("no observation: sufficiency = %s/%s, want INSUFFICIENT", suff.State, suff.Stage)
	}

	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "git-1", ToolName: "bash",
		Args: map[string]any{"command": "git status"},
		Result: agent.Result{
			Text:     "$ git status\n\nOn branch master\n\n[exit code 0, 12ms]",
			Executes: true, Finished: true,
			Details: map[string]any{"command": "git status", "exitCode": 0, "git_observation": true},
		},
	})
	if !evidence.hasVCSObservation() {
		t.Fatalf("a completed git inspection was not recorded: %+v", evidence.Items)
	}
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Sufficient {
		t.Fatalf("observed: sufficiency = %s/%s, want SUFFICIENT (%v)", suff.State, suff.Stage, suff.Missing)
	}

	// A broad repository question still needs implementation evidence: the VCS
	// requirement must not have replaced the survey for every repository task.
	broad := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Analyze this project's architecture.",
	}, ShapeParams{Authority: "ask"})
	if !requirementKeys(broad.RequiredKnowledge)["repository_survey"] {
		t.Fatalf("a broad analysis lost its survey requirement: %+v", broad.RequiredKnowledge)
	}

	// Naming git is not the same as asking about VCS state. "git hooks" is a
	// question about the implementation, so it must not be satisfied by a git
	// observation.
	implementation := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Explain how git hooks are configured in this repository.",
	}, ShapeParams{Authority: "ask"})
	if requirementKeys(implementation.RequiredKnowledge)["vcs_observation"] {
		t.Fatalf("an implementation question became a VCS inspection: %+v", implementation.RequiredKnowledge)
	}
}
