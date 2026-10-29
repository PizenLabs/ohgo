package workspace

import (
	"regexp"
	"strings"
	"unicode"
)

// A proposal is a decision the request leaves genuinely unresolved. It is not
// a project-type questionnaire: the control plane names the dimension the
// request itself opened, quotes the request's own object, and offers the
// resolutions that dimension admits.
//
// The type is deliberately data-only. It carries no authority: the human
// resolves it through the permission surface, and the resulting resolution is
// recorded as trusted state before execution.

// ProposalDimension names the kind of unresolved decision. It is a closed set
// because the control plane must know how to validate the resolutions it
// offers; it is not a registry of project kinds.
type ProposalDimension string

const (
	// ProposalStructure means the request asks for new work but determines
	// neither where it lives nor how it is laid out.
	ProposalStructure ProposalDimension = "structure"
	// ProposalAlternative means the request itself names competing
	// approaches without choosing between them.
	ProposalAlternative ProposalDimension = "alternative"
)

// Stable option identities. The structure options are the resolution space of
// ProposalStructure; alternative-* are produced from the request's own text.
const (
	// ProposalOptionOneFile keeps the requested work self-contained.
	ProposalOptionOneFile = "structure-one-file"
	// ProposalOptionMultipleFiles splits the requested work across files.
	ProposalOptionMultipleFiles = "structure-multiple-files"
	// ProposalOptionModelDecides delegates the unresolved decision to the
	// model with the human's explicit consent. It is always last so a stray
	// Enter delegates rather than committing to a shape.
	ProposalOptionModelDecides = "model-decides"
)

// ProposalOption is one resolution the human may choose. Directive is the
// control-plane-authored instruction the choice applies to execution; an
// empty directive means the human delegated the decision, so nothing is
// imposed on the model.
type ProposalOption struct {
	// ID is the machine identity, stable for tests and provenance.
	ID string
	// Label is the human-facing choice.
	Label string
	// Directive is the execution constraint the choice establishes.
	Directive string
}

// Proposal is an authorized request whose execution path depends on a
// decision the request did not make.
//
// Request is the exact user text, so the resolution can be bound to the turn
// it answers. Subject is an excerpt of that text, never a project category.
type Proposal struct {
	// Request is the exact user request the proposal answers.
	Request string
	// Dimension is the unresolved decision axis.
	Dimension ProposalDimension
	// Verb is the request's leading verb, when it has one.
	Verb string
	// Subject is the requested work, quoted from the request.
	Subject string
	// Question is the contextual ask, derived from Subject.
	Question string
	// Reason explains why the decision is needed, derived from Subject.
	Reason string
	// Options are the resolutions of Dimension, in display order. The last
	// is the conservative delegation.
	Options []ProposalOption
}

// Choice returns the option at index, or false when the selection is outside
// the proposal's resolution space.
func (p Proposal) Choice(index int) (ProposalOption, bool) {
	if index < 0 || index >= len(p.Options) {
		return ProposalOption{}, false
	}
	return p.Options[index], true
}

// ProposalFacts is the generic workspace evidence the assessment consults.
// It describes presence, never project type: a workspace with visible
// entries has a convention to inherit, an empty one does not.
type ProposalFacts struct {
	// HasConvention reports whether the workspace already contains visible
	// entries whose shape the request can inherit.
	HasConvention bool
	// VisibleEntries is the count consulted, retained for diagnostics.
	VisibleEntries int
}

