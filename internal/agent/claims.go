package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Claim scope is the answer-side complement to evidence sufficiency.
//
// The evidence ledger can prove that a run observed a manifest, a Compose file,
// or a source file. Those observations are not interchangeable: a dependency
// manifest declares a library, Compose declares a topology, and only a source
// observation or a successful execution establishes what the implementation
// does. This file keeps an informational answer from promoting one class into a
// stronger one.
//
// The checks are deliberately narrow and deterministic. They are completion
// vetoes, never authority, and they do not judge whether a supported claim is
// true. A false positive would turn an honest answer into NEEDS_REVIEW, so each
// pattern requires a concrete behavior, metric, deployment, or path-scoped
// speculation rather than a generic assertion.
type claimIssue struct {
	// Code is the grounding issue kind.
	Code string
	// Detail explains the missing observation and the narrower claim.
	Detail string
}

var (
	// behaviorClaimPattern matches a declarative claim about what a service
	// does at runtime. Dependency names, Compose topology, and documentation
	// prose do not satisfy it.
	behaviorClaimPattern = regexp.MustCompile(`(?i)\b(?:` +
		`validat(?:es?|ed|ing)\s+(?:against|with)\b|` +
		`publishes?\b|` +
		`consumes?\s+(?:the\s+)?(?:[a-z0-9_-]+\s+){0,2}(?:cart|queue|message|event|stream|api|service|order|product|payment)s?\b|` +
		`subscribes?\s+to\b|` +
		`fulfills?\b|` +
		`calls?\s+(?:the\s+)?[a-z0-9_-]+\s+(?:service|api|endpoint)\b|` +
		`sends?\s+(?:an?\s+)?(?:request|event|message)\s+to\b|` +
		`reads?\s+from\b|` +
		`writes?\s+to\b|` +
		`auto[- ]?appl(?:y|ies|ied|ying)\b|` +
		`routes?\s+(?:requests?\s+)?(?:to|through)\b|` +
		`listens?\s+(?:on|for)\b|` +
		`process(?:es)?\s+(?:orders?|payments?|events?)\b|` +
		`handles?\s+(?:orders?|requests?|events?)\b|` +
		`stores?\s+(?:data|orders?|sessions?)\s+in\b|` +
		`connects?\s+to\b` +
		`)`)

	// deploymentClaimPattern matches a conclusion that services can be
	// deployed independently. Separate service declarations are not that
	// conclusion.
	deploymentClaimPattern = regexp.MustCompile(`(?i)\b(?:` +
		`(?:independently|separately)[\s-]+(?:deploy(?:ed|able|ment)?|scaled)\b|` +
		`deploy(?:ed|able)?\s+independently\b|` +
		`separate(?:ly)?\s+(?:deploy(?:ed|ment)?|release)s?\b` +
		`)`)

	// lineCountClaimPattern matches a concrete line-count or LOC claim. A
	// byte size printed by a directory listing is not a line count.
	lineCountClaimPattern = regexp.MustCompile(
		`(?i)\b(?:~|about\s+|approx(?:imately)?\s+|around\s+|roughly\s+)?([0-9][0-9,]*)\s*(?:LOC\b|lines?\s+of\s+code\b|lines?\b)`)

	// claimPathPattern extracts a repository coordinate from the same
	// sentence. It includes documentation and manifest suffixes because a
	// line-count claim about README.md is valid when that file was read.
	claimPathPattern = regexp.MustCompile(
		`(?i)(?:[a-z0-9_.-]+/)*[a-z0-9_.-]+\.(?:go|tsx?|jsx?|mjs|cjs|py|rs|java|kt|kts|rb|php|cs|md|markdown|rst|adoc|txt|json|ya?ml|toml|ini|conf|sql|sh|bash|proto|graphql|vue|svelte|c|cc|cpp|h|hpp|swift|scala)\b`)

	// speculationPattern marks an inference about content the run has not
	// read. The check only applies when the same sentence names the path, so
	// a general "the backend likely uses PostgreSQL" is not forced to repeat
	// the manifest observation.
	speculationPattern = regexp.MustCompile(
		`(?i)\b(?:likely|probably|presumably|possibly|seems?\s+to|appears?\s+to|suggests?\b|may\s+(?:be|contain|include|have)|might\s+(?:be|contain|include|have))\b`)

	// claimDirectoryPattern extracts a directory coordinate. It is a fallback
	// for a sentence that names a directory rather than a file.
	claimDirectoryPattern = regexp.MustCompile(`(?i)(?:[a-z0-9_.-]+/)+`)

	// speculativeContentPattern names content categories that can be checked
	// against the observed text for the referenced path.
	speculativeContentPattern = regexp.MustCompile(
		`(?i)\b(schemas?|edge\s+functions?|routes?|pages?|handlers?|controllers?|services?|tests?|ci|migrations?|data\s+layer|apis?|databases?|queues?|brokers?|caches?|auth(?:entication)?|sessions?|models?|endpoints?|workers?)\b`)

	// boundedObservationPattern reports a claim about what the probes found
	// in this pass, not a claim that the property is absent from the
	// repository.
	boundedObservationPattern = regexp.MustCompile(
		`(?i)\b(?:surfaced|found|detected|observed|seen|encountered|reported)\b[^.\n]{0,80}\b(?:in|during|from|by)\s+(?:the\s+|this\s+)?(?:probes?|pass|scan|search|listing|observations?|run|investigation)\b`)

	// boundedRunPattern reports that a check did not run. That is a fact
	// about the run, not a repository absence.
	boundedRunPattern = regexp.MustCompile(
		`(?i)\bno\s+(?:tests?|checks?|commands?)\s+(?:were|was|have\s+been|has\s+been)?\s*(?:run|executed|performed)\b`)

	// globalNegativeScopePattern prevents a bounded-search phrase from
	// excusing the same sentence's repository-wide conclusion.
	globalNegativeScopePattern = regexp.MustCompile(
		`(?i)\b(?:in|across|throughout)\s+(?:the\s+)?(?:repository|repo|project|codebase|workspace|directory|folder)\b|\banywhere\b|\bat\s+all\b`)
)

