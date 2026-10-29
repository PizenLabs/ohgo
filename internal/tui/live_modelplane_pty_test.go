package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Real-terminal evidence for the model control plane flow.
//
// A unit test drives the state machine; this one drives the whole Program
// through a real pseudo-terminal — real raw mode, real winsize, real
// keystrokes — and asserts on the bytes it writes. It is the end-to-end proof
// that /model opens the dialog, that the model-first flow runs
// model -> role -> effort -> apply in a live terminal, and that the confirmed
// assignment reaches the authority hook.

const (
	modelPTYRows = 40
	modelPTYCols = 120
)

// TestLiveModelFlowUnderAPTY drives /model through a real pseudo-terminal.
func TestLiveModelFlowUnderAPTY(t *testing.T) {
	if os.Getenv("OHGO_MODEL_PTY_CHILD") == "1" {
		runModelFlowPTYChild()
		return
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skip("no script(1) to open a pseudo-terminal with")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := scriptPTY(fmt.Sprintf("stty rows %d cols %d; exec %q -test.run TestLiveModelFlowUnderAPTY -test.timeout 120s",
		modelPTYRows, modelPTYCols, exe))
	cmd.Env = append(os.Environ(), "OHGO_MODEL_PTY_CHILD=1", "TERM=xterm-256color")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	var mu sync.Mutex
	var seen strings.Builder
	go func() {
		_, _ = io.Copy(writerFunc(func(b []byte) (int, error) {
			mu.Lock()
			seen.Write(b)
			mu.Unlock()
			return len(b), nil
		}), stdout)
	}()

	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(stdin, s); err != nil {
			t.Fatalf("write %q: %v", s, err)
		}
	}
	waitFor := func(needle string, budget time.Duration) {
		t.Helper()
		for deadline := time.Now().Add(budget); time.Now().Before(deadline); {
			mu.Lock()
			got := seen.String()
			mu.Unlock()
			if strings.Contains(stripANSI(got), needle) {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
		mu.Lock()
		raw := seen.String()
		mu.Unlock()
		t.Fatalf("timed out waiting for %q in the terminal output:\n%s", needle, tail(stripANSI(raw)))
	}

	// The session starts: a composer is the first thing.
	waitFor("? help", 20*time.Second)

	// Open the control plane. It renders as a bounded dialog over the chrome.
	write("/model")
	waitFor("/model", 10*time.Second)
	write("\r")
	waitFor("MODEL CONTROL", 20*time.Second)
	waitFor("PROVIDERS", 5*time.Second)
	waitFor("Search / Filter", 5*time.Second)

	// Model-first flow: Provider -> Model (Enter), choose the model (Enter),
	// then the role step appears.
	write("\r") // providers -> models
	write("\r") // choose the highlighted model -> role step
	waitFor("Assign to role", 5*time.Second)

	// Move to SUP (ctrl+n) and continue to the effort step.
	write("\x0e") // down: build -> sup
	write("\r")   // choose sup -> effort step
	waitFor("Thinking effort", 5*time.Second)
	waitFor("off", 5*time.Second)
	waitFor("medium", 5*time.Second)

	// Apply and confirm the assignment reached the authority hook.
	write("\r")
	waitFor("assigned", 5*time.Second)

	// Close the dialog, then quit the idle session.
	write("\x1b") // esc: close the plane
	time.Sleep(200 * time.Millisecond)
	write("\x04") // ctrl+d quits an idle session
	_ = stdin.Close()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the session exited with %v", err)
	}
}

// runModelFlowPTYChild is the session half: the real Program on the real
// terminal, with the control plane wired to fixture data so no provider is
// contacted.
func runModelFlowPTYChild() {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "model-demo", Provider: "pty", ContextWindow: 200_000},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream("ok"), nil
		},
		Tools: []agent.Tool{newPTYTool()},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "loop:", err)
		os.Exit(1)
	}
	p := NewProgram(loop, "anthropic/claude-sonnet-4-5", 1_000_000)
	p.SetCatalogHooks(CatalogHooks{
		Snapshot: func() CatalogSnapshot {
			return CatalogSnapshot{Options: planeFixture().Models, Origin: "live"}
		},
	})
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			update.Name = "Applied"
			return update, planeFixture().Roles, nil
		},
	})
	if err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
	}
}
