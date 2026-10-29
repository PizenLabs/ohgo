package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestCommandRefusesReadOnlyWithoutARoot(t *testing.T) {
	_, err := Command(context.Background(), Spec{
		Argv:     []string{"sh", "-c", "true"},
		ReadOnly: true,
	})
	if !errors.Is(err, ErrNoRoot) {
		t.Fatalf("err = %v, want ErrNoRoot", err)
	}
}

func TestCommandRunsUnguardedWhenWritable(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	cmd, err := Command(context.Background(), Spec{
		Argv: []string{"sh", "-c", "echo ok > out.txt"},
		Dir:  dir,
		Root: dir,
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	Configure(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(dir + "/out.txt"); err != nil {
		t.Fatalf("the writable command did not write: %v", err)
	}
}

func TestCommandReadOnlyRequiresAnExistingRoot(t *testing.T) {
	_, err := Command(context.Background(), Spec{
		Argv:     []string{"sh", "-c", "true"},
		Root:     t.TempDir() + "/missing",
		ReadOnly: true,
	})
	if err == nil {
		t.Fatal("a missing root was accepted for read-only execution")
	}
}
