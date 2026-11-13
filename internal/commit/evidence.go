package commit

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/vcs"
)

// EvidenceKind names one proven relationship between changed paths.
//
// It is deliberately a small vocabulary. A grouping decision must be able to say
// why two paths belong together, and a future structural runtime must be able to
// supply the same facts without changing /commit's semantics.
type EvidenceKind string

// Grouping evidence kinds.
const (
	EvidenceImport    EvidenceKind = "import"
	EvidenceSymbol    EvidenceKind = "symbol"
	EvidenceTest      EvidenceKind = "test"
	EvidenceDocs      EvidenceKind = "docs"
	EvidenceSubsystem EvidenceKind = "subsystem"
	EvidenceSupport   EvidenceKind = "support"
)

// EvidenceEdge is one relationship between two changed paths.
type EvidenceEdge struct {
	// From and To are repository-relative paths.
	From string
	To   string
	// Kind is the proven relationship.
	Kind EvidenceKind
	// Detail is bounded, control-plane text used in the group evidence.
	Detail string
}

// StructuralEvidenceProvider supplies relationships the grouper cannot derive
// from path shape alone.
//
// The current implementation is a bounded Go import scan. A future OHGO
// AST/graph runtime can implement this interface with symbol references, calls,
// and type relationships; the grouper and /commit -a do not need to change.
type StructuralEvidenceProvider interface {
	Edges(ctx context.Context, root string, changes []FileChange) []EvidenceEdge
}

// DefaultStructuralEvidence is the bounded import provider used when a Service
// does not inject a richer runtime.
var DefaultStructuralEvidence StructuralEvidenceProvider = importEvidenceProvider{}

// importEvidenceProvider proves module-local Go import relationships between
// changed files.
type importEvidenceProvider struct{}

// Edges returns import relationships that cross changed paths.
func (importEvidenceProvider) Edges(ctx context.Context, root string, changes []FileChange) []EvidenceEdge {
	module := modulePath(root)
	byDir := make(map[string][]string, len(changes))
	byKey := make(map[string][]string, len(changes))
	for _, change := range changes {
		n := strings.Count(change.Path, "/")
		dir := manageDir(change.Path, n)
		byDir[dir] = append(byDir[dir], change.Path)
		byKey[importKey(change.Path)] = append(byKey[importKey(change.Path)], change.Path)
	}
	var edges []EvidenceEdge
	files := 0
	seen := make(map[string]bool)
	add := func(from, to, detail string) {
		if from == to {
			return
		}
		relation := from + "\x00" + to + "\x00" + detail
		if seen[relation] {
			return
		}
		seen[relation] = true
		edges = append(edges, EvidenceEdge{
			From: from, To: to, Kind: EvidenceImport, Detail: detail,
		})
	}
	for _, change := range changes {
		if files >= maxImportEvidenceFiles {
			break
		}
		if change.Kind == KindDeleted || change.Sensitive {
			continue
		}
		select {
		case <-ctx.Done():
			return edges
		default:
		}
		isGo := strings.HasSuffix(change.Path, ".go") && module != ""
		if !isGo && !scriptSourceExtensions[strings.ToLower(pathExt(pathBase(change.Path)))] {
			continue
		}
		files++
		if isGo {
			fromDir := manageDir(change.Path, strings.Count(change.Path, "/"))
			for _, target := range moduleImports(root, module, change.Path) {
				paths, ok := byDir[target]
				if !ok || target == fromDir {
					continue
				}
				for _, to := range paths {
					add(change.Path, to, "imports "+target)
				}
			}
			continue
		}
		// Script imports are lexical and bounded. They let a TypeScript or
		// JavaScript feature connect across directories even when its file
		// names do not share a subsystem token.
		for _, spec := range scriptImports(root, change.Path) {
			target, ok := resolveScriptImport(change.Path, spec)
			if !ok {
				continue
			}
			paths := byKey[target]
			if len(paths) == 0 {
				paths = byKey[target+"/index"]
			}
			for _, to := range paths {
				add(change.Path, to, "imports "+spec)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Detail < edges[j].Detail
	})
	return edges
}

// scriptImportPattern finds the import/specifier forms used by JavaScript and
// TypeScript. It is a bounded lexical scan, not a second parser.
var scriptImportPattern = regexp.MustCompile(`(?:from\s*|import\s*\(\s*|require\s*\(\s*|import\s+)["']([^"']+)["']`)

// scriptSourceExtensions are the files whose relative imports group a feature.
var scriptSourceExtensions = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".vue": true, ".svelte": true,
}

