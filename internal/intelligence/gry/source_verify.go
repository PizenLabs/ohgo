package gry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
)

// Source verification closes the freshness gap in trace and search payloads.
//
// GRY's trace/search JSON does not carry a generation today, so the adapter
// cannot treat an index timestamp as proof of currency. It can do the next best
// thing: read the current workspace and confirm that the symbol a row names
// still exists at the file and line range the index reports. A moved or deleted
// symbol is STALE; an unreadable or out-of-root source is UNAVAILABLE; a row
// with no source coordinate stays UNKNOWN.
//
// This verifies endpoints, not every edge. A verified endpoint is never
// upgraded to a proof of the relationship; when GRY supplies its own
// generation/freshness, that is preserved separately in the record snapshot.
const (
	// maxVerifiedRows bounds filesystem reads per observation. A large trace
	// is still useful as a candidate set; verifying every row would turn a
	// structural query into a repository scan.
	maxVerifiedRows = 24
	// maxVerifiedSourceBytes bounds one source read.
	maxVerifiedSourceBytes = 4 << 20
)

// sourceLocation is one file/line/symbol coordinate to verify.
type sourceLocation struct {
	file   string
	lines  []int
	symbol string
}

// verifySourceLocations returns the combined freshness of the coordinates and
// the reasons that explain a downgrade.
func verifySourceLocations(root string, locations []sourceLocation) (evidence.Freshness, []string) {
	if strings.TrimSpace(root) == "" {
		return evidence.FreshnessUnknown, nil
	}
	// An empty workspace cannot distinguish a moved file from a payload that
	// names a source the caller never expected to exist. Without any source at
	// all, freshness is unknown rather than a stale claim.
	if entries, err := os.ReadDir(root); err != nil || len(entries) == 0 {
		return evidence.FreshnessUnknown, nil
	}
	verified := 0
	unavailable := false
	var stale []string
	for _, location := range locations {
		if verified >= maxVerifiedRows {
			break
		}
		if strings.TrimSpace(location.file) == "" || strings.TrimSpace(location.symbol) == "" {
			continue
		}
		path, ok := workspacePath(root, location.file)
		if !ok {
			unavailable = true
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				stale = append(stale, location.file+" no longer exists")
			} else {
				unavailable = true
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > maxVerifiedSourceBytes {
			unavailable = true
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			unavailable = true
			continue
		}
		name := symbolName(location.symbol)
		if name == "" {
			continue
		}
		text := string(data)
		if !strings.Contains(lineWindow(text, location.lines), name) &&
			!strings.Contains(text, name) {
			stale = append(stale, location.file+" no longer contains "+name)
			continue
		}
		verified++
	}
	if len(stale) > 0 {
		if len(stale) > 3 {
			stale = stale[:3]
		}
		return evidence.FreshnessStale, stale
	}
	if unavailable {
		return evidence.FreshnessUnavailable, []string{"the current source needed to verify the external evidence is unavailable"}
	}
	if verified == 0 {
		return evidence.FreshnessUnknown, nil
	}
	return evidence.FreshnessCurrent, nil
}

// workspacePath resolves a provider path inside root. An absolute path is
// accepted only when it is already inside the root; a relative path is joined
// and cleaned. A path that escapes the workspace is not read.
func workspacePath(root, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(absoluteRoot); err == nil {
		absoluteRoot = resolved
	}
	candidate := value
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absoluteRoot, filepath.FromSlash(value))
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	// A relative path inside the root can still be a symlink to an outside
	// file. Resolve it before the containment check; evidence must not read
	// outside the workspace through a link.
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	relative, err := filepath.Rel(absoluteRoot, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
}

// lineWindow returns the requested 1-based line range, or the whole text when
// the range is absent or outside the file.
func lineWindow(text string, lines []int) string {
	if len(lines) < 2 || lines[0] <= 0 || lines[1] < lines[0] {
		return text
	}
	rows := strings.Split(text, "\n")
	start, end := lines[0]-1, lines[1]-1
	if start >= len(rows) {
		return text
	}
	if end >= len(rows) {
		end = len(rows) - 1
	}
	return strings.Join(rows[start:end+1], "\n")
}

// symbolName reduces a qualified name to the member a source line contains.
//
// GRY reports package-qualified names and receiver-qualified methods. The
// declaration line contains the final identifier, not the package prefix, so
// comparing the full qualified string would mark every valid row stale.
func symbolName(qualified string) string {
	qualified = strings.TrimSpace(qualified)
	if qualified == "" {
		return ""
	}
	// Drop a receiver form: pkg.(*Type).Method -> pkg.Type.Method.
	qualified = strings.NewReplacer("(*", "", ")", "", "(", "").Replace(qualified)
	if index := strings.LastIndexAny(qualified, "./\\"); index >= 0 && index+1 < len(qualified) {
		qualified = qualified[index+1:]
	}
	return strings.TrimSpace(qualified)
}
