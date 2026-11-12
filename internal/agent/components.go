package agent

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// parenthesesPattern removes a qualifier such as "(health checks only)"
	// before a component list is read. It qualifies a claim; it is not a
	// subject.
	parenthesesPattern = regexp.MustCompile(`\([^)]*\)`)
	// kebabComponentPattern extracts a concrete component coordinate such as
	// "product-service" or "api-gateway".
	kebabComponentPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]*(?:-[a-z0-9]+)+\b`)
	// componentListSeparator splits an explicit component list.
	componentListSeparator = regexp.MustCompile(`(?i),\s*|\s+and\s+`)
)

// Component identity is the coordinate an evidence-bound claim is about.
//
// A source read of product-service does not establish anything about
// order-service, and a file named api.go inside cart/ is not the API gateway.
// The identity below is derived from the path only: the enclosing project
// structure names the component, never an arbitrary substring and never the
// model's prose. When a path is ambiguous the derivation returns the most
// specific honest label, and callers must treat a mismatch as unobserved
// rather than guessing.
//
// The rules are deterministic and shared by the claim audit and the state
// projection, so the model's pre-answer evidence classes and the completion
// review cannot disagree about which component an observation covers.

// componentContainerSegments organize code but do not name a component.
var componentContainerSegments = map[string]bool{
	"backend": true, "backends": true, "server": true, "servers": true,
	"src": true, "source": true, "internal": true, "cmd": true,
	"pkg": true, "packages": true, "package": true, "module": true,
	"modules": true, "services": true, "apps": true, "app": true,
	"frontend": true, "frontends": true, "web": true, "ui": true,
	"client": true, "clients": true, "docker": true, "deploy": true,
	"deployment": true, "k8s": true, "helm": true, "charts": true,
	"scripts": true, "monitoring": true, "migrations": true,
	"proto": true,
}

// componentLeafSegments are package or file roles nested inside a component.
// Walking backwards finds the owning component instead of the role directory:
// backend/internal/order/service/service.go belongs to order, not "service".
var componentLeafSegments = map[string]bool{
	"service": true, "services": true, "handler": true, "handlers": true,
	"controller": true, "controllers": true, "repository": true,
	"repositories": true, "repo": true, "domain": true, "model": true,
	"models": true, "dto": true, "dtos": true, "usecase": true,
	"usecases": true, "config": true, "configs": true, "middleware": true,
	"middlewares": true, "router": true, "routers": true, "routes": true,
	"main": true, "index": true, "server": true, "app": true,
	"application": true, "utils": true, "util": true, "lib": true,
	"shared": true, "common": true, "types": true, "hooks": true,
	"contexts": true, "context": true, "pages": true, "components": true,
	"component": true, "store": true, "stores": true, "styles": true,
	"assets": true, "public": true, "static": true, "test": true,
	"tests": true, "__tests__": true, "fixtures": true, "testdata": true,
	"docs": true, "doc": true, "examples": true, "example": true,
	"build": true, "dist": true, "target": true, "vendor": true,
	"node_modules": true, "bin": true, "cmd": true, "pkg": true,
}

// componentFrontendAliases canonicalize frontend roots that name the same
// surface.
var componentFrontendAliases = map[string]bool{
	"frontend": true, "client": true, "web": true, "ui": true,
	"storefront": true,
}

// ComponentFromPath returns the most specific component coordinate a
// repository path belongs to, or "" when the path names no component (a root
// manifest or README).
func ComponentFromPath(path string) string {
	raw := strings.TrimSpace(path)
	normalized := strings.Trim(strings.ToLower(filepath.ToSlash(raw)), "/")
	normalized = strings.TrimPrefix(normalized, "./")
	if normalized == "" || normalized == "." {
		return ""
	}
	segments := strings.Split(normalized, "/")
	// An absolute path includes the user's directory layout. Start at the
	// innermost known project root so the enclosing folder name does not
	// become the component.
	if filepath.IsAbs(raw) {
		for i := len(segments) - 1; i >= 0; i-- {
			if componentRootSegment(segments[i]) {
				segments = segments[i:]
				break
			}
		}
	}
	// Drop a file segment. A dotted name is a file; main.go and index.ts are
	// also files even when the extension was normalized away by the caller.
	last := segments[len(segments)-1]
	if strings.Contains(last, ".") || componentFileStem(last) {
		segments = segments[:len(segments)-1]
	}
	if len(segments) == 0 {
		return ""
	}
	// A frontend root is one component even when the path continues through
	// pages/components. The model may still name a page, but the component is
	// the frontend surface.
	first := segments[0]
	if componentFrontendAliases[first] || first == "src" {
		return "frontend"
	}
	// services/<name>/... and apps/<name>/... are explicit component roots.
	if first == "services" || first == "apps" || first == "packages" {
		if len(segments) > 1 && !componentContainerSegments[segments[1]] &&
			!componentLeafSegments[segments[1]] {
			return canonicalComponent(segments[1])
		}
	}
	// Walk backwards through the path. The first meaningful segment is the
	// component; container and role segments are skipped.
	for i := len(segments) - 1; i >= 0; i-- {
		segment := segments[i]
		if segment == "" || segment == "." {
			continue
		}
		if segment == "api" {
			// "api" is a legitimate component only when it is an explicit
			// root directory (backend/api/...), not a file name or a role
			// segment. Since file names were already dropped, retain it.
			if i == 0 || componentContainerSegments[segments[i-1]] ||
				segments[i-1] == "backend" || segments[i-1] == "server" {
				return canonicalComponent(segment)
			}
			continue
		}
		if componentLeafSegments[segment] || componentContainerSegments[segment] {
			continue
		}
		return canonicalComponent(segment)
	}
	if first == "backend" || first == "server" {
		return "backend"
	}
	return ""
}

// componentRootSegment reports a repository root marker. An absolute path is
// processed from the innermost marker, because the folders above it are the
// user's filesystem, not project structure.
func componentRootSegment(segment string) bool {
	switch segment {
	case "backend", "backend-go", "server", "src", "frontend", "web", "ui",
		"services", "apps", "packages", "internal", "cmd":
		return true
	default:
		return false
	}
}

// componentFileStem reports a filename without an extension that still names a
// file role rather than a component directory.
func componentFileStem(segment string) bool {
	switch strings.ToLower(segment) {
	case "main", "index", "mod", "sum", "work", "lock", "dockerfile",
		"makefile", "readme", "license", "changelog", "package", "tsconfig",
		"vite", "eslint", "tailwind", "postcss":
		return true
	default:
		return false
	}
}

// canonicalComponent normalizes the small set of equivalent frontend labels.
func canonicalComponent(segment string) string {
	segment = strings.ToLower(strings.TrimSpace(segment))
	if componentFrontendAliases[segment] {
		return "frontend"
	}
	return segment
}

// componentWords splits a component label into meaningful identity words.
func componentWords(component string) []string {
	component = strings.ToLower(strings.TrimSpace(component))
	fields := strings.FieldsFunc(component, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	var out []string
	for _, word := range fields {
		switch word {
		case "the", "a", "an", "and", "component", "service", "services",
			"app", "application", "system", "module", "package", "backend",
			"frontend", "under", "with", "ui":
			continue
		}
		if word != "" {
			out = append(out, word)
		}
	}
	sort.Strings(out)
	return out
}

// ComponentMatchesPath reports whether a path belongs to the named component.
// It compares the derived component identity, never an arbitrary path
// substring: "api" does not match backend/internal/cart/api.go, and a read of
// product-service does not cover order-service.
func ComponentMatchesPath(component, path string) bool {
	subject := canonicalComponent(component)
	observed := ComponentFromPath(path)
	if subject == "" || observed == "" {
		return false
	}
	if subject == observed {
		return true
	}
	// A component's own root aggregates its children. "The backend" covers
	// backend/internal/product; it does not cover src/frontend.
	if subject == "backend" && pathHasRoot(path, "backend", "server") {
		return true
	}
	if subject == "frontend" && pathHasRoot(path, "src", "frontend", "client", "web", "ui", "app") {
		return true
	}
	subjectWords := componentWords(subject)
	observedWords := componentWords(observed)
	if len(subjectWords) == 0 || len(observedWords) == 0 {
		return false
	}
	return wordSetContains(observedWords, subjectWords) ||
		wordSetContains(subjectWords, observedWords)
}

// ComponentMatchesText reports whether an observation's own coordinate text
// (a runtime command, a service name, a URL) names the component. It is used
// only when the evidence carries an explicit coordinate; an empty hint is not
// a match.
func ComponentMatchesText(component, text string) bool {
	component = strings.TrimSpace(component)
	text = strings.ToLower(strings.TrimSpace(text))
	if component == "" || text == "" {
		return false
	}
	// A path-shaped command or URL is the strongest hint.
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '"' || r == '\'' || r == '=' || r == ','
	}) {
		token = strings.Trim(token, "`()[]{};")
		if strings.Contains(token, "/") || strings.Contains(token, ".") {
			if ComponentMatchesPath(component, token) {
				return true
			}
		}
	}
	// A bare service identity such as "cart-service" or "api-gateway".
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if ComponentMatchesPath(component, word) {
			return true
		}
	}
	return false
}

