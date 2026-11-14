package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Dependency-use claims have their own strength ladder:
//
//	DECLARED          a manifest or configuration names the dependency
//	IMPORTED          source imports it
//	INITIALIZED       source constructs a client or opens a connection
//	USED_IN_SOURCE    source performs a concrete operation with it
//	OBSERVED_AT_RUNTIME  behavior was observed while the project ran
//
// The ladder is not decoration. A `go.mod` entry, a Compose image, an import,
// a constructed client, and an executed operation are different observations.
// An answer that says "uses Redis for caching" needs the operation level and
// evidence bound to the component it names; reading product-service proves
// nothing about order-service.

type dependencyLevel int

const (
	dependencyNone dependencyLevel = iota
	dependencyDeclared
	dependencyImported
	dependencyInitializedLevel
	dependencyUsed
	dependencyRuntime
)

// dependencyObservationPhrase describes the strongest observation the run holds.
// The review is read by a person, so a level label alone ("NOT_OBSERVED") does
// not say what is missing.
func dependencyObservationPhrase(level dependencyLevel) string {
	switch level {
	case dependencyDeclared:
		return "the run observed only a declaration"
	case dependencyImported:
		return "the run observed only an import"
	case dependencyInitializedLevel:
		return "the run observed only an initialized client"
	case dependencyUsed:
		return "the run observed a source operation but no runtime behavior"
	case dependencyRuntime:
		return "the run observed runtime behavior"
	default:
		return "the run observed no declaration, import, initialization, or source use"
	}
}

// dependencySpec is one bounded technology identity and its common package
// aliases. The list is data, not policy: it lets a claim and its evidence name
// the same dependency without turning the audit into a semantic engine.
type dependencySpec struct {
	name    string
	aliases []string
}

var dependencyVocabulary = []dependencySpec{
	{name: "redis", aliases: []string{"redis", "go-redis", "ioredis"}},
	{name: "rabbitmq", aliases: []string{"rabbitmq", "amqp091", "amqp"}},
	{name: "kafka", aliases: []string{"kafka", "sarama", "segmentio/kafka"}},
	{name: "postgresql", aliases: []string{"postgresql", "postgres", "pgx", "lib/pq"}},
	{name: "mysql", aliases: []string{"mysql", "go-sql-driver/mysql"}},
	{name: "mongodb", aliases: []string{"mongodb", "mongo-driver", "mongoose"}},
	{name: "sqlite", aliases: []string{"sqlite", "sqlite3"}},
	{name: "jwt", aliases: []string{"jwt", "jsonwebtoken", "jose", "golang-jwt"}},
	{name: "oauth", aliases: []string{"oauth", "oauth2"}},
	{name: "grpc", aliases: []string{"grpc"}},
	{name: "graphql", aliases: []string{"graphql", "gqlgen", "apollo"}},
	{name: "elasticsearch", aliases: []string{"elasticsearch", "opensearch"}},
	{name: "gorm", aliases: []string{"gorm"}},
	{name: "prisma", aliases: []string{"prisma"}},
	{name: "react", aliases: []string{"react", "react-dom"}},
	{name: "axios", aliases: []string{"axios"}},
}

var (
	// dependencyDeclaredVerbPattern reports a declaration/configuration claim
	// rather than an operational one.
	dependencyDeclaredVerbPattern = regexp.MustCompile(
		`(?i)\b(?:declares?|declared|configured|configures|lists?|listed|includes?\s+as\s+a\s+dependency|dependency)\b`)
	// dependencyImportVerbPattern reports an import claim.
	dependencyImportVerbPattern = regexp.MustCompile(`(?i)\b(?:imports?|imported|requires?\s+the\s+package)\b`)
	// dependencyInitializeVerbPattern reports client creation.
	dependencyInitializeVerbPattern = regexp.MustCompile(
		`(?i)\b(?:initiali[sz]es?|creates?|opens?|instantiates?)\b`)
	// dependencyRuntimeVerbPattern reports a runtime observation claim.
	dependencyRuntimeVerbPattern = regexp.MustCompile(
		`(?i)\b(?:at\s+runtime|observed\s+(?:at\s+)?runtime|while\s+running|running\s+service|runtime\s+observation)\b`)
	// dependencyUseVerbPattern reports an operational use claim.
	dependencyUseVerbPattern = regexp.MustCompile(
		`(?i)\b(?:uses?|used|using|relies?\s+on|depends?\s+on|connects?\s+to|caches?|publishes?|consumes?|subscribes?|produces?|writes?|reads?|stores?|queries?|integrates?\s+with|calls?|sends?)\b`)
	// dependencyNegativePattern reports an absence form. It is handled by the
	// concept-absence audit, not by the positive use ladder.
	dependencyNegativePattern = regexp.MustCompile(
		`(?i)\b(?:does|do|did)\s+not\s+(?:use|import|include|support)|(?:is|are|was|were)\s+not\s+(?:used|imported|implemented)|unused\b`)
)

