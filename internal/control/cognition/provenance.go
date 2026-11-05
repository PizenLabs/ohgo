package cognition

import (
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Provenance bounds. The terms are a bounded audit aid, not a second copy of
// the documentation.
const (
	// MaxDocumentationTerms bounds the documentation-only vocabulary sent to
	// completion.
	MaxDocumentationTerms = 48
	// MaxProvenanceValueBytes is how much of one retained observation is
	// tokenised for provenance. The full value remains in evidence.
	MaxProvenanceValueBytes = 8 * 1024
)

// Provenance summarizes what the evidence can truthfully support.
//
// It is forward-looking state for the completion boundary: the projector
// builds it from observations it already owns, and the completion evaluator
// uses it to keep a documentation claim from being presented as an observed
// implementation fact.
func (s *EvidenceState) Provenance(shape TaskShape) agent.EvidenceProvenance {
	summary := agent.EvidenceProvenance{
		RequireAttribution: shape.Kind == contract.ObjectiveInformational &&
			shape.BroadRepositoryAnalysis,
		ExternalRequired: shape.External.Required,
	}
	if s == nil {
		return summary
	}
	var documentation, implementation, external []string
	titles := map[string]bool{}
	for _, item := range s.Items {
		if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
			continue
		}
		switch item.Provenance {
		case ProvenanceDocumentation:
			summary.HasDocumentation = true
			documentation = append(documentation, item.Value)
			for _, term := range DocumentationTitleTerms(item.Value) {
				titles[term] = true
			}
		case ProvenanceExternal:
			if strings.TrimSpace(item.Value) != "" || strings.TrimSpace(item.Summary) != "" {
				summary.ExternalObserved = true
				external = append(external, item.Value)
			}
		case ProvenanceObservation:
			if isImplementationObservation(item) && strings.TrimSpace(item.Value) != "" {
				summary.HasImplementation = true
				implementation = append(implementation, item.Value)
			}
		case ProvenanceManifest:
			implementation = append(implementation, item.Value)
		}
	}
	// External content is not implementation, but a term confirmed there is
	// not documentation-only either. Subtract it so an answer grounded in a
	// successful external observation is not forced to attribute what it
	// actually observed.
	observed := termSet(append(append([]string(nil), implementation...), external...))
	// The workspace probe and module manifest establish the implementation
	// language even when the short token ("go", "js") is below the content-term
	// length floor. A language is an observed property of the repository, not a
	// documentation claim.
	if shape.RepositoryFacts.Go {
		observed["go"] = true
		observed["golang"] = true
	}
	if shape.RepositoryFacts.JavaScript {
		observed["javascript"] = true
		observed["typescript"] = true
		observed["js"] = true
		observed["ts"] = true
	}
	if shape.RepositoryFacts.Python {
		observed["python"] = true
	}
	if shape.RepositoryFacts.Rust {
		observed["rust"] = true
	}
	summary.DocumentationTerms = documentationOnlyTerms(documentation, observed, titles)
	return summary
}

// documentationOnlyTerms returns bounded specific terms that appear in
// documentation but nowhere in the observed implementation or external
// evidence. Generic nouns ("platform", "service") are not claims; proper names
// and concrete metrics are.
func documentationOnlyTerms(documentation []string, observed, titles map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range documentation {
		for _, term := range documentationClaimTerms(text) {
			if seen[term] || observed[term] || titles[term] {
				continue
			}
			seen[term] = true
			out = append(out, term)
			if len(out) >= MaxDocumentationTerms {
				return out
			}
		}
	}
	sort.Strings(out)
	return out
}

// DocumentationTitleTerms returns the content terms of a document's title: the
// first non-empty line when it looks like a heading rather than a sentence.
// The project's own name is identity, not an unverified implementation claim.
func DocumentationTitleTerms(text string) []string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		if len(line) > 120 || strings.ContainsAny(line, ".!?") {
			return nil
		}
		return ContentTerms(line)
	}
	return nil
}

// numericClaimPattern keeps a metric such as "99.9%" or "100+" comparable even
// though surrounding prose punctuation is not part of the claim.
var numericClaimPattern = regexp.MustCompile(`\d+(?:\.\d+)?[%+]?`)

