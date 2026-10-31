package workspace

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The grounding extractor is deterministic and finite. It does not try to
// understand the request; it preserves what the user explicitly named so the
// runtime can reject a model proposal that silently replaces it. Natural
// language interpretation remains the model's job; this file only builds the
// reference the model is checked against.

// pluralListPattern matches a request that names its projects/entities as a
// list: "the additional projects are k8s and opencode". The plural subject is
// load-bearing: "the project is a Go service" is a description, not a list.
var pluralListPattern = regexp.MustCompile(
	`(?is)\b(?:(?:additional|other|these|those|the)\s+)?(?:projects?|repos?|repositories|services?|modules?|components?|apps?|applications?|targets?)\s+(?:are|include|includes)\s+([^.;\n]+)`)

// colonListPattern matches "additional projects: k8s, opencode".
var colonListPattern = regexp.MustCompile(
	`(?is)\b(?:projects?|repos?|repositories|services?|modules?|components?|apps?|applications?|targets?)\s*:\s*([^.;\n]+)`)

// requestedListPattern matches a request whose object is a list:
// "check k8s and opencode as additional projects".
var requestedListPattern = regexp.MustCompile(
	`(?is)^\s*(?:please\s+)?(?:check|inspect|review|test|use|look at|investigate|work on|verify|explore|scan|examine)\s+(.+)$`)

// groundingInvestigateHeads are diagnostic verbs that ask for investigation
// without being in the router's mutation set. They are local to grounding: the
// router's authority classification is deliberately left unchanged.
var groundingInvestigateHeads = map[string]bool{
	"find": true, "diagnose": true, "debug": true, "troubleshoot": true,
	"investigate": true,
}

// unresolvedReferencePhrases mark a scope the request does not resolve. The
// runtime must not invent which repositories "the other two" means.
var unresolvedReferencePhrases = []string{
	"the other two", "the other project", "the other projects",
	"the other repo", "the other repos", "the other repository",
	"the other repositories", "the remaining", "those projects",
	"those repos", "those repositories", "these projects", "these repos",
	"these repositories",
}

// buildGrounding derives the structured semantic contract from the user's own
// request. It runs after the completion contract's kind and targets are known.
func buildGrounding(text string, intent contract.IntentContract) *contract.IntentGrounding {
	grounding := &contract.IntentGrounding{
		Objective:      strings.TrimSpace(text),
		MutationIntent: intent.Kind == contract.ObjectiveMutation,
		Action:         contract.GroundingUnknown,
	}
	actions := groundingActions(text)
	if len(actions) == 0 && grounding.MutationIntent {
		actions = append(actions, contract.GroundingMutate)
	}
	grounding.Actions = actions
	grounding.Action = primaryGroundingAction(actions, grounding.MutationIntent)

	grounding.Entities = groundingEntities(text, intent)
	grounding.Scope = groundingScope(text, grounding.Entities)
	grounding.Constraints = groundingConstraints(text)
	grounding.ExpectedOutcome = groundingOutcome(grounding)

	if grounding.Action == contract.GroundingUnknown {
		grounding.Ambiguities = append(grounding.Ambiguities, contract.Ambiguity{
			Kind:     "unknown_action",
			Detail:   "the request names no actionable verb",
			Material: true,
		})
		grounding.Assumptions = append(grounding.Assumptions, contract.Assumption{
			Text:  "the request expects a concrete action",
			Kind:  contract.AssumptionUnsupported,
			Basis: "the request contains no actionable verb",
		})
	}
	if unresolvedReference(text) {
		grounding.Ambiguities = append(grounding.Ambiguities, contract.Ambiguity{
			Kind:     "unresolved_reference",
			Detail:   "the request refers to entities it does not name",
			Material: true,
		})
	}
	lowerText := strings.ToLower(text)
	alternative := (strings.Contains(lowerText, " either ") || strings.HasPrefix(lowerText, "either ")) &&
		strings.Contains(lowerText, " or ")
	if alternative || strings.Contains(lowerText, " vs ") || strings.Contains(lowerText, " versus ") {
		grounding.Ambiguities = append(grounding.Ambiguities, contract.Ambiguity{
			Kind:     "unresolved_alternative",
			Detail:   "the request names competing approaches without choosing",
			Material: true,
		})
	}
	if len(grounding.Entities) > 0 {
		grounding.Assumptions = append(grounding.Assumptions, contract.Assumption{
			Text:  "the request names the preserved entities",
			Kind:  contract.AssumptionGrounded,
			Basis: "named explicitly in the request",
		})
	}
	if grounding.MutationIntent && len(intent.Targets) == 0 {
		grounding.Assumptions = append(grounding.Assumptions, contract.Assumption{
			Text:  "the requested object is a repository change area",
			Kind:  contract.AssumptionInferred,
			Basis: "the request asks for a change without naming a file",
		})
	}

	switch {
	case grounding.Action == contract.GroundingUnknown:
		grounding.Confidence = contract.GroundingConfidenceLow
	case grounding.HasMaterialAmbiguity():
		grounding.Confidence = contract.GroundingConfidenceMedium
	default:
		grounding.Confidence = contract.GroundingConfidenceHigh
	}
	return grounding
}

