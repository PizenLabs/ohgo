package structural

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Documentation drift compares what documents claim exists with what the same
// snapshot actually inventoried. It is deliberately path-shaped: a document can
// prove that it names a path, and the inventory can prove whether that path
// exists. Nothing here infers implementation quality from prose.

// Drift bounds. A documentation scan is a bounded read, not a second index.
const (
	DriftMaxDocuments     = 20
	DriftMaxDocumentBytes = 256 << 10
	DriftMaxTotalBytes    = 1 << 20
	DriftMaxClaims        = 400
	DriftMaxFindings      = 80
)

// DriftSeverity classifies a finding by what the missing target is.
type DriftSeverity string

// Drift severities.
const (
	// DriftHigh is a missing source, configuration, API, or executable path.
	DriftHigh DriftSeverity = "high"
	// DriftMedium is a missing documentation target such as a linked page or
	// image.
	DriftMedium DriftSeverity = "medium"
	// DriftLow is a missing target that cannot be classified more precisely.
	DriftLow DriftSeverity = "low"
)

// DriftFinding is one documented target that the inventory could not confirm.
type DriftFinding struct {
	// Code is documented_path_missing when absence was provable, or
	// documented_path_unverified when the inventory could not decide.
	Code     string        `json:"code"`
	Severity DriftSeverity `json:"severity"`
	// Document is the repository-relative documentation path.
	Document string `json:"document"`
	Line     int    `json:"line"`
	// Target is the repository-relative path the document named.
	Target string `json:"target"`
	// Detail is a bounded, prose-free statement of what was compared.
	Detail string `json:"detail"`
	// Documentation proves the document made the claim.
	Documentation Evidence `json:"documentation"`
	// Source proves what the inventory observed about the target.
	Source Evidence `json:"source"`
}

// DriftReport is the bounded result of one documentation comparison.
type DriftReport struct {
	// Documents are the documentation files that were read, in scan order.
	Documents []string `json:"documents,omitempty"`
	// Claims counts unique path claims found in those documents.
	Claims int `json:"claims"`
	// Resolved counts claims whose target exists in the inventory.
	Resolved int `json:"resolved"`
	// Findings are the claims that could not be confirmed.
	Findings []DriftFinding `json:"findings,omitempty"`
	// Unreadable names documents that could not be read.
	Unreadable []string `json:"unreadable,omitempty"`
	// Truncated reports that a bound dropped documents, bytes, claims, or
	// findings.
	Truncated bool `json:"truncated,omitempty"`
	// Reason names the limitation when Truncated or Evidence is UNKNOWN.
	Reason   string   `json:"reason,omitempty"`
	Evidence Evidence `json:"evidence"`
}

var (
	driftLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	driftCodePattern = regexp.MustCompile("`([^`\\n]{1,240})`")
)

