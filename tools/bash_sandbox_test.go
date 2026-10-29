//go:build !windows

package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/execution/sandbox"
)

// A FUZZ execution is authorized as read-only by the authority decision, not
// by the command's spelling. Every one of these commands can mutate when it is
// allowed to; the boundary must stop all of them without a classifier knowing
// what they are.
func TestReadOnlyBashCannotMutateTheWorkspace(t *testing.T) {
	if !sandbox.ReadOnlySupported() {
		t.Skip("no enforceable read-only sandbox on this platform")
	}

	cases := []struct {
		name    string
		command string
		program string
	}{
		{"redirect", "echo hacked > file.txt", ""},
		{"append", "echo hacked >> file.txt", ""},
		{"remove", "rm file.txt", "rm"},
		{"move", "mv file.txt other.txt", "mv"},
		{"copy", "cp file.txt copy.txt", "cp"},
		{"sed in place", "sed -i 's/original/hacked/' file.txt", "sed"},
		{"awk write", `awk 'BEGIN{print "hacked" > "file.txt"}'`, "awk"},
		{"python open", `python3 -c "open('file.txt','w').write('hacked')"`, "python3"},
		{"python pathlib", `python3 -c "from pathlib import Path; Path('file.txt').write_text('hacked')"`, "python3"},
		{"python create", `python3 -c "open('newfile.txt','w').write('new')"`, "python3"},
		{"python mkdir", `python3 -c "import os; os.mkdir('newdir')"`, "python3"},
		{"perl open", `perl -e 'open(F,">file.txt"); print F "hacked"'`, "perl"},
		{"ruby write", `ruby -e 'File.write("file.txt","hacked")'`, "ruby"},
		{"bash nested", `bash -c 'echo hacked > file.txt'`, "bash"},
		{"sh nested", `sh -c 'echo hacked > file.txt'`, "sh"},
		{"osascript", `osascript -e 'do shell script "echo hacked > file.txt"'`, "osascript"},
		{"tee", `printf hacked | tee file.txt`, "tee"},
		{"dd", "dd if=/dev/zero of=file.txt bs=1 count=1", "dd"},
		{"truncate", "truncate -s 0 file.txt", "truncate"},
		{"chmod", "chmod 000 file.txt", "chmod"},
		{"touch", "touch file.txt", "touch"},
		{"symlink", "ln -sf /etc/passwd file.txt", "ln"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.program != "" {
				if _, err := exec.LookPath(tc.program); err != nil {
					t.Skipf("%s is not installed", tc.program)
				}
			}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(filepath.Join(root, "file.txt"))
			if err != nil {
				t.Fatal(err)
			}

			tool := NewBashTool(root)
			result := tool.Execute(agent.Context{Ctx: context.Background(), ReadOnly: true}, map[string]any{
				"command": tc.command,
			})
			if !result.IsError {
				t.Logf("command reported success, checking the filesystem anyway: %s", result.Text)
			}

			content, err := os.ReadFile(filepath.Join(root, "file.txt"))
			if err != nil {
				t.Fatalf("the protected file disappeared: %v", err)
			}
			if string(content) != "original" {
				t.Fatalf("read-only execution mutated the workspace: file.txt = %q", content)
			}
			after, err := os.Stat(filepath.Join(root, "file.txt"))
			if err != nil {
				t.Fatalf("the protected file disappeared: %v", err)
			}
			if after.Mode() != before.Mode() {
				t.Fatalf("read-only execution changed the mode: %v -> %v", before.Mode(), after.Mode())
			}
			if !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("read-only execution changed the mtime: %v -> %v", before.ModTime(), after.ModTime())
			}
			for _, created := range []string{"other.txt", "copy.txt", "newfile.txt", "newdir"} {
				if _, err := os.Stat(filepath.Join(root, created)); err == nil {
					t.Fatalf("read-only execution created %s", created)
				}
			}
		})
	}
}

