package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Level B of the Task Continuity product-readiness suite: the real turn
// entrypoint, the real workspace policy, the real task controller, and real
// file/command tools against temporary projects. Test IDs match
// docs/testing/TASK_CONTINUITY_TEST_CATALOG.md.

type readinessRuntime struct {
	t          *testing.T
	root       string
	role       *workspace.Workspace
	controller *task.Controller
	program    *Program
	loop       *agent.Loop
	messages   []ai.AssistantMessage
	index      *int
}

// newReadinessRuntime assembles the runtime the same way the application does:
// a controller behind TaskHooks, a workspace switcher that records the human
// transition, and a loop whose admission hook consults the live workspace
// authority. The model is scripted; no provider or network is involved.
func newReadinessRuntime(t *testing.T, root string, initial workspace.Workspace, toolSet []agent.Tool, messages []ai.AssistantMessage) *readinessRuntime {
	t.Helper()
	return newReadinessRuntimeWithTools(t, root, initial, func(*workspace.Workspace) []agent.Tool {
		return toolSet
	}, messages)
}

// newReadinessRuntimeWithTools is the same runtime, but the tool set is built
// after the live role exists. A tool whose authority is read per call (the
// artifact tool) needs that live role, not a copy captured at construction.
func newReadinessRuntimeWithTools(t *testing.T, root string, initial workspace.Workspace, toolsFor func(*workspace.Workspace) []agent.Tool, messages []ai.AssistantMessage) *readinessRuntime {
	t.Helper()
	role := initial
	roleRef := &role
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, _, err := controller.Restore(); err != nil {
		t.Fatalf("startup restore: %v", err)
	}
	index := 0
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if index >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}
	loop, err := agent.New(agent.Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    toolsFor(roleRef),
		MaxTurns: 16,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	loop.SetApprovalHook(func(ai.ToolCall, agent.Approval) (bool, string) {
		return true, "approved by the readiness suite"
	})
	loop.SetAdmitHook(func(call ai.ToolCall, tool agent.Tool) (bool, string) {
		if err := (workspace.Authority{Workspace: role}).Admit(tool, call.Arguments); err != nil {
			return false, err.Error()
		}
		return true, ""
	})

	program := NewProgram(loop, "test", 0)
	program.SetCompletionContext(root, fixedResolver{})
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return string(role) },
		Switch: func(name string) error {
			next, err := workspace.Parse(name)
			if err != nil {
				return err
			}
			from := role
			role = next
			if controller != nil {
				if err := controller.RecordWorkspaceSwitch(from, next, ""); err != nil {
					return err
				}
			}
			return nil
		},
		SwitchWithReason: func(name, reason, workspaceState string) error {
			next, err := workspace.Parse(name)
			if err != nil {
				return err
			}
			from := role
			role = next
			if controller != nil {
				if err := controller.RecordAutomaticSwitch(from, next, reason, workspaceState); err != nil {
					return err
				}
			}
			return nil
		},
		PrepareTurn: func(text string) TurnAuthority {
			route := workspace.RouteTurn(role, text)
			return TurnAuthority{
				Active:  role.Label(),
				Intent:  strings.ToUpper(string(route.Intent)),
				Blocked: !route.Allowed(),
				Reason:  route.Message(),
			}
		},
		Predict: func(text string) (TurnTransition, bool) {
			requirement := workspace.AnalyzeRequest(text)
			decision := workspace.DecideTransition(role, requirement)
			if !decision.Proposed() {
				return TurnTransition{}, false
			}
			current, proposed := decision.Current.Style(), decision.Target.Style()
			kind := TransitionKindWorkspace
			if decision.NeedsConfirmation() && !role.Encompasses(decision.Target) {
				kind = TransitionKindHandoff
			}
			return TurnTransition{
				Kind:           kind,
				Current:        current.Label,
				Proposed:       proposed.Label,
				CurrentAccent:  current.Accent,
				ProposedAccent: proposed.Accent,
				Intent:         strings.ToUpper(string(requirement.Intent)),
				Strength:       string(requirement.Strength),
				Question:       decision.Question,
				Reason:         decision.Reason,
				Automatic:      decision.Automatic(),
			}, true
		},
	})
	program.SetTaskHooks(runtimeHooks(controller))
	return &readinessRuntime{
		t: t, root: root, role: roleRef, controller: controller,
		program: program, loop: loop, messages: messages, index: &index,
	}
}