// runDocumentationDrift reads bounded documentation and compares every
// path-shaped claim with the inventory of the same snapshot.
func runDocumentationDrift(s *analysisState) error {
	root := strings.TrimSpace(s.inventory.Snapshot.Root)
	report := &DriftReport{}
	if root == "" {
		report.Evidence = UnknownEvidence("the snapshot has no repository root")
		s.drift = report
		return nil
	}
	files, dirs, prefixes := driftInventorySets(s.inventory)
	documents, docTruncated := driftDocuments(s.inventory)
	report.Truncated = docTruncated || s.inventory.FilesTruncated
	inventoryBounded := s.inventory.FilesTruncated
	var (
		refs        []EvidenceRef
		totalBytes  int
		unverified  int
		claimsSeen  = map[string]bool{}
		findings    []DriftFinding
		bytesPinned bool
	)
	for _, document := range documents {
		abs := filepath.Join(root, filepath.FromSlash(document))
		data, truncated, err := readDriftDocument(abs)
		if err != nil {
			report.Unreadable = append(report.Unreadable, document)
			continue
		}
		if truncated {
			bytesPinned = true
		}
		if totalBytes+len(data) > DriftMaxTotalBytes {
			data = data[:max(0, DriftMaxTotalBytes-totalBytes)]
			bytesPinned = true
		}
		totalBytes += len(data)
		if totalBytes >= DriftMaxTotalBytes {
			bytesPinned = true
		}
		report.Documents = append(report.Documents, document)
		for _, claim := range driftClaims(document, string(data), prefixes, files, dirs) {
			key := fmt.Sprintf("%s\x00%d\x00%s", document, claim.line, claim.target)
			if claimsSeen[key] {
				continue
			}
			claimsSeen[key] = true
			if report.Claims >= DriftMaxClaims {
				report.Truncated = true
				continue
			}
			report.Claims++
			claimEvidence := ObservedEvidence(KindDocumentClaim, EvidenceRef{
				Kind: KindDocumentClaim, Path: document, Line: claim.line,
			})
			if len(refs) < 8 {
				refs = append(refs, EvidenceRef{Kind: KindDocumentClaim, Path: document, Line: claim.line})
			}
			if files[claim.target] || dirs[claim.target] {
				report.Resolved++
				continue
			}
			provable := !s.inventory.FilesTruncated &&
				s.inventory.Snapshot.ScopeComplete &&
				!driftExcluded(claim.target, s.inventory)
			finding := DriftFinding{
				Code: "documented_path_missing", Severity: driftSeverity(claim.target),
				Document: document, Line: claim.line, Target: claim.target,
				Detail:        fmt.Sprintf("documented path %q is not in the indexed inventory", claim.target),
				Documentation: claimEvidence,
			}
			if provable {
				finding.Source = ObservedEvidence(KindInventory, EvidenceRef{
					Kind: KindInventory, Path: claim.target,
				})
				finding.Source.Reason = "complete inventory contains no matching file or directory"
			} else {
				finding.Code = "documented_path_unverified"
				finding.Severity = DriftLow
				finding.Detail = fmt.Sprintf("documented path %q could not be checked against a complete inventory", claim.target)
				finding.Source = UnknownEvidence(driftUnverifiedReason(s.inventory))
				unverified++
			}
			findings = append(findings, finding)
		}
	}
	if len(findings) > DriftMaxFindings {
		findings = findings[:DriftMaxFindings]
		report.Truncated = true
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return driftSeverityRank(findings[i].Severity) < driftSeverityRank(findings[j].Severity)
		}
		if findings[i].Document != findings[j].Document {
			return findings[i].Document < findings[j].Document
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Target < findings[j].Target
	})
	report.Findings = findings
	if bytesPinned {
		report.Truncated = true
	}

	switch {
	case len(documents) == 0:
		report.Evidence = UnknownEvidence("no documentation file was inventoried")
		report.Reason = "no documentation to compare"
	case len(refs) == 0:
		report.Evidence = UnknownEvidence("documentation contained no path-shaped claim that could be checked")
		report.Reason = "no comparable documentation claim"
	case unverified > 0 || bytesPinned || report.Truncated:
		report.Evidence = ObservedEvidence(KindDocumentClaim, refs...)
		report.Evidence.State = StatePartiallyObserved
		report.Evidence.Confidence = 0.6
		report.Evidence.Reason = driftLimitation(report, unverified, bytesPinned, inventoryBounded)
	default:
		report.Evidence = ObservedEvidence(KindDocumentClaim, refs...)
	}
	if report.Truncated && report.Reason == "" {
		report.Reason = driftLimitation(report, unverified, bytesPinned, inventoryBounded)
	}
	s.drift = report
	return nil
}

// driftClaim is one path-shaped phrase and the line it appeared on.
type driftClaim struct {
	target string
	line   int
}

// driftCommonRoots are conventional repository directories. A code span whose
// first segment is one of these is a repository claim even when the directory
// is empty or missing (that is exactly the drift being reported). The list is a
// heuristic, not a rule engine: an unknown first segment stays outside the
// repository unless it is explicitly relative or a single file-like name.
var driftCommonRoots = map[string]bool{
	"api": true, "app": true, "apps": true, "assets": true, "backend": true,
	"bench": true, "build": true, "cmd": true, "components": true,
	"config": true, "configs": true, "content": true, "db": true,
	"deploy": true, "doc": true, "docs": true, "examples": true,
	"frontend": true, "hooks": true, "infra": true, "internal": true,
	"k8s": true, "lib": true, "libs": true, "migrations": true,
	"mobile": true, "packages": true, "pkg": true, "public": true,
	"scripts": true, "server": true, "services": true, "src": true,
	"static": true, "terraform": true, "test": true, "tests": true,
	"tools": true, "web": true,
}

