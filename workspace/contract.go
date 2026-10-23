package workspace

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/ohgo/contract"
)

// The contract vocabulary lives in package contract so the execution engine can
// consume it without importing the authority layer (which already imports
// agent). These aliases keep the builder's call surface in workspace, where
// request classification lives.
type (
	ObjectiveKind           = contract.ObjectiveKind
	ChangeOp                = contract.ChangeOp
	TargetKind              = contract.TargetKind
	Target                  = contract.Target
	ExpectedChange          = contract.ExpectedChange
	VerificationKind        = contract.VerificationKind
	VerificationCheck       = contract.VerificationCheck
	VerificationRequirement = contract.VerificationRequirement
	IntentContract          = contract.IntentContract
)

const (
	ObjectiveInformational = contract.ObjectiveInformational
	ObjectiveExecution     = contract.ObjectiveExecution
	ObjectiveMutation      = contract.ObjectiveMutation

	ChangeAny    = contract.ChangeAny
	ChangeCreate = contract.ChangeCreate
	ChangeModify = contract.ChangeModify
	ChangeDelete = contract.ChangeDelete

	TargetPath    = contract.TargetPath
	TargetSymbol  = contract.TargetSymbol
	TargetPackage = contract.TargetPackage

	VerifyAny   = contract.VerifyAny
	VerifyScope = contract.VerifyScope

	CheckAny   = contract.CheckAny
	CheckTest  = contract.CheckTest
	CheckBuild = contract.CheckBuild
	CheckVet   = contract.CheckVet
)

// BuildIntentContract extracts a completion contract from a user request.
//
// The extraction is finite and deliberately conservative. It preserves what
// the user explicitly named and never invents a target. When a mutation
// objective names neither a target nor a behavioural check, Ambiguous is set so
// the completion evaluator can return NEEDS_REVIEW instead of guessing.
func BuildIntentContract(text string) IntentContract {
	objective := strings.TrimSpace(text)
	contract := IntentContract{Objective: objective}

	if objective == "" {
		contract.Kind = ObjectiveInformational
		contract.Ambiguous = true
		contract.Issues = append(contract.Issues, "the request is empty")
		return contract
	}

	intent := Classify(objective)
	mutation := RequestsMutation(objective)
	switch {
	case mutation:
		contract.Kind = ObjectiveMutation
	case intent == IntentRead:
		contract.Kind = ObjectiveInformational
	default:
		contract.Kind = ObjectiveExecution
	}

	contract.Verification = verificationRequirements(objective, contract.Kind)
	contract.BehavioralObjective = contract.Kind == ObjectiveMutation && behavioralMutationObjective(objective)

	// Command arguments are not mutation targets: "./tools" in
	// "go test ./tools" is the verification scope, not the file to change.
	commandSpans := verificationArgumentSpans(objective)

	paths := extractPathTargets(objective, commandSpans)
	symbols := extractSymbolTargets(objective)
	packages := extractPackageTargets(objective)

	add := func(target Target) {
		for _, existing := range contract.Targets {
			if existing == target {
				return
			}
		}
		// A request that names dozens of coordinates cannot be verified with a
		// small evidence set. Refuse to pretend otherwise.
		if len(contract.Targets) >= 8 {
			issue := "the request names more than 8 targets"
			seen := false
			for _, existing := range contract.Issues {
				if existing == issue {
					seen = true
					break
				}
			}
			if !seen {
				contract.Issues = append(contract.Issues, issue)
			}
			return
		}
		contract.Targets = append(contract.Targets, target)
		contract.ExpectedChanges = append(contract.ExpectedChanges, ExpectedChange{
			Target:    target,
			Operation: changeForAt(objective, target, targetStart(objective, target.Value)),
		})
	}
	for _, value := range paths {
		add(Target{Kind: TargetPath, Value: value})
	}
	for _, value := range symbols {
		add(Target{Kind: TargetSymbol, Value: value})
	}
	for _, value := range packages {
		add(Target{Kind: TargetPackage, Value: value})
	}

	if contract.Kind == ObjectiveMutation {
		// Phase 3 already requires a successful check after a change. Making it
		// explicit here lets the behavioural evaluator attach the target scope.
		if len(contract.Verification) == 0 {
			contract.Verification = []VerificationRequirement{{Kind: VerifyAny}}
		}
		// A behavioural objective with no target has no package to scope the
		// check to. Requiring a repo-wide check is the only way to establish
		// that "the failing tests" are actually fixed; a narrow pass in an
		// unrelated package is not evidence for an unbounded oracle.
		if contract.BehavioralObjective && len(contract.Targets) == 0 &&
			!hasVerificationScope(contract.Verification) {
			contract.Verification = []VerificationRequirement{{
				Kind:  VerifyScope,
				Scope: "*",
				Check: proseCheck(objective),
			}}
		}
		if len(contract.Targets) == 0 && !contract.BehavioralObjective {
			contract.Ambiguous = true
			contract.Issues = append(contract.Issues,
				"the request does not name a verifiable target or behavioural check")
		}
	}
	return contract
}