// The read-only boundary must not break ordinary observation: a command that
// only reads still returns its output.
func TestReadOnlyBashStillReads(t *testing.T) {
	if !sandbox.ReadOnlySupported() {
		t.Skip("no enforceable read-only sandbox on this platform")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewBashTool(root).Execute(agent.Context{Ctx: context.Background(), ReadOnly: true}, map[string]any{
		"command": "cat file.txt",
	})
	if result.IsError {
		t.Fatalf("read-only cat failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "original") {
		t.Fatalf("read-only cat did not return the file: %s", result.Text)
	}
}

// The same command is allowed to mutate when the authority decision does not
// mark it read-only. This proves the boundary follows authority rather than
// globally disabling the shell.
func TestBashWithoutReadOnlyAuthorityCanMutate(t *testing.T) {
	root := t.TempDir()
	result := NewBashTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"command": "echo created > new.txt",
	})
	if result.IsError {
		t.Fatalf("write failed: %s", result.Text)
	}
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatalf("the write did not land: %v", err)
	}
	if strings.TrimSpace(string(data)) != "created" {
		t.Fatalf("new.txt = %q, want created", data)
	}
	if !result.Mutates {
		t.Fatal("a successful redirect was not reported as mutation evidence")
	}
}

// A background job is detached from the turn, not from authority. It must be
// sandboxed too, or a model could start a write and return immediately.
func TestReadOnlyBackgroundJobCannotMutate(t *testing.T) {
	if !sandbox.ReadOnlySupported() {
		t.Skip("no enforceable read-only sandbox on this platform")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewBashTool(root)
	started := tool.Execute(agent.Context{Ctx: context.Background(), ReadOnly: true}, map[string]any{
		"command":    "echo hacked > file.txt",
		"background": true,
	})
	if started.IsError {
		// A fail-closed spawn is acceptable: the command never ran.
		return
	}
	details, _ := started.Details.(map[string]any)
	job, _ := details["job"].(string)
	if job == "" {
		t.Fatalf("background job id missing: %s", started.Text)
	}
	_ = NewWaitTool(tool.Jobs()).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"jobId":   job,
		"timeout": float64(2000),
	})

	content, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatalf("the protected file disappeared: %v", err)
	}
	if string(content) != "original" {
		t.Fatalf("a read-only background job mutated the workspace: %q", content)
	}
}

// Verification is evidence, not authority. A successful test command is
// recorded so completion can require it after a mutation.
func TestBashReportsVerificationEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/check\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "check_test.go"), []byte("package check\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewBashTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"command": "go test ./...",
		"timeout": float64(60000),
	})
	if result.IsError {
		t.Fatalf("go test failed: %s", result.Text)
	}
	if !result.Verifies {
		t.Fatalf("a successful go test was not recorded as verification: %s", result.Text)
	}
}

func TestIsVerificationCommand(t *testing.T) {
	yes := []string{
		"go test ./...",
		"go build ./...",
		"go vet ./...",
		"cargo test",
		"cargo check",
		"npm test",
		"npm run test",
		"pnpm build",
		"pytest -q",
		"python3 -m pytest",
		"make test",
		"make check",
		"mvn test",
		"./gradlew test",
		"dotnet test",
		"swift test",
		"deno test",
		"tsc --noEmit",
		"staticcheck ./...",
		"golangci-lint run",
		"cd sub && go test ./...",
	}
	for _, command := range yes {
		if !IsVerificationCommand(command) {
			t.Errorf("IsVerificationCommand(%q) = false, want true", command)
		}
	}

	no := []string{
		"go test ./... || true",
		"go test ./...; echo done",
		"echo go test ./...",
		"go run ./cmd/x",
		"git status",
		"ls -la",
		"gofmt -l .",
		"make",
		"golangci-lint linters",
		"",
	}
	for _, command := range no {
		if IsVerificationCommand(command) {
			t.Errorf("IsVerificationCommand(%q) = true, want false", command)
		}
	}
}

// A command killed by its timeout must not leave its children running. The
// process group kill is what closes the output pipes and prevents an orphan
// from outliving the tool call.
func TestBashTimeoutKillsTheProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	root := t.TempDir()
	tool := NewBashTool(root)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"command": "echo $$ > shell.pid; sleep 5",
		"timeout": float64(150),
	})
	if !result.IsError {
		t.Fatalf("a timed-out command reported success: %s", result.Text)
	}
	data, err := os.ReadFile(filepath.Join(root, "shell.pid"))
	if err != nil {
		t.Skipf("the shell did not record its pid: %v", err)
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		t.Skip("the shell recorded an empty pid")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %s survived the timeout", pid)
}

// processAlive reports whether a pid still exists. EPERM means it exists but
// belongs to another user, which is still alive for this test's purpose.
func processAlive(pid string) bool {
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 {
		return false
	}
	err = syscall.Kill(n, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