// driftPrefixes are the directory names that make a code span a repository
// path rather than a package or library name. root contains top-level
// directories; module contains top-level directories under the owning module
// (which is often a subdirectory such as backend/).
type driftPrefixes struct {
	root      map[string]bool
	module    map[string]bool
	moduleRel string
}

// driftClaims extracts local path claims from markdown. Fenced code blocks are
// skipped: an example is not a claim that the path exists.
//
// Markdown links are resolved relative to the document. Inline code spans are
// ambiguous in practice, so a span that starts with a known repository or
// module directory is resolved from that base first. This keeps a package
// name such as `adapter/` or a library import such as `html/template` from
// becoming a false missing-path finding.
func driftClaims(document, text string, prefixes driftPrefixes, files, dirs map[string]bool) []driftClaim {
	var out []driftClaim
	inFence := false
	for index, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		seen := map[string]bool{}
		add := func(raw string, isLink bool) {
			candidates := driftCandidates(document, raw, isLink, prefixes, files, dirs)
			if len(candidates) == 0 {
				return
			}
			target := candidates[0]
			for _, candidate := range candidates {
				if files[candidate] || dirs[candidate] {
					target = candidate
					break
				}
			}
			if seen[target] {
				return
			}
			seen[target] = true
			out = append(out, driftClaim{target: target, line: index + 1})
		}
		for _, match := range driftLinkPattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 {
				add(match[1], true)
			}
		}
		for _, match := range driftCodePattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 {
				add(match[1], false)
			}
		}
	}
	return out
}

// driftCandidates returns the repository-relative paths a claim could name, in
// resolution order. The caller picks the first that exists.
func driftCandidates(document, raw string, isLink bool, prefixes driftPrefixes, files, dirs map[string]bool) []string {
	if !looksLikeDriftTarget(raw, isLink, prefixes) {
		return nil
	}
	clean := cutDriftQuery(strings.TrimSpace(raw))
	if unescaped, err := url.PathUnescape(clean); err == nil {
		clean = unescaped
	}
	absolute := strings.HasPrefix(clean, "/")
	if absolute {
		if target, ok := driftTargetFrom(".", strings.TrimPrefix(clean, "/")); ok {
			return []string{target}
		}
		return nil
	}
	first := strings.Split(clean, "/")[0]
	var candidates []string
	add := func(base string) {
		if target, ok := driftTargetFrom(base, clean); ok {
			for _, existing := range candidates {
				if existing == target {
					return
				}
			}
			candidates = append(candidates, target)
		}
	}
	if isLink {
		add(path.Dir(document))
		if prefixes.root[first] {
			add(".")
		}
		if prefixes.module[first] {
			add(prefixes.moduleRel)
		}
	} else {
		if prefixes.root[first] {
			add(".")
		}
		if prefixes.module[first] {
			add(prefixes.moduleRel)
		}
		add(path.Dir(document))
	}
	if !isLink && !prefixes.root[first] && !prefixes.module[first] {
		// A code span with an unknown first segment is usually a package,
		// library, or machine-local path (`html/template`, `home/hypr/x.conf`).
		// Only an explicit relative spelling, a single file-like name, or an
		// actually present target counts as a repository claim; otherwise the
		// document is describing something outside this tree.
		explicitRelative := strings.HasPrefix(clean, "./") || strings.HasPrefix(clean, "../")
		fileLike := !strings.Contains(clean, "/") && driftSourceExtension(path.Ext(clean))
		conventional := driftCommonRoots[strings.ToLower(first)]
		if !explicitRelative && !fileLike && !conventional {
			for _, candidate := range candidates {
				if files[candidate] || dirs[candidate] {
					return []string{candidate}
				}
			}
			return nil
		}
	}
	return candidates
}

