package commit

import (
	"context"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxImportEvidenceFiles bounds how many changed files are parsed for import
// evidence. Grouping is a heuristic aid, not a full repository index; a huge
// change set must not turn /commit into an indexing job.
const maxImportEvidenceFiles = 80

// maxImportEvidenceBytes bounds one parsed source file.
const maxImportEvidenceBytes = 256 * 1024

// groupingInput is the complete, read-only evidence for one grouping pass.
type groupingInput struct {
	Root       string
	Mode       Mode
	Changes    []FileChange
	Diff       string
	Structural StructuralEvidenceProvider
	Objective  string
}

// buildGroups derives logical change groups from repository evidence.
//
// A group is a connected component of proven relationships, not a directory.
// The result is deterministic: same paths, same diff, and same imports always
// produce the same groups, evidence, and order.
func buildGroups(ctx context.Context, input groupingInput) ([]ChangeGroup, string) {
	if len(input.Changes) == 0 {
		return nil, "paths only"
	}
	facts, factsTruncated := collectChangeFacts(input.Root, input.Changes, input.Diff)
	byPath := make(map[string]int, len(input.Changes))
	for i, change := range input.Changes {
		byPath[change.Path] = i
	}

	union := newUnionFind(len(input.Changes))
	var proven []EvidenceEdge
	merge := func(left, right int, kind EvidenceKind, detail string) {
		if left == right {
			return
		}
		union.join(left, right)
		proven = append(proven, EvidenceEdge{
			From:   input.Changes[left].Path,
			To:     input.Changes[right].Path,
			Kind:   kind,
			Detail: detail,
		})
	}

	provider := input.Structural
	if provider == nil {
		provider = DefaultStructuralEvidence
	}
	for _, edge := range provider.Edges(ctx, input.Root, input.Changes) {
		left, leftOK := byPath[edge.From]
		right, rightOK := byPath[edge.To]
		if !leftOK || !rightOK {
			continue
		}
		merge(left, right, EvidenceImport, edge.Detail)
	}

	addSymbolEdges(input.Changes, facts, merge)
	addTestEdges(input.Changes, facts, merge)

	forcedAmbiguous := addDocsEdges(input.Changes, facts, union, merge)
	addSubsystemEdges(input.Changes, facts, merge)

	components := make(map[int][]int)
	for i := range input.Changes {
		root := union.find(i)
		components[root] = append(components[root], i)
	}
	mergeSupportComponents(input.Changes, facts, components, merge, forcedAmbiguous)

	// Recompute after support merges.
	components = make(map[int][]int)
	for i := range input.Changes {
		root := union.find(i)
		components[root] = append(components[root], i)
	}
	// A repository with no implementation change is one support purpose by
	// definition. Splitting README from CHANGELOG would manufacture commits
	// from individual files, which /commit -a must never do.
	if !componentsHaveSource(components, input.Changes, facts) &&
		componentsAreSupportPurpose(components, input.Changes, facts) {
		roots := make([]int, 0, len(components))
		for root := range components {
			roots = append(roots, root)
		}
		sort.Ints(roots)
		for _, root := range roots[1:] {
			merge(components[roots[0]][0], components[root][0], EvidenceSupport, "repository support")
		}
		components = make(map[int][]int)
		for i := range input.Changes {
			root := union.find(i)
			components[root] = append(components[root], i)
		}
	}

	groups := make([]ChangeGroup, 0, len(components))
	for _, indexes := range components {
		group := makeGroup(input.Changes, facts, indexes, input.Root)
		group.Confidence = groupConfidence(group, facts, forcedAmbiguous)
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Key != groups[j].Key {
			return groups[i].Key < groups[j].Key
		}
		return groupPrimaryPath(groups[i]) < groupPrimaryPath(groups[j])
	})
	disambiguateGroupKeys(groups)

	assignGroupEvidence(groups, input.Changes, proven)
	assignGroupOrder(groups, proven)
	label := structuralLabel(proven)
	if strings.Contains(input.Diff, "... (truncated)") {
		label += " (diff truncated)"
	}
	if factsTruncated {
		label += " (semantic sample truncated)"
	}
	return groups, label
}

