package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/execution/sandbox"
)

func TestParseEscape(t *testing.T) {
	tests := []struct {
		line    string
		command string
		ok      bool
	}{
		{"!git status", "git status", true},
		{"! go test ./...", "go test ./...", true},
		{"!go test ./...", "go test ./...", true},
		{"  !ls", "ls", true},
		{"!", "", true},
		{"!  ", "", true},
		{"!!echo", "!echo", true},
		{"go test ./...", "", false},
		{"", "", false},
		{"/model", "", false},
		{"@file.go", "", false},
		{"hello !world", "", false},
	}
	for _, tc := range tests {
		command, ok := ParseEscape(tc.line)
		if ok != tc.ok || command != tc.command {
			t.Errorf("ParseEscape(%q) = (%q, %v), want (%q, %v)", tc.line, command, ok, tc.command, tc.ok)
		}
	}
}

func TestDecideGatesMutationOnAuthority(t *testing.T) {
	tests := []struct {
		mode     string
		mutating bool
		want     Action
	}{
		{"BUILD", true, Run},
		{"BUILD", false, Run},
		{"FUZZ", true, Run},
		{"FUZZ", false, Run},
		{"ASK", true, Confirm},
		{"ASK", false, Run},
		{"ask", true, Confirm},
		{" build ", true, Run},
		{"", true, Confirm},
		{"", false, Run},
		{"UNKNOWN", true, Confirm},
	}
	for _, tc := range tests {
		if got := Decide(tc.mode, tc.mutating); got != tc.want {
			t.Errorf("Decide(%q, %v) = %v, want %v", tc.mode, tc.mutating, got, tc.want)
		}
	}
}

func TestRunCapturesAndStreamsCombinedOutput(t *testing.T) {
	var streamed strings.Builder
	runner := ShellRunner{Dir: t.TempDir()}
	result, err := runner.Run(context.Background(),
		`printf 'out\n'; printf 'err\n' 1>&2; exit 0`,
		func(chunk string) { streamed.WriteString(chunk) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Output, "out") || !strings.Contains(result.Output, "err") {
		t.Errorf("Output = %q, want both stdout and stderr captured", result.Output)
	}
	// The streamed chunks must reproduce the captured output exactly: a chunk
	// that is dropped or duplicated would desync the live block from the
	// final result.
	if streamed.String() != result.Output {
		t.Errorf("streamed = %q, captured = %q, want them identical", streamed.String(), result.Output)
	}
	if result.Duration <= 0 {
		t.Errorf("Duration = %v, want a measured elapsed time", result.Duration)
	}
}

func TestRunReportsNonZeroExitWithoutError(t *testing.T) {
	runner := ShellRunner{Dir: t.TempDir()}
	result, err := runner.Run(context.Background(), "exit 3", nil)
	if err != nil {
		t.Fatalf("Run: %v, want a non-zero exit reported as a result, not an error", err)
	}
	if result.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", result.ExitCode)
	}
}

func TestRunHonoursWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	runner := ShellRunner{Dir: dir}
	if _, err := runner.Run(context.Background(), ": > made.txt", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Errorf("stat made.txt: %v, want the command to run in Dir", err)
	}
}

func TestRunTruncatesAtRuneBoundary(t *testing.T) {
	runner := ShellRunner{Dir: t.TempDir(), MaxOutput: 10}
	result, err := runner.Run(context.Background(), "printf 'abcdefghijklmnop'", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want the cap reported")
	}
	if len(result.Output) > 10 {
		t.Errorf("Output = %q (%d bytes), want at most the cap", result.Output, len(result.Output))
	}
}

func TestRunCancellationAbortsPromptly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	runner := ShellRunner{Dir: t.TempDir()}
	start := time.Now()
	_, err := runner.Run(ctx, "sleep 5", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Errorf("elapsed = %v, want cancellation to kill the process early", elapsed)
	}
}

func TestRunRejectsAnEmptyCommand(t *testing.T) {
	runner := ShellRunner{Dir: t.TempDir()}
	if _, err := runner.Run(context.Background(), "   ", nil); err == nil {
		t.Error("Run(\"   \") = nil error, want an empty-command refusal")
	}
}

// The human shell escape uses the same authority-derived read-only boundary as
// the model's bash tool. A command spelled to write must not change the
// workspace, even though the classifier does not recognise the program.
func TestReadOnlyEscapeCannotMutate(t *testing.T) {
	if !sandbox.ReadOnlySupported() {
		t.Skip("no enforceable read-only sandbox on this platform")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := ShellRunner{Dir: dir, ReadOnly: true}
	_, _ = runner.Run(context.Background(), `python3 -c "open('file.txt','w').write('hacked')"`, nil)

	data, err := os.ReadFile(filepath.Join(dir, "file.txt"))
	if err != nil {
		t.Fatalf("the protected file disappeared: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("a read-only escape mutated the workspace: %q", data)
	}
}

func TestReadOnlyEscapeStillReads(t *testing.T) {
	if !sandbox.ReadOnlySupported() {
		t.Skip("no enforceable read-only sandbox on this platform")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := (ShellRunner{Dir: dir, ReadOnly: true}).Run(context.Background(), "cat file.txt", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Output, "original") {
		t.Fatalf("read-only escape did not return the file: %q", result.Output)
	}
}

func TestReadOnlyEscapeRequiresARoot(t *testing.T) {
	_, err := (ShellRunner{ReadOnly: true}).Run(context.Background(), "true", nil)
	if !errors.Is(err, sandbox.ErrNoRoot) {
		t.Fatalf("err = %v, want sandbox.ErrNoRoot", err)
	}
}
