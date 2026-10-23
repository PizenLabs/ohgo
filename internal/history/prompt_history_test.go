package history

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func closeOnCleanup(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

func readScope(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// TestAddWritesBothScopes is the core dual-write contract: one submission
// lands in the project file and the machine-wide file, creating both
// directories on the way.
func TestAddWritesBothScopes(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)

	store := Load(root)
	store.Add("explain the parser")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, ProjectDirName, FileName),
		filepath.Join(home, ".ohgo", FileName),
	} {
		if got := readScope(t, path); !reflect.DeepEqual(got, []string{"explain the parser"}) {
			t.Errorf("%s = %q, want the submitted prompt", path, got)
		}
	}
}

// TestLoadMergesWithProjectPriority pins both halves of the ordering rule:
// project history is visited first when walking backwards, and global entries
// already present locally are not duplicated.
func TestLoadMergesWithProjectPriority(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "project", FileName)
	global := filepath.Join(dir, "global", FileName)
	writeLines(t, local, "shared\nlocal one\nlocal two\n")
	writeLines(t, global, "global one\nshared\nglobal two\n")

	store := Open(local, global, 0)
	closeOnCleanup(t, store)

	want := []string{"global one", "global two", "shared", "local one", "local two"}
	if got := store.Entries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %q, want %q", got, want)
	}
	// Reverse traversal starts at the project tail, so the newest project
	// prompt is the first one recall reaches.
	if last := store.Entries()[len(store.Entries())-1]; last != "local two" {
		t.Fatalf("newest entry = %q, want the project prompt", last)
	}
}

// TestSuggestionPrefersProjectHistory proves autosuggestion follows the same
// priority as recall: a local match wins over a global match with the same
// prefix.
func TestSuggestionPrefersProjectHistory(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "project", FileName)
	global := filepath.Join(dir, "global", FileName)
	writeLines(t, global, "deploy global\n")
	writeLines(t, local, "deploy project\n")

	store := Open(local, global, 0)
	closeOnCleanup(t, store)

	if got := Suggestion(store.Entries(), "deploy"); got != "deploy project" {
		t.Fatalf("Suggestion = %q, want the project match", got)
	}
	if got := Suggestion(store.Entries(), "deploy g"); got != "deploy global" {
		t.Fatalf("Suggestion = %q, want the global fallback", got)
	}
}

// TestConsecutiveDuplicatesCollapseInEachScope proves holding enter cannot
// flood either file while a repeated prompt separated by another is kept.
func TestConsecutiveDuplicatesCollapseInEachScope(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "project", FileName)
	global := filepath.Join(dir, "global", FileName)
	store := Open(local, global, 0)

	for _, prompt := range []string{"a", "a", "b", "a"} {
		store.Add(prompt)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got, want := store.Entries(), []string{"a", "b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %q, want %q", got, want)
	}
	for _, path := range []string{local, global} {
		if got := readScope(t, path); !reflect.DeepEqual(got, []string{"a", "b", "a"}) {
			t.Errorf("%s = %q, want a,b,a", path, got)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestEmptyPromptsAreIgnored protects the file from blank submits.
func TestEmptyPromptsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	store := Open(filepath.Join(dir, "local", FileName), filepath.Join(dir, "global", FileName), 0)
	closeOnCleanup(t, store)

	store.Add("")
	store.Add("   \n")
	if got := store.Entries(); len(got) != 0 {
		t.Fatalf("Entries() = %q, want none", got)
	}
}

// TestMultilinePromptRoundTripsAsOneEntry protects recall: a prompt entered
// with alt+enter must come back whole, not as one entry per line, in both
// scopes.
func TestMultilinePromptRoundTripsAsOneEntry(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "project", FileName)
	global := filepath.Join(dir, "global", FileName)
	want := "first line\nsecond line with \\ and \\n literal"

	store := Open(local, global, 0)
	store.Add(want)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Fatalf("file has %d newlines, want one entry on one line: %q", lines, data)
	}

	reopened := Open(local, global, 0)
	closeOnCleanup(t, reopened)
	if got := reopened.Entries(); len(got) != 1 || got[0] != want {
		t.Fatalf("Entries() = %q, want [%q]", got, want)
	}
}

// TestHistoryIsBounded keeps each scope from growing without limit; the newest
// entries are the ones that survive.
func TestHistoryIsBounded(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "project", FileName)
	store := Open(local, "", 3)

	for _, prompt := range []string{"one", "two", "three", "four", "five"} {
		store.Add(prompt)
	}
	if got, want := store.Entries(), []string{"three", "four", "five"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %q, want %q", got, want)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := readScope(t, local); !reflect.DeepEqual(got, []string{"three", "four", "five"}) {
		t.Fatalf("file = %q, want the bounded tail", got)
	}
}

// TestUnwritableScopeFallsBackToMemory proves a permission/path failure is not
// a session failure: prompts still recall for this run and no panic escapes.
func TestUnwritableScopeFallsBackToMemory(t *testing.T) {
	dir := t.TempDir()
	// Opening a directory as the append target fails for every write path.
	store := Open(dir, "", 0)
	t.Cleanup(func() {
		// The write failure is the point of this test, so Close's report is
		// expected rather than a test failure.
		_ = store.Close()
	})

	store.Add("still available")
	if got, want := store.Entries(), []string{"still available"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %q, want %q", got, want)
	}
	// Flush reports the failure to an explicit caller, but Add never panicked
	// and the in-memory history stayed usable.
	_ = store.Flush()
}

func TestPathsMatchTheDualScopeLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	global, err := GlobalPath()
	if err != nil {
		t.Fatalf("GlobalPath: %v", err)
	}
	if want := filepath.Join(home, ".ohgo", FileName); global != want {
		t.Fatalf("GlobalPath() = %q, want %q", global, want)
	}
	if got, want := ProjectPath("/repo"), filepath.Join("/repo", ".ohgo", FileName); got != want {
		t.Fatalf("ProjectPath = %q, want %q", got, want)
	}
	if got := ProjectPath(""); got != "" {
		t.Fatalf("ProjectPath(empty) = %q, want no project scope", got)
	}
}

// TestSuggestionPrefersNewestPrefix pins the ghost-text lookup: the tail comes
// from the newest matching entry, exact matches and multi-line entries are not
// suggestions, and an empty prefix never proposes anything.
func TestSuggestionPrefersNewestPrefix(t *testing.T) {
	entries := []string{
		"deploy the docs",
		"multi\nline deploy",
		"deploy production",
		"deploy production",
		"deploy",
	}
	if got := Suggestion(entries, "deploy p"); got != "deploy production" {
		t.Errorf("Suggestion = %q, want the newest prefix match", got)
	}
	if got := Suggestion(entries, "deploy"); got != "deploy production" {
		t.Errorf("Suggestion for an exact newer entry = %q, want the next older match", got)
	}
	if got := Suggestion(entries, ""); got != "" {
		t.Errorf("Suggestion with an empty prefix = %q, want none", got)
	}
	if got := Suggestion(entries, "missing"); got != "" {
		t.Errorf("Suggestion without a match = %q, want none", got)
	}
	if got := Suggestion([]string{"multi\nline deploy"}, "multi"); got != "" {
		t.Errorf("Suggestion of a multi-line entry = %q, want none", got)
	}
}

func writeLines(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
