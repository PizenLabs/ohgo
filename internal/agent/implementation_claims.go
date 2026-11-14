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
	// statusNegationPattern names a negation that binds to the status word.
	// "Not production-ready" and "the service is not fully implemented" assert
	// the opposite of a completion claim; "not only fully implemented" does
	// not, so only a bounded set of adverbs may sit between the two.
	statusNegationPattern = regexp.MustCompile(`(?i)(?:^|\s)(?:not|never)\s+` +
		`(?:(?:yet|actually|currently|really|fully|entirely|strictly)\s+)?$|` +
		`(?:isn't|aren't|wasn't|weren't|no\s+longer)\s+$`)

	// mockStatusPattern names a mock, scaffold, or placeholder claim. Unlike a
	// bare stub claim, the marker is observable in source (a mock import, a
	// scaffold comment, a fixture reference), so the audit can require the
	// observation to show it rather than only the component path.
	mockStatusPattern = regexp.MustCompile(`(?i)\b(?:` +
		`mock(?:ed)?\s+(?:data|auth(?:entication)?|backend|api)|` +
		`(?:is|are|was|were)\s+(?:a\s+)?mock\b|` +
		`scaffold(?:ed)?\b|` +
		`placeholder\b` +
		`)`)

	// implementationStatusPattern matches a declarative completion, stub, mock,
	// or delivered-object claim. Bare "implemented" and "stubs" are
	// deliberately absent: headings such as "implemented vs. stubs" are not
	// claims. The present-tense verb form is present because it attaches a
	// concrete delivered object ("order-service implements a checkout API"),
	// which is a status claim even without a completion adjective.
	implementationStatusPattern = regexp.MustCompile(`(?i)\b(?:` +
		`fully[\s-]+implemented|` +
		`completely[\s-]+implemented|` +
		`partially[\s-]+implemented|` +
		`fully[\s-]+functional|` +
		`feature[\s-]+complete|` +
		`production[\s-]+ready|` +
		`stubs?\s*\(|` +
		`stubs?\s*:|` +
		`(?:is|are)\s+(?:a\s+)?(?:[a-z0-9][a-z0-9-]*\s+){0,2}stubs?\b|` +
		`health[\s-]+check[\s-]+only\b|` +
		`mock(?:ed)?\s+(?:data|auth(?:entication)?|backend|api)\b|` +
		`scaffold(?:ed)?\b|` +
		`placeholder\b|` +
		`\b(?:is|are)\s+mocked\b|` +
		`\b(?:implements?|implementing)\s+(?:a\s+|an\s+|the\s+)?(?:[a-z0-9][a-z0-9-]*\s+){0,2}` +
		`(?:api|service|backend|frontend|application|endpoint|crud|feature|functionality|workflow|pipeline)\b|` +
		`\bimplemented\s+(?:service|backend|frontend|api|application)\b|` +
		`\b(?:service|backend|frontend|api)\s+(?:is|are)\s+implemented\b` +
		`)`)
)

// statusClaimNegated reports whether a negation binds to the status phrase that
// starts at start.
func statusClaimNegated(sentence string, start int) bool {
	left := start - 24
	if left < 0 {
		left = 0
	}
	return statusNegationPattern.MatchString(sentence[left:start])
}

// implementationSubject is one component a status claim names.
type implementationSubject struct {
	// Label is the canonical component identity the source observation must
	// cover.
	Label string
	// Name is the readable component name for the review reason.
	Name string
}

// implementationAspect is one named capability a status claim asserts. A
// whole-component status does not establish every capability in its aspect
// list: "fully implemented with CRUD and graceful shutdown" asserts two
// different source observations.
type implementationAspect struct {
	// Label is the readable capability the answer named.
	Label string
	// Keywords identify the aspect inside the answer's aspect list.
	Keywords []string
	// Markers are the source tokens that can establish it.
	Markers []string
	// Minimum is how many distinct markers must appear. CRUD needs more than
	// one verb; most aspects need one.
	Minimum int
}

