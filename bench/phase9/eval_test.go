package main

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// TestPhase9Matrix runs the controlled matrix in both modes.
//
// The scripted model makes the action sequence identical, so a measured
// difference is attributable to the runtime's context and action surface. The
// test asserts both the safety contract (the same observable outcome) and the
// efficiency contract (no regression in input tokens).
func TestPhase9Matrix(t *testing.T) {
	if testing.Short() {
		t.Skip("phase9 matrix runs real tool and go build checks")
	}
	for _, sc := range matrix() {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			baseline, err := runScenario(context.Background(), sc, "off")
			if err != nil {
				t.Fatalf("baseline: %v", err)
			}
			if !expectationMet(sc.Expect, baseline) {
				t.Fatalf("baseline outcome = %s (%s), unexpected=%v",
					baseline.ObjectiveStatus, baseline.CompletionReason, baseline.Unexpected)
			}
			projected, err := runScenario(context.Background(), sc, "state")
			if err != nil {
				t.Fatalf("projected: %v", err)
			}
			if !expectationMet(sc.Expect, projected) {
				t.Fatalf("projected outcome = %s (%s), unexpected=%v",
					projected.ObjectiveStatus, projected.CompletionReason, projected.Unexpected)
			}
			if projected.InputTokens >= baseline.InputTokens {
				t.Fatalf("projection increased input tokens: %d -> %d",
					baseline.InputTokens, projected.InputTokens)
			}
			if projected.ToolSchemaBytes > baseline.ToolSchemaBytes {
				t.Fatalf("projection increased tool schema bytes: %d -> %d",
					baseline.ToolSchemaBytes, projected.ToolSchemaBytes)
			}
			if projected.EvidenceItems == 0 && sc.Expect.Mutation {
				t.Fatal("a mutation scenario produced no evidence")
			}
		})
	}
}

func TestPhase10Matrix(t *testing.T) {
	if testing.Short() {
		t.Skip("phase10 matrix runs real tool and go build checks")
	}
	for _, sc := range phase10Matrix() {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			baseline, err := runScenario(context.Background(), sc, "off")
			if err != nil {
				t.Fatalf("baseline: %v", err)
			}
			if !expectationMet(sc.Expect, baseline) {
				t.Fatalf("baseline outcome = %s (%s), unexpected=%v",
					baseline.ObjectiveStatus, baseline.CompletionReason, baseline.Unexpected)
			}
			projected, err := runScenario(context.Background(), sc, "state")
			if err != nil {
				t.Fatalf("projected: %v", err)
			}
			if !expectationMet(sc.Expect, projected) {
				t.Fatalf("projected outcome = %s (%s), unexpected=%v",
					projected.ObjectiveStatus, projected.CompletionReason, projected.Unexpected)
			}
			if projected.InputTokens >= baseline.InputTokens {
				t.Fatalf("projection increased input tokens: %d -> %d",
					baseline.InputTokens, projected.InputTokens)
			}
			if projected.ToolSchemaBytes > baseline.ToolSchemaBytes {
				t.Fatalf("projection increased tool schema bytes: %d -> %d",
					baseline.ToolSchemaBytes, projected.ToolSchemaBytes)
			}
		})
	}
}

func TestPhase9StopsExplorationAfterProof(t *testing.T) {
	if testing.Short() {
		t.Skip("phase9 proof stop runs a real go build")
	}
	sc := scenarioByID(t, "K7-stop-after-proof")
	baseline, err := runScenario(context.Background(), sc, "off")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := runScenario(context.Background(), sc, "state")
	if err != nil {
		t.Fatal(err)
	}
	if projected.CandidateRejections == 0 {
		t.Fatal("the post-verification exploration call was not withheld")
	}
	if projected.ObjectiveStatus != "COMPLETED" {
		t.Fatalf("a withheld candidate call must not downgrade completion: %s (%s)",
			projected.ObjectiveStatus, projected.CompletionReason)
	}
	if projected.InputTokens >= baseline.InputTokens {
		t.Fatalf("proof-aware projection did not reduce input tokens: %d -> %d",
			baseline.InputTokens, projected.InputTokens)
	}
}

func TestPhase9LargeContextRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("phase9 retention test reads a large fixture")
	}
	sc := scenarioByID(t, "K6-large-source-context")
	baseline, err := runScenario(context.Background(), sc, "off")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := runScenario(context.Background(), sc, "state")
	if err != nil {
		t.Fatal(err)
	}
	// The retention failure mode is "the final request still carries the old
	// large file". A 30% reduction is the floor that proves the working-set
	// budget engaged; the exact ratio is fixture-specific.
	if projected.LastRequestBytes >= baseline.LastRequestBytes*7/10 {
		t.Fatalf("large-source retention did not shrink the final request enough: %d vs %d",
			projected.LastRequestBytes, baseline.LastRequestBytes)
	}
}

func TestLiveScenarioPlumbingWithScriptedTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("live plumbing runs the real tool set")
	}
	sc := scenarioByID(t, "A-pure-read")
	model := newScriptedModel(sc.Steps...)
	live := liveModel{
		model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		stream: model.stream,
		ref:    "test/fake",
	}
	metrics, err := runLiveScenario(context.Background(), sc, live, "state")
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ProviderInputTokens == 0 {
		t.Fatal("live metrics did not record provider usage")
	}
	if metrics.ObjectiveStatus != "COMPLETED" {
		t.Fatalf("status = %s (%s)", metrics.ObjectiveStatus, metrics.CompletionReason)
	}
}

func scenarioByID(t *testing.T, id string) scenario {
	t.Helper()
	for _, sc := range matrix() {
		if sc.ID == id {
			return sc
		}
	}
	t.Fatalf("unknown scenario %q", id)
	return scenario{}
}