// AssessProposal decides whether an authorized request leaves a materially
// unresolved execution decision and, if so, describes that decision in the
// request's own terms.
//
// It returns true only when the active workspace is the mutation authority, the
// request asks for a repository change, and the work is not anchored by a
// behavioural check. Within that boundary, either:
//
//   - the request names competing approaches without choosing between them;
//     or
//   - the request names no targets, the workspace establishes no convention,
//     the request declares no structure, and it asks for new work whose
//     concrete object it does not say how to lay out.
//
// The dimension decides the question. A language, framework, artifact kind, or
// project category is never consulted, so the same code path answers every
// request class with the same rules and different request-derived wording.
func AssessProposal(active Workspace, text string, facts ProposalFacts) (Proposal, bool) {
	none := Proposal{}
	if active != Build || !RequestsMutation(text) {
		return none, false
	}
	contract := BuildIntentContract(text)
	if contract.Kind != ObjectiveMutation {
		return none, false
	}
	if (contract.BehavioralObjective || hasVerificationScope(contract.Verification)) && !createsVerifiableArtifact(text) {
		return none, false
	}
	// The request's own alternatives are the strongest evidence of an
	// unresolved decision. They outrank a target or an existing convention:
	// the user explicitly named two paths and did not choose. They also
	// outrank structure declarations: "one file vs multiple files" is an
	// explicit choice, not a declaration.
	if proposal, ok := alternativeProposal(text); ok {
		return proposal, true
	}
	// A named target already answers "what exactly is this about"; a
	// structural question would be interrogation, not clarification.
	if len(contract.Targets) > 0 {
		return none, false
	}
	if facts.HasConvention {
		return none, false
	}
	if declaresStructure(text) {
		return none, false
	}
	return structureProposal(text)
}

// structureProposal builds a layout decision for new work whose object is
// concrete enough to name but whose structure the request leaves open.
func structureProposal(text string) (Proposal, bool) {
	verb, subject := requestObject(text)
	if subject == "" {
		return Proposal{}, false
	}
	if !creationHeads[verb] && !headMatches(creationHeads, verb) {
		return Proposal{}, false
	}
	if !concreteSubject(subject) {
		return Proposal{}, false
	}
	question := `How should I organize "` + subject + `"?`
	reason := `The request asks to ` + verb + ` "` + subject +
		`", but it does not say how that work should be laid out, and this workspace has no convention to inherit.`
	return Proposal{
		Request:   text,
		Dimension: ProposalStructure,
		Verb:      verb,
		Subject:   subject,
		Question:  question,
		Reason:    reason,
		Options: []ProposalOption{
			{
				ID:        ProposalOptionOneFile,
				Label:     "One file",
				Directive: `Keep "` + subject + `" in one self-contained file.`,
			},
			{
				ID:        ProposalOptionMultipleFiles,
				Label:     "Multiple files",
				Directive: `Split "` + subject + `" across multiple files as appropriate.`,
			},
			{
				ID:        ProposalOptionModelDecides,
				Label:     "Let the model decide",
				Directive: "",
			},
		},
	}, true
}

// alternativeProposal builds a choice from two approaches the request itself
// names. The options are the user's words, not a fixed vocabulary.
func alternativeProposal(text string) (Proposal, bool) {
	left, right, ok := splitAlternatives(text)
	if !ok {
		return Proposal{}, false
	}
	subject := alternativeSubject(text)
	verb, _ := requestObject(text)

	question := "Which approach should I use?"
	reason := "The request names two approaches but does not say which one to take."
	if subject != "" {
		question = `Which approach should I use for "` + subject + `"?`
		reason = `The request names two approaches for "` + subject + `" but does not say which one to take.`
	}
	return Proposal{
		Request:   text,
		Dimension: ProposalAlternative,
		Verb:      verb,
		Subject:   subject,
		Question:  question,
		Reason:    reason,
		Options: []ProposalOption{
			{
				ID:        "alternative-1",
				Label:     sentenceCase(left),
				Directive: "Take the approach: " + sentenceCase(left) + ".",
			},
			{
				ID:        "alternative-2",
				Label:     sentenceCase(right),
				Directive: "Take the approach: " + sentenceCase(right) + ".",
			},
			{
				ID:        ProposalOptionModelDecides,
				Label:     "Let the model decide",
				Directive: "",
			},
		},
	}, true
}