// dependencyClaimIssues returns every unsupported dependency-strength claim in
// one sentence.
func dependencyClaimIssues(sentence string, turns []Turn) []claimIssue {
	if dependencyNegativePattern.MatchString(sentence) || speculationPattern.MatchString(sentence) {
		return nil
	}
	lower := strings.ToLower(sentence)
	var out []claimIssue
	for _, spec := range dependencyVocabulary {
		index := dependencyMentionIndex(sentence, spec)
		if index < 0 {
			continue
		}
		required := dependencyRequiredLevel(sentence, index)
		if required == dependencyNone || !dependencyVerbInClause(sentence, index) {
			continue
		}
		subject := dependencySubject(sentence, index, spec)
		observed, coordinator := dependencyObservedLevel(turns, subject, spec)
		if observed >= required {
			continue
		}
		claim := boundedClaim(dependencyClaimPhrase(sentence, index, spec))
		where := "the inspected evidence"
		if subject != "" {
			where = subject
		}
		detail := fmt.Sprintf(
			"the answer claims %q for %s, but %s; a declaration, import, or initialized client is not the same as a source operation. Verify the operation or use the weaker wording",
			claim, where, dependencyObservationPhrase(observed))
		if coordinator != "" {
			detail += " (observed at " + coordinator + ")"
		}
		out = append(out, claimIssue{Code: "unsupported_dependency_claim", Detail: detail})
	}
	if len(out) == 0 && strings.Contains(lower, "caching") {
		// "Redis caching" is a use claim even when the verb is a noun phrase.
		for _, spec := range dependencyVocabulary {
			index := dependencyMentionIndex(sentence, spec)
			if index < 0 {
				continue
			}
			subject := dependencySubject(sentence, index, spec)
			observed, _ := dependencyObservedLevel(turns, subject, spec)
			if observed >= dependencyUsed {
				break
			}
			return []claimIssue{{
				Code: "unsupported_dependency_claim",
				Detail: fmt.Sprintf(
					"the answer claims %q, but %s; caching requires a concrete cache operation in the component's source",
					boundedClaim(dependencyClaimPhrase(sentence, index, spec)), dependencyObservationPhrase(observed)),
			}}
		}
	}
	return out
}

// dependencyVerbInClause reports whether the dependency mention and a claim
// verb share a clause. A sentence that lists several technologies and then
// describes behavior elsewhere is several claims; the mention alone must not
// inherit a verb from another clause.
func dependencyVerbInClause(sentence string, mention int) bool {
	start := mention
	for start > 0 && !strings.ContainsRune(";:,\n", rune(sentence[start-1])) {
		start--
	}
	end := mention
	for end < len(sentence) && !strings.ContainsRune(";:,\n", rune(sentence[end])) {
		end++
	}
	clause := strings.ToLower(sentence[start:end])
	for _, set := range []*regexp.Regexp{
		dependencyDeclaredVerbPattern,
		dependencyImportVerbPattern,
		dependencyInitializeVerbPattern,
		dependencyRuntimeVerbPattern,
		dependencyUseVerbPattern,
	} {
		if set.MatchString(clause) {
			return true
		}
	}
	return false
}

// dependencyRequiredLevel maps the claim wording to the evidence strength it
// requires.
func dependencyRequiredLevel(sentence string, mention int) dependencyLevel {
	lower := strings.ToLower(sentence)
	switch {
	case dependencyNegativePattern.MatchString(sentence):
		return dependencyNone
	case dependencyRuntimeVerbPattern.MatchString(lower):
		return dependencyRuntime
	case dependencyImportVerbPattern.MatchString(lower):
		return dependencyImported
	case dependencyInitializeVerbPattern.MatchString(lower):
		return dependencyInitializedLevel
	case dependencyUseVerbPattern.MatchString(lower):
		return dependencyUsed
	case dependencyDeclaredVerbPattern.MatchString(lower):
		return dependencyDeclared
	default:
		// A bare dependency name in a status sentence is not a use claim; the
		// implementation-status audit owns that shape.
		_ = mention
		return dependencyNone
	}
}