// groundingActions returns every action the request appears to ask for, in
// clause order, with duplicates removed.
//
// A clause that asks about an operation grounds READ, not the operation. The
// semantic contract must agree with the router here: if "does this update
// GitHub?" grounded MUTATE, semantic admission would consider a mutating call
// permitted under a question.
func groundingActions(text string) []contract.GroundingAction {
	request := stripPoliteness(normalizeRequest(text))
	if request == "" {
		return nil
	}
	var out []contract.GroundingAction
	add := func(action contract.GroundingAction) {
		for _, existing := range out {
			if existing == action {
				return
			}
		}
		out = append(out, action)
	}
	for _, entry := range requestClauses(request) {
		if negatedClause(entry.text) {
			// "Do not create it" constrains the request; it is not an action
			// the model should perform.
			continue
		}
		clauseText := stripNegatedTail(entry.text)
		if entry.asked {
			add(contract.GroundingRead)
			continue
		}
		head := clauseHead(clauseText)
		switch {
		case headMatches(mutationHeads, head), containsPhrase(clauseText, mutationPhrases):
			add(contract.GroundingMutate)
		case headMatches(investigateHeads, head), groundingInvestigateHeads[head],
			containsPhrase(clauseText, investigatePhrases):
			add(contract.GroundingInvestigate)
		case headMatches(executeHeads, head), containsPhrase(clauseText, executePhrases):
			add(contract.GroundingExecute)
		case headMatches(readHeads, head):
			add(contract.GroundingRead)
		}
	}
	return out
}

// negatedClause reports a clause that forbids an action rather than requesting
// one. The constraint is recorded separately; it must not become an action.
func negatedClause(clause string) bool {
	fields := strings.Fields(strings.ToLower(clause))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "do":
		return len(fields) > 1 && fields[1] == "not"
	case "don't", "dont", "never", "without":
		return true
	}
	return false
}

// stripNegatedTail removes a trailing prohibition from one clause so
// "tell me whether to create it, do not create it" does not carry the
// prohibition's verb into the action list.
func stripNegatedTail(clause string) string {
	lower := strings.ToLower(clause)
	cut := len(clause)
	for _, marker := range []string{" do not ", " don't ", " dont ", " never ", " without "} {
		if index := strings.Index(lower, marker); index >= 0 && index < cut {
			cut = index
		}
	}
	return strings.TrimSpace(clause[:cut])
}

// primaryGroundingAction chooses the action that governs completion. A
// mutation wins because it carries the strongest authority requirement; a
// compound request still keeps the other actions in Actions.
func primaryGroundingAction(actions []contract.GroundingAction, mutation bool) contract.GroundingAction {
	if mutation {
		return contract.GroundingMutate
	}
	for _, action := range actions {
		if action == contract.GroundingMutate {
			return action
		}
	}
	for _, action := range actions {
		if action == contract.GroundingInvestigate {
			return action
		}
	}
	if len(actions) > 0 {
		return actions[0]
	}
	return contract.GroundingUnknown
}

// groundingEntities returns the named entities the request contains. It
// combines the contract's repository coordinates with explicitly listed
// projects; it never invents a project from a generic noun.
func groundingEntities(text string, intent contract.IntentContract) []contract.Entity {
	var out []contract.Entity
	seen := map[string]bool{}
	add := func(value string, kind contract.EntityKind, optional bool) {
		value = cleanEntity(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, contract.Entity{Value: value, Kind: kind, Optional: optional})
	}

	for _, target := range intent.Targets {
		switch target.Kind {
		case contract.TargetPath:
			add(target.Value, contract.EntityPath, false)
		case contract.TargetSymbol:
			add(target.Value, contract.EntitySymbol, false)
		case contract.TargetPackage:
			add(target.Value, contract.EntityPackage, false)
		}
	}

	optional := conditionalRelevance(text)
	for _, value := range listedEntities(text) {
		add(value, contract.EntityProject, optional)
	}
	return out
}

