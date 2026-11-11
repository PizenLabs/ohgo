package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// This is the production sequence from the bug report: a design answer, an
// explicit "fix it" that mutates but stops unverified, then a palette
// refinement. The refinement must stay in the same BUILD trajectory and update
// the files the implementation already created.
func TestLifecycleDesignFixThenImplementationRefinement(t *testing.T) {
	root := t.TempDir()

	design := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{readinessAnswer("Use a single stage with animated category pills.")})
	design.turn("I want to design a single-screen animation where clicking various buttons animates the information.")
	first := design.currentTask()
	if first.Contract == nil || first.Contract.Kind != contract.ObjectiveInformational {
		t.Fatalf("design contract = %+v, want informational", first.Contract)
	}
	if first.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("design objective = %s, want PROVEN", first.ObjectiveState())
	}

	fixed := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "index.html",
				"content": "<!doctype html><p>purple</p>\n",
			}),
			readinessAnswer("implemented the screen"),
		})
	fixed.turn("ok, fix it please")
	implementation := fixed.currentTask()
	if implementation.ID == first.ID {
		t.Fatal("the implementation reused the finished design task")
	}
	if implementation.Contract == nil || implementation.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("implementation contract = %+v, want mutation", implementation.Contract)
	}
	if implementation.Status != task.StatusNeedsReview {
		t.Fatalf("implementation status = %s, want NEEDS_REVIEW", implementation.Status)
	}

	refined := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-2", "write", map[string]any{
				"path":    "index.html",
				"content": "<!doctype html><p>green on black</p>\n",
			}),
			readinessAnswer("updated the palette"),
		})
	const prompt = "I want to use green and black as the main colors because I like those two colors the most."
	cmd := refined.program.submit(prompt)
	if cmd == nil {
		t.Fatal("submitting the palette refinement produced no turn")
	}
	cmd()
	drainProgram(refined.program)

	current := refined.currentTask()
	if current.ID != implementation.ID {
		t.Fatalf("the palette refinement replaced the task: %q -> %q", implementation.ID, current.ID)
	}
	data, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatalf("read the refined page: %v", err)
	}
	if !strings.Contains(string(data), "green on black") {
		t.Fatalf("the palette refinement did not reach the page: %q", data)
	}
	report, ok := refined.controller.LastProjection()
	if !ok {
		t.Fatal("the palette refinement recorded no projection")
	}
	if !slices.Contains(report.ToolNames, "write") {
		t.Fatalf("offered tools = %v, want the write surface", report.ToolNames)
	}
	if strings.Contains(report.Shape, "mutation=forbidden") {
		t.Fatalf("palette refinement shape = %q, want mutation authority", report.Shape)
	}
}

// ContinuationAuthorityPreservation: a BUILD implementation that ended
// unverified is still the open objective. A refinement that only names the
// desired result ("I want to use green and black") must inherit that objective
// and keep the write surface. Rebuilding an informational contract from the
// delta would remove mutation authority from a task that is still executing.
func TestContinuationAuthorityPreservationKeepsBuildMutationAuthority(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"

	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
			}),
			readinessAnswer("wrote the change"),
		})
	r.turn(objective)
	opened := r.currentTask()
	if opened.Status.IsTerminal() || opened.Proven() {
		t.Fatalf("the unverified implementation finished: status=%s objective=%s",
			opened.Status, opened.ObjectiveState())
	}
	if opened.Status != task.StatusNeedsReview {
		t.Fatalf("implementation status = %s, want NEEDS_REVIEW", opened.Status)
	}
	if opened.Contract == nil || opened.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("implementation contract = %+v, want mutation", opened.Contract)
	}

	const refinement = "I want to use green and black as the main colors because I like those two colors the most."
	r2 := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-2", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"green\" }\n",
			}),
			readinessAnswer("updated the palette"),
		})
	if !r2.program.continuesOpenTask(refinement) {
		t.Fatal("the refinement did not inherit the open implementation task")
	}
	cmd := r2.program.submit(refinement)
	if cmd == nil {
		t.Fatal("submitting the refinement produced no turn")
	}
	cmd()
	drainProgram(r2.program)

	current := r2.currentTask()
	if current.ID != opened.ID {
		t.Fatalf("the refinement replaced the task: %q -> %q", opened.ID, current.ID)
	}
	if *r2.role != workspace.Build {
		t.Fatalf("the refinement left BUILD for %s", r2.role.Label())
	}
	data, err := os.ReadFile(filepath.Join(root, "tools/grep.go"))
	if err != nil {
		t.Fatalf("read the refined file: %v", err)
	}
	if !strings.Contains(string(data), `"green"`) {
		t.Fatalf("the refinement did not reach the repository: %q", data)
	}
	report, ok := r2.controller.LastProjection()
	if !ok {
		t.Fatal("the refinement turn recorded no projection")
	}
	if !slices.Contains(report.ToolNames, "write") {
		t.Fatalf("offered tools = %v, want the write surface", report.ToolNames)
	}
	if strings.Contains(report.Shape, "mutation=forbidden") {
		t.Fatalf("refinement shape = %q, want mutation authority", report.Shape)
	}
}

