package workspace

import "strings"

// Intent is the semantic capability a user request asks for.
//
// Intent is not grammar. A question can carry execution intent ("can you run
// the tests?") and an imperative can be informational ("tell me what this
// does"). The runtime classifies the requested capability, not the sentence
// form, because natural language does not encode authority reliably (§6).
type Intent string

// The three semantic intents.
const (
	// IntentRead asks to understand: inspect, explain, compare, summarise.
	IntentRead Intent = "read"
	// IntentExecute asks to change or run something: implement, fix, run.
	IntentExecute Intent = "execute"
	// IntentInvestigate asks to probe, reproduce, or challenge behaviour.
	IntentInvestigate Intent = "investigate"
)

// Handler returns the workspace whose semantics answer the intent.
//
// The mapping is the authority contract: READ is answered by ASK, INVESTIGATE
// by FUZZ, EXECUTE by BUILD. It is a lower bound, never a licence — Route
// decides whether the request's active workspace permits reaching it.
func (i Intent) Handler() Workspace {
	switch i {
	case IntentExecute:
		return Build
	case IntentInvestigate:
		return Fuzz
	default:
		return Ask
	}
}

// Strength is how much evidence supports a prediction.
//
// It is a level, not a probability: the classifier has no calibrated score, so
// a caller must never render it as a percentage (§4, I12). The levels are
// ordered, and the ordering is the policy's only confidence input.
type Strength string

// The evidence levels, weakest first.
const (
	// StrengthAmbiguous means no signal matched (the classifier fell back to
	// READ's safety default) or a rival intent matched with evidence at least
	// as strong as the winner. Neither is a licence to act.
	StrengthAmbiguous Strength = "ambiguous"
	// StrengthModerate means exactly one intent matched, through a
	// corroborating phrase rather than a leading verb.
	StrengthModerate Strength = "moderate"
	// StrengthStrong means the winning intent matched through the request's
	// leading verb, uncontested. It is the separation a transition proposal
	// requires (§7).
	StrengthStrong Strength = "strong"
)

// AtLeast reports whether s carries at least the evidence of floor. The levels
// are totally ordered (ambiguous < moderate < strong), so a policy can state
// its threshold explicitly and a test can pin it.
func (s Strength) AtLeast(floor Strength) bool {
	return strengthRank(s) >= strengthRank(floor)
}

// strengthRank orders the levels. An unknown strength is ambiguous: a value
// the policy cannot place must not clear a threshold.
func strengthRank(s Strength) int {
	switch s {
	case StrengthStrong:
		return 2
	case StrengthModerate:
		return 1
	}
	return 0
}

// Prediction is the classifier's full result for one request.
//
// It separates what the request appears to ask for (Intent) from how strongly
// the classifier believes it (Strength). The caller decides what to do with
// that; the classifier itself never changes authority and never routes (§5).
type Prediction struct {
	// Intent is the classified capability, identical to Classify's result.
	Intent Intent
	// Strength is the evidence behind Intent.
	Strength Strength
	// Competing is the strongest intent other than Intent that produced any
	// signal, or "" when none did. It is a weaker signal unless Strength is
	// StrengthAmbiguous, where it names the contest.
	Competing Intent
}

// evidence is the tier of a single matched signal.
type evidence int

const (
	evidenceNone evidence = iota
	// evidencePhrase is a whole-word phrase match anywhere in the request.
	evidencePhrase
	// evidenceHead is a match on the request's leading verb.
	evidenceHead
)

// Classify returns the intent a request asks for.
//
// It is deterministic and deliberately small: no model, no embeddings (§7).
// A leading question word resolves to READ first, so "why does this use
// run()?" is informational even though it contains an execution verb.
//
// Ambiguity resolves downward. A request the classifier cannot place becomes
// READ, the lowest-authority interpretation, so a misclassification can only
// reduce authority, never raise it (§17). An explicit investigative signal
// wins over an execution verb, because a request that asks for both
// ("reproduce and fix") is safer served by FUZZ than by BUILD.
func Classify(text string) Intent { return ClassifyPrediction(text).Intent }

