package structural

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

func testEnvelope(t *testing.T) (Request, Analysis) {
	t.Helper()
	request := (Request{Intent: IntentProject}).normalized()
	snapshot := Build(ast.Inventory{Snapshot: ast.SnapshotInfo{
		Root: "/repo", Module: "example.com/app", SnapshotID: "abc123", Files: 0,
	}}, BuildOptions{Level: LevelPackage})
	analysis := Analysis{
		SchemaVersion: SchemaVersion, AnalyzerVersion: AnalyzerVersion,
		Request: request, Snapshot: snapshot, Coverage: snapshot.Coverage,
	}
	return request, analysis
}

// TestCacheEnvelopeRejectsForeignIdentity pins that reuse is only possible for
// the same schema, analyzer, adapter set, root, snapshot, and request. Any
// mismatch is a miss, never a partially trusted answer.
func TestCacheEnvelopeRejectsForeignIdentity(t *testing.T) {
	request, analysis := testEnvelope(t)
	data, err := encodeEnvelope(request, analysis)
	if err != nil {
		t.Fatalf("encodeEnvelope: %v", err)
	}
	if _, ok := decodeEnvelope(data, request); !ok {
		t.Fatal("fresh envelope did not decode")
	}

	mutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"schema", func(envelope map[string]any) { envelope["schema_version"] = "structural/v0" }},
		{"analyzer", func(envelope map[string]any) { envelope["analyzer_version"] = "older" }},
		{"adapters", func(envelope map[string]any) { envelope["adapters"] = "different" }},
		{"root", func(envelope map[string]any) { envelope["root"] = "/other" }},
		{"snapshot", func(envelope map[string]any) { envelope["snapshot_id"] = "other" }},
	}
	for _, mutation := range mutations {
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		mutation.mutate(decoded)
		changed, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, ok := decodeEnvelope(changed, request); ok {
			t.Errorf("%s mutation was accepted", mutation.name)
		}
	}

	otherRequest := (Request{Intent: IntentArchitecture}).normalized()
	if _, ok := decodeEnvelope(data, otherRequest); ok {
		t.Error("a different request reused an entry")
	}
}

// TestCacheKeySeparatesRootsAndAdapters pins the invalidation inputs that the
// snapshot identity alone does not cover.
func TestCacheKeySeparatesRootsAndAdapters(t *testing.T) {
	request := (Request{Intent: IntentProject}).normalized()
	first := ast.SnapshotInfo{Root: "/repo/a", SnapshotID: "same"}
	second := ast.SnapshotInfo{Root: "/repo/b", SnapshotID: "same"}
	if cacheKey(request, first) == cacheKey(request, second) {
		t.Fatal("two roots with the same snapshot id share a cache key")
	}
	again := ast.SnapshotInfo{Root: "/repo/a", SnapshotID: "same"}
	if cacheKey(request, first) != cacheKey(request, again) {
		t.Fatal("cache key is not stable for identical input")
	}
}

// TestFileCacheRoundTripAndPrune pins atomic persistence, explicit missing
// entries, key validation, and the entry bound.
func TestFileCacheRoundTripAndPrune(t *testing.T) {
	dir := t.TempDir()
	cache := NewFileCache(dir)
	cache.maxEntries = 2
	if _, err := cache.Load("0123456789abcdef"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing entry error = %v, want fs.ErrNotExist", err)
	}
	if _, err := cache.Load("../escape"); err == nil {
		t.Fatal("path-like key was accepted")
	}
	for _, key := range []string{"0123456789abcdef", "fedcba9876543210", "0011223344556677"} {
		if err := cache.Store(key, []byte("value-"+key)); err != nil {
			t.Fatalf("Store(%s): %v", key, err)
		}
		// Distinct mtimes keep pruning deterministic on coarse filesystems.
		time.Sleep(2 * time.Millisecond)
	}
	data, err := cache.Load("0011223344556677")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "value-0011223344556677" {
		t.Fatalf("value = %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("cache kept %d entries, want at most 2", count)
	}
}
