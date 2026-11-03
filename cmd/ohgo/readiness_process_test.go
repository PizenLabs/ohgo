package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// Level C of the Task Continuity product-readiness suite: durable state crosses
// a real process boundary. The helper process runs the production composition
// (installTaskContinuity, the authority gate, the session store) and exits at a
// controlled point; the parent starts a fresh process and judges the restored
// state. Test IDs match docs/testing/TASK_CONTINUITY_TEST_CATALOG.md.

const (
	readinessScenarioEnv = "OHGO_READINESS_SCENARIO"
	readinessRootEnv     = "OHGO_READINESS_ROOT"
	readinessSessionEnv  = "OHGO_READINESS_SESSIONS"
)

// TestReadinessHelperProcess is the child side. It is inert unless the parent
// sets the scenario variable, so a normal `go test` invocation skips it.
func TestReadinessHelperProcess(t *testing.T) {
	scenario := os.Getenv(readinessScenarioEnv)
	if scenario == "" {
		t.Skip("readiness subprocess helper")
	}
	root := os.Getenv(readinessRootEnv)
	if root == "" {
		t.Fatal("readiness helper: missing root")
	}
	dbPath := os.Getenv(readinessSessionEnv)
	if dbPath == "" {
		t.Fatal("readiness helper: missing session path")
	}
	role := workspace.Fuzz
	store, sess := readinessStartup(t, root, dbPath, role)
	gate := newAuthorityGate(role)
	held := newHarness(nil, nil, store, sess, &agent.UsageLog{}, nil, root, ai.Model{ID: "m", Provider: "p"})
	held.gate = gate
	held.workspace = role.Label()
	if err := os.WriteFile(filepath.Join(root, "session-id"), []byte(strconv.FormatInt(sess.ID, 10)), 0o644); err != nil {
		t.Fatalf("readiness helper: write session id: %v", err)
	}

	controller := installTaskContinuity(tui.NewProgram(nil, "test", 0), root,
		[]agent.Tool{tools.NewTodoTool()}, func() workspace.Workspace { return gate.Workspace() })

	switch scenario {
	case "handoff-switch-before-applied":
		started := readinessBeginHandoffTask(t, controller, root)
		handoff, err := controller.ProposeHandoff(task.HandoffInput{To: workspace.Build, Reason: "needs mutation"})
		if err != nil {
			t.Fatalf("readiness helper: propose: %v", err)
		}
		if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
			t.Fatalf("readiness helper: approve: %v", err)
		}
		// The authority owner performs the switch and persists the session. The
		// process dies before the task records APPLIED.
		if _, err := held.SwitchWorkspace(workspace.Build.String()); err != nil {
			t.Fatalf("readiness helper: switch: %v", err)
		}
		_ = started
		os.Exit(0)
	case "handoff-approval-before-switch":
		readinessBeginHandoffTask(t, controller, root)
		handoff, err := controller.ProposeHandoff(task.HandoffInput{To: workspace.Build, Reason: "needs mutation"})
		if err != nil {
			t.Fatalf("readiness helper: propose: %v", err)
		}
		if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
			t.Fatalf("readiness helper: approve: %v", err)
		}
		// The process dies after approval and before the authority switch.
		os.Exit(0)
	case "clean-completion-exit":
		mutation, err := controller.Begin(contract.IntentContract{
			Objective: "fix tools/grep.go", Kind: contract.ObjectiveMutation,
			Targets:      []contract.Target{{Kind: contract.TargetPath, Value: "tools/grep.go"}},
			Verification: []contract.VerificationRequirement{{Check: contract.CheckTest, Scope: "tools"}},
		}, nil)
		if err != nil {
			t.Fatalf("readiness helper: mutation Begin: %v", err)
		}
		readinessWriteCompletionEvidence(t, controller)
		completed, err := controller.CompleteWithEvidence(task.CompletionEvidence{Complete: true})
		if err != nil {
			t.Fatalf("readiness helper: complete: %v", err)
		}
		if !completed.Proven() {
			t.Fatalf("readiness helper: objective = %s, want PROVEN", completed.ObjectiveState())
		}
		readinessWriteTaskID(t, root, completed.ID)
		_ = mutation
		os.Exit(0)
	case "failed-persist-exit":
		mutation, err := controller.Begin(contract.IntentContract{
			Objective: "fix tools/grep.go", Kind: contract.ObjectiveMutation,
			Targets:      []contract.Target{{Kind: contract.TargetPath, Value: "tools/grep.go"}},
			Verification: []contract.VerificationRequirement{{Check: contract.CheckTest, Scope: "tools"}},
		}, nil)
		if err != nil {
			t.Fatalf("readiness helper: mutation Begin: %v", err)
		}
		readinessWriteTaskID(t, root, mutation.ID)
		readinessWriteCompletionEvidence(t, controller)
		dir := filepath.Join(root, ".ohgo")
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("readiness helper: chmod: %v", err)
		}
		_, completeErr := controller.CompleteWithEvidence(task.CompletionEvidence{Complete: true})
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("readiness helper: restore chmod: %v", err)
		}
		if completeErr == nil {
			t.Fatal("readiness helper: completion succeeded despite a read-only task directory")
		}
		if err := os.WriteFile(filepath.Join(root, "persist-failed"), []byte("1"), 0o644); err != nil {
			t.Fatalf("readiness helper: write marker: %v", err)
		}
		_ = mutation
		os.Exit(0)
	default:
		t.Fatalf("readiness helper: unknown scenario %q", scenario)
	}
}

