package ai

import (
	"errors"
	"net/url"
	"strings"
)

// OHGO's OpenRouter application identity.
//
// OpenRouter attributes usage to an application through request headers, not
// through the website's HTML metadata: HTTP-Referer is the application's
// stable identifier, X-OpenRouter-Title its display name, and
// X-OpenRouter-Categories its marketplace categories. These are application
// constants — never prompts, session ids, workspace paths, or credentials.
//
// cli-agent is the documented category for terminal-based coding assistants.
// "agent-runtime" is not an OpenRouter category and is silently dropped.
const (
	// OpenRouterReferer is the canonical application URL.
	OpenRouterReferer = "https://ohgo.pizenlabs.dev/"
	// OpenRouterTitle is the application display name.
	OpenRouterTitle = "OHGO"
	// OpenRouterCategories is the recognized marketplace category, lowercase
	// and hyphen-separated as OpenRouter requires.
	OpenRouterCategories = "cli-agent"
)

// OpenRouterAgenticHarnessStep is the routing gate OpenRouter reports when a
// free endpoint is reserved for applications it recognizes as agentic
// harnesses. Attribution headers are necessary for identification but do not
// by themselves prove eligibility for this gate.
const OpenRouterAgenticHarnessStep = "Gate Free Endpoints by Agentic Harness"

// ErrAgenticHarnessRestriction classifies an OpenRouter 403 that failed the
// agentic-harness routing gate.
//
// It is a classification, not a claim about OHGO's registration state: the
// transport can prove what OpenRouter answered, never what OpenRouter has
// approved.
var ErrAgenticHarnessRestriction = errors.New("ai: OpenRouter free endpoint restricted to recognized agentic harnesses")

// OpenRouterAttribution returns the attribution headers for one OpenRouter
// request. A fresh map prevents a caller from mutating shared state.
func OpenRouterAttribution() map[string]string {
	return map[string]string{
		"HTTP-Referer":            OpenRouterReferer,
		"X-OpenRouter-Title":      OpenRouterTitle,
		"X-OpenRouter-Categories": OpenRouterCategories,
	}
}

// isOpenRouterEndpoint reports whether a base URL addresses OpenRouter itself.
//
// It exists for transports that receive only a URL, such as diagnostics, and
// must not attribute traffic to an unrelated custom endpoint.
func isOpenRouterEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}
