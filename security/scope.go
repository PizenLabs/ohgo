package security

import (
	"os"
	"path/filepath"
	"strings"
)

// NormalizeScope resolves a model-supplied target against the workspace root
// and classifies it as inside or outside.
//
// The result is canonical: a relative path, an absolute path, "." and ".." all
// resolve to one absolute form, and symlinks are resolved through the deepest
// existing ancestor so a link inside the workspace cannot name a file outside
// it. Policy evaluates the result, never the raw string, which is why a grant
// for one spelling covers every equivalent spelling and none of its escapes.
//
// An empty raw value is Unspecified (an in-memory tool with no filesystem
// target), which policy treats as workspace-scoped. A root that cannot be
// resolved classifies the target as outside: an unverifiable scope fails
// closed.
func NormalizeScope(root, raw string) Scope {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Scope{Class: ScopeUnspecified, Raw: ""}
	}
	if root == "" {
		return Scope{Class: ScopeOutside, Raw: raw, Path: filepath.Clean(raw)}
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Scope{Class: ScopeOutside, Raw: raw, Path: filepath.Clean(raw)}
	}

	target := raw
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)

	final := resolveDeepest(target)
	if !within(resolvedRoot, final) {
		return Scope{Class: ScopeOutside, Raw: raw, Path: final}
	}
	rel, err := filepath.Rel(resolvedRoot, final)
	if err != nil {
		return Scope{Class: ScopeOutside, Raw: raw, Path: final}
	}
	// Keep the workspace's own spelling for the canonical path, so a caller
	// that compares it to the root it passed sees an in-root result on
	// platforms where /var is a symlink to /private/var.
	return Scope{Class: ScopeInside, Raw: raw, Path: filepath.Join(root, rel)}
}

// ExternalScope classifies a non-filesystem target (a URL host).
//
// The host, not the full URL, is the canonical path: two URLs to the same host
// share a scope, while a different host does not.
func ExternalScope(raw, host string) Scope {
	return Scope{Class: ScopeExternal, Raw: strings.TrimSpace(raw), Path: strings.ToLower(strings.TrimSpace(host))}
}

// resolveDeepest resolves symlinks through the deepest existing ancestor of
// target and rejoins the segments that do not exist yet, so a write to a new
// file is still classified by where it would land.
func resolveDeepest(target string) string {
	var pending string
	current := target
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if pending == "" {
				return resolved
			}
			return filepath.Join(resolved, pending)
		}
		if !os.IsNotExist(err) {
			// A permission error or similar: keep the lexical form rather than
			// inventing a resolution we could not verify.
			return target
		}
		parent := filepath.Dir(current)
		if parent == current {
			return target
		}
		if pending == "" {
			pending = filepath.Base(current)
		} else {
			pending = filepath.Join(filepath.Base(current), pending)
		}
		current = parent
	}
}

// within reports whether an absolute path is inside root.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ClassifyRisk computes the deterministic risk of a normalized request.
//
// The inputs are all trusted runtime facts: the capability, the normalized
// scope, whether the operation mutates, whether a process command is
// destructive, and whether the authority permits the operation at all. The
// same inputs always produce the same risk.
func ClassifyRisk(cap Capability, scope Scope, mutates, destructive bool) Risk {
	if !cap.Known() {
		return RiskHigh
	}
	if cap.Filesystem() && scope.Class == ScopeOutside {
		return RiskHigh
	}
	switch cap {
	case CapabilityRead:
		return RiskLow
	case CapabilityWrite:
		return RiskMedium
	case CapabilityExec:
		if destructive {
			return RiskHigh
		}
		return RiskMedium
	case CapabilityNetwork:
		return RiskMedium
	}
	return RiskHigh
}
