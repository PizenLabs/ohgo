package gry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

type recordedRun struct {
	calls [][]string
	reply func(args []string) ([]byte, []byte, error)
}

func (r *recordedRun) run(_ context.Context, _ Config, args []string) ([]byte, []byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.reply == nil {
		return nil, nil, errors.New("unexpected command")
	}
	return r.reply(args)
}

func testProvider(t *testing.T, run *recordedRun) *Provider {
	t.Helper()
	config := Config{
		Root: t.TempDir(), Project: "proj",
		Timeout: 2 * time.Second, MaxRows: 40, MaxNodes: 40, MaxDepth: 2, MaxTokens: 3200,
	}
	return newWithRunner(config, run.run)
}

func testMeter() *investigate.Meter {
	return investigate.NewMeter(investigate.Budget{})
}

func observe(t *testing.T, provider *Provider, req investigate.Request) investigate.Observation {
	t.Helper()
	obs, err := provider.Observe(context.Background(), req, planner.Step{Strategy: "test"}, testMeter())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	return obs
}

func TestDisabledProviderRunsNothingAndClaimsNothing(t *testing.T) {
	run := &recordedRun{reply: func([]string) ([]byte, []byte, error) {
		t.Fatal("disabled provider started a process")
		return nil, nil, nil
	}}
	provider := newWithRunner(Config{Root: t.TempDir(), Disabled: true}, run.run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentFlow, Focus: "x"})
	if obs.Outcome != investigate.OutcomeUnavailable {
		t.Fatalf("outcome = %s, want UNAVAILABLE", obs.Outcome)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "not configured") {
		t.Fatalf("issues = %v", obs.Issues)
	}
}

func TestNotIndexedIsExplicitAndDoesNotMeanAbsence(t *testing.T) {
	root := t.TempDir()
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"projects":[{"name":"other","root_path":"/somewhere/else"}]}`), nil, nil
	}}
	provider := newWithRunner(Config{Root: root, Timeout: time.Second}, run.run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeNotIndexed {
		t.Fatalf("outcome = %s, want NOT_INDEXED", obs.Outcome)
	}
	joined := strings.Join(obs.Issues, "\n")
	if !strings.Contains(joined, "not evidence that the target is absent") {
		t.Fatalf("missing uncertainty: %s", joined)
	}
}

func TestProjectIdentityIsResolvedByRootNotByName(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "projects":
			payload, _ := json.Marshal(map[string]any{"projects": []map[string]any{
				{"name": "unrelated", "root_path": "/other"},
				{"name": "matched", "root_path": resolvedRoot},
			}})
			return payload, nil, nil
		case "architecture":
			return []byte(`{"languages":[{"language":"go","files":10,"lines":100}],"packages":[],"entry_points":[]}`), nil, nil
		}
		return nil, nil, errors.New("unexpected")
	}}
	provider := newWithRunner(Config{Root: root, Timeout: time.Second}, run.run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeSuccess && obs.Outcome != investigate.OutcomePartial {
		t.Fatalf("outcome = %s, issues=%v", obs.Outcome, obs.Issues)
	}
	if len(run.calls) < 2 || run.calls[1][0] != "architecture" {
		t.Fatalf("calls = %v", run.calls)
	}
	if run.calls[1][1] != "matched" {
		t.Fatalf("resolved project = %q, want matched", run.calls[1][1])
	}
}

func TestAmbiguousProjectIdentityIsNotGuessed(t *testing.T) {
	root := t.TempDir()
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		payload, _ := json.Marshal(map[string]any{"projects": []map[string]any{
			{"name": "one", "root_path": root},
			{"name": "two", "root_path": root},
		}})
		return payload, nil, nil
	}}
	provider := newWithRunner(Config{Root: root, Timeout: time.Second}, run.run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeAmbiguous {
		t.Fatalf("outcome = %s, want AMBIGUOUS", obs.Outcome)
	}
	if len(run.calls) != 1 {
		t.Fatalf("a query ran after ambiguous project resolution: %v", run.calls)
	}
}

func TestResolvePreservesAllCandidates(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"total":2,"returned":2,"results":[
			{"qn":"pkg.a.Serve","name":"Serve","label":"Function","file":"a.go","lines":[3,9],"in":2,"out":4},
			{"qn":"pkg.b.Serve","name":"Serve","label":"Function","file":"b.go","lines":[5,6],"in":0,"out":1}
		]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentLocate, Focus: "Serve"})
	if obs.Outcome != investigate.OutcomePartial {
		t.Fatalf("outcome = %s, want PARTIAL for multiple candidates", obs.Outcome)
	}
	if len(obs.Facts) != 2 {
		t.Fatalf("facts = %+v, want both candidates", obs.Facts)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "no single target was selected") {
		t.Fatalf("ambiguity was not preserved: %v", obs.Issues)
	}
	for _, record := range obs.Records {
		if record.State == evidence.Verified {
			t.Fatalf("structural evidence was upgraded to VERIFIED: %+v", record)
		}
		if record.Source != evidence.SourceGRY || record.Provider != evidence.ProviderStructural {
			t.Fatalf("provenance is wrong: %+v", record)
		}
	}
}

func TestUnresolvedResolveIsNotAbsence(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"total":0,"returned":0,"results":[]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentLocate, Focus: "missing"})
	if obs.Outcome != investigate.OutcomeUnresolved {
		t.Fatalf("outcome = %s, want UNRESOLVED", obs.Outcome)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "not proof that the symbol does not exist") {
		t.Fatalf("issues = %v", obs.Issues)
	}
}

// A flow question is forward-only. Querying both directions lets inbound
// callers crowd out the outbound path the user asked for, which forces the
// model to repeat the exploration with grep/read.
func TestFlowTraceIsOutboundOnly(t *testing.T) {
	run := &recordedRun{reply: func([]string) ([]byte, []byte, error) {
		return []byte(`{"found":true,"start":{"qn":"pkg.A"},"total":1,"returned":1,"rows":[
			{"hop":1,"direction":"outbound","edge":"CALLS","qn":"pkg.B","file":"b.go","lines":[1,2],"observed":true}
		]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentFlow, Focus: "pkg.A"})
	if obs.Outcome != investigate.OutcomeSuccess {
		t.Fatalf("outcome = %s, want SUCCESS", obs.Outcome)
	}
	if len(run.calls) != 1 {
		t.Fatalf("commands = %v, want one trace", run.calls)
	}
	direction := ""
	for i, arg := range run.calls[0] {
		if arg == "--direction" && i+1 < len(run.calls[0]) {
			direction = run.calls[0][i+1]
		}
	}
	if direction != "outbound" {
		t.Fatalf("trace direction = %q, want outbound", direction)
	}
}

