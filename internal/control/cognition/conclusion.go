package cognition

import (
	"path"
	"regexp"
	"strings"
)

var (
	conclusionPathPattern   = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.go\b`)
	conclusionSymbolPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+`)
)

// unresolvedConclusionTargets extracts the concrete code coordinates a
// conclusion says remain unresolved. It is intentionally conservative: prose
// such as "not enough evidence" creates no target, while a named symbol or Go
// path creates the next evidence boundary.
//
// The extraction is not a semantic proof. It makes the model's own declaration
// actionable: if it says StockReserver.Reserve is unknown, the runtime asks for
// an observation of that coordinate before accepting the conclusion as final.
func unresolvedConclusionTargets(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		target := strings.Trim(raw, "`'\"()[]{}.,:;")
		if target == "" || len(out) >= 4 || seen[target] {
			return
		}
		seen[target] = true
		out = append(out, target)
	}
	for _, match := range conclusionPathPattern.FindAllString(text, -1) {
		// Canonicalise the path once, here, so the obligation and the evidence
		// ledger use the same coordinate. "./backend/x.go" and "backend/x.go"
		// are one file, not two unsatisfied obligations.
		if cleaned := cleanPath(match); cleaned != "" {
			add(cleaned)
		}
	}
	for _, match := range conclusionSymbolPattern.FindAllString(text, -1) {
		parts := strings.Split(match, ".")
		if len(parts) < 2 || len(match) < 5 {
			continue
		}
		last := parts[len(parts)-1]
		if last == "" || last[0] < 'A' || last[0] > 'Z' {
			continue
		}
		add(match)
	}
	return out
}

// conclusionTargetMatches reports whether one observation mentions a named
// target. A dotted symbol may be observed through its bare method name when
// the implementation file does not repeat the type qualifier; that is still
// the targeted observation the conclusion declared missing.
//
// The bare-name fallback matches on identifier boundaries, never as a raw
// substring: "Run" must not be satisfied by "runtime" or "CheckoutRunner".
// Boundary matching makes a length gate unnecessary, and the old len>=4 gate
// made short-method obligations (StockReserver.Run) unsatisfiable, forcing
// the runtime to demand the same inspection on every turn.
func conclusionTargetMatches(haystack, target string) bool {
	if target == "" {
		return false
	}
	if strings.Contains(haystack, target) {
		return true
	}
	if strings.HasSuffix(target, ".go") {
		if base := path.Base(target); base != "" && base != target {
			return strings.Contains(haystack, base)
		}
		return false
	}
	parts := strings.Split(target, ".")
	if len(parts) < 2 {
		return false
	}
	return identifierContains(haystack, parts[len(parts)-1])
}

// identifierContains reports whether name occurs in haystack as one complete
// code identifier, delimited by non-identifier bytes or text edges.
func identifierContains(haystack, name string) bool {
	if name == "" {
		return false
	}
	for from := 0; from+len(name) <= len(haystack); {
		at := strings.Index(haystack[from:], name)
		if at < 0 {
			return false
		}
		at += from
		if (at == 0 || !isIdentifierByte(haystack[at-1])) &&
			(at+len(name) == len(haystack) || !isIdentifierByte(haystack[at+len(name)])) {
			return true
		}
		from = at + 1
	}
	return false
}

// isIdentifierByte reports whether b can occur inside a code identifier.
func isIdentifierByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// mergeConclusionTargets preserves an existing provisional obligation while
// adding targets the new conclusion names. It keeps the first occurrence and
// bounds the frontier so a conclusion cannot grow into a repository survey.
func mergeConclusionTargets(existing, current []string) []string {
	out := make([]string, 0, len(existing)+len(current))
	seen := map[string]bool{}
	for _, target := range append(append([]string(nil), existing...), current...) {
		target = strings.TrimSpace(target)
		if target == "" || seen[target] || len(out) >= 4 {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return out
}
