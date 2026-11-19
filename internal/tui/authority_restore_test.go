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

// A new session restores the default BUILD workspace. An informational task
// interrupted under FUZZ must not resume under that default: the task's
// persisted contract is the authority, so the turn restores FUZZ before the
// model is asked and the write surface stays closed.
func TestResumedInformationalTaskRestoresItsOwnAuthority(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "investigate the bottleneck and evaluate in this project"

	first := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "go.mod"}),
			readinessAnswer(""),
		})
	first.turn(objective)
	opened := first.currentTask()
	if opened.ID == "" || opened.Status.IsTerminal() {
		t.Fatalf("setup left no resumable task: status=%s objective=%s", opened.Status, opened.ObjectiveState())
	}
	if opened.ActiveRole != workspace.Fuzz {
		t.Fatalf("setup role = %s, want FUZZ", opened.ActiveRole)
	}

	// The restart restores the session default, not the task's authority.
	resuming := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"mutated\" }\n",
			}),
			readinessAnswer("The bottleneck is the repeated scan in the hot path."),
		})
	if *resuming.role != workspace.Build {
		t.Fatalf("restart role = %s, want the session default BUILD", *resuming.role)
	}

	resuming.turn("continue")
	if *resuming.role != workspace.Fuzz {
		t.Fatalf("continuation role = %s, want the task's FUZZ authority", *resuming.role)
	}
	restored := resuming.currentTask()
	if restored.ID != opened.ID {
		t.Fatalf("the continuation replaced the task: %q -> %q", opened.ID, restored.ID)
	}
	if restored.ActiveRole != workspace.Fuzz {
		t.Fatalf("recorded role = %s, want FUZZ, not the session default", restored.ActiveRole)
	}
	// The informational contract forbids mutation. The live gate refused the
	// write, so the file still carries the fixture content.
	content, err := os.ReadFile(filepath.Join(root, "tools", "grep.go"))
	if err != nil {
		t.Fatalf("read tools/grep.go: %v", err)
	}
	if strings.Contains(string(content), "mutated") {
		t.Fatal("an informational FUZZ task mutated the repository after resume")
	}
}

// The inverse: a BUILD task interrupted under BUILD must not be downgraded by a
// session restored in FUZZ. The persisted grant is restored and the same task
// can still perform its verification.
func TestResumedMutationTaskKeepsBuildAuthority(t *testing.T) {
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
	if interrupted.Status.IsTerminal() || interrupted.Proven() {
		t.Fatalf("the unverified turn finished the task: status=%s objective=%s",
			interrupted.Status, interrupted.ObjectiveState())
	}
	if interrupted.ActiveRole != workspace.Build {
		t.Fatalf("setup role = %s, want BUILD", interrupted.ActiveRole)
	}

	resuming := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("the suite passes now"),
		})
	if *resuming.role != workspace.Fuzz {
		t.Fatalf("restart role = %s, want the session default FUZZ", *resuming.role)
	}

	resuming.turn("continue")
	if *resuming.role != workspace.Build {
		t.Fatalf("continuation role = %s, want the task's BUILD authority", *resuming.role)
	}
	restored := resuming.currentTask()
	if restored.ID != interrupted.ID {
		t.Fatalf("the continuation replaced the task: %q -> %q", interrupted.ID, restored.ID)
	}
	if restored.ActiveRole != workspace.Build {
		t.Fatalf("recorded role = %s, want BUILD", restored.ActiveRole)
	}
	if restored.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("resumed verification did not prove the change: %s (%s)",
			restored.ObjectiveState(), restored.ReviewReason)
	}
}