func (r *readinessRuntime) turn(text string) {
	r.t.Helper()
	r.program.runTurn(text)
	drainProgram(r.program)
}

func (r *readinessRuntime) turnAs(text string, role workspace.Workspace) {
	r.t.Helper()
	r.program.runTurnWithChoice(text, role)
	drainProgram(r.program)
}

// enableToolHandoff installs the production tool-boundary policy on the real
// turn path: the workspace owner decides the automatic move, records it on the
// same task, and the loop re-authorizes the call under the new live role.
func (r *readinessRuntime) enableToolHandoff() {
	r.t.Helper()
	var mu sync.Mutex
	handoff := func(_ ai.ToolCall, tool agent.Tool, args map[string]any) bool {
		mu.Lock()
		defer mu.Unlock()
		decision, ok := workspace.ToolCallTransition(*r.role, tool, args)
		if !ok {
			return false
		}
		from := *r.role
		*r.role = decision.Target
		if r.controller != nil {
			if err := r.controller.RecordAutomaticSwitch(from, decision.Target, decision.Reason, ""); err != nil {
				return false
			}
		}
		return true
	}
	hooks := r.program.workspace
	hooks.Handoff = handoff
	r.program.SetWorkspaceSwitcher(hooks)
}

func (r *readinessRuntime) currentTask() task.Task {
	r.t.Helper()
	current, ok := r.controller.Current()
	if !ok {
		r.t.Fatal("no durable task")
	}
	return current
}

func (r *readinessRuntime) toolResults() []agent.ToolResultMessage {
	var out []agent.ToolResultMessage
	for _, message := range r.program.history {
		if result, ok := message.(agent.ToolResultMessage); ok {
			out = append(out, result)
		}
	}
	return out
}

func writeReadinessModule(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":        "module example.com/readiness\n\ngo 1.21\n",
		"tools/grep.go": "package tools\n\nfunc GrepTool() string { return \"old\" }\n",
		"tools/grep_test.go": "package tools\n\nimport \"testing\"\n\n" +
			"func TestGrepTool(t *testing.T) {\n\tif GrepTool() == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readinessToolCall(id, name string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
		StopReason: ai.StopToolUse,
	}
}

