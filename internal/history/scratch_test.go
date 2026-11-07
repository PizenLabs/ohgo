package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestShouldOffloadUsesEitherHybridThreshold pins the two independent triggers.
func TestShouldOffloadUsesEitherHybridThreshold(t *testing.T) {
	short := "one\ntwo\nthree"
	if ShouldOffload(short) {
		t.Fatal("a small prompt must stay in memory")
	}
	// 49 newlines is 50 lines.
	if ShouldOffload(strings.Repeat("line\n", ScratchLineThreshold-2) + "line") {
		t.Fatal("49 lines must not offload")
	}
	if !ShouldOffload(strings.Repeat("line\n", ScratchLineThreshold-1) + "line") {
		t.Fatal("50 lines must offload")
	}
	if ShouldOffload(strings.Repeat("x", ScratchRuneThreshold-1)) {
		t.Fatal("3999 runes must not offload")
	}
	if !ShouldOffload(strings.Repeat("x", ScratchRuneThreshold)) {
		t.Fatal("4000 runes must offload")
	}
	// Runes, not bytes: multibyte text is counted by characters.
	if !ShouldOffload(strings.Repeat("日", ScratchRuneThreshold)) {
		t.Fatal("4000 multibyte runes must offload")
	}
}

// TestScratchOffloadWritesExactPromptAndInjectsReadBackBytes proves the file
// is the payload source, not an external retrieval step.
func TestScratchOffloadWritesExactPromptAndInjectsReadBackBytes(t *testing.T) {
	root := t.TempDir()
	scratch := NewScratch(root)
	now := time.Unix(1_700_000_000, 123)
	scratch.now = func() time.Time { return now }

	prompt := "# Master prompt\n\n" + strings.Repeat("detail line\n", ScratchLineThreshold)
	path, text, offloaded, err := scratch.Offload(7, prompt)
	if err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if !offloaded {
		t.Fatal("a large prompt was not offloaded")
	}
	if text != prompt {
		t.Fatalf("injected text differs from the prompt:\ngot %q\nwant %q", text, prompt)
	}
	wantPath := filepath.Join(root, ".ohgo", "scratch", "prompt_7_1700000000000000123.md")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(onDisk) != prompt {
		t.Fatal("the scratch file does not contain the raw prompt")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("scratch mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestScratchLeavesSmallPromptInMemory is the negative bound: no directory and
// no file for a prompt under both thresholds.
func TestScratchLeavesSmallPromptInMemory(t *testing.T) {
	root := t.TempDir()
	scratch := NewScratch(root)
	small := strings.Repeat("line\n", 19) + strings.Repeat("x", 1500)
	path, text, offloaded, err := scratch.Offload(1, small)
	if err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if offloaded || path != "" || text != "" {
		t.Fatalf("small prompt was offloaded: path=%q text=%q", path, text)
	}
	if _, err := os.Stat(filepath.Join(root, ".ohgo", "scratch")); !os.IsNotExist(err) {
		t.Fatalf("scratch directory was created for a small prompt: %v", err)
	}
}

// TestScratchReleaseSessionIsScoped proves switching one session cannot delete
// another live session's prompt file.
func TestScratchReleaseSessionIsScoped(t *testing.T) {
	scratch := NewScratch(t.TempDir())
	prompt := strings.Repeat("x", ScratchRuneThreshold)
	pathA, _, _, err := scratch.Offload(11, prompt)
	if err != nil {
		t.Fatal(err)
	}
	pathB, _, _, err := scratch.Offload(22, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if err := scratch.ReleaseSession(11); err != nil {
		t.Fatalf("ReleaseSession: %v", err)
	}
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Fatal("session 11's scratch file survived its release")
	}
	if _, err := os.Stat(pathB); err != nil {
		t.Fatalf("session 22's scratch file was removed: %v", err)
	}
	if err := scratch.ReleaseAll(); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Fatal("ReleaseAll left a tracked file behind")
	}
}

// TestScratchSweepRemovesOnlyOldOrphans pins the 24-hour boundary and keeps
// files this process is still using.
func TestScratchSweepRemovesOnlyOldOrphans(t *testing.T) {
	root := t.TempDir()
	scratch := NewScratch(root)
	now := time.Unix(1_800_000_000, 0)
	scratch.now = func() time.Time { return now }
	prompt := strings.Repeat("x", ScratchRuneThreshold)

	// The orphan simulates a file left by a process that is no longer running,
	// so this owner never tracks it.
	if err := os.MkdirAll(scratch.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(scratch.Dir(), "prompt_1_1.md")
	if err := os.WriteFile(oldPath, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	freshPath, _, _, err := scratch.Offload(2, prompt)
	if err != nil {
		t.Fatal(err)
	}
	activePath, _, _, err := scratch.Offload(3, prompt)
	if err != nil {
		t.Fatal(err)
	}
	// Make the orphan older than the cutoff. The second file stays fresh; the
	// third is also old but active, so it must survive: a live prompt must
	// never be swept out from under the turn that owns it.
	oldTime := now.Add(-ScratchStaleAge - time.Minute)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(activePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	removed, err := scratch.SweepStale(ScratchStaleAge)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("stale orphan survived the sweep")
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh scratch file was swept: %v", err)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active scratch file was swept: %v", err)
	}
}

// TestGlobalHistoryRecallSurvivesScratchPurge is the decoupling contract:
// deleting every scratch file does not change what Up/Ctrl+P can recall.
func TestGlobalHistoryRecallSurvivesScratchPurge(t *testing.T) {
	root := t.TempDir()
	globalPath := filepath.Join(t.TempDir(), "history")
	localPath := filepath.Join(root, ".ohgo", "history")
	// History trims trailing newlines by design; keep the payload's content
	// intact so the recall comparison is about the whole prompt, not one
	// normalization the store already owns.
	prompt := "master prompt\n" + strings.Repeat("full line\n", ScratchLineThreshold-1) + "final line"

	store := Open(localPath, globalPath, 100)
	store.Add(prompt)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	scratch := NewScratch(root)
	_, injected, offloaded, err := scratch.Offload(5, prompt)
	if err != nil || !offloaded {
		t.Fatalf("Offload: offloaded=%v err=%v", offloaded, err)
	}
	if injected != prompt {
		t.Fatal("the injected prompt was truncated")
	}
	if err := scratch.ReleaseAll(); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}

	// Read only the global scope, proving recall no longer depends on either
	// the project history file or the scratch file's lifecycle.
	reopened := Open("", globalPath, 100)
	defer func() { _ = reopened.Close() }()
	entries := reopened.Entries()
	found := false
	for _, entry := range entries {
		if entry == prompt {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the full prompt is not recoverable from the global history store")
	}
}