// splitAlternatives extracts the two approaches a request names.
//
// It recognises explicit choice syntax only ("A vs B", "A versus B",
// "either A or B"). A bare "or" is deliberately not enough: it is far more
// often a list than a decision the human expects to be asked about.
func splitAlternatives(text string) (string, string, bool) {
	collapsed := strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(collapsed)

	if start := eitherStart(lower); start >= 0 {
		rest := collapsed[start:]
		restLower := lower[start:]
		if orIdx := strings.Index(restLower, " or "); orIdx > 0 {
			left := trimAlternative(rest[:orIdx])
			right := trimAlternative(firstClause(rest[orIdx+len(" or "):]))
			if validAlternatives(left, right) {
				return left, right, true
			}
		}
	}

	for _, marker := range []string{" versus ", " vs "} {
		idx := strings.Index(lower, marker)
		if idx <= 0 {
			continue
		}
		left := trimAlternative(lastClause(collapsed[:idx]))
		right := trimAlternative(firstClause(collapsed[idx+len(marker):]))
		if validAlternatives(left, right) {
			return left, right, true
		}
	}
	return "", "", false
}

// eitherStart returns the index just after an "either" connector, whether it
// opens the request or follows its object.
func eitherStart(lower string) int {
	if strings.HasPrefix(lower, "either ") {
		return len("either ")
	}
	if idx := strings.Index(lower, " either "); idx >= 0 {
		return idx + len(" either ")
	}
	return -1
}

// lastClause isolates the alternative immediately before a marker. A sentence
// or dash boundary is authoritative; otherwise a "by" that introduces the
// approach is the boundary. The input may still include the request's leading
// verb and object when neither exists, so trimAlternative handles the rest.
func lastClause(s string) string {
	best := -1
	for _, sep := range []string{". ", "; ", " — ", " – ", " -- ", ": "} {
		if idx := strings.LastIndex(s, sep); idx > best {
			best = idx + len(sep)
		}
	}
	if best >= 0 {
		s = s[best:]
	} else if idx := strings.LastIndex(strings.ToLower(s), " by "); idx >= 0 {
		s = s[idx+len(" by "):]
	}
	return s
}

// firstClause isolates the alternative immediately after a marker, stopping
// at the first sentence boundary rather than swallowing the rest of the
// request.
func firstClause(s string) string {
	cut := len(s)
	for _, sep := range []string{". ", "; ", "\n", ", and "} {
		if idx := strings.Index(s, sep); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	return s[:cut]
}

// trimAlternative removes request scaffolding from one approach.
func trimAlternative(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, " .,;:!?—–-\t\n")
	for _, prefix := range []string{"either ", "or ", "by ", "to "} {
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			s = strings.TrimSpace(s[len(prefix):])
		}
	}
	return strings.Trim(s, " .,;:!?—–-\t\n")
}

// validAlternatives rejects fragments that cannot be a real decision.
func validAlternatives(left, right string) bool {
	if left == "" || right == "" || strings.EqualFold(left, right) {
		return false
	}
	if len(left) > 120 || len(right) > 120 {
		return false
	}
	return wordCount(left) >= 2 && wordCount(right) >= 2
}