func readinessAnswer(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

// RC-C06 characterizes the capability policy: ASK must ask before any work it
// cannot perform, and every such decision is a human-boundary confirmation.
func TestReadinessC06WideningPolicyShape(t *testing.T) {
	mutation := workspace.DecideTransition(workspace.Ask, workspace.AnalyzeRequest("Fix GrepTool in tools/grep.go and run go test ./tools"))
	if !mutation.NeedsConfirmation() || mutation.Target != workspace.Build {
		t.Fatalf("RC-C06: ASK + mutation = %+v, want confirmed BUILD", mutation)
	}
	mixed := workspace.DecideTransition(workspace.Ask, workspace.AnalyzeRequest("investigate the flaky test in tools/grep.go then fix it and run go test ./tools"))
	if !mixed.NeedsConfirmation() || mixed.Target != workspace.Fuzz {
		t.Fatalf("RC-C06: ASK + mixed investigation = %+v, want the investigation phase under FUZZ", mixed)
	}
	pure := workspace.DecideTransition(workspace.Ask, workspace.AnalyzeRequest("investigate why tools/grep.go is slow"))
	if !pure.NeedsConfirmation() || pure.Target != workspace.Fuzz {
		t.Fatalf("RC-C06: ASK + investigate = %+v, want FUZZ", pure)
	}
}

// RC-C07: a refused authority switch fails the recorded handoff and never
// reports APPLIED or changes the live role.
func TestReadinessC07FailedSwitchDoesNotReportApplied(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	program := NewProgram(scriptedLoop(t, "started", "implemented"), "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current: func() string { return string(role) },
		Switch: func(string) error {
			return os.ErrPermission
		},
		Predict: func(text string) (TurnTransition, bool) {
			requirement := workspace.AnalyzeRequest(text)
			decision := workspace.DecideTransition(role, requirement)
			if !decision.Proposed() {
				return TurnTransition{}, false
			}
			current, proposed := decision.Current.Style(), decision.Target.Style()
			kind := TransitionKindWorkspace
			if decision.NeedsConfirmation() && !role.Encompasses(decision.Target) {
				kind = TransitionKindHandoff
			}
			return TurnTransition{
				Kind:           kind,
				Current:        current.Label,
				Proposed:       proposed.Label,
				CurrentAccent:  current.Accent,
				ProposedAccent: proposed.Accent,
				Intent:         strings.ToUpper(string(requirement.Intent)),
				Strength:       string(requirement.Strength),
				Question:       decision.Question,
				Reason:         decision.Reason,
				Automatic:      decision.Automatic(),
			}, true
		},
	})
	program.SetTaskHooks(runtimeHooks(controller))
	program.runTurn("fix the flaky test")
	if _, ok := controller.Current(); !ok {
		t.Fatal("RC-C07: the initial turn recorded no task")
	}
	program.state.Apply(InputChanged{Text: "why does this work?"})
	if cmd := program.submit("why does this work?"); cmd != nil {
		t.Fatal("RC-C07: the handoff ran before the decision")
	}
	if program.state.Transition == nil {
		t.Fatal("RC-C07: no ASK-boundary decision was proposed")
	}
	program.resolveTransition(true)
	if role != workspace.Fuzz {
		t.Fatalf("RC-C07: a failed switch moved authority to %s", role)
	}
	current, _ := controller.Current()
	if current.Handoff == nil || current.Handoff.Status != task.HandoffFailed {
		t.Fatalf("RC-C07: handoff = %+v, want FAILED", current.Handoff)
	}
	if current.Handoff.Status == task.HandoffApplied || current.ActiveRole == workspace.Build {
		t.Fatalf("RC-C07: a failed switch was reported as applied: %+v", current)
	}
	if !strings.Contains(program.state.Input, "why does this work?") {
		t.Fatalf("RC-C07: the refused request was not restored: %q", program.state.Input)
	}
}

// RA-A02: two turns that carry the same objective text under the same role
// continue one durable identity. The accepted contract says multiple turns
// addressing the same objective preserve task identity.
func TestReadinessA02SameObjectiveSameRoleTurnsPreserveIdentity(t *testing.T) {
	root := t.TempDir()
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	r := newReadinessRuntime(t, root, workspace.Ask, []agent.Tool{tools.NewReadTool(root)}, []ai.AssistantMessage{
		readinessAnswer("still understanding"),
		readinessAnswer("still understanding"),
		readinessAnswer("done"),
	})
	r.turnAs(objective, workspace.Ask)
	first := r.currentTask()
	if first.Status.IsTerminal() {
		t.Fatalf("RA-A02: fixture completed unexpectedly: %s", first.Status)
	}
	r.turnAs(objective, workspace.Ask)
	second := r.currentTask()
	if second.ID != first.ID {
		t.Fatalf("RA-A02: a repeated turn on the same objective changed identity %q -> %q", first.ID, second.ID)
	}
	// Continuity is one durable identity, not merely an equal ID: a second
	// creation event would mean the task was replaced and relabelled.
	created := 0
	for _, event := range second.Timeline {
		if event.Kind == task.TimelineCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("RA-A02: %d creation events after two turns, want exactly one", created)
	}
}

