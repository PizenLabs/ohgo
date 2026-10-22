package agent

import (
	"testing"

	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/security"
)

func evaluate(root string, intent contract.IntentContract, resolver StructuralResolver, snapshot *TargetSnapshot, turns []Turn) Completion {
	return EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Resolver: resolver,
		Root:     root,
	})
}

// A change to the named target followed by a successful check is completion.
func TestEvaluateTaskCompletionAcceptsVerifiedTarget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := pathContract("note.txt", contract.ChangeModify, contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "note.txt", "new\n")

	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if !verdict.Complete() {
		t.Fatalf("verified target was not complete: %s", verdict.Reason)
	}
}

// A successful check does not cover a change to a different file.
func TestEvaluateTaskCompletionRejectsWrongTarget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	writeTarget(t, root, "other.txt", "old\n")
	intent := pathContract("note.txt", contract.ChangeModify, contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "other.txt", "new\n")

	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "other.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("a wrong target was reported complete")
	}
}

// A change that is reverted before the check leaves no target evidence, even
// though two mutating tool calls were observed.
func TestEvaluateTaskCompletionRejectsRevertedTarget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := pathContract("note.txt", contract.ChangeModify, contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "note.txt", "new\n")
	writeTarget(t, root, "note.txt", "old\n")

	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("a reverted target was reported complete")
	}
}

// A correct target plus an unrelated deletion is not completion.
func TestEvaluateTaskCompletionRejectsUnexpectedMutation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	writeTarget(t, root, "important.txt", "keep\n")
	intent := pathContract("note.txt", contract.ChangeModify, contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "note.txt", "new\n")
	removeTarget(t, root, "important.txt")

	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{
			mutationOutcome(root, "note.txt"),
			mutationOutcome(root, "important.txt"),
		}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("an out-of-scope mutation was reported complete")
	}
}

// A shell mutation the runtime cannot attribute to a target is uncertainty,
// even when the named target also changed and a check passed.
func TestEvaluateTaskCompletionRejectsUnattributedMutation(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := pathContract("note.txt", contract.ChangeModify, contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "note.txt", "new\n")

	bash := ToolOutcome{
		ToolName: "bash",
		Result: Result{
			Text:     "committed",
			Mutates:  true,
			Executes: true,
			Details:  map[string]any{"command": "git commit -m change"},
		},
		Authorization: &security.Decision{
			Capability: security.CapabilityExec,
			Mutates:    true,
			Scope:      security.NormalizeScope(root, "."),
		},
	}
	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt"), bash}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("an unattributed shell mutation was reported complete")
	}
}

