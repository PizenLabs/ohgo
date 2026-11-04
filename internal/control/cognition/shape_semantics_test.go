package cognition

import (
	"fmt"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// semanticCase is one row of the broad/narrow task-shape matrix.
type semanticCase struct {
	name   string
	prompt string
	repo   bool
	survey bool
}

// informationalShape returns the shape a fresh ASK turn would build.
func informationalShape(prompt string) TaskShape {
	return BuildShape(contract.IntentContract{
		Objective: prompt,
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask"})
}

func hasNeed(shape TaskShape, key string) bool {
	for _, need := range shape.RequiredKnowledge {
		if need.Key == key {
			return true
		}
	}
	return false
}

func readEvidence(id, path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "read", Args: map[string]any{"path": path},
		Result: agent.Result{
			Text: "1\tcontent of " + path,
			Details: map[string]any{
				"path": path, "totalLines": 1, "offset": 1, "limit": 2000,
			},
		},
	}
}

func readTurns(paths ...string) []agent.Turn {
	turns := make([]agent.Turn, 0, len(paths))
	for i, path := range paths {
		turns = append(turns, agent.Turn{Results: []agent.ToolOutcome{
			readEvidence(fmt.Sprintf("read-%d", i), path),
		}})
	}
	return turns
}

// TestBroadRepositoryObjectiveMatrix proves the task-shape decision is
// semantic rather than a phrase whitelist. Broad objectives get a survey
// requirement; targeted and ambiguous objectives do not.
func TestBroadRepositoryObjectiveMatrix(t *testing.T) {
	cases := []semanticCase{
		// Canonical broad forms.
		{name: "understand", prompt: "understand this project", repo: true, survey: true},
		{name: "explain repository", prompt: "explain what this repository is", repo: true, survey: true},
		{name: "overview codebase", prompt: "give me an overview of the codebase", repo: true, survey: true},
		{name: "inspect architecture", prompt: "inspect the project architecture", repo: true, survey: true},
		{name: "figure out works", prompt: "figure out how this repository works", repo: true, survey: true},
		{name: "as a whole", prompt: "analyze the repository as a whole", repo: true, survey: true},
		{name: "codebase about", prompt: "tell me what this codebase is about", repo: true, survey: true},
		{name: "system components", prompt: "summarize the system and its major components", repo: true, survey: true},
		{name: "overall structure", prompt: "investigate the overall structure", repo: true, survey: true},
		{name: "help understand", prompt: "help me understand this codebase", repo: true, survey: true},
		{name: "what does", prompt: "what does this project do", repo: true, survey: true},
		{name: "what is about", prompt: "what this project is about", repo: true, survey: true},
		{name: "describe", prompt: "describe this project", repo: true, survey: true},

		// Unusual broad formulations. These are not in any canonical list; the
		// concept classifier must recognize them.
		{name: "walkthrough", prompt: "walk me through the layout of this repo", repo: true, survey: true},
		{name: "big picture", prompt: "I need the big picture of this codebase", repo: true, survey: true},
		{name: "break down modules", prompt: "break down this project's main modules", repo: true, survey: true},
		{name: "kind of system", prompt: "what kind of system is this", repo: true, survey: true},
		{name: "get oriented", prompt: "help me get oriented in this repository", repo: true, survey: true},
		{name: "put together", prompt: "get me up to speed on how this application is put together", repo: true, survey: true},

		// Short broad forms.
		{name: "project overview", prompt: "project overview", repo: true, survey: true},
		{name: "repo architecture", prompt: "repo architecture", repo: true, survey: true},
		{name: "codebase structure", prompt: "codebase structure", repo: true, survey: true},

		// Targeted repository questions: repository evidence is needed, but one
		// content observation is enough.
		{name: "workflow behaviour", prompt: "Does this workflow run automatically?", repo: true, survey: false},
		{name: "system behaviour", prompt: "Does this system run automatically?", repo: true, survey: false},
		{name: "project license", prompt: "What is this project's license?", repo: true, survey: false},
		{name: "test suite", prompt: "Explain this project's test suite", repo: true, survey: false},
		{name: "readme file", prompt: "What does README.md say?", repo: false, survey: false},

		// Targeted coordinates. The completion contract carries their path or
		// symbol target; the shape must not widen them into a survey.
		{name: "file target", prompt: "What does index.html do?", repo: false, survey: false},
		{name: "source target", prompt: "Read internal/agent/loop.go and explain the retry logic", repo: false, survey: false},
		{name: "symbol target", prompt: "Who calls parseConfig?", repo: false, survey: false},

		// Ambiguous and general-knowledge forms stay conservative. A question
		// without a repository subject must not trigger a survey.
		{name: "ambiguous architecture", prompt: "What is the architecture?", repo: false, survey: false},
		{name: "ambiguous it", prompt: "Tell me about it", repo: false, survey: false},
		{name: "general knowledge", prompt: "What is a cron expression?", repo: false, survey: false},
		{name: "general ci", prompt: "What is the best CI for a Go project?", repo: false, survey: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape := informationalShape(tc.prompt)
			if got := shape.RepositoryScope; got != tc.repo {
				t.Fatalf("RepositoryScope(%q) = %v, want %v", tc.prompt, got, tc.repo)
			}
			if got := hasNeed(shape, "repository_survey"); got != tc.survey {
				t.Fatalf("repository_survey(%q) = %v, want %v; needs=%+v",
					tc.prompt, got, tc.survey, shape.RequiredKnowledge)
			}
			if tc.repo && !hasNeed(shape, "repository_overview") {
				t.Fatalf("%q declared no repository_overview need", tc.prompt)
			}
			if !tc.repo && hasNeed(shape, "repository_overview") {
				t.Fatalf("%q unnecessarily required repository evidence", tc.prompt)
			}

			// Empty evidence: a repository question is insufficient; a general
			// question can answer immediately.
			empty := NewEvidenceState()
			suff := EvaluateSufficiency(shape, empty, ExecutionState{}, false)
			if tc.repo {
				if suff.State != Insufficient || suff.Stage != StageExplore {
					t.Fatalf("empty evidence: sufficiency = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
				}
			} else if suff.State != Sufficient || suff.Stage != StageAnswer {
				t.Fatalf("empty evidence: sufficiency = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
			}

			// One content observation is enough for a targeted repository
			// question, but not for a survey.
			one := NewEvidenceState()
			one.ensureRequirements(shape)
			for _, turn := range readTurns("README.md") {
				one.ObserveTurns(shape, []agent.Turn{turn})
			}
			suffOne := EvaluateSufficiency(shape, one, ExecutionState{}, false)
			switch {
			case tc.survey:
				if suffOne.State != Insufficient || suffOne.Stage != StageExplore {
					t.Fatalf("one read: sufficiency = %s/%s, want INSUFFICIENT/explore for a survey",
						suffOne.State, suffOne.Stage)
				}
			case tc.repo:
				if suffOne.State != Sufficient || suffOne.Stage != StageAnswer {
					t.Fatalf("one read: sufficiency = %s/%s, want SUFFICIENT/answer for a targeted question",
						suffOne.State, suffOne.Stage)
				}
			}

			// A second content observation satisfies a survey. A code project
			// also needs an implementation observation, so a source read is the
			// second observation rather than another documentation file.
			if tc.survey {
				two := NewEvidenceState()
				two.ensureRequirements(shape)
				for _, turn := range readTurns("README.md", "main.go") {
					two.ObserveTurns(shape, []agent.Turn{turn})
				}
				suffTwo := EvaluateSufficiency(shape, two, ExecutionState{}, false)
				if suffTwo.State != Sufficient || suffTwo.Stage != StageAnswer {
					t.Fatalf("two reads: sufficiency = %s/%s, want SUFFICIENT/answer",
						suffTwo.State, suffTwo.Stage)
				}
			}
		})
	}
}

// TestBroadObjectiveCandidatesAndCompletion proves the decision reaches the
// action surface and the final verdict, not just the shape struct.
func TestBroadObjectiveCandidatesAndCompletion(t *testing.T) {
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "ls", tier: agent.TierRead},
	}

	// A broad objective with one read is still exploring: tools stay offered,
	// but the projector's evidence review vetoes a premature answer.
	broad := informationalShape("understand this project")
	one := NewEvidenceState()
	one.ensureRequirements(broad)
	one.ObserveTurns(broad, readTurns("README.md"))
	oneSuff := EvaluateSufficiency(broad, one, ExecutionState{}, false)
	set := Candidates(broad, one, ExecutionState{}, tools, RepositoryFacts{Go: true}, oneSuff)
	if len(set.Tools) == 0 {
		t.Fatalf("a broad objective with one observation withheld exploration: %+v", set.Excluded)
	}
	if review := completionFor(broad, []string{"README.md"}); review.Complete() {
		t.Fatalf("one observation completed a broad objective: %s", review.Reason)
	}

	// With a documentation and an implementation observation the action
	// surface closes and the answer can prove completion.
	two := NewEvidenceState()
	two.ensureRequirements(broad)
	two.ObserveTurns(broad, readTurns("README.md", "main.go"))
	twoSuff := EvaluateSufficiency(broad, two, ExecutionState{}, false)
	if twoSuff.Stage != StageAnswer {
		t.Fatalf("two observations: stage = %s, want answer", twoSuff.Stage)
	}
	set = Candidates(broad, two, ExecutionState{}, tools, RepositoryFacts{Go: true}, twoSuff)
	if len(set.Tools) != 0 {
		t.Fatalf("a sufficient broad answer still offered tools: %+v", set.Tools)
	}
	if review := completionFor(broad, []string{"README.md", "main.go"}); !review.Complete() {
		t.Fatalf("a sufficient broad answer did not complete: %s", review.Reason)
	}

	// A targeted repository question completes after one observation.
	narrow := informationalShape("Does this workflow run automatically?")
	if review := completionFor(narrow, []string{".github/workflows/ci.yml"}); !review.Complete() {
		t.Fatalf("a targeted repository answer required a survey: %s", review.Reason)
	}

	// A general-knowledge question needs no repository observation. An answer
	// in the first turn is complete.
	general := informationalShape("What is a cron expression?")
	got := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{{Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "A cron expression schedules recurring work."}},
		}}},
		Contract: contract.IntentContract{Objective: general.Objective, Kind: contract.ObjectiveInformational},
	})
	if !got.Complete() {
		t.Fatalf("a general answer did not complete: %s", got.Reason)
	}
}

// completionFor simulates a run that observed the named paths and then
// answered, using the projector's own evidence verdict. It proves the shape
// decision reaches the completion boundary rather than remaining advisory.
func completionFor(shape TaskShape, paths []string) agent.Completion {
	turns := readTurns(paths...)
	evidence := NewEvidenceState()
	projector := NewProjector(shape, evidence, RepositoryFacts{Go: true})
	result := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns:    turns,
		Tools:    []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
	})
	answer := "The answer is grounded in the observed files."
	turns = append(turns, agent.Turn{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: answer}},
	}})
	return agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          turns,
		Contract:       contract.IntentContract{Objective: shape.Objective, Kind: contract.ObjectiveInformational},
		EvidenceReview: result.Report.EvidenceReview(),
	})
}