// RA-A02b is the control for RA-A02: an explicit continuation phrase attaches
// the next turn to the same durable task.
func TestReadinessA02bExplicitContinuationPreservesIdentity(t *testing.T) {
	root := t.TempDir()
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	r := newReadinessRuntime(t, root, workspace.Ask, []agent.Tool{tools.NewReadTool(root)}, []ai.AssistantMessage{
		readinessAnswer("still understanding"),
		readinessAnswer("still understanding"),
	})
	r.turnAs(objective, workspace.Ask)
	first := r.currentTask()
	r.turn("continue")
	second := r.currentTask()
	if second.ID != first.ID {
		t.Fatalf("RA-A02b: explicit continuation changed identity %q -> %q", first.ID, second.ID)
	}
}

// RA-A08: starting a new objective while the live task is terminal archives the
// completed task first, so its objective, evidence, proof, and timeline survive
// the checkpoint replacement.
func TestReadinessA08CompletedTaskIsPreservedBeforeANewObjective(t *testing.T) {
	root := t.TempDir()
	r := newReadinessRuntime(t, root, workspace.Ask, []agent.Tool{tools.NewReadTool(root)}, []ai.AssistantMessage{
		readinessAnswer("the parser uses a queue"),
		readinessAnswer("the release process is documented"),
	})
	r.turnAs("explain the parser", workspace.Ask)
	completed := r.currentTask()
	if completed.Status != task.StatusCompleted {
		t.Fatalf("RA-A08: fixture did not complete the first task: %s", completed.Status)
	}
	r.turnAs("document the release process", workspace.Ask)
	next := r.currentTask()
	if next.ID == completed.ID {
		t.Fatalf("RA-A08: a new objective reused the completed task")
	}
	archived, err := r.controller.Store().LoadArchived(completed.ID)
	if err != nil || !archived.Found {
		t.Fatalf("RA-A08: the completed task was not preserved in the archive: found=%v err=%v", archived.Found, err)
	}
	if archived.Task.Objective != "explain the parser" {
		t.Fatalf("RA-A08: archived objective = %q", archived.Task.Objective)
	}
	// History means the whole continuity record, not just the objective: the
	// proof and timeline are what a human needs to audit the completed work.
	if archived.Task.Status != task.StatusCompleted {
		t.Fatalf("RA-A08: archived status = %s, want COMPLETED", archived.Task.Status)
	}
	if archived.Task.Completion == nil {
		t.Fatal("RA-A08: the archived completed task lost its completion proof")
	}
	if len(archived.Task.Timeline) == 0 {
		t.Fatal("RA-A08: the archived completed task lost its timeline")
	}
}

