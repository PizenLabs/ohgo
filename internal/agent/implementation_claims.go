package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Implementation status claims are the positive complement of the absence
// gate. A README can describe a service as "fully implemented", a Compose file
// can declare it, and an inventory can prove its directory exists; none of
// those observations shows what the implementation does. A status claim is
// therefore supportable only by source content for the component it names.
//
// A second distinction matters just as much: one source file is an observation
// of one file, not proof that a whole component is complete. Strong
// completeness vocabulary ("fully implemented", "feature-complete",
// "production-ready") additionally requires an observed component source
// scope, and when that scope is unavailable the answer must narrow the claim
// to the files inspected.
//
// The check is deterministic and narrow. It recognizes explicit status
// vocabulary, extracts the components the claim names, and decides whether the
// evidence class the claim requires was observed. It never judges whether the
// implementation is actually complete.

var (
	// implementationStatusPattern matches a declarative completion, stub, mock,
	// or scaffold claim. Bare "implemented" and "stubs" are deliberately
	// absent: headings such as "implemented vs. stubs" are not claims.
	implementationStatusPattern = regexp.MustCompile(`(?i)\b(?:` +
		`fully[\s-]+implemented|` +
		`completely[\s-]+implemented|` +
		`partially[\s-]+implemented|` +
		`fully[\s-]+functional|` +
		`feature[\s-]+complete|` +
		`production[\s-]+ready|` +
		`stubs?\s*\(|` +
		`(?:is|are)\s+(?:a\s+)?(?:[a-z0-9][a-z0-9-]*\s+){0,2}stubs?\b|` +
		`health[\s-]+check[\s-]+only\b|` +
		`mock(?:ed)?\s+(?:data|auth(?:entication)?|backend|api)\b|` +
		`scaffold(?:ed)?\b|` +
		`placeholder\b|` +
		`\b(?:is|are)\s+mocked\b|` +
		`\bimplemented\s+(?:service|backend|frontend|api|application)\b|` +
		`\b(?:service|backend|frontend|api)\s+(?:is|are)\s+implemented\b` +
		`)`)
)

// implementationSubject is one component a status claim names.
type implementationSubject struct {
	// Label is the canonical component identity the source observation must
	// cover.
	Label string
	// Name is the readable component name for the review reason.
	Name string
}

// implementationClaimIssue checks a status claim against source observations.
// A manifest, Compose file, README, directory listing, or structural inventory
// does not establish implementation state.
func implementationClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := implementationStatusPattern.FindStringIndex(sentence)
	if match == nil {
		return nil
	}
	claim := boundedClaim(sentence[match[0]:match[1]])
	subjects := implementationClaimSubjects(sentence, match)
	if len(subjects) == 0 {
		if !sourceObservationExists(turns) {
			return &claimIssue{
				Code: "unsupported_implementation_claim",
				Detail: fmt.Sprintf(
					"the answer states %q as implementation state, but the run observed only documentation, configuration, manifest, or inventory; read source before claiming implementation or mark it unverified",
					claim),
			}
		}
		// A strong claim with no component cannot be bounded to an observed
		// scope, so it is not automatically supported by an arbitrary source
		// read.
		if strongImplementationStatus(match, sentence) {
			return &claimIssue{
				Code: "unsupported_completeness_claim",
				Detail: fmt.Sprintf(
					"the answer states %q as complete implementation without naming the component whose full scope was observed; narrow the claim to the inspected source or state the component explicitly",
					claim),
			}
		}
		return nil
	}
	var missing []string
	for _, subject := range subjects {
		if !implementationSubjectObserved(turns, subject) {
			missing = append(missing, implementationSubjectName(subject))
		}
	}
	if len(missing) > 0 {
		return &claimIssue{
			Code: "unsupported_implementation_claim",
			Detail: fmt.Sprintf(
				"the answer states %q as implementation state, but no source observation covers %s; read that component or state the narrower claim",
				claim, strings.Join(missing, ", ")),
		}
	}
	if !strongImplementationStatus(match, sentence) {
		return nil
	}
	var unscoped []string
	for _, subject := range subjects {
		if !componentSourceScopeObserved(turns, subject) {
			unscoped = append(unscoped, implementationSubjectName(subject))
		}
	}
	if len(unscoped) > 0 {
		return &claimIssue{
			Code: "unsupported_completeness_claim",
			Detail: fmt.Sprintf(
				"the answer states %q as complete implementation, but only isolated source content was observed for %s; no complete source inventory for that component covers the claim. State what the inspected files implement or qualify that the component was not fully verified",
				claim, strings.Join(unscoped, ", ")),
		}
	}
	return nil
}