// assignGroupOrder gives every group a deterministic plan position.
//
// Imported groups are ordered before importers, so a shared API or model change
// lands before the implementation that depends on it. Remaining ties use the
// semantic rank (source, test, docs, build/CI) and then the group key; model
// preference never participates.
func assignGroupOrder(groups []ChangeGroup, edges []EvidenceEdge) {
	if len(groups) == 0 {
		return
	}
	keyOf := make(map[string]string, len(edges))
	for _, group := range groups {
		for _, file := range group.Files {
			keyOf[file.Path] = group.Key
		}
	}
	deps := make(map[string]map[string]bool, len(groups))
	for _, group := range groups {
		deps[group.Key] = make(map[string]bool)
	}
	for _, edge := range edges {
		if edge.Kind != EvidenceImport {
			continue
		}
		importer := keyOf[edge.From]
		imported := keyOf[edge.To]
		if importer == "" || imported == "" || importer == imported {
			continue
		}
		// The imported group must be committed before its importer.
		deps[importer][imported] = true
	}
	remaining := make(map[string]bool, len(groups))
	for _, group := range groups {
		remaining[group.Key] = true
	}
	order := 1
	for len(remaining) > 0 {
		ready := make([]string, 0, len(remaining))
		for key := range remaining {
			blocked := false
			for dependency := range deps[key] {
				if remaining[dependency] {
					blocked = true
					break
				}
			}
			if !blocked {
				ready = append(ready, key)
			}
		}
		if len(ready) == 0 {
			// A dependency cycle is still a valid change set. Break it
			// deterministically rather than dropping the groups.
			for key := range remaining {
				ready = append(ready, key)
			}
		}
		sort.Slice(ready, func(i, j int) bool {
			left, right := groupByKey(groups, ready[i]), groupByKey(groups, ready[j])
			if rank := groupRank(left) - groupRank(right); rank != 0 {
				return rank < 0
			}
			return ready[i] < ready[j]
		})
		next := ready[0]
		for i := range groups {
			if groups[i].Key == next {
				groups[i].Order = order
				break
			}
		}
		delete(remaining, next)
		order++
	}
}

// groupByKey returns a copy of the group with the given key.
func groupByKey(groups []ChangeGroup, key string) ChangeGroup {
	for _, group := range groups {
		if group.Key == key {
			return group
		}
	}
	return ChangeGroup{Key: key}
}

// groupRank orders support material after implementation work.
func groupRank(group ChangeGroup) int {
	hasSource, hasTest, hasDocs, hasBuild, hasConfig := false, false, false, false, false
	for _, file := range group.Files {
		switch changeRole(file) {
		case roleSource:
			hasSource = true
		case roleTest:
			hasTest = true
		case roleDocs:
			hasDocs = true
		case roleBuild, roleCI:
			hasBuild = true
		case roleConfig:
			hasConfig = true
		}
	}
	switch {
	case hasSource:
		return 0
	case hasTest:
		return 1
	case hasDocs:
		return 2
	case hasBuild:
		return 3
	case hasConfig:
		return 4
	default:
		return 5
	}
}

// addSymbolEdges joins a declaration to a changed reference of it.
//
// Definition/reference coupling is stronger evidence than a shared filename:
// it states that one changed file's new symbol is used by another changed file.
func addSymbolEdges(changes []FileChange, facts map[string]*changeFacts, merge func(int, int, EvidenceKind, string)) {
	index := make(map[string]int, len(changes))
	for i, change := range changes {
		index[change.Path] = i
	}
	declarers := make(map[string][]int)
	for i, change := range changes {
		fact := facts[change.Path]
		for _, name := range fact.Declared {
			if !isDistinctiveSymbol(name) {
				continue
			}
			declarers[name] = append(declarers[name], i)
		}
	}
	for i, change := range changes {
		fact := facts[change.Path]
		for _, name := range sortedWordKeys(fact.Words) {
			if !isDistinctiveSymbol(name) {
				continue
			}
			for _, j := range declarers[name] {
				if i == j {
					continue
				}
				merge(i, j, EvidenceSymbol, "shares symbol "+name)
			}
		}
		for _, name := range fact.Declared {
			if !isDistinctiveSymbol(name) {
				continue
			}
			for _, j := range declarers[name] {
				if i >= j {
					continue
				}
				merge(i, j, EvidenceSymbol, "changes symbol "+name)
			}
		}
	}
}

