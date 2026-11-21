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
	files, dirs := driftInventorySets(s.inventory)
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
		for _, claim := range driftClaims(document, string(data)) {
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

// driftClaims extracts local path claims from markdown. Fenced code blocks are
// skipped: an example is not a claim that the path exists.
func driftClaims(document, text string) []driftClaim {
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
		add := func(raw string) {
			if !looksLikeDriftTarget(raw) {
				return
			}
			target, ok := resolveDriftTarget(document, raw)
			if !ok || seen[target] {
				return
			}
			seen[target] = true
			out = append(out, driftClaim{target: target, line: index + 1})
		}
		for _, match := range driftLinkPattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 {
				add(match[1])
			}
		}
		for _, match := range driftCodePattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 {
				add(match[1])
			}
		}
	}
	return out
}

// looksLikeDriftTarget rejects prose, URLs, commands, globs, and external
// import paths. The remaining shapes are relative repository paths.
func looksLikeDriftTarget(raw string) bool {
	target := strings.TrimSpace(raw)
	if target == "" || target != raw {
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
	first := strings.Split(strings.TrimPrefix(target, "/"), "/")[0]
	if strings.Contains(first, ".") && first != "." && first != ".." {
		// A domain-shaped first segment is an external module or site, not a
		// repository path.
		return false
	}
	if strings.HasSuffix(target, "/") {
		return true
	}
	if driftSourceExtension(path.Ext(target)) {
		return true
	}
	return strings.Contains(target, "/")
}

// resolveDriftTarget maps a claim onto the repository-relative path it names.
// A leading slash is treated as repository-rooted; every other target is
// relative to the document's directory. An escaping target is ignored.
func resolveDriftTarget(document, raw string) (string, bool) {
	target := cutDriftQuery(raw)
	unescaped, err := url.PathUnescape(target)
	if err == nil {
		target = unescaped
	}
	var rel string
	if strings.HasPrefix(target, "/") {
		rel = strings.TrimPrefix(target, "/")
	} else {
		rel = path.Join(path.Dir(document), target)
	}
	rel = path.Clean(strings.TrimPrefix(rel, "./"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
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

// driftInventorySets builds the two membership sets the comparison needs. They
// are projections of one inventory, not a second tree walk.
func driftInventorySets(inventory ast.Inventory) (files, dirs map[string]bool) {
	files = make(map[string]bool, len(inventory.Files))
	dirs = map[string]bool{".": true}
	for _, file := range inventory.Files {
		rel := path.Clean(strings.TrimPrefix(file.Path, "./"))
		files[rel] = true
		for dir := path.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	return files, dirs
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