// implementationAspects is data, not policy by model or project. It covers the
// capability vocabulary callers use in implementation-status claims; an aspect
// outside the list is left to the ordinary implementation and dependency
// audits.
var implementationAspects = []implementationAspect{
	{Label: "CRUD", Keywords: []string{"crud"}, Markers: []string{"create", "update", "delete", "read", "get", "post", "put", "patch"}, Minimum: 2},
	{Label: "caching", Keywords: []string{"cach", "redis"}, Markers: []string{"cache", "redis", ".get(", ".set(", ".hget(", ".hset("}, Minimum: 1},
	{Label: "graceful shutdown", Keywords: []string{"shutdown", "graceful"}, Markers: []string{"shutdown", "signal.notify", "os.signal", "context.done", "graceful"}, Minimum: 1},
	{Label: "structured logging", Keywords: []string{"logging", "structured log"}, Markers: []string{"slog", "logger", "log.", "zap", "zerolog", "logrus", "logging"}, Minimum: 1},
	{Label: "authentication", Keywords: []string{"authentication", "auth", "login"}, Markers: []string{"auth", "jwt", "token", "login", "session"}, Minimum: 1},
	{Label: "migrations", Keywords: []string{"migration"}, Markers: []string{"migrat"}, Minimum: 1},
	{Label: "health checks", Keywords: []string{"health"}, Markers: []string{"health", "readiness", "liveness", "/healthz", "/readyz"}, Minimum: 1},
	{Label: "metrics", Keywords: []string{"metric"}, Markers: []string{"metric", "prometheus", "statsd"}, Minimum: 1},
	{Label: "tracing", Keywords: []string{"tracing", "trace"}, Markers: []string{"trac", "otel", "span"}, Minimum: 1},
	{Label: "rate limiting", Keywords: []string{"rate limit", "ratelimit", "throttl"}, Markers: []string{"ratelimit", "rate_limit", "limiter", "throttle"}, Minimum: 1},
	{Label: "retries", Keywords: []string{"retry", "retries", "backoff"}, Markers: []string{"retry", "backoff"}, Minimum: 1},
	{Label: "validation", Keywords: []string{"validation", "validates"}, Markers: []string{"validat"}, Minimum: 1},
	{Label: "idempotency", Keywords: []string{"idempot"}, Markers: []string{"idempot"}, Minimum: 1},
	{Label: "pagination", Keywords: []string{"pagination", "paginated"}, Markers: []string{"paginat"}, Minimum: 1},
	{Label: "WebSocket support", Keywords: []string{"websocket"}, Markers: []string{"websocket", "ws."}, Minimum: 1},
	{Label: "gRPC", Keywords: []string{"grpc"}, Markers: []string{"grpc"}, Minimum: 1},
	{Label: "REST endpoints", Keywords: []string{"rest", "restful"}, Markers: []string{"http.", "chi.", "gin.", "echo.", "mux.", "route"}, Minimum: 1},
	{Label: "tests", Keywords: []string{"test"}, Markers: []string{"_test", "test"}, Minimum: 1},
}

// mockMarkers are the source tokens that establish a mock/scaffold claim.
var mockMarkers = []string{
	"mock", "stub", "fake", "fixture", "scaffold", "placeholder", "todo",
}

// implementationClaimIssue checks a status claim against source observations.
// A manifest, Compose file, README, directory listing, or structural inventory
// does not establish implementation state.
func implementationClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := implementationStatusPattern.FindStringIndex(sentence)
	if match == nil {
		// The mock vocabulary names status too ("Auth is mock") without a
		// completion adjective, so it participates in the same gate.
		match = mockStatusPattern.FindStringIndex(sentence)
	}
	if match == nil {
		return nil
	}
	if statusClaimNegated(sentence, match[0]) {
		// "Not production-ready" is the inverse of a completion claim.
		return nil
	}
	claim := boundedClaim(strings.TrimRight(sentence[match[0]:match[1]], " (:"))
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
	if issue := mockImplementationClaimIssue(sentence, subjects, turns); issue != nil {
		return issue
	}
	if strongImplementationStatus(match, sentence) {
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
	}
	if issue := implementationAspectIssue(sentence, match, subjects, turns); issue != nil {
		return issue
	}
	return nil
}

