package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRipgrep writes a POSIX shell script that stands in for rg. The tests
// inject it through GrepTool.LookPath, so the backend protocol is exercised
// without depending on the host's ripgrep version.
func fakeRipgrep(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ripgrep uses a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "rg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// useFakeRipgrep points tool at body for the next execution.
func useFakeRipgrep(t *testing.T, tool *GrepTool, body string) {
	t.Helper()
	path := fakeRipgrep(t, body)
	tool.LookPath = func(string) (string, error) { return path, nil }
}

// rgMatchEvent renders one valid `rg --json` match event.
func rgMatchEvent(path string, line int, text string) string {
	return fmt.Sprintf(
		`{"type":"match","data":{"path":{"text":%q},"lines":{"text":%q},"line_number":%d}}`,
		path, text, line)
}

// rgEndEvent renders the event that closes a file. binary marks the file as
// binary, which is how rg reports a NUL byte after emitting any matches before
// it.
func rgEndEvent(path string, binary bool) string {
	offset := "null"
	if binary {
		offset = "7"
	}
	return fmt.Sprintf(`{"type":"end","data":{"path":{"text":%q},"binary_offset":%s}}`, path, offset)
}

// realRipgrepOrSkip reports the host rg path, skipping tests that need the
// real backend when the binary is absent.
func realRipgrepOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
}

// The production path must actually select rg when it is installed. The
// existing grep contract tests then run against the real backend, but this
// assertion makes the selection itself observable.
func TestGrepUsesRipgrepBackendWhenAvailable(t *testing.T) {
	realRipgrepOrSkip(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n// needle\n"})

	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	details, _ := result.Details.(map[string]any)
	if details["backend"] != "rg" {
		t.Fatalf("backend = %v, want rg; details = %#v", details["backend"], details)
	}
	if !strings.Contains(text, "a.go") {
		t.Fatalf("ripgrep result omitted the match: %s", text)
	}
}

// Absence of rg is a supported deployment, not a tool failure.
func TestGrepFallsBackToNativeWhenRipgrepIsAbsent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n// needle\n"})
	tool := NewGrepTool(root)
	tool.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "a.go") {
		t.Fatalf("native fallback missed the match: %s", text)
	}
	details, _ := result.Details.(map[string]any)
	if details["backend"] != "native" {
		t.Fatalf("backend = %v, want native; details = %#v", details["backend"], details)
	}
}

// A resolved path that no longer starts (uninstalled, permission denied) must
// take the same fallback instead of failing the search.
func TestGrepFallsBackWhenRipgrepCannotStart(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n// needle\n"})
	tool := NewGrepTool(root)
	tool.LookPath = func(string) (string, error) {
		return filepath.Join(root, "missing-rg"), nil
	}

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "a.go") {
		t.Fatalf("fallback after a failed start missed the match: %s", text)
	}
	details, _ := result.Details.(map[string]any)
	if details["backend"] != "native" {
		t.Fatalf("backend = %v, want native; details = %#v", details["backend"], details)
	}
}

// rg exit code 1 is "no matches", not an execution failure. Falling back to a
// shell grep here was the original false-negative loop.
func TestGrepTreatsRipgrepExitOneAsNoMatch(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "needle\n"})
	tool := NewGrepTool(root)
	useFakeRipgrep(t, tool, "exit 1\n")

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("exit 1 was treated as an error: %s", text)
	}
	if !strings.Contains(text, "No matches found.") {
		t.Fatalf("text = %q, want a no-match observation", text)
	}
	if !result.Useless {
		t.Error("an unannotated empty result should be marked useless")
	}
}

// Exit code 2 is a genuine failure. The diagnostic must reach the model; a
// swallowed stderr turns a bad regex or a broken backend into "no matches".
func TestGrepSurfacesRipgrepExitTwoStderr(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "needle\n"})
	tool := NewGrepTool(root)
	useFakeRipgrep(t, tool, "echo 'regex parse error: unclosed character class' >&2\nexit 2\n")

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if !result.IsError {
		t.Fatalf("exit 2 was not an error: %s", text)
	}
	if !strings.Contains(text, "regex parse error") {
		t.Fatalf("stderr was not surfaced: %s", text)
	}
}