// listedEntities extracts names from an explicit list construct.
func listedEntities(text string) []string {
	var lists []string
	for _, pattern := range []*regexp.Regexp{pluralListPattern, colonListPattern} {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				lists = append(lists, match[1])
			}
		}
	}
	if match := requestedListPattern.FindStringSubmatch(text); match != nil {
		// A requested object is only a list when it contains a coordinator and
		// no coordinated clause. "check k8s and opencode" is a list; "check
		// the API issue and update the documentation" is two clauses.
		if (listSeparatorPattern.MatchString(match[1]) || strings.Contains(match[1], ",")) &&
			!coordinatedClause(match[1]) {
			lists = append(lists, match[1])
		}
	}

	var out []string
	for _, list := range lists {
		for _, item := range splitEntityList(list) {
			out = appendUnique(out, item)
		}
	}
	return out
}

// coordinatedClause reports whether a captured object is really two clauses
// joined by a coordinator. The second clause beginning with a verb is the
// signal: "k8s and opencode" is a list, "the API issue and update ..." is not.
func coordinatedClause(value string) bool {
	parts := listSeparatorPattern.Split(value, -1)
	for i := 1; i < len(parts); i++ {
		words := strings.Fields(strings.TrimSpace(parts[i]))
		if len(words) == 0 {
			continue
		}
		if entityVerbHeads[strings.ToLower(strings.Trim(words[0], ".,;:!?\"'"))] {
			return true
		}
	}
	for _, part := range strings.Split(value, ",") {
		words := strings.Fields(strings.TrimSpace(part))
		if len(words) == 0 {
			continue
		}
		if entityVerbHeads[strings.ToLower(strings.Trim(words[0], ".,;:!?\"'"))] {
			return true
		}
	}
	return false
}

// entityVerbHeads are the clause-leading verbs that make a coordinated phrase
// two objectives rather than a list of named things.
var entityVerbHeads = map[string]bool{
	"update": true, "fix": true, "write": true, "create": true,
	"delete": true, "remove": true, "investigate": true, "find": true,
	"check": true, "read": true, "run": true, "build": true,
	"add": true, "change": true, "modify": true, "refactor": true,
	"implement": true, "clean": true, "explain": true, "describe": true,
	"review": true, "inspect": true, "verify": true, "test": true,
	"tell": true, "whether": true,
}

// listSeparatorPattern splits a list on its coordinator without depending on
// the user's casing.
var listSeparatorPattern = regexp.MustCompile(`(?i)\s+(?:and|&)\s+`)

