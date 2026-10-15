package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/tools"
)

// Real-terminal evidence for the output-first mode.
//
// A unit test can only show what a writer emits; it cannot show that a
// terminal is left free to scroll, select and copy. This one runs a whole
// live session under a real pseudo-terminal — real raw mode, real winsize,
// real keystrokes — and captures every byte the program writes. That byte
// stream is the evidence that ohgo asks the terminal for nothing it should
// not, and that a streamed answer really does arrive.
//
// The child half of the test is the session; the parent half drives it the way
// a person at a keyboard would.

const (
	// ptyRows and ptyCols are the winsize the harness installs on the slave.
	ptyRows = 40
	ptyCols = 100
)

// TestLiveSessionUnderARrealPTY drives a whole live session through a real
// pseudo-terminal and asserts on the exact bytes the program writes.
func TestLiveSessionUnderARrealPTY(t *testing.T) {
	if os.Getenv("OHGO_LIVE_PTY_CHILD") == "1" {
		runPTYChild()
		return
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skip("no script(1) to open a pseudo-terminal with")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := scriptPTY(fmt.Sprintf("stty rows %d cols %d; exec %q -test.run TestLiveSessionUnderARrealPTY -test.timeout 120s",
		ptyRows, ptyCols, exe))
	cmd.Env = append(os.Environ(), "OHGO_LIVE_PTY_CHILD=1", "TERM=xterm-256color")

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
		// Keep the capture even on failure: it is the evidence.
		path := filepath.Join(os.TempDir(), "ohgo-live-pty-capture.txt")
		_ = os.WriteFile(path, []byte(raw), 0o600)
		t.Fatalf("timed out waiting for %q in the terminal output (capture: %s):\n%s",
			needle, path, tail(stripANSI(raw)))
	}

	// The session starts: a live region with a composer is the first thing.
	waitFor("? help", 20*time.Second)

	// Type a mouse-report-shaped string. Without mouse reporting it is prose,
	// so it must come back byte for byte.
	write("look at [<64;67;30M here")
	waitFor("look at [<64;67;30M here", 5*time.Second)
	for i := 0; i < 5; i++ {
		write("\x7f") // backspace over " here"
	}
	write("\r")

	// A streamed answer arrives carrying every markdown shape the prototype
	// has to survive.
	waitFor("Session boundary inspection", 20*time.Second)
	waitFor("```go", 20*time.Second)
	waitFor("sessionCheck", 20*time.Second)
	waitFor("│ a │ b │", 20*time.Second)
	waitFor("Closing line.", 20*time.Second)

	// A second prompt asks for a tool call, which blocks on an approval.
	write("run the demo tool\r")
	waitFor("Do you want to proceed?", 20*time.Second)
	write("y")

	// The outcome lands, then the second answer streams.
	waitFor("demo tool finished", 20*time.Second)
	waitFor("tool ran and the answer", 20*time.Second)

	// A slash command works in live mode too.
	write("/status\r")
	waitFor("model: pty/pty-demo", 20*time.Second)

	write("\x04") // ctrl+d quits an idle session
	_ = stdin.Close()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the session exited with %v", err)
	}
	mu.Lock()
	out := seen.String()
	mu.Unlock()

	// The capture is the evidence for this mode, so keep it even on success.
	path := filepath.Join(os.TempDir(), "ohgo-live-pty-capture.txt")
	if err := os.WriteFile(path, []byte(out), 0o600); err == nil {
		t.Logf("terminal capture written to %s", path)
	}

	assertNoTerminalHijack(t, out)
	assertTranscriptArrived(t, out)
}

// writerFunc adapts a func to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// scriptPTY returns script(1) running command on a pseudo-terminal.
//
// The two script(1) implementations disagree on their argument shape: the
// BSD script (macOS and the BSDs) takes the command positionally after the
// file, while util-linux script (Linux, including CI) takes it with -c and
// the file last. Handing the BSD form to util-linux fails outright with
// "unexpected number of arguments" and no child runs, so the harness builds
// argv for its host.
func scriptPTY(command string) *exec.Cmd {
	if runtime.GOOS == "linux" {
		return exec.Command("/usr/bin/script", "-q", "-c", command, "/dev/null")
	}
	return exec.Command("/usr/bin/script", "-q", "/dev/null", "/bin/sh", "-c", command)
}