func readinessBeginHandoffTask(t *testing.T, controller *task.Controller, root string) task.Task {
	t.Helper()
	started, err := controller.Begin(contract.IntentContract{
		Objective: "fix the parser", Kind: contract.ObjectiveInformational,
	}, nil)
	if err != nil {
		t.Fatalf("readiness helper: Begin: %v", err)
	}
	readinessWriteTaskID(t, root, started.ID)
	return started
}

func readinessWriteTaskID(t *testing.T, root, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "task-id"), []byte(id), 0o644); err != nil {
		t.Fatalf("readiness helper: write task id: %v", err)
	}
}

func readinessWriteCompletionEvidence(t *testing.T, controller *task.Controller) {
	t.Helper()
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceArtifact, Source: "write:tools/grep.go",
		Summary: "changed tools/grep.go", Outcome: task.EvidenceObserved,
	}); err != nil {
		t.Fatalf("readiness helper: change evidence: %v", err)
	}
	if _, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceTest, Source: "check:test", Scope: "tools",
		Summary: "the tools suite passed", Outcome: task.EvidenceObserved,
	}); err != nil {
		t.Fatalf("readiness helper: verification evidence: %v", err)
	}
}

func readinessStartup(t *testing.T, root, dbPath string, role workspace.Workspace) (*session.Store, *session.Session) {
	t.Helper()
	store, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("readiness: open session store: %v", err)
	}
	sess, err := store.CreateWith(session.CreateOptions{
		Cwd: root, Model: "p/m", Workspace: role.String(),
	})
	if err != nil {
		t.Fatalf("readiness: create session: %v", err)
	}
	return store, &sess
}

func runReadinessHelper(t *testing.T, scenario, root, dbPath string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReadinessHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		readinessScenarioEnv+"="+scenario,
		readinessRootEnv+"="+root,
		readinessSessionEnv+"="+dbPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("READINESS PROCESS FAILURE in %s: %v\n%s", scenario, err, out)
	}
}

func readReadinessFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("readiness: read %s: %v", name, err)
	}
	return strings.TrimSpace(string(data))
}

