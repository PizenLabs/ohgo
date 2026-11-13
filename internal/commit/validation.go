package commit

import (
	"path"
	"strings"
)

// Validation codes. They name the deterministic rule that rejected a proposal
// so a repair turn receives one bounded correction instead of the repository.
const (
	// CodeInvalidType means the type is outside the repository vocabulary or
	// contradicts the semantic shape of the change set.
	CodeInvalidType = "INVALID_TYPE"
	// CodeInvalidScope means the scope cannot name a subsystem.
	CodeInvalidScope = "INVALID_SCOPE"
	// CodeUnsupportedClaim means the message asserts a change the evidence
	// does not contain.
	CodeUnsupportedClaim = "UNSUPPORTED_CLAIM"
	// CodeProcessNarration means the message describes the agent's work
	// instead of the repository change.
	CodeProcessNarration = "PROCESS_NARRATION"
	// CodeSubjectLength means the subject exceeds the repository limit.
	CodeSubjectLength = "INVALID_SUBJECT_LENGTH"
	// CodeBulletLength means one body line exceeds the repository limit.
	CodeBulletLength = "INVALID_BULLET_LENGTH"
)

// ValidateProposal applies semantic alignment to an otherwise well-formed
// proposal.
//
// Parse owns syntax and length. This pass owns meaning: the type must belong to
// the conventional vocabulary and match the shape of the selected change set,
// the message must describe the resulting repository change, and no body claim
// may reference a change that is not in the evidence.
func (s *Snapshot) ValidateProposal(proposal Proposal) error {
	if s == nil || s.Context == nil {
		return nil
	}
	evidence := typeEvidenceOf(s.Scope)

	types := s.Context.Constraints.Types
	if len(types) == 0 {
		types = defaultTypes
	}
	if !containsTypeIn(types, proposal.Type) {
		return invalidCode(CodeInvalidType,
			"type %q is not one of the repository types (%s)",
			proposal.Type, strings.Join(types, " "))
	}
	// A revert describes why the change exists, not the file roles of the
	// reverted diff: it may legitimately revert docs, tests, or code.
	if proposal.Type != "revert" {
		if evidence.DocsOnly && proposal.Type != "docs" {
			return invalidCode(CodeInvalidType,
				"the selected change set is documentation only; use type docs, not %s", proposal.Type)
		}
		if !evidence.DocsOnly && proposal.Type == "docs" {
			return invalidCode(CodeInvalidType,
				"the selected change set changes code or configuration; docs is not the whole change")
		}
		if evidence.TestsOnly && proposal.Type != "test" {
			return invalidCode(CodeInvalidType,
				"the selected change set is tests only; use type test, not %s", proposal.Type)
		}
		if !evidence.TestsOnly && proposal.Type == "test" {
			return invalidCode(CodeInvalidType,
				"the selected change set includes implementation changes; test is not the whole change")
		}
		if evidence.BuildOnly && proposal.Type != "build" && proposal.Type != "ci" && proposal.Type != "chore" {
			return invalidCode(CodeInvalidType,
				"the selected change set is build or CI infrastructure; use build, ci, or chore, not %s", proposal.Type)
		}
	}
	if proposal.Scope == "" {
		return invalidCode(CodeInvalidScope, "the scope is empty")
	}
	if other := conflictingGroupScope(s.Context, proposal.Scope); other != "" {
		return invalidCode(CodeInvalidScope,
			"scope %q names the %s change, not the approved change", proposal.Scope, other)
	}

	if err := validateClaims(proposal, s.Scope, evidence); err != nil {
		return err
	}
	return nil
}

// conflictingGroupScope reports a scope that belongs to a different, excluded
// logical change in an automatic plan. A brand-new scope is still allowed: the
// check only catches cross-group confusion, not vocabulary invention.
func conflictingGroupScope(context *CommitContext, scope string) string {
	if context == nil || scope == "" {
		return ""
	}
	selectedScope := ""
	for _, group := range context.Groups {
		if group.Selected {
			selectedScope = group.Scope
			break
		}
	}
	if selectedScope == scope {
		return ""
	}
	for _, group := range context.Groups {
		if group.Selected || group.Scope != scope {
			continue
		}
		return safePath(group.Label)
	}
	return ""
}

