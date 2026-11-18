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

// A deliverable that mentions read-only validation and asks not to change
// anything else is a mutation task. The quoted sentence is content, and
// "anything else" scopes the prohibition; neither is a whole-change limit.
func TestBuildIntentContractIgnoresQuotedReadOnlyDeliverable(t *testing.T) {
	const request = `Add a short "Validation note" section at the end of README.md containing exactly one sentence: "This repository was used for a read-only Ohgo validation." Do not change anything else. Verify the file content after the edit.`
	intent := BuildIntentContract(request)
	if intent.Kind != ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation", intent.Kind)
	}
	if intent.Grounding == nil {
		t.Fatal("no grounding")
	}
	for _, constraint := range intent.Grounding.Constraints {
		if constraint == "no_mutation" {
			t.Fatal("the quoted deliverable or scoped limit became a whole-change prohibition")
		}
	}
}

// Appending a line is a repository mutation even though the verb is not
// "add". Without this vocabulary the contract stays informational, the
// mutation tools are withheld, and the model is told mutation is forbidden.
func TestBuildIntentContractRecognizesAppend(t *testing.T) {
	const request = `Append exactly one line comment "// ohgo validation" to the end of backend/go.mod. Do not change any other file.`
	if !RequestsMutation(request) {
		t.Fatal("append was not recognized as a mutation request")
	}
	intent := BuildIntentContract(request)
	if intent.Kind != ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation", intent.Kind)
	}
}

// The benchmark's investigation-only prompt names a fix only to forbid it.
// It must stay informational, with no target or verification requirement that
// the FUZZ role can never satisfy.
func TestInvestigationOnlyPromptDoesNotBecomeMutation(t *testing.T) {
	text := `Investigate the order-creation flow and determine whether there is any production-reachable path.

The investigation must answer:
1. What is the actual production entrypoint?
2. Does validation happen before persistence?
3. Are there any bypass paths?
4. Conclude one of SAFE, BUG, or UNRESOLVED.

Do not modify source code, tests, configuration, migrations, or repository state.
Do not create a fix. This is an investigation-only benchmark.`
	c := BuildIntentContract(text)
	if c.Kind != ObjectiveInformational {
		t.Fatalf("kind = %s, want informational", c.Kind)
	}
	if c.ConditionalMutation {
		t.Fatal("the investigation-only prompt was marked conditional mutation")
	}
	for _, target := range c.Targets {
		if strings.EqualFold(target.Value, "UNRESOLVED") {
			t.Fatalf("terminal-state word became a symbol target: %+v", c.Targets)
		}
	}
}

// A negated clause is a prohibition, not a change request. The same clause
// model already handles grounding actions; mutation classification must agree.
func TestNegatedMutationClauseDoesNotRequestMutation(t *testing.T) {
	for _, text := range []string{
		"Do not create a fix.",
		"Do not modify source code; explain the flow.",
		"Never implement the change; report the finding.",
	} {
		if RequestsMutation(text) {
			t.Errorf("RequestsMutation(%q) = true, want false", text)
		}
	}
}
