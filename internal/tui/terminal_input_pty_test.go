package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real-terminal regression for the observed composer leak.
//
// The unit and decoder tests prove the recogniser; this one proves the whole
// alt-screen program under a pseudo-terminal: the terminal writes ESC, then
// the concatenated SGR reports, then ordinary typing, and the captured screen
// must contain the typing and none of the protocol bytes.
func TestAltScreenMouseReportDoesNotReachComposerUnderPTY(t *testing.T) {
	if os.Getenv("OHGO_ALT_PTY_CHILD") == "1" {
		runAltPTYChild()
		return
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skip("no script(1) to open a pseudo-terminal with")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := scriptPTY(fmt.Sprintf("stty rows %d cols %d; exec %q -test.run TestAltScreenMouseReportDoesNotReachComposerUnderPTY -test.timeout 60s",
		ptyRows, ptyCols, exe))
	cmd.Env = append(os.Environ(), "OHGO_ALT_PTY_CHILD=1", "TERM=xterm-256color")

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
	capture := func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen.String()
	}
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(stdin, s); err != nil {
			t.Fatalf("write %q: %v", s, err)
		}
	}
	waitFor := func(needle string, budget time.Duration) {
		t.Helper()
		for deadline := time.Now().Add(budget); time.Now().Before(deadline); {
			if strings.Contains(stripANSI(capture()), needle) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q:\n%s", needle, tail(stripANSI(capture())))
	}

	// Wait for the first painted frame (the hint bar is part of it), then send
	// the exact bytes that leaked: a lone ESC followed by three concatenated
	// reports. The pause forces the decoder to see the ESC on its own.
	waitFor("? help", 20*time.Second)
	write("\x1b")
	time.Sleep(40 * time.Millisecond)
	write("[<65;96;26M[<65;96;26M[<65;97;24M")

	// Ordinary typing must still work and be visible.
	write("MARK")
	waitFor("MARK", 5*time.Second)

	// Clear the draft so ctrl+d exits, then close the pty.
	for i := 0; i < len("MARK"); i++ {
		write("\x7f")
	}
	write("\x04")
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the session exited with %v", err)
	}

	out := stripANSI(capture())
	path := os.TempDir() + "/ohgo-alt-pty-capture.txt"
	_ = os.WriteFile(path, []byte(out), 0o600)
	for _, leaked := range []string{"[<65;96;26M", "[<65;97;24M"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("SGR mouse report reached the rendered screen (%q):\n%s", leaked, tail(out))
		}
	}
	if !strings.Contains(out, "MARK") {
		t.Fatalf("ordinary typing never reached the composer:\n%s", tail(out))
	}
}

// runAltPTYChild is the session half: the real alt-screen program with mouse
// reporting enabled, exactly the configuration that produced the leak.
func runAltPTYChild() {
	p := NewProgram(nil, "pty/pty-demo", 200_000, WithMouseReporting())
	if err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
	}
}
