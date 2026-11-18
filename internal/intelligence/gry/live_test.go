package gry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// TestLiveCompatibility is an opt-in test against a real gry installation and a
// real index of this repository.
//
// It is skipped by default so the normal suite never depends on another tool's
// build, cache, or index state. Set OHGO_GRY_LIVE=1 to run it. The test proves
// the adapter's documented query surface still decodes and that its evidence
// never reaches VERIFIED.
func TestLiveCompatibility(t *testing.T) {
	if os.Getenv("OHGO_GRY_LIVE") != "1" {
		t.Skip("set OHGO_GRY_LIVE=1 to run against a real gry installation")
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Skip(err)
	}
	provider := New(Config{Root: root, Timeout: 10 * time.Second})
	if !provider.Available(context.Background()) {
		t.Skip("gry is not installed")
	}
	meter := investigate.NewMeter(investigate.Budget{MaxExternalRequests: 8})

	tests := []struct {
		name string
		req  investigate.Request
	}{
		{"architecture", investigate.Request{Intent: planner.IntentArchitecture}},
		{"resolve", investigate.Request{Intent: planner.IntentLocate, Focus: "investigate.Engine.Run"}},
		{"trace", investigate.Request{Intent: planner.IntentFlow, Focus: "ohgo.internal.intelligence.investigate.investigate.Engine.Run"}},
		{"context", investigate.Request{Intent: planner.IntentDebug, Question: "how are providers selected", Focus: "investigate.Engine.Run"}},
		{"change-impact", investigate.Request{Intent: planner.IntentImpact}},
		{"test-impact", investigate.Request{Intent: planner.IntentVerify}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, err := provider.Observe(context.Background(), test.req, planner.Step{Strategy: test.name}, meter)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Outcome == investigate.OutcomeNotIndexed || observation.Outcome == investigate.OutcomeUnavailable {
				t.Skipf("gry is installed but this repository is not indexed: %s", strings.Join(observation.Issues, "; "))
			}
			if observation.Outcome == investigate.OutcomeFailed {
				t.Fatalf("gry %s failed: %s", test.name, strings.Join(observation.Issues, "; "))
			}
			if len(observation.Facts) == 0 {
				t.Fatalf("gry %s returned no bounded fact: %+v", test.name, observation)
			}
			for _, record := range observation.Records {
				if record.State == evidence.Verified {
					t.Fatalf("gry evidence reached VERIFIED: %+v", record)
				}
				if record.Provider != evidence.ProviderStructural || record.Source != evidence.SourceGRY {
					t.Fatalf("gry provenance is not preserved: %+v", record)
				}
			}
			t.Logf("outcome=%s facts=%d records=%d summary=%s",
				observation.Outcome, len(observation.Facts), len(observation.Records), observation.Summary)
		})
	}
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// TestLiveAmbiguityAndUnresolved exercises the two failure shapes that are
// easiest to accidentally flatten: a short name with many candidates, and a
// target the index cannot resolve. Both must remain explicit.
func TestLiveAmbiguityAndUnresolved(t *testing.T) {
	if os.Getenv("OHGO_GRY_LIVE") != "1" {
		t.Skip("set OHGO_GRY_LIVE=1 to run against a real gry installation")
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Skip(err)
	}
	provider := New(Config{Root: root, Timeout: 10 * time.Second})
	if !provider.Available(context.Background()) {
		t.Skip("gry is not installed")
	}
	ambiguous, err := provider.Observe(context.Background(),
		investigate.Request{Intent: planner.IntentFlow, Focus: "Run"},
		planner.Step{Strategy: "trace"}, investigate.NewMeter(investigate.Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous.Outcome == investigate.OutcomeNotIndexed || ambiguous.Outcome == investigate.OutcomeUnavailable {
		t.Skipf("gry is installed but this repository is not indexed: %s", strings.Join(ambiguous.Issues, "; "))
	}
	if ambiguous.Outcome != investigate.OutcomeAmbiguous {
		t.Fatalf("short name outcome = %s, want AMBIGUOUS: %+v", ambiguous.Outcome, ambiguous.Issues)
	}
	if len(ambiguous.Facts) == 0 {
		t.Fatal("ambiguity candidates were dropped")
	}

	unresolved, err := provider.Observe(context.Background(),
		investigate.Request{Intent: planner.IntentFlow, Focus: "definitely_not_a_symbol_xyz"},
		planner.Step{Strategy: "trace"}, investigate.NewMeter(investigate.Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if unresolved.Outcome != investigate.OutcomeUnresolved {
		t.Fatalf("missing target outcome = %s, want UNRESOLVED: %+v", unresolved.Outcome, unresolved.Issues)
	}
	if !strings.Contains(strings.Join(unresolved.Issues, "\n"), "not proof") {
		t.Fatalf("unresolved target became absence: %+v", unresolved.Issues)
	}
	t.Logf("ambiguous facts=%d; unresolved issue=%s", len(ambiguous.Facts), strings.Join(unresolved.Issues, "; "))
}
