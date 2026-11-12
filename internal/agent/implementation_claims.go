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
// The check is deterministic and narrow. It recognizes explicit status
// vocabulary, extracts the components the claim names, and requires a source
// observation for each. It never decides whether the implementation is
// actually complete; it decides only whether the evidence class the claim
// requires was observed.

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
		`(?:is|are)\s+(?:a\s+)?stubs?\b|` +
		`mock\s+(?:data|auth|backend|api)\b|` +
		`scaffold(?:ed)?\b` +
		`)`)

	// implementationKebabPattern extracts a concrete component coordinate such
	// as "product-service" or "api-gateway".
	implementationKebabPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]*(?:-[a-z0-9]+)+\b`)

	// implementationParentheticalPattern removes a qualifier such as
	// "(health checks only)" or "(REST CRUD, Redis caching)" before the
	// component list is read. Parentheticals qualify a claim; they are not
	// additional subjects.
	implementationParentheticalPattern = regexp.MustCompile(`\([^)]*\)`)

	// implementationListSeparator splits a component list. " and " is a list
	// separator in this vocabulary; a sentence boundary has already split the
	// answer into clauses.
	implementationListSeparator = regexp.MustCompile(`(?i),\s*|\s+and\s+`)

	// implementationSubjectStopWords are grammar and generic identity words
	// that do not name a component. "service" is a suffix, not a subject:
	// "product service" is the product component.
	implementationSubjectStopWords = map[string]bool{
		"the": true, "a": true, "an": true, "this": true, "that": true,
		"these": true, "those": true, "its": true, "their": true, "our": true,
		"your": true, "it": true, "and": true, "or": true, "of": true,
		"for": true, "to": true, "in": true, "on": true, "with": true,
		"by": true, "from": true, "as": true, "at": true, "is": true,
		"are": true, "was": true, "were": true, "be": true, "been": true,
		"being": true, "has": true, "have": true, "had": true,
		"project": true, "repository": true, "repo": true, "codebase": true,
		"workspace": true, "application": true, "app": true, "system": true,
		"platform": true, "code": true, "source": true, "implementation": true,
		"module": true, "package": true, "service": true, "services": true,
	}

	// implementationTrailingStatusWords are grammar or status words that may
	// follow the component phrase before the status marker ("the backend is").
	implementationTrailingStatusWords = map[string]bool{
		"is": true, "are": true, "was": true, "were": true, "be": true,
		"been": true, "being": true, "has": true, "have": true, "had": true,
		"fully": true, "completely": true, "partially": true,
		"implemented": true, "functional": true, "production": true,
		"ready": true, "feature": true, "complete": true,
		"stub": true, "stubs": true, "scaffolded": true,
		"now": true, "currently": true, "already": true, "actually": true,
		"also": true, "both": true, "all": true,
	}
)

// implementationComponentNouns is the finite vocabulary of component names a
// status claim can name. A kebab-case identifier is also accepted, so a
// project-specific name such as "product-service" does not need a vocabulary
// entry. The noun is chosen from the end of the phrase so "React frontend"
// resolves to the frontend, not to the technology.
var implementationComponentNouns = map[string]bool{
	"product": true, "order": true, "cart": true, "delivery": true,
	"notification": true, "payment": true, "inventory": true, "user": true,
	"auth": true, "gateway": true, "frontend": true, "backend": true,
	"web": true, "admin": true, "worker": true, "client": true,
	"server": true, "api": true, "checkout": true, "catalog": true,
	"search": true, "shipping": true, "billing": true, "email": true,
	"account": true,
}

// implementationSubject is one component a status claim names.
type implementationSubject struct {
	// Label is the path or noun the source observation must carry.
	Label string
	// Name is the readable component name for the review reason.
	Name string
	// Words are the normalized content words of a compound label. They are
	// used when the label itself is not a path segment.
	Words []string
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
		if sourceObservationExists(turns) {
			return nil
		}
		return &claimIssue{
			Code: "unsupported_implementation_claim",
			Detail: fmt.Sprintf(
				"the answer states %q as implementation state, but the run observed only documentation, configuration, manifest, or inventory; read source before claiming implementation or mark it unverified",
				claim),
		}
	}
	var missing []string
	for _, subject := range subjects {
		if !implementationSubjectObserved(turns, subject) {
			name := subject.Name
			if name == "" {
				name = subject.Label
			}
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_implementation_claim",
		Detail: fmt.Sprintf(
			"the answer states %q as implementation state, but no source observation covers %s; read that component or state the narrower claim",
			claim, strings.Join(missing, ", ")),
	}
}

// implementationClaimSubjects extracts the components a status claim names.
// The explicit list after a colon is preferred; a kebab-case coordinate and
// the phrase before the status marker are fallbacks.
func implementationClaimSubjects(sentence string, match []int) []implementationSubject {
	seen := map[string]bool{}
	var out []implementationSubject
	add := func(subject implementationSubject) {
		if subject.Label == "" || seen[subject.Label] {
			return
		}
		seen[subject.Label] = true
		out = append(out, subject)
	}
	withoutParentheticals := implementationParentheticalPattern.ReplaceAllString(sentence, " ")

	for _, item := range implementationStatusList(sentence, match[1]) {
		add(normalizeImplementationSubject(item))
	}
	for _, token := range implementationKebabPattern.FindAllString(withoutParentheticals, -1) {
		if implementationTokenIsStatus(token) || implementationTokenIsFileName(withoutParentheticals, token) {
			continue
		}
		add(normalizeImplementationSubject(token))
	}
	if len(out) == 0 {
		before := strings.TrimSpace(sentence[:match[0]])
		for _, item := range implementationListSeparator.Split(before, -1) {
			add(normalizeImplementationSubject(item))
		}
	}
	return out
}

// implementationStatusList returns the explicit component list after the status
// marker. A colon that belongs to a later clause is not a component list.
func implementationStatusList(sentence string, after int) []string {
	rest := sentence[after:]
	colon := strings.Index(rest, ":")
	if colon < 0 || colon > 48 {
		return nil
	}
	if strings.ContainsAny(rest[:colon], ";?!") {
		return nil
	}
	tail := strings.TrimSpace(rest[colon+1:])
	if tail == "" {
		return nil
	}
	// Parentheticals contain feature lists with their own commas. Remove them
	// before splitting so "(REST CRUD, Redis caching)" is not mistaken for
	// additional components.
	tail = implementationParentheticalPattern.ReplaceAllString(tail, " ")
	items := implementationListSeparator.Split(tail, -1)
	for _, item := range items {
		if normalizeImplementationSubject(item).Label != "" {
			return items
		}
	}
	return nil
}

// normalizeImplementationSubject reduces a list item or phrase to one component
// label. A recognised component noun wins; otherwise a kebab-case identifier or
// path is retained so project-specific names remain checkable.
func normalizeImplementationSubject(raw string) implementationSubject {
	raw = implementationParentheticalPattern.ReplaceAllString(raw, " ")
	raw = strings.TrimSpace(strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_"))
	if raw == "" {
		return implementationSubject{}
	}
	raw = trimImplementationLeadingArticle(raw)
	display := trimImplementationTrailingStatusWords(raw)
	words := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	var content []string
	for _, word := range words {
		if implementationSubjectStopWords[word] {
			continue
		}
		content = append(content, singularImplementationComponent(word))
	}
	for len(content) > 0 && implementationTrailingStatusWords[content[len(content)-1]] {
		content = content[:len(content)-1]
	}
	if len(content) == 0 {
		return implementationSubject{}
	}
	for i := len(content) - 1; i >= 0; i-- {
		if implementationComponentNouns[content[i]] {
			return implementationSubject{Label: content[i], Name: display, Words: []string{content[i]}}
		}
	}
	label := strings.Join(content, "-")
	if strings.Contains(raw, "/") {
		label = strings.Trim(strings.ToLower(filepath.ToSlash(raw)), "/")
	}
	if label == "" {
		return implementationSubject{}
	}
	return implementationSubject{Label: label, Name: display, Words: content}
}

// trimImplementationTrailingStatusWords removes the grammar and status words
// that follow a component before the status marker. "The backend is" becomes
// "backend" in the review reason.
func trimImplementationTrailingStatusWords(raw string) string {
	fields := strings.Fields(raw)
	for len(fields) > 0 {
		last := strings.ToLower(strings.Trim(fields[len(fields)-1], " \t\r\n.,;:!?()[]{}<>\"'`*_"))
		if !implementationTrailingStatusWords[last] && !implementationSubjectStopWords[last] {
			break
		}
		fields = fields[:len(fields)-1]
	}
	return strings.TrimSpace(strings.Join(fields, " "))
}