func restoredSessionRole(t *testing.T, dbPath string) workspace.Workspace {
	t.Helper()
	store, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("readiness: reopen session store: %v", err)
	}
	defer func() { _ = store.Close() }()
	// The helper process created exactly one session and exited; the list is
	// the durable authority record a restart would read.
	list, err := store.List(10)
	if err != nil || len(list) == 0 {
		t.Fatalf("readiness: list sessions: n=%d err=%v", len(list), err)
	}
	role, err := workspace.Parse(list[0].Workspace)
	if err != nil {
		t.Fatalf("readiness: restored session workspace %q: %v", list[0].Workspace, err)
	}
	return role
}

// RA-A05: task identity is independent of session identity. Opening a second
// session in the same project does not rename, replace, or lose the task.
func TestReadinessA05TaskIdentityOutlivesSessionIdentity(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "sessions.db")
	store, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("RA-A05: open session store: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.CreateWith(session.CreateOptions{Cwd: root, Model: "p/m", Workspace: "build"})
	if err != nil {
		t.Fatalf("RA-A05: first session: %v", err)
	}
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	created, err := controller.Begin(contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational}, nil)
	if err != nil {
		t.Fatalf("RA-A05: Begin: %v", err)
	}
	second, err := store.CreateWith(session.CreateOptions{Cwd: root, Model: "p/m", Workspace: "ask"})
	if err != nil {
		t.Fatalf("RA-A05: second session: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("RA-A05: fixture created the same session twice")
	}
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Ask })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RA-A05: restore under a new session: found=%v err=%v", found, err)
	}
	if restored.ID != created.ID {
		t.Fatalf("RA-A05: the session boundary changed task identity: %q -> %q", created.ID, restored.ID)
	}
}

// and the process died before APPLIED was durably recorded. A fresh process
// restores the session authority, recognizes the live target, records APPLIED
// without another switch, preserves identity, and does not duplicate history.
// RD-D02: the previously corrected crash window. The authority switch succeeded
// and the process died before APPLIED was durably recorded. A fresh process
// restores the session authority, recognizes the live target, records APPLIED
// without another switch, preserves identity, and does not duplicate history.
func TestReadinessD02CrashAfterSwitchBeforeApplied(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "sessions.db")
	runReadinessHelper(t, "handoff-switch-before-applied", root, dbPath)
	taskID := readReadinessFile(t, root, "task-id")
	role := restoredSessionRole(t, dbPath)
	if role != workspace.Build {
		t.Fatalf("RD-D02: restored session authority = %s, want BUILD", role)
	}

	got := installTaskContinuity(tui.NewProgram(nil, "test", 0), root,
		[]agent.Tool{tools.NewTodoTool()}, func() workspace.Workspace { return role })
	current, ok := got.Current()
	if !ok {
		t.Fatal("RD-D02: the fresh process lost the task")
	}
	if current.ID != taskID {
		t.Fatalf("RD-D02: restart changed identity: %q -> %q", taskID, current.ID)
	}
	if current.Handoff == nil || current.Handoff.Status != task.HandoffApplied || !current.Handoff.Consumed {
		t.Fatalf("RD-D02: handoff = %+v, want APPLIED and consumed", current.Handoff)
	}
	if current.ActiveRole != workspace.Build {
		t.Fatalf("RD-D02: active role = %s, want BUILD", current.ActiveRole)
	}
	applied := 0
	for _, event := range current.Timeline {
		if event.Kind == task.TimelineHandoff && event.Status == string(task.HandoffApplied) {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("RD-D02: %d APPLIED events, want exactly one reconciliation", applied)
	}

	// The session authority the next turn will use is still BUILD. Recovery
	// recorded the transition; it did not perform another switch.
	if restored := restoredSessionRole(t, dbPath); restored != workspace.Build {
		t.Fatalf("RD-D02: recovery changed the session authority to %s", restored)
	}
}