// claimScopeIssues returns the answer claims the run's observations cannot
// support. It is called only for an informational objective; a mutation answer
// is governed by target and verification evidence instead.
func claimScopeIssues(answer string, turns []Turn) []claimIssue {
	answer = stripFencedCode(answer)
	var issues []claimIssue
	for _, raw := range answerSentenceBoundary.Split(strings.TrimSpace(answer), -1) {
		sentence := strings.TrimSpace(raw)
		if sentence == "" || claimIsBounded(sentence) || claimIsDocumentationAttributed(sentence) {
			continue
		}
		lineIssue := lineCountClaimIssue(sentence, turns)
		if lineIssue != nil {
			issues = append(issues, *lineIssue)
		}
		if issue := behaviorClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := deploymentClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		// A line-count issue already explains why a metric about this path
		// is unsupported. Repeating the same path as speculation would only
		// crowd the bounded review reason.
		if lineIssue == nil {
			if issue := speculationClaimIssue(sentence, turns); issue != nil {
				issues = append(issues, *issue)
			}
		}
	}
	return prioritizeClaimIssues(dedupeClaimIssues(issues))
}

// prioritizeClaimIssues orders the review reason by consequence: an unsupported
// behavior claim is more misleading than a speculative detail, and a concrete
// line-count contradiction is more actionable than another inference. The
// caller still bounds the final reason; this ordering makes the first class of
// failure visible instead of letting answer order decide.
func prioritizeClaimIssues(issues []claimIssue) []claimIssue {
	rank := map[string]int{
		"unsupported_implementation_claim": 0,
		"unsupported_deployment_claim":     1,
		"unsupported_line_count":           2,
		"unverified_speculation":           3,
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return rank[issues[i].Code] < rank[issues[j].Code]
	})
	return issues
}

