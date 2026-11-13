package contract

import "testing"

// needFor returns the required capability with the given class, if present.
func needFor(needs []CapabilityNeed, want Capability) (CapabilityNeed, bool) {
	for _, need := range needs {
		if need.Capability == want {
			return need, true
		}
	}
	return CapabilityNeed{}, false
}

// A mutation objective that promises a behavioural check needs three things:
// reading to locate the change, a mutation operation to make it, and execution
// to run the check. Missing the check requirement is how a BUILD turn reaches a
// surface that can edit but cannot verify.
func TestMutationContractRequiresReadMutateAndExecute(t *testing.T) {
	intent := testContract(ObjectiveMutation, VerificationRequirement{Kind: VerifyAny, Scope: "*"})
	needs := intent.RequiredCapabilities()
	if len(needs) != 3 {
		t.Fatalf("needs = %+v, want read, mutate, execute", needs)
	}
	for _, want := range []Capability{CapabilityRead, CapabilityMutate, CapabilityExecute} {
		if _, ok := needFor(needs, want); !ok {
			t.Errorf("missing %s requirement in %+v", want, needs)
		}
	}
}

// An informational objective is an answer, so it must not demand execution or
// mutation. Requiring either would block a legitimate ASK turn.
func TestInformationalContractRequiresReadOnly(t *testing.T) {
	intent := testContract(ObjectiveInformational)
	needs := intent.RequiredCapabilities()
	if len(needs) != 1 || needs[0].Capability != CapabilityRead {
		t.Fatalf("needs = %+v, want read only", needs)
	}
}

// A change with no named direction accepts either way of producing it. A
// runtime with only a write tool can still complete the request, so reporting a
// gap would be a false block.
func TestUndirectedMutationIsSatisfiedByEitherWriteOrPatch(t *testing.T) {
	intent := testContract(ObjectiveMutation, VerificationRequirement{Kind: VerifyAny, Scope: "*"})
	mutate, ok := needFor(intent.RequiredCapabilities(), CapabilityMutate)
	if !ok {
		t.Fatal("no mutate requirement")
	}
	if !mutate.SatisfiedBy([]Operation{OpWrite}) {
		t.Errorf("write alone did not satisfy %+v", mutate)
	}
	if !mutate.SatisfiedBy([]Operation{OpPatch}) {
		t.Errorf("edit alone did not satisfy %+v", mutate)
	}
	if mutate.SatisfiedBy([]Operation{OpRead, OpExec}) {
		t.Errorf("read+exec satisfied a mutation requirement: %+v", mutate)
	}
}

// A delete request is not served by a create-only capability: the operation the
// user named is part of the contract.
func TestDeleteMutationRequiresDeleteOperation(t *testing.T) {
	intent := testContract(ObjectiveMutation)
	intent.ExpectedChanges = []ExpectedChange{{
		Target:    Target{Kind: TargetPath, Value: "legacy.go"},
		Operation: ChangeDelete,
	}}
	mutate, ok := needFor(intent.RequiredCapabilities(), CapabilityMutate)
	if !ok {
		t.Fatal("no mutate requirement")
	}
	if mutate.SatisfiedBy([]Operation{OpWrite, OpPatch}) {
		t.Errorf("create/edit satisfied a delete requirement: %+v", mutate)
	}
	if !mutate.SatisfiedBy([]Operation{OpDelete}) {
		t.Errorf("delete did not satisfy its own requirement: %+v", mutate)
	}
}

// The unmet set is the blocking gap, in requirement order, and it is empty
// when everything mandatory is available.
func TestUnmetCapabilitiesReportsTheBlockingGap(t *testing.T) {
	intent := testContract(ObjectiveMutation, VerificationRequirement{Kind: VerifyScope, Scope: "."})
	unmet := intent.UnmetCapabilities([]Operation{OpRead})
	if len(unmet) != 2 {
		t.Fatalf("unmet = %+v, want mutate and execute", unmet)
	}
	if unmet[0].Capability != CapabilityMutate || unmet[1].Capability != CapabilityExecute {
		t.Fatalf("unmet order = %+v, want mutate then execute", unmet)
	}
	if got := intent.UnmetCapabilities([]Operation{OpRead, OpWrite, OpExec}); len(got) != 0 {
		t.Fatalf("unmet = %+v, want none", got)
	}
}

// Reading is required but not blocking: an answer can be produced from the
// conversation, and an observation-free repository answer is a review verdict
// the evidence layer already reports. Treating it as blocking would turn that
// review into an unreachable objective.
func TestReadRequirementDoesNotBlock(t *testing.T) {
	intent := testContract(ObjectiveInformational)
	needs := intent.RequiredCapabilities()
	if len(needs) != 1 || needs[0].Capability != CapabilityRead {
		t.Fatalf("needs = %+v, want a read requirement", needs)
	}
	if needs[0].Mandatory() {
		t.Fatal("reading was reported as a blocking requirement")
	}
	if got := intent.UnmetCapabilities(nil); len(got) != 0 {
		t.Fatalf("unmet = %+v, want none: a read-only objective is still attemptable", got)
	}
}

// testContract is a small fixture so the tests state a contract without
// restating the request classifier.
func testContract(kind ObjectiveKind, verification ...VerificationRequirement) IntentContract {
	return IntentContract{
		Objective:    "fixture objective",
		Kind:         kind,
		Verification: verification,
	}
}
