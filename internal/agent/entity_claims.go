package agent

import (
	"fmt"
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
	if codeClaimStopWords[strings.ToLower(identifier)] {
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

// codeTokenObserved reports an identifier occurrence on a code boundary. A
// plain substring test would let `Order` claim support from `NewOrder` and
// `NewOrder` claim support from `NewOrderView`.
func codeTokenObserved(text, token string) bool {
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
