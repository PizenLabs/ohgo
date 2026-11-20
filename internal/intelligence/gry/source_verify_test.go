package gry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

func writeSource(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A symbol that still exists at the indexed coordinate is current. This is the
// positive case the adapter needs before it may present a candidate as a
// current source fact.
func TestSourceVerificationCurrentWhenSymbolStillExists(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "order/service.go", "package order\n\nfunc NewOrder() {}\n")
	freshness, reasons := verifySourceLocations(root, []sourceLocation{
		{file: "order/service.go", lines: []int{3, 3}, symbol: "order.NewOrder"},
	})
	if freshness != evidence.FreshnessCurrent {
		t.Fatalf("freshness = %s (%v), want CURRENT", freshness, reasons)
	}
}

// A missing file is not "unknown": the source of truth moved or disappeared,
// so the indexed row is stale.
func TestSourceVerificationMissingFileIsStale(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "present.go", "package present\n")
	freshness, reasons := verifySourceLocations(root, []sourceLocation{
		{file: "order/service.go", lines: []int{3, 3}, symbol: "order.NewOrder"},
	})
	if freshness != evidence.FreshnessStale {
		t.Fatalf("freshness = %s (%v), want STALE", freshness, reasons)
	}
}

// A file that exists but no longer contains the symbol is stale even when GRY
// claims the row is observed.
func TestSourceVerificationMovedSymbolIsStale(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "order/service.go", "package order\n\nfunc NewOrder() {}\n")
	freshness, reasons := verifySourceLocations(root, []sourceLocation{
		{file: "order/service.go", lines: []int{10, 12}, symbol: "order.CancelOrder"},
	})
	if freshness != evidence.FreshnessStale {
		t.Fatalf("freshness = %s (%v), want STALE", freshness, reasons)
	}
}

// A path outside the workspace is never read. The check is unavailable, not a
// source fact and not a stale claim.
func TestSourceVerificationOutsideRootIsUnavailable(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "present.go", "package present\n")
	freshness, reasons := verifySourceLocations(root, []sourceLocation{
		{file: "../outside.go", lines: []int{1, 1}, symbol: "pkg.X"},
	})
	if freshness != evidence.FreshnessUnavailable {
		t.Fatalf("freshness = %s (%v), want UNAVAILABLE", freshness, reasons)
	}
}

// A symlink inside the root must not smuggle an outside file into evidence.
func TestSourceVerificationSymlinkOutsideRootIsUnavailable(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	freshness, reasons := verifySourceLocations(root, []sourceLocation{
		{file: "link.go", lines: []int{1, 1}, symbol: "outside.X"},
	})
	if freshness != evidence.FreshnessUnavailable {
		t.Fatalf("freshness = %s (%v), want UNAVAILABLE", freshness, reasons)
	}
}

// Without a workspace root the adapter cannot verify anything; the honest
// state is unknown, never current.
func TestSourceVerificationWithoutRootIsUnknown(t *testing.T) {
	freshness, _ := verifySourceLocations("", []sourceLocation{
		{file: "a.go", lines: []int{1, 1}, symbol: "pkg.X"},
	})
	if freshness != evidence.FreshnessUnknown {
		t.Fatalf("freshness = %s, want UNKNOWN", freshness)
	}
}