// pathHasRoot reports whether a slash-separated path begins with one of the
// named roots. It is used only for aggregate component aliases.
func pathHasRoot(path string, roots ...string) bool {
	normalized := strings.Trim(strings.ToLower(filepath.ToSlash(strings.TrimSpace(path))), "/")
	normalized = strings.TrimPrefix(normalized, "./")
	for _, root := range roots {
		if normalized == root || strings.HasPrefix(normalized, root+"/") ||
			strings.Contains(normalized, "/"+root+"/") {
			return true
		}
	}
	return false
}

// wordSetContains reports whether every want word appears in have.
func wordSetContains(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	set := make(map[string]bool, len(have))
	for _, word := range have {
		set[word] = true
	}
	for _, word := range want {
		if !set[word] {
			return false
		}
	}
	return true
}

// ComponentSubject extracts the component a sentence names before a marker.
// It is the shared, bounded subject reader for status, behavior, and
// dependency claims. It returns "" when the phrase names no recognized
// component; callers must not guess.
func ComponentSubject(sentence string, markerStart int) string {
	if markerStart < 0 || markerStart > len(sentence) {
		markerStart = len(sentence)
	}
	before := sentence[:markerStart]
	candidates := componentCandidatesFromText(before)
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// ComponentSubjects extracts every distinct component a sentence names,
// preserving their order. The explicit list after a colon is preferred; the
// bounded kebab and noun vocabulary follows.
func ComponentSubjects(sentence string, markerStart, markerEnd int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(value string) {
		value = canonicalComponent(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, item := range splitComponentList(sentence, markerEnd) {
		add(normalizeComponentPhrase(item))
	}
	withoutParentheticals := parenthesesPattern.ReplaceAllString(sentence, " ")
	for _, token := range kebabComponentPattern.FindAllString(withoutParentheticals, -1) {
		if componentTokenIsRole(token) || componentTokenIsFileName(withoutParentheticals, token) {
			continue
		}
		add(normalizeComponentPhrase(token))
	}
	if len(out) == 0 {
		for _, candidate := range componentCandidatesFromText(sentence[:minInt(markerStart, len(sentence))]) {
			add(candidate)
		}
	}
	return out
}

// minInt bounds a marker index without importing a generic helper package.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// componentCandidatesFromText reads a bounded phrase and returns the
// components it names. It is deliberately conservative: a phrase with no
// recognizable component returns nothing rather than inventing one.
func componentCandidatesFromText(text string) []string {
	text = parenthesesPattern.ReplaceAllString(text, " ")
	var out []string
	seen := map[string]bool{}
	add := func(value string) {
		value = normalizeComponentPhrase(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, token := range kebabComponentPattern.FindAllString(text, -1) {
		if componentTokenIsRole(token) || componentTokenIsFileName(text, token) {
			continue
		}
		add(token)
	}
	// Recognized component nouns, from the end of each comma-separated phrase.
	for _, clause := range componentListSeparator.Split(text, -1) {
		words := strings.FieldsFunc(strings.ToLower(clause), func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		})
		for i := len(words) - 1; i >= 0; i-- {
			word := words[i]
			if componentTrailingWords[word] {
				continue
			}
			if componentNouns[word] {
				add(word)
				break
			}
			break
		}
	}
	return out
}

// splitComponentList returns the explicit list after a colon when the colon is
// close to the marker and belongs to the same clause.
func splitComponentList(sentence string, markerEnd int) []string {
	if markerEnd < 0 || markerEnd > len(sentence) {
		return nil
	}
	rest := sentence[markerEnd:]
	colon := strings.Index(rest, ":")
	if colon < 0 || colon > 48 || strings.ContainsAny(rest[:colon], ";?!") {
		return nil
	}
	tail := strings.TrimSpace(rest[colon+1:])
	if tail == "" {
		return nil
	}
	tail = parenthesesPattern.ReplaceAllString(tail, " ")
	items := componentListSeparator.Split(tail, -1)
	for _, item := range items {
		if normalizeComponentPhrase(item) != "" {
			return items
		}
	}
	return nil
}

// normalizeComponentPhrase reduces a phrase to one component label. A
// recognized noun wins; a kebab coordinate or path is retained. A long
// descriptive phrase is not a component: returning "" preserves ambiguity.
func normalizeComponentPhrase(raw string) string {
	raw = parenthesesPattern.ReplaceAllString(raw, " ")
	raw = strings.TrimSpace(strings.Trim(raw, " \t\r\n.,;:!?()[]{}<>\"'`*_"))
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, article) {
			raw = strings.TrimSpace(raw[len(article):])
			lower = strings.ToLower(raw)
			break
		}
	}
	if strings.Contains(raw, "/") {
		segments := strings.Split(strings.Trim(filepath.ToSlash(lower), "/"), "/")
		// A route such as "/shop" is not a component coordinate. Only a
		// repository-shaped path with a known root can name one.
		if len(segments) < 2 || !componentRootSegment(segments[0]) {
			return ""
		}
		for i := len(segments) - 1; i >= 0; i-- {
			if segment := canonicalComponent(segments[i]); segment != "" &&
				!componentContainerSegments[segment] && !componentLeafSegments[segment] {
				return segment
			}
		}
	}
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	var content []string
	for _, word := range words {
		if componentSubjectStopWords[word] || componentTrailingWords[word] {
			continue
		}
		content = append(content, word)
	}
	for len(content) > 0 && componentTrailingWords[content[len(content)-1]] {
		content = content[:len(content)-1]
	}
	if len(content) == 0 || len(content) > 3 {
		return ""
	}
	for i := len(content) - 1; i >= 0; i-- {
		if componentNouns[content[i]] {
			return content[i]
		}
	}
	label := strings.Join(content, "-")
	if label == "" || len(label) > 48 {
		return ""
	}
	return label
}

// componentSubjectStopWords are grammar or generic identity words that do not
// name a component.
var componentSubjectStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true,
	"these": true, "those": true, "its": true, "their": true, "our": true,
	"your": true, "it": true, "and": true, "or": true, "of": true,
	"for": true, "to": true, "in": true, "on": true, "with": true,
	"by": true, "from": true, "as": true, "at": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true,
	"being": true, "has": true, "have": true, "had": true, "uses": true,
	"use": true, "using": true, "contains": true, "includes": true,
	"exposes": true, "provides": true, "offers": true, "supports": true,
	"project": true, "repository": true, "repo": true, "codebase": true,
	"workspace": true, "application": true, "system": true,
	"platform": true, "code": true, "source": true, "implementation": true,
	"module": true, "package": true, "service": true, "services": true,
	"component": true, "components": true, "part": true, "area": true,
}

// componentTrailingWords may follow a component before a status marker. They
// are removed from the subject so "the frontend uses" resolves to frontend.
var componentTrailingWords = map[string]bool{
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "being": true, "has": true, "have": true, "had": true,
	"fully": true, "completely": true, "partially": true,
	"implemented": true, "functional": true, "production": true,
	"ready": true, "feature": true, "complete": true,
	"stub": true, "stubs": true, "scaffolded": true, "mocked": true,
	"now": true, "currently": true, "already": true, "actually": true,
	"also": true, "both": true, "all": true, "uses": true, "use": true,
	"using": true, "contains": true, "includes": true, "relies": true,
	"depends": true, "exposes": true, "provides": true, "offers": true,
	"supports": true, "and": true, "the": true, "a": true, "an": true,
	"service": true, "services": true, "component": true,
	"components": true, "part": true, "area": true, "system": true,
	"application": true, "platform": true,
}

// componentNouns is the finite component vocabulary. A kebab coordinate is
// also accepted; the list keeps ordinary prose from becoming a subject.
var componentNouns = map[string]bool{
	"product": true, "order": true, "cart": true, "delivery": true,
	"notification": true, "payment": true, "inventory": true, "user": true,
	"auth": true, "gateway": true, "frontend": true, "backend": true,
	"web": true, "admin": true, "worker": true, "client": true,
	"server": true, "checkout": true, "catalog": true, "search": true,
	"shipping": true, "billing": true, "email": true, "account": true,
	"storefront": true, "ui": true,
}

// componentTokenIsRole reports a kebab token that describes a role, not a
// component coordinate.
func componentTokenIsRole(token string) bool {
	switch strings.ToLower(token) {
	case "fully-implemented", "completely-implemented", "partially-implemented",
		"fully-functional", "feature-complete", "production-ready",
		"health-checks", "health-check", "mock-data", "mock-auth",
		"mock-backend", "mock-api", "server-computed", "unit-tests",
		"end-to-end", "read-only", "data-layer", "check-out":
		return true
	default:
		return false
	}
}

// componentTokenIsFileName reports a kebab fragment that is part of a filename
// such as docker-compose.yml rather than a component coordinate.
func componentTokenIsFileName(sentence, token string) bool {
	index := strings.Index(strings.ToLower(sentence), strings.ToLower(token))
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