// lineCountClaimIssue checks a concrete line count against the file the run
// actually read. A directory listing prints a byte size; it is not a line
// count, and it must not be promoted into one.
func lineCountClaimIssue(sentence string, turns []Turn) *claimIssue {
	matches := lineCountClaimPattern.FindAllStringSubmatchIndex(sentence, -1)
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		claimed := claimCount(sentence[match[2]:match[3]])
		if claimed <= 0 {
			continue
		}
		path := nearestClaimPath(sentence, match[0], match[1])
		if path == "" {
			if commandLineCountObserved(turns, claimed) {
				continue
			}
			return &claimIssue{
				Code: "unsupported_line_count",
				Detail: fmt.Sprintf(
					"the answer gives a line count of about %d but the run never measured one; read the file or run a line-count command, or remove the count",
					claimed),
			}
		}
		total, observed := observedReadLineCount(turns, path)
		if !observed {
			return &claimIssue{
				Code: "unsupported_line_count",
				Detail: fmt.Sprintf(
					"the answer gives %s about %d lines, but the run never read it; a listing reports bytes, not lines. Read it or remove the count",
					path, claimed),
			}
		}
		if !lineCountMatches(total, claimed) {
			return &claimIssue{
				Code: "unsupported_line_count",
				Detail: fmt.Sprintf(
					"the answer says %s has about %d lines, but the observed read reported %d; correct the count or cite the measurement",
					path, claimed, total),
			}
		}
	}
	return nil
}

// behaviorClaimIssue checks a runtime-behavior claim against source
// observations. Configuration and manifests declare intent; they do not prove
// execution.
func behaviorClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := behaviorClaimPattern.FindString(sentence)
	if match == "" {
		return nil
	}
	if autoApplyClaim(match) {
		// A database init hook is a configuration mechanism: the Compose
		// mount into docker-entrypoint-initdb.d proves that init scripts are
		// provided by configuration, even without a source read. Prose that
		// mentions the hook is not the mechanism.
		if observedMechanismContains(turns, "docker-entrypoint-initdb", "initdb", "entrypoint-initdb") {
			return nil
		}
		return &claimIssue{
			Code: "unsupported_implementation_claim",
			Detail: fmt.Sprintf(
				"the answer says %q, but no observed configuration or source shows that mechanism; verify it or mark it unverified",
				boundedClaim(match)),
		}
	}
	stem := behaviorStem(match)
	if stem == "" || implementationTextContains(turns, stem) {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_implementation_claim",
		Detail: fmt.Sprintf(
			"the answer states %q as runtime behavior without a source observation; configuration declares topology, not execution. Verify it or mark it unverified",
			boundedClaim(match)),
	}
}

// deploymentClaimIssue checks a claim that services are independently
// deployable against deployment artifacts, not merely separate Compose
// service declarations.
func deploymentClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := deploymentClaimPattern.FindString(sentence)
	if match == "" || deploymentObserved(turns) {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_deployment_claim",
		Detail: fmt.Sprintf(
			"the answer says %q, but separate service declarations do not establish independent deployment. Read a deployment artifact or state the narrower topology claim",
			boundedClaim(match)),
	}
}

// speculationClaimIssue checks a path-scoped inference about content the run
// never read. A general inference is left to the model; a claim that a named
// path contains a category must be supported by an observation of that path.
func speculationClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := speculationPattern.FindStringIndex(sentence)
	if match == nil {
		return nil
	}
	ref := nearestReferencePath(sentence, match[0], match[1])
	if ref == "" {
		return nil
	}
	noun := speculativeContentPattern.FindString(sentence)
	if noun == "" || speculationSupported(turns, ref, noun) {
		return nil
	}
	return &claimIssue{
		Code: "unverified_speculation",
		Detail: fmt.Sprintf(
			"the answer speculates that %s contains %q, but no observation of %s supports it; mark it as uninspected or omit it",
			ref, strings.ToLower(noun), ref),
	}
}

