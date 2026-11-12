package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// runtimeVerifyTool is the structured primitive that starts a project runtime,
// observes readiness, checks it, and cleans it up. It reports the live-runtime
// observation the runtime objective requires, without owning a real port.
type runtimeVerifyTool struct{}

func (t *runtimeVerifyTool) Name() string                   { return "verify" }
func (t *runtimeVerifyTool) Description() string            { return "observe the project runtime" }
func (t *runtimeVerifyTool) Parameters() *jsonschema.Schema { return nil }
func (t *runtimeVerifyTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierExec} }
func (t *runtimeVerifyTool) Exclusive(map[string]any) bool  { return true }
func (t *runtimeVerifyTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{
		Text: "verification: PASS\nservice: npm run dev\n- http: PASS status 200",
		Details: map[string]any{
			"verification": map[string]any{"status": "PASS"},
		},
		Executes:     true,
		Finished:     true,
		Verifies:     true,
		Verification: &agent.VerificationEvidence{Command: "npm run dev"},
		Runtime: &agent.RuntimeEvidence{
			Command: "npm run dev",
			URL:     "http://127.0.0.1:3000",
			PID:     4242,
			Checks:  1,
		},
	}
}

// "Run this project help me" followed by "run it" is the reported failure: the
// first turn ended completed · proven after an environment probe, and the
// second was refused as already proven. The regression is the whole sequence:
// the probe leaves the task open with the live-runtime gap named, and the
// repeated imperative executes the runtime instead of being treated as a
// redundant verification of the old turn.
func TestRunThisProjectProvesOnlyAfterLiveRuntimeObservation(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewBashTool(root), &runtimeVerifyTool{}},
		[]ai.AssistantMessage{
			readinessToolCall("probe-1", "bash", map[string]any{"command": "echo environment ok"}),
			readinessAnswer("Run npm run dev to start the project."),
			readinessToolCall("verify-1", "verify", map[string]any{}),
			readinessAnswer("The project is serving at http://127.0.0.1:3000."),
		})

	r.turn("run this project help me")
	afterProbe := r.currentTask()
	if afterProbe.Proven() || afterProbe.ObjectiveState() == task.ObjectiveProven {
		t.Fatalf("a finished command proved the runtime objective: %s (%s)",
			afterProbe.ObjectiveState(), afterProbe.CompletionSummary())
	}
	if afterProbe.Completion != nil {
		t.Fatalf("the probe recorded a completion proof: %+v", afterProbe.Completion)
	}
	if afterProbe.Status != task.StatusNeedsReview {
		t.Fatalf("status after the probe = %s, want NEEDS_REVIEW", afterProbe.Status)
	}
	if !strings.Contains(afterProbe.ReviewReason, "live runtime") {
		t.Fatalf("review reason = %q, want it to name the missing live runtime", afterProbe.ReviewReason)
	}
	if !hasEvidenceResult(r.toolResults(), "echo environment ok") {
		t.Fatal("the probe command did not run; the fixture did not reproduce the failure")
	}
	// The action surface must stay open: the failure mode being fixed cut it
	// off at StageComplete and told the model to answer from evidence.
	if report, ok := r.controller.LastProjection(); ok {
		if !slices.Contains(report.ToolNames, "verify") {
			t.Fatalf("offered tools after the probe = %v, want the runtime capability", report.ToolNames)
		}
		if strings.Contains(report.StopReason, "blocked") {
			t.Fatalf("the runtime gap was reported as terminal: %q", report.StopReason)
		}
	}

	r.turn("run it")
	proven := r.currentTask()
	if proven.ID != afterProbe.ID {
		t.Fatalf("the repeated execution replaced the open task: %q -> %q", afterProbe.ID, proven.ID)
	}
	if proven.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("after the runtime observation objective = %s, want PROVEN: %s (review=%q)",
			proven.ObjectiveState(), proven.CompletionSummary(), proven.ReviewReason)
	}
	if proven.Completion == nil || !proven.Completion.Executed {
		t.Fatalf("proof = %+v, want an executed runtime observation", proven.Completion)
	}
	runtimeRecords := 0
	for _, record := range proven.Context.Evidence {
		if record.Kind == task.EvidenceRuntime && record.Validity == task.EvidenceCurrent {
			runtimeRecords++
		}
	}
	if runtimeRecords == 0 {
		t.Fatalf("durable evidence has no runtime observation: %+v", proven.Context.Evidence)
	}
	if r.index != nil && *r.index < 4 {
		t.Fatalf("only %d provider messages were consumed; the runtime turn never reached the model", *r.index)
	}
}

// hasEvidenceResult reports whether the transcript carries a tool result for a
// command. It proves the probe executed rather than being narrated.
func hasEvidenceResult(results []agent.ToolResultMessage, needle string) bool {
	for _, result := range results {
		if strings.Contains(result.Text(), needle) {
			return true
		}
	}
	return false
}