// Trace and search payloads do not carry a generation today. The adapter must
// not treat their success as current; it verifies the endpoints, while the
// relationship between them stays UNVERIFIED.
func TestTraceWithoutGenerationVerifiesEndpointsButNotEdges(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "order/service.go", "package order\nfunc NewOrder() {}\nfunc Place() { NewOrder() }\n")
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"found":true,"total":1,"returned":1,"start":{"qn":"order.Place"},"rows":[
			{"hop":1,"direction":"outbound","edge":"CALLS","qn":"order.NewOrder","file":"order/service.go","lines":[2,2],"via_qn":"order.Place","observed":true}
		]}`), nil, nil
	}}
	provider := newWithRunner(Config{Root: root, Project: "proj", Timeout: 1, MaxRows: 10, MaxDepth: 2}, run.run)
	obs, err := provider.Observe(context.Background(), investigate.Request{Intent: planner.IntentFlow, Focus: "Place"}, planner.Step{Strategy: "test"}, testMeter())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Outcome != investigate.OutcomeSuccess && obs.Outcome != investigate.OutcomePartial {
		t.Fatalf("outcome = %s, issues=%v", obs.Outcome, obs.Issues)
	}
	if len(obs.Records) == 0 || obs.Records[0].Freshness != evidence.FreshnessUnverified {
		t.Fatalf("records = %+v, want UNVERIFIED edge freshness", obs.Records)
	}
	if len(obs.Facts) == 0 || obs.Facts[0].Freshness != evidence.FreshnessUnverified {
		t.Fatalf("facts = %+v, want the edge fact marked UNVERIFIED", obs.Facts)
	}
	if !strings.Contains(strings.Join(obs.Issues, " "), "edges are UNVERIFIED") {
		t.Fatalf("issues = %v, want the edge-freshness boundary", obs.Issues)
	}
}

// A row whose source no longer contains the symbol is stale, and the router
// must be able to see that rather than receiving an unqualified success.
func TestTraceStaleRowDowngradesObservation(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "order/service.go", "package order\nfunc Place() {}\n")
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{"found":true,"total":1,"returned":1,"start":{"qn":"order.Place"},"rows":[
			{"hop":1,"direction":"outbound","edge":"CALLS","qn":"order.NewOrder","file":"order/service.go","lines":[2,2],"via_qn":"order.Place","observed":true}
		]}`), nil, nil
	}}
	provider := newWithRunner(Config{Root: root, Project: "proj", Timeout: 1, MaxRows: 10, MaxDepth: 2}, run.run)
	obs, err := provider.Observe(context.Background(), investigate.Request{Intent: planner.IntentFlow, Focus: "Place"}, planner.Step{Strategy: "test"}, testMeter())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Outcome != investigate.OutcomeStale {
		t.Fatalf("outcome = %s, want STALE; issues=%v", obs.Outcome, obs.Issues)
	}
	for _, record := range obs.Records {
		if record.Freshness != evidence.FreshnessStale {
			t.Fatalf("record freshness = %s, want STALE", record.Freshness)
		}
	}
}

// A current node does not make the relationships around it current. Context
// carries both, so the node record stays CURRENT while the edge record is
// explicitly UNVERIFIED.
func TestContextKeepsNodeAndEdgeFreshnessSeparate(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "order/service.go", "package order\nfunc NewOrder() {}\nfunc Place() { NewOrder() }\n")
	run := &recordedRun{reply: func(args []string) ([]byte, []byte, error) {
		return []byte(`{
			"status":"ok","generation":"gen-1",
			"evidence":{"complete_for_scope":true,"freshness":"current"},
			"nodes":[
				{"qn":"order.NewOrder","label":"Function","file":"order/service.go","lines":[2,2],"role":"target","distance":0,"provenance":"verified"},
				{"qn":"order.Place","label":"Function","file":"order/service.go","lines":[3,3],"role":"context","distance":1,"provenance":"verified"}
			],
			"relationships":[
				{"source":"order.Place","target":"order.NewOrder","type":"CALLS","direction":"outbound","provenance":"verified","observed":true}
			]
		}`), nil, nil
	}}
	provider := newWithRunner(Config{Root: root, Project: "proj", Timeout: 1, MaxRows: 10, MaxDepth: 2}, run.run)
	obs, err := provider.Observe(context.Background(), investigate.Request{Intent: planner.IntentDebug, Question: "why does Place call NewOrder", Focus: "order.NewOrder"}, planner.Step{Strategy: "test"}, testMeter())
	if err != nil {
		t.Fatal(err)
	}
	var sawNode, sawEdge bool
	for _, record := range obs.Records {
		switch record.Relation {
		case "appears_in_context":
			sawNode = true
			if record.Freshness != evidence.FreshnessCurrent {
				t.Fatalf("node freshness = %s, want CURRENT: %+v", record.Freshness, record)
			}
		case "CALLS":
			sawEdge = true
			if record.Freshness != evidence.FreshnessUnverified {
				t.Fatalf("edge freshness = %s, want UNVERIFIED: %+v", record.Freshness, record)
			}
		}
	}
	if !sawNode || !sawEdge {
		t.Fatalf("records = %+v, want both a node and an edge", obs.Records)
	}
	if !strings.Contains(strings.Join(obs.Issues, " "), "edges are UNVERIFIED") {
		t.Fatalf("issues = %v, want the edge boundary", obs.Issues)
	}
}