// singularImplementationComponent maps a plural component noun to its singular
// form. The mapping is limited to the component vocabulary so an ordinary
// plural does not become a subject; an unknown word is returned unchanged for
// the kebab/path fallback.
func singularImplementationComponent(word string) string {
	if word == "" {
		return ""
	}
	if implementationComponentNouns[word] {
		return word
	}
	if implementationTrailingStatusWords[word] {
		return word
	}
	if strings.HasSuffix(word, "ies") && len(word) > 3 {
		candidate := word[:len(word)-3] + "y"
		if implementationComponentNouns[candidate] {
			return candidate
		}
	}
	if strings.HasSuffix(word, "s") && len(word) > 2 {
		candidate := word[:len(word)-1]
		if implementationComponentNouns[candidate] {
			return candidate
		}
	}
	return word
}

// trimImplementationLeadingArticle removes a determiner that carries no
// component identity.
func trimImplementationLeadingArticle(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, article) {
			return strings.TrimSpace(raw[len(article):])
		}
	}
	return raw
}

// implementationTokenIsStatus reports a kebab token that is part of the status
// vocabulary rather than a component name.
func implementationTokenIsStatus(token string) bool {
	switch strings.ToLower(token) {
	case "fully-implemented", "completely-implemented", "partially-implemented",
		"fully-functional", "feature-complete", "production-ready",
		"health-checks", "health-check", "mock-data", "mock-auth",
		"mock-backend", "mock-api", "server-computed":
		return true
	default:
		return false
	}
}