// rg reports matches that precede a NUL byte before its end event marks the
// file binary. Those are not text matches and must be discarded.
func TestGrepDiscardsMatchesFromBinaryRipgrepFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"image.bin": "needle"})
	tool := NewGrepTool(root)
	events := []string{
		rgMatchEvent("image.bin", 1, "needle\n"),
		rgEndEvent("image.bin", true),
	}
	useFakeRipgrep(t, tool, "cat <<'JSON'\n"+strings.Join(events, "\n")+"\nJSON\nexit 0\n")

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if strings.Contains(text, "image.bin") || strings.Contains(text, "needle") {
		t.Fatalf("binary file match leaked into the result: %s", text)
	}
}

// The native scanner and rg must both skip binary files by default, so a
// search cannot present object-file bytes as source text.
func TestGrepSkipsBinaryFilesOnBothBackends(t *testing.T) {
	cases := []struct {
		name      string
		disableRG bool
	}{
		{name: "native", disableRG: true},
		{name: "ripgrep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.disableRG {
				realRipgrepOrSkip(t)
			}
			root := t.TempDir()
			writeTree(t, root, map[string]string{
				"bin.dat":  "needle before NUL\x00tail\n",
				"text.txt": "needle\n",
			})
			tool := NewGrepTool(root)
			if tc.disableRG {
				tool.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			}

			text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
			if result.IsError {
				t.Fatalf("unexpected error: %s", text)
			}
			if !strings.Contains(text, "text.txt") {
				t.Fatalf("the text file was not searched: %s", text)
			}
			if strings.Contains(text, "bin.dat") {
				t.Fatalf("a binary file was reported as text: %s", text)
			}
		})
	}
}

// The per-file cap is a tool contract, not a backend hint. A backend that
// ignores --max-count must still be capped by the decoder.
func TestGrepAppliesPerFileCapToRipgrepEvents(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	tool := NewGrepTool(root)
	var events []string
	for line := 1; line <= 25; line++ {
		events = append(events, rgMatchEvent("a.go", line, "needle\n"))
	}
	events = append(events, rgEndEvent("a.go", false))
	events = append(events, rgMatchEvent("b.go", 1, "needle\n"))
	events = append(events, rgEndEvent("b.go", false))
	useFakeRipgrep(t, tool, "cat <<'JSON'\n"+strings.Join(events, "\n")+"\nJSON\nexit 0\n")

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if got := strings.Count(text, "needle"); got != GrepMultiFileMatches+1 {
		t.Fatalf("match lines = %d, want %d", got, GrepMultiFileMatches+1)
	}
	if !strings.Contains(text, fmt.Sprintf("capped at %d", GrepMultiFileMatches)) {
		t.Fatalf("the per-file cap was not disclosed: %s", text)
	}
}

// The real backend must disclose that a file hit the per-file cap. Asking rg
// for exactly perFile lines would hide the evidence that more matches exist,
// so the invocation requests one extra line as the truncation probe.
func TestGrepDisclosesRipgrepPerFileTruncation(t *testing.T) {
	realRipgrepOrSkip(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": strings.Repeat("needle line\n", 25),
		"b.go": "needle line\n",
	})

	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if got := strings.Count(text, "needle line"); got != GrepMultiFileMatches+1 {
		t.Fatalf("match lines = %d, want %d (20 from a.go plus b.go)", got, GrepMultiFileMatches+1)
	}
	if !strings.Contains(text, fmt.Sprintf("capped at %d", GrepMultiFileMatches)) {
		t.Fatalf("the per-file cap was not disclosed: %s", text)
	}
}

// A match event beyond the native search window is not a match the native
// contract would have observed. The decoder must drop it and mark the file
// partial so the result is not presented as complete.
func TestGrepDropsRipgrepMatchBeyondSearchWindow(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"big.go": "package big\n"})
	tool := NewGrepTool(root)
	events := []string{
		fmt.Sprintf(
			`{"type":"match","data":{"path":{"text":"big.go"},"lines":{"text":"needle\n"},"line_number":1,"absolute_offset":%d}}`,
			GrepMaxFileBytes),
		rgEndEvent("big.go", false),
	}
	useFakeRipgrep(t, tool, "cat <<'JSON'\n"+strings.Join(events, "\n")+"\nJSON\nexit 0\n")

	text, result := grepText(t, tool, map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if strings.Contains(text, "needle") {
		t.Fatalf("a match beyond the search window was reported: %s", text)
	}
	if !strings.Contains(text, "search window") {
		t.Fatalf("a windowed file was not disclosed: %s", text)
	}
}