// claimIsBounded reports an explicit limitation. An answer that says a claim
// is unverified, not checked, or worth verifying is using the lower-assertion
// option rather than presenting the claim as observed.
func claimIsBounded(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{
		"not verified", "unverified", "cannot verify", "can't verify",
		"could not verify", "couldn't verify", "not established",
		"no evidence", "would need", "worth verifying", "not evaluated",
		"not inspected", "not checked", "not probed", "in the probes",
		"in this pass", "does not show", "doesn't show", "does not establish",
		"doesn't establish", "does not prove", "doesn't prove", "not shown",
		"not proven", "not implemented", "planned", "stubbed", "inferred",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// claimIsDocumentationAttributed reports a claim explicitly assigned to the
// project's own description. The provenance layer already keeps documentation
// claims distinct from implementation facts; a behavior claim that names the
// README is not an unattributed implementation claim.
func claimIsDocumentationAttributed(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{
		"readme", "documentation", "docs", "according to", "described in",
		"stated in", "reported in", "project says", "project states",
		"project claims", "project describes", "repository says",
		"repository states", "repository claims", "repository describes",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// boundedNegativeReport reports whether a negative phrase is scoped to what
// this run's probes surfaced. It is not a claim that the property is absent
// from the repository, unless the same sentence also makes a global scope
// claim.
func boundedNegativeReport(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{
		"not evaluated", "not inspected", "not checked", "not probed",
		"not searched", "worth verifying", "would need to check",
		"would need to verify", "would need to inspect", "would need to search",
		"in the probes", "in this pass", "in this scan",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if globalNegativeScopePattern.MatchString(lower) {
		return false
	}
	return boundedObservationPattern.MatchString(lower) || boundedRunPattern.MatchString(lower)
}

// nearestClaimPath returns the path token closest to the metric span. It
// prefers a path before the number ("src/App.tsx (~1200 LOC)") and falls back
// to a path after it ("1200 LOC in src/App.tsx").
func nearestClaimPath(sentence string, start, end int) string {
	const maxDistance = 96
	best := ""
	bestDistance := 1 << 30
	for _, path := range claimPathPattern.FindAllStringIndex(sentence, -1) {
		distance := 0
		switch {
		case path[1] <= start:
			distance = start - path[1]
		case path[0] >= end:
			distance = path[0] - end
		default:
			continue
		}
		if distance < bestDistance {
			bestDistance = distance
			best = sentence[path[0]:path[1]]
		}
	}
	if bestDistance > maxDistance {
		return ""
	}
	return strings.Trim(best, "`'\" ")
}

// nearestReferencePath returns the file or directory coordinate closest to a
// speculation phrase. A sentence may name an entry point and then the file the
// inference is about ("src/main.tsx → src/App.tsx, likely..."); the nearest
// coordinate is the subject of the inference.
func nearestReferencePath(sentence string, start, end int) string {
	const maxDistance = 160
	type candidate struct {
		start, end int
		value      string
	}
	var candidates []candidate
	files := claimPathPattern.FindAllStringIndex(sentence, -1)
	for _, path := range files {
		candidates = append(candidates, candidate{path[0], path[1], sentence[path[0]:path[1]]})
	}
	for _, dir := range claimDirectoryPattern.FindAllStringIndex(sentence, -1) {
		// The directory prefix of a named file is less specific than the file
		// itself: "src/" must not win over "src/App.tsx".
		filePrefix := false
		for _, path := range files {
			if path[0] == dir[0] {
				filePrefix = true
				break
			}
		}
		if filePrefix {
			continue
		}
		value := strings.Trim(sentence[dir[0]:dir[1]], "`'\" ")
		segments := strings.Split(strings.Trim(value, "/"), "/")
		// A single-segment token such as "schema/" or "routes/" in
		// "likely schema/edge functions" is the speculative category, not the
		// path being described. A real nested directory keeps its parent.
		if len(segments) == 1 && speculativeContentPattern.MatchString(segments[0]) {
			continue
		}
		candidates = append(candidates, candidate{dir[0], dir[1], value})
	}
	best := ""
	bestDistance := 1 << 30
	bestLength := 0
	for _, item := range candidates {
		distance := 0
		switch {
		case item.end <= start:
			distance = start - item.end
		case item.start >= end:
			distance = item.start - end
		default:
			continue
		}
		length := item.end - item.start
		if distance < bestDistance || (distance == bestDistance && length > bestLength) {
			bestDistance = distance
			bestLength = length
			best = item.value
		}
	}
	if bestDistance > maxDistance {
		return ""
	}
	return strings.Trim(best, "`'\" ")
}

// observedReadLineCount returns the total line count reported by a successful
// read of path.
func observedReadLineCount(turns []Turn, path string) (int, bool) {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName != "read" || outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if !claimPathsMatch(outcomePath(outcome), path) {
				continue
			}
			if total := claimDetailInt(outcome.Result.Details, "totalLines"); total > 0 {
				return total, true
			}
		}
	}
	return 0, false
}

// commandLineCountObserved reports a line count measured by a command.
func commandLineCountObserved(turns []Turn, claimed int) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName != "bash" || outcome.Result.IsError || !outcome.Result.Executes {
				continue
			}
			command := strings.ToLower(proposalArgString(outcome.Args, "command"))
			if !strings.Contains(command, "wc ") && !strings.Contains(command, "cloc") &&
				!strings.Contains(command, "tokei") && !strings.Contains(command, "scc ") &&
				!strings.Contains(command, "sloc") {
				continue
			}
			for _, raw := range lineCountNumberPattern.FindAllString(outcome.Result.Text, -1) {
				if value := claimCount(raw); value > 0 && lineCountMatches(value, claimed) {
					return true
				}
			}
		}
	}
	return false
}

