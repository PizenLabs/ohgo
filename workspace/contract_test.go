package workspace

import (
	"reflect"
	"testing"
)

// The contract must preserve an explicit target and the verification scope that
// actually checks it, without turning the command's argument into a mutation
// target. Removing either protection makes a correct change reviewable or a
// wrong change complete.
func TestBuildIntentContractCarriesTargetAndVerificationScope(t *testing.T) {
	contract := BuildIntentContract("Fix GrepTool in tools/grep.go so `go test ./tools` passes.")

	if contract.Kind != ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation", contract.Kind)
	}
	if contract.Objective != "Fix GrepTool in tools/grep.go so `go test ./tools` passes." {
		t.Fatalf("objective = %q, want the user's request", contract.Objective)
	}
	wantTargets := []Target{
		{Kind: TargetPath, Value: "tools/grep.go"},
		{Kind: TargetSymbol, Value: "GrepTool"},
	}
	if !reflect.DeepEqual(contract.Targets, wantTargets) {
		t.Fatalf("targets = %+v, want %+v", contract.Targets, wantTargets)
	}
	if got := contract.OperationFor(wantTargets[0]); got != ChangeAny {
		t.Errorf("path operation = %s, want any (it is the location of the fix)", got)
	}
	if got := contract.OperationFor(wantTargets[1]); got != ChangeModify {
		t.Errorf("symbol operation = %s, want modify", got)
	}
	wantVerification := []VerificationRequirement{{Kind: VerifyScope, Scope: "tools", Check: CheckTest}}
	if !reflect.DeepEqual(contract.Verification, wantVerification) {
		t.Fatalf("verification = %+v, want %+v", contract.Verification, wantVerification)
	}
	if contract.Ambiguous || contract.BehavioralObjective {
		t.Fatalf("contract = %+v, want a bounded non-behavioural contract", contract)
	}
}

// A direct-object mutation verb sets the operation; a location preposition does
// not. This is what lets the evaluator distinguish deleting a file from editing
// code inside it.
func TestBuildIntentContractDerivesOperationsFromDirectObjects(t *testing.T) {
	cases := []struct {
		text string
		want []ExpectedChange
	}{
		{
			text: "Create new.md.",
			want: []ExpectedChange{{
				Target: Target{Kind: TargetPath, Value: "new.md"}, Operation: ChangeCreate,
			}},
		},
		{
			text: "Delete note.txt.",
			want: []ExpectedChange{{
				Target: Target{Kind: TargetPath, Value: "note.txt"}, Operation: ChangeDelete,
			}},
		},
		{
			text: "Update note.txt.",
			want: []ExpectedChange{{
				Target: Target{Kind: TargetPath, Value: "note.txt"}, Operation: ChangeModify,
			}},
		},
		{
			text: "Add a helper in tools/grep.go.",
			want: []ExpectedChange{{
				Target: Target{Kind: TargetPath, Value: "tools/grep.go"}, Operation: ChangeAny,
			}},
		},
		{
			text: "Fix `NewHelper` in tools/grep.go.",
			want: []ExpectedChange{
				{Target: Target{Kind: TargetPath, Value: "tools/grep.go"}, Operation: ChangeAny},
				{Target: Target{Kind: TargetSymbol, Value: "NewHelper"}, Operation: ChangeModify},
			},
		},
	}
	for _, tc := range cases {
		if got := BuildIntentContract(tc.text).ExpectedChanges; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("BuildIntentContract(%q).ExpectedChanges = %+v, want %+v", tc.text, got, tc.want)
		}
	}
}

// A mutation objective with no target and no behavioural oracle cannot be
// assessed from "something changed"; it must stay reviewable.
func TestBuildIntentContractMarksUnboundedMutationAmbiguous(t *testing.T) {
	contract := BuildIntentContract("Update the code to be more robust and run go test ./...")
	if contract.Kind != ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation", contract.Kind)
	}
	if !contract.Ambiguous {
		t.Fatal("an unbounded mutation objective was treated as assessable")
	}
	if len(contract.Targets) != 0 {
		t.Fatalf("invented targets: %+v", contract.Targets)
	}
}