// alternativeSubject names the work the alternatives belong to, when the
// request states it before the choice. It never invents one.
func alternativeSubject(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(collapsed)
	cut := len(collapsed)
	for _, marker := range []string{" versus ", " vs "} {
		if idx := strings.Index(lower, marker); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	if idx := strings.Index(lower, " either "); idx >= 0 && idx < cut {
		cut = idx
	}
	if strings.HasPrefix(lower, "either ") {
		cut = 0
	}
	prefix := collapsed[:cut]
	// A "by" that follows the object introduces the alternative itself:
	// "implement config loading by extending ... vs ...".
	if idx := strings.LastIndex(strings.ToLower(prefix), " by "); idx >= 0 {
		prefix = prefix[:idx]
	}
	// Only a sentence, dash, semicolon, or comma boundary leaves the object
	// behind; otherwise the prefix is the first alternative, not a subject.
	boundary := -1
	for _, sep := range []string{". ", "; ", " — ", " – ", " -- ", ": "} {
		if idx := strings.LastIndex(prefix, sep); idx > boundary {
			boundary = idx
		}
	}
	if idx := strings.LastIndex(prefix, ", "); idx > boundary && wordCount(prefix[:idx]) >= 3 {
		boundary = idx
	}
	if boundary < 0 {
		// A trailing comma closes the object in "implement config loading,
		// either ..."; without one the prefix is the first alternative and
		// must not be mistaken for the subject.
		trimmed := strings.TrimSpace(prefix)
		if !strings.HasSuffix(trimmed, ",") {
			return ""
		}
		_, subject := requestObject(strings.TrimSuffix(trimmed, ","))
		return subject
	}
	_, subject := requestObject(prefix[:boundary])
	return subject
}

// requestObject extracts the leading verb and the requested object from a
// request, preserving the user's wording. It does not classify the object.
func requestObject(text string) (string, string) {
	s := strings.Join(strings.Fields(text), " ")
	s = stripRequestPrefix(s)
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", ""
	}
	verb := strings.ToLower(fields[0])
	if isRequestVerb(verb) {
		s = strings.TrimSpace(s[len(fields[0]):])
	}
	// A sentence boundary ends the object; a comma usually starts context
	// ("..., software engineer"), not part of the thing to build.
	cut := len(s)
	for _, sep := range []string{". ", "; ", "\n", " — ", " – ", " -- "} {
		if idx := strings.Index(s, sep); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	if idx := strings.Index(s, ", "); idx >= 0 && wordCount(s[:idx]) >= 3 && idx < cut {
		cut = idx
	}
	s = strings.TrimSpace(s[:cut])
	s = strings.Trim(s, " .,;:!?\"'")
	for _, article := range []string{"a ", "an ", "the "} {
		if len(s) > len(article) && strings.EqualFold(s[:len(article)], article) {
			s = strings.TrimSpace(s[len(article):])
			break
		}
	}
	s = strings.Trim(s, " .,;:!?\"'")
	if s == "" {
		return verb, ""
	}
	// Keep the dialog readable while preserving the user's words.
	if len([]rune(s)) > 120 {
		s = truncateWords(s, 120)
	}
	return verb, s
}

// stripRequestPrefix removes request wrappers without changing the user's
// object wording. It is case-insensitive because the display copy preserves
// the original capitalisation.
func stripRequestPrefix(s string) string {
	for {
		lower := strings.ToLower(s)
		stripped := s
		for _, prefix := range politenessPrefixes {
			if strings.HasPrefix(lower, prefix+" ") {
				stripped = strings.TrimSpace(s[len(prefix)+1:])
				break
			}
		}
		if stripped == s {
			return s
		}
		s = stripped
	}
}

// isRequestVerb reports whether token is a verb the classifier recognises as
// carrying a request. "make" is included because mutationPhrases recognises
// "make it work" even though it is not a mutation head.
func isRequestVerb(token string) bool {
	if token == "make" {
		return true
	}
	return headMatches(mutationHeads, token) ||
		headMatches(executeHeads, token) ||
		headMatches(investigateHeads, token) ||
		headMatches(readHeads, token)
}

// creationHeads are the leading verbs whose work starts from nothing, so an
// unspecified layout is material. Modification verbs ("fix", "refactor",
// "rename") inherit their location from the code they change and are not here.
var creationHeads = map[string]bool{
	"create": true, "build": true, "generate": true, "scaffold": true,
	"init": true, "initialize": true, "bootstrap": true, "write": true,
	"add": true, "implement": true, "introduce": true, "make": true,
	"start": true, "develop": true, "design": true, "compose": true,
	"port": true, "convert": true, "migrate": true, "seed": true,
	"assemble": true, "produce": true, "construct": true,
}

// ordinaryCodeUnitWords name an implementation unit rather than a requested
// artifact. A subject headed by one of them is ordinary coding detail, so a
// layout question would be interrogation. This suppresses; it never triggers.
var ordinaryCodeUnitWords = map[string]bool{
	"it": true, "this": true, "that": true, "these": true, "those": true,
	"them": true, "something": true, "anything": true, "things": true,
	"stuff": true, "work": true, "code": true, "changes": true,
	"improvement": true, "improvements": true,
	"test": true, "tests": true,
	"function": true, "functions": true, "method": true, "methods": true,
	"class": true, "classes": true, "variable": true, "variables": true,
	"constant": true, "constants": true, "helper": true, "helpers": true,
	"utility": true, "utilities": true, "comment": true, "comments": true,
	"doc": true, "docs": true, "documentation": true, "readme": true,
	"bug": true, "bugs": true, "error": true, "errors": true,
	"log": true, "logs": true, "fix": true, "fixes": true,
	"change": true,
}

// vagueSubjectWords are weak nouns that name no artifact on their own. They
// suppress only when the whole subject is vague: "support" alone is not a
// thing to lay out, but "support for a configuration format" is.
var vagueSubjectWords = map[string]bool{
	"support": true, "feature": true, "functionality": true, "new": true,
	"a": true, "an": true, "the": true, "for": true, "of": true,
	"to": true, "with": true, "and": true, "or": true,
}

// concreteSubject reports whether the object names a thing rather than a
// pronoun, an ordinary code unit, or a bare abstraction.
func concreteSubject(subject string) bool {
	words := strings.Fields(strings.ToLower(subject))
	if len(words) == 0 {
		return false
	}
	head := strings.Trim(words[0], ".,;:!?\"'")
	if ordinaryCodeUnitWords[head] {
		return false
	}
	// "implement the requested changes" is still ordinary change work even
	// though its head is not the code-unit noun.
	last := strings.Trim(words[len(words)-1], ".,;:!?\"'")
	if ordinaryCodeUnitWords[last] {
		return false
	}
	allVague := true
	for _, word := range words {
		word = strings.Trim(word, ".,;:!?\"'")
		if word == "" {
			continue
		}
		if !vagueSubjectWords[word] && !ordinaryCodeUnitWords[word] {
			allVague = false
			break
		}
	}
	return !allVague
}

func wordCount(s string) int { return len(strings.Fields(s)) }

// truncateWords bounds a quoted subject without cutting a word in half.
func truncateWords(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	cut := string(runes[:limit])
	if idx := strings.LastIndex(cut, " "); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimSpace(cut) + "…"
}

// sentenceCase capitalises the first rune of an option label. The rest keeps
// the user's own wording.
func sentenceCase(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// declaresStructure reports whether the request itself determines the
// execution structure through generic structural markers: file-count and
// layout phrases, "as a <shape>" nouns, or a slash-enumerated multi-part
// stack. The markers are structural-linguistic, never domain-specific: no
// language, framework, or artifact name appears here.
func declaresStructure(text string) bool {
	normalized := " " + stripPoliteness(normalizeRequest(text)) + " "
	for _, phrase := range structurePhrases {
		if strings.Contains(normalized, " "+phrase+" ") {
			return true
		}
	}
	for _, noun := range structureKindNouns {
		if strings.Contains(normalized, " as a "+noun+" ") ||
			strings.Contains(normalized, " as an "+noun+" ") {
			return true
		}
	}
	return slashCompoundPattern.MatchString(strings.ToLower(text))
}

// structurePhrases are generic file-count and layout markers. Each names a
// shape (one file, several files, a new file, a split), never a project type
// or artifact.
var structurePhrases = []string{
	"single file",
	"one file",
	"in one file",
	"as one file",
	"multiple files",
	"multi file",
	"separate files",
	"separate file",
	"split into",
	"split across",
	"across files",
	"across multiple files",
	"each in its own",
	"own file",
	"own files",
	"new file",
	"additional file",
	"another file",
	"one module",
	"separate modules",
}

// structureKindNouns are the generic shape nouns a request may cast its work
// "as". The set is closed and structural: a noun outside it (a role, a
// person, a technology) is not a structure declaration. These are
// user-evidence markers: they suppress a proposal only when the user says the
// shape themselves.
var structureKindNouns = []string{
	"package", "module", "library", "script", "binary", "tool",
	"file", "service", "app", "application", "cli", "program",
}

// slashCompoundPattern matches a slash-enumerated multi-part stack of three
// or more parts, e.g. a "A/B/C" stack named inline. Three parts is the floor:
// a two-part token is as likely a choice ("and/or") or a path as an explicit
// structure, and a bare path is already a named target handled earlier.
var slashCompoundPattern = regexp.MustCompile(`[a-z0-9]+(?:/[a-z0-9]+){2,}`)
