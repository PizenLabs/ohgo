package gry

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// Ranked search for "NewOrder" returns NewOrderView/NewOrderSummary before the
// exact declaration. A locate question that names a symbol must ask for the
// exact name, not accept lexical neighbours.
func TestResolvePrefersExactNamePattern(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		if len(args) < 3 || args[0] != "search" {
			t.Fatalf("unexpected command: %v", args)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--name-pattern") || !strings.Contains(joined, "^NewOrder$") {
			t.Fatalf("search did not request the exact name: %v", args)
		}
		return []byte(`{"total":1,"returned":1,"results":[
			{"qn":"pkg.domain.NewOrder","name":"NewOrder","label":"Function","file":"order.go","lines":[99,123],"in":13,"out":18}
		]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentLocate, Focus: "NewOrder"})
	if obs.Outcome != investigate.OutcomeSuccess {
		t.Fatalf("outcome = %s, issues=%v", obs.Outcome, obs.Issues)
	}
	if len(run.calls) != 1 {
		t.Fatalf("locate ran %d searches; exact resolution should not need a fallback: %v", len(run.calls), run.calls)
	}
	if !strings.Contains(obs.Summary, "NewOrder") {
		t.Fatalf("observation summary = %q, want the resolved symbol", obs.Summary)
	}
}

// Test call sites are impact evidence. Omitting them made a symbol with
// thirteen test callers look unused.
func TestTraceIncludesTestCallSites(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if args[0] != "trace" || !strings.Contains(joined, "--include-tests") {
			t.Fatalf("trace did not include test call sites: %v", args)
		}
		return []byte(`{"found":true,"start":{"qn":"pkg.NewOrder"},"rows":[
			{"hop":1,"direction":"inbound","edge":"CALLS","qn":"pkg.TestNewOrder","name":"TestNewOrder","label":"Function","file":"x_test.go","lines":[10,20],"via_qn":"pkg.NewOrder","observed":false}
		],"total":1,"returned":1,"depth":2,"truncated":false}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentImpact, Focus: "NewOrder"})
	if obs.Outcome != investigate.OutcomeSuccess {
		t.Fatalf("outcome = %s, issues=%v", obs.Outcome, obs.Issues)
	}
	if len(obs.Facts) == 0 || !strings.Contains(obs.Facts[0].Detail, "inbound") {
		t.Fatalf("trace fact was not recorded: %+v", obs.Facts)
	}
}