// RI-I01: the documented understand -> investigate -> implement -> verify
// scenario through the real turn entrypoint. The ASK and FUZZ phases use the
// explicit human selector/workspace-command paths because the widening policy
// only proposes FUZZ for an investigate-classified request while a mutation
// contract is only derived from a mutation-classified request; that boundary is
// reported as finding F-03.
func TestReadinessI01UnderstandInvestigateImplementVerifyRestart(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	messages := []ai.AssistantMessage{
		readinessToolCall("read-1", "read", map[string]any{"path": "tools/grep.go"}),
		readinessAnswer("GrepTool returns the old value."),
		readinessToolCall("fuzz-1", "bash", map[string]any{"command": "go test ./tools"}),
		readinessAnswer("the current suite passes"),
		readinessToolCall("write-1", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
		}),
		readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
		readinessAnswer("implemented and verified"),
	}
	r := newReadinessRuntime(t, root, workspace.Ask,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, messages)

	// ASK: a read-only turn under the human-selected read authority.
	r.turnAs(objective, workspace.Ask)
	first := r.currentTask()
	if first.ActiveRole != workspace.Ask {
		t.Fatalf("RI-I01: ASK turn role = %s", first.ActiveRole)
	}
	if first.ID == "" {
		t.Fatal("RI-I01: the ASK turn did not create a durable task")
	}
	if first.Status.IsTerminal() {
		t.Fatalf("RI-I01: the ASK turn completed a mutation objective: %s", first.Status)
	}

	// FUZZ: an explicit human workspace command, then a bounded investigation.
	if !r.program.switchWorkspace("fuzz") {
		t.Fatal("RI-I01: switching to FUZZ was refused")
	}
	if *r.role != workspace.Fuzz {
		t.Fatalf("RI-I01: live role = %s, want FUZZ", *r.role)
	}
	r.turnAs(objective, workspace.Fuzz)
	fuzz := r.currentTask()
	if fuzz.ID != first.ID {
		t.Fatalf("RI-I01: the FUZZ turn replaced the task: %q -> %q", first.ID, fuzz.ID)
	}
	if fuzz.ActiveRole != workspace.Fuzz {
		t.Fatalf("RI-I01: FUZZ turn role = %s", fuzz.ActiveRole)
	}

	// BUILD: FUZZ -> BUILD is cooperative execution-workspace movement. The
	// control plane applies it automatically and the same task continues.
	cmd := r.program.submit("implement the fix and verify it")
	if cmd == nil {
		t.Fatal("RI-I01: the automatic BUILD handoff did not start the turn")
	}
	if *r.role != workspace.Build {
		t.Fatalf("RI-I01: automatic handoff left authority at %s", *r.role)
	}
	if r.program.state.Transition != nil {
		t.Fatalf("RI-I01: automatic handoff opened a decision surface: %+v", r.program.state.Transition)
	}
	// The next message is the BUILD write/check/answer sequence.
	cmd()
	drainProgram(r.program)

	if *r.role != workspace.Build {
		t.Fatalf("RI-I01: live authority = %s, want BUILD", *r.role)
	}
	if err := (workspace.Authority{Workspace: *r.role}).Admit(tools.NewWriteTool(root), map[string]any{"path": "tools/grep.go"}); err != nil {
		t.Fatalf("RI-I01: BUILD could not write: %v", err)
	}
	completed := r.currentTask()
	if completed.ID != first.ID {
		t.Fatalf("RI-I01: the BUILD handoff recreated the task: %q -> %q", first.ID, completed.ID)
	}
	if completed.Status != task.StatusCompleted {
		t.Fatalf("RI-I01: status = %s, want COMPLETED: %s", completed.Status, completed.CompletionSummary())
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("RI-I01: objective = %s, want PROVEN: %s", completed.ObjectiveState(), completed.CompletionSummary())
	}
	proof, ok := completed.CompletionProof()
	if !ok || !proof.Observed || len(proof.Verified) == 0 || len(proof.EvidenceIDs) == 0 {
		t.Fatalf("RI-I01: proof = %+v, want observed change, verification, and evidence ids", proof)
	}
	for _, role := range []workspace.Workspace{workspace.Ask, workspace.Fuzz, workspace.Build} {
		if _, ok := completed.Context.Role(role); !ok {
			t.Fatalf("RI-I01: %s context was not preserved across the transition", role.Label())
		}
	}
	buildCtx, _ := completed.Context.Role(workspace.Build)
	joinedProgress := strings.Join(buildCtx.Progress, "\n")
	if !strings.Contains(joinedProgress, "control-plane workspace switch from FUZZ") {
		t.Fatalf("RI-I01: BUILD context did not receive the handoff summary: %+v", buildCtx)
	}
	for _, id := range proof.EvidenceIDs {
		found := false
		for _, record := range completed.Context.Evidence {
			if record.ID != id {
				continue
			}
			found = true
			if record.TaskID != completed.ID || !record.Role.Valid() || record.Source == "" || record.Artifact == "" {
				t.Fatalf("RI-I01: proof evidence %q lost provenance: %+v", id, record)
			}
		}
		if !found {
			t.Fatalf("RI-I01: proof cites %q, which is absent from task evidence", id)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "tools", "grep.go"))
	if err != nil {
		t.Fatalf("RI-I01: read changed file: %v", err)
	}
	if !strings.Contains(string(data), "new") {
		t.Fatalf("RI-I01: file content = %q, want the written change", data)
	}

	// Restart: a fresh application instance restores the same identity, role,
	// evidence, and proof.
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return *r.role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RI-I01: restore: found=%v err=%v", found, err)
	}
	if restored.ID != completed.ID {
		t.Fatalf("RI-I01: restart changed identity: %q -> %q", completed.ID, restored.ID)
	}
	if restored.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("RI-I01: restart objective = %s, want PROVEN", restored.ObjectiveState())
	}
	if restored.ActiveRole != workspace.Build {
		t.Fatalf("RI-I01: restored role = %s, want BUILD", restored.ActiveRole)
	}
	if len(restored.Context.Evidence) == 0 || len(restored.Timeline) == 0 {
		t.Fatalf("RI-I01: restart lost evidence or timeline: %+v", restored)
	}
}