// scriptImports returns relative import specifiers from one script file.
func scriptImports(root, rel string) []string {
	if !scriptSourceExtensions[strings.ToLower(pathExt(pathBase(rel)))] {
		return nil
	}
	data, err := readBoundedFile(filepath.Join(root, filepath.FromSlash(rel)), maxImportEvidenceBytes)
	if err != nil {
		return nil
	}
	matches := scriptImportPattern.FindAllStringSubmatch(string(data), -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		spec := strings.TrimSpace(match[1])
		if strings.HasPrefix(spec, ".") {
			out = append(out, spec)
		}
	}
	sort.Strings(out)
	return uniqueStrings(out)
}

// resolveScriptImport resolves a relative specifier to an extensionless path.
func resolveScriptImport(from, spec string) (string, bool) {
	if !strings.HasPrefix(spec, ".") {
		return "", false
	}
	target := path.Clean(path.Join(path.Dir(from), spec))
	if target == "." || strings.HasPrefix(target, "../") {
		return "", false
	}
	return importKey(target), true
}

// importKey normalizes a path for import resolution.
func importKey(filePath string) string {
	clean := strings.TrimPrefix(path.Clean(filePath), "./")
	ext := path.Ext(clean)
	if scriptSourceExtensions[strings.ToLower(ext)] || ext == ".go" {
		clean = strings.TrimSuffix(clean, ext)
	}
	return clean
}

// manageDir returns the directory key for a path without importing filepath for
// slash-normalized repository paths.
func manageDir(filePath string, separators int) string {
	if separators == 0 {
		return "."
	}
	return path.Dir(strings.TrimPrefix(filePath, "./"))
}

// GroupConfidence reports how strongly the control plane can defend a grouping
// decision.
type GroupConfidence string

// Grouping confidence levels. Only strong and weak groups are committable.
const (
	// GroupStrong means a structural, test, docs, subsystem, or symbol
	// relationship proves the files belong together.
	GroupStrong GroupConfidence = "strong"
	// GroupWeak means a single self-contained change with no relation to any
	// other changed path.
	GroupWeak GroupConfidence = "weak"
	// GroupAmbiguous means the evidence points at more than one logical change
	// and the control plane refuses to guess.
	GroupAmbiguous GroupConfidence = "ambiguous"
	// GroupInsufficient means the change set has no semantic evidence at all,
	// so manufacturing a commit boundary would be invention.
	GroupInsufficient GroupConfidence = "insufficient_evidence"
)

// Committable reports a group the control plane is willing to propose.
// Ambiguous and insufficient groups remain explicit and are never proposed.
func (c GroupConfidence) Committable() bool {
	return c != GroupAmbiguous && c != GroupInsufficient
}

// changeFacts is the bounded, deterministic evidence extracted for one path.
type changeFacts struct {
	Path      string
	Role      kindRole
	Dir       string
	Stem      string
	StemKey   string
	PathWords map[string]bool
	StemWords map[string]bool
	Declared  []string
	Words     map[string]bool
	// Semantic reports that at least one identifier was observed. It is what
	// separates a self-contained change from an empty or opaque blob.
	Semantic bool
	// HasContent reports that an untracked file was not empty. It keeps a
	// non-empty but symbol-free change committable while an empty new file
	// stays insufficient evidence.
	HasContent bool
}

// maxFactsPerFile bounds one file's identifier memory contribution.
const maxFactsPerFile = 256

// maxGroupingFileBytes bounds reading an untracked file for semantic evidence.
// The diff path is already bounded by maxGroupingDiffBytes.
const maxGroupingFileBytes = 64 * 1024

// maxGroupingDiffBytes bounds the diff parsed for grouping. A larger diff
// degrades semantic evidence; it never changes inclusion.
const maxGroupingDiffBytes = 256 * 1024

// maxSemanticReadBytes bounds the aggregate untracked-file read for grouping.
// The bound is deliberately small: grouping needs a hint, not the repository.
const maxSemanticReadBytes = 4 << 20

