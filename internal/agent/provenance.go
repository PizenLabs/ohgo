package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// AssessAnswerProvenance keeps documentation claims distinguishable from
// observed implementation facts.
//
// The check is deliberately narrow. It runs only for a broad repository
// account where the evidence contains documentation-only terms: a term that
// appears in a README or other documentation but nowhere in an observed source
// or structural fact. An answer sentence that names such a term as a proper
// name or a concrete metric is a documentation claim; it must say where the
// claim comes from ("the README describes ...", "according to the docs ...")
// rather than presenting it as an observed property of the implementation.
//
// It is a completion veto, never authority and never a substitute for reading
// the source. It does not judge whether an attributed claim is true; it only
// prevents an unattributed documentation claim from being silently promoted to
// a repository fact.
func AssessAnswerProvenance(intent contract.IntentContract, answer string, provenance EvidenceProvenance) contract.GroundingAssessment {
	assessment := contract.GroundingAssessment{Verdict: contract.GroundingPass}
	if intent.Kind != contract.ObjectiveInformational ||
		!provenance.RequireAttribution ||
		len(provenance.DocumentationTerms) == 0 {
		return assessment
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return assessment
	}
	documented := make(map[string]bool, len(provenance.DocumentationTerms))
	for _, term := range provenance.DocumentationTerms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" {
			documented[term] = true
		}
	}
	if len(documented) == 0 {
		return assessment
	}

	previous := ""
	attributedSection := false
	for _, line := range strings.Split(answer, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// A heading is a provenance boundary. "What the README claims" makes
		// the claims under it documentation-derived; the next heading without
		// an attribution ends that section. This is how a truthful analysis
		// separates documentation from observed implementation.
		if strings.HasPrefix(line, "#") {
			attributedSection = hasAttribution(line)
			previous = line
			continue
		}
		for _, sentence := range splitClaimSentences(line) {
			if attributedSection || hasAttribution(sentence) || hasAttribution(previous) || reportsAbsence(sentence) {
				previous = sentence
				continue
			}
			for _, term := range ClaimTerms(sentence) {
				if !documented[term] {
					continue
				}
				return provenanceReview(term)
			}
			// A lower-case technology or concept name can still be a specific
			// documentation claim. Content terms in the answer that only the
			// documentation contains are checked too, so "the project uses
			// redis" cannot bypass the rule by avoiding capitalisation.
			for _, raw := range strings.Fields(sentence) {
				term := strings.ToLower(strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_"))
				if len(term) < 4 || genericClaimTerm(term) || !documented[term] {
					continue
				}
				return provenanceReview(term)
			}
			previous = sentence
		}
	}
	return assessment
}

// provenanceReview builds the completion veto for one documentation-only term.
func provenanceReview(term string) contract.GroundingAssessment {
	return contract.GroundingAssessment{
		Verdict: contract.GroundingReview,
		Issues: []contract.GroundingIssue{{
			Code:   "unsupported_documentation_claim",
			Detail: "the answer presents documentation-only term " + term + " as an observed repository fact",
		}},
		Reasons: []string{
			"the answer presents documentation-only term " + term + " as an observed repository fact; attribute it to the documentation or verify it in the implementation",
		},
	}
}