// dependencySubject returns the component a dependency claim names, from
// before the dependency mention or from an explicit "by/in/for <component>"
// phrase after it.
func dependencySubject(sentence string, mention int, spec dependencySpec) string {
	if subject := ComponentSubject(sentence, mention); subject != "" &&
		!dependencyNameMatchesSubject(spec, subject) {
		return subject
	}
	after := sentence[mention:]
	lower := strings.ToLower(after)
	for _, preposition := range []string{" by ", " in ", " for ", " within "} {
		if index := strings.Index(lower, preposition); index >= 0 {
			phrase := after[index+len(preposition):]
			if subject := ComponentSubject(phrase, len(phrase)); subject != "" &&
				!dependencyNameMatchesSubject(spec, subject) {
				return subject
			}
		}
	}
	return ""
}

// dependencyNameMatchesSubject prevents "Redis uses ..." from treating the
// dependency itself as the component.
func dependencyNameMatchesSubject(spec dependencySpec, subject string) bool {
	subject = strings.ToLower(subject)
	for _, alias := range spec.aliases {
		if strings.Contains(subject, strings.ToLower(alias)) {
			return true
		}
	}
	return false
}

// dependencyObservedLevel returns the strongest evidence for the dependency in
// the named component, plus a bounded coordinate for the review reason.
func dependencyObservedLevel(turns []Turn, subject string, spec dependencySpec) (dependencyLevel, string) {
	level := dependencyNone
	coordinator := ""
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			path := outcomePath(outcome)
			if subject != "" && path != "" && !ComponentMatchesPath(subject, path) {
				continue
			}
			if subject != "" && path == "" && outcome.Result.Runtime != nil &&
				!ComponentMatchesText(subject, outcome.Result.Runtime.Command) &&
				!ComponentMatchesText(subject, outcome.Result.Runtime.URL) {
				continue
			}
			if outcome.Result.Runtime != nil && dependencyRuntimeText(outcome, spec) {
				if dependencyRuntime > level {
					level = dependencyRuntime
					coordinator = firstNonEmptyString(outcome.Result.Runtime.URL, outcome.Result.Runtime.Command, path)
				}
				continue
			}
			switch ClassifyPath(path) {
			case PathDocumentation, PathGenerated:
				continue
			case PathManifest, PathConfiguration:
				if dependencyTextMentions(outcome.Result.Text, spec) {
					if dependencyDeclared > level {
						level = dependencyDeclared
						coordinator = path
					}
				}
				continue
			}
			if !sourceObservationOutcome(outcome) {
				continue
			}
			text := strings.ToLower(outcome.Result.Text)
			if !dependencyTextMentions(text, spec) {
				continue
			}
			if dependencyImportObserved(text, spec) && dependencyImported > level {
				level = dependencyImported
				coordinator = path
			}
			if dependencyClientInitialized(text, spec) {
				if dependencyInitializedLevel > level {
					level = dependencyInitializedLevel
					coordinator = path
				}
				if dependencyOperationObserved(text) {
					level = dependencyUsed
					coordinator = path
				}
			}
		}
	}
	return level, coordinator
}