// Reverse wording asks for the path into the focus. The final wrapper keeps
// the direction test identical while the question supplies the intent.
func TestReverseFlowQuestionTracesInbound(t *testing.T) {
	run := &recordedRun{reply: func([]string) ([]byte, []byte, error) {
		return []byte(`{"found":true,"start":{"qn":"pkg.B"},"total":1,"returned":1,"rows":[
			{"hop":1,"direction":"inbound","edge":"CALLS","qn":"pkg.A","file":"a.go","lines":[1,2],"observed":true}
		]}`), nil, nil
	}}
	provider := testProvider(t, run)
	_ = observe(t, provider, investigate.Request{
		Intent: planner.IntentFlow, Focus: "pkg.B",
		Question: "Who calls pkg.B?",
	})
	direction := ""
	for i, arg := range run.calls[0] {
		if arg == "--direction" && i+1 < len(run.calls[0]) {
			direction = run.calls[0][i+1]
		}
	}
	if direction != "inbound" {
		t.Fatalf("trace direction = %q, want inbound", direction)
	}
}

func TestTraceAmbiguityPreservesCandidatesAndTheDecisionBoundary(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"found":false,"ambiguous":true,"message":"ambiguous symbol: Run matches 3 indexed symbols","candidates":[
			{"qn":"pkg.a.Run","label":"Function","file":"a.go","lines":[1,2]},
			{"qn":"pkg.b.Run","label":"Function","file":"b.go","lines":[3,4]},
			{"qn":"pkg.c.Run","label":"Function","file":"c.go","lines":[5,6]}
		]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentFlow, Focus: "Run"})
	if obs.Outcome != investigate.OutcomeAmbiguous {
		t.Fatalf("outcome = %s, want AMBIGUOUS", obs.Outcome)
	}
	if len(obs.Facts) != 3 {
		t.Fatalf("facts = %+v, want three candidates", obs.Facts)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "no candidate was selected") {
		t.Fatalf("issues = %v", obs.Issues)
	}
	if obs.State == evidence.Verified {
		t.Fatal("ambiguity was upgraded to proof")
	}
}