var lineCountNumberPattern = regexp.MustCompile(`\b\d[\d,]*\b`)

// lineCountMatches accepts an approximation within a fifth of the claim (or
// 50 lines, whichever is larger). A gross contradiction such as 40 vs 1200 is
// not an approximation.
func lineCountMatches(observed, claimed int) bool {
	tolerance := claimed / 5
	if tolerance < 50 {
		tolerance = 50
	}
	difference := observed - claimed
	if difference < 0 {
		difference = -difference
	}
	return difference <= tolerance
}

// claimCount parses a positive decimal count with optional commas.
func claimCount(raw string) int {
	value, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(raw), ",", ""))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// implementationTextContains reports whether a source observation contains the
// behavior stem.
func implementationTextContains(turns []Turn, stem string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			switch outcome.ToolName {
			case "read":
				if isImplementationPath(outcomePath(outcome)) &&
					strings.Contains(strings.ToLower(outcome.Result.Text), stem) {
					return true
				}
			case "grep":
				if isImplementationSearchPath(outcomePath(outcome)) &&
					strings.Contains(strings.ToLower(outcome.Result.Text), stem) {
					return true
				}
			}
		}
	}
	return false
}

// deploymentObserved reports a read deployment artifact. A Dockerfile or a
// Kubernetes/Helm manifest is a deployment boundary; a Compose service entry
// alone is topology.
func deploymentObserved(turns []Turn) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || isDocumentationOutcome(outcome) {
				continue
			}
			path := strings.ToLower(filepath.ToSlash(outcomePath(outcome)))
			base := filepath.Base(path)
			if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
				return true
			}
			for _, prefix := range []string{"k8s/", "deploy/", "deployment/", "helm/", "charts/"} {
				if strings.HasPrefix(path, prefix) || strings.Contains(path, "/"+prefix) {
					return true
				}
			}
			text := strings.ToLower(outcome.Result.Text)
			if strings.Contains(text, "kind: deployment") || strings.Contains(text, "apiversion: apps/v1") {
				return true
			}
		}
	}
	return false
}

// observedMechanismContains reports a successful configuration or source
// observation whose text contains one of the markers. Documentation prose is
// not a mechanism.
func observedMechanismContains(turns []Turn, markers ...string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || isDocumentationOutcome(outcome) {
				continue
			}
			text := strings.ToLower(outcome.Result.Text)
			for _, marker := range markers {
				if strings.Contains(text, marker) {
					return true
				}
			}
		}
	}
	return false
}

// speculationSupported reports whether the observation scoped to ref contains
// the speculative content noun.
func speculationSupported(turns []Turn, ref, noun string) bool {
	stem := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(noun), "s"))
	if stem == "" {
		return false
	}
	reference := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ref)), "/")
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || isDocumentationOutcome(outcome) {
				continue
			}
			path := strings.TrimSuffix(strings.ToLower(filepath.ToSlash(outcomePath(outcome))), "/")
			text := strings.ToLower(outcome.Result.Text)
			if !pathScopeCovers(path, reference) && !strings.Contains(text, reference) {
				continue
			}
			if strings.Contains(text, stem) {
				return true
			}
		}
	}
	return false
}

// pathScopeCovers reports whether an observation path is the reference or an
// ancestor listing of it.
func pathScopeCovers(observed, reference string) bool {
	observed = strings.Trim(strings.TrimSpace(filepath.ToSlash(observed)), "/")
	reference = strings.Trim(strings.TrimSpace(filepath.ToSlash(reference)), "/")
	if observed == "" || reference == "" {
		return false
	}
	if observed == reference ||
		strings.HasPrefix(reference, observed+"/") ||
		strings.HasPrefix(observed, reference+"/") {
		return true
	}
	return filepath.Base(observed) == filepath.Base(reference)
}

