package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

func testSwitchHarness(t *testing.T) *harness {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model: catalog.Default(),
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		},
		Tools: []agent.Tool{testIdleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	settings := config.Default()
	return newHarness(ai.NewRegistry(), loop, nil, nil, &agent.UsageLog{}, &settings, t.TempDir(), catalog.Default())
}

type testIdleTool struct{}

func (testIdleTool) Name() string                   { return "idle" }
func (testIdleTool) Description() string            { return "idle" }
func (testIdleTool) Parameters() *jsonschema.Schema { return nil }

func (testIdleTool) Approval() agent.Approval      { return agent.Approval{} }
func (testIdleTool) Exclusive(map[string]any) bool { return false }
func (testIdleTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}

// A workspace switch is a persisted state change: the session row must describe
// the authority and model that will actually execute, or a later --resume
// restores a workspace the session never ran under.
func TestSwitchWorkspaceRecordsTheSessionAuthority(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	sess, err := store.CreateWith(session.CreateOptions{
		Cwd: t.TempDir(), Model: "anthropic/claude-sonnet-4-5", Workspace: "build",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.store = store
	h.sess = &sess
	h.model = model

	if _, err := h.SwitchWorkspace("ask"); err != nil {
		t.Fatalf("SwitchWorkspace: %v", err)
	}
	if h.sess.Workspace != "ask" || h.sess.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("in-memory session = %s/%s, want ask/anthropic/claude-sonnet-4-5",
			h.sess.Workspace, h.sess.Model)
	}
	stored, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.Workspace != "ask" || stored.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("stored session = %s/%s, want ask/anthropic/claude-sonnet-4-5",
			stored.Workspace, stored.Model)
	}
}