func TestStaleContextIsMarkedAndBounded(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{
			"status":"stale","generation":"gen-9","truncated":true,
			"evidence":{"query_ok":true,"complete_for_scope":false,"freshness":"stale","reasons":["traversal_bounded"],"limitation":"not repository-wide"},
			"freshness":{"freshness":"stale","stale":true,"stale_paths":["a.go"]},
			"unknowns":[{"kind":"nodes_truncated","omitted":125}],
			"nodes":[{"qn":"pkg.A","name":"A","label":"Function","file":"a.go","lines":[1,2],"role":"target","distance":0,"provenance":"inferred"}],
			"relationships":[{"source":"pkg.B","target":"pkg.A","type":"CALLS","direction":"inbound","provenance":"verified","observed":false}],
			"tests":[{"qn":"pkg.TestA","name":"TestA","file":"a_test.go","lines":[3,9],"provenance":"declared"}]
		}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentDebug, Question: "why does A fail", Focus: "pkg.A"})
	if obs.Outcome != investigate.OutcomeStale {
		t.Fatalf("outcome = %s, want STALE", obs.Outcome)
	}
	for _, record := range obs.Records {
		if record.Freshness != evidence.FreshnessStale {
			t.Fatalf("record freshness = %s, want STALE: %+v", record.Freshness, record)
		}
		if record.State == evidence.Verified {
			t.Fatalf("gry verified provenance became Ohgo proof: %+v", record)
		}
	}
	joined := strings.Join(obs.Issues, "\n")
	for _, want := range []string{"125 omitted", "not repository-wide", "repository-wide completeness"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issue %q missing: %s", want, joined)
		}
	}
	if len(run.calls) != 1 || !containsArg(run.calls[0], "--target") || !containsArg(run.calls[0], "pkg.A") {
		t.Fatalf("context did not receive the target: %v", run.calls)
	}
}

func TestLargeSearchResultIsBounded(t *testing.T) {
	results := make([]map[string]any, 0, 200)
	for i := 0; i < 200; i++ {
		results = append(results, map[string]any{
			"qn": fmt.Sprintf("pkg.Symbol%03d", i), "name": fmt.Sprintf("Symbol%03d", i),
			"label": "Function", "file": fmt.Sprintf("file%03d.go", i), "lines": []int{i + 1, i + 2},
		})
	}
	payload, _ := json.Marshal(map[string]any{"total": 200, "returned": 200, "results": results})
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) { return payload, nil, nil }}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentLocate, Focus: "Symbol"})
	if len(obs.Facts) > maxObservationFacts {
		t.Fatalf("facts = %d, want <= %d", len(obs.Facts), maxObservationFacts)
	}
	if len(obs.Records) > maxObservationRecords {
		t.Fatalf("records = %d, want <= %d", len(obs.Records), maxObservationRecords)
	}
	if obs.Outcome != investigate.OutcomePartial {
		t.Fatalf("outcome = %s, want PARTIAL", obs.Outcome)
	}
	joined := strings.Join(obs.Issues, "\n")
	if !strings.Contains(joined, "omitted") || !strings.Contains(joined, "bounded") {
		t.Fatalf("omission is not explicit: %s", joined)
	}
}

func TestCommandTimeoutIsAnExplicitUnavailableOutcome(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return nil, []byte("still working"), errTimeout
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeUnavailable {
		t.Fatalf("outcome = %s, want UNAVAILABLE", obs.Outcome)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "timeout") {
		t.Fatalf("issues = %v", obs.Issues)
	}
}

func TestInvalidJSONIsFailureNotAnEmptySuccess(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"nope":`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeFailed {
		t.Fatalf("outcome = %s, want FAILED", obs.Outcome)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "invalid JSON") {
		t.Fatalf("issues = %v", obs.Issues)
	}
}

func TestOnlyReadOnlyGrySubcommandsAreReachable(t *testing.T) {
	allowed := map[string]bool{
		"projects": true, "search": true, "trace": true, "context": true,
		"architecture": true, "changes": true,
	}
	requests := []investigate.Request{
		{Intent: planner.IntentArchitecture},
		{Intent: planner.IntentLocate, Focus: "x"},
		{Intent: planner.IntentFlow, Focus: "x"},
		{Intent: planner.IntentImpact, Focus: "x"},
		{Intent: planner.IntentImpact},
		{Intent: planner.IntentDebug, Focus: "x"},
		{Intent: planner.IntentVerify},
		{Intent: planner.IntentProject},
	}
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"found":false,"total":0,"results":[]}`), nil, nil
	}}
	provider := testProvider(t, run)
	for _, req := range requests {
		_ = observe(t, provider, req)
	}
	for _, call := range run.calls {
		if len(call) == 0 || !allowed[call[0]] {
			t.Fatalf("reachable subcommand is not read-only: %v", call)
		}
		for _, arg := range call {
			if arg == "--include-source" || arg == "index" || arg == "delete" || arg == "traces" || arg == "adr" {
				t.Fatalf("mutating/source-dumping argument reached gry: %v", call)
			}
		}
	}
}

