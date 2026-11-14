package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// TestCompactionProjectionIsDeterministicAndStructured defends the state-first
// contract: the same runtime state produces the same bytes, with authority,
// objective, completion, and evidence classes explicit.
func TestCompactionProjectionIsDeterministicAndStructured(t *testing.T) {
	shape := TaskShape{
		Objective:   "fix the failing store test",
		Kind:        contract.ObjectiveMutation,
		Authority:   Authority{Workspace: "build", MutationAllowed: true, ExecutionAllowed: true},
		Constraints: []string{"do not change the public API"},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyScope, Scope: "./internal/store", Check: contract.CheckTest,
		}},
	}
	evidence := NewEvidenceState()
	evidence.Items = []EvidenceItem{
		{ID: "verification:test", Status: StatusKnown, Seq: 2, Summary: "go test ./internal/store passed",
			Source: Source{Check: "test", Scope: "./internal/store"}},
		{ID: "mutation:store.go", Status: StatusKnown, Seq: 1, Summary: "store.go changed",
			Provenance: ProvenanceObservation, Source: Source{Path: "internal/store/store.go"}},
	}
	first := CompactionProjection(shape, evidence, "NEEDS_REVIEW", "run the store test", nil)
	second := CompactionProjection(shape, evidence, "NEEDS_REVIEW", "run the store test", nil)
	if first.Digest != "" || second.Digest != "" {
		t.Fatal("projection filled a digest before validation")
	}
	if strings.Join(first.Blocks, "\n") != strings.Join(second.Blocks, "\n") {
		t.Fatal("projection is not deterministic")
	}
	for _, want := range []string{"build", "fix the failing store test", "NEEDS_REVIEW", "do not change the public API"} {
		joined := first.Authority + "\n" + first.Objective + "\n" + first.Completion + "\n" + strings.Join(first.Blocks, "\n")
		if !strings.Contains(joined, want) {
			t.Fatalf("projection lost %q:\n%s", want, joined)
		}
	}
	if first.Counts.Verifications != 1 || first.Counts.Changes != 1 {
		t.Fatalf("counts = %+v, want one verification and one change", first.Counts)
	}
}

// TestValidateCompactionProjectionRejectsAuthoritativeDrift defends the trust
// boundary: a candidate may add evidence detail but cannot change authority,
// objective, or completion state.
func TestValidateCompactionProjectionRejectsAuthoritativeDrift(t *testing.T) {
	baseline := CompactionProjection(TaskShape{
		Objective: "explain the allocator",
		Kind:      contract.ObjectiveInformational,
		Authority: Authority{Workspace: "ask", ReadOnly: true},
	}, NewEvidenceState(), "IN_PROGRESS", "", nil)
	if err := ValidateCompactionProjection(baseline, baseline); err != nil {
		t.Fatalf("baseline rejected itself: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(agent.ContextProjection) agent.ContextProjection
	}{
		{"authority", func(p agent.ContextProjection) agent.ContextProjection { p.Authority = "BUILD"; return p }},
		{"objective", func(p agent.ContextProjection) agent.ContextProjection {
			p.Objective = "change the allocator"
			return p
		}},
		{"completion", func(p agent.ContextProjection) agent.ContextProjection { p.Completion = "PROVEN"; return p }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := tc.mutate(baseline)
			if err := ValidateCompactionProjection(candidate, baseline); err == nil {
				t.Fatalf("candidate with changed %s was accepted", tc.name)
			}
		})
	}
}