// The reconciliation must not become sticky BUILD. A request that explicitly
// forbids repository mutation is not a refinement, must not inherit the open
// implementation, and must not reach the write tool.
func TestImplementationRefinementStopsAtAnExplicitNoMutationRequest(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"

	first := []ai.AssistantMessage{
		readinessToolCall("write-1", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
		}),
		readinessAnswer("wrote the change"),
	}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, first)
	r.turn(objective)

	const stop = "Actually don't change anything. Explain the current architecture."
	r2 := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-2", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"forbidden\" }\n",
			}),
			readinessAnswer("The current implementation returns the old value."),
		})
	if r2.program.continuesOpenTask(stop) {
		t.Fatal("a no-mutation request inherited the open implementation task")
	}
	if cmd := r2.program.submit(stop); cmd != nil {
		t.Fatal("the no-mutation request ran before the ASK boundary decision")
	}
	pending := r2.program.state.Transition
	if pending == nil || pending.Proposed != "ASK" || pending.Automatic {
		t.Fatalf("transition = %+v, want the ASK confirmation boundary", pending)
	}
	if *r2.role != workspace.Build {
		t.Fatalf("the pending decision moved authority to %s", r2.role.Label())
	}
	_, cmd := r2.program.resolveTransition(true)
	if cmd == nil {
		t.Fatal("accepting the ASK boundary produced no turn")
	}
	cmd()
	drainProgram(r2.program)

	if *r2.role != workspace.Ask {
		t.Fatalf("accepted no-mutation role = %s, want ASK", r2.role.Label())
	}
	current := r2.currentTask()
	if current.ID == "" {
		t.Fatal("the ASK turn recorded no task")
	}
	data, err := os.ReadFile(filepath.Join(root, "tools/grep.go"))
	if err != nil {
		t.Fatalf("read the repository file: %v", err)
	}
	if strings.Contains(string(data), "forbidden") {
		t.Fatalf("a no-mutation request changed the repository: %q", data)
	}
	if report, ok := r2.controller.LastProjection(); ok && !strings.Contains(report.Shape, "mutation=forbidden") {
		t.Fatalf("no-mutation shape = %q, want mutation forbidden", report.Shape)
	}
}

// The other explicit authority change is investigation. A request that asks to
// investigate before changing must yield to FUZZ, not inherit BUILD because the
// implementation is open.
func TestImplementationRefinementYieldsToInvestigation(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"

	first := []ai.AssistantMessage{
		readinessToolCall("write-1", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
		}),
		readinessAnswer("wrote the change"),
	}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, first)
	r.turn(objective)
	opened := r.currentTask()

	const investigate = "Before changing it, investigate why the animation stutters."
	r2 := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{readinessAnswer("The animation stalls while the counter is recomputed.")})
	if r2.program.continuesOpenTask(investigate) {
		t.Fatal("an investigation request inherited the open implementation task")
	}
	cmd := r2.program.submit(investigate)
	if cmd == nil {
		t.Fatal("submitting the investigation produced no turn")
	}
	cmd()
	drainProgram(r2.program)

	if *r2.role != workspace.Fuzz {
		t.Fatalf("investigation role = %s, want FUZZ", r2.role.Label())
	}
	current := r2.currentTask()
	if current.ID == opened.ID {
		t.Fatal("an investigation objective was recorded as the unchanged implementation task")
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("investigation task role = %s, want FUZZ", current.ActiveRole)
	}
}

// An explicit mutation prompt is a new mutation objective, not an ambiguous
// delta. It must not inherit the open contract, but the control plane must keep
// BUILD and the write surface: the authority class did not change.
func TestExplicitMutationRefinementStaysBuildWithANewContract(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"

	first := []ai.AssistantMessage{
		readinessToolCall("write-1", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
		}),
		readinessAnswer("wrote the change"),
	}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, first)
	r.turn(objective)
	opened := r.currentTask()

	const delta = "remove the sidebar"
	r2 := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
		[]ai.AssistantMessage{
			readinessToolCall("write-2", "write", map[string]any{
				"path":    "styles.css",
				"content": ".sidebar { display: none; }\n",
			}),
			readinessAnswer("removed the sidebar"),
		})
	if r2.program.continuesOpenTask(delta) {
		t.Fatal("an explicit mutation prompt inherited the open task contract")
	}
	cmd := r2.program.submit(delta)
	if cmd == nil {
		t.Fatal("submitting the explicit mutation produced no turn")
	}
	cmd()
	drainProgram(r2.program)

	current := r2.currentTask()
	if current.ID == opened.ID {
		t.Fatal("the explicit mutation reused the previous objective")
	}
	if current.Contract == nil || current.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("explicit mutation contract = %+v, want mutation", current.Contract)
	}
	if *r2.role != workspace.Build {
		t.Fatalf("explicit mutation role = %s, want BUILD", r2.role.Label())
	}
	data, err := os.ReadFile(filepath.Join(root, "styles.css"))
	if err != nil {
		t.Fatalf("read the explicit mutation result: %v", err)
	}
	if !strings.Contains(string(data), "display: none") {
		t.Fatalf("the explicit mutation did not reach the repository: %q", data)
	}
	if report, ok := r2.controller.LastProjection(); ok {
		if !slices.Contains(report.ToolNames, "write") {
			t.Fatalf("offered tools = %v, want the write surface", report.ToolNames)
		}
		if strings.Contains(report.Shape, "mutation=forbidden") {
			t.Fatalf("explicit mutation shape = %q, want mutation authority", report.Shape)
		}
	}
}

// The task layer owns the refinement before the prompt is classified in
// isolation. The exact scenario is an unverified BUILD change followed by a
// preference statement that names no verb of its own.
func TestTaskTurnDecisionNamesImplementationRefinement(t *testing.T) {
	current := TaskResume{
		ID:          "task-1",
		Status:      string(task.StatusNeedsReview),
		Role:        string(workspace.Build),
		HasContract: true,
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveMutation,
			Objective: "Fix GrepTool in tools/grep.go and run go test ./tools",
		},
	}
	text := "I want to use green and black as the main colors."
	decision, owned := taskTurnDecision(current, text, "build", workspace.BuildIntentContract(text))
	if !owned || decision != "IMPLEMENTATION_REFINEMENT" {
		t.Fatalf("decision = %q owned=%v, want IMPLEMENTATION_REFINEMENT", decision, owned)
	}
}
