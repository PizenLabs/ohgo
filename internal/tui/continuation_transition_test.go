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

// A BUILD task interrupted after its change must still be able to run the
// verification its own contract requires. "verify the fix" is a continuation,
// not a new READ request that needs ASK authority.
func TestVerificationContinuationRunsUnderBuildAndProves(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
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

	r.turn("Fix GrepTool in tools/grep.go and run go test ./tools")
	interrupted := r.currentTask()
	if interrupted.Status.IsTerminal() || interrupted.Proven() {
		t.Fatalf("the unverified turn published a finished task: status=%s objective=%s",
			interrupted.Status, interrupted.ObjectiveState())
	}

	r.turn("verify the fix")
	completed := r.currentTask()
	if report, ok := r.controller.LastProjection(); ok {
		t.Logf("last projection shape=%s suff=%s stage=%s evidence=%s", report.Shape, report.Sufficiency, report.Stage, report.Evidence)
	}
	if completed.ID != interrupted.ID {
		t.Fatalf("the verification continuation replaced the task: %q -> %q", interrupted.ID, completed.ID)
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("verification continuation did not prove the durable change: objective=%s summary=%s reason=%q",
			completed.ObjectiveState(), completed.CompletionSummary(), completed.ReviewReason)
	}
	if completed.Completion == nil || len(completed.Completion.Verified) == 0 {
		t.Fatalf("completion proof = %+v, want the post-change verification", completed.Completion)
	}
	if completed.ReviewReason != "" {
		t.Fatalf("completed task retained its resolved review reason %q", completed.ReviewReason)
	}
}

// The full lifecycle keeps one identity across an understanding phase, an
// investigation phase, an interrupted implementation, and a natural
// verification continuation. Only the explicit switches move authority; each
// turn still runs under the workspace in force.
func TestLifecycleUnderstandInvestigateImplementThenVerify(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("GrepTool returns the old value."),
			readinessToolCall("fuzz-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("the current suite passes"),
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
			}),
			readinessAnswer("wrote the change"),
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("the suite passes now"),
		})

	// ASK: understand the current implementation without executing or mutating.
	r.turnAs(objective, workspace.Ask)
	understood := r.currentTask()
	if understood.ActiveRole != workspace.Ask {
		t.Fatalf("understanding phase role = %s, want ASK", understood.ActiveRole)
	}
	if understood.Status.IsTerminal() || understood.Proven() {
		t.Fatalf("understanding phase published a finished mutation: status=%s objective=%s",
			understood.Status, understood.ObjectiveState())
	}
	if understood.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("understanding phase = %s (%s), want ACTIVE with the mutation deferred",
			understood.ObjectiveState(), understood.ReviewReason)
	}
	if strings.Contains(understood.ReviewReason, "no change was observed") {
		t.Fatalf("understanding phase was reported as a failed change: %s", understood.ReviewReason)
	}

	// FUZZ: explicit human switch, then investigate under FUZZ authority. The
	// applied handoff is the continuation decision for this task.
	if !r.program.switchWorkspace("fuzz") {
		t.Fatal("switching to FUZZ was refused")
	}
	r.turn(objective)
	investigated := r.currentTask()
	if investigated.ID != understood.ID {
		t.Fatalf("investigation replaced the task: %q -> %q", understood.ID, investigated.ID)
	}
	if investigated.ActiveRole != workspace.Fuzz {
		t.Fatalf("investigation role = %s, want FUZZ", investigated.ActiveRole)
	}
	if investigated.Proven() {
		t.Fatal("an investigation-only turn proved the mutation")
	}
	if investigated.ObjectiveState() != task.ObjectiveActive {
		t.Fatalf("investigation phase = %s (%s), want ACTIVE with the mutation deferred",
			investigated.ObjectiveState(), investigated.ReviewReason)
	}
	if strings.Contains(investigated.ReviewReason, "no change was observed") {
		t.Fatalf("investigation phase was reported as a failed change: %s", investigated.ReviewReason)
	}

	// BUILD: explicit human switch, then implement. The turn is interrupted
	// after the change and before its verification.
	if !r.program.switchWorkspace("build") {
		t.Fatal("switching to BUILD was refused")
	}
	r.turn(objective)
	implemented := r.currentTask()
	if implemented.ID != understood.ID {
		t.Fatalf("implementation replaced the task: %q -> %q", understood.ID, implemented.ID)
	}
	if implemented.ActiveRole != workspace.Build {
		t.Fatalf("implementation role = %s, want BUILD", implemented.ActiveRole)
	}
	if implemented.Proven() {
		t.Fatal("the unverified implementation was already proven")
	}

	// Verification: natural wording continues the same task and proves it
	// without a new workspace decision or a new identity.
	r.turn("check whether the fix worked")
	proven := r.currentTask()
	if proven.ID != understood.ID {
		t.Fatalf("verification replaced the task: %q -> %q", understood.ID, proven.ID)
	}
	if proven.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("verification did not prove the objective: %s (%s)",
			proven.ObjectiveState(), proven.CompletionSummary())
	}
	if proven.Completion == nil || len(proven.Completion.Verified) == 0 {
		t.Fatalf("verification proof = %+v, want the post-change check", proven.Completion)
	}
}

