package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Code-identifier claims are the narrowest form of implementation evidence a
// readable answer can assert: a named prop, field, parameter, or member of a
// named component/type. A model can invent one while every other sentence is
// correct, and the prior completion policy had no way to notice.
//
// The check is deliberately limited to identifiers the answer marks as code
// (`showDetails`) and to a run that actually observed source. It never judges
// whether the surrounding design is right; it only refuses to treat an
// identifier that no source observation contains as an established fact.

var codeClaimContextPattern = regexp.MustCompile(
	`(?i)\b(?:props?|fields?|attributes?|parameters?|arguments?|members?)\b`)

var codeClaimVerbPattern = regexp.MustCompile(
	`(?i)\b(?:accepts?|receives?|takes?|has|have|exposes?|supports?|includes?|adds?|passes?|sets?|reads?|uses?)\b`)

var backtickedIdentifierPattern = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]{2,})`")

var identifierTokenPattern = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]{2,}\b`)

// codeClaimStopWords are standard or grammar-like identifiers whose presence
// in prose does not identify a project-specific contract. A real project prop
// is not in this list; skipping these avoids reviewing a correct answer that
// mentions React's own vocabulary.
var codeClaimStopWords = map[string]bool{
	"children": true, "classname": true, "key": true, "ref": true,
	"id": true, "name": true, "type": true, "value": true, "data": true,
	"props": true, "state": true, "error": true, "context": true,
	"style": true, "title": true, "text": true, "url": true, "path": true,
	"config": true, "options": true, "items": true, "user": true,
	"post": true, "test": true, "file": true, "version": true,
	"size": true, "count": true, "index": true, "string": true,
	"number": true, "boolean": true, "null": true, "true": true, "false": true,
	"onclick": true, "onchange": true, "onsubmit": true, "onload": true,
	"href": true, "src": true, "alt": true, "class": true, "role": true,
	"label": true, "placeholder": true, "disabled": true, "readonly": true,
}

// codeClaimIssues audits code-identifier claims in one sentence.
func codeClaimIssues(sentence string, turns []Turn) []claimIssue {
	if claimIsBounded(sentence) || claimIsNonAssertive(sentence) ||
		!codeClaimContextPattern.MatchString(sentence) || !codeClaimVerbPattern.MatchString(sentence) {
		return nil
	}
	identifiers := backtickedIdentifierPattern.FindAllStringSubmatch(sentence, -1)
	if len(identifiers) == 0 {
		// A non-backticked prop/field name is accepted only in the immediate
		// "X prop" / "prop X" shape, so ordinary prose ("the field is large")
		// is not mistaken for a code contract.
		identifiers = adjacentIdentifierClaims(sentence)
	}
	if len(identifiers) == 0 {
		return nil
	}
	observations := sourceObservations(turns)
	if len(observations) == 0 {
		// A resumed answer may rely on evidence from an earlier session. No
		// current source observation means this run cannot refute the claim, so
		// it must not invent a review from absence.
		return nil
	}
	subject := codeClaimSubject(sentence, identifiers)
	var issues []claimIssue
	for _, match := range identifiers {
		identifier := strings.TrimSpace(match[1])
		if identifier == "" || codeClaimStopWords[strings.ToLower(identifier)] {
			continue
		}
		all := joinObservationText(observations)
		present := strings.Contains(all, identifier)
		if subject != "" {
			if block, found := declarationBlock(observations, subject); found {
				if !strings.Contains(block, identifier) {
					issues = append(issues, claimIssue{
						Code: "unsupported_code_claim",
						Detail: fmt.Sprintf(
							"the answer says %s has %q, but the observed declaration of %s does not contain it; verify the member or remove the claim",
							subject, identifier, subject),
					})
					continue
				}
				// The observed declaration contains the identifier, so the claim
				// has direct source support even if the rest of the file does
				// not repeat it.
				continue
			}
		}
		if !present {
			what := identifier
			if subject != "" {
				what = identifier + " on " + subject
			}
			issues = append(issues, claimIssue{
				Code: "unsupported_code_claim",
				Detail: fmt.Sprintf(
					"the answer asserts %s, but no source observation in this run contains %q; read the declaration or mark the claim unverified",
					what, identifier),
			})
		}
	}
	return issues
}

// adjacentIdentifierClaims extracts identifiers from the explicit
// "<identifier> prop" / "prop <identifier>" shape without backticks.
func adjacentIdentifierClaims(sentence string) [][]string {
	var out [][]string
	out = append(out, regexp.MustCompile(
		`(?i)\b(?:props?|fields?|attributes?|parameters?|arguments?)\s+(?:named\s+|called\s+)?([A-Za-z_][A-Za-z0-9_]{2,})\b`).
		FindAllStringSubmatch(sentence, -1)...)
	out = append(out, regexp.MustCompile(
		`\b([A-Za-z_][A-Za-z0-9_]{2,})\s+(?:prop|field|attribute|parameter|argument)\b`).
		FindAllStringSubmatch(sentence, -1)...)
	return out
}

// codeClaimSubject picks the component/type a code claim is about. It prefers
// the first PascalCase token that is not the asserted identifier; a lowercase
// identifier is never a component name.
func codeClaimSubject(sentence string, identifiers [][]string) string {
	excluded := map[string]bool{}
	for _, match := range identifiers {
		excluded[match[1]] = true
	}
	for _, token := range identifierTokenPattern.FindAllString(sentence, -1) {
		if excluded[token] || !containsUpper(token) || codeClaimStopWords[strings.ToLower(token)] {
			continue
		}
		if len(token) < 3 {
			continue
		}
		return token
	}
	return ""
}

// sourceObservations returns successful source-content outcomes.
func sourceObservations(turns []Turn) []ToolOutcome {
	var out []ToolOutcome
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || !sourceObservationOutcome(outcome) {
				continue
			}
			if strings.TrimSpace(outcome.Result.Text) == "" {
				continue
			}
			out = append(out, outcome)
		}
	}
	return out
}

// joinObservationText joins the observed source with a separator so a
// declaration cannot accidentally bridge two files.
func joinObservationText(observations []ToolOutcome) string {
	var b strings.Builder
	for _, outcome := range observations {
		b.WriteString(outcome.Result.Text)
		b.WriteString("\n\x00\n")
	}
	return b.String()
}

// declarationBlock finds a type/interface/class declaration for subject and
// returns its brace-balanced body. It is bounded so a malformed source file
// cannot turn the answer audit into a full-file parser.
func declarationBlock(observations []ToolOutcome, subject string) (string, bool) {
	if subject == "" {
		return "", false
	}
	names := []string{subject, subject + "Props"}
	for _, outcome := range observations {
		text := outcome.Result.Text
		for _, name := range names {
			pattern := regexp.MustCompile(`\b(?:interface|type|class|struct)\s+` + regexp.QuoteMeta(name) + `\b`)
			location := pattern.FindStringIndex(text)
			if location == nil {
				continue
			}
			open := strings.IndexByte(text[location[1]:], '{')
			if open < 0 {
				continue
			}
			start := location[1] + open
			if block, ok := balancedBlock(text, start, 4000); ok {
				return block, true
			}
		}
	}
	return "", false
}

// balancedBlock returns the brace-balanced block beginning at open.
func balancedBlock(text string, open, limit int) (string, bool) {
	if open < 0 || open >= len(text) || text[open] != '{' {
		return "", false
	}
	depth := 0
	for index := open; index < len(text) && index-open <= limit; index++ {
		switch text[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[open : index+1], true
			}
		}
	}
	return "", false
}

// containsUpper reports whether a token has an upper-case letter.
func containsUpper(value string) bool {
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}
