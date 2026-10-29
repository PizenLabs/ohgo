package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// testContext returns a tool context backed by a background context.
func testContext() agent.Context {
	return agent.Context{Ctx: context.Background()}
}

func TestReadToolNumbersLinesAndPagesWithOffset(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(root)
	result := tool.Execute(testContext(), map[string]any{"path": "a.go"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	want := "1\tone\n2\ttwo\n3\tthree\n4\tfour"
	if result.Text != want {
		t.Errorf("text = %q, want %q", result.Text, want)
	}

	// A trailing newline must not create a phantom fifth line.
	if got := result.Details.(map[string]any)["totalLines"]; got != 4 {
		t.Errorf("totalLines = %v, want 4", got)
	}
}

func TestReadToolRejectsOffsetPastEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewReadTool(root).Execute(testContext(), map[string]any{
		"path":   "a.go",
		"offset": 99.0,
	})
	if !result.IsError {
		t.Fatal("expected an error for an offset past the end of the file")
	}
	if !strings.Contains(result.Text, "past the end") {
		t.Errorf("error = %q, want a past-the-end message", result.Text)
	}
}

func TestReadToolReportsDirectoryAndMissingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.go"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(root)
	result := tool.Execute(testContext(), map[string]any{"path": "sub"})
	if !result.IsError {
		t.Error("expected a directory to be rejected")
	}
	if !strings.Contains(result.Text, "a.go") {
		t.Errorf("directory error should list entries, got %q", result.Text)
	}
	if result := tool.Execute(testContext(), map[string]any{"path": "missing.go"}); !result.IsError {
		t.Error("expected a missing file to be rejected")
	}
}

func TestReadToolRefusesPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewReadTool(root).Execute(testContext(), map[string]any{"path": outside})
	if !result.IsError {
		t.Fatal("expected a path outside the root to be rejected")
	}
	if !strings.Contains(result.Text, "escapes") {
		t.Errorf("error = %q, want an escape message", result.Text)
	}
}

func TestReadToolHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewReadTool(root).Execute(agent.Context{Ctx: ctx}, map[string]any{"path": "a.go"})
	if !result.IsError {
		t.Fatal("expected a cancelled context to fail")
	}
}