// RD-D03: a crash after approval and before the authority switch leaves the
// source role in force, does not apply the handoff, and stays resumable.
func TestReadinessD03CrashAfterApprovalBeforeSwitch(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "sessions.db")
	runReadinessHelper(t, "handoff-approval-before-switch", root, dbPath)
	taskID := readReadinessFile(t, root, "task-id")
	role := restoredSessionRole(t, dbPath)
	if role != workspace.Fuzz {
		t.Fatalf("RD-D03: restored session authority = %s, want FUZZ", role)
	}
	got := installTaskContinuity(tui.NewProgram(nil, "test", 0), root,
		[]agent.Tool{tools.NewTodoTool()}, func() workspace.Workspace { return role })
	current, ok := got.Current()
	if !ok || current.ID != taskID {
		t.Fatalf("RD-D03: task not restored: ok=%v id=%q", ok, current.ID)
	}
	if current.Handoff == nil || current.Handoff.Status != task.HandoffInterrupted {
		t.Fatalf("RD-D03: handoff = %+v, want INTERRUPTED", current.Handoff)
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("RD-D03: active role = %s, want the source FUZZ", current.ActiveRole)
	}
	if !strings.Contains(current.Handoff.Failure, "still FUZZ") {
		t.Fatalf("RD-D03: failure = %q, want the observed source workspace", current.Handoff.Failure)
	}
	if _, err := got.Begin(contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational}, nil); err != nil {
		t.Fatalf("RD-D03: interrupted task is not resumable: %v", err)
	}
}

// RE-E05: a completed checkpoint written by one process is restored by a
// different process with the same identity and a still-valid proof.
func TestReadinessE05CleanRestartRestoresProvenCompletion(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "sessions.db")
	runReadinessHelper(t, "clean-completion-exit", root, dbPath)
	taskID := readReadinessFile(t, root, "task-id")
	role := restoredSessionRole(t, dbPath)
	if role != workspace.Fuzz {
		t.Fatalf("RE-E05: fixture session role = %s", role)
	}
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RE-E05: restore: found=%v err=%v", found, err)
	}
	if restored.ID != taskID {
		t.Fatalf("RE-E05: restart changed identity: %q -> %q", taskID, restored.ID)
	}
	if restored.Status != task.StatusCompleted {
		t.Fatalf("RE-E05: status = %s, want COMPLETED", restored.Status)
	}
	if restored.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("RE-E05: objective = %s, want PROVEN", restored.ObjectiveState())
	}
	if restored.Completion == nil || len(restored.Completion.EvidenceIDs) == 0 {
		t.Fatalf("RE-E05: proof lost across the process boundary: %+v", restored.Completion)
	}
}

// RE-E06: a failed completion checkpoint in one process leaves the durable task
// resumable and not completed in the next process.
func TestReadinessE06FailedPersistenceAcrossProcess(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "sessions.db")
	runReadinessHelper(t, "failed-persist-exit", root, dbPath)
	if _, err := os.Stat(filepath.Join(root, "persist-failed")); err != nil {
		t.Skip("the filesystem permitted the write; the permission fault could not be injected")
	}
	taskID := readReadinessFile(t, root, "task-id")
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RE-E06: restore: found=%v err=%v", found, err)
	}
	if restored.ID != taskID {
		t.Fatalf("RE-E06: restart changed identity")
	}
	if restored.Status == task.StatusCompleted || restored.Proven() || restored.Completion != nil {
		t.Fatalf("RE-E06: the failed completion was published: status=%s objective=%s proof=%+v",
			restored.Status, restored.ObjectiveState(), restored.Completion)
	}
	if _, err := reopened.Begin(contract.IntentContract{
		Objective: "fix tools/grep.go", Kind: contract.ObjectiveMutation,
		Targets:      []contract.Target{{Kind: contract.TargetPath, Value: "tools/grep.go"}},
		Verification: []contract.VerificationRequirement{{Check: contract.CheckTest, Scope: "tools"}},
	}, nil); err != nil {
		t.Fatalf("RE-E06: the task was not resumable after the failed commit: %v", err)
	}
}
