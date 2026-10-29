package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// symbolsFixture writes a repository tree for structural-tool tests.
func symbolsFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// symbolCoord extracts one coordinate from tool details.
func symbolCoord(t *testing.T, result agent.Result, i int) map[string]any {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	coords, ok := details["coordinates"].([]map[string]any)
	if !ok {
		t.Fatalf("coordinates = %T, want []map[string]any", details["coordinates"])
	}
	if i >= len(coords) {
		t.Fatalf("only %d coordinates, want index %d", len(coords), i)
	}
	return coords[i]
}

func symbolStatus(t *testing.T, result agent.Result) string {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	status, _ := details["status"].(string)
	return status
}

const symbolsTestRepo = "package tools\n\n// GrepTool searches files.\ntype GrepTool struct{}\n\n// Execute runs the search.\nfunc (g GrepTool) Execute() {}\n"

func TestSymbolsToolFindsExactCoordinate(t *testing.T) {
	// The core Phase 1 promise: one lookup yields a coordinate precise
	// enough to open the declaration without any lexical searching.
	root := symbolsFixture(t, map[string]string{"tools/grep.go": symbolsTestRepo})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"symbol": "GrepTool.Execute"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if symbolStatus(t, result) != "ok" {
		t.Errorf("status = %q, want ok: %s", symbolStatus(t, result), result.Text)
	}
	coord := symbolCoord(t, result, 0)
	for key, want := range map[string]any{
		"path": "tools/grep.go", "package": "tools",
		"symbol": "GrepTool.Execute", "kind": "method",
	} {
		if coord[key] != want {
			t.Errorf("coordinate[%q] = %v, want %v", key, coord[key], want)
		}
	}
	if coord["start_line"] != 7 || coord["end_line"] != 7 {
		t.Errorf("lines = %v..%v, want 7..7", coord["start_line"], coord["end_line"])
	}
	if !strings.Contains(result.Text, "tools/grep.go") {
		t.Errorf("text omits the path: %s", result.Text)
	}
}

func TestSymbolsToolReportsAmbiguous(t *testing.T) {
	// Two declarations sharing a name must never collapse into one: the
	// tool reports ambiguity so the caller disambiguates explicitly.
	root := symbolsFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
		"b.go": "package main\n\nfunc alpha() {}\n",
	})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"symbol": "alpha"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if symbolStatus(t, result) != "ambiguous" {
		t.Errorf("status = %q, want ambiguous: %s", symbolStatus(t, result), result.Text)
	}
}

func TestSymbolsToolReportsNotFound(t *testing.T) {
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"symbol": "nope"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if symbolStatus(t, result) != "not_found" {
		t.Errorf("status = %q, want not_found", symbolStatus(t, result))
	}
	if !result.Useless {
		t.Error("an empty result should be marked useless so compaction can elide it")
	}
}

func TestSymbolsToolTruncatesLargeResults(t *testing.T) {
	// A bounded tool must say when it is bounded: the status and the totals
	// tell the model the listing is a page, not the whole set.
	var sb strings.Builder
	sb.WriteString("package main\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "\nfunc fn%02d() {}\n", i)
	}
	root := symbolsFixture(t, map[string]string{"a.go": sb.String()})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"limit": 2.0})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if symbolStatus(t, result) != "truncated" {
		t.Errorf("status = %q, want truncated: %s", symbolStatus(t, result), result.Text)
	}
	details := result.Details.(map[string]any)
	if details["returned"] != 2 || details["truncated"] != true {
		t.Errorf("details = %v, want returned=2 truncated=true", details)
	}
	if total, _ := details["results"].(int); total <= 2 {
		t.Errorf("total = %v, want the full unpaged count", details["results"])
	}
	if !strings.Contains(result.Text, "total:") {
		t.Errorf("text omits the total: %s", result.Text)
	}
}

func TestSymbolsToolListsFileSymbols(t *testing.T) {
	root := symbolsFixture(t, map[string]string{"tools/grep.go": symbolsTestRepo})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"path": "tools/grep.go"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if symbolStatus(t, result) != "ok" {
		t.Errorf("status = %q, want ok: %s", symbolStatus(t, result), result.Text)
	}
	if !strings.Contains(result.Text, "GrepTool.Execute") || !strings.Contains(result.Text, "GrepTool") {
		t.Errorf("file listing omits declared symbols: %s", result.Text)
	}
}