// numericFragment reports whether a whitespace token contains only digits and
// metric punctuation. Such a token is already represented by the regex pass;
// admitting it again would split "99.9%" into a bogus "9" claim.
func numericFragment(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if (r < '0' || r > '9') && r != '%' && r != '+' {
			return false
		}
	}
	return true
}

// documentationClaimTerms returns the specific claim-shaped terms in a
// documentation text: proper-name tokens and numbers. Lower-case generic words
// are not documentation claims and would make the completion check noisy.
func documentationClaimTerms(text string) []string {
	if len(text) > MaxProvenanceValueBytes {
		text = text[:MaxProvenanceValueBytes]
	}
	seen := map[string]bool{}
	var out []string
	add := func(term string) {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		out = append(out, term)
	}
	for _, numeric := range numericClaimPattern.FindAllString(text, -1) {
		// A bare small integer is too ambiguous to be a documentation-only
		// claim: "18 entries present" and "React 18" are different facts that
		// share the token. Keep metrics and longer identifiers; the technology
		// name attached to a version remains the substantive claim.
		if len(numeric) < 3 && !strings.ContainsAny(numeric, ".%+") {
			continue
		}
		add(numeric)
	}
	for _, line := range strings.Split(text, "\n") {
		previous := ""
		for _, raw := range strings.Fields(splitTermText(line)) {
			token := strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_#|/-")
			if token == "" {
				previous = token
				continue
			}
			normalized := strings.ToLower(token)
			if !strings.Contains(token, ".") {
				normalized = stripVersionSuffix(normalized)
			}
			if len(normalized) < 2 || genericProvenanceTerm(normalized) {
				previous = token
				continue
			}
			numeric := false
			upper := false
			innerUpper := false
			allUpper := true
			for i, r := range token {
				if r >= '0' && r <= '9' {
					numeric = true
				}
				if r >= 'A' && r <= 'Z' {
					upper = true
					if i > 0 {
						innerUpper = true
					}
				} else {
					allUpper = false
				}
			}
			if !numeric && !upper {
				previous = token
				continue
			}
			// A first-letter capital is ambiguous: it may be a heading,
			// table, or sentence start ("Shopping"), not a proper name. It
			// becomes a claim when it appears mid-sentence after a lowercase
			// word ("uses Redis") or when its spelling itself is unmistakable
			// (PostgreSQL, RabbitMQ, gRPC, JWT, Go1.23). This keeps ordinary
			// domain nouns out of the documentation vocabulary.
			strong := innerUpper || numeric || (allUpper && upper)
			if !strong && startsLowerWord(previous) {
				strong = true
			}
			if !strong || (numeric && numericFragment(normalized)) {
				previous = token
				continue
			}
			add(normalized)
			previous = token
			if len(out) >= MaxDocumentationTerms*2 {
				return out
			}
		}
	}
	return out
}

// startsLowerWord reports whether a token begins with a lowercase ASCII
// letter. It is the context test for a mid-sentence proper name: "uses Redis"
// is a name, while "Redis" at the start of a heading is ambiguous.
func startsLowerWord(token string) bool {
	return token != "" && token[0] >= 'a' && token[0] <= 'z'
}

// termSet returns the content terms in the supplied texts.
func termSet(texts []string) map[string]bool {
	out := map[string]bool{}
	for _, text := range texts {
		for _, term := range ContentTerms(text) {
			out[term] = true
		}
	}
	return out
}

