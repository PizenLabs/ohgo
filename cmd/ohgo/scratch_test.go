package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/history"
)

func TestHandleScratchSignalCleansBeforeReraising(t *testing.T) {
	cleaned := false
	var forwarded os.Signal
	handleScratchSignal(func() { cleaned = true }, syscall.SIGTERM, func(sig os.Signal) {
		forwarded = sig
	})
	if !cleaned {
		t.Fatal("the signal handler did not remove scratch files")
	}
	if forwarded != syscall.SIGTERM {
		t.Fatalf("re-raised %v, want SIGTERM", forwarded)
	}
}

// TestScratchSignalCleanupRemovesActiveFiles is the forced-exit contract. The
// helper process creates a real scratch file and then waits for SIGTERM; the
// parent proves the file is gone and the signal still terminates the process.
func TestScratchSignalCleanupRemovesActiveFiles(t *testing.T) {
	if os.Getenv("OHGO_SCRATCH_SIGNAL_HELPER") == "1" {
		root := os.Getenv("OHGO_SCRATCH_SIGNAL_ROOT")
		scratch := history.NewScratch(root)
		_, _, offloaded, err := scratch.Offload(1, strings.Repeat("line\n", history.ScratchLineThreshold))
		if err != nil || !offloaded {
			fmt.Printf("helper setup failed: %v\n", err)
			os.Exit(2)
		}
		_ = installScratchSignalCleanup(func() { _ = scratch.ReleaseAll() })
		fmt.Println("ready")
		for {
			time.Sleep(time.Hour)
		}
	}

	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestScratchSignalCleanupRemovesActiveFiles$")
	child.Env = append(os.Environ(),
		"OHGO_SCRATCH_SIGNAL_HELPER=1",
		"OHGO_SCRATCH_SIGNAL_ROOT="+root,
	)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()

	ready := make(chan error, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr != nil {
			ready <- readErr
			return
		}
		if strings.TrimSpace(line) != "ready" {
			ready <- fmt.Errorf("helper said %q", line)
			return
		}
		ready <- nil
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("helper did not become ready: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not become ready")
	}

	files, err := filepath.Glob(filepath.Join(root, ".ohgo", "scratch", "prompt_1_*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("scratch files before signal = %v (err %v), want one", files, err)
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal helper: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case err := <-waited:
		// The re-raised signal is the expected exit; only the file removal is
		// asserted. Keep the value checked so errcheck documents the intent.
		_ = err
	case <-time.After(10 * time.Second):
		t.Fatal("helper survived SIGTERM; the default signal action was not restored")
	}

	remaining, err := filepath.Glob(filepath.Join(root, ".ohgo", "scratch", "prompt_1_*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("scratch files survived the signal: %v", remaining)
	}
}
