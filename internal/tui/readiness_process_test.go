package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// This file proves that the full production turn entrypoint — not a recovery
// helper — produces durable state a different process can restore.

const (
	tuiReadinessScenarioEnv = "OHGO_TUI_READINESS_SCENARIO"
	tuiReadinessRootEnv     = "OHGO_TUI_READINESS_ROOT"
)

// TestReadinessTUIHelperProcess is inert unless the parent selects a scenario.
func TestReadinessTUIHelperProcess(t *testing.T) {
	scenario := os.Getenv(tuiReadinessScenarioEnv)
	if scenario == "" {
		t.Skip("readiness subprocess helper")
	}
	root := os.Getenv(tuiReadinessRootEnv)
	if root == "" {
		t.Fatal("tui readiness helper: missing root")
	}
	objective := "Fix GrepTool in tools/grep.go and run go test ./tools"
	switch scenario {
	case "build-turn-exit":
		messages := []ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
			}),
			readinessToolCall("check-1", "bash", map[string]any{"command": "go test ./tools"}),
			readinessAnswer("implemented and verified"),
		}
		r := newReadinessRuntime(t, root, workspace.Build,
			[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
			messages)
		r.turn(objective)
		if completed, ok := r.controller.Current(); !ok || !completed.Proven() {
			t.Fatalf("tui readiness helper: turn did not prove the objective (ok=%v)", ok)
		}
		os.Exit(0)
	case "interrupted-turn-exit":
		messages := []ai.AssistantMessage{
			readinessToolCall("write-1", "write", map[string]any{
				"path":    "tools/grep.go",
				"content": "package tools\n\nfunc GrepTool() string { return \"new\" }\n",
			}),
			readinessAnswer("wrote the change"),
		}
		r := newReadinessRuntime(t, root, workspace.Build,
			[]agent.Tool{tools.NewReadTool(root), tools.NewBashTool(root), tools.NewWriteTool(root)},
			messages)
		r.turn(objective)
		if completed, ok := r.controller.Current(); !ok || completed.Proven() {
			t.Fatalf("tui readiness helper: unverified turn was proven (ok=%v)", ok)
		}
		os.Exit(0)
	default:
		t.Fatalf("tui readiness helper: unknown scenario %q", scenario)
	}
}

func runTUIReadinessHelper(t *testing.T, scenario, root string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReadinessTUIHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		tuiReadinessScenarioEnv+"="+scenario,
		tuiReadinessRootEnv+"="+root,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("TUI READINESS PROCESS FAILURE in %s: %v\n%s", scenario, err, out)
	}
}

// RI-I01b: a full BUILD turn runs in one process, exits, and a fresh process
// restores the same task identity, evidence, and proof from durable state.
func TestReadinessI01bFullTurnSurvivesProcessRestart(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	runTUIReadinessHelper(t, "build-turn-exit", root)

	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RI-I01b: restore: found=%v err=%v", found, err)
	}
	if restored.ObjectiveState() != task.ObjectiveProven {
		t.Fatalf("RI-I01b: restored objective = %s, want PROVEN", restored.ObjectiveState())
	}
	if restored.Completion == nil || len(restored.Completion.EvidenceIDs) == 0 {
		t.Fatalf("RI-I01b: restored proof = %+v", restored.Completion)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "tools", "grep.go"))
	if readErr != nil || !strings.Contains(string(data), "new") {
		t.Fatalf("RI-I01b: filesystem change was not durable: %q err=%v", data, readErr)
	}
}

// RI-I02c: an interrupted full turn in one process leaves a resumable task in
// the next process, not a completed one.
func TestReadinessI02cInterruptedFullTurnSurvivesProcessRestart(t *testing.T) {
	root := t.TempDir()
	writeReadinessModule(t, root)
	runTUIReadinessHelper(t, "interrupted-turn-exit", root)

	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RI-I02c: restore: found=%v err=%v", found, err)
	}
	if restored.Status.IsTerminal() || restored.Proven() {
		t.Fatalf("RI-I02c: restart published an unfinished task: status=%s objective=%s", restored.Status, restored.ObjectiveState())
	}
	if restored.Completion != nil {
		t.Fatalf("RI-I02c: restart carried a proof for an unfinished task: %+v", restored.Completion)
	}
	if restored.Contract == nil {
		t.Fatal("RI-I02c: restored task has no persisted contract")
	}
	if _, err := reopened.Begin(*restored.Contract, restored.Snapshot); err != nil {
		t.Fatalf("RI-I02c: the interrupted task was not resumable: %v", err)
	}
}