// implementationTokenIsFileName reports a kebab fragment that is part of a
// filename such as docker-compose.yml rather than a component coordinate.
func implementationTokenIsFileName(sentence, token string) bool {
	index := strings.Index(sentence, token)
	if index < 0 {
		return false
	}
	after := sentence[index+len(token):]
	if strings.HasPrefix(after, ".") {
		return true
	}
	before := sentence[:index]
	return strings.HasSuffix(before, "/") || strings.HasSuffix(before, `\`)
}

// implementationSubjectObserved reports a source read or source-scoped grep
// that covers the subject. The path is the primary coordinate; a single-word
// feature name may also be observed in the source text.
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
			text := strings.ToLower(outcome.Result.Text)
			switch subject.Label {
			case "frontend":
				if implementationPathUnderRoot(path, "src", "frontend", "client", "web", "ui", "app") {
					return true
				}
			case "backend":
				if implementationPathUnderRoot(path, "backend", "server", "api") {
					return true
				}
			}
			if strings.Contains(path, subject.Label) {
				return true
			}
			if len(subject.Words) == 1 && len(subject.Words[0]) >= 4 &&
				strings.Contains(text, subject.Words[0]) {
				return true
			}
			if len(subject.Words) > 1 &&
				implementationWordsInPath(path, subject.Words) {
				return true
			}
		}
	}
	return false
}

// implementationPathUnderRoot reports a path under one of the named roots.
func implementationPathUnderRoot(path string, roots ...string) bool {
	path = strings.Trim(strings.ToLower(filepath.ToSlash(path)), "/")
	if path == "" {
		return false
	}
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+"/") || strings.Contains(path, "/"+root+"/") {
			return true
		}
	}
	return false
}

// implementationWordsInPath reports a path that contains every word of a
// compound subject. It allows "product service" to match
// backend/internal/product/service.go without requiring the exact phrase.
func implementationWordsInPath(path string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(path, word) {
			return false
		}
	}
	return true
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
