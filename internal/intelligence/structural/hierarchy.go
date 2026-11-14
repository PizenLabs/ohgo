package structural

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Hierarchy bounds.
const (
	// DefaultMaxFiles is how many file nodes one analysis materialises before
	// it collapses to the directory resolution. Progressive disclosure is the
	// default: 50,000 file nodes are a dump, not a summary.
	DefaultMaxFiles = 4000
	// MaxHierarchyFiles is the hard ceiling for a caller that asks for more.
	MaxHierarchyFiles = ast.InventoryMaxFiles
)

// BuildOptions selects one resolution of the structural model.
type BuildOptions struct {
	// Level is the deepest resolution to materialise. Counts are aggregated at
	// every level regardless: a project summary still knows how many files it
	// summarised.
	Level Level
	// MaxFiles bounds materialised file nodes. Exceeding it clamps the model
	// to LevelPackage and records the bound in Coverage.
	MaxFiles int
}

// Build derives the multi-resolution model from one inventory projection.
//
// It is a pure function over the inventory: no filesystem access, no parser, no
// clock. Identical inventory and options produce byte-identical nodes, which is
// what makes the result cacheable and comparable.
func Build(inventory ast.Inventory, opts BuildOptions) *Snapshot {
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	if maxFiles > MaxHierarchyFiles {
		maxFiles = MaxHierarchyFiles
	}
	level := opts.Level
	if level < LevelProject || level > LevelRelationship {
		level = LevelPackage
	}
	materializeFiles := level >= LevelFile
	truncated := inventory.FilesTruncated
	reason := inventory.Reason
	if materializeFiles && len(inventory.Files) > maxFiles {
		level = LevelPackage
		materializeFiles = false
		truncated = true
		if reason == "" {
			reason = "file_node_limit"
		}
	}
	if !materializeFiles && level > LevelPackage {
		level = LevelPackage
	}

	module := inventory.Snapshot.Module
	projectID := ast.ProjectNodeID(module)
	projectName := module
	if projectName == "" {
		projectName = "."
	}

	nodes := map[string]*nodeAggregate{}
	order := []string{}
	add := func(id, parent, name, nodePath string, kind ast.NodeKind, nodeLevel Level) *nodeAggregate {
		if existing, ok := nodes[id]; ok {
			return existing
		}
		entry := &nodeAggregate{
			Node: Node{
				ID: id, Kind: kind, Level: nodeLevel, Name: name, Path: nodePath, Parent: parent,
			},
			languages: map[ast.Language]bool{},
			classes:   map[SourceClass]int{},
			packages:  map[string]bool{},
		}
		nodes[id] = entry
		order = append(order, id)
		return entry
	}

	workspaces := includedWorkspaces(inventory.Snapshot)
	project := add(projectID, "", projectName, ".", ast.NodeProject, LevelProject)
	if len(workspaces) == 0 {
		workspaces = []string{"."}
	}
	workspaceIDs := map[string]string{}
	for _, workspace := range workspaces {
		id := "workspace:" + workspace
		workspaceIDs[workspace] = id
		name := workspace
		if name == "." {
			if module != "" {
				name = module
			} else {
				name = "root"
			}
		} else {
			name = path.Base(workspace)
		}
		project.ensureChild(add(id, projectID, name, workspace, NodeWorkspace, LevelWorkspace))
	}

	filesByPath := make([]ast.FileInfo, 0, len(inventory.Files))
	filesByPath = append(filesByPath, inventory.Files...)
	sort.SliceStable(filesByPath, func(i, j int) bool { return filesByPath[i].Path < filesByPath[j].Path })

	for _, file := range filesByPath {
		workspace := workspaceFor(file.Path, workspaces)
		workspaceID := workspaceIDs[workspace]
		if workspaceID == "" {
			workspace = "."
			workspaceID = "workspace:."
			project.ensureChild(add(workspaceID, projectID,
				workspaceLabel(workspace, module), workspace, NodeWorkspace, LevelWorkspace))
		}
		chain := directoryChain(workspace, file.Path)
		parentID := workspaceID
		for _, dir := range chain {
			id := ast.DirectoryNodeID(dir)
			kind := ast.NodeDirectory
			level := LevelPackage
			if dir == chain[0] && workspaceHasModuleSegment(workspace, dir) {
				kind = ast.NodeModule
				level = LevelModule
			}
			add(id, parentID, path.Base(dir), dir, kind, level)
			parentID = id
		}

		class, classEvidence := classifyPath(file.Path, file.Test)
		unobserved := !file.Parsed
		if file.Unsupported {
			unobserved = true
		}
		for _, id := range ancestorChain(projectID, workspaceID, chain) {
			entry := nodes[id]
			if entry == nil {
				continue
			}
			entry.Files++
			if file.Test {
				entry.TestFiles++
			}
			entry.Symbols += file.Symbols
			entry.classes[class]++
			if unobserved {
				entry.Unobserved++
			}
			if file.Language != "" {
				entry.languages[ast.Language(file.Language)] = true
			}
		}

		if materializeFiles {
			id := ast.FileNodeID(file.Path)
			fileNode := add(id, parentID, path.Base(file.Path), file.Path, ast.NodeFile, LevelFile)
			fileNode.Files = 1
			if file.Test {
				fileNode.TestFiles = 1
			}
			fileNode.Symbols = file.Symbols
			fileNode.Class = class
			fileNode.Classification = classEvidence
			fileNode.Language = ast.Language(file.Language)
			if file.Language != "" {
				fileNode.languages[ast.Language(file.Language)] = true
			}
			fileNode.classes[class] = 1
			if unobserved {
				fileNode.Unobserved = 1
			}
			fileNode.Evidence = fileEvidence(file)
		}
	}

	for _, pkg := range inventory.Packages {
		entry := nodes[ast.DirectoryNodeID(pkg.Dir)]
		if entry == nil {
			continue
		}
		if pkg.Name != "" {
			entry.packages[pkg.Name] = true
		}
		for _, name := range pkg.Packages {
			entry.packages[name] = true
		}
	}

	// Project and workspace evidence aggregate their members. A workspace with
	// an unparsed file is PARTIALLY_OBSERVED even though its file count is
	// observed.
	for _, id := range order {
		entry := nodes[id]
		if entry.Kind != ast.NodeProject {
			entry.Evidence = entry.evidenceFor()
			continue
		}
		parts := make([]Evidence, 0, len(entry.children))
		for _, childID := range entry.children {
			parts = append(parts, nodes[childID].evidenceFor())
		}
		if len(parts) == 0 {
			entry.Evidence = ObservedEvidence(KindInventory,
				EvidenceRef{Kind: KindInventory, Path: entry.Path, Rule: "repository_root"})
		} else {
			entry.Evidence = AggregateContainment(parts...)
		}
	}

	snapshot := &Snapshot{
		SchemaVersion: SchemaVersion,
		Version:       AnalyzerVersion,
		ProjectID:     projectID,
		Source:        inventory.Snapshot,
		Level:         level,
		Coverage:      coverageFor(inventory, level, truncated, reason),
	}
	for _, id := range order {
		entry := nodes[id]
		if entry.Level > level {
			continue
		}
		node := entry.Node
		node.Files = entry.Files
		node.TestFiles = entry.TestFiles
		node.Symbols = entry.Symbols
		node.Languages = sortedLanguages(entry.languages)
		if len(node.Languages) == 1 {
			node.Language = node.Languages[0]
		}
		if len(entry.classes) > 0 {
			node.Classes = map[SourceClass]int{}
			for class, count := range entry.classes {
				node.Classes[class] = count
			}
		}
		node.Packages = sortedStringSet(entry.packages)
		node.Evidence = entry.Evidence
		if truncated && node.Kind != ast.NodeFile && node.Level <= LevelPackage {
			if node.Evidence.State.Rank() > StatePartiallyObserved.Rank() {
				node.Evidence.State = StatePartiallyObserved
			}
			if node.Evidence.Confidence > 0.5 {
				node.Evidence.Confidence = 0.5
			}
			node.Evidence.Reason = "inventory row set truncated; counts cover materialised rows only"
		}
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	sortNodes(snapshot.Nodes)
	// Publish the resolution that was actually materialised. Requesting a
	// symbol-level snapshot does not create symbol nodes (the focal resolver
	// answers that level), so claiming LevelSymbol here would be a false
	// resolution claim.
	published := LevelProject
	for _, node := range snapshot.Nodes {
		if node.Level > published {
			published = node.Level
		}
	}
	snapshot.Level = published
	snapshot.Coverage.Level = published
	snapshot.index()
	snapshot.Issues = issuesFor(inventory, truncated, reason)
	return snapshot
}

// nodeAggregate is the mutable builder form of a Node. It carries counts that a
// published Node never needs: they are folded into Coverage and summaries.
type nodeAggregate struct {
	Node
	Unobserved int
	languages  map[ast.Language]bool
	classes    map[SourceClass]int
	packages   map[string]bool
	children   []string
}

func (n *nodeAggregate) ensureChild(child *nodeAggregate) {
	if child == nil {
		return
	}
	for _, existing := range n.children {
		if existing == child.ID {
			return
		}
	}
	n.children = append(n.children, child.ID)
}

func (n *nodeAggregate) evidenceFor() Evidence {
	ref := EvidenceRef{Kind: KindInventory, Path: n.Path, Rule: "directory_scan"}
	if n.Path == "." {
		ref.Rule = "repository_root"
	}
	if n.Unobserved > 0 {
		evidence := ObservedEvidence(KindInventory, ref)
		evidence.State = StatePartiallyObserved
		evidence.Confidence = 0.55
		evidence.Reason = fmt.Sprintf("%d of %d files unparsed or unreadable", n.Unobserved, n.Files)
		return evidence
	}
	return ObservedEvidence(KindInventory, ref)
}

func fileEvidence(file ast.FileInfo) Evidence {
	ref := EvidenceRef{Kind: KindInventory, Path: file.Path}
	if file.ParseError != "" {
		evidence := UnknownEvidence("parse failed: " + file.ParseError)
		evidence.Refs = []EvidenceRef{ref}
		return evidence
	}
	if !file.Parsed {
		evidence := UnknownEvidence("file is inventoried but not parsed")
		evidence.Refs = []EvidenceRef{ref}
		return evidence
	}
	return ObservedEvidence(KindAdapterFact, EvidenceRef{
		Kind: KindAdapterFact, Path: file.Path, Adapter: file.Adapter,
	})
}

// includedWorkspaces returns the nested repository roots included in the
// snapshot, plus the root workspace when it holds files of its own.
func includedWorkspaces(info ast.SnapshotInfo) []string {
	var out []string
	for _, boundary := range info.NestedProjects {
		if !boundary.Included || boundary.Path == "" || boundary.Path == "." {
			continue
		}
		out = append(out, boundary.Path)
	}
	sort.Strings(out)
	return out
}

// workspaceFor returns the deepest included workspace containing rel.
func workspaceFor(rel string, workspaces []string) string {
	best := "."
	for _, workspace := range workspaces {
		if rel == workspace || strings.HasPrefix(rel, workspace+"/") {
			if len(workspace) > len(best) {
				best = workspace
			}
		}
	}
	return best
}

func workspaceLabel(workspace, module string) string {
	if workspace != "." {
		return path.Base(workspace)
	}
	if module != "" {
		return module
	}
	return "root"
}

// directoryChain returns the module/directory chain for a file, from the
// first module segment down to the containing directory.
//
// The first element is the module grouping (first path segment under the
// workspace). A file at the workspace root has no chain.
func directoryChain(workspace, rel string) []string {
	dir := path.Dir(rel)
	if dir == "." || dir == workspace {
		return nil
	}
	rest := dir
	if workspace != "." {
		rest = strings.TrimPrefix(dir, workspace+"/")
	}
	if rest == "" || rest == "." {
		return nil
	}
	parts := strings.Split(rest, "/")
	chain := make([]string, 0, len(parts))
	for i := range parts {
		segment := strings.Join(parts[:i+1], "/")
		if workspace != "." {
			segment = workspace + "/" + segment
		}
		chain = append(chain, segment)
	}
	return chain
}

func workspaceHasModuleSegment(workspace, dir string) bool {
	rest := dir
	if workspace != "." {
		rest = strings.TrimPrefix(dir, workspace+"/")
	}
	return rest != "" && !strings.Contains(rest, "/")
}

func ancestorChain(projectID, workspaceID string, chain []string) []string {
	ids := make([]string, 0, len(chain)+2)
	ids = append(ids, projectID, workspaceID)
	for _, dir := range chain {
		ids = append(ids, ast.DirectoryNodeID(dir))
	}
	return ids
}

func sortedLanguages(set map[ast.Language]bool) []ast.Language {
	if len(set) == 0 {
		return nil
	}
	out := make([]ast.Language, 0, len(set))
	for language := range set {
		out = append(out, language)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedStringSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func coverageFor(inventory ast.Inventory, level Level, truncated bool, reason string) Coverage {
	info := inventory.Snapshot
	coverage := Coverage{
		Level:        level,
		Files:        info.Files,
		Materialized: len(inventory.Files),
		Parsed:       info.ParsedFiles,
		Pending:      info.PendingFiles,
		Failed:       info.FailedFiles,
		Truncated:    truncated,
		Reason:       reason,
	}
	switch {
	case info.Files == 0 && info.ScopeComplete:
		coverage.State = StateObserved
		coverage.Confidence = confidenceInventory
	case truncated || info.PendingFiles > 0 || info.FailedFiles > 0 || !info.StructuralComplete:
		coverage.State = StatePartiallyObserved
		coverage.Confidence = 0.55
	default:
		coverage.State = StateObserved
		coverage.Confidence = confidenceInventory
	}
	if !info.ScopeComplete {
		coverage.State = StatePartiallyObserved
		if coverage.Confidence > 0.5 {
			coverage.Confidence = 0.5
		}
		if coverage.Reason == "" {
			coverage.Reason = "scope_incomplete"
		}
	}
	return coverage
}

func issuesFor(inventory ast.Inventory, truncated bool, reason string) []Issue {
	info := inventory.Snapshot
	var issues []Issue
	if len(info.Unsupported) > 0 {
		extensions := make([]string, 0, len(info.Unsupported))
		for _, item := range info.Unsupported {
			extensions = append(extensions, item.Extension)
		}
		sort.Strings(extensions)
		issues = append(issues, Issue{
			Code:   "unsupported_language",
			Detail: "no adapter in this build for " + strings.Join(extensions, ", "),
		})
	}
	if info.FailedFiles > 0 {
		issues = append(issues, Issue{
			Code:   "parse_failure",
			Detail: fmt.Sprintf("%d source files failed to parse", info.FailedFiles),
		})
	}
	if info.PendingFiles > 0 {
		issues = append(issues, Issue{
			Code:   "unparsed_source",
			Detail: fmt.Sprintf("%d supported source files are not parsed in this analysis", info.PendingFiles),
		})
	}
	if !info.ScopeComplete {
		issues = append(issues, Issue{
			Code:   "scope_incomplete",
			Detail: "part of the repository was not inspected; absence claims are not repository-wide",
		})
	}
	if truncated {
		detail := "the analysis cut its row set"
		if reason != "" {
			detail += " (" + reason + ")"
		}
		issues = append(issues, Issue{Code: "truncated_analysis", Detail: detail})
	}
	if info.Files == 0 {
		issues = append(issues, Issue{
			Code:   "empty_inventory",
			Detail: "no indexed files were observed under the analysis root",
		})
	}
	return issues
}