// hasVerificationScope reports whether a requirement names a package scope.
func hasVerificationScope(requirements []VerificationRequirement) bool {
	for _, requirement := range requirements {
		if requirement.Kind == VerifyScope {
			return true
		}
	}
	return false
}

// targetStart finds the first occurrence of a target value in the request, for
// operation extraction. A miss returns 0, which yields ChangeAny: a target the
// parser cannot place must not acquire a guessed direction.
func targetStart(text, value string) int {
	if value == "" {
		return 0
	}
	if index := strings.Index(strings.ToLower(text), strings.ToLower(value)); index >= 0 {
		return index
	}
	return 0
}

// changeForAt derives the requested operation from the clause immediately
// before a target.
//
// A verb separated from the target by a preposition names the target's
// location, not the target itself ("add a timeout in server.go"), so the
// operation is ChangeAny. This keeps a create request from rejecting an edit
// to an existing file that merely contains the new code.
func changeForAt(text string, target Target, start int) ChangeOp {
	if start <= 0 || start > len(text) {
		return ChangeAny
	}
	prefix := strings.ToLower(text[:start])
	cut := 0
	for _, separator := range []string{",", ";", "\n", " and ", " then ", " plus ", " & "} {
		if index := strings.LastIndex(prefix, separator); index >= 0 {
			if next := index + len(separator); next > cut {
				cut = next
			}
		}
	}
	clause := prefix[cut:]
	fields := strings.Fields(clause)
	for i := len(fields) - 1; i >= 0; i-- {
		op, ok := changeVerb(fields[i], target.Kind)
		if !ok {
			continue
		}
		between := fields[i+1:]
		for _, token := range between {
			if locationPrepositions[token] {
				return ChangeAny
			}
		}
		return op
	}
	return ChangeAny
}

// changeVerb maps a mutation verb to an operation for one target kind.
//
// "Add" creates a symbol but only locates code in a file; "write" overwrites a
// file but does not necessarily create one. Encoding that distinction here
// keeps the strict create/delete checks from firing on ordinary edits.
func changeVerb(verb string, kind TargetKind) (ChangeOp, bool) {
	switch verb {
	case "create", "new":
		return ChangeCreate, true
	case "delete", "remove", "drop", "unlink":
		return ChangeDelete, true
	case "update", "modify", "change", "edit", "fix", "refactor", "rewrite",
		"replace", "rename", "format", "tidy", "clean", "organize", "organise":
		return ChangeModify, true
	case "add", "introduce", "write", "generate", "implement", "ensure":
		if kind == TargetSymbol {
			return ChangeCreate, true
		}
		return ChangeAny, true
	}
	return ChangeAny, false
}

// locationPrepositions mark a target as the place a change happens rather than
// the thing being changed.
var locationPrepositions = map[string]bool{
	"in": true, "inside": true, "into": true, "within": true, "under": true,
	"to": true, "from": true, "on": true, "at": true, "of": true, "for": true,
}

// verificationCommand matches the command forms whose arguments carry a
// package or path scope.
var verificationCommand = regexp.MustCompile(`(?i)\bgo\s+(test|build|vet)\b`)

// verificationArgumentSpans returns byte spans of arguments that belong to a
// Go verification command.
func verificationArgumentSpans(text string) [][2]int {
	var spans [][2]int
	for _, match := range verificationCommand.FindAllStringIndex(text, -1) {
		position := match[1]
		for position < len(text) {
			for position < len(text) && (text[position] == ' ' || text[position] == '\t') {
				position++
			}
			if position >= len(text) {
				break
			}
			if text[position] == ',' || text[position] == ';' || text[position] == '\n' || text[position] == '`' {
				break
			}
			start := position
			for position < len(text) && !strings.ContainsRune(" \t,;\n`", rune(text[position])) {
				position++
			}
			if position == start {
				position++
				continue
			}
			token := text[start:position]
			switch strings.ToLower(token) {
			case "and", "then", "plus", "&", "so":
				return spans
			}
			if strings.HasPrefix(token, "-") {
				continue
			}
			spans = append(spans, [2]int{start, position})
		}
	}
	return spans
}