// groupingDiff reads one bounded diff restricted to the candidate paths.
func groupingDiff(ctx context.Context, repo *vcs.RepoInfo, mode Mode, changes []FileChange) string {
	if len(changes) == 0 {
		return ""
	}
	files := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Sensitive {
			// A protected deletion is committable, but its removed bytes are
			// never grouping evidence either.
			continue
		}
		files = append(files, change.Path)
	}
	if len(files) == 0 {
		return ""
	}
	value, err := repo.Diff(ctx, vcs.DiffOptions{Files: uniqueSorted(files), Cached: mode == ModeStaged})
	if err != nil {
		return ""
	}
	return truncateUTF8(value, maxGroupingDiffBytes)
}

// collectChangeFacts builds the evidence projection for every candidate path.
//
// The diff supplies changed declarations and identifiers for tracked paths. An
// untracked path is read directly because git diff cannot describe it. A
// sensitive path is never read: its content must not leave the machine merely
// because grouping needs a hint.
func collectChangeFacts(root string, changes []FileChange, diff string) (map[string]*changeFacts, bool) {
	fromDiff := parseDiffFacts(diff)
	facts := make(map[string]*changeFacts, len(changes))
	readBudget := maxSemanticReadBytes
	truncated := false
	for _, change := range changes {
		fact := &changeFacts{
			Path:      change.Path,
			Role:      changeRole(change),
			Dir:       manageDir(change.Path, strings.Count(change.Path, "/")),
			PathWords: distinctiveWords(change.Path),
			Words:     make(map[string]bool),
		}
		base := pathBase(change.Path)
		fact.Stem = strings.TrimSuffix(base, pathExt(base))
		fact.StemKey = normalizeStem(fact.Stem)
		fact.StemWords = distinctiveWords(stripTestMarkers(fact.Stem))

		if parsed, ok := fromDiff[change.Path]; ok {
			fact.Declared = parsed.declared
			fact.Words = parsed.words
		}
		if change.Untracked && !change.Sensitive {
			if readBudget > 0 {
				read := readUntrackedFacts(root, change.Path, fact)
				readBudget -= read
				fact.HasContent = read > 0
			} else {
				truncated = true
			}
		}
		fact.Semantic = len(fact.Declared) > 0 || len(fact.Words) > 0
		facts[change.Path] = fact
	}
	return facts, truncated
}

// diffFacts is the bounded identifier projection of one file's diff hunk.
type diffFacts struct {
	declared []string
	words    map[string]bool
}

// parseDiffFacts scans added and removed lines. Deleted content is evidence
// about the change, not a claim that the file still exists.
func parseDiffFacts(diff string) map[string]*diffFacts {
	out := make(map[string]*diffFacts)
	if diff == "" {
		return out
	}
	current := ""
	currentFacts := func() *diffFacts {
		if current == "" {
			return nil
		}
		facts, ok := out[current]
		if !ok {
			facts = &diffFacts{words: make(map[string]bool)}
			out[current] = facts
		}
		return facts
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = ""
		case strings.HasPrefix(line, "+++ "):
			if target := diffHeaderPath(line[4:], "b/"); target != "" {
				current = target
			}
		case strings.HasPrefix(line, "--- "):
			if current == "" {
				if source := diffHeaderPath(line[4:], "a/"); source != "" {
					current = source
				}
			}
		case strings.HasPrefix(line, "@@"):
			continue
		case current != "" && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
				continue
			}
			facts := currentFacts()
			scanLine(line[1:], facts)
		}
	}
	for _, facts := range out {
		sort.Strings(facts.declared)
		facts.declared = uniqueStrings(facts.declared)
	}
	return out
}

// diffHeaderPath extracts the repository path from a diff header, ignoring the
// /dev/null side of an addition or deletion.
func diffHeaderPath(value, prefix string) string {
	value = strings.TrimSpace(value)
	if value == "/dev/null" {
		return ""
	}
	if i := strings.IndexByte(value, '\t'); i >= 0 {
		value = value[:i]
	}
	value = strings.TrimPrefix(value, prefix)
	value = strings.TrimPrefix(value, "./")
	if value == "" || value == "/dev/null" {
		return ""
	}
	return value
}