// dependencyRuntimeText reports a live runtime whose output names the
// dependency and a concrete operation or readiness signal.
func dependencyRuntimeText(outcome ToolOutcome, spec dependencySpec) bool {
	if outcome.Result.Runtime == nil {
		return false
	}
	text := strings.ToLower(outcome.Result.Text + " " + outcome.Result.Runtime.Command + " " + outcome.Result.Runtime.URL)
	if !dependencyTextMentions(text, spec) {
		return false
	}
	for _, marker := range []string{"ping", "ready", "connected", "operation", "ok"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return dependencyOperationObserved(text)
}

// dependencyTextMentions reports a whole-token alias mention.
func dependencyTextMentions(text string, spec dependencySpec) bool {
	lower := strings.ToLower(text)
	for _, alias := range spec.aliases {
		if strings.Contains(lower, strings.ToLower(alias)) {
			return true
		}
	}
	return false
}

// dependencyImportObserved reports a language-shaped import/require statement.
func dependencyImportObserved(text string, spec dependencySpec) bool {
	if !dependencyTextMentions(text, spec) {
		return false
	}
	for _, marker := range []string{
		"import ", "import(", "from \"", "from '", "require(", "require \"",
		"#include", "using ", "use ",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// dependencyClientInitialized reports a constructor, dial, open, or new client.
func dependencyClientInitialized(text string, spec dependencySpec) bool {
	for _, alias := range spec.aliases {
		alias = strings.ToLower(alias)
		for _, marker := range []string{
			"newclient", "createclient", "newpool", "dial(", "open(", "connect(",
			alias + ".new", "new " + alias, "new(" + alias,
		} {
			if strings.Contains(text, marker) {
				return true
			}
		}
	}
	return false
}

// dependencyOperationObserved reports a concrete method or function operation.
func dependencyOperationObserved(text string) bool {
	for _, marker := range []string{
		".set(", ".get(", ".hset(", ".hget(", ".lpush(", ".rpush(",
		".publish(", ".subscribe(", ".consume(", ".produce(", ".send(",
		".query(", ".queryrow(", ".exec(", ".insert(", ".update(",
		".delete(", ".find(", ".sign(", ".signedstring(", ".verify(",
		".parse(", ".open(", ".dial(", ".do(", ".post(", ".put(",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// dependencyMentionIndex returns the byte index of a whole-token dependency
// mention, or -1. A mention inside a path or filename is not a dependency
// claim.
func dependencyMentionIndex(sentence string, spec dependencySpec) int {
	lower := strings.ToLower(sentence)
	best := -1
	for _, alias := range spec.aliases {
		alias = strings.ToLower(alias)
		start := 0
		for {
			index := strings.Index(lower[start:], alias)
			if index < 0 {
				break
			}
			index += start
			before := byte(0)
			if index > 0 {
				before = lower[index-1]
			}
			after := byte(0)
			if end := index + len(alias); end < len(lower) {
				after = lower[end]
			}
			if !dependencyTokenByte(before) && !dependencyTokenByte(after) &&
				!dependencyMentionInPath(lower, index, alias) {
				if best < 0 || index < best {
					best = index
				}
				break
			}
			start = index + len(alias)
		}
	}
	return best
}

// dependencyTokenByte reports a character that continues an identifier. A
// match adjacent to one is part of a larger name, not a dependency. Path
// punctuation is handled separately so a sentence-final period does not hide a
// mention.
func dependencyTokenByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') ||
		value == '_' || value == '-'
}

// dependencyMentionInPath reports a mention inside a path, filename, or URL.
func dependencyMentionInPath(lower string, index int, alias string) bool {
	start := index
	for start > 0 && !strings.ContainsRune(" \t\r\n\"'`()[]{}<>;:,", rune(lower[start-1])) {
		start--
	}
	end := index + len(alias)
	for end < len(lower) && !strings.ContainsRune(" \t\r\n\"'`()[]{}<>;:,", rune(lower[end])) {
		end++
	}
	token := strings.Trim(lower[start:end], ".")
	if strings.ContainsAny(token, "/\\") {
		return true
	}
	return strings.Contains(token, ".") && !strings.EqualFold(token, alias)
}

// dependencyClaimPhrase returns a short readable phrase around the mention.
func dependencyClaimPhrase(sentence string, index int, spec dependencySpec) string {
	start := index - 40
	if start < 0 {
		start = 0
	}
	// Never begin inside a word. Session 322 quoted the claim as "T API under
	// `/api/v1`, …": the 40-byte window started mid-way through "REST", which
	// reads as a typo and hides what the sentence actually claimed. Walk back
	// to the start of that word so the quote keeps it.
	for start > 0 && !claimPhraseBoundary(sentence[start-1]) {
		start--
	}
	end := index + len(spec.name) + 60
	if end > len(sentence) {
		end = len(sentence)
	}
	// The same rule applies to the tail: finish the word instead of cutting it
	// in half. boundedClaim bounds the display length.
	for end < len(sentence) && !claimPhraseBoundary(sentence[end]) {
		end++
	}
	return strings.TrimSpace(sentence[start:end])
}

// claimPhraseBoundary reports a byte that separates words in a quoted claim.
func claimPhraseBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}
