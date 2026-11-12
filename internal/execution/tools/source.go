package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/ignore"
)

// StructuralSource owns the one lazily built structural snapshot shared by
// every structural tool.
//
// Constructing it does not parse the repository. It owns the repository-scope
// adapter as well: the index is dependency-free, so .gitignore semantics are
// injected rather than imported into intelligence/ast.
type StructuralSource struct {
	root  string
	scope *ignoreScope

	mu  sync.Mutex
	idx *ast.Index
}

// NewStructuralSource returns a lazy index handle for root.
func NewStructuralSource(root string) *StructuralSource {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	return &StructuralSource{
		root:  absRoot,
		scope: &ignoreScope{root: absRoot},
	}
}

// Root returns the absolute workspace root the source was built for.
func (s *StructuralSource) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Index returns the cached index, building it on first use. Freshness remains
// owned by ast.Index; this method must not add a second refresh policy.
func (s *StructuralSource) Index() (*ast.Index, error) {
	if s == nil {
		return nil, fmt.Errorf("tools: structural source is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx == nil {
		idx, err := ast.NewIndexWithOptions(s.root, ast.IndexOptions{Scope: s.scope})
		if err != nil {
			return nil, err
		}
		s.idx = idx
	}
	return s.idx, nil
}

// addStructuralProvenance adds the snapshot header every structural result
// must carry. Evidence records these fields directly instead of parsing prose.
func addStructuralProvenance(details map[string]any, idx *ast.Index, complete bool) {
	if idx == nil {
		details["graph_state"] = string(ast.FreshnessInvalid)
		details["complete"] = false
		return
	}
	info := idx.Info()
	details["snapshot_id"] = info.SnapshotID
	details["generation"] = info.Generation
	details["graph_state"] = string(info.State)
	details["partial"] = info.Partial
	details["parse_failures"] = info.ParseFailures
	details["complete"] = complete && !info.Partial
}

// structuralSourceIndex builds and returns the shared snapshot, mapping the
// error to the tool result vocabulary.
func structuralSourceIndex(source *StructuralSource, name string) (*ast.Index, error) {
	if source == nil {
		return nil, fmt.Errorf("tools: %s tool has no structural source", name)
	}
	return source.Index()
}

type ignoreScope struct {
	root string

	mu        sync.RWMutex
	matcher   *ignore.Matcher
	loadedSig string
}

// Ignored reports whether rel is excluded by the current matcher.
func (s *ignoreScope) Ignored(rel string, isDir bool) bool {
	s.mu.RLock()
	matcher := s.matcher
	s.mu.RUnlock()
	return matcher.Ignored(rel, isDir)
}

// Fingerprint reloads the matcher when an ignore file changed and returns the
// rule-text identity the index records with the snapshot.
func (s *ignoreScope) Fingerprint() string {
	sig := ignoreFileSignature(s.root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if sig != s.loadedSig {
		s.matcher = ignore.New(s.root, true)
		s.loadedSig = sig
	}
	return s.matcher.Fingerprint()
}

// ignoreFileSignature hashes the .gitignore file metadata. It does not read
// rule text: ignore.New owns parsing, and the matcher fingerprint owns the
// semantic identity.
func ignoreFileSignature(root string) string {
	hasher := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			switch name {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") && name != ".github" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != ".gitignore" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		_, _ = fmt.Fprintf(hasher, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}