// readUntrackedFacts reads a bounded head of a new file. Symlinks and
// non-regular files are not opened.
func readUntrackedFacts(root, rel string, fact *changeFacts) int {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	file, err := os.Open(abs)
	if err != nil {
		return 0
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxGroupingFileBytes))
	if err != nil {
		return 0
	}
	scanned := &diffFacts{words: make(map[string]bool)}
	for _, line := range strings.Split(string(data), "\n") {
		scanLine(line, scanned)
	}
	sort.Strings(scanned.declared)
	fact.Declared = uniqueStrings(scanned.declared)
	for word := range scanned.words {
		fact.Words[word] = true
	}
	return len(data)
}

// declarationPattern finds a declared symbol in an added or removed line.
//
// The optional receiver clause is what makes a Go method declaration yield the
// method name instead of the receiver variable.
// Local const/let/var declarations are deliberately excluded: they are
// implementation details, not symbols another changed file can meaningfully
// share.
var declarationPattern = regexp.MustCompile(`\b(?:func\s+(?:\([^)]*\)\s*)?|function\s+|class\s+|interface\s+|struct\s+|enum\s+|type\s+|def\s+|trait\s+|impl\s+)([A-Za-z_][A-Za-z0-9_]*)`)

// scanLine records declarations and identifier words from one changed line.
func scanLine(line string, facts *diffFacts) {
	if facts == nil {
		return
	}
	for _, match := range declarationPattern.FindAllStringSubmatch(line, -1) {
		if len(match) < 2 {
			continue
		}
		name := match[1]
		if isKeyword(name) || isGenericIdentifier(name) {
			continue
		}
		facts.declared = append(facts.declared, name)
	}
	for _, word := range identifierWords(line) {
		if len(facts.words) >= maxFactsPerFile {
			break
		}
		if isKeyword(word) || isGenericIdentifier(word) {
			continue
		}
		facts.words[word] = true
	}
}

// identifierWords splits a line into identifier-shaped words.
func identifierWords(line string) []string {
	var out []string
	start := -1
	for i, r := range line {
		if isIdentifierRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, line[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, line[start:])
	}
	return out
}

// isIdentifierRune reports a character that can belong to a source identifier.
func isIdentifierRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' || r == '_' || r == '$'
}

// distinctiveWords extracts lower-case subsystem words from a repository path
// or file stem, dropping structural bucket names.
func distinctiveWords(value string) map[string]bool {
	out := make(map[string]bool)
	for _, token := range splitPathWords(value) {
		if len(token) < 3 || genericPathWords[token] || isAllDigits(token) {
			continue
		}
		out[token] = true
	}
	return out
}

// splitPathWords splits separators and camelCase into lower-case words.
func splitPathWords(value string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = nil
		}
	}
	runes := []rune(value)
	for i, r := range runes {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			if len(current) > 0 {
				previous := runes[i-1]
				// camelCase and letter/digit boundaries are word boundaries.
				if upperRune(r) && lowerRune(previous) || digitRune(r) != digitRune(previous) {
					flush()
				}
			}
			current = append(current, r)
		default:
			flush()
		}
	}
	flush()
	return words
}

// upperRune reports an ASCII upper-case letter.
func upperRune(r rune) bool { return r >= 'A' && r <= 'Z' }

// lowerRune reports an ASCII lower-case letter.
func lowerRune(r rune) bool { return r >= 'a' && r <= 'z' }

// digitRune reports an ASCII digit.
func digitRune(r rune) bool { return r >= '0' && r <= '9' }

// normalizeStem lower-cases a stem and removes conventional test markers so a
// test file and its implementation can be compared exactly.
func normalizeStem(stem string) string {
	lower := strings.ToLower(strings.TrimSpace(stem))
	for _, suffix := range []string{
		"_test", "-test", ".test", "_spec", "-spec", ".spec",
		"_specs", "-specs", ".specs", "_tests", "-tests", ".tests",
	} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimSuffix(lower, suffix)
		}
	}
	return lower
}