// verificationRequirements extracts explicit command scopes and generic check
// language from a request.
func verificationRequirements(text string, kind ObjectiveKind) []VerificationRequirement {
	if kind == ObjectiveInformational {
		return nil
	}
	var out []VerificationRequirement
	addScope := func(scope string, check VerificationCheck) {
		scope = normalizeScope(scope)
		if scope == "" {
			return
		}
		for _, existing := range out {
			if existing.Kind == VerifyScope && existing.Scope == scope && existing.EffectiveCheck() == check {
				return
			}
		}
		out = append(out, VerificationRequirement{Kind: VerifyScope, Scope: scope, Check: check})
	}
	for _, match := range verificationCommand.FindAllStringSubmatchIndex(text, -1) {
		check := commandCheck(text[match[2]:match[3]])
		scopes := commandScopes(text[match[1]:])
		if len(scopes) == 0 {
			addScope(".", check)
			continue
		}
		for _, scope := range scopes {
			addScope(scope, check)
		}
	}
	prose := proseCheck(text)
	if len(out) == 0 && mentionsCheck(text) {
		out = append(out, VerificationRequirement{Kind: VerifyAny, Check: prose})
	}
	if scope := proseVerificationScope(text); scope != "" {
		addScope(scope, prose)
	}
	return out
}

// commandCheck maps a Go verification subcommand to the check it performs.
func commandCheck(subcommand string) VerificationCheck {
	switch strings.ToLower(subcommand) {
	case "test":
		return CheckTest
	case "build":
		return CheckBuild
	case "vet":
		return CheckVet
	default:
		return CheckAny
	}
}

// proseCheck derives the check kind from the request's own words. It is
// deliberately coarse: "tests" is stronger evidence than "build", and a
// request that only says "verify" accepts any recognised check.
func proseCheck(text string) VerificationCheck {
	normalized := " " + normalizeRequest(text) + " "
	switch {
	case containsPhrase(normalized, []string{"test", "tests", "test suite", "pytest", "unit tests"}):
		return CheckTest
	case containsPhrase(normalized, []string{"build", "builds", "compile", "compiles"}):
		return CheckBuild
	case containsPhrase(normalized, []string{"vet", "staticcheck", "lint"}):
		return CheckVet
	default:
		return CheckAny
	}
}

// proseVerificationScope reads a package named as the place a check runs
// ("fix the failing tests in tools"). It is narrow on purpose: a generic word
// such as "production" or "the repository" is not a package coordinate.
var proseVerificationScopePattern = regexp.MustCompile(
	`(?i)\b(?:tests?|test suite|build)\s+(?:in|for|under)\s+(?:the\s+)?([a-z][a-z0-9_]*)\b`)

func proseVerificationScope(text string) string {
	match := proseVerificationScopePattern.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	value := strings.ToLower(match[1])
	switch value {
	case "the", "a", "an", "this", "that", "repository", "repo", "project",
		"code", "ci", "production", "general", "same", "package":
		return ""
	}
	return value
}

// commandScopes reads package/path arguments from the text after a Go
// verification subcommand. Only explicit path-like arguments are accepted;
// flags and their values are deliberately not guessed at.
func commandScopes(after string) []string {
	var scopes []string
	for _, field := range strings.FieldsFunc(after, func(r rune) bool {
		switch r {
		case ' ', '\t', ',', ';', '\n', '`':
			return true
		}
		return false
	}) {
		if strings.HasPrefix(field, "-") {
			continue
		}
		if field == "and" || field == "then" || field == "so" || field == "&" {
			break
		}
		if !strings.HasPrefix(field, ".") && !strings.Contains(field, "/") && !strings.HasSuffix(field, "...") {
			continue
		}
		scope := normalizeScope(field)
		if scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// normalizeScope maps a command argument to a repository-relative directory.
func normalizeScope(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "`'\""))
	value = strings.TrimSuffix(value, ",")
	value = strings.TrimSuffix(value, ";")
	if value == "" {
		return ""
	}
	if value == "..." || value == "./..." {
		return "*"
	}
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimSuffix(value, "/...")
	value = strings.TrimSuffix(value, "...")
	value = strings.Trim(value, "/")
	if value == "" || value == "." {
		return "."
	}
	if strings.Contains(value, "..") {
		return ""
	}
	return value
}