// ClassifyPrediction returns the intent and the evidence behind it.
//
// The precedence is Classify's contract; this function only adds the strength
// of the signal that decided it. A leading verb counts as strong evidence, a
// corroborating phrase as moderate, and no match at all as ambiguous — the
// READ fallback is a safety default, not a reading of the request.
func ClassifyPrediction(text string) Prediction {
	request := stripPoliteness(normalizeRequest(text))
	if request == "" {
		return Prediction{Intent: IntentRead, Strength: StrengthAmbiguous}
	}
	head, _, _ := strings.Cut(request, " ")

	// The evidence each intent actually has, independent of precedence.
	read := evidenceNone
	if readHeads[head] {
		read = evidenceHead
	}
	investigate := phraseEvidence(investigateHeads[head], containsPhrase(request, investigatePhrases))
	execute := phraseEvidence(executeHeads[head], containsPhrase(request, executePhrases))

	// Precedence: a question head is informational; investigate before
	// execute (FUZZ is the lower authority when a request asks for both); an
	// unmatched request narrows to READ.
	winner := evidenceNone
	intent := IntentRead
	switch {
	case read != evidenceNone:
		intent, winner = IntentRead, read
	case investigate != evidenceNone:
		intent, winner = IntentInvestigate, investigate
	case execute != evidenceNone:
		intent, winner = IntentExecute, execute
	}

	// The strongest rival signal. A prediction is contested when a rival is
	// at least as strong as the winner, and a contested prediction is never
	// strong enough to justify asking about.
	competing := Intent("")
	rival := evidenceNone
	for _, candidate := range []Intent{IntentRead, IntentInvestigate, IntentExecute} {
		if candidate == intent {
			continue
		}
		tier := read
		switch candidate {
		case IntentInvestigate:
			tier = investigate
		case IntentExecute:
			tier = execute
		}
		if tier > rival {
			rival, competing = tier, candidate
		}
	}
	if rival == evidenceNone {
		competing = ""
	}

	prediction := Prediction{Intent: intent, Competing: competing}
	switch {
	case winner == evidenceNone || rival >= winner:
		prediction.Strength = StrengthAmbiguous
	case winner == evidenceHead:
		prediction.Strength = StrengthStrong
	default:
		prediction.Strength = StrengthModerate
	}
	return prediction
}

// phraseEvidence ranks a leading-verb match above a phrase match.
func phraseEvidence(head, phrase bool) evidence {
	if head {
		return evidenceHead
	}
	if phrase {
		return evidencePhrase
	}
	return evidenceNone
}

// Outcome is whether a turn may run under its handler authority.
type Outcome string

// Outcomes.
const (
	// Allow means the turn runs under Route.Handler.
	Allow Outcome = "allow"
	// Require means the request needs an authority the active workspace does
	// not hold, so it must be refused until the user changes workspace
	// explicitly (§3, §18).
	Require Outcome = "require_authority"
)

// Route is the authority decision for one turn.
//
// It keeps the session's active workspace and the turn's semantic handler
// distinct: an informational turn delegated to ASK is handled with ASK
// semantics while the active workspace remains authoritative (§9, §10).
type Route struct {
	// Active is the session's authoritative workspace. A route never changes
	// it; only an explicit user switch does.
	Active Workspace
	// Handler is the workspace whose semantics answer this turn. It is always
	// at most Active's authority: delegation narrows, it never widens.
	Handler Workspace
	// Intent is the classified request.
	Intent Intent
	// Outcome is whether the turn may run.
	Outcome Outcome
	// Required names the workspace the request needs when Outcome is Require.
	Required Workspace
}

// Allowed reports whether the turn may run.
func (r Route) Allowed() bool { return r.Outcome == Allow }

// Delegated reports whether the turn is answered by a workspace other than
// the active one. It is how the UI stays truthful about a narrowed turn.
func (r Route) Delegated() bool { return r.Allowed() && r.Handler != r.Active }

// Effective is the authority actually available to this turn.
//
// A permitted route exercises its handler; a refused route executes nothing,
// so the active workspace remains the only authority that could apply. The
// result is never stronger than Active: a route narrows, it never widens.
func (r Route) Effective() Workspace {
	if !r.Allowed() {
		return r.Active
	}
	return r.Handler
}