// mockImplementationClaimIssue requires a mock/scaffold marker in the source
// observation for each named component. Reading a component's entrypoint does
// not establish that its data or authentication is mocked; the source has to
// show the mock.
func mockImplementationClaimIssue(sentence string, subjects []implementationSubject, turns []Turn) *claimIssue {
	if !mockStatusPattern.MatchString(sentence) || len(subjects) == 0 {
		return nil
	}
	var missing []string
	for _, subject := range subjects {
		text := observedSourceText(turns, subject)
		if !textContainsAny(text, mockMarkers) {
			missing = append(missing, implementationSubjectName(subject))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_implementation_claim",
		Detail: fmt.Sprintf(
			"the answer claims a mock, scaffold, or placeholder state for %s, but the observed source for that component contains no such marker; read the mock/scaffold source or narrow the claim",
			strings.Join(missing, ", ")),
	}
}

// implementationAspectIssue requires source evidence for the capability list a
// status claim asserts. A complete component scope proves what was inspected,
// not that every named capability is implemented.
func implementationAspectIssue(sentence string, match []int, subjects []implementationSubject, turns []Turn) *claimIssue {
	if len(subjects) == 0 || len(match) < 2 {
		return nil
	}
	tail := implementationAspectTail(sentence, match[1])
	if tail == "" {
		return nil
	}
	var required []implementationAspect
	seen := map[string]bool{}
	for _, phrase := range componentListSeparator.Split(tail, -1) {
		phrase = strings.ToLower(strings.TrimSpace(phrase))
		if phrase == "" {
			continue
		}
		for _, aspect := range implementationAspects {
			if seen[aspect.Label] || !aspectPhraseMatches(aspect, phrase) {
				continue
			}
			seen[aspect.Label] = true
			required = append(required, aspect)
			break
		}
	}
	if len(required) == 0 {
		return nil
	}
	var missing []string
	for _, subject := range subjects {
		text := observedSourceText(turns, subject)
		for _, aspect := range required {
			if !aspectObserved(text, aspect) {
				missing = append(missing, implementationSubjectName(subject)+": "+aspect.Label)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_implementation_claim",
		Detail: fmt.Sprintf(
			"the answer states %q with capabilities the observed source does not show (%s); implementation status covers the component, not every asserted capability. Verify each capability or state only the observed ones",
			boundedClaim(sentence[match[0]:match[1]]), strings.Join(missing, ", ")),
	}
}

// implementationAspectTail returns the aspect list after a status marker. It is
// deliberately narrow: only an explicit "with/including/that includes/featuring"
// clause or a parenthetical list is treated as a capability list.
func implementationAspectTail(sentence string, start int) string {
	if start < 0 || start >= len(sentence) {
		return ""
	}
	rest := sentence[start:]
	lower := strings.ToLower(rest)
	best := -1
	marker := ""
	for _, candidate := range []string{" with ", " including ", " that includes ", " featuring ", " ("} {
		if index := strings.Index(lower, candidate); index >= 0 && (best < 0 || index < best) {
			best = index
			marker = candidate
		}
	}
	if best < 0 {
		return ""
	}
	tail := rest[best+len(marker):]
	// A parenthetical list ends at its closing bracket; a clause ends at the
	// sentence boundary. Reading past either would attribute the next claim's
	// capabilities to this one.
	cut := ".;\n"
	if marker == " (" {
		cut = ")"
	}
	if index := strings.IndexAny(tail, cut); index >= 0 {
		tail = tail[:index]
	}
	return strings.TrimSpace(tail)
}

// aspectPhraseMatches reports whether one aspect-list phrase names the aspect.
func aspectPhraseMatches(aspect implementationAspect, phrase string) bool {
	for _, keyword := range aspect.Keywords {
		if strings.Contains(phrase, keyword) {
			return true
		}
	}
	return false
}

// aspectObserved reports whether the source text carries enough distinct
// markers for the aspect.
func aspectObserved(text string, aspect implementationAspect) bool {
	seen := map[string]bool{}
	for _, marker := range aspect.Markers {
		if strings.Contains(text, marker) {
			seen[marker] = true
		}
	}
	minimum := aspect.Minimum
	if minimum < 1 {
		minimum = 1
	}
	return len(seen) >= minimum
}

// observedSourceText returns the lower-case source content observed for one
// component. It never includes documentation, manifests, or configuration.
func observedSourceText(turns []Turn, subject implementationSubject) string {
	var sb strings.Builder
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || !sourceObservationOutcome(outcome) {
				continue
			}
			path := strings.ToLower(filepath.ToSlash(outcomePath(outcome)))
			if subject.Label != "" && !ComponentMatchesPath(subject.Label, path) {
				continue
			}
			sb.WriteString(strings.ToLower(outcome.Result.Text))
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// textContainsAny reports whether text contains one of the markers.
func textContainsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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
