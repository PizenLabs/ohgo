package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A runtime conclusion carries machine-readable vocabulary. It must render as
// a status record, not markdown prose: underscore emphasis would publish
// `verificationnotestablished` and destroy the reason a host or user reads.
func TestRuntimeConclusionPreservesTheReasonCode(t *testing.T) {
	state := New()
	state.Apply(TerminalResized{Width: 100, Height: 30})
	state.Apply(UserSubmitted{Text: "fix it", Seq: 1})
	state.Apply(ConclusionPublished{
		Seq: 1,
		Conclusion: agent.RunConclusion{
			Status:  agent.ConclusionUnresolved,
			Reason:  agent.ConclusionVerificationNotEstablished,
			Summary: "the requested verification was not established",
		},
	})
	frame := ansi.Strip(Render(state))
	if !strings.Contains(frame, "runtime conclusion") {
		t.Fatalf("the runtime conclusion label is missing:\n%s", frame)
	}
	if !strings.Contains(frame, "UNRESOLVED · verification_not_established") {
		t.Fatalf("the reason code was mangled or missing:\n%s", frame)
	}
	if strings.Contains(frame, "verificationnotestablished") {
		t.Fatalf("markdown emphasis consumed the reason code:\n%s", frame)
	}
}

// A continuation after an interrupted change must resume at verification. A
// model that keeps reading instead is stopped by the verification window, not
// by the generic four-turn counter or the model-turn resource cap, and the
// task records the real reason. This is the production regression: an edit was
// applied, the user continued, and the run spent its whole turn grant
// rediscovering the repository.
func TestResumedMutationStopsAtTheVerificationWindow(t *testing.T) {
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
			readinessToolCall("read-7", "read", map[string]any{"path": "go.mod"}),
			readinessToolCall("read-8", "read", map[string]any{"path": "tools/grep.go"}),
			readinessToolCall("read-9", "read", map[string]any{"path": "tools/grep_test.go"}),
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
	if !strings.Contains(reason, "verification") {
		t.Fatalf("review reason = %q, want the verification window", current.ReviewReason)
	}
	if strings.Contains(reason, "turn limit") {
		t.Fatalf("review reason = %q, want the semantic bound rather than the resource cap", current.ReviewReason)
	}
	// The lifecycle status alone is not an outcome. The review carries the
	// terminal semantic conclusion, and the transcript shows that conclusion
	// as the final explanation because the model produced no answer.
	if current.Conclusion == nil {
		t.Fatal("the stalled task recorded no conclusion")
	}
	if current.Conclusion.Status != agent.ConclusionUnresolved {
		t.Fatalf("conclusion status = %q, want UNRESOLVED", current.Conclusion.Status)
	}
	if current.Conclusion.Reason != agent.ConclusionVerificationNotEstablished {
		t.Fatalf("conclusion reason = %q, want verification_not_established", current.Conclusion.Reason)
	}
	if !strings.Contains(strings.ToLower(current.Conclusion.Summary), "verification") {
		t.Fatalf("conclusion summary = %q, want the missing verification", current.Conclusion.Summary)
	}
	var conclusionEntry, footerEntry = -1, -1
	for i, entry := range resuming.program.state.Entries {
		if entry.Kind == EntryAssistant && entry.Runtime {
			conclusionEntry = i
		}
		if entry.Kind == EntryFooter {
			footerEntry = i
		}
	}
	if conclusionEntry < 0 {
		t.Fatal("the run ended with no final answer after the verification block")
	}
	if footerEntry < 0 || conclusionEntry > footerEntry {
		t.Fatalf("conclusion entry %d is not before the terminal footer %d", conclusionEntry, footerEntry)
	}
	final := resuming.program.state.Entries[conclusionEntry].AssistantText
	if !strings.Contains(final, "UNRESOLVED") ||
		!strings.Contains(final, "verification_not_established") {
		t.Fatalf("final answer = %q, want the unresolved verification conclusion", final)
	}
	for _, message := range resuming.program.history {
		assistant, ok := message.(agent.AssistantMessage)
		if ok && strings.Contains(assistant.Message.Text(), "verification_not_established") {
			t.Fatalf("the runtime conclusion entered the model conversation: %q", assistant.Message.Text())
		}
	}
	frame := ansi.Strip(Render(resuming.program.state))
	if !strings.Contains(frame, "runtime conclusion") {
		t.Fatalf("the conclusion block is missing its provenance label:\n%s", frame)
	}
	conclusionAt := strings.Index(frame, "UNRESOLVED · verification_not_established")
	footerAt := strings.Index(frame, "⚠ needs review:")
	if conclusionAt < 0 {
		t.Fatalf("the final explanation is not rendered:\n%s", frame)
	}
	if footerAt < 0 || conclusionAt > footerAt {
		t.Fatalf("the final explanation is not before the review footer:\n%s", frame)
	}
	results := resuming.toolResults()
	if len(results) != cognition.MaxVerifyObservationTurns {
		t.Fatalf("executed %d observations, want the verification window %d",
			len(results), cognition.MaxVerifyObservationTurns)
	}
	if len(results) >= 16 {
		t.Fatalf("the run reached the fixture's turn cap (%d observations)", len(results))
	}
}