// addTestEdges joins a test to the implementation it names.
//
// The exact stem comparison is what makes one-letter Go names work; the
// subsystem-word requirement keeps a same-named test in an unrelated tree from
// being absorbed.
func addTestEdges(changes []FileChange, facts map[string]*changeFacts, merge func(int, int, EvidenceKind, string)) {
	var sources []int
	for i, change := range changes {
		if facts[change.Path].Role == roleSource {
			sources = append(sources, i)
		}
	}
	if len(sources) == 0 {
		return
	}
	for i, change := range changes {
		test := facts[change.Path]
		if test.Role != roleTest {
			continue
		}
		for _, j := range sources {
			source := facts[changes[j].Path]
			if !testNamesImplementation(test, source) {
				continue
			}
			merge(i, j, EvidenceTest, "test "+test.Path)
		}
	}
}

// testNamesImplementation reports exact or subsystem-backed test correspondence.
func testNamesImplementation(test, source *changeFacts) bool {
	if test.StemKey == "" || source.StemKey == "" {
		return false
	}
	stemMatch := test.StemKey == source.StemKey
	if !stemMatch && len(source.StemKey) >= 4 {
		// CheckoutSummary.test.tsx describes CheckoutSummary.tsx, which is
		// part of the Checkout change; a short stem is not prefix-matched.
		stemMatch = strings.HasPrefix(test.StemKey, source.StemKey) ||
			strings.HasPrefix(source.StemKey, test.StemKey)
	}
	if !stemMatch {
		return false
	}
	if test.Dir == source.Dir {
		return true
	}
	return len(sharedWords(test.PathWords, source.PathWords)) > 0
}

// addDocsEdges attaches documentation to the one logical change it names.
//
// A document that matches several distinct changes is left standalone and
// marked ambiguous: choosing one would be a guess, and silently attaching it to
// all of them would merge unrelated work.
func addDocsEdges(changes []FileChange, facts map[string]*changeFacts, union *unionFind, merge func(int, int, EvidenceKind, string)) map[string]bool {
	forced := make(map[string]bool)
	var sources []int
	for i, change := range changes {
		if facts[change.Path].Role == roleSource {
			sources = append(sources, i)
		}
	}
	if len(sources) == 0 {
		return forced
	}
	for i, change := range changes {
		doc := facts[change.Path]
		if doc.Role != roleDocs {
			continue
		}
		matches := make(map[int]int)
		representative := make(map[int]int)
		for _, j := range sources {
			source := facts[changes[j].Path]
			if !docNamesSource(doc, source) {
				continue
			}
			root := union.find(j)
			matches[root]++
			if existing, ok := representative[root]; !ok || j < existing {
				representative[root] = j
			}
		}
		if len(matches) == 0 {
			continue
		}
		if len(matches) > 1 {
			forced[doc.Path] = true
			continue
		}
		target := -1
		for _, j := range representative {
			if target < 0 || j < target {
				target = j
			}
		}
		if target >= 0 {
			merge(i, target, EvidenceDocs, "documentation "+doc.Path)
		}
	}
	return forced
}

// docNamesSource reports whether a document's stem names the source's path.
func docNamesSource(doc, source *changeFacts) bool {
	if len(sharedWords(doc.StemWords, source.PathWords)) > 0 {
		return true
	}
	return len(sharedWords(doc.StemWords, source.StemWords)) > 0
}

// addSubsystemEdges joins files whose distinctive basenames name the same
// subsystem. Directory names are deliberately absent: sharing a package is not
// proof that two changes are one logical change.
func addSubsystemEdges(changes []FileChange, facts map[string]*changeFacts, merge func(int, int, EvidenceKind, string)) {
	index := make(map[string][]int)
	for i, change := range changes {
		role := facts[change.Path].Role
		if role == roleDocs || role == roleTest {
			// Documentation and tests have purpose-built correspondence rules.
			// A shared stem token is not enough to attach them to a group.
			continue
		}
		for token := range facts[change.Path].StemWords {
			index[token] = append(index[token], i)
		}
	}
	tokens := make([]string, 0, len(index))
	for token := range index {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		indexes := index[token]
		if len(indexes) < 2 {
			continue
		}
		sort.Ints(indexes)
		first := indexes[0]
		for _, other := range indexes[1:] {
			merge(first, other, EvidenceSubsystem, "shared subsystem "+token)
		}
	}
}

