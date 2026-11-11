package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A continuation after an interrupted change must resume at verification. A
// model that keeps reading instead is stopped by the semantic no-progress
// bound, not by the model-turn resource cap, and the task records the real
// reason. This is the production regression: an edit was applied, the user
// continued, and the run spent its whole turn grant rediscovering the
// repository.
func TestResumedMutationStopsAtTheSemanticNoProgressBound(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"

	first := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
			}),
			readinessAnswer("wrote the change"),
		})
	first.turn(objective)
	interrupted := first.currentTask()
	if interrupted.Status != task.StatusNeedsReview {
		t.Fatalf("interrupted task status = %s, want NEEDS_REVIEW", interrupted.Status)
	}
	if interrupted.Proven() {
		t.Fatal("an unverified change was reported as proven")
	}

	// A continuation must see the pending verification before the model runs.
	// The directive is the durable checkpoint, not model narration.
	resuming := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "go.mod"}),
			readinessToolCall("read-2", "read", map[string]any{"path": "tools/grep.go"}),
			readinessToolCall("read-3", "read", map[string]any{"path": "tools/grep_test.go"}),
			readinessToolCall("read-4", "read", map[string]any{"path": "go.mod"}),
			readinessToolCall("read-5", "read", map[string]any{"path": "tools/grep.go"}),
			readinessToolCall("read-6", "read", map[string]any{"path": "tools/grep_test.go"}),
			readinessAnswer("I could not verify the change"),
		})
	briefing := strings.Join(resuming.controller.Context(), "\n")
	for _, want := range []string{"Resume directive:", "Last observed change: changed tools/grep.go", "Verification: pending"} {
		if !strings.Contains(briefing, want) {
			t.Fatalf("resume briefing is missing %q:\n%s", want, briefing)
		}
	}

	resuming.turn("continue")
	current := resuming.currentTask()
	if current.ID != interrupted.ID {
		t.Fatalf("the continuation replaced the task: %q -> %q", interrupted.ID, current.ID)
	}
	if current.Status != task.StatusNeedsReview {
		t.Fatalf("stalled task status = %s, want NEEDS_REVIEW", current.Status)
	}
	reason := strings.ToLower(current.ReviewReason)
	if !strings.Contains(reason, "no meaningful task progress") {
		t.Fatalf("review reason = %q, want the semantic no-progress reason", current.ReviewReason)
	}
	if strings.Contains(reason, "turn limit") {
		t.Fatalf("review reason = %q, want the semantic bound rather than the resource cap", current.ReviewReason)
	}
	results := resuming.toolResults()
	if len(results) != cognition.MaxConsecutiveNoProgress {
		t.Fatalf("executed %d no-progress observations, want %d", len(results), cognition.MaxConsecutiveNoProgress)
	}
	if len(results) >= 16 {
		t.Fatalf("the run reached the fixture's turn cap (%d observations)", len(results))
	}
}