// RI-I02: an implementation interrupted after the mutation and before its
// verification is resumable, is not falsely completed, and completes through
// the normal turn path once the check runs.
func TestReadinessI02InterruptedImplementationResumesAndCompletes(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"

	first := []ai.AssistantMessage{
		readinessToolCall("write-1", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
		}),
		readinessAnswer("wrote the change"),
	}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, first)
	r.turn(objective)
	interrupted, ok := r.controller.Current()
	if !ok {
		t.Fatalf("RI-I02: the first turn recorded no task (notice=%q)", lastNotice(r.program))
	}
	if interrupted.Status.IsTerminal() {
		t.Fatalf("RI-I02: the unverified turn completed: %s", interrupted.Status)
	}
	if interrupted.Proven() {
		t.Fatal("RI-I02: an unverified mutation was PROVEN")
	}
	if interrupted.Completion != nil {
		t.Fatalf("RI-I02: an interrupted task carried a proof: %+v", interrupted.Completion)
	}

	// A fresh instance restores the interrupted task.
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RI-I02: restore: found=%v err=%v", found, err)
	}
	if restored.ID != interrupted.ID {
		t.Fatalf("RI-I02: restart changed identity: %q -> %q", interrupted.ID, restored.ID)
	}
	if restored.Status.IsTerminal() || restored.Proven() {
		t.Fatalf("RI-I02: restart published an unfinished task: status=%s objective=%s", restored.Status, restored.ObjectiveState())
	}
	if restored.Status != task.StatusPaused && restored.Status != task.StatusNeedsReview {
		t.Fatalf("RI-I02: restored status = %s, want a resumable non-terminal state", restored.Status)
	}

	// Resume through the real turn path. The persisted contract is reused, the
	// surviving change evidence is still current, and the new check follows it.
	remaining := []ai.AssistantMessage{
		readinessToolCall("write-2", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\n// GrepTool returns the current value.\nfunc GrepTool() string { return \"new\" }\n",
		}),
		readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
		readinessAnswer("the suite passes now"),
	}
	r2 := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, remaining)
	r2.turn("run the tests")
	completed := r2.currentTask()
	if completed.ID != interrupted.ID {
		t.Fatalf("RI-I02: resume replaced the task: %q -> %q", interrupted.ID, completed.ID)
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("RI-I02: resumed objective = %s, want PROVEN: %s (evidence=%+v results=%+v)",
			completed.ObjectiveState(), completed.CompletionSummary(), completed.Context.Evidence, r2.toolResults())
	}
	if completed.Completion == nil || len(completed.Completion.Verified) == 0 {
		t.Fatalf("RI-I02: proof = %+v, want the post-change verification", completed.Completion)
	}
}

// RI-I02b: after an interrupted mutation, a resumed turn that only verifies the
// durable change completes through the normal path. The persisted change is
// current evidence, and the new verification follows it, so the task layer's
// mutation proof rule is satisfied without redoing the change.
func TestReadinessI02bResumeWithVerificationOnlyCompletesInterruptedMutation(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	first := []ai.AssistantMessage{
		readinessToolCall("write-1", "write", map[string]any{
			"path":    "tools/grep.go",
			"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
		}),
		readinessAnswer("wrote the change"),
	}
	r := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, first)
	r.turn(objective)
	interrupted := r.currentTask()
	if interrupted.Proven() {
		t.Fatal("RI-I02b: fixture was already proven")
	}

	remaining := []ai.AssistantMessage{
		readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
		readinessAnswer("the suite passes now"),
	}
	r2 := newReadinessRuntime(t, root, workspace.Build,
		[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)}, remaining)
	r2.turn("run the tests")
	completed := r2.currentTask()
	if completed.ID != interrupted.ID {
		t.Fatalf("RI-I02b: resume replaced the task")
	}
	if completed.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("RI-I02b: a durable change followed by a current verification did not prove: objective=%s summary=%s reason=%q",
			completed.ObjectiveState(), completed.CompletionSummary(), completed.ReviewReason)
	}
}