func TestSymbolsToolListsPackageSymbols(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"tools/a.go": "package tools\n\nfunc Alpha() {}\n",
		"tools/b.go": "package tools\n\nfunc Beta() {}\n",
		"other/c.go": "package other\n\nfunc Gamma() {}\n",
	})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"package": "tools"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	text := result.Text
	if !strings.Contains(text, "Alpha") || !strings.Contains(text, "Beta") {
		t.Errorf("package listing omits members: %s", text)
	}
	if strings.Contains(text, "Gamma") {
		t.Errorf("package listing leaked another package: %s", text)
	}
}

func TestSymbolsToolScopesDirectoryToItsFiles(t *testing.T) {
	// A directory listing must cover exactly the files beneath it. The
	// directory name is not a package query: here dir "victim" holds
	// package "other", while package "victim" lives in dir "a". Looking
	// up the package by the directory's name would return coordinates for
	// files outside the requested scope.
	root := symbolsFixture(t, map[string]string{
		"a/a.go":      "package victim\n\nfunc Alpha() {}\n",
		"victim/b.go": "package other\n\nfunc Beta() {}\n",
	})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"path": "victim"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "Beta") {
		t.Errorf("directory listing omits its own file: %s", result.Text)
	}
	if strings.Contains(result.Text, "Alpha") {
		t.Errorf("directory listing leaked a same-named package elsewhere: %s", result.Text)
	}
}

func TestSymbolsToolRejectsPathsOutsideRoot(t *testing.T) {
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	tool := NewSymbolsTool(root)

	for _, path := range []string{"../outside", "../../etc/passwd", "nope.go"} {
		if result := tool.Execute(testContext(), map[string]any{"path": path}); !result.IsError {
			t.Errorf("path %q should have failed", path)
		}
	}
	// An absolute path inside the root is a legitimate coordinate.
	inside := filepath.Join(root, "a.go")
	result := tool.Execute(testContext(), map[string]any{"path": inside})
	if result.IsError {
		t.Errorf("an in-root absolute path should resolve: %s", result.Text)
	}
}

