package agent

import "testing"

// Scope coverage is the behavioural half of the contract. A check that only
// covered another package must never satisfy a target in this one.
func TestVerificationScopeCoverage(t *testing.T) {
	cases := []struct {
		name     string
		scope    VerificationScope
		required string
		want     bool
	}{
		{"exact", VerificationScope{Path: "tools"}, "tools", true},
		{"other", VerificationScope{Path: "ast"}, "tools", false},
		{"recursive-child", VerificationScope{Path: "tools", Recursive: true}, "tools/sub", true},
		{"non-recursive-child", VerificationScope{Path: "tools"}, "tools/sub", false},
		{"repo-wide", VerificationScope{All: true}, "tools/sub", true},
		{"repo-wide-wildcard", VerificationScope{All: true}, "*", true},
		{"root", VerificationScope{Path: "."}, ".", true},
		{"root-is-not-all", VerificationScope{Path: "."}, "tools", false},
		{"narrow-is-not-wildcard", VerificationScope{Path: "tools"}, "*", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Covers(tc.required); got != tc.want {
				t.Fatalf("%+v.Covers(%q) = %v, want %v", tc.scope, tc.required, got, tc.want)
			}
		})
	}
}

// A child's checks are carried as real scopes, deduplicated across turns.
func TestVerificationFromTurnsCollectsScopes(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{{Result: Result{
			Verifies:     true,
			Verification: &VerificationEvidence{Command: "go test ./tools", Scopes: []VerificationScope{{Path: "tools"}}},
		}}}},
		{Results: []ToolOutcome{{Result: Result{
			Verifies:     true,
			Verification: &VerificationEvidence{Command: "go test ./ast", Scopes: []VerificationScope{{Path: "ast"}}},
		}}}},
	}
	evidence := verificationFromTurns(turns)
	if evidence == nil {
		t.Fatal("verification evidence was dropped")
	}
	if !evidence.Covers("tools") || !evidence.Covers("ast") {
		t.Fatalf("scopes = %+v, want tools and ast", evidence.Scopes)
	}
	if len(evidence.Scopes) != 2 {
		t.Fatalf("scopes = %+v, want deduplicated", evidence.Scopes)
	}
}

// An unscoped check is not evidence for a scoped target. This is the difference
// between "a test passed" and "the changed package's test passed".
func TestUnscopedVerificationDoesNotCoverATarget(t *testing.T) {
	var evidence *VerificationEvidence
	if evidence.Covers("tools") {
		t.Fatal("nil evidence claimed coverage")
	}
	evidence = &VerificationEvidence{Command: "pytest"}
	if evidence.Covers("tools") {
		t.Fatal("an unscoped check claimed coverage")
	}
}
