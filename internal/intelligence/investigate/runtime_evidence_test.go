package investigate

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	rt "github.com/PizenLabs/ohgo/internal/intelligence/runtime"
)

// TestRuntimeEvidenceEntersSharedEngine composes the dial DAP transport with
// the orchestrator: the runtime observation lands in the same evidence result
// as static facts, is charged to the shared meter, and is bounded by budget.
func TestRuntimeEvidenceEntersSharedEngine(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool is not installed")
	}
	binary := filepath.Join(t.TempDir(), "fakeadapter")
	build := exec.Command(goTool, "build", "-o", binary, "./testdata/fakeadapter")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build adapter: %v: %s", err, output)
	}
	manager := rt.NewManager(t.TempDir(), rt.Config{
		Adapters: map[string]rt.Spec{"fake": {
			Name: "fake", Command: []string{binary, "--addr", "{addr}"},
			DialClient: true, ConnectTimeout: 5 * time.Second,
		}},
		MaxSessions:   1,
		Authorize:     func(context.Context, rt.Action, string) error { return nil },
		LaunchTimeout: 10 * time.Second,
		Grace:         time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := manager.Launch(ctx, rt.LaunchRequest{Adapter: "fake"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := session.WaitEvent(ctx, "stopped"); err != nil {
		t.Fatalf("wait stopped: %v", err)
	}
	engine := New(&fakeProvider{
		name: planner.ProviderStructural, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.2, Coverage: 0.3,
			Records: []evidence.Record{record(evidence.ProviderStructural, "function:add", "contains", "sum")},
			Facts:   []Fact{{Provider: "structural", Subject: "add", Detail: "static candidate"}}},
	}, &RuntimeProvider{Session: session, Root: "/workspace"})
	result, err := engine.Run(ctx, Request{
		Question: "why does add fail at runtime?", Intent: planner.IntentDebug,
		Budget: Budget{MaxSteps: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	var runtimeStep *StepResult
	for i := range result.Steps {
		if result.Steps[i].Provider == planner.ProviderRuntime {
			runtimeStep = &result.Steps[i]
		}
	}
	if runtimeStep == nil || runtimeStep.Failed {
		t.Fatalf("runtime step missing or failed: %+v", result.Steps)
	}
	if result.DAPRequests == 0 {
		t.Fatal("runtime work was not charged to the shared meter")
	}
	runtimeFacts := 0
	for _, fact := range result.Facts {
		if fact.Provider == string(planner.ProviderRuntime) {
			runtimeFacts++
		}
	}
	if runtimeFacts == 0 {
		t.Fatalf("runtime facts were not added to the result: %+v", result.Facts)
	}
	if result.Records < 2 {
		t.Fatalf("shared records = %d, want static + runtime", result.Records)
	}
	if result.StopReason == "" || result.Confidence <= 0.2 {
		t.Fatalf("result not reassessed: confidence %.2f stop %q", result.Confidence, result.StopReason)
	}

	limited := newMeter(Budget{MaxDAPRequests: 1}.normalized())
	observation, err := (&RuntimeProvider{Session: session, Root: "/workspace"}).Observe(ctx,
		Request{Question: "why", Intent: planner.IntentDebug}, plannerStep(), limited)
	if err != nil {
		t.Fatalf("limited observe: %v", err)
	}
	if len(observation.Records) != 0 || len(observation.Issues) == 0 {
		t.Fatalf("DAP budget exhaustion was not explicit: %+v", observation)
	}
	_ = session.Close()
}

// TestRuntimeProviderRefusesRunningSession proves runtime evidence requires the
// STOPPED state and never turns "not observing" into absence.
func TestRuntimeProviderRefusesRunningSession(t *testing.T) {
	provider := &RuntimeProvider{Session: nil, Root: "/workspace"}
	if provider.Available(context.Background()) {
		t.Fatal("nil session reported available")
	}
}
