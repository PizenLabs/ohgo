package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Concept-level negative claims are the weakest conclusion an answer can make.
// A complete file inventory or one empty search result does not prove that a
// concept has no implementation: JWT, RabbitMQ, authentication, and caching can
// live under names a filename scan cannot see. Those conclusions therefore
// remain bounded to the inspected scope unless the runtime has a defensible
// proof mechanism, which this layer deliberately does not invent.
//
// The vocabulary is finite so ordinary prose is not mistaken for an absence
// conclusion. File and test absence stay in the existing negative-claim gate,
// which owns their stricter completeness rules.

var (
	// conceptNotImplementedPattern catches "X is not implemented/used".
	conceptNotImplementedPattern = regexp.MustCompile(
		`(?i)\b([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\s+(?:is|are|was|were)\s+not\s+(?:implemented|used|present|supported|configured|integrated)\b`)
	// conceptNoPattern catches "there is no X", "has no X", "contains no X".
	conceptNoPattern = regexp.MustCompile(
		`(?i)\b(?:there\s+(?:is|are)|has|have|contains?|includes?)\s+no\s+([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\b`)
	// conceptNoQualifiedPattern catches "no real/actual/working X". The
	// qualifier turns a list item into an existence conclusion ("it looks
	// present but is not"), which is the semantic absence this layer owns.
	conceptNoQualifiedPattern = regexp.MustCompile(
		`(?i)\bno\s+(?:real|actual|working|proper|native)\s+([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\b`)
	// conceptNoBareListPattern catches the head item of an absence list: "No
	// JWT, RabbitMQ client code, gRPC server". Only a leading item qualifies;
	// a bare "no X" inside prose stays with the inventory facts the survey
	// tests admit ("no Cache-Control", "no database").
	conceptNoBareListPattern = regexp.MustCompile(
		`(?i)^\s*(?:[-*•]\s*)?no\s+(?:real\s+|actual\s+|working\s+|proper\s+|native\s+)?` +
			`([a-z][a-z0-9+#.-]*)\s*,`)
	// conceptNoMechanismPattern catches a list item that denies an
	// implementation mechanism: "no RabbitMQ client code", "no gRPC server
	// wired up". A bare "no Cache-Control" or "no database" is an inventory
	// fact a bounded survey demonstrated, so it stays out.
	conceptNoMechanismPattern = regexp.MustCompile(
		`(?i)(?:^|[,;:]\s*|[-*•]\s+|\band\s+|\bbut\s+)no\s+([a-z][a-z0-9+#.-]*)` +
			`(?:\s+[a-z][a-z0-9+#.-]*){0,1}\s+(?:client|server|handler|implementation|integration|wiring|wired|code|worker|adapter)\b`)
	// conceptDoesNotUsePattern catches "does not use X" and "does not include X".
	conceptDoesNotUsePattern = regexp.MustCompile(
		`(?i)\b(?:does|do|did)\s+not\s+(?:use|implement|support|include|have)\s+([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\b`)
	// conceptUnusedPattern catches "X is unused/absent/missing".
	conceptUnusedPattern = regexp.MustCompile(
		`(?i)\b([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\s+(?:is|are|was|were)\s+(?:unused|absent|missing)\b`)
	// conceptNoIntegrationPattern catches "no X integration/implementation".
	conceptNoIntegrationPattern = regexp.MustCompile(
		`(?i)\bno\s+([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\s+(?:integration|implementation|support|usage)\b`)
	// conceptBareNoPattern catches a fragment that only names the absent
	// concept ("No authentication.").
	conceptBareNoPattern = regexp.MustCompile(
		`(?i)^\s*no\s+([a-z][a-z0-9+#.-]*(?:\s+[a-z][a-z0-9+#.-]*){0,2})\s*[.!]?\s*$`)
)

// conceptAbsenceVocabulary is the bounded set of repository concepts whose
// absence is a semantic conclusion rather than a file-existence fact.
var conceptAbsenceVocabulary = map[string]bool{
	"jwt": true, "oauth": true, "auth": true, "authentication": true,
	"authorization": true, "rabbitmq": true, "kafka": true, "redis": true,
	"memcached": true, "postgres": true, "postgresql": true, "mysql": true,
	"mongodb": true, "sqlite": true, "database": true, "db": true,
	"cache": true, "caching": true, "queue": true, "broker": true,
	"pubsub": true, "grpc": true, "graphql": true, "rest": true,
	"websocket": true, "telemetry": true, "metrics": true, "tracing": true,
	"monitoring": true, "logging": true, "payment": true, "payments": true,
	"notification": true, "notifications": true, "inventory": true,
	"search": true, "billing": true, "shipping": true, "email": true,
	"checkout": true, "idempotency": true, "health": true, "gateway": true,
	"test": false, // test absence is owned by the stricter negative gate.
}

// conceptAbsenceIssue reports an unbounded semantic absence conclusion.
func conceptAbsenceIssue(sentence string) *claimIssue {
	subject := conceptAbsenceSubject(sentence)
	if subject == "" {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_concept_absence",
		Detail: fmt.Sprintf(
			"the answer treats %q as absent without a scoped proof; a file inventory or one empty search cannot establish concept absence. Bound the statement to the inspected source or mark it unverified",
			subject),
	}
}

// conceptAbsenceSubject returns the concept a sentence claims is absent, or ""
// when the sentence is not a concept-absence conclusion.
func conceptAbsenceSubject(sentence string) string {
	for _, pattern := range []*regexp.Regexp{
		conceptNotImplementedPattern,
		conceptNoPattern,
		conceptNoQualifiedPattern,
		conceptNoBareListPattern,
		conceptNoMechanismPattern,
		conceptDoesNotUsePattern,
		conceptUnusedPattern,
		conceptNoIntegrationPattern,
		conceptBareNoPattern,
	} {
		for _, match := range pattern.FindAllStringSubmatchIndex(sentence, -1) {
			if len(match) < 4 || match[2] < 0 || match[3] < 0 {
				continue
			}
			subject := cleanConceptSubject(sentence[match[2]:match[3]])
			if conceptSubjectQualifies(subject) {
				return subject
			}
		}
	}
	return ""
}

// cleanConceptSubject trims qualifiers that are not part of the concept.
func cleanConceptSubject(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, " .,;:!?\"'`*_")
	for _, suffix := range []string{" integration", " implementation", " support", " usage"} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
		}
	}
	return value
}

// conceptSubjectQualifies reports a bounded concept, not a file or a test.
func conceptSubjectQualifies(value string) bool {
	if value == "" || strings.ContainsAny(value, "/\\") || strings.Contains(value, ".") {
		return false
	}
	switch value {
	case "test", "tests", "test file", "test files", "go test", "go tests",
		"readme", "readme.md", "documentation", "docs", "config",
		"configuration", "source file", "source files", "file", "files":
		return false
	}
	for _, word := range strings.FieldsFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if conceptAbsenceVocabulary[word] {
			return true
		}
	}
	return false
}