// An ASK task must stay read-only across a restart, even when the session comes
// back in BUILD. Restoring the task's role is a narrowing, never a widening.
func TestResumedAskTaskStaysReadOnly(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "what does the GrepTool entry point do?"

	first := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer(""),
		})
	first.turn(objective)
	opened := first.currentTask()
	if opened.ID == "" || opened.Status.IsTerminal() {
		t.Fatalf("setup left no resumable task: status=%s objective=%s", opened.Status, opened.ObjectiveState())
	}
	if opened.ActiveRole != workspace.Ask {
		t.Fatalf("setup role = %s, want ASK", opened.ActiveRole)
	}

	resuming := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("exec-1", "bash", map[string]any{"command": "go test ./..."}),
			readinessAnswer("GrepTool returns the fixture value."),
		})
	resuming.turn("continue")
	if *resuming.role != workspace.Ask {
		t.Fatalf("continuation role = %s, want the task's ASK authority", *resuming.role)
	}
	restored := resuming.currentTask()
	if restored.ActiveRole != workspace.Ask {
		t.Fatalf("recorded role = %s, want ASK", restored.ActiveRole)
	}
	for _, record := range restored.Context.Evidence {
		if strings.HasPrefix(record.Artifact, "cognition:command:") {
			t.Fatalf("an ASK task recorded command evidence after resume: %+v", record)
		}
	}
	refused := false
	for _, outcome := range resuming.toolResults() {
		if outcome.ToolName != "bash" {
			continue
		}
		// The stage projection or the live gate refuses the call; either way
		// the model is told it did not run.
		if !outcome.IsError {
			t.Fatalf("ASK executed a command after resume: %q", outcome.Text())
		}
		refused = true
	}
	if !refused {
		t.Fatal("the ASK turn never reached a refusal boundary")
	}
}

// A proven task that is reopened and given the same prompt starts a new
// invocation. It must not reuse the archived objective, proof, or evidence.
func TestProvenTaskSamePromptAfterRestartStartsANewTask(t *testing.T) {
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
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("the suite passes now"),
		})
	first.turn(objective)
	first.turn("verify the fix")
	proven := first.currentTask()
	if proven.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("setup did not prove the task: %s", proven.ObjectiveState())
	}

	// The restart restores the proven checkpoint. The same prompt is a new
	// invocation, not a continuation of the finished one.
	second := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{readinessAnswer("a fresh answer")})
	second.turn(objective)
	started := second.currentTask()
	if started.ID == proven.ID {
		t.Fatalf("the same prompt reused the proven task %q", proven.ID)
	}
	if started.Completion != nil || started.Proven() {
		t.Fatalf("the new invocation inherited the archived proof: %+v", started.Completion)
	}
	if len(started.Context.Facts) != 0 {
		t.Fatalf("the new invocation inherited the archived facts: %+v", started.Context.Facts)
	}
	archived, err := second.controller.ArchivedTasks()
	if err != nil {
		t.Fatalf("ArchivedTasks: %v", err)
	}
	found := false
	for _, record := range archived {
		if record.ID == proven.ID && record.Completion != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("the proven task was not preserved in history: %+v", archived)
	}
}

// Continuation wording with no live task is an ordinary request. It must not
// resurrect archived work or inherit its evidence; detached work needs an
// explicit /task resume.
func TestContinuationWithoutAnOpenTaskDoesNotResurrectArchivedWork(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "fix the flaky test in tools/grep.go"

	first := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer(""),
		})
	first.turn(objective)
	detached := first.currentTask()
	if detached.ID == "" {
		t.Fatal("setup created no task")
	}
	if _, _, err := first.controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	second := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root)},
		[]ai.AssistantMessage{readinessAnswer("there is no work to continue")})
	second.turn("continue")
	started := second.currentTask()
	if started.ID == "" {
		t.Fatal("the continuation created no task")
	}
	if started.ID == detached.ID {
		t.Fatalf("continuation wording resurrected the detached task %q", detached.ID)
	}
	for _, fact := range started.Context.Facts {
		if fact.Text == "" {
			t.Fatalf("the new task inherited an empty fact: %+v", fact)
		}
	}
	if len(started.Context.Evidence) != 0 {
		t.Fatalf("the new task inherited detached evidence: %+v", started.Context.Evidence)
	}
}