// stripTestMarkers removes test markers before word extraction.
func stripTestMarkers(stem string) string {
	for _, suffix := range []string{
		"_test", "-test", ".test", "_spec", "-spec", ".spec",
		"_specs", "-specs", ".specs", "_tests", "-tests", ".tests",
	} {
		if strings.HasSuffix(strings.ToLower(stem), suffix) {
			return stem[:len(stem)-len(suffix)]
		}
	}
	return stem
}

// sharedWords returns the deterministic intersection of two word sets.
func sharedWords(left, right map[string]bool) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	var out []string
	for word := range left {
		if right[word] {
			out = append(out, word)
		}
	}
	sort.Strings(out)
	return out
}

// sameStringSet reports set equality for the plan revalidation guard.
func sameStringSet(left, right []string) bool {
	return sameStrings(left, right)
}

// isKeyword reports a language keyword that carries no subsystem meaning.
func isKeyword(word string) bool {
	return keywords[strings.ToLower(word)]
}

// isGenericIdentifier drops common standard-library and test vocabulary from
// symbol evidence. It is deliberately conservative: a shared generic word is
// not proof of cohesion.
func isGenericIdentifier(word string) bool {
	return genericIdentifiers[strings.ToLower(word)]
}

// isDistinctiveSymbol reports a declaration worth treating as grouping
// evidence. A one- or two-character test helper is not a subsystem.
func isDistinctiveSymbol(name string) bool {
	return len(name) >= 4 && !isKeyword(name) && !isGenericIdentifier(name)
}

// isAllDigits reports a numeric token.
func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// uniqueStrings keeps the first occurrence in stable order.
func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// pathGroupKey derives the directory that owns a path.
func pathGroupKey(filePath string) string {
	clean := strings.TrimPrefix(path.Clean(filePath), "./")
	dir := path.Dir(clean)
	if dir == "." {
		return "."
	}
	return dir
}

// evidenceDetail renders a bounded detail for one edge.
func evidenceDetail(kind EvidenceKind, detail string) string {
	detail = safePath(strings.TrimSpace(detail))
	if detail == "" {
		return string(kind)
	}
	return detail
}

// formatEvidence renders one evidence sentence.
func formatEvidence(kind EvidenceKind, detail string) string {
	return fmt.Sprintf("%s: %s", kind, evidenceDetail(kind, detail))
}

// moduleImportTarget normalizes a module-local import into a directory path.
func moduleImportTarget(module, importPath string) (string, bool) {
	if !strings.HasPrefix(importPath, module+"/") {
		return "", false
	}
	target := strings.TrimPrefix(importPath, module+"/")
	if target == "" {
		return ".", true
	}
	return target, true
}

// keywords are language tokens that carry no subsystem meaning.
var keywords = map[string]bool{
	"abstract": true, "and": true, "as": true, "assert": true, "async": true,
	"await": true, "break": true, "case": true, "catch": true, "chan": true,
	"class": true, "const": true, "continue": true, "debugger": true,
	"default": true, "defer": true, "def": true, "delete": true, "do": true,
	"else": true, "enum": true, "export": true, "extends": true, "fallthrough": true,
	"false": true, "finally": true, "for": true, "from": true, "func": true,
	"function": true, "go": true, "goto": true, "if": true, "implements": true,
	"impl": true, "import": true, "in": true, "instanceof": true, "interface": true,
	"let": true, "map": true, "module": true, "namespace": true, "new": true,
	"nil": true, "none": true, "not": true, "null": true, "or": true,
	"package": true, "pass": true, "private": true, "protected": true,
	"public": true, "raise": true, "range": true, "readonly": true,
	"return": true, "select": true, "static": true, "struct": true,
	"super": true, "switch": true, "this": true, "throw": true, "trait": true,
	"true": true, "try": true, "type": true, "typeof": true, "var": true,
	"void": true, "while": true, "with": true, "yield": true,
}

