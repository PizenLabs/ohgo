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
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A widening request is proposed, approved, and applied through the real turn
// entrypoint. The task keeps its identity, both role contexts survive, and the
// authority moves only after the human accepts.
func TestHandoffThroughTurnEntrypointPreservesTaskAndAuthority(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	var (
		mu       sync.Mutex
		requests int
	)
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 1 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "f1", Name: "bash", Arguments: map[string]any{"command": "inspect the latest note.txt"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []agent.Tool{&runtimeBashTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:          func() string { return string(role) },
		Switch:           switchWorkspaceForTest(&role, controller),
		SwitchWithReason: switchWorkspaceWithReasonForTest(&role, controller),
		Predict:          predictTransition(func() workspace.Workspace { return role }),
	})
	program.SetTaskHooks(runtimeHooks(controller))

	objective := "fix the flaky test in note.txt"
	program.runTurn(objective)
	first, ok := controller.Current()
	if !ok {
		t.Fatal("the FUZZ turn recorded no task")
	}
	if first.Status.IsTerminal() {
		t.Fatalf("the FUZZ turn completed unexpectedly: %s", first.Status)
	}
	if first.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want FUZZ", first.ActiveRole)
	}
	if _, ok := first.Context.Role(workspace.Fuzz); !ok {
		t.Fatal("the FUZZ context was not populated")
	}
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceReproduction, Role: workspace.Fuzz,
		Source: "go test ./... --count=10", Summary: "the failure reproduces twice",
		Outcome: task.EvidenceObserved,
	}); err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Fuzz, task.RoleContextUpdate{
		Strategy:  "reproduce under load",
		Artifacts: []string{"artifact:repro"},
	}); err != nil {
		t.Fatalf("FUZZ context: %v", err)
	}

	// FUZZ -> BUILD is cooperative execution-workspace movement: the control
	// plane applies it automatically, without a human decision, and records it
	// on the same task.
	cmd := program.submit("implement the fix")
	if cmd == nil {
		t.Fatal("the automatic FUZZ -> BUILD handoff did not start the turn")
	}
	if program.state.Transition != nil {
		t.Fatalf("automatic handoff opened a decision surface: %+v", program.state.Transition)
	}
	if role != workspace.Build {
		t.Fatalf("automatic handoff left the role at %s", role)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("FUZZ mutation authority was granted by leaving FUZZ")
	}
	cmd()

	after, ok := controller.Current()
	if !ok {
		t.Fatal("no task after the handoff")
	}
	if after.ID != first.ID {
		t.Fatalf("the handoff recreated the task: %q -> %q", first.ID, after.ID)
	}
	if after.ActiveRole != workspace.Build {
		t.Fatalf("active role = %s, want BUILD", after.ActiveRole)
	}
	if after.Handoff == nil || after.Handoff.Status != task.HandoffApplied {
		t.Fatalf("handoff = %+v, want APPLIED", after.Handoff)
	}
	if after.Handoff.From != workspace.Fuzz || after.Handoff.To != workspace.Build {
		t.Fatalf("applied handoff roles = %s -> %s", after.Handoff.From, after.Handoff.To)
	}
	fuzz, ok := after.Context.Role(workspace.Fuzz)
	if !ok || fuzz.Strategy != "reproduce under load" {
		t.Fatalf("FUZZ context did not survive: %+v", fuzz)
	}
	build, ok := after.Context.Role(workspace.Build)
	if !ok || !hasSubstring(build.Progress, "FUZZ") {
		t.Fatalf("BUILD context = %+v, want the transition summary", build)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("the applied handoff granted FUZZ mutation authority")
	}

	// A genuinely new objective still detaches, preserving the old task.
	program.runTurn("summarize the README")
	third, ok := controller.Current()
	if !ok {
		t.Fatal("no task after the new objective")
	}
	if third.ID == first.ID {
		t.Fatal("a new objective reused the handed-off task")
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	found := false
	for _, candidate := range resumable {
		if candidate.ID == first.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the handed-off task was not preserved as resumable")
	}
}