func TestWriteToolCreatesFileAndParents(t *testing.T) {
	root := t.TempDir()
	result := NewWriteTool(root).Execute(testContext(), map[string]any{
		"path":    "nested/dir/a.go",
		"content": "package main\n",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}

	data, err := os.ReadFile(filepath.Join(root, "nested", "dir", "a.go"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "package main\n" {
		t.Errorf("contents = %q, want %q", data, "package main\n")
	}
}

func TestWriteToolRequiresContent(t *testing.T) {
	root := t.TempDir()
	result := NewWriteTool(root).Execute(testContext(), map[string]any{"path": "a.go"})
	if !result.IsError {
		t.Fatal("expected a missing content argument to fail")
	}
	if !strings.Contains(result.Text, "content") {
		t.Errorf("error = %q, want a missing-content message", result.Text)
	}
}

func TestEditToolReplacesUniqueString(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewEditTool(root).Execute(testContext(), map[string]any{
		"path":       "a.go",
		"old_string": "func old() {}",
		"new_string": "func renamed() {}",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "func renamed() {}") {
		t.Errorf("file = %q, want the replacement", data)
	}
	if strings.Contains(string(data), "func old() {}") {
		t.Errorf("file = %q, still contains the original", data)
	}
}

func TestEditToolRefusesAmbiguousMatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("x\nx\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewEditTool(root).Execute(testContext(), map[string]any{
		"path":       "a.go",
		"old_string": "x",
		"new_string": "y",
	})
	if !result.IsError {
		t.Fatal("expected an ambiguous match to be refused")
	}
	if !strings.Contains(result.Text, "3 times") {
		t.Errorf("error = %q, want a count of 3", result.Text)
	}

	// Refusing must leave the file untouched.
	data, _ := os.ReadFile(path)
	if string(data) != "x\nx\nx\n" {
		t.Errorf("file was modified: %q", data)
	}
}

func TestEditToolRefusesMissingAndIdenticalStrings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(root)

	if result := tool.Execute(testContext(), map[string]any{
		"path": "a.go", "old_string": "absent", "new_string": "x",
	}); !result.IsError {
		t.Error("expected a missing old_string to fail")
	}

	if result := tool.Execute(testContext(), map[string]any{
		"path": "a.go", "old_string": "same", "new_string": "same",
	}); !result.IsError {
		t.Error("expected identical strings to fail")
	}

	if result := tool.Execute(testContext(), map[string]any{
		"path": "a.go", "old_string": "", "new_string": "x",
	}); !result.IsError {
		t.Error("expected an empty old_string to fail")
	}
}

func TestBashToolReturnsOutputAndExitCode(t *testing.T) {
	root := t.TempDir()
	result := NewBashTool(root).Execute(testContext(), map[string]any{"command": "echo hello"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "hello") {
		t.Errorf("text = %q, want the command output", result.Text)
	}
	if !strings.Contains(result.Text, "exit code 0") {
		t.Errorf("text = %q, want exit code 0", result.Text)
	}
}

func TestBashToolMarksNonZeroExitAsError(t *testing.T) {
	root := t.TempDir()
	result := NewBashTool(root).Execute(testContext(), map[string]any{
		"command": "echo out; echo err >&2; exit 3",
	})
	if !result.IsError {
		t.Fatal("expected a non-zero exit to be an error")
	}
	if !strings.Contains(result.Text, "out") || !strings.Contains(result.Text, "err") {
		t.Errorf("text = %q, want both stdout and stderr", result.Text)
	}
	if !strings.Contains(result.Text, "exit code 3") {
		t.Errorf("text = %q, want exit code 3", result.Text)
	}
}

func TestBashToolTimesOut(t *testing.T) {
	root := t.TempDir()
	result := NewBashTool(root).Execute(testContext(), map[string]any{
		"command": "sleep 30",
		"timeout": 150.0,
	})
	if !result.IsError {
		t.Fatal("expected a timeout to be an error")
	}
	if !strings.Contains(result.Text, "timed out") {
		t.Errorf("text = %q, want a timeout message", result.Text)
	}
}

func TestBashToolRunsInRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := NewBashTool(root).Execute(testContext(), map[string]any{"command": "cat marker.txt"})
	if !strings.Contains(result.Text, "here") {
		t.Errorf("text = %q, want the working directory to be root", result.Text)
	}
}

func TestBashToolTruncatesLargeOutput(t *testing.T) {
	root := t.TempDir()
	// Produce more output than the cap allows.
	result := NewBashTool(root).Execute(testContext(), map[string]any{
		"command": "head -c 100000 /dev/zero | tr '\\0' 'a'",
	})
	if !strings.Contains(result.Text, "truncated") {
		t.Errorf("text = %q, want a truncation notice", result.Text)
	}
	if len(result.Text) > BashMaxOutput+200 {
		t.Errorf("text length = %d, want at most %d", len(result.Text), BashMaxOutput+200)
	}
}

func TestToolSchemasRejectBadArguments(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		tool agent.Tool
		args map[string]any
	}{
		{"read without path", NewReadTool(root), map[string]any{}},
		{"read with non-string path", NewReadTool(root), map[string]any{"path": 12.0}},
		{"write without content", NewWriteTool(root), map[string]any{"path": "a"}},
		{"edit without old_string", NewEditTool(root), map[string]any{"path": "a", "new_string": "b"}},
		{"bash with non-string command", NewBashTool(root), map[string]any{"command": 5.0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := agent.ValidateArgs(tc.tool, tc.args)
			if err == nil {
				t.Fatal("expected schema validation to fail")
			}
		})
	}
}

