package workspace

import (
	"strings"
	"testing"
)

func TestConditionalWorkflowRequiresAStagedInvestigation(t *testing.T) {
	yes := []string{
		stagedWorkflowPrompt,
		"Investigate the project first. If you confirm a concrete issue, fix it. Otherwise explain what you found.",
		"Check this project and if you find an issue, fix it.",
	}
	for _, text := range yes {
		if !ConditionalWorkflow(text) {
			t.Errorf("ConditionalWorkflow(%.60q) = false, want true", text)
		}
	}
	no := []string{
		"Fix the checkout bug.",
		"Investigate the checkout flow.",
		"If the tests fail, fix them.",
		"Check the project and create the missing files.",
		"Reproduce the failure and fix it.",
	}
	for _, text := range no {
		if ConditionalWorkflow(text) {
			t.Errorf("ConditionalWorkflow(%.60q) = true, want false", text)
		}
	}
}

func TestBuildIntentContractMarksAStagedConditionalObjective(t *testing.T) {
	contract := BuildIntentContract(stagedWorkflowPrompt)
	if contract.Kind != ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation so the future branch is authorized", contract.Kind)
	}
	if !contract.ConditionalMutation {
		t.Fatal("staged investigation plus conditional fix was not marked conditional")
	}
	if contract.Grounding == nil {
		t.Fatal("no grounding")
	}
	if contract.Grounding.Action != "investigate" {
		t.Errorf("grounding action = %s, want the investigation phase", contract.Grounding.Action)
	}
	if !contract.Grounding.MutationIntent || !contract.Grounding.HasAction("mutate") {
		t.Error("the conditional mutation branch is not grounded; BUILD could not perform it later")
	}
	for _, constraint := range contract.Grounding.Constraints {
		if constraint == "no_mutation" || constraint == "no_creation" {
			t.Errorf("phase-scoped constraint %q survived as a global task limit", constraint)
		}
	}
	if !strings.Contains(contract.Objective, "order/checkout") {
		t.Error("the contract lost the named target")
	}
}

func TestBuildIntentContractKeepsUnconditionalMutationAndConstraints(t *testing.T) {
	unconditional := BuildIntentContract("Fix the checkout bug.")
	if unconditional.ConditionalMutation {
		t.Fatal("an unconditional fix was marked conditional")
	}

	conditionalButDirect := BuildIntentContract("If the tests fail, fix them.")
	if conditionalButDirect.ConditionalMutation {
		t.Fatal("a conditional mutation without an investigation phase was marked staged")
	}

	readOnly := BuildIntentContract("Do not modify anything. Explain the checkout flow.")
	if readOnly.Kind != ObjectiveInformational {
		t.Fatalf("kind = %s, want informational", readOnly.Kind)
	}
	if readOnly.Grounding == nil || !containsString(readOnly.Grounding.Constraints, "no_mutation") {
		t.Fatal("an unconditional no-mutation limit was dropped")
	}

	continuity := BuildIntentContract("Do not create a new task. Explain the checkout flow.")
	if continuity.Grounding == nil || containsString(continuity.Grounding.Constraints, "no_creation") {
		t.Fatal("a task-continuity instruction became a repository creation limit")
	}
}

func TestIsTaskFollowUpIsAboutTheOpenResult(t *testing.T) {
	yes := []string{
		"fix it", "implement the fix", "implement the fix and verify it",
		"reproduce it", "verify the fix", "apply the fix",
		"redesign it", "rework it", "revamp it", "restyle it",
	}
	for _, text := range yes {
		if !IsTaskFollowUp(text) {
			t.Errorf("IsTaskFollowUp(%q) = false, want true", text)
		}
	}
	no := []string{
		"fix the checkout bug", "implement a new endpoint",
		"reproduce the failure under load", "explain the checkout flow",
	}
	for _, text := range no {
		if IsTaskFollowUp(text) {
			t.Errorf("IsTaskFollowUp(%q) = true, want false", text)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A contracted whole-change prohibition is the user's own constraint. The
// completion contract must preserve it even when the sentence opens with a
// discourse word and the read request follows a full stop.
func TestBuildIntentContractRecognizesAContractedNoMutationLimit(t *testing.T) {
	readOnly := BuildIntentContract("Actually don't change anything. Explain the current architecture.")
	if readOnly.Kind != ObjectiveInformational {
		t.Fatalf("kind = %s, want informational", readOnly.Kind)
	}
	if readOnly.Grounding == nil || !containsString(readOnly.Grounding.Constraints, "no_mutation") {
		t.Fatal("a contracted no-mutation limit was dropped from the contract")
	}
}
