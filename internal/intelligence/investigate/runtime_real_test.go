package investigate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	rt "github.com/PizenLabs/ohgo/internal/intelligence/runtime"
)

// TestRealDlvRuntimeEvidence drives a real Delve DAP session through the
// orchestrator. It skips when dlv or the go tool is unavailable.
func TestRealDlvRuntimeEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("real debugger test skipped in -short")
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Skip("dlv is not installed")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool is not installed")
	}
	// macOS blocks debugserver launches while developer mode is disabled: the
	// debuggee starts in a stopped state and Delve never answers launch. Skip
	// instead of reporting a transport failure that is really an environment
	// permission failure.
	if status, err := exec.Command("DevToolsSecurity", "-status").CombinedOutput(); err == nil &&
		strings.Contains(string(status), "disabled") {
		t.Skip("developer mode is disabled; debugserver cannot run the fixture")
	}
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.go")
	source := `package main

import "fmt"

func main() {
	total := add(2, 3)
	fmt.Println(total)
}

func add(a, b int) int {
	sum := a + b
	return sum
}
`
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/debugfixture\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "app")
	build := exec.Command(goTool, "build", "-o", binary, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	arguments, err := json.Marshal(map[string]any{
		"name": "fixture", "type": "go", "request": "launch", "mode": "exec",
		"program": binary, "cwd": root, "stopOnEntry": false,
		"output": filepath.Join(root, "debug.out"),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := rt.NewManager(root, rt.Config{
		Adapters: map[string]rt.Spec{"dlv": {
			Name: "dlv", Command: []string{dlv, "dap", "--client-addr={addr}"},
			Dir: root, DialClient: true, ConnectTimeout: 20 * time.Second,
		}},
		MaxSessions:   1,
		Authorize:     func(context.Context, rt.Action, string) error { return nil },
		LaunchTimeout: 30 * time.Second,
		Grace:         2 * time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := manager.Launch(ctx, rt.LaunchRequest{
		Adapter:   "dlv",
		Arguments: arguments,
		Breakpoints: []rt.SourceBreakpoints{{
			Name: "main.go", Path: mainPath,
			Breakpoints: []rt.SourceBreakpoint{{Line: 11}},
		}},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("launch dlv: %v", err)
	}
	if _, err := session.WaitEvent(ctx, "stopped"); err != nil {
		t.Fatalf("wait stopped: %v; state %s", err, session.State())
	}

	engine := New(&fakeProvider{
		name: planner.ProviderStructural, available: true,
		obs: Observation{State: evidence.Observed, Confidence: 0.3, Coverage: 0.4,
			Records: []evidence.Record{record(evidence.ProviderStructural, "function:add", "contains", "sum")},
			Facts:   []Fact{{Provider: "structural", Subject: "add", Detail: "sum assignment at main.go:11"}}},
	}, &RuntimeProvider{Session: session, Root: root})
	result, err := engine.Run(ctx, Request{
		Question: "why does add return the wrong sum at runtime?", Intent: planner.IntentDebug,
		Budget: Budget{MaxSteps: 3},
	})
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	runtimeSteps := 0
	for _, step := range result.Steps {
		if step.Provider == planner.ProviderRuntime {
			runtimeSteps++
		}
	}
	if runtimeSteps != 1 {
		t.Fatalf("runtime steps = %d, want 1: %+v", runtimeSteps, result.Steps)
	}
	runtimeRecords := 0
	for _, fact := range result.Facts {
		if fact.Provider == string(planner.ProviderRuntime) {
			runtimeRecords++
		}
	}
	if runtimeRecords == 0 {
		t.Fatalf("no runtime facts in the shared result: %+v", result.Facts)
	}
	if result.DAPRequests == 0 {
		t.Fatal("DAP requests were not charged to the shared meter")
	}
	if result.Confidence <= 0.3 {
		t.Fatalf("runtime evidence did not raise confidence: %.2f", result.Confidence)
	}

	// A bounded meter must stop runtime work explicitly, not report absence.
	limited := newMeter(Budget{MaxDAPRequests: 1}.normalized())
	observation, err := (&RuntimeProvider{Session: session, Root: root}).Observe(ctx,
		Request{Question: "why", Intent: planner.IntentDebug}, plannerStep(), limited)
	if err != nil {
		t.Fatalf("limited observe: %v", err)
	}
	if len(observation.Records) != 0 || len(observation.Issues) == 0 {
		t.Fatalf("budget exhaustion was not explicit: records=%d issues=%v", len(observation.Records), observation.Issues)
	}

	if err := session.Continue(ctx, firstThread(t, ctx, session)); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if _, err := session.WaitEvent(ctx, "exited"); err != nil {
		t.Fatalf("wait exited: %v; state %s", err, session.State())
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if state := session.State(); state != rt.StateTerminated {
		t.Fatalf("final state = %s, want TERMINATED", state)
	}
}

func firstThread(t *testing.T, ctx context.Context, session *rt.Session) int {
	t.Helper()
	threads, err := session.Threads(ctx)
	if err != nil || len(threads) == 0 {
		t.Fatalf("threads: %v (%d)", err, len(threads))
	}
	return threads[0].ID
}