func TestToolApprovalTiers(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		tool agent.Tool
		want agent.Tier
	}{
		{"read", NewReadTool(root), agent.TierRead},
		{"write", NewWriteTool(root), agent.TierWrite},
		{"edit", NewEditTool(root), agent.TierWrite},
		{"bash", NewBashTool(root), agent.TierExec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tool.Approval().Tier; got != tc.want {
				t.Errorf("tier = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBashToolIsExclusive(t *testing.T) {
	root := t.TempDir()
	if !NewBashTool(root).Exclusive(nil) {
		t.Error("bash must run alone")
	}
	if NewReadTool(root).Exclusive(nil) {
		t.Error("read should be shareable")
	}
}

func TestBashStreamsProgress(t *testing.T) {
	// A command that runs longer than the throttle interval must produce at
	// least one preview before it exits. Without streaming, a slow command
	// shows nothing at all until it finishes.
	root := t.TempDir()
	var (
		mu       sync.Mutex
		previews []string
	)
	ctx := agent.Context{
		Ctx: context.Background(),
		OnProgress: func(r agent.Result) {
			mu.Lock()
			previews = append(previews, r.Text)
			mu.Unlock()
		},
	}

	// Emit output, then sleep past the throttle window, then emit more.
	result := NewBashTool(root).Execute(ctx, map[string]any{
		"command": "echo first; sleep 0.4; echo last",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(previews) == 0 {
		t.Fatal("no progress previews were emitted for a command that ran for 0.4s")
	}
	// The previews must come from the command's own output.
	if !strings.Contains(strings.Join(previews, "\n"), "first") {
		t.Errorf("previews did not contain the command output: %v", previews)
	}
}

func TestBashWorksWithoutAProgressCallback(t *testing.T) {
	// The callback is optional; a caller with no UI must still get the output.
	root := t.TempDir()
	result := NewBashTool(root).Execute(testContext(), map[string]any{"command": "echo hello"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "hello") {
		t.Errorf("output = %q, want the command output", result.Text)
	}
}

func TestBashCombinesStdoutAndStderrInPreviews(t *testing.T) {
	// The final output interleaves both streams, so a preview must show both:
	// a preview carrying only one stream would not match what arrives at the
	// end, and the user would see output appear from nowhere.
	root := t.TempDir()
	var (
		mu       sync.Mutex
		previews []string
	)
	ctx := agent.Context{
		Ctx: context.Background(),
		OnProgress: func(r agent.Result) {
			mu.Lock()
			previews = append(previews, r.Text)
			mu.Unlock()
		},
	}

	// Write stderr, wait past the throttle window, then write stdout, so both
	// streams are guaranteed to be inside a single preview.
	result := NewBashTool(root).Execute(ctx, map[string]any{
		"command": "echo err >&2; sleep 0.4; echo out",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(previews) == 0 {
		t.Fatal("no previews were emitted")
	}
	// The latest preview carries everything written so far, which is what a
	// UI shows as the command runs.
	last := previews[len(previews)-1]
	if !strings.Contains(last, "err") {
		t.Errorf("stderr missing from the preview: %q", last)
	}
	if !strings.Contains(last, "out") {
		t.Errorf("stdout missing from the preview: %q", last)
	}
}

func TestEditToolAcceptANearMatchAndSaySo(t *testing.T) {
	// The value over strings.Replace: a model that mis-transcribes one
	// character of a long line gets the edit applied rather than a failure —
	// and it must be told the match was approximate.
	root := t.TempDir()
	real := "\tthis is a reasonably long line of source code here\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(real), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewEditTool(root).Execute(testContext(), map[string]any{
		"path":       "a.go",
		"old_string": "\tthis is a reasonably long line of source code herf",
		"new_string": "\treplaced",
	})
	if result.IsError {
		t.Fatalf("a near match was refused: %s", result.Text)
	}
	// The approximate match must be reported, or a later reader cannot tell
	// that the file holds slightly different text than was requested.
	if !strings.Contains(result.Text, "approximately") {
		t.Errorf("an approximate match was silent: %s", result.Text)
	}

	data, err := os.ReadFile(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\treplaced") {
		t.Errorf("file = %q, want the replacement applied", data)
	}
}

func TestEditToolSuggestsTheClosestMatch(t *testing.T) {
	// A failed edit must offer a concrete suggestion: "not found" alone forces
	// the model to re-read the whole file to find what it got wrong.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("\tthis is a reasonably long line of source code here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewEditTool(root).Execute(testContext(), map[string]any{
		"path":       "a.go",
		"old_string": "\tthis is a reasonably long line of source code herf",
		"new_string": "\treplaced",
	})
	_ = result

	// A target that is genuinely unrelated must fail with a closest match.
	unrelated := NewEditTool(root).Execute(testContext(), map[string]any{
		"path":       "a.go",
		"old_string": "something completely different\n",
		"new_string": "\treplaced",
	})
	if !unrelated.IsError {
		t.Fatal("an unrelated target was accepted")
	}
	if !strings.Contains(unrelated.Text, "closest match") {
		t.Errorf("error = %q, want a closest-match suggestion", unrelated.Text)
	}
}

func TestEditToolStillRefusesAnAmbiguousMatch(t *testing.T) {
	// Adding fuzzy matching must not weaken the ambiguity rule: guessing which
	// occurrence a model meant would silently corrupt the wrong line.
	root := t.TempDir()
	body := "alpha\nbeta\ngamma\nbeta\ndelta\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewEditTool(root).Execute(testContext(), map[string]any{
		"path":       "a.go",
		"old_string": "beta",
		"new_string": "BETA",
	})
	if !result.IsError {
		t.Fatal("an ambiguous match was resolved")
	}
	if !strings.Contains(result.Text, "2 times") {
		t.Errorf("error = %q, want the occurrence count", result.Text)
	}

	// The file must be untouched.
	data, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(data) != body {
		t.Errorf("the file was modified by a refused edit: %q", data)
	}
}
