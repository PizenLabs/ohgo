package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The LongCat regression: a zero test-file inventory and a package.json with no
// test script do not establish that the project has no tests. Tooling and
// excluded paths were not inspected, so the repository-wide conclusion must be
// reviewed. The bounded form is accepted because it states exactly what was
// observed.
func TestTestAbsenceClaimRequiresCompleteToolingAndScope(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	structure := ToolOutcome{
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: Result{Details: map[string]any{
			"view":                "tests",
			"test_files":          0,
			"total":               0,
			"graph_state":         "CURRENT",
			"truncated":           false,
			"scope_complete":      true,
			"inventory_complete":  true,
			"structural_complete": true,
		}},
	}
	turns := []Turn{{Assistant: ai.AssistantMessage{}, Results: []ToolOutcome{structure}}}

	unsupported := "This project has no tests."
	if assessment := AssessGrounding(intent, turns, unsupported); assessment.Pass() {
		t.Fatal("a zero test-file inventory was promoted to a repository-wide absence")
	}

	bounded := "No test files were observed in the available inventory. Test tooling and excluded paths were not inspected."
	if assessment := AssessGrounding(intent, turns, bounded); !assessment.Pass() {
		t.Fatalf("a bounded observation was reviewed: %s", assessment.Reason())
	}
}

// The noun form "absence of tests" is the same repository-wide conclusion as
// "there are no tests". A zero test-file inventory cannot establish it unless
// the claim is bounded to what was inspected.
func TestAbsenceOfTestsIsNotProvedByZeroInventory(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	structure := ToolOutcome{
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: Result{Details: map[string]any{
			"view":                "tests",
			"test_files":          0,
			"total":               0,
			"graph_state":         "CURRENT",
			"scope_complete":      true,
			"inventory_complete":  true,
			"structural_complete": true,
		}},
	}
	turns := []Turn{{Results: []ToolOutcome{structure}}}
	if assessment := AssessGrounding(intent, turns, "Established: the absence of tests."); assessment.Pass() {
		t.Fatal("the noun form of test absence was accepted from a zero file count")
	}
	if assessment := AssessGrounding(intent, turns, "The absence of tests is limited to the inspected scope."); !assessment.Pass() {
		t.Fatalf("a scoped absence was reviewed: %s", assessment.Reason())
	}
}

// A missing test script is a fact about one manifest file, not a test
// inventory. The answer may report it, but the leap to "no tests exist" is the
// unsupported part.
func TestMissingTestScriptDoesNotProveNoTests(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	manifest := ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": "package.json"},
		Result:   Result{Text: `{"scripts":{"dev":"next dev","build":"next build"}}`},
	}
	turns := []Turn{{Results: []ToolOutcome{manifest}}}
	answer := "`package.json` has no test script, so the project has no tests."
	assessment := AssessGrounding(intent, turns, answer)
	if assessment.Pass() {
		t.Fatal("a missing script was promoted to a repository-wide test absence")
	}
	if !strings.Contains(assessment.Reason(), "absent") && !strings.Contains(assessment.Reason(), "test") {
		t.Fatalf("reason = %q, want it to name the absence claim", assessment.Reason())
	}
}
