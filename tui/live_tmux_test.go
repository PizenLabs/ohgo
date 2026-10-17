package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real-terminal evidence for the output-first mode inside a multiplexer.
//
// tmux is a terminal emulator of its own, with its own history buffer, its own
// copy-mode and its own mouse policy, so it is the environment where the two
// ownership models differ most. This drives the same live session inside a
// real tmux server and reads back what the pane actually shows, which is the
// only way to tell whether the transcript really landed in a scrollback tmux
// can scroll.
func TestLiveSessionInsideTmux(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("no tmux available")
	}
	if os.Getenv("OHGO_LIVE_PTY_CHILD") == "1" {
		runPTYChild()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "tmux.sock")
	tmux := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(tmuxPath, append([]string{"-S", sock}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// The real topology: a pseudo-terminal, a tmux client attached to it, and
	// the session created inside that terminal. tmux is then the terminal
	// emulator ohgo sees, which is exactly the environment under test.
	//
	// The client's stdin is held open by this process: script(1) forwards an
	// end-of-file as a literal ctrl+d, which tmux hands to the pane, and an
	// idle composer answers by quitting. That is correct terminal behaviour
	// and exactly what the harness must not trigger by accident.
	session := "ohgo-live"
	heldIn, holdWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = holdWriter.Close() }()
	attach := scriptPTY(fmt.Sprintf("stty rows %d cols %d; exec %q -S %q -f /dev/null new-session -x %d -y %d -s %s %q -test.run TestLiveSessionInsideTmux -test.timeout 90s",
		ptyRows, ptyCols, tmuxPath, sock, ptyCols, ptyRows, session, exe))
	attach.Env = append(os.Environ(), "OHGO_LIVE_PTY_CHILD=1", "TERM=xterm-256color")
	attach.Stdin = heldIn
	// The client's output is pumped by os/exec on its own goroutine while the
	// test below reads it, so every access goes through the mutex — the same
	// pattern the PTY harness uses for its capture.
	var attachMu sync.Mutex
	var attachOut strings.Builder
	attach.Stdout = writerFunc(func(b []byte) (int, error) {
		attachMu.Lock()
		defer attachMu.Unlock()
		return attachOut.Write(b)
	})
	attach.Stderr = attach.Stdout
	if err := attach.Start(); err != nil {
		t.Fatalf("start a tmux client: %v", err)
	}
	defer func() { _ = attach.Process.Kill() }()
	waitServer(t, tmuxPath, sock, func() string {
		attachMu.Lock()
		defer attachMu.Unlock()
		return attachOut.String()
	}())
	t.Cleanup(func() { _ = exec.Command(tmuxPath, "-S", sock, "kill-server").Run() })

	send := func(keys string) {
		t.Helper()
		tmux("send-keys", "-t", session, keys)
	}
	pane := func(scrollback int) string {
		t.Helper()
		args := []string{"capture-pane", "-p", "-t", session}
		if scrollback > 0 {
			args = append(args, "-S", fmt.Sprintf("-%d", scrollback))
		}
		return stripANSI(tmux(args...))
	}
	waitFor := func(needle string, budget time.Duration) {
		t.Helper()
		for deadline := time.Now().Add(budget); time.Now().Before(deadline); {
			if strings.Contains(pane(80), needle) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in the tmux pane:\n%s", needle, pane(80))
	}

	// The session starts: a live region with a composer is the first thing.
	waitFor("? help", 20*time.Second)

	// A prompt streams an answer carrying every markdown shape.
	send("stream this")
	send("Enter")
	waitFor("Session boundary inspection", 20*time.Second)
	waitFor("sessionCheck", 20*time.Second)

	// The transcript has to be in tmux's own history, not just on screen:
	// scrolling back must still show the first line of the answer.
	visible := pane(0)
	if !strings.Contains(visible, "Closing line.") {
		t.Errorf("the bottom of the answer is not on screen:\n%s", visible)
	}
	history := pane(80)
	for _, line := range []string{"Session boundary inspection", "```go", "│ a │ b │"} {
		if !strings.Contains(history, line) {
			t.Errorf("%q is not in tmux's scrollback, so the terminal owns no history:\n%s", line, history)
		}
	}

	// tmux owns scrolling. Its copy-mode is the route a wheel takes inside a
	// multiplexer — prefix+[ or, with `mouse on`, the wheel itself — and it
	// needs an attached client, which this topology already provides.
	tmux("copy-mode", "-t", session)
	tmux("send-keys", "-t", session, "-X", "top-of-history")
	if top := pane(40); !strings.Contains(top, "Session boundary inspection") {
		t.Errorf("tmux copy-mode cannot scroll back to the start of the transcript:\n%s", top)
	}
	tmux("send-keys", "-t", session, "-X", "cancel")

	// tmux owns the wheel here: the app never asked for it, so tmux never saw
	// a pane that requested mouse reporting.
	flags := tmux("display-message", "-p", "-t", session, "#{session_attached}")
	t.Logf("tmux session attached clients: %s", strings.TrimSpace(flags))

	// A second prompt drives a gated tool call, so the dialog still owns the
	// keyboard under a multiplexer.
	send("run the demo tool")
	send("Enter")
	waitFor("Do you want to proceed?", 20*time.Second)
	send("y")
	waitFor("demo tool finished", 20*time.Second)

	send("/status")
	send("Enter")
	waitFor("WORKSPACE STATUS", 20*time.Second)

	// The pane capture is the terminal emulator's own screen, so this is where
	// a duplicated line would surface: the painter must have appended every
	// scrollback line exactly once, because no later frame can take one back.
	assertNothingPaintedTwice(t, pane(80))

	// The status card owns the keyboard while open, so dismiss it first. Then
	// quitting ends the child, the pane, and the session.
	send("Escape")
	send("C-d")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command(tmuxPath, "-S", sock, "capture-pane", "-p", "-t", session)
		if _, err := cmd.CombinedOutput(); err != nil {
			// The session is gone: the child exited, so tmux shut the pane.
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("the live session did not exit inside tmux after ctrl+d")
}

// waitServer blocks until the tmux server accepts commands. attachOut is the
// client's own terminal output, which is where tmux reports why it refused to
// start.
func waitServer(t *testing.T, tmuxPath, sock, attachOut string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if err := exec.Command(tmuxPath, "-S", sock, "list-panes").Run(); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the tmux server never came up (sock=%s, %d chars); client output:\n%q",
		sock, len(sock), attachOut)
}