// validateClaims rejects invented outcomes and process narration.
func validateClaims(proposal Proposal, scope []FileChange, evidence TypeEvidence) error {
	subject := strings.ToLower(proposal.Summary)
	if verb, ok := narrationVerb(subject, true); ok {
		return invalidCode(CodeProcessNarration,
			"the subject describes agent work (%q); describe the repository change instead", verb)
	}
	if err := validateClaimText(subject, evidence, "subject"); err != nil {
		return err
	}
	paths := scopePaths(scope)
	for _, token := range repositoryPathTokens(subject) {
		if !paths[token] {
			return invalidCode(CodeUnsupportedClaim,
				"the subject references %q, which is not in the approved change set", token)
		}
	}
	for _, bullet := range proposal.Body {
		text := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(bullet, "- ")))
		if verb, ok := narrationVerb(text, false); ok {
			return invalidCode(CodeProcessNarration,
				"body line %q describes agent work (%q); describe the repository change instead", bullet, verb)
		}
		if err := validateClaimText(text, evidence, "body"); err != nil {
			return err
		}
		for _, token := range repositoryPathTokens(text) {
			if !paths[token] {
				return invalidCode(CodeUnsupportedClaim,
					"body references %q, which is not in the approved change set", token)
			}
		}
	}
	return nil
}

// validateClaimText checks outcome verbs against the observed churn.
func validateClaimText(text string, evidence TypeEvidence, where string) error {
	for _, verb := range []string{"remove", "removes", "removed", "delete", "deletes", "deleted", "drop", "drops", "dropped", "eliminate", "eliminates", "eliminated"} {
		if containsWord(text, verb) && !negatedClaim(text, verb) && !evidence.HasDeletions {
			return invalidCode(CodeUnsupportedClaim,
				"the %s claims a removal (%q) but the selected changes contain no deletion", where, verb)
		}
	}
	for _, verb := range []string{"add", "adds", "added", "create", "creates", "created", "introduce", "introduces", "introduced"} {
		if containsWord(text, verb) && !negatedClaim(text, verb) && !evidence.HasAdditions {
			return invalidCode(CodeUnsupportedClaim,
				"the %s claims an addition (%q) but the selected changes contain no added content", where, verb)
		}
	}
	return nil
}

// negatedClaim reports a claim word immediately qualified by no/not/never/
// without. "No tests added" is a truthful statement about a change that only
// removes code, not an unsupported addition claim.
func negatedClaim(text, word string) bool {
	index := strings.Index(text, word)
	if index < 0 {
		return false
	}
	prefix := strings.TrimSpace(text[:index])
	fields := strings.Fields(prefix)
	start := len(fields) - 3
	if start < 0 {
		start = 0
	}
	for _, field := range fields[start:] {
		field = strings.Trim(field, ".,;:")
		switch field {
		case "no", "not", "never", "without", "zero":
			return true
		}
	}
	return false
}

// narrationVerb reports agent process narration without rejecting a change
// description that happens to start with the same verb.
//
// "read config from env" describes the repository behavior; "read claims.go"
// describes the agent's work. A verb alone is therefore not enough: the phrase
// must also name a repository artifact, a process object, or a Git command.
func narrationVerb(text string, subject bool) (string, bool) {
	text = strings.TrimSpace(text)
	verbs := []string{
		"inspected", "searched", "ran", "staged", "decided",
		"checked", "examined", "looked", "verified", "reviewed",
	}
	if !subject {
		verbs = append(verbs, "read")
	}
	verb := ""
	for _, candidate := range verbs {
		if containsWord(text, candidate) && strings.HasPrefix(text, candidate) {
			verb = candidate
			break
		}
	}
	if verb == "" {
		return "", false
	}
	for _, phrase := range []string{
		"git status", "git diff", "git log", "the agent",
		"i inspected", "i read", "i searched", "i ran", "i staged",
	} {
		if strings.Contains(text, phrase) {
			return phrase, true
		}
	}
	processObjects := []string{
		"file", "files", "repo", "repository", "code", "diff", "status",
		"claims", "source", "log", "logs", "tests", "test", "suite",
		"build", "lint", "scope", "coverage", "command", "commands",
		"change", "changes", "path", "paths", "everything",
	}
	for _, object := range processObjects {
		if containsWord(text, object) {
			return verb, true
		}
	}
	if len(repositoryPathTokens(text)) > 0 {
		return verb, true
	}
	return "", false
}

