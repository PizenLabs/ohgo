package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/project"
)

// TestBootProjectIsReadOnly pins the one-shot contract: resolving the project
// identity for a command that cannot show the initialization gate must never
// create .ohgo/. An uninitialized project is reported, not initialized.
func TestBootProjectIsReadOnly(t *testing.T) {
	root := t.TempDir()

	if _, err := bootProject(root); err == nil {
		t.Fatal("bootProject succeeded for an uninitialized directory")
	}
	if _, err := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(err) {
		t.Errorf("bootProject created .ohgo: %v", err)
	}
}

// TestBootProjectLoadsInitialized is the fast path: an initialized project
// resolves its stable id and nothing is recreated.
func TestBootProjectLoadsInitialized(t *testing.T) {
	root := t.TempDir()
	created, err := project.Ensure(root)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	before, err := os.ReadFile(project.Path(root))
	if err != nil {
		t.Fatal(err)
	}

	id, err := bootProject(root)
	if err != nil {
		t.Fatalf("bootProject: %v", err)
	}
	if id != created.ID {
		t.Errorf("id = %q, want the stable %q", id, created.ID)
	}
	after, err := os.ReadFile(project.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("project.yml rewritten:\nbefore %q\nafter  %q", before, after)
	}
}

// TestRunCommandRefusesUninitializedProject is the end-to-end guard for the
// reported defect: `ohgo run` in a fresh directory must fail with a clear
// instruction rather than silently creating .ohgo/.
func TestRunCommandRefusesUninitializedProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OHGO_HISTORY_FILE", "")
	root := t.TempDir()

	cmd := rootCmd()
	cmd.SetArgs([]string{"run", "--dir", root, "--no-tools", "hello"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("ohgo run succeeded on an uninitialized project")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("error = %q, want it to name the uninitialized project", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf("ohgo run created .ohgo: %v", statErr)
	}
}