// An unbounded mutation objective is uncertain even with a change and a check.
func TestEvaluateTaskCompletionRejectsAmbiguousObjective(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := contract.IntentContract{
		Objective:    "update the code",
		Kind:         contract.ObjectiveMutation,
		Ambiguous:    true,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	writeTarget(t, root, "note.txt", "new\n")

	verdict := evaluate(root, intent, nil, nil, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("an ambiguous mutation objective was reported complete")
	}
}

// A behavioural objective is bounded by the check, so it does not need a path.
func TestEvaluateTaskCompletionAcceptsBehavioralObjective(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := contract.IntentContract{
		Objective:           "fix the failing tests",
		Kind:                contract.ObjectiveMutation,
		BehavioralObjective: true,
		Verification:        []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	writeTarget(t, root, "note.txt", "new\n")

	verdict := evaluate(root, intent, nil, nil, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if !verdict.Complete() {
		t.Fatalf("behavioural objective was not complete: %s", verdict.Reason)
	}
}

// An execution request that named a check is not complete unless the check ran.
func TestEvaluateTaskCompletionRejectsMissingExecutionCheck(t *testing.T) {
	intent := contract.IntentContract{
		Objective:    "run the tests",
		Kind:         contract.ObjectiveExecution,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyScope, Scope: "*"}},
	}
	verdict := evaluate("", intent, nil, nil, []Turn{cleanStop()})
	if verdict.Complete() {
		t.Fatal("an execution request with no check was reported complete")
	}
}

// A generic "run the tests" request is also a required check, not an optional
// hint. This guards the any-check branch independently of scope coverage.
func TestEvaluateTaskCompletionRejectsMissingGenericExecutionCheck(t *testing.T) {
	intent := contract.IntentContract{
		Objective:    "run the tests",
		Kind:         contract.ObjectiveExecution,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	verdict := evaluate("", intent, nil, nil, []Turn{cleanStop()})
	if verdict.Complete() {
		t.Fatal("an execution request with no check was reported complete")
	}
}

// The same execution request completes when the named check actually ran.
func TestEvaluateTaskCompletionAcceptsExecutedCheck(t *testing.T) {
	intent := contract.IntentContract{
		Objective:    "run the tests",
		Kind:         contract.ObjectiveExecution,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyScope, Scope: "*"}},
	}
	verdict := evaluate("", intent, nil, nil, []Turn{
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if !verdict.Complete() {
		t.Fatalf("executed check was not complete: %s", verdict.Reason)
	}
}

// A read request that mutates is not a successful read.
func TestEvaluateTaskCompletionRejectsMutationDuringInformationalRequest(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := contract.IntentContract{Objective: "explain this", Kind: contract.ObjectiveInformational}
	writeTarget(t, root, "note.txt", "new\n")

	verdict := evaluate(root, intent, nil, nil, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("a mutating read request was reported complete")
	}
}

// A pure answer is a complete informational turn.
func TestEvaluateTaskCompletionAcceptsInformationalAnswer(t *testing.T) {
	intent := contract.IntentContract{Objective: "explain this", Kind: contract.ObjectiveInformational}
	verdict := evaluate("", intent, nil, nil, []Turn{cleanStop()})
	if !verdict.Complete() {
		t.Fatalf("informational answer was not complete: %s", verdict.Reason)
	}
}

// A check in another package does not cover a Go target.
func TestEvaluateTaskCompletionRejectsUnrelatedVerificationScope(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"bug\" }\n")
	intent := pathContract("tools/grep.go", contract.ChangeModify,
		contract.VerificationRequirement{Kind: contract.VerifyScope, Scope: "tools"})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"fixed\" }\n")

	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "tools/grep.go")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{Path: "ast"})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("an unrelated package's check was accepted as verification")
	}
}

// A target the index cannot resolve is uncertainty, not a pass.
func TestEvaluateTaskCompletionRejectsUnresolvedTarget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := symbolContract("MissingSymbol", contract.ChangeModify,
		contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, newIndexResolver(t, root))
	writeTarget(t, root, "note.txt", "new\n")

	verdict := evaluate(root, intent, newIndexResolver(t, root), snapshot, []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("an unresolved target was reported complete")
	}
}

// A verification before the last change is not evidence that the final state
// works.
func TestEvaluateTaskCompletionRejectsVerificationBeforeChange(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", "old\n")
	intent := pathContract("note.txt", contract.ChangeModify, contract.VerificationRequirement{Kind: contract.VerifyAny})
	snapshot := CaptureTargets(root, intent, nil)
	writeTarget(t, root, "note.txt", "new\n")

	verdict := evaluate(root, intent, nil, snapshot, []Turn{
		{Results: []ToolOutcome{verificationOutcome(VerificationScope{All: true})}},
		{Results: []ToolOutcome{mutationOutcome(root, "note.txt")}},
		cleanStop(),
	})
	if verdict.Complete() {
		t.Fatal("verification before the change was accepted")
	}
}

