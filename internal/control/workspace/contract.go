package workspace

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
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

	ExecutionCommand = contract.ExecutionCommand
	ExecutionRuntime = contract.ExecutionRuntime

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
		contract.Grounding = buildGrounding(objective, contract)
		return contract
	}

	intent := Classify(objective)
	mutation := RequestsMutation(objective)
	// A staged conditional request keeps the change as its objective so the
	// semantics, targets, and verification survive for the branch, but records
	// that the change is deferred until the investigation finds a reason for it.
	contract.ConditionalMutation = mutation && ConditionalWorkflow(objective)
	switch {
	case mutation:
		contract.Kind = ObjectiveMutation
	case intent == IntentRead:
		contract.Kind = ObjectiveInformational
	case RequestsServiceExecution(objective):
		// Starting a service is an execution deliverable even when the request
		// also says to test it: the process and its readiness must be observed,
		// so an answer alone cannot complete the objective.
		contract.Kind = ObjectiveExecution
	case intent == IntentInvestigate:
		// Investigation is an informational deliverable even though it runs
		// under FUZZ and may execute to acquire evidence. Mapping it to an
		// execution objective made any successful process (the first `ls`, for
		// example) satisfy the contract, which withdrew the tool surface while
		// the model was still gathering evidence. The answer is what the user
		// asked for; the commands are how it is established.
		contract.Kind = ObjectiveInformational
	default:
		contract.Kind = ObjectiveExecution
	}
	if contract.Kind == ObjectiveExecution && RequestsRuntimeExecution(objective) {
		// Running the project is not proven by any finished process. The
		// contract must name the stronger observation before the model can
		// satisfy it with an environment probe such as "node --version".
		contract.ExecutionMode = ExecutionRuntime
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
	contract.Grounding = buildGrounding(objective, contract)

	// A conditional change may be scoped by a named repository concept and an
	// explicit check rather than by a literal path: "understand the checkout
	// flow; if the issue is confirmed, fix it and run the tests". The concept
	// is not a filesystem target, but it is a scope and the named check is the
	// oracle, so the branch is not unbounded. This must be decided from the
	// request's own verification, before the mutation default adds VerifyAny.
	conceptOracle := len(contract.Targets) == 0 && len(contract.Verification) > 0 &&
		contract.Grounding.HasConcept()

	if contract.Kind == ObjectiveMutation {
		// Phase 3 already requires a successful check after a change. Making it
		// explicit here lets the behavioural evaluator attach the target scope.
		if len(contract.Verification) == 0 {
			contract.Verification = []VerificationRequirement{{Kind: VerifyAny}}
		}
		if conceptOracle {
			contract.BehavioralObjective = true
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

// PhaseContract returns the contract the active authority can actually satisfy
// for this turn, and whether the persisted objective is deferred to a later
// authority.
//
// A task's completion contract describes the whole objective, which may need
// BUILD even though the current turn runs under ASK or FUZZ. Judging that turn
// against the mutation requirement would report a failed repository change for
// work the authority never permitted; the investigation or understanding phase
// would be mislabelled as an evidence gap. The phase contract is informational:
// it asks whether the turn produced a substantive answer, not whether it
// changed the repository. The caller must keep the task active rather than
// complete it, because the persisted objective is still outstanding.
//
// The original contract is returned unchanged when the active authority can
// perform it, including a mutation under BUILD and an execution under FUZZ or
// BUILD. An invalid authority cannot perform a mutation or execution objective,
// so that contract is deferred rather than silently treated as BUILD.
func PhaseContract(intent IntentContract, active Workspace) (IntentContract, bool) {
	canPerform := false
	switch intent.Kind {
	case ObjectiveInformational:
		return intent, false
	case ObjectiveMutation:
		canPerform = active == Build
	case ObjectiveExecution:
		canPerform = active == Fuzz || active == Build
	default:
		// An unset or unknown objective kind is not a phase. The completion
		// evaluator must review the unsupported contract instead of treating it
		// as an answer.
		return intent, false
	}
	if canPerform {
		return intent, false
	}
	return intent.InformationalPhase(), true
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
		"replace", "rename", "format", "tidy", "clean", "organize", "organise",
		"redesign", "rework", "revamp", "restyle", "redo", "overhaul",
		"modernize", "modernise", "redraw", "reimplement", "rearchitect":
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
	case containsPhrase(normalized, []string{
		"tests", "test suite", "unit tests", "integration tests", "pytest",
		"run test", "run tests", "the test", "this test", "that test",
		"failing test", "failing tests", "npm test", "yarn test", "pnpm test",
		"cargo test", "go test",
	}):
		return CheckTest
	case containsPhrase(normalized, []string{"build", "builds", "compile", "compiles"}):
		return CheckBuild
	case containsPhrase(normalized, []string{"vet", "staticcheck", "lint"}):
		return CheckVet
	default:
		// A bare "test this project" names a verification outcome, not a
		// particular test-runner kind. HTTP/browser behaviour checks are valid
		// evidence for it; requiring a code-test check would reject the exact
		// serve-and-test workflow the runtime supports.
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
//
// The vocabulary is the request's own way of naming a check as the outcome:
// "make the tests pass", "fix the failing test", "implement the fix for this
// failing test". A mutation whose success condition is a failing test can be
// verified without guessing a file, so it must not be reported as ambiguous.
func behavioralMutationObjective(text string) bool {
	normalized := " " + normalizeRequest(text) + " "
	for _, phrase := range []string{
		"fix the failing tests", "fix the failing test", "fix failing tests",
		"fix the tests", "fix the test", "fix tests", "fix the build",
		"fix this failing test", "fix this failing tests", "fix that failing test",
		"fix the failing suite", "fix the failing build",
		"implement the fix for this failing test", "implement the fix for the failing test",
		"implement the fix for this failing tests", "implement the fix for the failing tests",
		"implement the fix for the failing build", "implement the fix for this failing build",
		"implement the fix for this broken test", "resolve the failing test",
		"resolve the failing tests",
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
	// Creating a concrete runnable artifact can be judged by observed changes
	// followed by a repository-level check even when the user did not name a
	// filename (for example, a portfolio website in an empty workspace).
	return behavioralCreationObjective.MatchString(strings.TrimSpace(normalized))
}

var behavioralCreationObjective = regexp.MustCompile(
	`\b(?:create|build|scaffold|generate|implement|write)\b.{0,64}\b(?:website|web site|web app|web application|landing page|portfolio site)\b`)

func createsVerifiableArtifact(text string) bool {
	return behavioralCreationObjective.MatchString(normalizeRequest(text))
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
//
// A slash alone is not proof of a coordinate. "order/checkout flow" names a
// concept; "order/checkout.go", "the order/checkout directory", and
// "fix it in order/checkout" name filesystem objects. The distinction is
// load-bearing: a literal target demands target_state/target_content, while a
// concept is answered from the implementing files.
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
		if !literalPathTarget(text, match[0], match[1], value) {
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

// pathObjectNouns name a filesystem object in a request.
var pathObjectNouns = map[string]bool{
	"file": true, "files": true, "directory": true, "directories": true,
	"folder": true, "folders": true, "path": true, "paths": true,
	"dir": true, "dirs": true,
}

// pathCueVerbs are verbs that directly introduce a filesystem coordinate.
var pathCueVerbs = map[string]bool{
	"read": true, "open": true, "inspect": true, "browse": true, "list": true,
	"view": true, "cat": true, "show": true, "examine": true, "edit": true,
	"write": true, "create": true, "delete": true, "remove": true,
	"update": true, "modify": true, "change": true, "fix": true,
	"patch": true, "refactor": true, "add": true, "move": true,
	"rename": true, "copy": true, "run": true, "execute": true, "test": true,
	"build": true, "format": true, "check": true, "review": true,
	"explain": true, "describe": true, "understand": true,
}

// pathCuePrepositions locate a change or observation at a coordinate.
var pathCuePrepositions = map[string]bool{
	"in": true, "inside": true, "into": true, "within": true, "under": true,
	"at": true, "from": true, "to": true, "on": true, "of": true, "for": true,
}

// pathDeterminers may sit between a cue and the coordinate ("read the
// order/checkout"). The cue check looks through only one determiner.
var pathDeterminers = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true,
	"these": true, "those": true, "our": true, "its": true, "your": true,
}

// literalPathTarget reports whether a slash token is a filesystem coordinate
// rather than a conceptual compound.
func literalPathTarget(text string, start, end int, value string) bool {
	if looksLikeFile(value) || strings.HasSuffix(value, "/") {
		return true
	}
	if backtickedAt(text, start, end) {
		return true
	}
	next := nextSentenceWord(text, end)
	if pathObjectNouns[next] {
		return true
	}
	// A concept noun is stronger than a preceding path verb: "fix the
	// order/checkout flow" names the flow, while "fix order/checkout" names a
	// location.
	if repositoryConceptNouns[next] {
		return false
	}
	cue := previousCueWord(text, start)
	return pathCueVerbs[cue] || pathCuePrepositions[cue] || pathObjectNouns[cue]
}

// backtickedAt reports whether the token at [start,end) is backticked.
func backtickedAt(text string, start, end int) bool {
	return start > 0 && end < len(text) && text[start-1] == '`' && text[end] == '`'
}

// nextSentenceWord returns the word immediately after end, or "" when
// punctuation intervenes. Only a direct modifier position is evidence.
func nextSentenceWord(text string, end int) string {
	i := end
	for i < len(text) && isSpaceByte(text[i]) {
		i++
	}
	if i >= len(text) {
		return ""
	}
	start := i
	for i < len(text) && isWordByte(text[i]) {
		i++
	}
	if i == start {
		return ""
	}
	return strings.ToLower(text[start:i])
}

// previousCueWord returns the word immediately before start. When that word is
// a determiner, it looks one word further so "read the order/checkout" keeps
// its cue.
func previousCueWord(text string, start int) string {
	i := start
	for i > 0 && isSpaceByte(text[i-1]) {
		i--
	}
	end := i
	for i > 0 && isWordByte(text[i-1]) {
		i--
	}
	word := strings.ToLower(strings.TrimSpace(text[i:end]))
	if !pathDeterminers[word] {
		return word
	}
	i = i - 1
	for i > 0 && isSpaceByte(text[i]) {
		i--
	}
	end = i
	for i > 0 && isWordByte(text[i-1]) {
		i--
	}
	return strings.ToLower(strings.TrimSpace(text[i:end]))
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_' || b == '-'
}

// repositoryConceptNouns name behaviors or components whose answer lives in
// the implementation. They turn a slash compound into a topic, not a path.
var repositoryConceptNouns = map[string]bool{
	"flow": true, "flows": true, "pipeline": true, "pipelines": true,
	"handler": true, "handlers": true, "service": true, "services": true,
	"module": true, "modules": true, "subsystem": true, "subsystems": true,
	"endpoint": true, "endpoints": true, "controller": true, "controllers": true,
	"worker": true, "workers": true, "middleware": true, "router": true,
	"routers": true, "parser": true, "parsers": true, "validator": true,
	"validators": true, "lifecycle": true, "logic": true, "behavior": true,
	"behaviour": true, "event": true, "events": true, "job": true,
	"jobs": true, "process": true, "processes": true, "workflow": true,
	"workflows": true, "mechanism": true,
}

// extractConceptTopics returns named repository concepts in the request.
//
// It is deliberately conservative. A bare slash compound is not enough: the
// phrase must use a definite/demonstrative reference, an understanding/request
// head, or a following concept noun. General-knowledge framing ("a checkout
// flow", "how do checkout flows work") is not a repository topic.
func extractConceptTopics(text string) []string {
	tokens := conceptTokens(stripPossessives(text))
	var out []string
	add := func(value string) {
		value = cleanEntity(value)
		if value != "" {
			out = appendUnique(out, value)
		}
	}
	for i := 1; i < len(tokens); i++ {
		noun := strings.ToLower(tokens[i])
		if !repositoryConceptNouns[noun] {
			continue
		}
		modifier := strings.ToLower(tokens[i-1])
		before := ""
		if i >= 2 {
			before = strings.ToLower(tokens[i-2])
		}
		if conceptTopicPhrase(modifier, before) {
			add(strings.Join(tokens[i-1:i+1], " "))
		}
	}
	// A slash compound can stand on its own as a definite noun ("explain the
	// order/checkout"). Skip it when a concept noun already consumed it.
	for i, token := range tokens {
		if !conceptSlashStandalone(token) {
			continue
		}
		if i+1 < len(tokens) && repositoryConceptNouns[strings.ToLower(tokens[i+1])] {
			continue
		}
		before := ""
		if i > 0 {
			before = strings.ToLower(tokens[i-1])
		}
		if conceptReferenceWords[before] || conceptRequestHeads[before] {
			add(token)
		}
	}
	return out
}

// conceptTopicPhrase reports whether modifier+noun is a named repository
// concept rather than a generic noun phrase.
func conceptTopicPhrase(modifier, before string) bool {
	if modifier == "" || conceptModifierStopWords[modifier] ||
		conceptStructuralWords[modifier] || conceptRepositoryNouns[modifier] {
		return false
	}
	if strings.Contains(modifier, "/") {
		return conceptSlashModifier(modifier) && !conceptIndefiniteOrAux[before]
	}
	if len(modifier) < 3 {
		return false
	}
	if conceptReferenceWords[before] || conceptRequestHeads[before] ||
		conceptRepositoryNouns[before] || conceptGenericQualifiers[before] ||
		before == "" {
		return true
	}
	return false
}

// conceptGenericQualifiers describe the concept without making it generic.
// "the main checkout flow" still names this repository's flow.
var conceptGenericQualifiers = map[string]bool{
	"main": true, "primary": true, "core": true, "central": true,
	"general": true, "overall": true, "full": true, "complete": true,
	"whole": true, "basic": true, "simple": true, "typical": true,
	"normal": true, "new": true, "current": true, "existing": true,
	"legacy": true, "old": true,
}

// conceptTokens keeps the user's case and slash compounds intact, so an
// acronym such as TCP/IP stays distinguishable from a repository concept.
func conceptTokens(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return !isConceptByte(r) })
}

func isConceptByte(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '/'
}

// stripPossessives removes an English possessive so "this project's checkout
// flow" exposes "project checkout flow" to the modifier window.
func stripPossessives(text string) string {
	text = strings.ReplaceAll(text, "'s", "")
	return strings.ReplaceAll(text, "’s", "")
}

// conceptReferenceWords point at a contextually identified object.
var conceptReferenceWords = map[string]bool{
	"the": true, "this": true, "that": true, "these": true, "those": true,
	"our": true, "its": true, "your": true, "their": true, "my": true,
}

// conceptRequestHeads introduce what the user wants to understand or inspect.
var conceptRequestHeads = map[string]bool{
	"how": true, "what": true, "why": true, "when": true, "where": true,
	"which": true, "who": true, "whether": true, "explain": true,
	"explains": true, "explaining": true, "understand": true,
	"understanding": true, "describe": true, "describes": true,
	"analyze": true, "analyse": true, "analyzes": true, "analyses": true,
	"inspect": true, "inspection": true, "investigate": true,
	"investigation": true, "review": true, "read": true, "show": true,
	"list": true, "tell": true, "summarize": true, "summarise": true,
	"summary": true, "outline": true, "walk": true, "check": true,
	"explore": true, "examine": true,
}

// conceptIndefiniteOrAux rejects generic or question framing before a slash
// compound: "a client/server flow" and "how do checkout flows work" are not
// this repository's named concept.
var conceptIndefiniteOrAux = func() map[string]bool {
	words := []string{
		"a", "an", "some", "any", "all", "every", "each", "no", "not",
		"do", "does", "did", "is", "are", "was", "were", "be", "been",
		"being", "can", "could", "will", "would", "should", "may", "might",
		"must", "has", "have", "had",
	}
	out := make(map[string]bool, len(words))
	for _, word := range words {
		out[word] = true
	}
	return out
}()

// conceptModifierStopWords are grammar, generic qualifiers, and verbs that do
// not name a repository concept.
var conceptModifierStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true,
	"these": true, "those": true, "our": true, "its": true, "your": true,
	"their": true, "my": true, "main": true, "general": true, "overall": true,
	"full": true, "whole": true, "entire": true, "complete": true,
	"basic": true, "simple": true, "typical": true, "normal": true,
	"new": true, "current": true, "existing": true, "same": true,
	"different": true, "some": true, "any": true, "all": true, "every": true,
	"each": true, "no": true, "not": true, "and": true, "or": true,
	"but": true, "for": true, "with": true, "from": true, "into": true,
	"onto": true, "over": true, "under": true, "about": true, "of": true,
	"to": true, "in": true, "on": true, "at": true, "by": true, "then": true,
	"also": true, "only": true, "just": true, "very": true, "more": true,
	"most": true, "such": true, "than": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"do": true, "does": true, "did": true, "can": true, "could": true,
	"will": true, "would": true, "should": true, "may": true, "might": true,
	"must": true, "has": true, "have": true, "had": true,
}

// conceptStructuralWords describe the repository as a whole; they must not
// narrow a broad survey into one concept.
var conceptStructuralWords = map[string]bool{
	"architecture": true, "architectural": true, "structure": true,
	"structural": true, "design": true, "component": true, "components": true,
	"stack": true, "technology": true, "technologies": true, "layout": true,
	"organization": true, "organisation": true,
}

// conceptRepositoryNouns are the repository itself, not a modifier of one
// concept. "project flow" is too close to a whole-project question to become a
// narrow topic implicitly.
var conceptRepositoryNouns = map[string]bool{
	"project": true, "repository": true, "repo": true, "codebase": true,
	"workspace": true, "system": true, "application": true, "app": true,
	"program": true, "product": true, "tool": true, "code": true,
}

// nonConceptSlashTokens are common non-repository compounds.
var nonConceptSlashTokens = map[string]bool{
	"and/or": true, "w/o": true, "n/a": true, "km/h": true, "mi/h": true,
	"m/s": true, "ft/s": true, "kb/s": true, "mb/s": true, "gb/s": true,
}

// conceptSlashModifier reports whether a slash token can be a concept modifier.
func conceptSlashModifier(token string) bool {
	if !strings.Contains(token, "/") || token == "/" ||
		strings.Contains(token, "//") || strings.HasPrefix(token, "/") ||
		strings.HasSuffix(token, "/") {
		return false
	}
	lower := strings.ToLower(token)
	if nonConceptSlashTokens[lower] || strings.Contains(lower, "http") ||
		strings.Contains(lower, "www.") {
		return false
	}
	return true
}

// conceptSlashStandalone reports whether a slash token is a definite concept on
// its own. All-uppercase compounds (TCP/IP) and ratios are general knowledge
// unless a concept noun follows them.
func conceptSlashStandalone(token string) bool {
	if !conceptSlashModifier(token) {
		return false
	}
	letters, upper := 0, 0
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
			letters++
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
		}
	}
	return letters > 0 && upper != letters
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
	"one": true, "two": true, "three": true, "four": true, "five": true,
	"six": true, "seven": true, "eight": true, "nine": true, "ten": true,
}

// symbolStopWords are English words and product names that happen to look like
// identifiers. A hosting platform or product named in a request is a location
// or context, not a repository coordinate: demanding an index hit for "GitHub"
// would make the evidence requirement impossible to satisfy.
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
	"github": true, "gitlab": true, "bitbucket": true, "npm": true, "pypi": true,
	"dockerhub": true, "openai": true, "anthropic": true, "google": true,
	"microsoft": true, "amazon": true, "aws": true, "azure": true, "gcp": true,
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
