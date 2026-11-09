package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A staged task started in BUILD must be able to move to FUZZ for a finding,
// back to BUILD for the change, and to FUZZ again if verification opens a new
// investigation. The move is driven by the model's own capability request at
// the tool boundary; no user turn, new task, or manual workspace choice is
// involved.
func TestToolBoundaryHandoffRoundTripKeepsOneTask(t *testing.T) {
	root := t.TempDir()
	writeHandoffModule(t, root)

	var artifacts []workspace.Artifact
	// The artifact tool is built with the live role pointer, exactly as the
	// production tool reads the live gate on every call.
	toolSet := func(role *workspace.Workspace) []agent.Tool {
		return []agent.Tool{
			tools.NewReadTool(root),
			tools.NewWriteTool(root),
			tools.NewBashTool(root),
			tools.NewArtifactTool(func(artifact workspace.Artifact) error {
				artifacts = append(artifacts, artifact)
				return nil
			}, func() workspace.Workspace { return *role }),
		}
	}

	finding := map[string]any{
		"kind": "finding", "target": "sales/checkout",
		"status":       "reproduced",
		"reproduction": "go test ./...",
		"evidence":     "checkout returned no order",
	}
	// The first implementation is intentionally wrong so its check fails.
	// That keeps the task in verification and lets the script exercise a second
	// finding -> change -> check round without pretending the first check
	// succeeded.
	writeCheckout := map[string]any{
		"path":    "checkout.go",
		"content": "package checkout\n\nfunc Checkout() string { return \"bad\" }\n",
	}
	messages := []ai.AssistantMessage{
		readinessToolCall("read-0", "read", map[string]any{"path": "go.mod"}),
		readinessToolCall("finding-1", "artifact", finding),
		readinessToolCall("write-1", "write", writeCheckout),
		readinessToolCall("change-1", "artifact", map[string]any{
			"kind": "change", "target": "sales/checkout", "status": "implemented",
		}),
		readinessToolCall("test-1", "bash", map[string]any{"command": "go test ./..."}),
		// Verification exposed a second issue. The same task returns to FUZZ
		// for the new finding and then to BUILD for the correction.
		readinessToolCall("finding-2", "artifact", map[string]any{
			"kind": "finding", "target": "sales/checkout",
			"status":       "reproduced",
			"reproduction": "go test ./...",
			"evidence":     "the second checkout path still returned no order",
		}),
		readinessToolCall("write-2", "write", map[string]any{
			"path":    "checkout.go",
			"content": "package checkout\n\nfunc Checkout() string { return \"ok\" }\n\nfunc Confirm() bool { return true }\n",
		}),
		readinessToolCall("change-2", "artifact", map[string]any{
			"kind": "change", "target": "sales/checkout", "status": "implemented",
		}),
		readinessToolCall("test-2", "bash", map[string]any{"command": "go test ./..."}),
		readinessAnswer("Implemented the checkout flow and verified it with go test ./..."),
	}

	r := newReadinessRuntimeWithTools(t, root, workspace.Build, toolSet, messages)
	r.enableToolHandoff()

	r.turnAs("Fix the checkout flow and run the tests.", workspace.Build)
	final := r.currentTask()

	if *r.role != workspace.Build {
		t.Fatalf("final workspace = %s, want BUILD after the change phase", *r.role)
	}
	if final.ID == "" {
		t.Fatal("the round trip created no durable task")
	}
	if final.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("objective = %s, want PROVEN (review=%q)", final.ObjectiveState(), final.ReviewReason)
	}
	if len(artifacts) != 4 {
		t.Fatalf("recorded %d artifacts, want two finding/change pairs: %+v", len(artifacts), artifacts)
	}
	for i, want := range []struct {
		kind   workspace.ArtifactKind
		origin workspace.Workspace
	}{
		{workspace.KindFinding, workspace.Fuzz},
		{workspace.KindChange, workspace.Build},
		{workspace.KindFinding, workspace.Fuzz},
		{workspace.KindChange, workspace.Build},
	} {
		if artifacts[i].Kind != want.kind || artifacts[i].Origin != want.origin {
			t.Fatalf("artifact %d = %s/%s, want %s/%s",
				i, artifacts[i].Kind, artifacts[i].Origin, want.kind, want.origin)
		}
	}
	written, err := os.ReadFile(filepath.Join(root, "checkout.go"))
	if err != nil || !strings.Contains(string(written), "func Checkout") {
		t.Fatalf("the BUILD write did not land: %q err=%v", written, err)
	}

	// The durable timeline must describe the phase movement as transitions of
	// this one task, not as detached work.
	buildToFuzz, fuzzToBuild := 0, 0
	for _, event := range final.Timeline {
		if event.Kind != task.TimelineHandoff {
			continue
		}
		if strings.Contains(event.Summary, "BUILD -> FUZZ") {
			buildToFuzz++
		}
		if strings.Contains(event.Summary, "FUZZ -> BUILD") {
			fuzzToBuild++
		}
	}
	if buildToFuzz < 2 || fuzzToBuild < 2 {
		t.Fatalf("timeline = %d BUILD->FUZZ, %d FUZZ->BUILD; want two round trips on the same task",
			buildToFuzz, fuzzToBuild)
	}
}

// writeHandoffModule creates a passing Go module so the verification command is
// real: the write must define the function the test calls.
func writeHandoffModule(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/checkout\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	test := "package checkout\n\nimport \"testing\"\n\nfunc TestCheckout(t *testing.T) {\n\tif Checkout() != \"ok\" {\n\t\tt.Fatal(\"checkout is not wired\")\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(root, "checkout_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
}
