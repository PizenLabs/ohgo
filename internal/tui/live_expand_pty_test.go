package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Real-terminal evidence for Ctrl+O activity expansion.
//
// A unit test can show the toggle flips state; only a real pseudo-terminal
// shows that the keypress reaches the program, the bounded detail lands in
// the scrollback exactly once, the sibling block stays collapsed, and the
// terminal is asked for nothing it should not be.

// expandTool is one of two sequential demo tools. The first succeeds with
// multiline output; the second fails with multiline output, so the run
// proves success and failure stay distinct under expansion.
type expandTool struct {
	tools.Base
	text  string
	isErr bool
}

func newExpandTool(name, text string, isErr bool) expandTool {
	return expandTool{
		Base: tools.NewBase(name, "a tool for the expand demo",
			map[string]any{"type": "object", "properties": map[string]any{}},
			agent.Approval{Tier: agent.TierRead}),
		text:  text,
		isErr: isErr,
	}
}

func (t expandTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	return agent.Result{Text: t.text, IsError: t.isErr}
}

// Exclusive serializes the two demo calls so entry order matches emission
// order: without it concurrent starts could seal in either order and the
// "latest" block would be nondeterministic by design.
func (t expandTool) Exclusive(map[string]any) bool { return true }

// expandScript runs one turn with two sequential tool calls, then an answer.
func expandScript() ai.StreamFn {
	var calls int
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		calls++
		if calls == 1 {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.ToolCall{ID: "call_a", Name: "first", Arguments: map[string]any{}},
					ai.ToolCall{ID: "call_b", Name: "second", Arguments: map[string]any{}},
				},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return ai.NewTextStream("both tools ran and the answer follows."), nil
	}
}

// runExpandPTYChild is the session half: the real Program on the real terminal.
func runExpandPTYChild() {
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "pty-expand", Provider: "pty", ContextWindow: 200_000},
		Stream: expandScript(),
		Tools: []agent.Tool{
			newExpandTool("first", "first tool line one\nfirst tool line two\nfirst tool line three\n", false),
			newExpandTool("second", "second failure line one\nsecond failure line two\n", true),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "loop:", err)
		os.Exit(1)
	}
	p := NewProgram(loop, "pty/pty-expand", 200_000)
	// No approval hook: read-tier demo tools auto-run, so the run proves
	// the collapsed/expanded projections without a modal in between.
	// (Approval interplay is covered by TestCtrlODuringApprovalIsNoOp.)
	if err := p.RunLive(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
	}
}

func TestLiveExpandUnderARealPTY(t *testing.T) {
	if os.Getenv("OHGO_EXPAND_PTY_CHILD") == "1" {
		runExpandPTYChild()
		return
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skip("no script(1) to open a pseudo-terminal with")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := scriptPTY(fmt.Sprintf("stty rows %d cols %d; exec %q -test.run TestLiveExpandUnderARealPTY -test.timeout 120s",
		ptyRows, ptyCols, exe))
	cmd.Env = append(os.Environ(), "OHGO_EXPAND_PTY_CHILD=1", "TERM=xterm-256color")

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

	plain := func() string {
		mu.Lock()
		defer mu.Unlock()
		return stripANSI(seen.String())
	}
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(stdin, s); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}
	waitFor := func(needle string, budget time.Duration) {
		t.Helper()
		for deadline := time.Now().Add(budget); time.Now().Before(deadline); {
			if strings.Contains(plain(), needle) {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
		raw := plain()
		path := filepath.Join(os.TempDir(), "ohgo-expand-pty-capture.txt")
		_ = os.WriteFile(path, []byte(raw), 0o600)
		t.Fatalf("timed out waiting for %q in the terminal output (capture: %s):\n%s",
			needle, path, tail(raw))
	}

	waitFor("? help", 20*time.Second)
	write("run both demo tools\r")

	// Both blocks seal collapsed: compact outcomes, no raw bodies.
	waitFor("3 lines", 20*time.Second)
	waitFor("error: second failure line one", 20*time.Second)
	waitFor("both tools ran and the answer follows.", 20*time.Second)
	if got := plain(); strings.Contains(got, "second failure line two") {
		t.Fatalf("collapsed transcript leaked the raw body:\n%s", tail(got))
	}

	// Ctrl+O expands the latest block only: the failure detail arrives,
	// the sibling's body never does.
	write("\x0f") // ctrl+o
	waitFor("second failure line two", 10*time.Second)
	if got := plain(); strings.Contains(got, "first tool line two") {
		t.Fatalf("expanding the latest block leaked its sibling:\n%s", tail(got))
	}

	// Collapse again, then quit an idle session.
	write("\x0f") // ctrl+o
	time.Sleep(500 * time.Millisecond)
	write("\x04") // ctrl+d quits an idle session
	_ = stdin.Close()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the session exited with %v", err)
	}
	mu.Lock()
	out := seen.String()
	mu.Unlock()

	path := filepath.Join(os.TempDir(), "ohgo-expand-pty-capture.txt")
	if err := os.WriteFile(path, []byte(out), 0o600); err == nil {
		t.Logf("terminal capture written to %s", path)
	}

	assertNoTerminalHijack(t, out)
}