// Indicator is the canonical turn-level projection, or "" when the active
// workspace itself answers.
//
// It names the persistent workspace first and the ephemeral handler second,
// so the relationship reads as "BUILD stays active; this turn is ASK/READ"
// rather than "the session became ASK" (§12).
func (r Route) Indicator() string {
	if !r.Delegated() {
		return ""
	}
	return r.Active.Label() + " · " + r.Handler.Label() + " / " + strings.ToUpper(string(r.Intent))
}

// Notice is the turn-level indicator for a delegated turn, or "" when the
// active workspace itself answers.
func (r Route) Notice() string { return r.Indicator() }

// Message is the user-facing explanation of a refused route.
func (r Route) Message() string {
	if r.Allowed() {
		return ""
	}
	if !r.Required.Valid() {
		return "no workspace authority is in force for this session"
	}
	return "this request needs " + r.Required.Label() +
		" authority; switch with /" + r.Required.String() + " and resend it"
}

// RouteTurn derives the authority decision for a request in the active
// workspace.
//
// The transition table is explicit and boring by design (§8, §24):
//
//	ACTIVE   INTENT        RESULT
//	ASK      READ          ASK (read-only)
//	ASK      EXECUTE       refused — requires an explicit BUILD switch
//	ASK      INVESTIGATE   refused — requires an explicit FUZZ switch
//	BUILD    READ          ASK semantics, BUILD remains active
//	BUILD    EXECUTE       BUILD
//	BUILD    INVESTIGATE   FUZZ semantics, BUILD remains active
//	FUZZ     READ          ASK semantics, FUZZ remains active
//	FUZZ     INVESTIGATE   FUZZ
//	FUZZ     EXECUTE       refused — requires an explicit BUILD switch
//
// The rule behind it: information may flow between workspaces, authority may
// not. ASK never escalates implicitly, FUZZ never becomes BUILD, and a
// delegated turn is always narrower than the workspace that delegated it.
func RouteTurn(active Workspace, text string) Route {
	intent := Classify(text)
	route := Route{Active: active, Handler: active, Intent: intent, Outcome: Allow}
	if !active.Valid() {
		// An unresolved workspace is denied, never defaulted to one of the
		// three (see Workspace's doc comment).
		route.Outcome = Require
		return route
	}
	switch active {
	case Ask:
		switch intent {
		case IntentExecute:
			route.Outcome, route.Required = Require, Build
		case IntentInvestigate:
			route.Outcome, route.Required = Require, Fuzz
		}
	case Build:
		switch intent {
		case IntentRead:
			route.Handler = Ask
		case IntentInvestigate:
			route.Handler = Fuzz
		}
	case Fuzz:
		switch intent {
		case IntentRead:
			route.Handler = Ask
		case IntentExecute:
			route.Outcome, route.Required = Require, Build
		}
	}
	return route
}