// isImplementationPath reports whether a read path is source code rather than
// documentation, a manifest, configuration, or generated content.
func isImplementationPath(path string) bool {
	return ClassifyPath(path) == PathSource
}

// isImplementationSearchPath reports whether a grep path is a source scope.
// A grep normally names a directory, so the extension check cannot apply. A
// repository-root search is mixed content, not a source-only observation.
func isImplementationSearchPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return false
	}
	switch ClassifyPath(path) {
	case PathDocumentation, PathManifest, PathConfiguration, PathGenerated:
		return false
	default:
		return true
	}
}

// claimPathsMatch compares a read path with the answer's coordinate. The tools
// may report an absolute path while the answer names a repository-relative one.
func claimPathsMatch(observed, claimed string) bool {
	observed = strings.Trim(strings.ToLower(filepath.ToSlash(strings.TrimSpace(observed))), "/")
	claimed = strings.Trim(strings.ToLower(filepath.ToSlash(strings.TrimSpace(claimed))), "/")
	if observed == "" || claimed == "" {
		return false
	}
	if observed == claimed || strings.HasSuffix(observed, "/"+claimed) {
		return true
	}
	if !strings.Contains(claimed, "/") && filepath.Base(observed) == claimed {
		return true
	}
	return false
}

// behaviorStem returns the source token a claim's behavior word would appear
// as in an implementation observation.
func behaviorStem(phrase string) string {
	word := strings.ToLower(strings.Fields(phrase)[0])
	switch {
	case strings.HasPrefix(word, "validat"):
		return "validat"
	case strings.HasPrefix(word, "publish"):
		return "publish"
	case strings.HasPrefix(word, "consum"):
		return "consum"
	case strings.HasPrefix(word, "subscrib"):
		return "subscrib"
	case strings.HasPrefix(word, "fulfil"):
		return "fulfil"
	case strings.HasPrefix(word, "call"):
		return "call"
	case strings.HasPrefix(word, "send"):
		return "send"
	case strings.HasPrefix(word, "read"):
		return "read"
	case strings.HasPrefix(word, "writ"):
		return "writ"
	case strings.HasPrefix(word, "rout"):
		return "rout"
	case strings.HasPrefix(word, "listen"):
		return "listen"
	case strings.HasPrefix(word, "process"):
		return "process"
	case strings.HasPrefix(word, "handl"):
		return "handl"
	case strings.HasPrefix(word, "stor"):
		return "stor"
	case strings.HasPrefix(word, "connect"):
		return "connect"
	default:
		return ""
	}
}

// autoApplyClaim reports whether the matched behavior phrase is an
// auto-application mechanism.
func autoApplyClaim(phrase string) bool {
	return strings.HasPrefix(strings.ToLower(phrase), "auto")
}

// boundedClaim trims a matched claim to a readable length for the review
// reason.
func boundedClaim(claim string) string {
	claim = strings.TrimSpace(claim)
	runes := []rune(claim)
	if len(runes) <= 80 {
		return claim
	}
	return strings.TrimSpace(string(runes[:77])) + "..."
}

// outcomePath returns the path a tool observation names.
func outcomePath(outcome ToolOutcome) string {
	if path := detailString(outcome.Result.Details, "path"); path != "" {
		return path
	}
	return proposalArgString(outcome.Args, "path")
}

// isDocumentationOutcome reports a prose observation. Documentation can
// support an attributed claim, but it is not implementation or configuration
// evidence.
func isDocumentationOutcome(outcome ToolOutcome) bool {
	return ClassifyPath(outcomePath(outcome)) == PathDocumentation
}

// claimDetailInt reads an integer field from a tool result's Details map.
func claimDetailInt(details any, key string) int {
	values, ok := details.(map[string]any)
	if !ok {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

// stripFencedCode removes quoted tool output so a code block cannot be read as
// a prose claim.
func stripFencedCode(answer string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// dedupeClaimIssues preserves the first occurrence of each issue.
func dedupeClaimIssues(issues []claimIssue) []claimIssue {
	seen := map[string]bool{}
	var out []claimIssue
	for _, issue := range issues {
		key := issue.Code + "\x00" + issue.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	return out
}
