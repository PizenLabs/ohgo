package agent

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Entity-to-evidence association is the general complement to the vocabulary
// checks. Those checks know a finite set of claim shapes ("validates", "calls",
// "publishes"); a model can invent a plausible identifier while using a verb
// outside that set. This layer does not try to enumerate predicates. It asks a
// question that holds for any factual sentence: did this run observe the code
// entity the answer names as code?
//
// Only explicit code spans are considered. Prose nouns, product names, and
// paths are owned by other checks; a path proves a coordinate exists, not that
// the identifier inside it was observed. The check is inert when the run has no
// observations, because absence of a baseline cannot refute a resumed answer.

var (
	// backtickedCodeEntity matches a code span that looks like one identifier
	// or a qualified identifier. A path, URL, or sentence is not an entity.
	backtickedCodeEntity = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*)`")

	// negativeEntityObservation matches an answer that names an entity in order
	// to report it was not found. Naming an unobserved symbol is the point of a
	// negative observation, so it must not be reviewed as an invented fact.
	negativeEntityObservation = regexp.MustCompile(
		`(?i)\b(?:not\s+found|not\s+observed|not\s+present|absent|missing|` +
			`could\s+not\s+find|couldn't\s+find|no\s+such|does\s+not\s+exist|doesn't\s+exist)\b`)

	// entityAbsenceClaim matches a negative existential conclusion about an
	// explicit code entity. It is the contradiction side of the association
	// check: a negative claim is only affordable when the entity was not
	// observed, and a positive observation refutes it.
	entityAbsenceClaim = regexp.MustCompile(
		`(?i)(?:` + "`" + `[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*` + "`" + `)\s+` +
			`(?:does\s+not|doesn't|is\s+not|isn't|was\s+not|wasn't)\s+` +
			`(?:exist|present|defined|declared|used|referenced|implemented)\b|` +
			`\b(?:there\s+(?:is|are)|contains?|has|have)\s+no\s+` + "`" +
			`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*` + "`")
)

// imperativeClaimStart identifies advice or a next step. An imperative sentence
// recommends an external API; it does not assert that the project contains it.
var imperativeClaimStart = regexp.MustCompile(
	`(?i)^\s*(?:use|call|prefer|wrap|replace|add|run|check|read|open|inspect|` +
		`try|avoid|remove|delete|rename|move|keep|make|ensure|verify|test)\b`)

// entityClaimIssues reports an explicit code identifier that no observation in
// this run contains.
func entityClaimIssues(sentence, observed string) []claimIssue {
	if observed == "" || claimIsBounded(sentence) || claimIsNonAssertive(sentence) ||
		claimIsDocumentationAttributed(sentence) || imperativeClaimStart.MatchString(sentence) {
		return nil
	}
	if match := entityAbsenceClaim.FindString(sentence); match != "" {
		for _, entity := range backtickedCodeEntity.FindAllStringSubmatch(match, -1) {
			if len(entity) < 2 {
				continue
			}
			if codeTokenObserved(observed, entity[1]) {
				return []claimIssue{{
					Code: "unsupported_entity_absence",
					Detail: fmt.Sprintf(
						"the answer says `%s` is absent, but an observation in this run contains it; correct the negative claim or bound it to the inspected scope",
						entity[1]),
					Claim:       entity[1],
					Category:    "absence",
					Requirement: "an observation covering the entity",
					Reference:   entity[1],
					Action:      "remove the negative claim or correct it against the observation",
				}}
			}
		}
		return nil
	}
	if negativeEntityObservation.MatchString(sentence) {
		return nil
	}
	seen := map[string]bool{}
	var issues []claimIssue
	for _, match := range backtickedCodeEntity.FindAllStringSubmatch(sentence, -1) {
		if len(match) < 2 {
			continue
		}
		identifier := strings.TrimSpace(match[1])
		if identifier == "" || seen[identifier] {
			continue
		}
		seen[identifier] = true
		if !entityLooksProjectSpecific(identifier) {
			continue
		}
		if codeTokenObserved(observed, identifier) {
			continue
		}
		issues = append(issues, claimIssue{
			Code: "unsupported_code_entity",
			Detail: fmt.Sprintf(
				"the answer names `%s` as code, but no observation in this run contains that identifier; read the declaration or remove the name",
				identifier),
			Claim:       identifier,
			Location:    claimLocation(sentence, identifier),
			Category:    "entity",
			Requirement: "the declaration of the named identifier",
			Reference:   identifier,
			Action:      "read the declaration or remove the identifier",
		})
	}
	return issues
}