// mentionsCheck reports whether the request names a check without spelling out
// a command.
func mentionsCheck(text string) bool {
	normalized := " " + normalizeRequest(text) + " "
	for _, phrase := range []string{
		" test ", " tests ", " suite ", " build ", " compile ", " vet ",
		" verify ", " verification ", " passes ", " passing ", " pass ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// behavioralMutationObjective reports that the request's success condition is
// a check rather than a named repository coordinate.
func behavioralMutationObjective(text string) bool {
	normalized := " " + normalizeRequest(text) + " "
	for _, phrase := range []string{
		"fix the failing tests", "fix the failing test", "fix failing tests",
		"fix the tests", "fix the test", "fix tests", "fix the build",
		"make the tests pass", "make tests pass", "make the test pass",
		"get the tests passing", "get tests passing", "get the test passing",
		"so the tests pass", "so tests pass", "so the test passes",
		"make the build pass", "make it compile", "make the project compile",
		"ensure the tests pass", "ensure tests pass", "ensure the test passes",
		"keep the tests passing",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// pathToken matches one candidate path-like token. It stops at punctuation so
// a sentence comma is not part of the path.
var pathToken = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*`)

// extensionToken matches a filename extension with at least one letter.
var extensionToken = regexp.MustCompile(`\.([A-Za-z][A-Za-z0-9]{0,7})$`)

// knownFileExtensions are the extensions the extractor treats as a file path.
// A qualified symbol also contains dots, so accepting every dotted token would
// turn "tools.GrepTool.Execute" into a path.
var knownFileExtensions = map[string]bool{
	"go": true, "mod": true, "sum": true, "md": true, "txt": true,
	"json": true, "yaml": true, "yml": true, "toml": true, "xml": true,
	"rs": true, "py": true, "ts": true, "tsx": true, "js": true, "jsx": true,
	"css": true, "html": true, "sh": true, "sql": true, "proto": true,
	"cfg": true, "ini": true, "env": true, "lock": true, "csv": true,
	"log": true, "diff": true, "patch": true, "make": true, "mk": true,
}

// knownFileNames are extensionless files a request can name directly.
var knownFileNames = map[string]bool{
	"makefile": true, "dockerfile": true, "license": true, "readme": true,
	"changelog": true,
}

// looksLikeFile reports whether value ends in a recognised file extension or
// is one of the extensionless filenames the runtime recognises.
func looksLikeFile(value string) bool {
	base := value
	if index := strings.LastIndex(value, "/"); index >= 0 {
		base = value[index+1:]
	}
	if knownFileNames[strings.ToLower(base)] {
		return true
	}
	match := extensionToken.FindStringSubmatch(base)
	if match == nil {
		return false
	}
	return knownFileExtensions[strings.ToLower(match[1])]
}

// extractPathTargets returns explicit file paths in the request.
func extractPathTargets(text string, commandSpans [][2]int) []string {
	var out []string
	for _, match := range pathToken.FindAllStringIndex(text, -1) {
		if insideAnySpan(match[0], commandSpans) {
			continue
		}
		value := strings.TrimRight(text[match[0]:match[1]], ".-")
		if value == "" || strings.Contains(value, "..") {
			continue
		}
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "http") || strings.HasPrefix(lower, "www.") {
			continue
		}
		if !strings.Contains(value, "/") && !looksLikeFile(value) {
			continue
		}
		value = strings.TrimPrefix(value, "./")
		if value == "" || value == "." || value == "..." {
			continue
		}
		out = appendUnique(out, value)
	}
	return out
}

// extractBackticked returns the contents of single-backtick spans.
func extractBackticked(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '`' {
			continue
		}
		end := strings.IndexByte(text[i+1:], '`')
		if end < 0 {
			break
		}
		value := strings.TrimSpace(text[i+1 : i+1+end])
		if value != "" && !strings.ContainsAny(value, " \t\n") {
			out = append(out, value)
		}
		i += end + 1
	}
	return out
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	qualifiedPattern  = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+\b`)
	qualifiedExact    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$`)
	camelCandidate    = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*\b`)
	packagePattern    = regexp.MustCompile(`\bpackage\s+([a-z][a-z0-9_]*)\b`)
	namedPackage      = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\s+package\b`)
)

// packageStopWords are English words and generic nouns that can follow or
// precede "package" in a question without naming a Go package. A word absent
// from this list remains a candidate, so a real package such as "auth" is not
// lost to over-filtering.
var packageStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true,
	"these": true, "those": true, "its": true, "your": true, "our": true,
	"declared": true, "defined": true, "named": true, "called": true,
	"name": true, "names": true, "manager": true, "json": true, "main": true,
	"structure": true, "layout": true, "dependency": true, "dependencies": true,
	"import": true, "imports": true, "path": true, "paths": true,
	"directory": true, "directories": true, "file": true, "files": true,
	"list": true, "version": true, "versions": true, "code": true,
	"source": true, "sources": true, "module": true, "modules": true,
	"function": true, "functions": true, "type": true, "types": true,
	"test": true, "tests": true, "build": true, "builds": true,
	"compile": true, "compiles": true, "documentation": true, "docs": true,
	"readme": true, "root": true, "project": true, "repository": true,
	"repo": true, "package": true, "packages": true,
}