// RC-C04: the real runtime admission hook refuses mutation under ASK and FUZZ.
// The file is the observable: it must not exist after a denied call.
func TestReadinessC04AskAndFuzzCannotMutateThroughRuntime(t *testing.T) {
	for _, role := range []workspace.Workspace{workspace.Ask, workspace.Fuzz} {
		t.Run(role.Label(), func(t *testing.T) {
			root := t.TempDir()
			messages := []ai.AssistantMessage{
				readinessToolCall("write-1", "write", map[string]any{
					"path":    "forbidden.txt",
					"content": "should not exist\n",
				}),
				readinessAnswer("could not write"),
			}
			r := newReadinessRuntime(t, root, role, []agent.Tool{tools.NewWriteTool(root)}, messages)
			r.turnAs("write forbidden.txt", role)
			if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); err == nil {
				t.Fatalf("RC-C04: %s mutated the workspace", role.Label())
			}
			results := r.toolResults()
			if len(results) == 0 {
				t.Fatalf("RC-C04: %s turn recorded no tool result", role.Label())
			}
			if !results[0].IsError {
				t.Fatalf("RC-C04: %s write result = %+v, want a denial", role.Label(), results[0])
			}
			text := strings.ToLower(results[0].Text())
			if !strings.Contains(text, "denied") &&
				!strings.Contains(text, "not permitted") &&
				!strings.Contains(text, "not offered") {
				t.Fatalf("RC-C04: %s denial text = %q", role.Label(), results[0].Text())
			}
		})
	}
	// BUILD is the control: the same call succeeds and changes the file.
	root := t.TempDir()
	messages := []ai.AssistantMessage{
		readinessToolCall("write-1", "write", map[string]any{"path": "allowed.txt", "content": "written\n"}),
		readinessAnswer("wrote it"),
	}
	r := newReadinessRuntime(t, root, workspace.Build, runtimeCaps(tools.NewWriteTool(root)), messages)
	r.turnAs("write allowed.txt", workspace.Build)
	data, err := os.ReadFile(filepath.Join(root, "allowed.txt"))
	if err != nil || string(data) != "written\n" {
		t.Fatalf("RC-C04: BUILD write = %q err=%v, want the written file", data, err)
	}
}

// RC-C05: a stale task role claim is refused, so it never becomes runtime
// authority. The model's write is admitted by the live gate, not by the task
// metadata.
func TestReadinessC05StaleTaskRoleDoesNotGrantRuntimeAuthority(t *testing.T) {
	root := t.TempDir()
	messages := []ai.AssistantMessage{
		readinessAnswer("started"),
		readinessToolCall("write-1", "write", map[string]any{"path": "stale.txt", "content": "x\n"}),
		readinessAnswer("no"),
	}
	r := newReadinessRuntime(t, root, workspace.Fuzz, []agent.Tool{tools.NewWriteTool(root)}, messages)
	r.turnAs("write stale.txt", workspace.Fuzz)
	started := r.currentTask()
	// A host that claims a role inconsistent with the live gate is refused:
	// the task keeps the role the authority actually held.
	if err := r.controller.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "claimed"); err == nil {
		t.Fatal("RC-C05: a stale role claim was recorded")
	}
	claimed := r.currentTask()
	if claimed.ActiveRole != workspace.Fuzz {
		t.Fatalf("RC-C05: metadata moved to %s, want the live FUZZ", claimed.ActiveRole)
	}
	if *r.role != workspace.Fuzz {
		t.Fatalf("RC-C05: the claim moved the live gate to %s", *r.role)
	}
	// The same objective continues under the live FUZZ gate, and the write is
	// denied by the actual authority.
	r.turnAs("write stale.txt", workspace.Fuzz)
	if _, err := os.Stat(filepath.Join(root, "stale.txt")); err == nil {
		t.Fatal("RC-C05: task metadata granted FUZZ the BUILD write authority")
	}
	if *r.role != workspace.Fuzz {
		t.Fatalf("RC-C05: the stale claim moved the live gate to %s", *r.role)
	}
	after := r.currentTask()
	if after.ID != started.ID {
		t.Fatalf("RC-C05: the turn replaced the task instead of continuing it")
	}
	if after.ActiveRole != workspace.Fuzz {
		t.Fatalf("RC-C05: the task role drifted from the live authority: %s", after.ActiveRole)
	}
}

