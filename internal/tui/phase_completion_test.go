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

const ecommerceInvestigationPrompt = `Check this Go e-commerce project and understand how the main order/checkout flow works.

Start by investigating the project without changing anything. If you identify a concrete issue that needs reproduction or deeper investigation, investigate it and reproduce it without modifying source code.

If the issue is confirmed, switch to the appropriate workspace, implement the fix, run the relevant tests, and verify that the fix actually works.

Keep the same task context throughout the process. Do not create a new task for continuation or verification. Do not claim completion without evidence.`

// A FUZZ investigation of a task whose overall objective is a mutation must
// not be reported as a failed repository change. The turn can complete the
// investigation phase, but the task remains active for the BUILD phase.
func TestFuzzInvestigationPhaseOfMutationTaskStaysActive(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("The order/checkout flow is represented by tools/grep.go in this snapshot."),
		})

	r.turn(ecommerceInvestigationPrompt)
	current := r.currentTask()
	if current.Status == task.StatusNeedsReview {
		t.Fatalf("the investigation phase was flagged as a failed change: %s", current.ReviewReason)
	}
	if current.Status != task.StatusRunning {
		t.Fatalf("task status = %s, want RUNNING while the mutation is deferred", current.Status)
	}
	if current.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("objective state = %s, want ACTIVE while the task awaits BUILD", current.ObjectiveState())
	}
	if current.Completion != nil {
		t.Fatalf("the investigation phase recorded a completion proof: %+v", current.Completion)
	}
	if strings.Contains(current.ReviewReason, "no change was observed") {
		t.Fatalf("review reason = %q", current.ReviewReason)
	}
	if notice := lastNotice(r.program); strings.Contains(notice, "no change was observed") {
		t.Fatalf("the failed-change notice was published: %q", notice)
	}
}

// The same task must survive the deferred phase and prove after the human
// authorizes BUILD. The prompt keeps the mutation conditional but does not
// carry the investigation-only "without modifying" constraint, which is a
// separate constraint-scoping boundary.
func TestFuzzInvestigationPhaseContinuesIntoBuildSameTask(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	if err := os.MkdirAll(filepath.Join(root, "order"), 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := "Investigate the order/checkout flow in this project. If you find a concrete issue, implement the fix in order/checkout and run go test ./... . Keep the same task context."
	r := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("The order/checkout flow is represented by tools/grep.go in this snapshot."),
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "order/checkout",
				"content": "checkout flow implementation\n",
			}),
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./..."}),
			readinessAnswer("implemented and verified the order/checkout flow"),
		})

	r.turn(prompt)
	investigated := r.currentTask()
	if investigated.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("investigation phase = %s (%s), want ACTIVE", investigated.ObjectiveState(), investigated.ReviewReason)
	}
	if !r.program.switchWorkspace("build") {
		t.Fatal("switching to BUILD was refused")
	}
	r.turn(prompt)
	proven := r.currentTask()
	if proven.ID != investigated.ID {
		t.Fatalf("BUILD replaced the investigated task: %q -> %q", investigated.ID, proven.ID)
	}
	if proven.ObjectiveState() != task.ObjectiveProven {
		for _, result := range r.toolResults() {
			t.Logf("tool=%s error=%v text=%s", result.ToolName, result.IsError, result.Text())
		}
		t.Fatalf("BUILD did not prove the same task: %s (%s)",
			proven.ObjectiveState(), proven.ReviewReason)
	}
	if proven.Completion == nil || len(proven.Completion.Verified) == 0 {
		t.Fatalf("completion proof = %+v, want the post-change verification", proven.Completion)
	}
}

// An execution objective under ASK is deferred. The turn can still answer what
// it observed, but it cannot run the process; judging the phase against
// execution evidence would report a failed execution the authority never
// permitted.
func TestExecutionObjectiveUnderAskCompletesPhaseNotReview(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "go.mod"}),
			readinessAnswer("The module is example.com/readiness; running its tests needs BUILD authority."),
		})

	r.turnAs("Run the tests.", workspace.Ask)
	current := r.currentTask()
	if current.Status != task.StatusRunning {
		t.Fatalf("phase status = %s (%s), want RUNNING", current.Status, current.ReviewReason)
	}
	if current.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("objective state = %s, want ACTIVE", current.ObjectiveState())
	}
	if current.Completion != nil {
		t.Fatalf("the deferred execution phase recorded a proof: %+v", current.Completion)
	}
	if strings.Contains(current.ReviewReason, "execution") {
		t.Fatalf("the unperformed execution was reported as a failed phase: %q", current.ReviewReason)
	}
}

// A deferred plan is not a phase result. Suppressing the mutation demand must
// not turn the phase boundary into an unconditional pass.
func TestFuzzInvestigationPhaseRejectsADeferredPlan(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessAnswer("Let me check the implementation and report back."),
		})

	r.turnAs("Fix GrepTool in tools/grep.go and run go test ./tools", workspace.Fuzz)
	current := r.currentTask()
	if current.Status != task.StatusNeedsReview {
		t.Fatalf("a deferred plan left status %s, want NEEDS_REVIEW", current.Status)
	}
	// The loop first classifies the announcement as execution intent without a
	// tool; its bounded recovery produced only another answer, so the phase is
	// still not proof. The exact review text may be the evidence boundary
	// rather than the deferred-plan rule, but it must never be COMPLETED.
	if strings.TrimSpace(current.ReviewReason) == "" {
		t.Fatal("a deferred plan was reviewed without a reason")
	}
	if current.ObjectiveState() == task.ObjectiveProven {
		t.Fatal("a deferred plan was reported as proven")
	}
}