// symbolStopWords are English words that happen to look like identifiers.
var symbolStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true,
	"add": true, "apply": true, "build": true, "change": true, "check": true,
	"clean": true, "create": true, "delete": true, "describe": true,
	"edit": true, "ensure": true, "explain": true, "fix": true, "format": true,
	"generate": true, "implement": true, "inspect": true, "introduce": true,
	"make": true, "modify": true, "move": true, "new": true, "organize": true,
	"organise": true, "please": true, "read": true, "refactor": true,
	"remove": true, "rename": true, "replace": true, "review": true,
	"rewrite": true, "run": true, "show": true, "tidy": true, "update": true,
	"write": true, "go": true, "golang": true, "readme": true, "todo": true,
	"ok": true, "cli": true, "tui": true, "api": true, "url": true,
	"json": true, "http": true, "https": true, "id": true, "ast": true,
	"sse": true, "ui": true, "ai": true,
}

// extractSymbolTargets returns Go symbols the user named unambiguously:
// backticked identifiers, qualified names, and CamelCase identifiers.
func extractSymbolTargets(text string) []string {
	var out []string
	for _, value := range extractBackticked(text) {
		if qualifiedExact.MatchString(value) || identifierPattern.MatchString(value) {
			if !symbolStopWords[strings.ToLower(value)] && !looksLikePath(value) {
				out = appendUnique(out, value)
			}
		}
	}
	var qualifiedSpans [][2]int
	for _, match := range qualifiedPattern.FindAllStringIndex(text, -1) {
		value := text[match[0]:match[1]]
		if looksLikePath(value) || symbolStopWords[strings.ToLower(value)] {
			continue
		}
		if !hasUpper(value) {
			continue
		}
		qualifiedSpans = append(qualifiedSpans, [2]int{match[0], match[1]})
		out = appendUnique(out, value)
	}
	for _, match := range camelCandidate.FindAllStringIndex(text, -1) {
		if insideAnySpan(match[0], qualifiedSpans) {
			continue
		}
		value := text[match[0]:match[1]]
		if symbolStopWords[strings.ToLower(value)] || !camelLike(value) {
			continue
		}
		out = appendUnique(out, value)
	}
	return out
}

// extractPackageTargets returns explicitly named Go packages.
//
// The surrounding words are natural language, not coordinates: "what package
// is declared here?" is a question about a file, not a request to locate a
// package named "declared". The stop list keeps those generic words from
// becoming evidence requirements.
func extractPackageTargets(text string) []string {
	var out []string
	for _, match := range packagePattern.FindAllStringSubmatch(text, -1) {
		value := match[1]
		if packageStopWords[value] {
			continue
		}
		out = appendUnique(out, value)
	}
	for _, match := range namedPackage.FindAllStringSubmatch(text, -1) {
		value := match[1]
		if packageStopWords[value] {
			continue
		}
		out = appendUnique(out, value)
	}
	return out
}

// changeForAt needs the target kind; targetStart is the occurrence used for
// both paths and symbols.
func insideAnySpan(position int, spans [][2]int) bool {
	for _, span := range spans {
		if position >= span[0] && position < span[1] {
			return true
		}
	}
	return false
}

// looksLikePath reports whether a token is a filename or directory, not a
// symbol. A dot is ambiguous; an extension or slash settles it.
func looksLikePath(value string) bool {
	if strings.Contains(value, "/") {
		return true
	}
	return looksLikeFile(value)
}

// camelLike reports whether an identifier has the internal shape of a Go
// exported name rather than an ordinary capitalised English word.
func camelLike(value string) bool {
	if value == "" {
		return false
	}
	uppers := 0
	transition := false
	for i, r := range value {
		if r >= 'A' && r <= 'Z' {
			uppers++
			if i > 0 && value[i-1] >= 'a' && value[i-1] <= 'z' {
				transition = true
			}
		}
	}
	if uppers == len(value) && len(value) <= 5 {
		// JSON, HTTP, API: acronyms, not navigation coordinates.
		return false
	}
	return transition || uppers >= 2
}

func hasUpper(value string) bool {
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
