package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/project"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
	"github.com/spf13/cobra"
)

func TestResolveOptionsReadsWorkspaceAndScopedModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	cmd := &cobra.Command{Use: "ohgo"}
	cmd.Flags().String("workspace", "ask", "")
	cmd.Flags().String("dir", root, "")
	cmd.Flags().String("model", "", "")
	// The workspace-specific model must win over the global default (§12).
	settings := config.Settings{Model: "anthropic/global", Models: map[string]string{"ask": "anthropic/claude-haiku-4-5"}}
	if err := config.Save(settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	opts, _, err := resolveOptions(cmd)
	if err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	if opts.workspace != workspace.Ask {
		t.Errorf("workspace = %q, want ask", opts.workspace)
	}
	if opts.model.ID != "claude-haiku-4-5" {
		t.Errorf("model = %q, want the ask-specific model", opts.model.ID)
	}
	// Project creation belongs to startup, not option resolution: a fresh
	// directory resolves with no identity, and an initialized one loads it.
	if opts.projectID != "" {
		t.Error("project identity was invented for an uninitialized directory")
	}
	if _, err := project.Ensure(root); err != nil {
		t.Fatalf("Ensure project: %v", err)
	}
	retry, _, err := resolveOptions(cmd)
	if err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	if retry.projectID == "" {
		t.Error("project identity was not loaded for an initialized directory")
	}
}

func TestResolveOptionsRejectsUnknownWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := &cobra.Command{Use: "ohgo"}
	cmd.Flags().String("workspace", "bulid", "")
	cmd.Flags().String("dir", t.TempDir(), "")

	if _, _, err := resolveOptions(cmd); err == nil {
		t.Fatal("an unknown workspace was accepted")
	}
}

func TestResolveOptionsDefaultsToBuildAuthority(t *testing.T) {
	// No flag means the pre-workspace behavior: full mutation authority.
	t.Setenv("HOME", t.TempDir())
	cmd := &cobra.Command{Use: "ohgo"}
	cmd.Flags().String("workspace", "", "")
	cmd.Flags().String("dir", t.TempDir(), "")

	opts, _, err := resolveOptions(cmd)
	if err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	if opts.workspace != workspace.Build {
		t.Errorf("workspace = %q, want build", opts.workspace)
	}
}

func TestAdmitForEnforcesWorkspaceOnRealTools(t *testing.T) {
	// The gate is the runtime boundary: a shell mutation is refused in FUZZ
	// and allowed in BUILD, through the same tools the loop is given.
	bash := tools.NewBashTool(t.TempDir())
	mutating := ai.ToolCall{Name: "bash", Arguments: map[string]any{"command": "rm -rf internal"}}
	reading := ai.ToolCall{Name: "bash", Arguments: map[string]any{"command": "go test ./..."}}

	if admitted, reason := admitFor(workspace.Fuzz)(mutating, bash); admitted {
		t.Error("FUZZ admitted a shell mutation")
	} else if !strings.Contains(reason, "mutation_not_permitted") {
		t.Errorf("refusal reason = %q, want mutation_not_permitted", reason)
	}
	if admitted, _ := admitFor(workspace.Fuzz)(reading, bash); !admitted {
		t.Error("FUZZ refused a read-only shell command")
	}
	if admitted, _ := admitFor(workspace.Build)(mutating, bash); !admitted {
		t.Error("BUILD refused a shell mutation")
	}

	write := tools.NewWriteTool(t.TempDir())
	writeCall := ai.ToolCall{Name: "write", Arguments: map[string]any{"path": "x.go", "content": "package x"}}
	if admitted, _ := admitFor(workspace.Ask)(writeCall, write); admitted {
		t.Error("ASK admitted a write tool")
	}
	if admitted, _ := admitFor(workspace.Build)(writeCall, write); !admitted {
		t.Error("BUILD refused a write tool")
	}
}

func TestApplySessionAuthorityRestoresWorkspaceAndModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	sess, err := store.CreateWith(session.CreateOptions{
		Cwd: t.TempDir(), Model: "anthropic/claude-sonnet-4-5", Workspace: "fuzz", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A fresh configuration now points elsewhere; the resumed session must
	// keep its own model rather than silently switching (§13, I8).
	opts := options{settings: config.Settings{Model: "openai/gpt-5"}, model: catalog.MustResolve("openai/gpt-5")}
	cmd := &cobra.Command{Use: "ohgo"}
	cmd.Flags().String("workspace", "", "")
	cmd.Flags().String("model", "", "")

	if err := applySessionAuthority(cmd, &opts, catalog.Resolve, store, &sess); err != nil {
		t.Fatalf("applySessionAuthority: %v", err)
	}
	if opts.workspace != workspace.Fuzz {
		t.Errorf("workspace = %q, want the session's fuzz", opts.workspace)
	}
	if opts.model.ID != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want the session's snapshot", opts.model.ID)
	}

	// The snapshot must be written back so a later run restores the truth.
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != "fuzz" || got.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("stored authority = %s/%s, want fuzz/anthropic/claude-sonnet-4-5", got.Workspace, got.Model)
	}
}

func TestApplySessionAuthorityHonoursExplicitOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.CreateWith(session.CreateOptions{
		Cwd: t.TempDir(), Model: "anthropic/claude-sonnet-4-5", Workspace: "fuzz",
	})
	if err != nil {
		t.Fatal(err)
	}

	opts := options{settings: config.Default(), model: catalog.MustResolve("openai/gpt-5")}
	cmd := &cobra.Command{Use: "ohgo"}
	cmd.Flags().String("workspace", "build", "")
	cmd.Flags().String("model", "openai/gpt-5", "")

	if err := applySessionAuthority(cmd, &opts, catalog.Resolve, store, &sess); err != nil {
		t.Fatalf("applySessionAuthority: %v", err)
	}
	if opts.workspace != workspace.Build {
		t.Errorf("workspace = %q, want the explicit build", opts.workspace)
	}
	if opts.model.ID != "gpt-5" {
		t.Errorf("model = %q, want the explicit gpt-5", opts.model.ID)
	}
}

func TestResolveOptionsDoesNotCreateProject(t *testing.T) {
	// Option resolution is read-only for project state: creating .ohgo/
	// here would let startup.Ensure fast-path over a project the user was
	// never prompted to initialize, which is exactly the deleted-.ohgo
	// bypass. Only startup.Ensure may create project state.
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	cmd := &cobra.Command{Use: "ohgo"}
	cmd.Flags().String("dir", root, "")

	opts, _, err := resolveOptions(cmd)
	if err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	if opts.projectID != "" {
		t.Errorf("projectID = %q for an uninitialized directory, want empty", opts.projectID)
	}
	if _, err := os.Stat(filepath.Join(root, ".ohgo")); !os.IsNotExist(err) {
		t.Errorf(".ohgo was created during option resolution: %v", err)
	}
}