// strongImplementationStatus reports a completeness claim that covers a whole
// component rather than a specific observed file.
func strongImplementationStatus(match []int, sentence string) bool {
	if len(match) < 2 {
		return false
	}
	phrase := strings.ToLower(sentence[match[0]:match[1]])
	for _, marker := range []string{
		"fully implemented", "fully-implemented",
		"completely implemented", "completely-implemented",
		"fully functional", "fully-functional",
		"feature complete", "feature-complete",
		"production ready", "production-ready",
	} {
		if strings.Contains(phrase, marker) {
			return true
		}
	}
	return false
}

// implementationClaimSubjects extracts the components a status claim names.
// The explicit list after a colon is preferred; a kebab coordinate and the
// bounded component noun vocabulary follow.
func implementationClaimSubjects(sentence string, match []int) []implementationSubject {
	labels := ComponentSubjects(sentence, match[0], match[1])
	seen := map[string]bool{}
	var out []implementationSubject
	for _, label := range labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == "" || seen[label] || componentTokenIsRole(label) {
			continue
		}
		seen[label] = true
		out = append(out, implementationSubject{
			Label: label,
			Name:  implementationDisplayName(sentence, label),
		})
	}
	return out
}

// implementationDisplayName returns the readable component coordinate the
// answer used. A kebab coordinate keeps its full spelling so the review can
// name product-service rather than only product.
func implementationDisplayName(sentence, label string) string {
	for _, token := range kebabComponentPattern.FindAllString(sentence, -1) {
		if strings.EqualFold(normalizeComponentPhrase(token), label) {
			return token
		}
	}
	for _, token := range strings.Fields(sentence) {
		cleaned := strings.Trim(token, " \t\r\n.,;:!?()[]{}<>\"'`*_")
		if strings.EqualFold(normalizeComponentPhrase(cleaned), label) {
			return cleaned
		}
	}
	return label
}

// implementationSubjectName renders the review name, falling back to the
// canonical label.
func implementationSubjectName(subject implementationSubject) string {
	if strings.TrimSpace(subject.Name) != "" {
		return subject.Name
	}
	return subject.Label
}

// implementationSubjectObserved reports a source read or source-scoped grep
// that covers the component. The path is the only coordinate: a phrase in the
// source text does not prove that the file belongs to another component.
func implementationSubjectObserved(turns []Turn, subject implementationSubject) bool {
	if subject.Label == "" {
		return false
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || !sourceObservationOutcome(outcome) {
				continue
			}
			path := strings.ToLower(filepath.ToSlash(outcomePath(outcome)))
			if ComponentMatchesPath(subject.Label, path) {
				return true
			}
		}
	}
	return false
}

// componentSourceScopeObserved reports an observed, current, complete source
// inventory for the component whose files were all read. It is the evidence
// that can support a whole-component completeness claim.
func componentSourceScopeObserved(turns []Turn, subject implementationSubject) bool {
	if subject.Label == "" {
		return false
	}
	if componentScopeFromStructure(turns, subject) {
		return true
	}
	return componentScopeFromGlob(turns, subject)
}