// RI-I07: a genuinely different objective starts a new task without inheriting
// the previous task's facts or proof, and the old task remains resumable.
func TestReadinessI07DifferentObjectiveIdentityIsolation(t *testing.T) {
	root := t.TempDir()
	first := newReadinessRuntime(t, root, workspace.Ask, []agent.Tool{tools.NewReadTool(root)}, []ai.AssistantMessage{readinessAnswer("first answer")})
	first.turnAs("Fix GrepTool in tools/grep.go and run go test ./tools", workspace.Ask)
	taskA := first.currentTask()
	if taskA.Status.IsTerminal() {
		t.Fatalf("RI-I07: fixture task completed unexpectedly: %s", taskA.Status)
	}
	evidence, err := first.controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceSource, Source: "scheduler.go:1", Summary: "the queue is bounded",
		Outcome: task.EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RI-I07: evidence: %v", err)
	}
	if _, err := first.controller.RecordFact(task.FactInput{Text: "the queue is bounded", EvidenceIDs: []string{evidence.ID}}); err != nil {
		t.Fatalf("RI-I07: fact: %v", err)
	}
	if _, err := first.controller.UpdateRoleContext(workspace.Ask, task.RoleContextUpdate{
		Reasoning: []string{"task-a-only-marker"},
	}); err != nil {
		t.Fatalf("RI-I07: role marker: %v", err)
	}

	second := newReadinessRuntime(t, root, workspace.Ask, []agent.Tool{tools.NewReadTool(root)}, []ai.AssistantMessage{readinessAnswer("second answer")})
	second.turnAs("document the release process", workspace.Ask)
	taskB := second.currentTask()
	if taskB.ID == taskA.ID {
		t.Fatalf("RI-I07: a different objective reused task %q", taskA.ID)
	}
	if len(taskB.Context.Facts) != 0 || len(taskB.Context.Evidence) != 0 {
		t.Fatalf("RI-I07: task B inherited task A state: %+v", taskB.Context)
	}
	if taskB.Completion != nil && (taskB.Completion.Observed || taskB.Completion.Executed) {
		t.Fatalf("RI-I07: task B inherited task A's execution proof: %+v", taskB.Completion)
	}
	for _, role := range taskB.Context.Roles {
		for _, reasoning := range role.Reasoning {
			if reasoning == "task-a-only-marker" {
				t.Fatalf("RI-I07: task B inherited task A's ASK reasoning: %+v", role)
			}
		}
	}
	resumable, err := second.controller.ListResumable()
	if err != nil {
		t.Fatalf("RI-I07: list: %v", err)
	}
	found := false
	for _, candidate := range resumable {
		if candidate.ID == taskA.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("RI-I07: task A is not resumable: %+v", resumable)
	}
	restored, err := second.controller.ResumeTask(taskA.ID)
	if err != nil {
		t.Fatalf("RI-I07: resume A: %v", err)
	}
	if restored.ID != taskA.ID || len(restored.Context.Facts) != 1 {
		t.Fatalf("RI-I07: resumed A = %+v", restored)
	}
}