// entityLooksProjectSpecific filters generic words and external coordinates.
// A lowercase bare word such as `error` is prose in a code span; a qualified
// or underscored name is a concrete code coordinate.
func entityLooksProjectSpecific(identifier string) bool {
	if strings.ContainsAny(identifier, "/\\") || strings.Contains(identifier, "://") {
		return false
	}
	// A filename is a path coordinate owned by the file/claim checks. A read
	// of `package.json` proves the file exists; it does not declare an
	// identifier named `package.json`, and reviewing it sends the model to
	// re-read a file it already observed.
	if fileCoordinateIdentifier(identifier) {
		return false
	}
	if codeClaimStopWords[strings.ToLower(identifier)] {
		return false
	}
	// The runtime state block prints evidence statuses in backticks. They are
	// the runtime's vocabulary, not identifiers the repository declares.
	if runtimeStatusIdentifier(identifier) {
		return false
	}
	if strings.ContainsAny(identifier, "._") {
		return true
	}
	for _, r := range identifier {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	// A lowercase bare identifier is too easily a generic word. The existing
	// code-claim check still owns prop/field claims for that shape.
	return false
}

// fileCoordinateIdentifier reports a name that denotes a file rather than a
// code declaration. The extension list is the finite set of source, manifest,
// and documentation formats the repository tools read.
func fileCoordinateIdentifier(identifier string) bool {
	if isRootFileName(identifier) {
		return true
	}
	switch strings.ToLower(path.Ext(identifier)) {
	case ".go", ".md", ".json", ".yaml", ".yml", ".toml", ".ts", ".tsx",
		".js", ".jsx", ".css", ".html", ".sql", ".sh", ".mod", ".sum",
		".lock", ".txt", ".xml":
		return true
	}
	return false
}

// runtimeStatusIdentifier reports an evidence-status token from the runtime
// state block. A model may quote `NOT_OBSERVED` or `PARTIAL` as a status; that
// is not a claim about a declaration.
func runtimeStatusIdentifier(identifier string) bool {
	return runtimeStatusIdentifiers[strings.ToUpper(identifier)]
}

var runtimeStatusIdentifiers = map[string]bool{
	"OBSERVED": true, "NOT_OBSERVED": true, "UNOBSERVED": true,
	"SUPPORTED": true, "UNSUPPORTED": true, "PARTIAL": true,
	"UNVERIFIED": true, "VERIFIED": true, "UNKNOWN": true,
	"KNOWN": true, "CURRENT": true, "STALE": true, "PENDING": true,
	"PARSED": true, "UNPARSED": true, "FAILED": true, "COMPLETE": true,
	"INCOMPLETE": true, "SUFFICIENT": true, "INSUFFICIENT": true,
	"BLOCKED": true, "NOT_QUERIED": true, "NOT_APPLICABLE": true,
	"TRUNCATED": true, "INVENTORY": true,
}

// codeTokenObserved reports an identifier occurrence on a code boundary. A
// plain substring test would let `Order` claim support from `NewOrder` and
// `NewOrder` claim support from `NewOrderView`.
func codeTokenObserved(text, token string) bool {
	if tokenObservedBoundary(text, token) {
		return true
	}
	// A package-qualified reference names a declaration by its package, not
	// its source spelling: `checkout.New` is the `New` declared in
	// `package checkout`. The observed file contains `func New(`, so the
	// literal `checkout.New` never appears even though the run read exactly
	// the declaration the answer names. The qualifier must be declared in the
	// same observation, so a bare `New` from another package cannot satisfy it.
	if dot := strings.LastIndexByte(token, '.'); dot > 0 && dot < len(token)-1 {
		qualifier, member := token[:dot], token[dot+1:]
		if packageDeclared(text, qualifier) && tokenObservedBoundary(text, member) {
			return true
		}
		if typeMemberDeclared(text, qualifier, member) {
			return true
		}
	}
	// An import path can qualify a reference the source writes with only its
	// final package segment. The answer names `cart.domain.Product`; the read
	// file imports `.../cart/domain` and uses `domain.Product`. Requiring the
	// literal dotted token reviewed a source-verified declaration as invented.
	// The suffix must be observed, and the removed prefix must name a path
	// segment in the same observation so an unrelated `domain.Product` cannot
	// support an invented package.
	segments := strings.Split(token, ".")
	for index := 1; index < len(segments)-1; index++ {
		suffix := strings.Join(segments[index:], ".")
		if !tokenObservedBoundary(text, suffix) {
			continue
		}
		prefix := strings.Join(segments[:index], "/")
		if tokenObservedBoundary(text, prefix+"/"+segments[index]) {
			return true
		}
	}
	return false
}

// typeMemberDeclared reports a member declared on the named type: an interface
// method inside `type T interface { … }`, or a method whose receiver is T.
// `StockReserver.Reserve` and `httpHandler.Checkout` are how an answer names
// the declaration; the source spells them `Reserve(ctx …)` inside the interface
// and `func (handler httpHandler) Checkout(…)`.
func typeMemberDeclared(text, qualifier, member string) bool {
	if qualifier == "" || member == "" {
		return false
	}
	receiver := regexp.MustCompile(`\bfunc\s*\([^)]*\b` + regexp.QuoteMeta(qualifier) + `\)\s*` + regexp.QuoteMeta(member) + `\b`)
	if receiver.MatchString(text) {
		return true
	}
	marker := "type " + qualifier + " interface"
	offset := 0
	for {
		index := strings.Index(text[offset:], marker)
		if index < 0 {
			return false
		}
		start := offset + index + len(marker)
		end := strings.IndexByte(text[start:], '}')
		if end < 0 {
			return false
		}
		block := text[start : start+end]
		if tokenObservedBoundary(block, member) {
			return true
		}
		offset = start + end + 1
	}
}

// tokenObservedBoundary reports a literal identifier occurrence that is not
// part of a longer identifier.
func tokenObservedBoundary(text, token string) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], token)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(token)
		offset = start + 1
		if start > 0 && codeIdentByte(text[start-1]) {
			continue
		}
		if end < len(text) && codeIdentByte(text[end]) {
			continue
		}
		return true
	}
	return false
}

// packageDeclared reports a `package <name>` clause in observed source. The
// read tool renders line numbers, so a numeric gutter is tolerated.
func packageDeclared(text, name string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "0123456789 \t")
		if !strings.HasPrefix(line, "package ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "package "))
		if len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

func codeIdentByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// successfulObservationText joins every successful outcome the run actually
// obtained. Reused observations stay in the evidence: reuse means the fact was
// already current, not that it was never observed.
func successfulObservationText(turns []Turn) string {
	var builder strings.Builder
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.Result.Rejected() {
				continue
			}
			if strings.TrimSpace(outcome.Result.Text) == "" {
				continue
			}
			builder.WriteString(outcome.Result.Text)
			builder.WriteString("\n\x00\n")
		}
	}
	return builder.String()
}
