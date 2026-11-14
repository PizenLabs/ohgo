package structural

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Cache stores analysis results between runs.
//
// It is an interface because the engine must be testable without a filesystem,
// and because a deployment may not want a cache at all. Cache failure never
// fails an analysis: a stale or unreadable entry is reported and recomputed.
type Cache interface {
	// Load returns the stored bytes, or an error wrapping fs.ErrNotExist when
	// no entry exists.
	Load(key string) ([]byte, error)
	// Store replaces the entry for key.
	Store(key string, value []byte) error
}

// FileCache is a bounded, versioned directory of analysis results.
//
// Entries are keyed by a hash of the schema, analyzer, snapshot identity, and
// normalized request, so a changed repository cannot reuse a result computed
// for a different one. The envelope is validated again on load; a key collision
// or a hand-edited file therefore degrades to a miss, not to false evidence.
type FileCache struct {
	dir        string
	maxEntries int

	mu sync.Mutex
}

// NewFileCache returns a cache rooted at dir. An empty dir disables it.
func NewFileCache(dir string) *FileCache {
	return &FileCache{dir: dir, maxEntries: 64}
}

// Dir reports the cache directory, empty when persistence is disabled.
func (c *FileCache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// Load reads one entry.
func (c *FileCache) Load(key string) ([]byte, error) {
	if c == nil || c.dir == "" {
		return nil, fs.ErrNotExist
	}
	if err := validateCacheKey(key); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Store writes one entry atomically.
//
// A partial file is never visible: the value is written to a temporary file in
// the same directory and renamed over the target. Pruning is best effort; a
// failed prune must not fail the analysis that produced the value.
func (c *FileCache) Store(key string, value []byte) error {
	if c == nil || c.dir == "" {
		return nil
	}
	if err := validateCacheKey(key); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	temp, err := os.CreateTemp(c.dir, ".analysis-*.tmp")
	if err != nil {
		return fmt.Errorf("create cache entry: %w", err)
	}
	name := temp.Name()
	defer func() {
		_ = os.Remove(name)
	}()
	if _, err := temp.Write(value); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write cache entry: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close cache entry: %w", err)
	}
	if err := os.Rename(name, c.path(key)); err != nil {
		return fmt.Errorf("publish cache entry: %w", err)
	}
	c.pruneLocked()
	return nil
}

func (c *FileCache) path(key string) string {
	return filepath.Join(c.dir, "analysis-"+key+".json")
}

// validateCacheKey keeps a caller-supplied key from becoming a path. The key is
// an internal hash, so a non-hex value is a bug, not a user error to repair.
func validateCacheKey(key string) error {
	if len(key) < 8 || len(key) > 64 {
		return fmt.Errorf("structural: invalid cache key length %d", len(key))
	}
	for _, char := range key {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'f':
		default:
			return fmt.Errorf("structural: invalid cache key character %q", char)
		}
	}
	return nil
}

// pruneLocked keeps the newest maxEntries analysis files.
func (c *FileCache) pruneLocked() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type entry struct {
		name    string
		modTime time.Time
	}
	var files []entry
	for _, item := range entries {
		if item.IsDir() {
			continue
		}
		name := item.Name()
		if len(name) < len("analysis-")+len(".json") || name[:len("analysis-")] != "analysis-" {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{name: name, modTime: info.ModTime()})
	}
	if len(files) <= c.maxEntries {
		return
	}
	sort.SliceStable(files, func(i, j int) bool {
		if !files[i].modTime.Equal(files[j].modTime) {
			return files[i].modTime.Before(files[j].modTime)
		}
		return files[i].name < files[j].name
	})
	for _, file := range files[:len(files)-c.maxEntries] {
		_ = os.Remove(filepath.Join(c.dir, file.name))
	}
}

// cacheEnvelope is the persisted unit. Every field here exists so a stale entry
// can be recognised as stale instead of being trusted.
type cacheEnvelope struct {
	SchemaVersion   string   `json:"schema_version"`
	AnalyzerVersion string   `json:"analyzer_version"`
	Adapters        string   `json:"adapters"`
	Root            string   `json:"root"`
	SnapshotID      string   `json:"snapshot_id"`
	Request         Request  `json:"request"`
	Result          Analysis `json:"result"`
}

// adapterFingerprint identifies the parsers compiled into this binary.
//
// It is the cache's guard against reusing a result produced by a different
// adapter set or a different Go standard library. Grammar revisions are pinned
// by go.mod, and adapter names change when the set changes; a change that keeps
// both identical still requires the AnalyzerVersion bump that owns analysis
// semantics.
func adapterFingerprint() string {
	names := make([]string, 0)
	for _, adapter := range ast.DefaultAdapters() {
		names = append(names, adapter.Name())
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, ",") + "|" + runtime.Version()))
	return hex.EncodeToString(sum[:])[:16]
}

// cacheKey derives the content-addressed identity of one analysis.
//
// Anything that changes the answer changes the key: the schema, the analyzer
// semantics, the snapshot identity, and the normalized request. The key is not
// a security boundary; the envelope validation is.
func cacheKey(request Request, snapshot ast.SnapshotInfo) string {
	payload, err := json.Marshal(struct {
		Schema     string  `json:"schema"`
		Analyzer   string  `json:"analyzer"`
		Adapters   string  `json:"adapters"`
		Root       string  `json:"root"`
		SnapshotID string  `json:"snapshot_id"`
		Request    Request `json:"request"`
	}{
		Schema: SchemaVersion, Analyzer: AnalyzerVersion, Adapters: adapterFingerprint(),
		Root: snapshot.Root, SnapshotID: snapshot.SnapshotID, Request: request,
	})
	if err != nil {
		// Request is a struct of scalars; encoding cannot fail. A zero key is
		// safer than a panic and still validates as a miss.
		return "0000000000000000"
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:32]
}

// decodeEnvelope validates and restores one cached analysis.
func decodeEnvelope(data []byte, request Request) (Analysis, bool) {
	var envelope cacheEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Analysis{}, false
	}
	if envelope.SchemaVersion != SchemaVersion ||
		envelope.AnalyzerVersion != AnalyzerVersion ||
		envelope.Adapters != adapterFingerprint() ||
		envelope.Request != request ||
		envelope.Result.Snapshot == nil {
		return Analysis{}, false
	}
	result := envelope.Result
	if result.Snapshot.Source.SnapshotID != envelope.SnapshotID ||
		result.Snapshot.Source.Root != envelope.Root {
		return Analysis{}, false
	}
	// The ID index is unexported and is not part of the wire form. Rebuild it
	// from the nodes the entry carries; a missing node is a corrupt entry, not
	// an empty one.
	result.Snapshot.index()
	if result.Snapshot.byID == nil || len(result.Snapshot.byID) != len(result.Snapshot.Nodes) {
		return Analysis{}, false
	}
	return result, true
}

// encodeEnvelope serializes one analysis for the cache.
func encodeEnvelope(request Request, result Analysis) ([]byte, error) {
	if result.Snapshot == nil {
		return nil, errors.New("structural: refusing to cache an analysis with no snapshot")
	}
	envelope := cacheEnvelope{
		SchemaVersion: SchemaVersion, AnalyzerVersion: AnalyzerVersion,
		Adapters:   adapterFingerprint(),
		Root:       result.Snapshot.Source.Root,
		SnapshotID: result.Snapshot.Source.SnapshotID,
		Request:    request, Result: result,
	}
	return json.Marshal(envelope)
}