// Declining an ASK-boundary handoff leaves authority, task identity, and
// context untouched. The request is restored, never run under FUZZ just because
// the user declined the read-only workspace.
func TestDeclinedAskHandoffLeavesAuthorityAndTaskUnchanged(t *testing.T) {
	role := workspace.Fuzz
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	program := NewProgram(scriptedLoop(t), "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:          func() string { return string(role) },
		Switch:           switchWorkspaceForTest(&role, controller),
		SwitchWithReason: switchWorkspaceWithReasonForTest(&role, controller),
		Predict:          predictTransition(func() workspace.Workspace { return role }),
	})
	program.SetTaskHooks(runtimeHooks(controller))

	program.runTurn("fix the flaky test")
	first, _ := controller.Current()
	program.state.Apply(InputChanged{Text: "why does this work?"})
	program.submit("why does this work?")
	if program.state.Transition == nil {
		t.Fatal("no ASK handoff proposal was shown")
	}
	program.resolveTransition(false)
	if role != workspace.Fuzz {
		t.Fatalf("declining moved authority to %s", role)
	}
	after, _ := controller.Current()
	if after.ID != first.ID {
		t.Fatalf("declining changed the task identity: %q -> %q", first.ID, after.ID)
	}
	if after.Handoff != nil {
		t.Fatalf("declining recorded a handoff: %+v", after.Handoff)
	}
	if !strings.Contains(program.state.Input, "why does this work?") {
		t.Fatalf("declining lost the request: %q", program.state.Input)
	}
}

// predictTransition mirrors the host's transition policy for the TUI tests:
// the policy stays in workspace, and only the projection is local. It covers
// both directions, so a narrowing proposal and a widening handoff reach the
// same decision surface.
func predictTransition(active func() workspace.Workspace) func(string) (TurnTransition, bool) {
	return func(text string) (TurnTransition, bool) {
		current := active()
		if !current.Valid() {
			return TurnTransition{}, false
		}
		requirement := workspace.AnalyzeRequest(text)
		decision := workspace.DecideTransition(current, requirement)
		if !decision.Proposed() {
			return TurnTransition{}, false
		}
		from, to := decision.Current.Style(), decision.Target.Style()
		kind := TransitionKindWorkspace
		if decision.NeedsConfirmation() && !current.Encompasses(decision.Target) {
			kind = TransitionKindHandoff
		}
		return TurnTransition{
			Kind:           kind,
			Current:        from.Label,
			Proposed:       to.Label,
			CurrentAccent:  from.Accent,
			ProposedAccent: to.Accent,
			Intent:         strings.ToUpper(string(requirement.Intent)),
			Strength:       string(requirement.Strength),
			Question:       decision.Question,
			Reason:         decision.Reason,
			Automatic:      decision.Automatic(),
		}, true
	}
}

func hasSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

