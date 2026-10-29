package diff

import (
	"sort"
	"strings"
)

// FileNode is one entry in the sidebar tree: either a directory or a file.
//
// Directories carry the aggregated stats of their subtree so a collapsed
// folder still reports how much changed inside it.
type FileNode struct {
	// Name is the display label: the last path segment for a file, or the
	// collapsed "a/b/c" chain for a directory.
	Name string
	// Path is the repository-root-relative path. It is the pathspec handed
	// back to git for a per-file diff.
	Path string
	// IsDir reports a directory, which has Children and no Status.
	IsDir bool
	// Status is the file's change kind; empty for a directory.
	Status FileStatus
	// Children are the directory's entries, directories first then files,
	// each alphabetically.
	Children []*FileNode
	// Expanded reports whether the directory's children are visible. It is
	// meaningless for a file.
	Expanded bool
	// StatAdd and StatDel are the added and removed line counts: the file's
	// own for a file, the subtree total for a directory.
	StatAdd int
	StatDel int
	// Binary reports a file git could not line-count. It is meaningful only
	// for a file leaf.
	Binary bool
}

// Row is one visible tree line: a node plus its indentation depth.
type Row struct {
	// Node is the entry.
	Node *FileNode
	// Depth is the nesting level, zero at the top.
	Depth int
}

// VisibleRows flattens the tree to the rows a collapsed/expanded sidebar shows.
//
// It is the only place expansion is interpreted, so the viewport's row count,
// the cursor's range, and the renderer's slice always agree.
func VisibleRows(roots []*FileNode) []Row {
	var rows []Row
	var walk func(nodes []*FileNode, depth int)
	walk = func(nodes []*FileNode, depth int) {
		for _, node := range nodes {
			rows = append(rows, Row{Node: node, Depth: depth})
			if node.IsDir && node.Expanded {
				walk(node.Children, depth+1)
			}
		}
	}
	walk(roots, 0)
	return rows
}

// BuildTree folds a flat change list into the sidebar hierarchy.
//
// Directories are synthesized from the path segments; a chain of directories
// that contains no file of its own collapses to one row ("cmd/ohgo" rather
// than "cmd" > "ohgo"), which is what keeps a deep path from indenting off the
// pane. Directories start expanded so the full change set is visible on open.
func BuildTree(files []FileChange) []*FileNode {
	root := &FileNode{IsDir: true, Expanded: true}
	// index maps a directory's path to its node so a segment lookup is a map
	// probe rather than a scan of the parent's children.
	index := map[string]*FileNode{"": root}

	for _, change := range files {
		if change.Path == "" {
			continue
		}
		segments := strings.Split(change.Path, "/")
		parent := root
		prefix := ""
		for i, segment := range segments {
			if segment == "" {
				continue
			}
			if prefix == "" {
				prefix = segment
			} else {
				prefix += "/" + segment
			}
			isLeaf := i == len(segments)-1

			node, ok := index[prefix]
			if !ok {
				node = &FileNode{
					Name:     segment,
					Path:     prefix,
					IsDir:    !isLeaf,
					Expanded: !isLeaf,
				}
				index[prefix] = node
				parent.Children = append(parent.Children, node)
			}
			if isLeaf {
				node.IsDir = false
				node.Status = change.Status
				node.StatAdd = change.Added
				node.StatDel = change.Removed
				node.Binary = change.Binary
			}
			parent = node
		}
	}

	for _, child := range root.Children {
		collapse(child)
	}
	for _, child := range root.Children {
		aggregate(child)
		sortNodes(child)
	}
	sortNodes(root)
	return root.Children
}

// collapse merges a directory whose only child is another directory into a
// single "a/b" row, recursively. A directory holding a file is left alone: its
// name is the last context before a leaf.
func collapse(node *FileNode) {
	for _, child := range node.Children {
		collapse(child)
	}
	if node.IsDir && len(node.Children) == 1 && node.Children[0].IsDir {
		only := node.Children[0]
		node.Name = node.Name + "/" + only.Name
		node.Path = only.Path
		node.Children = only.Children
	}
}

// aggregate sums a directory's subtree line counts into the directory itself.
func aggregate(node *FileNode) (added, removed int) {
	if !node.IsDir {
		return node.StatAdd, node.StatDel
	}
	for _, child := range node.Children {
		a, d := aggregate(child)
		added += a
		removed += d
	}
	node.StatAdd, node.StatDel = added, removed
	return added, removed
}

// sortNodes orders a node's children: directories first, then files, each
// alphabetically and case-insensitively so "README" and "readme" neighbours
// stay together.
func sortNodes(node *FileNode) {
	sort.SliceStable(node.Children, func(i, j int) bool {
		a, b := node.Children[i], node.Children[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		al, bl := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if al != bl {
			return al < bl
		}
		return a.Name < b.Name
	})
}