// Natural verification wording is the same task continuation as "verify the
// fix". The open task must own it before prediction can reinterpret the
// question as a new READ request, and the proof must still land on the same
// identity.
func TestVerificationQuestionContinuationKeepsTaskIdentityAndProves(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
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

	r.turn("Fix GrepTool in tools/grep.go and run go test ./tools")
	interrupted := r.currentTask()
	if interrupted.Proven() {
		t.Fatal("the unverified turn was already proven")
	}

	predicted := false
	predict := r.program.workspace.Predict
	r.program.workspace.Predict = func(text string) (TurnTransition, bool) {
		predicted = true
		return predict(text)
	}
	cmd := r.program.submit("check whether the fix worked")
	if predicted {
		t.Fatal("workspace prediction ran before the open task owned the prompt")
	}
	if cmd == nil {
		t.Fatalf("the verification continuation opened a workspace decision: %+v", r.program.state.Transition)
	}
	cmd()
	drainProgram(r.program)

	completed := r.currentTask()
	if completed.ID != interrupted.ID {
		t.Fatalf("the verification question replaced the task: %q -> %q", interrupted.ID, completed.ID)
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("verification question did not prove the durable change: objective=%s summary=%s reason=%q",
			completed.ObjectiveState(), completed.CompletionSummary(), completed.ReviewReason)
	}
	if completed.Completion == nil || len(completed.Completion.Verified) == 0 {
		t.Fatalf("completion proof = %+v, want the post-change verification", completed.Completion)
	}
}

// Keeping the current workspace after a decision must not detach the open task.
// The prompt still asks about the task's own work, so it continues the same
// contract under BUILD.
func TestVerificationQuestionContinuesWhenCurrentWorkspaceIsChosen(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
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

	r.turn("Fix GrepTool in tools/grep.go and run go test ./tools")
	interrupted := r.currentTask()
	r.turnAs("check whether the fix worked", workspace.Build)
	completed := r.currentTask()
	if completed.ID != interrupted.ID {
		t.Fatalf("choosing the current workspace replaced the task: %q -> %q", interrupted.ID, completed.ID)
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("the chosen continuation did not prove the task: objective=%s summary=%s",
			completed.ObjectiveState(), completed.CompletionSummary())
	}
}

// A continuation in FUZZ stays in FUZZ. It must not be redirected to ASK just
// because "continue" has no execution verb, and it must not change authority.
func TestOpenFuzzTaskContinueStaysInFuzz(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Fuzz,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("fuzz-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("GrepTool returns the old value."),
			readinessAnswer("the investigation continues"),
		})

	r.turnAs("Fix GrepTool in tools/grep.go and run go test ./tools", workspace.Fuzz)
	first := r.currentTask()
	if first.ActiveRole != workspace.Fuzz {
		t.Fatalf("initial role = %s, want FUZZ", first.ActiveRole)
	}
	r.turn("continue")
	second := r.currentTask()
	if second.ID != first.ID {
		t.Fatalf("continue replaced the FUZZ task: %q -> %q", first.ID, second.ID)
	}
	if second.ActiveRole != workspace.Fuzz {
		t.Fatalf("continue moved the task to %s, want FUZZ", second.ActiveRole)
	}
	if *r.role != workspace.Fuzz {
		t.Fatalf("continue changed live authority to %s, want FUZZ", *r.role)
	}
	if second.Proven() {
		t.Fatal("a read-only continuation proved the mutation objective")
	}
}