// The reverse direction goes through the same surface: BUILD -> FUZZ keeps the
// implementation context, the task identity, and FUZZ's read/execute-only
// authority.
func TestBuildToFuzzTransitionPreservesImplementationContext(t *testing.T) {
	role := workspace.Build
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	request := 0
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		request++
		if request == 1 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "b1", Name: "write", Arguments: map[string]any{"path": "note.txt"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []agent.Tool{&runtimeWriteTool{path: "note.txt"}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:          func() string { return string(role) },
		Switch:           switchWorkspaceForTest(&role, controller),
		SwitchWithReason: switchWorkspaceWithReasonForTest(&role, controller),
		Predict:          predictTransition(func() workspace.Workspace { return role }),
	})
	program.SetTaskHooks(runtimeHooks(controller))

	program.runTurn("implement the fix in note.txt")
	first, ok := controller.Current()
	if !ok || first.Status.IsTerminal() {
		t.Fatalf("BUILD task = %+v, want a non-terminal task", first)
	}
	build, ok := first.Context.Role(workspace.Build)
	if !ok || len(build.Progress) == 0 {
		t.Fatalf("BUILD context = %+v, want implementation progress", build)
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, task.RoleContextUpdate{
		Plan: []string{"build-plan-marker"},
	}); err != nil {
		t.Fatalf("BUILD plan: %v", err)
	}

	cmd := program.submit("reproduce it")
	if cmd == nil {
		t.Fatal("the automatic BUILD -> FUZZ handoff did not start the turn")
	}
	if program.state.Transition != nil {
		t.Fatalf("automatic handoff opened a decision surface: %+v", program.state.Transition)
	}
	if role != workspace.Fuzz {
		t.Fatalf("automatic transition left the role at %s", role)
	}
	cmd()

	after, ok := controller.Current()
	if !ok {
		t.Fatal("no task after the transition")
	}
	if after.ID != first.ID {
		t.Fatalf("BUILD -> FUZZ recreated the task: %q -> %q", first.ID, after.ID)
	}
	if after.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want FUZZ", after.ActiveRole)
	}
	if after.Handoff == nil || after.Handoff.Status != task.HandoffApplied ||
		after.Handoff.From != workspace.Build || after.Handoff.To != workspace.Fuzz {
		t.Fatalf("handoff = %+v", after.Handoff)
	}
	buildAfter, ok := after.Context.Role(workspace.Build)
	if !ok || !hasSubstring(buildAfter.Plan, "build-plan-marker") {
		t.Fatalf("BUILD context was lost: %+v", buildAfter)
	}
	if _, ok := after.Context.Role(workspace.Fuzz); !ok {
		t.Fatal("FUZZ did not receive a role context")
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("BUILD -> FUZZ granted mutation authority")
	}
}

// An interrupted handoff is visible in the task briefing after a restart.
func TestInterruptedHandoffIsVisibleAfterRestart(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	program := NewProgram(scriptedLoop(t), "test", 0)
	program.SetTaskHooks(runtimeHooks(controller))
	program.runTurn("fix the flaky test")

	handoff, err := controller.ProposeHandoff(task.HandoffInput{To: workspace.Build, Reason: "needs mutation"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}

	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Fuzz })
	if _, found, err := reopened.Restore(); err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	// The data layer leaves an approved record undecided; the application
	// boundary reconciles it with the restored workspace.
	if _, err := reopened.ReconcileRecoveredHandoff(workspace.Fuzz); err != nil {
		t.Fatalf("ReconcileRecoveredHandoff: %v", err)
	}
	restored, ok := reopened.Current()
	if !ok {
		t.Fatal("recovery lost the task")
	}
	brief := strings.Join(task.ContextFor(restored, workspace.Fuzz, ""), "\n")
	if !strings.Contains(brief, "INTERRUPTED") {
		t.Fatalf("interrupted handoff is not visible:\n%s", brief)
	}
	if restored.ActiveRole != workspace.Fuzz {
		t.Fatalf("interrupted handoff moved the role to %s", restored.ActiveRole)
	}
}