// containsWord reports whole-word presence without a regexp allocation for
// every body line.
func containsWord(text, word string) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], word)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(word)
		if (start == 0 || !isWordByte(text[start-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		offset = end
	}
	return false
}

// isWordByte reports a character that can belong to an identifier-like word.
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == '-'
}

// scopePaths is the set of exact and basename path tokens in the scope.
func scopePaths(scope []FileChange) map[string]bool {
	out := make(map[string]bool, len(scope)*2)
	for _, change := range scope {
		addPathAndParents(out, change.Path)
		if change.OrigPath != "" {
			addPathAndParents(out, change.OrigPath)
		}
	}
	return out
}

// addPathAndParents records a file, its basename, and every parent directory.
// A claim may legitimately name the package rather than one file in it.
func addPathAndParents(set map[string]bool, value string) {
	if value == "" {
		return
	}
	set[value] = true
	set[pathBase(value)] = true
	for dir := path.Dir(value); dir != "" && dir != "." && dir != "/"; dir = path.Dir(dir) {
		set[dir] = true
	}
}

// repositoryPathTokens extracts filename-shaped tokens from a claim.
func repositoryPathTokens(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !isPathTokenRune(r)
	})
	var out []string
	for _, token := range fields {
		token = strings.TrimRight(token, ".,;:")
		if token == "" || strings.Contains(token, "://") {
			// A URL is governed by reference validation, not by the path
			// claim rule.
			continue
		}
		if looksLikeRepositoryPath(token) {
			out = append(out, token)
		}
	}
	return out
}

// isPathTokenRune reports a character that may appear in a repository path
// token.
func isPathTokenRune(r rune) bool {
	return isAlphaNumeric(r) || r == '_' || r == '-' || r == '.' || r == '/'
}

// repositoryPathRoots are conventional top-level path segments. A slash token
// under one of these is a repository path claim even without an extension.
var repositoryPathRoots = map[string]bool{
	"internal": true, "cmd": true, "pkg": true, "src": true, "lib": true,
	"test": true, "tests": true, "testdata": true, "docs": true, "old_docs": true,
	"scripts": true, "api": true, "app": true, "web": true, "config": true,
	"deploy": true, "assets": true, "examples": true, "bench": true,
	"build": true, "tools": true, "vendor": true, "migrations": true,
	"public": true, "static": true, "templates": true, "proto": true,
	"schemas": true, ".github": true, ".ohgo": true,
}

// looksLikeRepositoryPath reports a token with a known source/document
// extension or a conventional repository path shape. It deliberately rejects
// ratio-like tokens such as "24/7" and version numbers.
func looksLikeRepositoryPath(token string) bool {
	if strings.Contains(token, "..") {
		return false
	}
	if strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		return true
	}
	base := pathBase(token)
	ext := strings.ToLower(pathExt(base))
	if ext != "" {
		switch ext {
		case ".go", ".md", ".mdx", ".mod", ".sum", ".yml", ".yaml", ".json",
			".toml", ".sh", ".txt", ".rs", ".c", ".h", ".cc", ".cpp", ".hpp",
			".js", ".jsx", ".ts", ".tsx", ".py", ".rb", ".java", ".kt", ".kts",
			".cs", ".css", ".scss", ".html", ".sql", ".xml", ".ini", ".conf",
			".env", ".lock", ".mk", ".gradle", ".swift", ".php", ".lua":
			return true
		}
	}
	if !strings.Contains(token, "/") {
		return false
	}
	root := token
	if i := strings.IndexByte(token, '/'); i >= 0 {
		root = token[:i]
	}
	return repositoryPathRoots[root]
}