// Continuation grants no authority. Under ASK the task can keep its identity,
// but a verification tool call is still denied by the live gate and the
// objective cannot become proven from the model's claim.
func TestVerificationContinuationUnderAskCannotExecute(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
			readinessAnswer("GrepTool returns the old value."),
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("the suite passes now"),
		})

	r.turnAs("Fix GrepTool in tools/grep.go and run go test ./tools", workspace.Ask)
	first := r.currentTask()
	r.turn("verify the fix")
	current := r.currentTask()
	if current.ID != first.ID {
		t.Fatalf("ASK continuation replaced the task: %q -> %q", first.ID, current.ID)
	}
	if *r.role != workspace.Ask {
		t.Fatalf("continuation changed ASK authority to %s", *r.role)
	}
	if current.Proven() || current.Completion != nil {
		t.Fatalf("ASK continuation executed verification and proved the task: status=%s objective=%s",
			current.Status, current.ObjectiveState())
	}
	denied := false
	for _, result := range r.toolResults() {
		if result.ToolName == "bash" && result.IsError {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("the ASK continuation's bash call was not denied: %+v", r.toolResults())
	}
}

// A failing verification continuation keeps the same task and its evidence,
// and does not publish a proof. The task stays resumable for another attempt.
func TestFailingVerificationContinuationDoesNotProve(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":        "module example.com/readiness\n\ngo 1.21\n",
		"tools/grep.go": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
		"tools/grep_test.go": "package tools\n\nimport \"testing\"\n\n" +
			"func TestGrepTool(t *testing.T) { t.Fatal(\"still broken\") }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\n// GrepTool still returns the old value.\nfunc GrepTool() string { return \"old\" }\n",
			}),
			readinessAnswer("wrote the change"),
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("the suite still fails"),
		})

	r.turn("Fix GrepTool in tools/grep.go and run go test ./tools")
	interrupted := r.currentTask()
	r.turn("check whether the fix worked")
	current := r.currentTask()
	if current.ID != interrupted.ID {
		t.Fatalf("failing verification replaced the task: %q -> %q", interrupted.ID, current.ID)
	}
	if current.Proven() || current.Status.IsTerminal() {
		t.Fatalf("failing verification published a finished task: status=%s objective=%s",
			current.Status, current.ObjectiveState())
	}
	if current.Completion != nil {
		t.Fatalf("failing verification carried a proof: %+v", current.Completion)
	}
}

// Once the objective is proven, repeating the continuation must not archive
// the proof and start a duplicate task. The lifecycle terminates; a new
// objective is the only way to continue.
func TestProvenTaskRejectsRedundantContinuation(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	r := newReadinessRuntime(t, root, workspace.Build,
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

	r.turn("Fix GrepTool in tools/grep.go and run go test ./tools")
	r.turn("verify the fix")
	proven := r.currentTask()
	if proven.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("setup did not prove the task: %s", proven.ObjectiveState())
	}

	requestsBefore := *r.index
	if cmd := r.program.submit("verify the fix"); cmd != nil {
		cmd()
		drainProgram(r.program)
	}
	if *r.index != requestsBefore {
		t.Fatal("a proven task reached the provider for a redundant continuation")
	}
	current := r.currentTask()
	if current.ID != proven.ID {
		t.Fatalf("a redundant continuation replaced the proven task: %q -> %q", proven.ID, current.ID)
	}
	if current.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("the proof was lost after a redundant continuation: %s", current.ObjectiveState())
	}
	if notice := lastNotice(r.program); !strings.Contains(notice, "already proven") {
		t.Fatalf("redundant continuation notice = %q, want the proven state", notice)
	}
}

// The task state owns a continuation before workspace prediction runs. An open
// task must not be sent through a predictor that sees only the prompt text.
func TestOpenTaskContinuationBypassesWorkspacePrediction(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.task.Current = func() (TaskResume, bool) {
		return TaskResume{ID: "task-1", Status: "RUNNING"}, true
	}
	p.workspace.Current = func() string { return "BUILD" }

	for _, text := range []string{"continue", "verify the fix", "retry"} {
		if !p.continuesOpenTask(text) {
			t.Errorf("continuesOpenTask(%q) = false, want the open task to own it", text)
		}
	}
	if p.continuesOpenTask("fix the bug") {
		t.Fatal("a new objective was treated as a continuation")
	}

	p.task.Current = func() (TaskResume, bool) {
		return TaskResume{ID: "task-1", Status: "COMPLETED"}, true
	}
	if p.continuesOpenTask("continue") {
		t.Fatal("a terminal task owned a continuation")
	}

	p.task.Current = func() (TaskResume, bool) {
		return TaskResume{
			ID: "task-1", Status: "RUNNING",
			HandoffStatus: "APPLIED", HandoffTo: "BUILD",
		}, true
	}
	p.workspace.Current = func() string { return "BUILD" }
	if !p.continuesOpenTask("implement the follow-up") {
		t.Fatal("an applied handoff did not own its next turn")
	}
}
