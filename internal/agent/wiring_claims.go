package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A manifest declares a dependency. It never says when that dependency runs,
// where it is initialized, or which part of the product it implements.
//
// "Search: FlexSearch with a build-time prebuilt index" promoted a
// package.json entry into a wiring claim: the run never opened lib/search.ts,
// and the real index is constructed in the browser. The existing dependency
// ladder owns the strength vocabulary ("uses", "initializes", "caches"), but a
// mechanism phrase attached to a stack-table row carries no verb at all, so
// neither the ladder nor the citation audit sees it.
//
// This audit reads the dependency names from the manifests the run actually
// observed — never from a hardcoded ecosystem list — and requires source
// evidence of that dependency before the answer may assert how it is wired.

// wiringMechanismPattern names a claim about when or where a dependency runs.
// A stack table that only names a version is not a wiring claim.
var wiringMechanismPattern = regexp.MustCompile(`(?i)\b(?:` +
	`build[\s-]?time|at\s+build|prebuilt|pre-?computed|` +
	`client[\s-]side|server[\s-]side|in\s+the\s+browser|` +
	`at\s+runtime|by\s+default|initiali[sz]ed|wired|mounted|` +
	`lazy[\s-]?load(?:ed|ing)?|bundled|cached` +
	`)\b`)

// wiringClaimIssue checks a mechanism phrase attached to a declared
// dependency.
func wiringClaimIssue(sentence string, turns []Turn) *claimIssue {
	deps := manifestDependencyNames(turns)
	if len(deps) == 0 {
		return nil
	}
	for _, mention := range dependencyMentions(sentence, deps) {
		clause := dependencyClause(sentence, mention.start, mention.end)
		if !wiringMechanismPattern.MatchString(clause) {
			continue
		}
		observed, _ := dependencyObservedLevel(turns, "", dependencySpec{
			name: mention.name, aliases: []string{mention.name},
		})
		if observed >= dependencyImported {
			continue
		}
		mechanism := wiringMechanismPattern.FindString(clause)
		return &claimIssue{
			Code: "unsupported_wiring_claim",
			Detail: fmt.Sprintf(
				"the answer says %s is %s, but the run observed only its declaration in a manifest; a dependency name does not show when or where it runs. Read the source that imports it or state the dependency without the mechanism",
				mention.name, strings.ToLower(mechanism)),
		}
	}
	return nil
}

// dependencyMention is one dependency name found in a sentence.
type dependencyMention struct {
	name       string
	start, end int
}

// dependencyMentions returns the dependency names a sentence names, longest
// name first so "next-themes" is not read as "next".
func dependencyMentions(sentence string, deps []string) []dependencyMention {
	lower := strings.ToLower(sentence)
	claimed := make([]bool, len(lower))
	var out []dependencyMention
	for _, dep := range deps {
		for offset := 0; offset < len(lower); {
			index := strings.Index(lower[offset:], dep)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(dep)
			offset = start + 1
			if !dependencyBoundary(lower, start, true) || !dependencyBoundary(lower, end, false) {
				continue
			}
			free := true
			for i := start; i < end; i++ {
				if claimed[i] {
					free = false
					break
				}
			}
			if !free {
				continue
			}
			for i := start; i < end; i++ {
				claimed[i] = true
			}
			out = append(out, dependencyMention{name: dep, start: start, end: end})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// dependencyBoundary reports whether the position sits on a name boundary. Dash,
// dot, and underscore are name characters: "next" is not a mention inside
// "next-themes" or "Next.js", and "react" is not a mention inside "react-dom".
func dependencyBoundary(text string, position int, start bool) bool {
	if start {
		return position == 0 || !dependencyNameByte(text, position-1)
	}
	return position >= len(text) || !dependencyNameByte(text, position)
}

// dependencyNameByte reports whether the byte at i continues a dependency name.
// A dot continues one only when a name follows it ("next.js"), so the sentence
// period in "next." ends the name while the dot in "Next.js" does not.
func dependencyNameByte(text string, i int) bool {
	b := text[i]
	switch {
	case b >= 'a' && b <= 'z', b >= '0' && b <= '9', b == '-', b == '_':
		return true
	case b == '.':
		if i+1 >= len(text) {
			return false
		}
		next := text[i+1]
		return next >= 'a' && next <= 'z' || next >= '0' && next <= '9'
	}
	return false
}

// dependencyClause returns the comma- or semicolon-bounded fragment around a
// mention. A version or an unrelated property in the next comma-separated item
// is not part of this claim.
func dependencyClause(sentence string, start, end int) string {
	from := start
	for from > 0 && !strings.ContainsRune(",;\n", rune(sentence[from-1])) {
		from--
	}
	to := end
	for to < len(sentence) && !strings.ContainsRune(",;\n", rune(sentence[to])) {
		to++
	}
	return sentence[from:to]
}

// manifestDependencyNames returns the dependency identifiers named by the
// manifests the run observed. The list is derived from evidence, not from a
// table of known ecosystems, so a project using an unfamiliar library is
// covered by the same rule.
func manifestDependencyNames(turns []Turn) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if len(name) < 4 || seen[name] || len(out) >= 256 {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if ClassifyPath(outcomePath(outcome)) != PathManifest {
				continue
			}
			text := outcome.Result.Text
			switch strings.ToLower(filepath.Base(outcomePath(outcome))) {
			case "package.json":
				for _, name := range JSONDependencyNames(text) {
					add(name)
				}
			case "go.mod":
				for _, name := range GoModDependencyNames(text) {
					add(name)
				}
			case "requirements.txt", "pyproject.toml", "cargo.toml", "gemfile", "pom.xml":
				// These formats name dependencies in prose-shaped lines; the
				// manifest text itself is already declared evidence, and the
				// mechanism audit stays conservative rather than guessing at a
				// format it does not parse.
			}
		}
	}
	// Longest name first, so a scoped or hyphenated package wins over its
	// prefix.
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// JSONDependencyNames extracts dependency keys from a package.json-shaped
// document. Every dependency section is included: a dev dependency is still
// declared by the manifest.
func JSONDependencyNames(text string) []string {
	var pkg struct {
		Dependencies         map[string]json.RawMessage `json:"dependencies"`
		DevDependencies      map[string]json.RawMessage `json:"devDependencies"`
		PeerDependencies     map[string]json.RawMessage `json:"peerDependencies"`
		OptionalDependencies map[string]json.RawMessage `json:"optionalDependencies"`
	}
	if err := json.Unmarshal([]byte(text), &pkg); err != nil {
		return nil
	}
	out := make([]string, 0, len(pkg.Dependencies)+len(pkg.DevDependencies))
	for _, section := range []map[string]json.RawMessage{
		pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies, pkg.OptionalDependencies,
	} {
		for name := range section {
			out = append(out, name)
		}
	}
	return out
}

// GoModDependencyNames extracts module names from require directives.
func GoModDependencyNames(text string) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		case !inBlock:
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		module := fields[0]
		if slash := strings.LastIndex(module, "/"); slash >= 0 {
			module = module[slash+1:]
		}
		out = append(out, module)
	}
	return out
}