// mergeSupportComponents attaches support-only components to implementation
// work when the evidence is unambiguous.
func mergeSupportComponents(changes []FileChange, facts map[string]*changeFacts, components map[int][]int, merge func(int, int, EvidenceKind, string), forced map[string]bool) {
	var sourceRoots []int
	for root, indexes := range components {
		for _, i := range indexes {
			if facts[changes[i].Path].Role == roleSource {
				sourceRoots = append(sourceRoots, root)
				break
			}
		}
	}
	sort.Ints(sourceRoots)

	for root, indexes := range components {
		if containsInt(sourceRoots, root) {
			continue
		}
		if !componentIsSupport(indexes, changes, facts) {
			continue
		}
		ambiguous := false
		for _, i := range indexes {
			if forced[changes[i].Path] {
				ambiguous = true
				break
			}
		}
		if ambiguous {
			// A document that names several logical changes stays explicit.
			continue
		}
		target := -1
		if len(sourceRoots) == 1 && componentCanAttach(indexes, changes, facts, components[sourceRoots[0]]) {
			target = sourceRoots[0]
		} else if len(sourceRoots) > 1 {
			matches := make(map[int]bool)
			for _, i := range indexes {
				for _, sourceRoot := range sourceRoots {
					if componentSharesWords(i, components[sourceRoot], changes, facts) {
						matches[sourceRoot] = true
					}
				}
			}
			if len(matches) == 1 {
				for sourceRoot := range matches {
					target = sourceRoot
				}
			}
		}
		if target < 0 || target == root {
			continue
		}
		merge(indexes[0], components[target][0], EvidenceSupport, "supports "+changes[components[target][0]].Path)
	}
}

// componentsHaveSource reports whether any component contains implementation
// source.
func componentsHaveSource(components map[int][]int, changes []FileChange, facts map[string]*changeFacts) bool {
	for _, indexes := range components {
		for _, i := range indexes {
			if facts[changes[i].Path].Role == roleSource {
				return true
			}
		}
	}
	return false
}