// ContentTerms returns lower-case content terms in text.
//
// It is deliberately the same shape the answer's ClaimTerms produces, so the
// two sides compare directly. Grammar words are excluded; the bound keeps a
// large documentation file from inflating the projection.
func ContentTerms(text string) []string {
	if len(text) > MaxProvenanceValueBytes {
		text = text[:MaxProvenanceValueBytes]
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Fields(splitTermText(text)) {
		token := strings.ToLower(strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_#|/-"))
		token = stripVersionSuffix(token)
		if len(token) < 3 || genericProvenanceTerm(token) || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
		if len(out) >= MaxDocumentationTerms*4 {
			return out
		}
	}
	return out
}

// stripVersionSuffix maps a dependency token such as "sqlite3" or "http2" to
// its identity stem. It only strips a suffix when a real word remains, so a
// numeric metric is untouched.
func stripVersionSuffix(token string) string {
	end := len(token)
	for end > 0 && token[end-1] >= '0' && token[end-1] <= '9' {
		end--
	}
	if end >= 3 && end < len(token) {
		return token[:end]
	}
	return token
}

// splitTermText separates path, package, and version punctuation so a manifest
// entry such as "github.com/gin-gonic/gin" contributes its identity tokens,
// not one opaque string.
func splitTermText(text string) string {
	return strings.NewReplacer(
		"/", " ", "\\", " ", "-", " ", ".", " ", "_", " ",
		"@", " ", ":", " ", "+", " ", "=", " ", "(", " ",
		")", " ", "[", " ", "]", " ", ",", " ", "\"", " ",
		"'", " ", "`", " ",
	).Replace(text)
}

// provenanceStopWords are grammar and generic words that carry no repository
// identity. They keep the documentation vocabulary focused on claims.
var provenanceStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true,
	"with": true, "from": true, "have": true, "has": true, "are": true,
	"was": true, "were": true, "will": true, "would": true, "can": true,
	"could": true, "should": true, "may": true, "might": true, "must": true,
	"not": true, "you": true, "your": true, "our": true, "its": true,
	"their": true, "they": true, "them": true, "these": true, "those": true,
	"there": true, "here": true, "when": true, "where": true, "which": true,
	"what": true, "who": true, "why": true, "how": true, "all": true,
	"any": true, "each": true, "more": true, "most": true, "some": true,
	"such": true, "than": true, "then": true, "into": true, "onto": true,
	"over": true, "under": true, "about": true, "after": true, "before": true,
	"between": true, "through": true, "during": true, "without": true,
	"within": true, "also": true, "only": true, "just": true, "very": true,
	"moreover": true, "however": true, "therefore": true, "because": true,
	"while": true, "using": true, "used": true, "use": true, "uses": true,
	"get": true, "got": true, "make": true, "made": true, "see": true,
	"run": true, "runs": true, "running": true, "build": true, "builds": true,
	"test": true, "tests": true, "file": true, "files": true, "path": true,
	"paths": true, "code": true, "project": true, "repository": true,
	"repo": true, "codebase": true, "system": true, "application": true,
	"app": true, "source": true, "docs": true, "documentation": true,
	"readme": true, "license": true, "version": true, "content": true,
	"contents": true, "page": true, "section": true, "example": true,
	"examples": true, "following": true, "provided": true, "provide": true,
	"provides": true, "available": true, "support": true, "supports": true,
	"supported": true, "feature": true, "features": true, "including": true,
	"include": true, "includes": true, "included": true, "based": true,
	"requires": true, "require": true, "required": true, "requirement": true,
	"requirements": true, "ensure": true, "ensures": true,
	"framework": true, "frameworks": true, "database": true, "databases": true,
	"config": true, "configs": true, "configuration": true, "architecture": true,
	"structure": true, "stack": true,
	"technology": true, "technologies": true, "library": true, "libraries": true,
	"dependency": true, "dependencies": true, "tool": true, "tools": true,
	"usage": true, "setup": true, "installation": true, "overview": true,
	"introduction": true, "getting": true, "started": true, "layer": true,
	"layers": true, "component": true, "components": true, "purpose": true,
	"design": true, "choices": true, "notable": true, "key": true, "api": true,
	"server": true, "service": true, "services": true, "module": true,
	"modules": true, "package": true, "packages": true,
	// Marketing adjectives and generic qualifiers are not claims about the
	// implementation even when a README capitalises them.
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
	"starter": true, "sample": true,
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

// genericProvenanceTerm reports whether a token is a grammar word, a generic
// project noun, or a plural of one. That keeps "APIs", "frameworks", and
// "tools" from being treated as specific claims about the implementation.
func genericProvenanceTerm(token string) bool {
	if provenanceStopWords[token] {
		return true
	}
	if strings.HasSuffix(token, "s") {
		return provenanceStopWords[strings.TrimSuffix(token, "s")]
	}
	if strings.HasSuffix(token, "es") {
		return provenanceStopWords[strings.TrimSuffix(token, "es")]
	}
	return false
}