// componentScopeFromStructure accepts a scoped structure files view only when
// every completeness axis is current and every returned source file was read.
func componentScopeFromStructure(turns []Turn, subject implementationSubject) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.ToolName != "structure" {
				continue
			}
			details, ok := outcome.Result.Details.(map[string]any)
			if !ok || detailValueString(details, "view") != "files" {
				continue
			}
			if !detailValueBool(details, "scope_complete") ||
				!detailValueBool(details, "inventory_complete") ||
				!detailValueBool(details, "structural_complete") ||
				detailValueBool(details, "partial") ||
				detailValueBool(details, "truncated") ||
				detailValueString(details, "graph_state") != "CURRENT" {
				continue
			}
			pathArg := proposalArgString(outcome.Args, "path")
			if pathArg == "" {
				pathArg = detailValueString(details, "path")
			}
			if pathArg == "" || !ComponentMatchesPath(subject.Label, pathArg) {
				continue
			}
			total := detailValueInt(details, "total")
			returned := detailValueInt(details, "returned")
			if total <= 0 || returned < total {
				continue
			}
			files := claimFileRows(details["files"])
			if len(files) == 0 {
				continue
			}
			sourceFiles := 0
			allRead := true
			for _, file := range files {
				path := detailValueString(file, "path")
				if path == "" || detailValueBool(file, "test") || detailValueBool(file, "unsupported") {
					continue
				}
				if ClassifyPath(path) != PathSource {
					continue
				}
				sourceFiles++
				if !successfulSourceRead(turns, path) {
					allRead = false
					break
				}
			}
			if sourceFiles > 0 && allRead {
				return true
			}
		}
	}
	return false
}

// componentScopeFromGlob accepts a complete, unpruned, component-scoped scan
// only when every returned source file was read. A gitignore=false scan is an
// investigative result, not a complete project inventory.
func componentScopeFromGlob(turns []Turn, subject implementationSubject) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.ToolName != "glob" {
				continue
			}
			details, ok := outcome.Result.Details.(map[string]any)
			if !ok {
				continue
			}
			if detailValueInt(details, "ignored") > 0 || detailValueInt(details, "excluded") > 0 {
				continue
			}
			if value, ok := details["gitignore"].(bool); ok && !value {
				continue
			}
			if detailValueBool(details, "timedOut") || detailValueBool(details, "truncated") {
				continue
			}
			pathArg := proposalArgString(outcome.Args, "path")
			if pathArg == "" {
				pathArg = detailValueString(details, "path")
			}
			if pathArg == "" || !ComponentMatchesPath(subject.Label, pathArg) {
				continue
			}
			sourceFiles := 0
			allRead := true
			for _, path := range globSourcePaths(outcome.Result.Text) {
				sourceFiles++
				if !successfulSourceRead(turns, path) {
					allRead = false
					break
				}
			}
			if sourceFiles > 0 && allRead {
				return true
			}
		}
	}
	return false
}

// claimFileRows accepts the slice shape the structure tool returns.
func claimFileRows(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if row, ok := entry.(map[string]any); ok {
				out = append(out, row)
			}
		}
		return out
	default:
		return nil
	}
}

// globSourcePaths extracts the paths a glob returned. Notes are not paths.
func globSourcePaths(text string) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "No files") {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			continue
		}
		if ClassifyPath(line) == PathSource {
			out = append(out, line)
		}
		if len(out) >= 200 {
			break
		}
	}
	return out
}

// successfulSourceRead reports a successful read of the exact source path.
func successfulSourceRead(turns []Turn, path string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName != "read" || outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if !claimPathsMatch(outcomePath(outcome), path) {
				continue
			}
			if strings.TrimSpace(outcome.Result.Text) != "" {
				return true
			}
		}
	}
	return false
}

// sourceObservationExists reports at least one successful source observation.
func sourceObservationExists(turns []Turn) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if sourceObservationOutcome(outcome) {
				return true
			}
		}
	}
	return false
}