// driftTargetFrom joins a base directory and a claim, rejecting an escape from
// the repository.
func driftTargetFrom(base, target string) (string, bool) {
	if base == "" {
		base = "."
	}
	rel := path.Clean(path.Join(base, target))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// looksLikeDriftTarget rejects prose, URLs, commands, globs, route paths, and
// external import paths. The remaining shapes are relative repository paths.
func looksLikeDriftTarget(raw string, isLink bool, prefixes driftPrefixes) bool {
	target := strings.TrimSpace(raw)
	if target == "" || target != raw {
		return false
	}
	// A home-relative path names the reader's machine, not the repository.
	// `~/.tmux.conf` in a tutorial is configuration guidance, not a claim that
	// the file lives in this tree.
	if target == "~" || strings.HasPrefix(target, "~/") || strings.HasPrefix(target, `~\`) {
		return false
	}
	if strings.ContainsAny(target, " \t\r\n{}<>$*|\\\"'`") {
		return false
	}
	if strings.HasPrefix(target, "#") || strings.HasPrefix(target, "//") {
		return false
	}
	lower := strings.ToLower(target)
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "tel:") || strings.HasPrefix(lower, "javascript:") {
		return false
	}
	target = cutDriftQuery(target)
	if target == "" || target == "." || target == "/" {
		return false
	}
	absolute := strings.HasPrefix(target, "/")
	first := strings.Split(strings.TrimPrefix(target, "/"), "/")[0]
	if strings.Contains(first, ".") && first != "." && first != ".." {
		// A domain-shaped first segment is an external module or site, not a
		// repository path.
		return false
	}
	if !isLink && !driftSourceExtension(path.Ext(target)) && strings.Contains(path.Base(target), ".") {
		// `domain.Document` is a Go qualified name, not a file. A real file
		// with a dot has a known source or documentation extension.
		return false
	}
	if absolute && !driftSourceExtension(path.Ext(target)) && !prefixes.root[first] {
		// A leading slash in prose is usually an HTTP route (/products), not a
		// repository path. Only a file-like target or a known top-level
		// directory is treated as a path claim.
		return false
	}
	if !isLink && !driftSourceExtension(path.Ext(target)) && !strings.Contains(target, "/") {
		return false
	}
	if !isLink && !driftSourceExtension(path.Ext(target)) {
		segments := strings.Split(strings.Trim(target, "/"), "/")
		if len(segments) == 1 && !prefixes.root[segments[0]] && !prefixes.module[segments[0]] {
			// A bare package name (`adapter/`, `domain/`) is not a repository
			// path claim.
			return false
		}
	}
	if strings.HasSuffix(target, "/") {
		return true
	}
	if driftSourceExtension(path.Ext(target)) {
		return true
	}
	return strings.Contains(target, "/")
}

func cutDriftQuery(target string) string {
	if index := strings.IndexAny(target, "#?"); index >= 0 {
		return target[:index]
	}
	return target
}

// driftDocuments selects documentation that is current guidance. Changelogs,
// licenses, and archived documents are excluded: their paths are historical by
// design, so treating them as drift would manufacture findings.
func driftDocuments(inventory ast.Inventory) ([]string, bool) {
	var documents []string
	for _, file := range inventory.Files {
		if file.Kind != ast.FileDocumentation {
			continue
		}
		if driftDocumentExcluded(file.Path) {
			continue
		}
		documents = append(documents, file.Path)
	}
	sort.SliceStable(documents, func(i, j int) bool {
		di, dj := strings.Count(documents[i], "/"), strings.Count(documents[j], "/")
		if di != dj {
			return di < dj
		}
		return documents[i] < documents[j]
	})
	truncated := len(documents) > DriftMaxDocuments
	if truncated {
		documents = documents[:DriftMaxDocuments]
	}
	return documents, truncated
}

func driftDocumentExcluded(rel string) bool {
	lower := strings.ToLower(rel)
	base := path.Base(lower)
	switch base {
	case "changelog.md", "license", "license.md", "code_of_conduct.md":
		return true
	}
	for _, prefix := range []string{"old_docs/", "vendor/", "node_modules/"} {
		if strings.HasPrefix(lower, prefix) || strings.Contains(lower, "/"+prefix) {
			return true
		}
	}
	return false
}

