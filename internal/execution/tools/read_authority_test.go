package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A shell is the one capability whose filesystem targets are text. The read
// boundary must name the same unauthorized target the dedicated read tools
// already refuse, and it must not refuse ordinary workspace commands.
func TestOutsideWorkspaceReadDistinguishesAuthorizedTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")

	denied := []string{
		"grep -R project " + outside,
		"grep -rl \"OpenCode\" " + outside,
		"rg project " + outside,
		"cat " + secret,
		"head -n 5 " + secret,
		"sed -n '1,20p' " + secret,
		"awk '{print}' " + secret,
		"find " + outside + " -name '*.tsx'",
		"ls " + outside,
		"wc -l " + secret,
		"diff " + secret + " index.html",
		"cd " + outside + " && bw a",
		"cd " + outside + " && cat secret.txt",
		"cat " + secret + " | head -n 1",
		"bash -c 'grep -R project " + outside + "'",
		"sh -c \"cat " + secret + "\"",
		"xargs cat < " + secret,
	}
	for _, command := range denied {
		if target, outsideWorkspace := OutsideWorkspaceRead(command, root); !outsideWorkspace {
			t.Errorf("OutsideWorkspaceRead(%q) allowed a read outside the workspace (target %q)", command, target)
		}
	}

	allowed := []string{
		"grep -R project .",
		"grep -n \"OpenCode\" index.html",
		"rg --files",
		"cat index.html",
		"sed -n '1,20p' index.html",
		"find . -maxdepth 2 -type f",
		"find src -name '*.html'",
		"ls -la",
		"go build ./...",
		"go test ./...",
		"git log --oneline -5",
		"node --version",
		"cat /dev/null",
		"cat /dev/zero",
		"echo hi > out.txt",
		"cd src && cat ../index.html",
		"mktemp -d",
	}
	for _, command := range allowed {
		if target, outsideWorkspace := OutsideWorkspaceRead(command, root); outsideWorkspace {
			t.Errorf("OutsideWorkspaceRead(%q) refused an authorized command (target %q)", command, target)
		}
	}
}

// The dedicated read capabilities already confine their path arguments. This
// pins the boundary the shell rule mirrors, so a future change to either cannot
// silently leave one path open.
func TestDedicatedReadToolsDenyOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := agent.Context{Ctx: context.Background()}

	if result := NewReadTool(root).Execute(ctx, map[string]any{"path": secret}); !result.IsError {
		t.Fatalf("read outside the workspace succeeded: %s", result.Text)
	}
	if result := NewGrepTool(root).Execute(ctx, map[string]any{"pattern": "secret", "path": outside}); !result.IsError {
		t.Fatalf("grep outside the workspace succeeded: %s", result.Text)
	}
	if result := NewGlobTool(root).Execute(ctx, map[string]any{"pattern": "*", "path": outside}); !result.IsError {
		t.Fatalf("glob outside the workspace succeeded: %s", result.Text)
	}
}

// Existing absolute and $HOME-relative literals are refused wherever they
// appear, including inside a shell loop whose body the segment parser cannot
// model. The user's home is the sibling-and-parent shape this boundary exists
// to stop; system paths remain ordinary program inputs.
func TestOutsideWorkspaceReadRefusesHomeLiteralsInCompoundCommands(t *testing.T) {
	root := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no home directory on this host")
	}
	if within, err := filepath.Rel(root, home); err == nil && (within == "." || !strings.HasPrefix(within, "..")) {
		t.Skip("the workspace contains the home directory")
	}
	denied := []string{
		"for d in " + home + "; do echo \"$d\"; done",
		"for d in " + home + "/*; do ls \"$d\"; done",
		"find $HOME -maxdepth 2 -type f",
		"find ${HOME} -name '*.tsx'",
		"cat ~/.gitconfig",
	}
	for _, command := range denied {
		if target, outside := OutsideWorkspaceRead(command, root); !outside {
			t.Errorf("OutsideWorkspaceRead(%q) allowed a home literal outside the workspace (target %q)", command, target)
		}
	}
}

// The bash tool refuses before the process starts, so a model cannot use the
// shell to re-issue a read a dedicated tool already denied.
func TestBashRefusesOutsideWorkspaceRead(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewBashTool(root)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"command": "cat " + secret})
	if !result.IsError || !result.Refused {
		t.Fatalf("outside read was not refused: error=%v refused=%v text=%s", result.IsError, result.Refused, result.Text)
	}
	if result.FailureClass != "outside_workspace" {
		t.Fatalf("failure class = %q, want outside_workspace", result.FailureClass)
	}
}

// A `cd` out of the workspace is itself the scope escape; the later relative
// read must not be evaluated as though it were still in the workspace.
func TestBashRefusesReadAfterDirectoryEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewBashTool(root)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"command": "cd " + outside + " && cat secret.txt",
	})
	if !result.Refused {
		t.Fatalf("read after cd escape was not refused: %+v", result)
	}
}

// The temporary tree is a staging sandbox for a workspace that lives
// elsewhere: refusing a build log there would break ordinary execution. The
// exemption must not extend to the user's own tree.
func TestOutsideWorkspaceReadAllowsTemporaryStagingForANonTempWorkspace(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no home directory on this host")
	}
	root := filepath.Join(home, "ohgo-nonexistent-workspace-for-test")
	staged := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(staged, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if target, denied := OutsideWorkspaceRead("cat "+staged, root); denied {
		t.Fatalf("temporary staging read was refused: target %q", target)
	}
	if target, denied := OutsideWorkspaceRead("cd "+filepath.Dir(staged)+" && make", root); denied {
		t.Fatalf("temporary staging directory was refused: target %q", target)
	}
	if target, denied := OutsideWorkspaceRead("cat "+home, root); !denied {
		t.Fatalf("home read was allowed for a home-rooted workspace: target %q", target)
	}
}

// A verify service command is model-supplied shell text. It must cross the
// same read boundary as a bash call, or the shell rule has a second door.
func TestVerifyRefusesServiceCommandThatReadsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{
			"command":   "cat " + secret,
			"readiness": map[string]any{"kind": "process"},
		},
		"checks": []any{map[string]any{"kind": "command", "command": "true"}},
	})
	if !result.IsError || !strings.Contains(result.Text, "outside the workspace") {
		t.Fatalf("verify accepted an outside-workspace service read: error=%v text=%s", result.IsError, result.Text)
	}
}