// Repeated transitions preserve both roles' contexts, keep one task identity,
// and never duplicate a role record. A dirty workspace is not touched by the
// authority move.
func TestRepeatedHandoffsPreserveContextsAndWorkspace(t *testing.T) {
	root := t.TempDir()
	dirty := filepath.Join(root, "note.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted work"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	role := workspace.Fuzz
	controller := task.NewController(task.NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	program := NewProgram(scriptedLoop(t), "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:          func() string { return string(role) },
		Switch:           switchWorkspaceForTest(&role, controller),
		SwitchWithReason: switchWorkspaceWithReasonForTest(&role, controller),
		Predict:          predictTransition(func() workspace.Workspace { return role }),
	})
	program.SetTaskHooks(runtimeHooks(controller))
	program.state.Apply(IdentityUpdated{GitBranch: "main", GitStatus: "3 modified"})

	program.runTurn("fix the flaky test")
	first, ok := controller.Current()
	if !ok || first.Status.IsTerminal() {
		t.Fatalf("initial task = %+v", first)
	}
	if _, err := controller.UpdateRoleContext(workspace.Fuzz, task.RoleContextUpdate{
		Strategy: "fuzz-strategy-marker",
	}); err != nil {
		t.Fatalf("FUZZ strategy: %v", err)
	}

	// FUZZ -> BUILD is automatic.
	cmd := program.submit("implement the fix")
	if cmd == nil {
		t.Fatal("the automatic FUZZ -> BUILD handoff did not start the turn")
	}
	if program.state.Transition != nil {
		t.Fatalf("automatic handoff opened a decision surface: %+v", program.state.Transition)
	}
	cmd()
	buildTask, _ := controller.Current()
	if buildTask.Handoff == nil || buildTask.Handoff.Status != task.HandoffApplied {
		t.Fatalf("first handoff = %+v", buildTask.Handoff)
	}
	if buildTask.Handoff.Workspace != "main; 3 modified" {
		t.Fatalf("recorded workspace = %q, want the measured footer state", buildTask.Handoff.Workspace)
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, task.RoleContextUpdate{
		Plan: []string{"build-plan-marker"},
	}); err != nil {
		t.Fatalf("BUILD plan: %v", err)
	}

	// BUILD -> FUZZ is automatic too.
	cmd = program.submit("reproduce it")
	if cmd == nil {
		t.Fatal("the automatic BUILD -> FUZZ handoff did not start the turn")
	}
	if program.state.Transition != nil {
		t.Fatalf("automatic handoff opened a decision surface: %+v", program.state.Transition)
	}
	cmd()
	after, _ := controller.Current()
	if after.ID != first.ID {
		t.Fatalf("repeated handoffs recreated the task: %q -> %q", first.ID, after.ID)
	}
	if after.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want FUZZ", after.ActiveRole)
	}
	fuzz, ok := after.Context.Role(workspace.Fuzz)
	if !ok || fuzz.Strategy != "fuzz-strategy-marker" {
		t.Fatalf("FUZZ context = %+v", fuzz)
	}
	build, ok := after.Context.Role(workspace.Build)
	if !ok || !hasSubstring(build.Plan, "build-plan-marker") {
		t.Fatalf("BUILD context = %+v", build)
	}
	seen := map[workspace.Workspace]bool{}
	for _, roleCtx := range after.Context.Roles {
		if seen[roleCtx.Role] {
			t.Fatalf("duplicate role context after repeated handoffs: %s", roleCtx.Role)
		}
		seen[roleCtx.Role] = true
	}
	if got, err := os.ReadFile(dirty); err != nil || string(got) != "uncommitted work" {
		t.Fatalf("a role transition discarded dirty work: %q err=%v", got, err)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("repeated transitions granted FUZZ mutation authority")
	}
}

// Only an unconsumed applied handoff continues the next turn; once consumed,
// the ordinary continuation policy resumes.
func TestHandoffContinuationConsumesAppliedTransitionOnce(t *testing.T) {
	intent := contract.IntentContract{Objective: "fix the flaky test", Kind: contract.ObjectiveMutation}
	current := TaskResume{
		HandoffStatus: "APPLIED", HandoffTo: "build", HandoffConsumed: false,
		HandoffBy: "human", HasContract: true, Contract: intent,
	}
	if !handoffContinuation(current, "build", "fix the flaky test", intent) {
		t.Fatal("an unconsumed applied human handoff did not continue the next turn")
	}
	consumed := current
	consumed.HandoffConsumed = true
	if handoffContinuation(consumed, "build", "fix the flaky test", intent) {
		t.Fatal("a consumed handoff still continued a later turn")
	}
	approved := current
	approved.HandoffStatus = "APPROVED"
	if !handoffContinuation(approved, "build", "fix the flaky test", intent) {
		t.Fatal("an approved handoff did not continue the next turn")
	}
	if handoffContinuation(approved, "fuzz", "fix the flaky test", intent) {
		t.Fatal("a handoff continued a different role")
	}

	// An automatic handoff does not make an unrelated objective a continuation.
	automatic := current
	automatic.HandoffBy = "control-plane"
	unrelated := contract.IntentContract{Objective: "write a README", Kind: contract.ObjectiveMutation}
	if handoffContinuation(automatic, "build", "write a README", unrelated) {
		t.Fatal("an automatic handoff attached an unrelated objective")
	}
	followUp := contract.IntentContract{Objective: "fix it", Kind: contract.ObjectiveMutation}
	if !handoffContinuation(automatic, "build", "fix it", followUp) {
		t.Fatal("an automatic handoff did not attach a task follow-up")
	}
}