// driftInventorySets builds the membership sets and path prefixes the
// comparison needs. They are projections of one inventory, not a second tree
// walk.
func driftInventorySets(inventory ast.Inventory) (files, dirs map[string]bool, prefixes driftPrefixes) {
	files = make(map[string]bool, len(inventory.Files))
	dirs = map[string]bool{".": true}
	prefixes.root = map[string]bool{}
	prefixes.module = map[string]bool{}
	root := inventory.Snapshot.Root
	if root != "" && inventory.Snapshot.ModuleRoot != "" {
		// The index walk root is symlink-resolved while Snapshot.Root keeps
		// the workspace spelling. Compare the resolved forms so a nested
		// module is not mistaken for an outside path.
		resolvedRoot := root
		if value, err := filepath.EvalSymlinks(root); err == nil {
			resolvedRoot = value
		}
		resolvedModule := inventory.Snapshot.ModuleRoot
		if value, err := filepath.EvalSymlinks(inventory.Snapshot.ModuleRoot); err == nil {
			resolvedModule = value
		}
		if rel, err := filepath.Rel(resolvedRoot, resolvedModule); err == nil {
			rel = filepath.ToSlash(rel)
			if rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
				prefixes.moduleRel = rel
			}
		}
	}
	for _, file := range inventory.Files {
		rel := path.Clean(strings.TrimPrefix(file.Path, "./"))
		files[rel] = true
		prefixes.root[strings.Split(rel, "/")[0]] = true
		if prefixes.moduleRel != "" && strings.HasPrefix(rel, prefixes.moduleRel+"/") {
			rest := strings.TrimPrefix(rel, prefixes.moduleRel+"/")
			prefixes.module[strings.Split(rest, "/")[0]] = true
		}
		for dir := path.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	return files, dirs, prefixes
}

func readDriftDocument(abs string) ([]byte, bool, error) {
	file, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, DriftMaxDocumentBytes+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(data) > DriftMaxDocumentBytes
	if truncated {
		data = data[:DriftMaxDocumentBytes]
	}
	return data, truncated, nil
}

func driftExcluded(target string, inventory ast.Inventory) bool {
	for _, root := range inventory.Snapshot.ExcludedRoots {
		if root.Path == "" {
			continue
		}
		if target == root.Path || strings.HasPrefix(target, root.Path+"/") {
			return true
		}
	}
	return false
}

func driftUnverifiedReason(inventory ast.Inventory) string {
	switch {
	case inventory.FilesTruncated:
		return "the inventory file list was truncated; absence is not established"
	case !inventory.Snapshot.ScopeComplete:
		return "repository scope is incomplete; absence is not established"
	default:
		return "the target lies outside the inspected scope"
	}
}

func driftLimitation(report *DriftReport, unverified int, bytesPinned, inventoryBounded bool) string {
	var parts []string
	if inventoryBounded {
		parts = append(parts, "file inventory was truncated; documentation discovery may be incomplete")
	}
	if bytesPinned {
		parts = append(parts, "documentation read was bounded")
	}
	if report.Truncated {
		parts = append(parts, "result was bounded")
	}
	if unverified > 0 {
		parts = append(parts, fmt.Sprintf("%d documented paths could not be checked against a complete inventory", unverified))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func driftSeverity(target string) DriftSeverity {
	if driftSourceExtension(path.Ext(target)) {
		return DriftHigh
	}
	switch {
	case strings.HasPrefix(target, "internal/"), strings.HasPrefix(target, "cmd/"),
		strings.HasPrefix(target, "api/"), strings.HasPrefix(target, "pkg/"),
		strings.HasPrefix(target, "src/"):
		return DriftHigh
	}
	switch strings.ToLower(path.Ext(target)) {
	case ".md", ".markdown", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".txt", ".html":
		return DriftMedium
	default:
		return DriftLow
	}
}

func driftSeverityRank(severity DriftSeverity) int {
	switch severity {
	case DriftHigh:
		return 0
	case DriftMedium:
		return 1
	default:
		return 2
	}
}

func driftSourceExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".rs", ".java",
		".kt", ".kts", ".cs", ".py", ".rb", ".php", ".swift", ".c", ".h",
		".cc", ".cpp", ".hpp", ".sql", ".yaml", ".yml", ".json", ".toml",
		".proto", ".graphql", ".gql", ".sh", ".bash", ".tf", ".xml", ".ini",
		".env", ".conf", ".csproj", ".sln", ".gradle", ".mod":
		return true
	default:
		return false
	}
}
