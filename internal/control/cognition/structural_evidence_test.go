package cognition

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// structuralDetails is a small stand-in for a structural tool result. The
// evidence layer must read provenance from structured fields, not prose.
func structuralDetails(tool string) map[string]any {
	details := map[string]any{
		"status":      "ok",
		"snapshot_id": "snap-123",
		"generation":  uint64(4),
		"graph_state": "CURRENT",
		"partial":     false,
		"truncated":   false,
		"complete":    true,
		"coordinates": []map[string]any{
			{"path": "pkg/foo.go", "line": 3, "end_line": 5, "symbol": "pkg.Foo", "kind": "target"},
			{"path": "pkg/foo_test.go", "line": 8, "column": 2, "symbol": "TestFoo", "kind": "test"},
		},
	}
	if tool == "structure" {
		details["view"] = "files"
	}
	return details
}

func TestGraphRefsAndStructureBecomeStructuralEvidence(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"graph", map[string]any{"package": "pkg"}},
		{"refs", map[string]any{"symbol": "Foo"}},
		{"structure", map[string]any{"view": "files"}},
	} {
		state := NewEvidenceState()
		changed := state.ObserveOutcome(agent.ToolOutcome{
			CallID: tc.tool + "-1", ToolName: tc.tool, Args: tc.args,
			Result: agent.Result{Text: "status: ok", Details: structuralDetails(tc.tool)},
		})
		if !changed {
			t.Errorf("%s: first structural observation was not new", tc.tool)
		}
		query := ""
		for _, key := range []string{"symbol", "package", "path", "view"} {
			if value, ok := tc.args[key].(string); ok {
				query = value
				break
			}
		}
		item, ok := state.Find(tc.tool + ":" + query)
		if !ok {
			t.Fatalf("%s: aggregate structural evidence missing", tc.tool)
		}
		if item.Provenance != ProvenanceObservation || item.Retention != RetentionStructural {
			t.Errorf("%s: provenance/retention = %s/%s, want observation/structural",
				tc.tool, item.Provenance, item.Retention)
		}
		if item.Source.SnapshotID != "snap-123" || item.Source.GraphState != "CURRENT" || item.Source.Generation != 4 {
			t.Errorf("%s: source lacks snapshot provenance: %+v", tc.tool, item.Source)
		}
		if !item.Complete {
			t.Errorf("%s: exact structural observation was marked incomplete", tc.tool)
		}
		if _, ok := state.Find("symbol:pkg/foo.go:3:pkg.Foo"); !ok {
			t.Errorf("%s: target coordinate was not retained as evidence", tc.tool)
		}
		if _, ok := state.Find("test:pkg/foo_test.go:8:TestFoo"); !ok {
			t.Errorf("%s: test coordinate was not retained as evidence", tc.tool)
		}
	}
}

func TestTruncatedOrPartialStructuralEvidenceIsNotComplete(t *testing.T) {
	state := NewEvidenceState()
	details := structuralDetails("graph")
	details["truncated"] = true
	details["complete"] = false
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "graph-1", ToolName: "graph", Args: map[string]any{"package": "pkg"},
		Result: agent.Result{Text: "status: truncated", Details: details},
	})
	item, ok := state.Find("graph:pkg")
	if !ok {
		t.Fatal("aggregate graph evidence missing")
	}
	if item.Complete {
		t.Error("a truncated graph result was recorded as complete")
	}
	for _, coordinate := range state.Items {
		if coordinate.ID == "symbol:pkg/foo.go:3:pkg.Foo" && coordinate.Complete {
			t.Error("a coordinate from a truncated result was marked complete")
		}
	}

	details = structuralDetails("structure")
	details["partial"] = true
	details["complete"] = false
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "structure-1", ToolName: "structure", Args: map[string]any{"view": "files"},
		Result: agent.Result{Text: "status: ok", Details: details},
	})
	item, ok = state.Find("structure:files")
	if !ok {
		t.Fatal("aggregate structure evidence missing")
	}
	if item.Complete {
		t.Error("a partial structure result was recorded as complete")
	}
}