func TestSymbolsToolRejectsSymlinkEscape(t *testing.T) {
	// resolvePath resolves the link before checking containment, so a
	// model-supplied path cannot use a symlink to leave the repository.
	outside := symbolsFixture(t, map[string]string{"secret.go": "package secret\n\nfunc Leaked() {}\n"})
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(filepath.Join(outside, "secret.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{"path": "link.go"})
	if !result.IsError {
		t.Fatalf("a symlink escaping the root should fail: %s", result.Text)
	}
	if got := NewSymbolsTool(root).Execute(testContext(), map[string]any{"symbol": "Leaked"}); symbolStatus(t, got) != "not_found" {
		t.Errorf("outside symbol leaked through a symlink: %s", got.Text)
	}
}

func TestSymbolsToolIsReadOnly(t *testing.T) {
	// Repository intelligence is evidence only: invoking it must neither
	// claim mutation authority nor touch the tree.
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	tool := NewSymbolsTool(root)
	if tool.Approval().Tier != agent.TierRead {
		t.Errorf("tier = %q, want read", tool.Approval().Tier)
	}

	snapshot := func() map[string]string {
		t.Helper()
		got := map[string]string{}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			got[path] = string(data)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return got
	}

	before := snapshot()
	for _, args := range []map[string]any{{"symbol": "alpha"}, {"path": "a.go"}, {}} {
		_ = tool.Execute(testContext(), args)
	}
	after := snapshot()
	if len(before) != len(after) {
		t.Fatal("executing the symbols tool changed the repository tree")
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("executing the symbols tool modified %s", path)
		}
	}
}

func TestSymbolsToolSeesDeletionAndCreation(t *testing.T) {
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	tool := NewSymbolsTool(root)

	if got := tool.Execute(testContext(), map[string]any{"symbol": "alpha"}); symbolStatus(t, got) != "ok" {
		t.Fatal("precondition: alpha resolves")
	}
	if err := os.Remove(filepath.Join(root, "a.go")); err != nil {
		t.Fatal(err)
	}
	if got := tool.Execute(testContext(), map[string]any{"symbol": "alpha"}); symbolStatus(t, got) != "not_found" {
		t.Errorf("deleted file still contributes a coordinate: %s", got.Text)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package main\n\nfunc beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tool.Execute(testContext(), map[string]any{"symbol": "beta"}); symbolStatus(t, got) != "ok" {
		t.Errorf("new file is not discoverable: %s", got.Text)
	}
}

func TestSymbolsToolWorksThroughSymlinkedRoot(t *testing.T) {
	// The tool receives the raw working directory. If that directory is a
	// symlink, lookups and file listings must still work under the same
	// repository-relative coordinates.
	realRoot := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	link := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := NewSymbolsTool(link)

	found := tool.Execute(testContext(), map[string]any{"symbol": "alpha"})
	if found.IsError || symbolStatus(t, found) != "ok" {
		t.Fatalf("lookup through a symlinked root: %s", found.Text)
	}
	file := tool.Execute(testContext(), map[string]any{"path": "a.go"})
	if file.IsError || symbolStatus(t, file) != "ok" {
		t.Fatalf("file listing through a symlinked root: %s", file.Text)
	}
}

func TestSymbolsToolOutputIsDeterministic(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"b.go": "package main\n\nfunc beta() {}\nfunc alpha() {}\n",
		"a.go": "package main\n\nfunc gamma() {}\n",
	})
	tool := NewSymbolsTool(root)

	first := tool.Execute(testContext(), map[string]any{"package": "main"}).Text
	second := tool.Execute(testContext(), map[string]any{"package": "main"}).Text
	if first != second {
		t.Errorf("repeated package listing differs:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestSymbolsToolConcurrentExecute(t *testing.T) {
	// The agent may issue tool calls concurrently. Lazy index construction
	// and the cached handle must be safe, and every call must see the same
	// coordinate.
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	tool := NewSymbolsTool(root)

	const workers = 8
	const iterations = 10
	errCh := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				result := tool.Execute(testContext(), map[string]any{"symbol": "alpha"})
				if result.IsError {
					errCh <- result.Text
					return
				}
				details, ok := result.Details.(map[string]any)
				if !ok {
					errCh <- "result details are not a map"
					return
				}
				if status, _ := details["status"].(string); status != "ok" {
					errCh <- result.Text
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for text := range errCh {
		t.Errorf("concurrent execute failed: %s", text)
	}
}

func TestSymbolsToolRefreshesStaleIndex(t *testing.T) {
	// The tool caches its index for speed; the cache must still observe
	// later edits, or lookups would serve stale coordinates.
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	tool := NewSymbolsTool(root)

	if got := tool.Execute(testContext(), map[string]any{"symbol": "alpha"}); symbolStatus(t, got) != "ok" {
		t.Fatal("precondition: alpha resolves")
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := tool.Execute(testContext(), map[string]any{"symbol": "alpha"}); symbolStatus(t, got) != "not_found" {
		t.Errorf("renamed symbol still resolves: %s", got.Text)
	}
	if got := tool.Execute(testContext(), map[string]any{"symbol": "beta"}); symbolStatus(t, got) != "ok" {
		t.Errorf("new symbol does not resolve: %s", got.Text)
	}
}

func TestSymbolsToolName(t *testing.T) {
	if got := NewSymbolsTool(t.TempDir()).Name(); got != "symbols" {
		t.Errorf("Name = %q, want the single structural surface symbols", got)
	}
}

func TestSymbolsToolScopesLookupByPath(t *testing.T) {
	// A path scope must be applied before ambiguity is decided: two
	// declarations exist, but only one is in the requested directory, so
	// the caller gets an exact coordinate rather than a false ambiguity.
	root := symbolsFixture(t, map[string]string{
		"a/a.go": "package a\n\nfunc alpha() {}\n",
		"b/b.go": "package b\n\nfunc alpha() {}\n",
	})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{
		"symbol": "alpha",
		"path":   "b",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if got := symbolStatus(t, result); got != "ok" {
		t.Errorf("status = %q, want ok: %s", got, result.Text)
	}
	coord := symbolCoord(t, result, 0)
	if coord["path"] != "b/b.go" {
		t.Errorf("path = %v, want b/b.go", coord["path"])
	}
	details := result.Details.(map[string]any)
	if details["results"] != 1 {
		t.Errorf("results = %v, want 1", details["results"])
	}
}

func TestSymbolsToolScopesPackageByPath(t *testing.T) {
	// Package names are not unique across directories. The path scope is
	// what keeps a package listing from merging two import paths that
	// happen to share a package clause.
	root := symbolsFixture(t, map[string]string{
		"a/a.go": "package tools\n\nfunc Alpha() {}\n",
		"b/b.go": "package tools\n\nfunc Beta() {}\n",
	})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{
		"package": "tools",
		"path":    "a",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "Alpha") {
		t.Errorf("scoped package listing omits Alpha: %s", result.Text)
	}
	if strings.Contains(result.Text, "Beta") {
		t.Errorf("scoped package listing leaked a same-named package: %s", result.Text)
	}
}

func TestSymbolsToolReportsParseFailures(t *testing.T) {
	// An unparsable file must not turn a partial index into a lying
	// not_found: the response carries the failure count.
	root := symbolsFixture(t, map[string]string{
		"good.go": "package main\n\nfunc alpha() {}\n",
		"bad.go":  "package broken\n\nfunc ( {\n",
	})
	tool := NewSymbolsTool(root)

	found := tool.Execute(testContext(), map[string]any{"symbol": "alpha"})
	if found.IsError {
		t.Fatalf("unexpected error: %s", found.Text)
	}
	if got := symbolStatus(t, found); got != "ok" {
		t.Errorf("status = %q, want ok: %s", got, found.Text)
	}
	details := found.Details.(map[string]any)
	if details["parse_failures"] != 1 {
		t.Errorf("parse_failures = %v, want 1", details["parse_failures"])
	}
	if !strings.Contains(found.Text, "unparsed: 1") {
		t.Errorf("text omits the parse failure: %s", found.Text)
	}

	missing := tool.Execute(testContext(), map[string]any{"symbol": "nope"})
	if got := symbolStatus(t, missing); got != "not_found" {
		t.Errorf("status = %q, want not_found", got)
	}
	if !strings.Contains(missing.Text, "unparsed: 1") {
		t.Errorf("not_found omits the parse failure: %s", missing.Text)
	}
	if missing.Useless {
		t.Error("a not_found over a partial index must not be marked useless")
	}
}

func TestSymbolsToolReportsAmbiguityWhenTruncated(t *testing.T) {
	// Truncation must not erase ambiguity. A lookup with two matches and a
	// one-row page is both ambiguous and truncated, and the text says so.
	root := symbolsFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
		"b.go": "package main\n\nfunc alpha() {}\n",
	})

	result := NewSymbolsTool(root).Execute(testContext(), map[string]any{
		"symbol": "alpha",
		"limit":  1.0,
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if got := symbolStatus(t, result); got != "ambiguous" {
		t.Errorf("status = %q, want ambiguous: %s", got, result.Text)
	}
	details := result.Details.(map[string]any)
	if details["truncated"] != true || details["results"] != 2 || details["returned"] != 1 {
		t.Errorf("details = %v, want truncated with results=2 returned=1", details)
	}
	for _, want := range []string{"total: 2", "truncated: true"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("text omits %q: %s", want, result.Text)
		}
	}
}

func TestSymbolsToolHonoursCancellation(t *testing.T) {
	root := symbolsFixture(t, map[string]string{"a.go": "package main\n\nfunc alpha() {}\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := NewSymbolsTool(root).Execute(agent.Context{Ctx: ctx}, map[string]any{"symbol": "alpha"})
	if !result.IsError {
		t.Fatal("expected a cancelled context to fail")
	}
}

func TestSymbolsToolSchemaIsClosed(t *testing.T) {
	// The tool has one input vocabulary. An old alias must be rejected by
	// the harness instead of being silently ignored and running a listing.
	tool := NewSymbolsTool(t.TempDir())
	if err := agent.ValidateArgs(tool, map[string]any{"symbol": "alpha", "limit": 10.0}); err != nil {
		t.Errorf("valid arguments rejected: %v", err)
	}
	if err := agent.ValidateArgs(tool, map[string]any{"operation": "file", "path": "a.go"}); err == nil {
		t.Error("an unknown argument was accepted")
	}
	if err := agent.ValidateArgs(tool, map[string]any{"symbol": ""}); err == nil {
		t.Error("an empty symbol was accepted")
	}
}