// splitEntityList splits one list expression into validated entity names.
func splitEntityList(list string) []string {
	list = strings.TrimSpace(list)
	for _, suffix := range []string{
		" as additional projects", " as additional project",
		" as additional repos", " as additional repositories",
		" as well", " too", " also",
	} {
		if strings.HasSuffix(strings.ToLower(list), suffix) {
			list = strings.TrimSpace(list[:len(list)-len(suffix)])
		}
	}
	replaced := listSeparatorPattern.ReplaceAllString(list, ",")
	var out []string
	for _, item := range strings.Split(replaced, ",") {
		item = cleanEntity(item)
		if !validEntityName(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// cleanEntity trims scaffolding without changing the user's wording.
func cleanEntity(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`'\"()[]{}")
	value = strings.Trim(value, " .,;:!?—–-")
	value = strings.Join(strings.Fields(value), " ")
	lower := strings.ToLower(value)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			lower = strings.ToLower(value)
			break
		}
	}
	for _, suffix := range []string{
		" too", " as well", " also", " if relevant", " if needed",
		" as additional projects", " as additional project",
		" as additional repos", " as additional repositories",
	} {
		if strings.HasSuffix(lower, suffix) {
			value = strings.TrimSpace(value[:len(value)-len(suffix)])
			lower = strings.ToLower(value)
		}
	}
	return value
}

// validEntityName rejects fragments that are not a named thing.
func validEntityName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	words := strings.Fields(value)
	if len(words) == 0 || len(words) > 4 {
		return false
	}
	head := strings.ToLower(strings.Trim(words[0], ".,;:!?\"'"))
	if entityVerbHeads[head] {
		return false
	}
	lower := strings.ToLower(value)
	if entityStopWords[lower] {
		return false
	}
	if strings.HasPrefix(lower, "other ") || strings.HasPrefix(lower, "remaining ") ||
		strings.HasPrefix(lower, "all ") || strings.HasPrefix(lower, "both ") ||
		strings.HasPrefix(lower, "some ") || strings.HasPrefix(lower, "any ") ||
		strings.HasPrefix(lower, "each ") || strings.HasPrefix(lower, "every ") ||
		strings.HasPrefix(lower, "this ") || strings.HasPrefix(lower, "that ") ||
		strings.HasPrefix(lower, "these ") || strings.HasPrefix(lower, "those ") ||
		strings.HasPrefix(lower, "our ") || strings.HasPrefix(lower, "your ") ||
		strings.HasPrefix(lower, "my ") {
		return false
	}
	// An entity must contain a letter or digit. Punctuation-only fragments
	// come from a malformed list and would poison completion.
	hasValue := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			hasValue = true
			break
		}
	}
	return hasValue
}

// entityStopWords are generic list scaffolding, never a named entity.
var entityStopWords = map[string]bool{
	"project": true, "projects": true, "repo": true, "repos": true,
	"repository": true, "repositories": true, "service": true, "services": true,
	"module": true, "modules": true, "component": true, "components": true,
	"app": true, "apps": true, "application": true, "applications": true,
	"target": true, "targets": true, "them": true, "they": true, "it": true,
	"this": true, "that": true, "these": true, "those": true, "other": true,
	"additional": true, "current": true, "relevant": true, "same": true,
	"if": true, "use": true, "too": true, "also": true,
}

// groundingScope records the breadth the request establishes.
func groundingScope(text string, entities []contract.Entity) contract.GroundingScope {
	scope := contract.GroundingScope{Kind: contract.ScopeUnknown}
	if len(entities) > 0 {
		scope.Kind = contract.ScopeNamed
		for _, entity := range entities {
			scope.Values = append(scope.Values, entity.Value)
		}
	}
	normalized := " " + normalizeRequest(text) + " "
	for _, phrase := range []string{
		" this project ", " the project ", " this repository ", " the repository ",
		" this repo ", " the repo ", " this codebase ", " the codebase ",
		" this code ", " the code ", " this workspace ", " the workspace ",
	} {
		if strings.Contains(normalized, phrase) {
			if scope.Kind == contract.ScopeUnknown {
				scope.Kind = contract.ScopeRepository
			}
			break
		}
	}
	scope.Conditional = conditionalRelevance(text)
	return scope
}

// conditionalRelevance reports that the user made the named entities optional.
func conditionalRelevance(text string) bool {
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"if relevant", "if needed", "if applicable", "where relevant",
		"as needed", "use them if", "use it if", "only if",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// unresolvedReference reports a scope the request leaves unnamed.
func unresolvedReference(text string) bool {
	lower := strings.ToLower(text)
	for _, phrase := range unresolvedReferencePhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// groundingConstraints extracts explicit limits the request states.
func groundingConstraints(text string) []string {
	normalized := " " + normalizeRequest(text) + " "
	var out []string
	add := func(key string, phrases ...string) {
		for _, phrase := range phrases {
			if strings.Contains(normalized, " "+phrase+" ") {
				out = appendUnique(out, key)
				return
			}
		}
	}
	add("no_mutation", "do not modify", "do not change", "read only", "without modifying")
	add("no_creation", "do not create")
	add("no_external", "do not use the web", "do not browse", "no external")
	return out
}

// groundingOutcome states what a truthful completion would demonstrate.
func groundingOutcome(grounding *contract.IntentGrounding) string {
	switch {
	case grounding.MutationIntent:
		return "the named targets changed and a successful check followed"
	case grounding.Action == contract.GroundingExecute:
		return "the requested process reached a successful terminal state"
	case grounding.Action == contract.GroundingInvestigate:
		return "an answer grounded in observed evidence, without a global negative conclusion"
	case grounding.Action == contract.GroundingRead:
		return "an answer grounded in observed evidence that preserves the named entities"
	default:
		return "a clarification of the intended action, or an acknowledgement that preserves the named entities"
	}
}