// Fixing the failing tests is bounded by the check itself: the user named no
// file, but did name the oracle. This must not be marked ambiguous.
func TestBuildIntentContractAcceptsBehaviouralMutationObjective(t *testing.T) {
	contract := BuildIntentContract("Fix the failing tests.")
	if contract.Kind != ObjectiveMutation {
		t.Fatalf("kind = %s, want mutation", contract.Kind)
	}
	if contract.Ambiguous {
		t.Fatal("a behavioural objective was marked ambiguous")
	}
	if !contract.BehavioralObjective {
		t.Fatal("a behavioural objective was not recorded")
	}
	if !reflect.DeepEqual(contract.Verification, []VerificationRequirement{{Kind: VerifyScope, Scope: "*", Check: CheckTest}}) {
		t.Fatalf("verification = %+v, want a repo-wide check", contract.Verification)
	}
}

func TestBuildIntentContractCanVerifyAnUnnamedCreatedWebsite(t *testing.T) {
	contract := BuildIntentContract("Create a portfolio website")
	if contract.Kind != ObjectiveMutation || contract.Ambiguous || !contract.BehavioralObjective {
		t.Fatalf("contract = %+v, want a verifiable behavioral mutation", contract)
	}
	want := []VerificationRequirement{{Kind: VerifyScope, Scope: "*", Check: CheckAny}}
	if !reflect.DeepEqual(contract.Verification, want) {
		t.Fatalf("verification = %+v, want repository-level check evidence %+v", contract.Verification, want)
	}
}

// Running a check is an execution objective, not a mutation. The check is still
// required evidence: a clean stop without running it is not a completion.
func TestBuildIntentContractSeparatesExecutionFromMutation(t *testing.T) {
	contract := BuildIntentContract("Run go test ./...")
	if contract.Kind != ObjectiveExecution {
		t.Fatalf("kind = %s, want execution", contract.Kind)
	}
	if contract.Ambiguous {
		t.Fatal("an execution objective was marked ambiguous")
	}
	if !reflect.DeepEqual(contract.Verification, []VerificationRequirement{{Kind: VerifyScope, Scope: "*", Check: CheckTest}}) {
		t.Fatalf("verification = %+v, want a repo-wide check", contract.Verification)
	}
}

// A behavioural objective that names the package scope keeps that scope: a
// passing check in another package must not stand in for it.
func TestBuildIntentContractKeepsProseVerificationScope(t *testing.T) {
	contract := BuildIntentContract("Fix the failing tests in tools.")
	if !contract.BehavioralObjective || contract.Ambiguous {
		t.Fatalf("contract = %+v, want a bounded behavioural objective", contract)
	}
	found := false
	for _, requirement := range contract.Verification {
		if requirement.Kind == VerifyScope && requirement.Scope == "tools" {
			found = true
		}
	}
	if !found {
		t.Fatalf("verification = %+v, want a tools scope", contract.Verification)
	}
}

// An informational request keeps the existing intent authority and never acquires a
// completion requirement from a symbol the answer may mention.
func TestBuildIntentContractKeepsInformationalRequestsInformational(t *testing.T) {
	for _, text := range []string{
		"Explain what GrepTool returns.",
		"What does tidy up mean here?",
		"Show me how to update the config.",
		"Explain how to make sure the server times out.",
		"How do I fix this?",
		"Why does clean up fail?",
	} {
		contract := BuildIntentContract(text)
		if contract.Kind != ObjectiveInformational {
			t.Errorf("BuildIntentContract(%q).Kind = %s, want informational", text, contract.Kind)
		}
		if len(contract.Verification) != 0 {
			t.Errorf("BuildIntentContract(%q).Verification = %+v, want none", text, contract.Verification)
		}
		if contract.Ambiguous {
			t.Errorf("BuildIntentContract(%q) is ambiguous", text)
		}
	}
}

// The Phase 3 compound and vocabulary guarantees must survive the extraction:
// a later mutation clause is still the objective, and the request is still not
// given an invented target.
func TestBuildIntentContractPreservesPhase3Vocabulary(t *testing.T) {
	for _, text := range []string{
		"Inspect the server and then add a timeout.",
		"Tidy up the imports.",
		"Make sure the server times out after 5s.",
		"Organise the package.",
	} {
		contract := BuildIntentContract(text)
		if contract.Kind != ObjectiveMutation {
			t.Errorf("BuildIntentContract(%q).Kind = %s, want mutation", text, contract.Kind)
		}
	}
}