// genericIdentifiers are common identifiers that do not prove cohesion when two
// diffs happen to share them.
var genericIdentifiers = map[string]bool{
	"added": true, "after": true, "args": true, "argument": true, "before": true,
	"all": true, "block": true, "bool": true, "buffer": true, "byte": true,
	"bytes": true, "call": true, "calls": true, "case": true, "change": true,
	"changes": true, "channel": true, "current": true, "diagnostics": true,
	"edges": true, "evidence": true, "excluded": true, "grouping": true,
	"must": true, "parts": true, "proposal": true, "proven": true,
	"remaining": true, "rule": true, "validation": true, "vocabulary": true,
	"check": true, "client": true, "command": true, "config": true, "context": true,
	"copy": true, "count": true, "data": true, "default": true, "deleted": true,
	"describe": true, "detail": true, "details": true, "duration": true,
	"element": true, "empty": true, "error": true, "errors": true, "event": true,
	"events": true, "expected": true, "field": true, "fields": true, "file": true,
	"files": true, "first": true, "flag": true, "flags": true, "float": true,
	"found": true, "group": true, "groups": true, "handler": true, "header": true,
	"help": true, "html": true, "http": true, "index": true, "input": true,
	"int": true, "int64": true, "item": true, "items": true, "json": true,
	"key": true, "keys": true, "last": true, "length": true, "line": true,
	"lines": true, "list": true, "log": true, "logs": true, "main": true,
	"make": true, "manager": true, "map": true, "message": true, "meta": true,
	"method": true, "mock": true, "mode": true, "model": true, "name": true,
	"names": true, "new": true, "node": true, "none": true, "object": true,
	"offset": true, "option": true, "options": true, "output": true, "page": true,
	"param": true, "params": true, "parse": true, "path": true, "paths": true,
	"props": true, "provider": true, "query": true, "react": true, "read": true,
	"record": true, "records": true, "render": true, "request": true,
	"response": true, "result": true, "results": true, "return": true,
	"row": true, "rows": true, "run": true, "runner": true, "sb": true,
	"schema": true, "scope": true, "second": true, "select": true, "server": true,
	"service": true, "set": true, "snapshot": true,
	"size": true, "slice": true, "source": true, "spec": true, "state": true,
	"status": true, "store": true, "string": true, "strings": true, "struct": true,
	"summary": true, "table": true, "target": true, "task": true, "test": true,
	"tests": true, "text": true, "time": true, "title": true, "tmp": true,
	"type": true, "types": true, "update": true, "updated": true, "url": true,
	"use": true, "util": true, "utils": true, "valid": true, "validate": true,
	"value": true, "values": true, "var": true, "view": true, "want": true,
	"widget": true, "work": true, "write": true,
}

// genericPathWords are structural directory or file-role names. They are never
// evidence that two files describe the same subsystem.
var genericPathWords = map[string]bool{
	"api": true, "apis": true, "app": true, "apps": true, "asset": true,
	"assets": true, "backend": true, "build": true, "builds": true, "cache": true,
	"client": true, "cmd": true, "command": true, "commands": true, "common": true,
	"component": true, "components": true, "config": true, "configs": true,
	"context": true, "controller": true, "controllers": true, "core": true,
	"css":  true,
	"data": true, "database": true, "db": true, "demo": true, "dist": true,
	"doc": true, "docs": true, "documentation": true, "domain": true,
	"domains": true, "example": true, "examples": true, "feature": true,
	"features": true, "fixture": true, "fixtures": true, "frontend": true,
	"go": true, "handler": true, "handlers": true, "helper": true, "helpers": true,
	"hook": true, "hooks": true, "html": true, "http": true, "https": true,
	"index": true, "internal": true, "js": true, "json": true, "jsx": true,
	"layout": true, "layouts": true, "lib": true, "libs": true, "main": true,
	"manager": true, "md": true, "mdx": true, "mock": true, "mocks": true,
	"mod": true, "model": true, "models": true, "module": true, "modules": true,
	"page": true, "pages": true, "pkg": true, "pkgs": true, "public": true,
	"py": true, "reducer": true, "reducers": true, "route": true, "router": true,
	"routes": true, "rs": true, "scss": true, "server": true, "service": true,
	"services": true, "shared": true, "sh": true, "spec": true, "specs": true,
	"sql": true, "src": true, "state": true, "static": true, "store": true,
	"stores": true, "style": true, "styles": true, "temp": true, "test": true,
	"testdata": true, "tests": true, "tmp": true, "ts": true, "tsx": true,
	"type": true, "types": true, "ui": true, "use": true, "util": true, "utils": true,
	"view": true, "views": true, "web": true, "widget": true, "widgets": true,
	"xml": true, "yaml": true, "yml": true,
}