// ClaimTerms returns the tokens in a sentence that can carry a specific
// documentation claim: proper-name-shaped words and concrete numeric metrics.
// Common sentence starters and grammar words are excluded so an ordinary
// sentence beginning with "The" is not treated as a claim about a product.
func ClaimTerms(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Fields(text) {
		token := strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_")
		if token == "" {
			continue
		}
		normalized := strings.ToLower(token)
		if genericClaimTerm(normalized) {
			continue
		}
		numeric := false
		upper := false
		for _, r := range token {
			if r >= '0' && r <= '9' {
				numeric = true
			}
			if r >= 'A' && r <= 'Z' {
				upper = true
			}
		}
		if numeric {
			// Keep the numeric shape, not the punctuation around it. A metric
			// such as "99.9%" and a version such as "v2" both identify a
			// concrete claim.
			normalized = strings.TrimRight(normalized, "%+")
			if len(normalized) < 2 {
				continue
			}
		} else if !upper || len(normalized) < 2 {
			continue
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
		if len(out) >= 32 {
			break
		}
	}
	return out
}

// splitClaimSentences splits an answer into sentence-sized units. Newlines are
// boundaries too: models often list claims one per line.
func splitClaimSentences(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if sentence := strings.TrimSpace(b.String()); sentence != "" {
			out = append(out, sentence)
		}
		b.Reset()
	}
	for _, r := range text {
		switch r {
		case '.', '!', '?', '\n':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

// reportsAbsence reports whether a sentence asserts that a documented claim is
// not present in the implementation. That is the opposite of promoting the
// claim to fact, so it is not a provenance violation.
func reportsAbsence(sentence string) bool {
	lower := strings.ToLower(sentence)
	lower = strings.NewReplacer("*", "", "_", "", "`", "").Replace(lower)
	for _, marker := range []string{
		"not present", "not found", "not implemented", "not in the repository",
		"not in this repository", "absent", "does not exist", "doesn't exist",
		"missing", "no evidence", "none of", "❌", "does not contain",
		"doesn't contain", "not supported", "not configured",
		"there is no", "there are no", "has no ", "have no ", "contains no ",
		"no longer", "is not ", "are not ",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// hasAttribution reports whether a sentence names the source of a claim.
func hasAttribution(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{
		"readme", "documentation", "documented", "docs",
		"according to", "described in", "stated in", "reported in",
		"project description", "repository description",
		"project says", "project states", "project claims", "project describes", "project reports",
		"repository says", "repository states", "repository claims", "repository describes", "repository reports",
		"claims to", "advertises", "marketing",
		"inferred", "i infer", "this suggests", "appears to", "likely", "probably",
		"not verified", "unverified", "cannot verify", "could not verify",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// genericClaimTerms are grammar words and ordinary sentence starters that do
// not identify a specific repository claim.
var genericClaimTerms = map[string]bool{
	"the": true, "this": true, "that": true, "these": true, "those": true,
	"a": true, "an": true, "and": true, "or": true, "but": true, "if": true,
	"when": true, "while": true, "after": true, "before": true, "then": true,
	"it": true, "its": true, "they": true, "their": true, "we": true, "our": true,
	"i": true, "you": true, "your": true, "in": true, "on": true, "at": true,
	"for": true, "from": true, "with": true, "by": true, "as": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true,
	"has": true, "have": true, "had": true, "do": true, "does": true, "did": true,
	"can": true, "could": true, "may": true, "might": true, "must": true,
	"should": true, "would": true, "will": true, "there": true, "here": true,
	"also": true, "however": true, "therefore": true, "overall": true,
	"project": true, "repository": true, "repo": true, "codebase": true,
	"application": true, "app": true, "system": true, "code": true,
	"file": true, "files": true, "source": true, "evidence": true,
	"observed": true, "answer": true, "grounded": true, "based": true,
	"uses": true, "use": true, "using": true, "includes": true, "include": true,
	"framework": true, "frameworks": true, "database": true, "databases": true,
	"config": true, "configs": true, "configuration": true, "architecture": true,
	"structure": true, "feature": true, "features": true, "stack": true,
	"technology": true, "technologies": true, "library": true, "libraries": true,
	"dependency": true, "dependencies": true, "tool": true, "tools": true,
	"usage": true, "setup": true, "installation": true, "overview": true,
	"introduction": true, "getting": true, "started": true, "layer": true,
	"layers": true, "component": true, "components": true, "purpose": true,
	"design": true, "choices": true, "notable": true, "key": true, "api": true,
	"server": true, "service": true, "services": true, "module": true,
	"modules": true, "package": true, "packages": true,
	"comprehensive": true, "modern": true, "simple": true, "fast": true,
	"powerful": true, "minimal": true, "minimalist": true, "clean": true,
	"easy": true, "robust": true, "scalable": true, "production": true,
	"ready": true, "lightweight": true, "flexible": true, "secure": true,
	"advanced": true, "complete": true, "full": true, "automatic": true,
	"high": true, "low": true, "best": true, "great": true, "good": true,
	"new": true, "real": true, "true": true, "custom": true, "smart": true,
	"efficient": true, "reliable": true, "maintainable": true,
	"extensible": true, "modular": true, "clear": true, "explicit": true,
	"typical": true, "common": true, "solid": true, "strong": true,
	"designed": true, "built": true, "driven": true, "focused": true,
	"oriented": true, "friendly": true, "seamless": true, "intuitive": true,
	"elegant": true, "beautiful": true, "nice": true, "excellent": true,
	"quality": true, "professional": true, "tested": true, "proven": true,
	"stable": true, "active": true, "current": true, "latest": true,
	"recent": true, "major": true, "minor": true, "primary": true,
	"secondary": true, "multiple": true, "various": true, "several": true,
	"different": true, "similar": true, "specific": true, "general": true,
	"detailed": true, "template": true, "templates": true, "pattern": true,
	"patterns": true, "tooling": true, "scaffold": true, "boilerplate": true,
	"starter": true, "example": true, "examples": true, "sample": true,
	"environment": true, "environments": true, "variable": true,
	"variables": true, "management": true, "development": true,
	"developer": true, "developers": true, "infrastructure": true,
	"deployment": true, "deployments": true, "organization": true,
	"separation": true, "concerns": true, "entry": true, "point": true,
	"endpoint": true, "endpoints": true, "route": true, "routes": true,
	"handler": true, "handlers": true, "middleware": true,
	"connection": true, "connections": true, "pool": true, "pooling": true,
	"shutdown": true, "timeout": true, "timeouts": true, "signal": true,
	"signals": true, "request": true, "requests": true, "response": true,
	"responses": true, "status": true, "body": true, "json": true,
	"cli": true, "command": true, "commands": true, "binary": true,
	"coverage": true, "reload": true, "cache": true, "token": true,
	"tokens": true, "auth": true, "authentication": true, "security": true,
	"logging": true, "error": true, "errors": true, "metrics": true,
	"health": true,
}

// genericClaimTerm reports whether a token is a grammar word, a generic
// project noun, or a plural of one.
func genericClaimTerm(token string) bool {
	if genericClaimTerms[token] {
		return true
	}
	if strings.HasSuffix(token, "s") {
		return genericClaimTerms[strings.TrimSuffix(token, "s")]
	}
	if strings.HasSuffix(token, "es") {
		return genericClaimTerms[strings.TrimSuffix(token, "es")]
	}
	return false
}