// A qualified symbol is one target. Its segments are not separate bare
// targets; otherwise a correct method edit would be blocked by a missing
// package or ambiguous method name.
func TestBuildIntentContractDoesNotSplitQualifiedSymbols(t *testing.T) {
	contract := BuildIntentContract("Fix tools.GrepTool.Execute.")
	var symbols []string
	for _, target := range contract.Targets {
		if target.Kind == TargetSymbol {
			symbols = append(symbols, target.Value)
		}
	}
	if !reflect.DeepEqual(symbols, []string{"tools.GrepTool.Execute"}) {
		t.Fatalf("symbol targets = %v, want only the qualified name", symbols)
	}
}

// A package target is explicit only when the word "package" names it. An
// ordinary word after a path must not become a package requirement.
func TestBuildIntentContractExtractsExplicitPackages(t *testing.T) {
	cases := map[string]string{
		"Fix the bug in package tools.":     "tools",
		"Fix the bug in the tools package.": "tools",
	}
	for text, want := range cases {
		contract := BuildIntentContract(text)
		found := false
		for _, target := range contract.Targets {
			if target.Kind == TargetPackage && target.Value == want {
				found = true
			}
		}
		if !found {
			t.Errorf("BuildIntentContract(%q).Targets = %+v, want package %q", text, contract.Targets, want)
		}
	}
}

// "package declared" and "package name" are prose, not coordinates. The only
// repository target is the file the question names.
func TestBuildIntentContractDoesNotInventPackagesFromQuestions(t *testing.T) {
	contract := BuildIntentContract("What is the package declared in tui/approval.go? Answer with the package name only.")
	if contract.Kind != ObjectiveInformational {
		t.Fatalf("kind = %s, want informational", contract.Kind)
	}
	for _, target := range contract.Targets {
		if target.Kind == TargetPackage {
			t.Fatalf("invented package target %#v from question prose", target)
		}
	}
	foundPath := false
	for _, target := range contract.Targets {
		if target.Kind == TargetPath && target.Value == "tui/approval.go" {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("targets = %#v, want tui/approval.go", contract.Targets)
	}
}

func TestBuildIntentContractEmptyRequest(t *testing.T) {
	contract := BuildIntentContract("   ")
	if contract.Kind != ObjectiveInformational {
		t.Fatalf("kind = %s, want informational", contract.Kind)
	}
	if !contract.Ambiguous || len(contract.Issues) == 0 {
		t.Fatalf("contract = %+v, want an explicit empty-request issue", contract)
	}
}

// Verification scopes retain their real shape: a recursive package pattern is
// not flattened into a non-recursive one, and repo-wide is distinguishable.
func TestBuildIntentContractVerificationScopeShapes(t *testing.T) {
	cases := []struct {
		text string
		want VerificationRequirement
	}{
		{"Run go test ./tools/...", VerificationRequirement{Kind: VerifyScope, Scope: "tools", Check: CheckTest}},
		{"Run go test ./...", VerificationRequirement{Kind: VerifyScope, Scope: "*", Check: CheckTest}},
		{"Run go test .", VerificationRequirement{Kind: VerifyScope, Scope: ".", Check: CheckTest}},
	}
	for _, tc := range cases {
		contract := BuildIntentContract(tc.text)
		if len(contract.Verification) != 1 {
			t.Errorf("BuildIntentContract(%q).Verification = %+v, want one", tc.text, contract.Verification)
			continue
		}
		if contract.Verification[0].Kind != tc.want.Kind ||
			contract.Verification[0].Scope != tc.want.Scope ||
			contract.Verification[0].EffectiveCheck() != tc.want.Check {
			t.Errorf("BuildIntentContract(%q).Verification[0] = %+v, want %+v",
				tc.text, contract.Verification[0], tc.want)
		}
	}
}

// The contract is bounded: an adversarial request cannot make the verifier
// read an unbounded list of coordinates.
func TestBuildIntentContractBoundsTargets(t *testing.T) {
	contract := BuildIntentContract("Update a.go b.go c.go d.go e.go f.go g.go h.go i.go j.go k.go")
	if len(contract.Targets) > 8 {
		t.Fatalf("targets = %d, want at most 8", len(contract.Targets))
	}
	if len(contract.Issues) == 0 {
		t.Fatal("truncating a target list must be recorded")
	}
}
