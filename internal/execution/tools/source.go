package tools

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// StructuralSource owns the one lazily built repository index shared by every
// structural tool.
//
// Phase 1 gave symbols a private cached index. Phase 6 adds relationship
// queries that must read the same snapshot, not a second one. This handle is
// the ownership point: constructing it does not parse the repository, and both
// tools receive the same pointer in production.
type StructuralSource struct {
	root string

	mu  sync.Mutex
	idx *ast.Index
}

// NewStructuralSource returns a lazy index handle for root.
func NewStructuralSource(root string) *StructuralSource {
	return &StructuralSource{root: root}
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
		idx, err := ast.NewIndex(s.root)
		if err != nil {
			return nil, err
		}
		s.idx = idx
	}
	return s.idx, nil
}