func TestSwitchWorkspaceCyclesAuthority(t *testing.T) {
	h := testSwitchHarness(t)
	for _, tc := range []struct {
		name string
		want workspace.Workspace
	}{
		{"ask", workspace.Ask},
		{"fuzz", workspace.Fuzz},
		{"build", workspace.Build},
	} {
		got, err := h.SwitchWorkspace(tc.name)
		if err != nil {
			t.Fatalf("SwitchWorkspace(%s): %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("workspace = %q, want %q", got, tc.want)
		}
		if h.workspace != tc.want.Label() {
			t.Errorf("harness label = %q, want %q", h.workspace, tc.want.Label())
		}
		// The runtime gate must follow the label: ASK denies mutation.
		if err := workspace.Admit(tc.want, workspace.OpWrite); (tc.want == workspace.Build) == (err != nil) {
			t.Errorf("Admit(%s, write) err = %v, want build-only", tc.want, err)
		}
	}
}

// A workspace transition must preserve model specialization: the target role's
// effective model becomes the one the loop actually calls.
func TestSwitchWorkspaceRebindsTheRoleModel(t *testing.T) {
	h := testSwitchHarness(t)
	h.settings.Roles = map[string]config.Profile{
		string(config.RoleAsk):   {Model: "anthropic/claude-haiku-4-5"},
		string(config.RoleFuzz):  {Model: "openai/gpt-5"},
		string(config.RoleBuild): {Model: "anthropic/claude-sonnet-4-5"},
	}
	for _, tc := range []struct {
		workspace string
		want      string
	}{
		{"ask", "anthropic/claude-haiku-4-5"},
		{"fuzz", "openai/gpt-5"},
		{"build", "anthropic/claude-sonnet-4-5"},
	} {
		if _, err := h.SwitchWorkspace(tc.workspace); err != nil {
			t.Fatalf("SwitchWorkspace(%s): %v", tc.workspace, err)
		}
		if got := h.model.Ref(); got != tc.want {
			t.Errorf("workspace %s model = %q, want the role binding %q", tc.workspace, got, tc.want)
		}
	}
}

func TestSwitchWorkspaceRejectsUnknown(t *testing.T) {
	h := testSwitchHarness(t)
	if _, err := h.SwitchWorkspace("nope"); err == nil {
		t.Error("unknown workspace accepted")
	}
}

func TestReplWorkspaceCommandsReachOwner(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	h := testSwitchHarness(t)
	in := strings.NewReader("/ask\n/fuzz\n/build\n/quit\n")
	var out strings.Builder
	if err := repl(h, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	text := out.String()
	for _, want := range []string{"switched to ASK", "switched to FUZZ", "switched to BUILD"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "unknown command /ask") || strings.Contains(text, "unknown command /fuzz") || strings.Contains(text, "unknown command /build") {
		t.Errorf("workspace command reported unknown:\n%s", text)
	}
}

// The host seam applies the open task's persisted authority, not the restored
// session default. This is the production path behind the TUI hook: the live
// gate, the session snapshot, and the task record must agree after it runs.
func TestApplyTaskAuthorityRestoresTheContractNotTheSessionDefault(t *testing.T) {
	held := testSwitchHarness(t)
	if _, err := held.SwitchWorkspace("fuzz"); err != nil {
		t.Fatalf("SwitchWorkspace(fuzz): %v", err)
	}
	root := t.TempDir()
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return held.activeWorkspace() })
	intent := contract.IntentContract{Objective: "investigate the bottleneck", Kind: contract.ObjectiveInformational}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin FUZZ: %v", err)
	}

	// The next process restores the default BUILD workspace.
	if _, err := held.SwitchWorkspace("build"); err != nil {
		t.Fatalf("SwitchWorkspace(build): %v", err)
	}
	from, to, moved, err := applyTaskAuthority(held, controller)
	if err != nil {
		t.Fatalf("applyTaskAuthority: %v", err)
	}
	if !moved || from != workspace.Build || to != workspace.Fuzz {
		t.Fatalf("restore = %s -> %s moved=%v, want BUILD -> FUZZ", from, to, moved)
	}
	if got := held.activeWorkspace(); got != workspace.Fuzz {
		t.Fatalf("live gate = %s, want FUZZ", got)
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the restored task disappeared")
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("recorded role = %s, want FUZZ", current.ActiveRole)
	}
	if current.ExecutionContract.Mutate {
		t.Fatalf("the restore widened the contract: %s", current.ExecutionContract.String())
	}

	// A second restore is a no-op: the gate already runs under the contract.
	if _, _, moved, err := applyTaskAuthority(held, controller); err != nil || moved {
		t.Fatalf("second restore moved=%v err=%v, want an idempotent no-op", moved, err)
	}

	// The inverse: a BUILD task restored under a FUZZ session keeps BUILD.
	buildRoot := t.TempDir()
	buildController := task.NewController(task.NewStore(buildRoot), nil)
	buildController.SetRoleSource(func() workspace.Workspace { return held.activeWorkspace() })
	if _, err := held.SwitchWorkspace("build"); err != nil {
		t.Fatalf("SwitchWorkspace(build): %v", err)
	}
	mutation := contract.IntentContract{
		Objective:    "fix tools/grep.go",
		Kind:         contract.ObjectiveMutation,
		Targets:      []contract.Target{{Kind: contract.TargetPath, Value: "tools/grep.go"}},
		Verification: []contract.VerificationRequirement{{Check: contract.CheckTest, Scope: "tools"}},
	}
	if _, err := buildController.Begin(mutation, nil); err != nil {
		t.Fatalf("Begin BUILD: %v", err)
	}
	if _, err := held.SwitchWorkspace("fuzz"); err != nil {
		t.Fatalf("SwitchWorkspace(fuzz): %v", err)
	}
	if _, to, moved, err := applyTaskAuthority(held, buildController); err != nil || !moved || to != workspace.Build {
		t.Fatalf("restore to %s moved=%v err=%v, want BUILD", to, moved, err)
	}
	if got := held.activeWorkspace(); got != workspace.Build {
		t.Fatalf("live gate = %s, want the granted BUILD", got)
	}
}
