package structural

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// fixture writes a repository tree into a temporary directory.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// newIndex builds the production adaptive index shape over root.
func newIndex(t *testing.T, root string) *ast.Index {
	t.Helper()
	idx, err := ast.NewIndexWithOptions(root, ast.IndexOptions{Adaptive: true})
	if err != nil {
		t.Fatalf("NewIndexWithOptions: %v", err)
	}
	return idx
}

func loadInventory(t *testing.T, idx *ast.Index, parse bool) ast.Inventory {
	t.Helper()
	inventory, err := idx.Inventory(context.Background(), ast.InventoryQuery{Parse: parse})
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	return inventory
}

// nodeByPath finds the single node with a path.
func nodeByPath(t *testing.T, snapshot *Snapshot, nodePath string) Node {
	t.Helper()
	var found []Node
	for _, node := range snapshot.Nodes {
		if node.Path == nodePath {
			found = append(found, node)
		}
	}
	if len(found) != 1 {
		t.Fatalf("path %q matched %d nodes: %+v", nodePath, len(found), snapshot.Nodes)
	}
	return found[0]
}

// nodeAt finds the single node with a path and level.
func nodeAt(t *testing.T, snapshot *Snapshot, nodePath string, level Level) Node {
	t.Helper()
	var found []Node
	for _, node := range snapshot.Nodes {
		if node.Path == nodePath && node.Level == level {
			found = append(found, node)
		}
	}
	if len(found) != 1 {
		t.Fatalf("path %q level %s matched %d nodes: %+v", nodePath, level, len(found), snapshot.Nodes)
	}
	return found[0]
}

// hasIssue reports whether the analysis recorded a code.
func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