// normalizeRequest lowercases a request and collapses every run of
// non-word characters to a single space, so phrase matching is not defeated
// by trailing punctuation, code syntax, or doubled whitespace.
func normalizeRequest(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := false
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			space = false
			continue
		}
		if r == '\'' && !space {
			// Keep an intra-word apostrophe so "what's" stays one token.
			b.WriteRune(r)
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// stripPoliteness removes leading request wrappers so the first token is the
// verb that carries the intent ("can you run the tests" → "run the tests").
// It strips repeatedly, because wrappers stack ("please can you ...").
func stripPoliteness(request string) string {
	for {
		stripped := request
		for _, prefix := range politenessPrefixes {
			if strings.HasPrefix(request, prefix+" ") {
				stripped = strings.TrimSpace(request[len(prefix)+1:])
				break
			}
		}
		if stripped == request {
			return request
		}
		request = stripped
	}
}

// containsPhrase reports whether phrase occurs in text as a whole word
// sequence. Text and phrase are already normalized to space-separated words.
func containsPhrase(text string, phrases []string) bool {
	padded := " " + text + " "
	for _, phrase := range phrases {
		if strings.Contains(padded, " "+phrase+" ") {
			return true
		}
	}
	return false
}

// politenessPrefixes are request wrappers stripped before classification.
var politenessPrefixes = []string{
	"can you", "could you", "would you", "will you", "please",
	"i want you to", "i'd like you to", "i would like you to",
	"i need you to", "go ahead and", "help me",
	"now", "then",
}

// readHeads are leading tokens that mark an informational request. Over-
// including here is safe: a false READ only narrows authority.
var readHeads = map[string]bool{
	"what": true, "what's": true, "whats": true,
	"why": true, "why's": true,
	"how": true, "how's": true,
	"explain": true, "describe": true, "summarize": true, "summarise": true,
	"summary": true, "compare": true, "comparison": true,
	"show": true, "list": true, "where": true, "where's": true,
	"when": true, "which": true, "who": true, "who's": true, "whose": true,
	"is": true, "are": true, "was": true, "were": true,
	"does": true, "did": true, "can": true, "could": true,
	"would": true, "should": true, "will": true,
	"tell": true, "walk": true, "define": true, "clarify": true,
	"outline": true, "review": true, "analyze": true, "analyse": true,
	"inspect": true, "check": true, "discuss": true, "elaborate": true,
	"understand": true, "read": true, "give": true, "help": true,
}

// investigateHeads are leading verbs that ask to probe or reproduce rather
// than to understand or to change. They map to FUZZ semantics.
var investigateHeads = map[string]bool{
	"reproduce": true, "repro": true, "probe": true, "fuzz": true,
	"stress": true, "benchmark": true, "challenge": true, "break": true,
	"test": true, "attack": true, "exploit": true,
}

// executeHeads are leading verbs that ask for a change or a run. They map to
// BUILD semantics.
var executeHeads = map[string]bool{
	"implement": true, "fix": true, "modify": true, "change": true,
	"refactor": true, "restructure": true, "create": true, "add": true,
	"remove": true, "delete": true, "rename": true, "move": true,
	"apply": true, "run": true, "execute": true, "write": true,
	"patch": true, "edit": true, "update": true, "upgrade": true,
	"build": true, "rebuild": true, "install": true, "uninstall": true,
	"migrate": true, "commit": true, "revert": true, "replace": true,
	"introduce": true, "generate": true, "rewrite": true, "drop": true,
	"merge": true, "publish": true, "deploy": true, "bump": true,
	"wire": true, "format": true, "lint": true, "configure": true,
	"enable": true, "disable": true, "reset": true, "clean": true,
	"init": true, "initialize": true, "bootstrap": true, "scaffold": true,
	"vendor": true, "pin": true, "port": true, "convert": true,
	"seed": true, "compile": true, "bundle": true, "connect": true,
	"disconnect": true, "set": true,
}

// investigatePhrases are multi-word investigative signals that may appear
// anywhere in a non-question request ("the tests fail, try to reproduce it").
var investigatePhrases = []string{
	"try to break", "try breaking", "attempt to break",
	"try to reproduce", "attempt to reproduce", "reproduce the", "reproduces the",
	"find bugs", "find a bug", "find the bug",
	"find edge cases", "find the edge cases", "find failure modes",
	"edge case", "edge cases", "corner case", "corner cases",
	"failure mode", "failure modes",
	"stress test", "stress testing", "race condition", "race conditions",
	"race detector", "malformed input", "invalid input", "fuzz test",
	"make it fail", "make it crash", "make it panic",
	"see if it breaks", "see if it fails", "see if it crashes", "see if it panics",
	"break the", "break this", "breaks the", "challenge the", "probe the",
	"benchmark the", "security review", "threat model",
	"exploit", "vulnerability", "vulnerabilities", "hardening",
}

// executePhrases are multi-word execution signals that may appear anywhere in
// a non-question request. They are narrower than the leading verbs so an
// informational sentence that merely names an action is not promoted.
var executePhrases = []string{
	"run the tests", "run the test", "run tests", "run the suite",
	"run the build", "run the command", "run it", "run this", "run them",
	"fix it", "fix this", "fix that", "fix the", "fix a", "fix my",
	"implement the", "implement a", "implement it", "implement this",
	"refactor the", "refactor this",
	"add a", "add the", "add it", "add support",
	"remove the", "remove it", "delete the", "delete it",
	"rename the", "move the", "apply the", "apply it", "apply this",
	"write the", "write a", "write it", "create a", "create the", "create it",
	"update the", "update it", "change the", "change it", "change this",
	"make the change", "make the fix", "make it work",
	"commit the", "revert the", "replace the", "replace it", "patch the",
	"bump the", "compile the", "build the", "install the", "migrate the",
}