// assertNoTerminalHijack checks the property the whole mode rests on: nothing
// in the byte stream asks the terminal for the wheel, for drag selection, or
// for its own screen.
func assertNoTerminalHijack(t *testing.T, out string) {
	t.Helper()
	// Only the enabling form is a request. Bubble Tea's own shutdown emits
	// the disabling forms unconditionally, which turns off a mode that was
	// never switched on and costs the user nothing.
	for _, seq := range mouseModeSequences {
		if !strings.HasSuffix(seq, "h") {
			continue
		}
		if strings.Contains(out, seq) {
			t.Errorf("the session requested %q, which takes the wheel and drag selection from the terminal", seq)
		}
	}
	for _, seq := range []string{"\x1b[?1049h", "\x1b[?1049l"} {
		if strings.Contains(out, seq) {
			t.Errorf("the session switched the alternate screen with %q, which removes the terminal's scrollback", seq)
		}
	}
	// The only mouse bytes permitted are those unconditional teardown forms.
	var mouseBytes strings.Builder
	for i := 0; i+1 < len(out); i++ {
		if out[i] != 0x1b || out[i+1] != '[' {
			continue
		}
		if j := strings.IndexByte(out[i:], 'l'); j > 0 && j < 12 && strings.HasPrefix(out[i:], "\x1b[?10") {
			mouseBytes.WriteString(out[i : i+j+1])
		}
	}
	if got := mouseBytes.String(); got != "" && got != strings.Repeat("", 0) {
		for _, seq := range []string{"\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l"} {
			for strings.Contains(got, seq) {
				got = strings.Replace(got, seq, "", 1)
			}
		}
		if got != "" {
			t.Errorf("unexpected mouse bytes in the stream: %q", got)
		}
	}
}

// assertTranscriptArrived checks the answer really reached the terminal, in
// order. A phrase may legitimately repeat in the byte stream: a live row is
// repainted on every state change, and the terminal erases it again before
// the row is ever sealed. Duplication in the terminal's *state* is what
// matters, and that is asserted against a real emulator in the tmux test.
func assertTranscriptArrived(t *testing.T, out string) {
	t.Helper()
	plain := stripANSI(out)
	for _, phrase := range []string{
		"Session boundary inspection",
		"The session check moved to",
		"```go",
		"sessionCheck",
		"│ a │ b │",
		"│ 1 │ 2 │",
		"Closing line.",
	} {
		if !strings.Contains(plain, phrase) {
			t.Errorf("%q never reached the terminal:\n%s", phrase, tail(plain))
		}
	}
	order := []string{"Session boundary inspection", "Here is the relevant part:", "Closing line."}
	last := -1
	for _, phrase := range order {
		at := strings.Index(plain, phrase)
		if at < last {
			t.Errorf("%q reached the terminal out of order", phrase)
		}
		last = at
	}
}

// assertNothingPaintedTwice checks the terminal's own state: a pane capture is
// the emulator's screen, so a phrase appearing twice there means the painter
// appended a line that was already on screen, which no later frame can take
// back.
func assertNothingPaintedTwice(t *testing.T, paneText string) {
	t.Helper()
	for _, phrase := range []string{
		"Session boundary inspection",
		"sessionCheck",
		"demo tool finished",
		"tool ran and the answer",
	} {
		if n := strings.Count(paneText, phrase); n != 1 {
			t.Errorf("%q is on the terminal %d times, want exactly once:\n%s", phrase, n, paneText)
		}
	}
}

// ptyTool is the tool the demo model calls, gated on approval so the run
// proves the dialog still owns the keyboard in output-first mode.
type ptyTool struct {
	tools.Base
}

func newPTYTool() ptyTool {
	return ptyTool{Base: tools.NewBase("demo", "a tool for the pty demo",
		map[string]any{"type": "object", "properties": map[string]any{}},
		agent.Approval{Tier: agent.TierExec, Reason: "runs a shell command"})}
}

func (t ptyTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	return agent.Result{Text: "demo tool finished"}
}

// ptyAnswer is the streamed first answer, carrying every markdown shape the
// prototype has to survive.
var ptyAnswer = []string{
	"# Session boundary inspection\n\n",
	"The session check moved to `auth/login.go`.\n\n",
	"Here is the relevant part:\n\n",
	"```go\n",
	"func sessionCheck(u User) bool {\n",
	"\treturn u.ID != \"\"\n",
	"}\n",
	"```\n\n",
	"| a | b |\n|---|---|\n| 1 | 2 |\n\n",
	"Closing line.\n",
}

// ptyScript is the demo model's reply sequence: prose, then a gated tool call,
// then prose again.
func ptyScript() ai.StreamFn {
	var calls int
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		calls++
		switch calls {
		case 1:
			return ai.NewTextStream(ptyAnswer...), nil
		case 2:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "call_1", Name: "demo", Arguments: map[string]any{},
				}},
			}), nil
		default:
			return ai.NewTextStream("The tool ran and the answer is complete."), nil
		}
	}
}

// runPTYChild is the session half: the real Program on the real terminal.
func runPTYChild() {
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "pty-demo", Provider: "pty", ContextWindow: 200_000},
		Stream: ptyScript(),
		Tools:  []agent.Tool{newPTYTool()},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "loop:", err)
		os.Exit(1)
	}
	p := NewProgram(loop, "pty/pty-demo", 200_000)
	// The CLI wires this; the demo has to too, or no tool call would ever
	// block on the user.
	loop.SetApprovalHook(p.ApprovalFunc())
	if err := p.RunLive(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
	}
}

// tail is the end of a long output, for a readable failure message.
func tail(s string) string {
	if len(s) > 3000 {
		return s[len(s)-3000:]
	}
	return s
}
