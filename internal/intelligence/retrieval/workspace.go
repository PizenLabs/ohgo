package retrieval

import (
	"context"
	"errors"
	"sync"
)

// Workspace owns one live retrieval index for a repository root.
//
// It is the shared snapshot both the retrieve tool and the investigation
// orchestrator use: two callers must not tokenize the same tree twice or
// persist two competing index files. Sync happens under the workspace lock, so
// concurrent searches see one consistent index rather than racing refreshes.
type Workspace struct {
	root    string
	indexer *Indexer
	mu      sync.Mutex
	index   *Index
	stats   SyncStats
}

// NewWorkspace returns a lazy index for root. cacheDir empty disables
// persistence; the index then lives only in this process.
func NewWorkspace(root, cacheDir string, opts Options) *Workspace {
	return &Workspace{root: root, indexer: NewIndexer(root, cacheDir, opts)}
}

// Root reports the indexed root.
func (w *Workspace) Root() string {
	if w == nil {
		return ""
	}
	return w.root
}

// Search reconciles the index with source and ranks documents for q.
//
// The stats describe the reconciliation that actually happened, so a caller
// can report whether it reused a warm index or read changed files.
func (w *Workspace) Search(ctx context.Context, source Source, q Query) (Result, SyncStats, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	index, stats, err := w.indexer.Refresh(ctx, source, w.index)
	if err != nil {
		return Result{}, w.stats, err
	}
	w.index, w.stats = index, stats
	result, err := index.Search(q)
	return result, stats, err
}

// ErrNotReady reports a query before the workspace has an index.
var ErrNotReady = errors.New("retrieval: workspace index is not ready")

// Query ranks against the current index without reconciling it.
//
// It exists for benchmarking and for callers that already paid for a Refresh;
// the normal tool path uses Search so it never serves a stale corpus.
func (w *Workspace) Query(q Query) (Result, error) {
	w.mu.Lock()
	index := w.index
	w.mu.Unlock()
	if index == nil {
		return Result{}, ErrNotReady
	}
	return index.Search(q)
}

// Refresh reconciles the index with source without running a query. It exists
// so a caller that only needs freshness can pay for it once.
func (w *Workspace) Refresh(ctx context.Context, source Source) (SyncStats, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	index, stats, err := w.indexer.Refresh(ctx, source, w.index)
	if err != nil {
		return w.stats, err
	}
	w.index, w.stats = index, stats
	return stats, nil
}

// Stats reports the last reconciliation.
func (w *Workspace) Stats() SyncStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}