// A restored BUILD workspace with stale FUZZ task metadata must still record a
// valid BUILD -> FUZZ narrowing. The source comes from the live authority, so
// the transition is neither refused as "already active" nor mislabelled.
func TestNarrowingUsesLiveAuthorityAfterStaleRecovery(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := contract.IntentContract{Objective: "fix the flaky test", Kind: contract.ObjectiveMutation}
	started, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Restart with the live authority at BUILD while the task still records
	// FUZZ. This is the stale metadata F2 describes.
	role = workspace.Build
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ActiveRole != workspace.Fuzz {
		t.Fatalf("precondition: recorded role = %s, want the stale FUZZ", restored.ActiveRole)
	}

	program := NewProgram(scriptedLoop(t), "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{
		Current:          func() string { return string(role) },
		Switch:           switchWorkspaceForTest(&role, reopened),
		SwitchWithReason: switchWorkspaceWithReasonForTest(&role, reopened),
		Predict:          predictTransition(func() workspace.Workspace { return role }),
	})
	program.SetTaskHooks(runtimeHooks(reopened))

	cmd := program.submit("reproduce it")
	if cmd == nil {
		t.Fatal("the automatic BUILD -> FUZZ handoff did not start the turn")
	}
	if program.state.Transition != nil {
		t.Fatalf("automatic handoff opened a decision surface: %+v", program.state.Transition)
	}
	if role != workspace.Fuzz {
		t.Fatalf("automatic narrowing left the role at %s", role)
	}
	cmd()

	current, ok := reopened.Current()
	if !ok {
		t.Fatal("no task after the narrowing")
	}
	if current.ID != started.ID {
		t.Fatalf("the narrowing recreated the task: %q -> %q", started.ID, current.ID)
	}
	if current.Handoff == nil || current.Handoff.From != workspace.Build || current.Handoff.To != workspace.Fuzz {
		t.Fatalf("handoff = %+v, want BUILD -> FUZZ", current.Handoff)
	}
	if current.Handoff.Status != task.HandoffApplied {
		t.Fatalf("handoff status = %s, want APPLIED", current.Handoff.Status)
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("active role = %s, want FUZZ", current.ActiveRole)
	}
}

// switchWorkspaceForTest moves the live role and records the applied switch,
// matching the production workspace owner.
func switchWorkspaceForTest(role *workspace.Workspace, controller *task.Controller) func(string) error {
	return func(name string) error {
		ws, err := workspace.Parse(name)
		if err != nil {
			return err
		}
		from := *role
		*role = ws
		if controller != nil {
			if err := controller.RecordWorkspaceSwitch(from, ws, ""); err != nil {
				return err
			}
		}
		return nil
	}
}

// switchWorkspaceWithReasonForTest mirrors the automatic control-plane seam:
// it records the measured state and the control-plane provenance.
func switchWorkspaceWithReasonForTest(role *workspace.Workspace, controller *task.Controller) func(string, string, string) error {
	return func(name, reason, workspaceState string) error {
		ws, err := workspace.Parse(name)
		if err != nil {
			return err
		}
		from := *role
		*role = ws
		if controller != nil {
			if err := controller.RecordAutomaticSwitch(from, ws, reason, workspaceState); err != nil {
				return err
			}
		}
		return nil
	}
}
