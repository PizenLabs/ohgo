package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A tag listing is observation. The runtime reads the bash result's mutation
// flag as the evidence that a change now needs a check, so reporting a listing
// as a mutation stranded a BUILD run in the verification stage: it kept reading
// to discover a change that never happened and was stopped as stalled with the
// release workflow still unedited. This is the exact command from that run.
func TestGitTagListingReportsNoMutationEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=ohgo", "GIT_AUTHOR_EMAIL=ohgo@example.com",
			"GIT_COMMITTER_NAME=ohgo", "GIT_COMMITTER_EMAIL=ohgo@example.com",
			"HOME="+root,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("commit", "--allow-empty", "-m", "initial")
	git("tag", "v0.1.2")

	tool := NewBashTool(root)
	listing := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"command": `git tag | head -50 && echo "---branches---" && git branch -a && echo "---status---" && git status --short`,
	})
	if listing.IsError {
		t.Fatalf("listing tags failed: %s", listing.Text)
	}
	if !strings.Contains(listing.Text, "v0.1.2") {
		t.Fatalf("the listing did not print the tag: %s", listing.Text)
	}
	if listing.Mutates || len(listing.AffectedPaths) > 0 {
		t.Fatalf("listing tags reported mutation evidence (mutates=%t paths=%v)",
			listing.Mutates, listing.AffectedPaths)
	}

	created := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"command": "git tag -a v0.1.3 -m release",
	})
	if created.IsError {
		t.Fatalf("creating a tag failed: %s", created.Text)
	}
	if !created.Mutates {
		t.Fatal("creating a tag was not reported as mutation evidence")
	}
}

// File tools report whether they changed the workspace so completion can
// require verification. The flag is set from the tool's own success, not from
// model text.
func TestFileToolsReportMutationEvidence(t *testing.T) {
	root := t.TempDir()
	write := NewWriteTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path":    "a.txt",
		"content": "one",
	})
	if write.IsError || !write.Mutates {
		t.Fatalf("write evidence = (error %v, mutates %v), want a mutation", write.IsError, write.Mutates)
	}

	edit := NewEditTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path":       "a.txt",
		"old_string": "one",
		"new_string": "two",
	})
	if edit.IsError || !edit.Mutates {
		t.Fatalf("edit evidence = (error %v, mutates %v), want a mutation", edit.IsError, edit.Mutates)
	}

	read := NewReadTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": "a.txt",
	})
	if read.IsError || read.Mutates {
		t.Fatalf("read evidence = (error %v, mutates %v), want no mutation", read.IsError, read.Mutates)
	}
	if data, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(data) != "two" {
		t.Fatalf("the edit did not land: %q, %v", data, err)
	}
}