// A symbol target derives its package directory as a verification scope, even
// when the request only says "verify". A check in another package cannot stand
// in for the changed symbol.
func TestEvaluateTaskCompletionDerivesSymbolVerificationScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  VerificationScope
		wantOK bool
	}{
		{"same-package", VerificationScope{Path: "tools"}, true},
		{"repo-wide", VerificationScope{All: true}, true},
		{"other-package", VerificationScope{Path: "ast"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"bug\" }\n")
			intent := symbolContract("GrepTool", contract.ChangeModify,
				contract.VerificationRequirement{Kind: contract.VerifyAny})
			resolver := newIndexResolver(t, root)
			snapshot := CaptureTargets(root, intent, resolver)
			writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"fixed\" }\n")

			verdict := evaluate(root, intent, resolver, snapshot, []Turn{
				{Results: []ToolOutcome{mutationOutcome(root, "tools/grep.go")}},
				{Results: []ToolOutcome{verificationOutcome(tc.scope)}},
				cleanStop(),
			})
			if verdict.Complete() != tc.wantOK {
				t.Fatalf("complete = %v, want %v (reason %q)", verdict.Complete(), tc.wantOK, verdict.Reason)
			}
		})
	}
}

// A passing build is not evidence that the tests pass. A test is evidence that
// the package builds, so it covers a build requirement.
func TestEvaluateTaskCompletionMatchesCheckKind(t *testing.T) {
	cases := []struct {
		name     string
		required contract.VerificationCheck
		provided contract.VerificationCheck
		wantOK   bool
	}{
		{"test-for-test", contract.CheckTest, contract.CheckTest, true},
		{"build-for-test", contract.CheckTest, contract.CheckBuild, false},
		{"test-for-build", contract.CheckBuild, contract.CheckTest, true},
		{"build-for-build", contract.CheckBuild, contract.CheckBuild, true},
		{"vet-for-test", contract.CheckTest, contract.CheckVet, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"bug\" }\n")
			intent := symbolContract("GrepTool", contract.ChangeModify,
				contract.VerificationRequirement{Kind: contract.VerifyScope, Scope: "tools", Check: tc.required})
			resolver := newIndexResolver(t, root)
			snapshot := CaptureTargets(root, intent, resolver)
			writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"fixed\" }\n")

			verdict := evaluate(root, intent, resolver, snapshot, []Turn{
				{Results: []ToolOutcome{mutationOutcome(root, "tools/grep.go")}},
				{Results: []ToolOutcome{verificationOutcomeWithCheck(tc.provided, VerificationScope{Path: "tools"})}},
				cleanStop(),
			})
			if verdict.Complete() != tc.wantOK {
				t.Fatalf("complete = %v, want %v (reason %q)", verdict.Complete(), tc.wantOK, verdict.Reason)
			}
		})
	}
}

// A targetless behavioural objective is bounded by the packages that actually
// changed: a passing check in an unrelated package is not evidence.
func TestEvaluateTaskCompletionBoundsBehavioralObjectiveToChangedPackage(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"bug\" }\n")
	intent := contract.IntentContract{
		Objective:           "fix the failing tests",
		Kind:                contract.ObjectiveMutation,
		BehavioralObjective: true,
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyScope, Scope: "*", Check: contract.CheckTest,
		}},
	}
	resolver := newIndexResolver(t, root)
	snapshot := CaptureTargets(root, intent, resolver)
	writeTarget(t, root, "tools/grep.go", "package tools\n\nfunc GrepTool() string { return \"fixed\" }\n")

	for _, tc := range []struct {
		name   string
		scope  VerificationScope
		wantOK bool
	}{
		{"changed-package", VerificationScope{Path: "tools"}, true},
		{"repo-wide", VerificationScope{All: true}, true},
		{"other-package", VerificationScope{Path: "ast"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict := evaluate(root, intent, resolver, snapshot, []Turn{
				{Results: []ToolOutcome{mutationOutcome(root, "tools/grep.go")}},
				{Results: []ToolOutcome{verificationOutcomeWithCheck(contract.CheckTest, tc.scope)}},
				cleanStop(),
			})
			if verdict.Complete() != tc.wantOK {
				t.Fatalf("complete = %v, want %v (reason %q)", verdict.Complete(), tc.wantOK, verdict.Reason)
			}
		})
	}
}

// Without a contract there is no bounded expectation to complete against.
func TestEvaluateTaskCompletionRequiresAContract(t *testing.T) {
	if EvaluateTaskCompletion(CompletionInput{Turns: []Turn{cleanStop()}}).Complete() {
		t.Fatal("completion without a contract was accepted")
	}
}