// componentsAreSupportPurpose reports that every changed file is repository
// documentation, build/CI, configuration, or metadata. Arbitrary text or data
// files are not merged merely because the tree has no source change.
func componentsAreSupportPurpose(components map[int][]int, changes []FileChange, facts map[string]*changeFacts) bool {
	for _, indexes := range components {
		for _, i := range indexes {
			change := changes[i]
			switch facts[change.Path].Role {
			case roleDocs, roleBuild, roleCI, roleConfig:
			case roleOther:
				if !isRepositorySupportPath(change.Path) {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// componentCanAttach reports a support component that may join the only
// implementation group. A test must still prove correspondence; the old
// "one source group exists" shortcut was how an unrelated test tree got
// absorbed.
func componentCanAttach(indexes []int, changes []FileChange, facts map[string]*changeFacts, target []int) bool {
	allTests, allDocs := true, true
	for _, i := range indexes {
		switch facts[changes[i].Path].Role {
		case roleTest:
			allDocs = false
		case roleDocs:
			allTests = false
		default:
			allTests = false
			allDocs = false
		}
	}
	if allTests || allDocs {
		// Test and documentation correspondence already had their chance in
		// the purpose-built rules. An unmatched one stays its own logical
		// change rather than being absorbed by the sole source group.
		return false
	}
	for _, i := range indexes {
		if pathGroupKey(changes[i].Path) == "." {
			return true
		}
		if componentSharesWords(i, target, changes, facts) {
			return true
		}
	}
	// Build/CI/config support at a non-root path can still join the only
	// implementation group.
	return true
}

// componentIsSupport reports a component made only of material that can attach
// to implementation work.
func componentIsSupport(indexes []int, changes []FileChange, facts map[string]*changeFacts) bool {
	for _, i := range indexes {
		switch facts[changes[i].Path].Role {
		case roleSource:
			return false
		case roleDocs, roleTest, roleBuild, roleCI, roleConfig, roleOther:
		default:
			return false
		}
	}
	return len(indexes) > 0
}

// componentSharesWords reports a distinctive path/subsystem word shared by one
// file and a whole component.
func componentSharesWords(leftIndex int, right []int, changes []FileChange, facts map[string]*changeFacts) bool {
	leftFact := facts[changes[leftIndex].Path]
	for _, j := range right {
		rightFact := facts[changes[j].Path]
		if len(sharedWords(leftFact.PathWords, rightFact.PathWords)) > 0 ||
			len(sharedWords(leftFact.StemWords, rightFact.PathWords)) > 0 {
			return true
		}
	}
	return false
}

// makeGroup builds one complete logical change from its member indexes.
func makeGroup(changes []FileChange, facts map[string]*changeFacts, indexes []int, root string) ChangeGroup {
	files := make([]FileChange, 0, len(indexes))
	for _, i := range indexes {
		files = append(files, changes[i])
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	primary := primaryFile(files)
	key := pathGroupKey(primary.Path)
	scope := groupScopeFor(files, facts, key, root)
	label := groupLabel(key, root)
	if scope != "" && scope != pathBase(key) {
		label = scope
	}
	return ChangeGroup{
		Key:   key,
		Label: label,
		Scope: scope,
		Files: files,
	}
}

// primaryFile picks the anchor for a group's key and label. Implementation
// source wins over support material when weights tie, so a README attached to a
// feature never renames the feature's commit.
func primaryFile(files []FileChange) FileChange {
	primary := files[0]
	bestWeight := -1
	bestSource := false
	for _, file := range files {
		source := changeRole(file) == roleSource
		weight := groupWeight(ChangeGroup{Files: []FileChange{file}})
		switch {
		case source && !bestSource:
			primary, bestWeight, bestSource = file, weight, true
		case source == bestSource && weight > bestWeight:
			primary, bestWeight = file, weight
		case source == bestSource && weight == bestWeight && file.Path < primary.Path:
			primary = file
		}
	}
	return primary
}

// groupScopeFor derives the conventional scope from the strongest subsystem
// word in the group. It falls back to the path-derived scope.
func groupScopeFor(files []FileChange, facts map[string]*changeFacts, key, root string) string {
	counts := make(map[string]int)
	for _, file := range files {
		// Only implementation basenames name a subsystem. A doc named
		// README.md inside commit/ describes the commit subsystem, not
		// "readme".
		if changeRole(file) != roleSource {
			continue
		}
		for token := range facts[file.Path].StemWords {
			counts[token]++
		}
	}
	best := ""
	for token, count := range counts {
		switch {
		case count > counts[best]:
			best = token
		case count == counts[best] && (best == "" || token < best):
			best = token
		}
	}
	// A single file's basename is not evidence of a subsystem: fall back to
	// the directory unless the same subsystem word names several files.
	if counts[best] >= 2 {
		if scope := scopeToken(best); scope != "" && fitsScopeBudget(scope) {
			return scope
		}
	}
	// A documentation-only change names its subject in the document, not in
	// the checkout directory. README.md is "readme", not the temporary root's
	// random basename.
	docsOnly := len(files) > 0
	for _, file := range files {
		if changeRole(file) != roleDocs {
			docsOnly = false
			break
		}
	}
	if docsOnly {
		// A document lives under a subsystem directory (docs/commit.md,
		// commit/README.md): the directory names the subsystem. Only a
		// root-level document is named by its own stem.
		if key != "." {
			if scope := groupScopeFrom(key, files, root); fitsScopeBudget(scope) {
				return scope
			}
		}
		for _, file := range files {
			stem := strings.TrimSuffix(pathBase(file.Path), path.Ext(file.Path))
			if scope := scopeToken(stem); scope != "" && fitsScopeBudget(scope) {
				return scope
			}
		}
	}
	scope := groupScopeFrom(key, files, root)
	if fitsScopeBudget(scope) {
		return scope
	}
	return "repo"
}

// groupConfidence decides whether a component may be proposed at all.
func groupConfidence(group ChangeGroup, facts map[string]*changeFacts, forced map[string]bool) GroupConfidence {
	if len(group.Files) > 1 {
		return GroupStrong
	}
	file := group.Files[0]
	fact := facts[file.Path]
	if forced[file.Path] {
		return GroupAmbiguous
	}
	switch fact.Role {
	case roleDocs, roleTest, roleBuild, roleCI, roleConfig:
		return GroupStrong
	case roleOther:
		if isRepositorySupportPath(file.Path) {
			return GroupWeak
		}
	}
	if fact.Semantic || fact.HasContent || changeHasChurn(file) {
		return GroupWeak
	}
	return GroupInsufficient
}

// isRepositorySupportPath reports conventional repository metadata files that
// are self-contained changes even without extractable symbols.
func isRepositorySupportPath(filePath string) bool {
	switch strings.ToLower(pathBase(filePath)) {
	case ".gitignore", ".gitattributes", ".gitmodules", ".dockerignore",
		".npmignore", ".editorconfig", ".prettierrc", ".prettierignore",
		".eslintignore", ".mailmap", ".gitkeep", "codeowners":
		return true
	}
	return false
}

// changeHasChurn reports observable line movement even when no symbol was
// extracted (a whitespace or comment edit, for example).
func changeHasChurn(change FileChange) bool {
	if !change.Untracked {
		// A tracked change is observable repository work even when its diff
		// carries no extractable identifier (a mode change, a binary, or a
		// whitespace-only edit). An untracked empty file is different: it has
		// no purpose the control plane can establish.
		return true
	}
	if change.Binary {
		return true
	}
	if change.Added != nil && *change.Added > 0 {
		return true
	}
	if change.Removed != nil && *change.Removed > 0 {
		return true
	}
	return change.Kind == KindDeleted || change.Kind == KindRenamed
}

// disambiguateGroupKeys keeps plan keys unique when independent changes share a
// directory.
func disambiguateGroupKeys(groups []ChangeGroup) {
	counts := make(map[string]int)
	for _, group := range groups {
		counts[group.Key]++
	}
	used := make(map[string]bool)
	for i := range groups {
		key := groups[i].Key
		if counts[key] > 1 {
			suffix := groupPrimaryStem(groups[i])
			if suffix == "" {
				suffix = "change"
			}
			key = key + "/" + suffix
		}
		base := key
		for n := 2; used[key]; n++ {
			key = base + "#" + itoa(n)
		}
		groups[i].Key = key
		used[key] = true
	}
}

// groupPrimaryStem returns a stable stem for a duplicate-key suffix.
func groupPrimaryStem(group ChangeGroup) string {
	if len(group.Files) == 0 {
		return ""
	}
	file := primaryFile(group.Files)
	stem := strings.TrimSuffix(pathBase(file.Path), path.Ext(file.Path))
	return scopeToken(stem)
}

// itoa is a tiny positive decimal formatter for key disambiguation.
func itoa(value int) string {
	if value <= 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

// groupPrimaryPath returns the lexicographically first path, used for a stable
// sort tie-break.
func groupPrimaryPath(group ChangeGroup) string {
	if len(group.Files) == 0 {
		return ""
	}
	return primaryFile(group.Files).Path
}

// assignGroupEvidence attaches the proven relationships to the component they
// joined. Evidence is sorted and de-duplicated so the same state renders the
// same explanation.
func assignGroupEvidence(groups []ChangeGroup, changes []FileChange, edges []EvidenceEdge) {
	byPath := make(map[string]string, len(changes))
	for _, group := range groups {
		for _, file := range group.Files {
			byPath[file.Path] = group.Key
		}
	}
	for _, edge := range edges {
		key := byPath[edge.From]
		if key == "" {
			continue
		}
		for i := range groups {
			if groups[i].Key != key {
				continue
			}
			groups[i].Evidence = append(groups[i].Evidence, formatEvidence(edge.Kind, edge.Detail))
			break
		}
	}
	for i := range groups {
		sort.Strings(groups[i].Evidence)
		groups[i].Evidence = uniqueStrings(groups[i].Evidence)
	}
}

// structuralLabel states which evidence class grouping actually used. It never
// claims a structural runtime that was not consulted.
func structuralLabel(edges []EvidenceEdge) string {
	if len(edges) == 0 {
		return "paths only"
	}
	kinds := make(map[EvidenceKind]bool)
	for _, edge := range edges {
		kinds[edge.Kind] = true
	}
	if len(kinds) == 1 && kinds[EvidenceImport] {
		return "package and import evidence (bounded sample)"
	}
	parts := make([]string, 0, 4)
	if kinds[EvidenceImport] {
		parts = append(parts, "import")
	}
	if kinds[EvidenceSymbol] {
		parts = append(parts, "symbol")
	}
	if kinds[EvidenceTest] {
		parts = append(parts, "test")
	}
	if kinds[EvidenceDocs] {
		parts = append(parts, "docs")
	}
	if kinds[EvidenceSubsystem] {
		parts = append(parts, "subsystem")
	}
	if kinds[EvidenceSupport] {
		parts = append(parts, "support")
	}
	return strings.Join(parts, ", ") + " evidence (bounded sample)"
}

// groupLabel renders a group key for evidence and notes.
func groupLabel(key, root string) string {
	if key == "." {
		base := filepath.Base(root)
		if base == "" || base == "." || base == string(filepath.Separator) {
			return "repository root"
		}
		return base + " root"
	}
	return key
}

// groupScopeFrom derives the conventional-commit scope a group suggests. The
// files are consulted because a docs directory's name is not its subject:
// docs/commit.md describes "commit", not "docs".
func groupScopeFrom(key string, files []FileChange, root string) string {
	if key == "." {
		if scope := scopeToken(filepath.Base(root)); scope != "" {
			return scope
		}
		return "repo"
	}
	segments := strings.Split(key, "/")
	switch {
	case segments[0] == "internal" && len(segments) >= 2:
		return scopeToken(segments[len(segments)-1])
	case segments[0] == "docs":
		for _, file := range files {
			base := strings.TrimSuffix(pathBase(file.Path), path.Ext(file.Path))
			if scope := scopeToken(base); scope != "" {
				return scope
			}
		}
		return "docs"
	case segments[0] == "cmd":
		return "cli"
	case segments[0] == ".github":
		return "ci"
	}
	return scopeToken(segments[len(segments)-1])
}

// modulePath reads the module path from go.mod. It bounds the read: a malformed
// or missing module file simply means import evidence is unavailable.
func modulePath(root string) string {
	data, err := readBoundedFile(filepath.Join(root, "go.mod"), 64*1024)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// moduleImports returns the module-local directories imported by one file.
func moduleImports(root, module, rel string) []string {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxImportEvidenceBytes {
		return nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), abs, nil, parser.ImportsOnly)
	if err != nil {
		// An unparseable changed file is not a grouping failure. It simply
		// contributes no edge, and the degradation is stated by the caller.
		return nil
	}
	var out []string
	for _, spec := range parsed.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if target, ok := moduleImportTarget(module, importPath); ok {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

// readBoundedFile reads at most limit bytes. A larger file is truncated rather
// than treated as an inspection failure.
func readBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, int64(limit)))
}

// selectGroups chooses the one logical change set a single /commit proposal
// represents.
//
// A staged scope is the human's explicit selection and is always accepted as
// one proposal. A worktree scope selects the dominant committable group, using
// the task objective as evidence only when it names that group. A change set
// whose parts are all support is one purpose by definition and stays together.
func selectGroups(groups []ChangeGroup, objective string, staged bool) []ChangeGroup {
	if len(groups) == 0 {
		return groups
	}
	if staged {
		for i := range groups {
			groups[i].Selected = groups[i].Confidence.Committable()
		}
		return groups
	}
	committable := 0
	for _, group := range groups {
		if group.Confidence.Committable() {
			committable++
		}
	}
	if committable == 1 {
		for i := range groups {
			groups[i].Selected = groups[i].Confidence.Committable()
		}
		return groups
	}
	if allSupportOnly(groups) && committable == len(groups) {
		for i := range groups {
			groups[i].Selected = true
		}
		return groups
	}
	chosen := objectiveGroup(groups, objective)
	if chosen < 0 {
		chosen = dominantGroup(groups)
	}
	if chosen >= 0 {
		groups[chosen].Selected = true
	}
	return groups
}

// allSupportOnly reports that no group contains implementation source.
func allSupportOnly(groups []ChangeGroup) bool {
	for _, group := range groups {
		if groupHasSource(&group) {
			return false
		}
	}
	return true
}

// dominantGroup returns the highest-evidence committable group, with a stable
// tie-break.
func dominantGroup(groups []ChangeGroup) int {
	best := -1
	for i := range groups {
		if !groups[i].Confidence.Committable() {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		bestSource := groupHasSource(&groups[best])
		thisSource := groupHasSource(&groups[i])
		switch {
		case thisSource && !bestSource:
			best = i
		case thisSource == bestSource && groupWeight(groups[i]) > groupWeight(groups[best]):
			best = i
		case thisSource == bestSource && groupWeight(groups[i]) == groupWeight(groups[best]) &&
			groups[i].Key < groups[best].Key:
			best = i
		}
	}
	return best
}

// objectiveGroup selects the group the authoritative task objective names.
func objectiveGroup(groups []ChangeGroup, objective string) int {
	tokens := tokenSet([]string{objective})
	if len(tokens) == 0 {
		return -1
	}
	best, bestScore := -1, 0
	for i, group := range groups {
		if !group.Confidence.Committable() {
			continue
		}
		score := 0
		for token := range tokenSet(strings.Split(group.Key, "/")) {
			if tokens[token] {
				score += 2
			}
		}
		if tokens[group.Scope] {
			score += 2
		}
		for _, file := range group.Files {
			base := strings.TrimSuffix(pathBase(file.Path), path.Ext(file.Path))
			if tokens[strings.ToLower(base)] {
				score++
			}
		}
		if score > bestScore || (score == bestScore && score > 0 && best >= 0 && group.Key < groups[best].Key) {
			best, bestScore = i, score
		}
	}
	return best
}

// groupWeight is the deterministic evidence weight for a group.
func groupWeight(group ChangeGroup) int {
	weight := len(group.Files) * 1000
	for _, file := range group.Files {
		if file.Added != nil {
			weight += *file.Added
		}
		if file.Removed != nil {
			weight += *file.Removed
		}
	}
	return weight
}

// changeRole returns the semantic role, deriving it from the path when a caller
// constructed the change without classification.
func changeRole(file FileChange) kindRole {
	if file.Role != roleOther || file.Path == "" {
		return file.Role
	}
	return roleOf(file.Path)
}

// groupHasSource reports whether any file is implementation source.
func groupHasSource(group *ChangeGroup) bool {
	for _, file := range group.Files {
		if changeRole(file) == roleSource {
			return true
		}
	}
	return false
}

// tokenSet lowercases, splits separators, and drops very short words.
func tokenSet(values []string) map[string]bool {
	out := make(map[string]bool)
	for _, value := range values {
		for _, token := range splitPathWords(value) {
			if len(token) >= 3 && !genericPathWords[token] {
				out[token] = true
			}
		}
	}
	return out
}

// sortedWordKeys returns map keys in stable order.
func sortedWordKeys(words map[string]bool) []string {
	keys := make([]string, 0, len(words))
	for word := range words {
		keys = append(keys, word)
	}
	sort.Strings(keys)
	return keys
}

// containsInt reports membership in an ascending int slice.
func containsInt(values []int, value int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// unionFind is a deterministic disjoint-set structure for grouping.
type unionFind struct {
	parent []int
	size   []int
}

// newUnionFind builds one component per element.
func newUnionFind(n int) *unionFind {
	parent := make([]int, n)
	size := make([]int, n)
	for i := range parent {
		parent[i] = i
		size[i] = 1
	}
	return &unionFind{parent: parent, size: size}
}

// find returns the component root with path compression.
func (u *unionFind) find(value int) int {
	for u.parent[value] != value {
		u.parent[value] = u.parent[u.parent[value]]
		value = u.parent[value]
	}
	return value
}

// join merges two components, preferring the lower index on a tie so two
// identical states cannot produce different roots.
func (u *unionFind) join(left, right int) {
	left = u.find(left)
	right = u.find(right)
	if left == right {
		return
	}
	if u.size[left] < u.size[right] || (u.size[left] == u.size[right] && right < left) {
		left, right = right, left
	}
	u.parent[right] = left
	u.size[left] += u.size[right]
}