func TestAdapterDoesNotChargeTheBudgetOwnedByTheRouter(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"languages":[{"language":"go","files":1,"lines":1}],"packages":[],"entry_points":[]}`), nil, nil
	}}
	provider := testProvider(t, run)
	meter := investigate.NewMeter(investigate.Budget{MaxExternalRequests: 1})
	for i := 0; i < 2; i++ {
		if _, err := provider.Observe(context.Background(), investigate.Request{Intent: planner.IntentArchitecture},
			planner.Step{}, meter); err != nil {
			t.Fatal(err)
		}
	}
	if meter.External() != 0 {
		t.Fatalf("adapter charged the router's budget: %d", meter.External())
	}
}

func TestEmptyChangeDiffIsUnresolved(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"git_available":true,"changed_files":{"total":0,"rows":[]},"impact":{"total":0,"rows":[]},"tests":[]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentVerify})
	if obs.Outcome != investigate.OutcomeUnresolved {
		t.Fatalf("outcome = %s, want UNRESOLVED", obs.Outcome)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "not proof") {
		t.Fatalf("issues = %v", obs.Issues)
	}
}

func TestArchitectureOutputIsBoundedWithOmittedCount(t *testing.T) {
	languages := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		languages = append(languages, map[string]any{"language": fmt.Sprintf("l%d", i), "files": i + 1, "lines": 10})
	}
	packages := make([]map[string]any, 0, 50)
	for i := 0; i < 50; i++ {
		packages = append(packages, map[string]any{
			"name": fmt.Sprintf("pkg%d", i), "qn": fmt.Sprintf("__package__pkg%d", i),
			"ecosystem": "go", "external": false, "dependents": 50 - i, "dependencies": i,
		})
	}
	payload, _ := json.Marshal(map[string]any{
		"languages": languages, "packages": packages, "entry_points": []any{},
		"nodes": 100, "edges": 200, "caveat": "static analysis only",
	})
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) { return payload, nil, nil }}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if len(obs.Facts) > maxObservationFacts {
		t.Fatalf("facts = %d, want <= %d", len(obs.Facts), maxObservationFacts)
	}
	if obs.Outcome != investigate.OutcomePartial {
		t.Fatalf("outcome = %s, want PARTIAL", obs.Outcome)
	}
	joined := strings.Join(obs.Issues, "\n")
	if !strings.Contains(joined, "omitted") || !strings.Contains(joined, "static analysis only") {
		t.Fatalf("issues = %s", joined)
	}
}

func TestRealProcessTimeoutAndEnvironmentFiltering(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "gry")
	body := `#!/bin/sh
echo "secret=$ANTHROPIC_API_KEY" >&2
sleep 1
echo '{"projects":[]}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	provider := New(Config{Root: dir, Binary: script, Timeout: 80 * time.Millisecond})
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeUnavailable {
		t.Fatalf("outcome = %s, want UNAVAILABLE for a timeout", obs.Outcome)
	}
	joined := strings.Join(obs.Issues, "\n")
	if !strings.Contains(joined, "timeout") {
		t.Fatalf("timeout was not explicit: %s", joined)
	}
	if strings.Contains(joined, "sk-should-not-leak") {
		t.Fatalf("secret leaked into the observation: %s", joined)
	}
}

func TestRealProcessOutputCap(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "gry")
	body := `#!/bin/sh
i=0
while [ $i -lt 2000 ]; do
  printf 'x%.0s' 1 2 3 4 5 6 7 8 9 10
  i=$((i+1))
done
echo '{}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := New(Config{Root: dir, Binary: script, Project: "p", Timeout: 2 * time.Second, MaxOutputBytes: 128})
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentArchitecture})
	if obs.Outcome != investigate.OutcomeFailed {
		t.Fatalf("outcome = %s, want FAILED", obs.Outcome)
	}
	if !strings.Contains(strings.Join(obs.Issues, "\n"), "output_too_large") {
		t.Fatalf("issues = %v", obs.Issues)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// A symbol with several callees is not contradicting itself. Graph edges are
// functional per edge identity, not per source symbol, and the shared
// correlation model must not turn a real traversal into fabricated disagreement.
func TestMultipleEdgesFromOneSymbolAreNotContradictions(t *testing.T) {
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"found":true,"total":2,"returned":2,"start":{"qn":"pkg.A"},"rows":[
			{"hop":1,"direction":"outbound","edge":"CALLS","qn":"pkg.B","file":"b.go","lines":[1,2],"observed":true},
			{"hop":1,"direction":"outbound","edge":"CALLS","qn":"pkg.C","file":"c.go","lines":[3,4],"observed":true}
		]}`), nil, nil
	}}
	provider := testProvider(t, run)
	obs := observe(t, provider, investigate.Request{Intent: planner.IntentFlow, Focus: "pkg.A"})
	engine := evidence.New()
	for _, record := range obs.Records {
		if _, err := engine.Add(record); err != nil {
			t.Fatal(err)
		}
	}
	correlation := engine.Correlate()
	if len(correlation.Contradictions) != 0 {
		t.Fatalf("separate edges were reported as disagreement: %+v", correlation.Contradictions)
	}
	if len(correlation.Claims) != 2 {
		t.Fatalf("claims = %+v, want one claim per edge", correlation.Claims)
	}
}
