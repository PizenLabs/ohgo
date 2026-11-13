package workspace

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

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
	clauses := requestClauses(request)

	// The evidence each intent actually has, independent of precedence.
	// A request is evaluated as a whole objective, not only by its first
	// verb: a leading read/discovery clause must not mask a later explicit
	// mutation clause ("check this project and write README.md"). Clause
	// heads are therefore considered for the mutation/investigation intents,
	// while a read signal is taken only from the leading clause so an
	// informational opener is not fabricated from a later noun phrase. The one
	// exception is a leading whole-change prohibition: it is a constraint, not
	// an objective, so a later explicit read verb is the requested effect (see
	// the NoMutationRequested branch below).
	read := evidenceNone
	if len(clauses) > 0 {
		head := clauseHead(clauses[0].text)
		switch {
		case interrogativeHeads[head]:
			// The request opens a question about an operation. Asking what
			// something does is a request to understand it.
			read = evidenceHead
		case headMatches(readHeads, head):
			read = evidenceHead
			if headMatches(ambiguousReadHeads, head) {
				read = evidencePhrase
			}
		case clauses[0].asked:
			// A first-person question frame ("I want to ask if ...") carries
			// no verb the classifier recognises, but it is still a question.
			read = evidencePhrase
		}
	}
	// A whole-change prohibition is a constraint, not the objective. When it
	// opens the request, a later read verb is the requested effect even if a
	// discourse word or a sentence boundary keeps it out of the head position
	// ("Actually don't change anything. Explain the architecture."). Without
	// this, the READ fallback left a request that explicitly forbids mutation
	// as an ambiguous BUILD turn instead of offering the ASK boundary.
	if read == evidenceNone && contract.NoMutationRequested(request) {
		if laterReadRequest(request) {
			read = evidenceHead
		}
	}
	// A later clause with an unambiguous read head can strengthen a leading
	// read signal. "Check this project and analyze what it is about" opens with
	// the ambiguous verb "check"; the later "analyze" clause is the objective's
	// verb and makes the prediction strong.
	//
	// It must not turn an investigative opener into a contested READ:
	// "investigate this project and analyze what it is about" asks for an
	// investigation whose deliverable is an explanation. Treating the
	// explanatory clause as a rival read objective made the prediction both
	// READ and ambiguous, so BUILD kept the turn and never handed off to FUZZ.
	leadingInvestigation := len(clauses) > 0 && clauseInvestigates(clauses[0].text)
	if leadingInvestigation {
		// "Analyze how ..." is both a read verb and an investigative
		// construction. The complement decides its effect, so it must not
		// leave a READ signal that contests its own investigation.
		read = evidenceNone
	}
	if !leadingInvestigation {
		for i, entry := range clauses {
			if i == 0 || entry.asked {
				continue
			}
			head := clauseHead(entry.text)
			if headMatches(readHeads, head) && !headMatches(ambiguousReadHeads, head) &&
				!headMatches(investigateHeads, head) && !headMatches(executeHeads, head) {
				read = evidenceHead
				break
			}
		}
	}
	investigateHead := false
	executeHead := false
	// executeClause records a mutation-bearing phrase in a later clause. A
	// read opener must not hide "inspect X and make sure Y": the phrase is the
	// only mutation signal in that clause, so a head-only rule would drop it.
	executeClause := false
	// Only clauses that request an effect can supply imperative evidence. A
	// verb inside a question describes what is being asked about, not what the
	// runtime must do; treating it as an imperative is what escalated
	// "does this update GitHub?" into BUILD authority.
	var requested []string
	for i, entry := range clauses {
		if entry.asked {
			continue
		}
		token := clauseHead(entry.text)
		requested = append(requested, entry.text)
		if clauseInvestigates(entry.text) {
			investigateHead = true
		}
		if headMatches(executeHeads, token) {
			executeHead = true
		}
		if i > 0 && !headMatches(readHeads, token) && containsPhrase(entry.text, executePhrases) {
			executeClause = true
		}
	}
	requestedText := strings.Join(requested, " ")
	investigate := phraseEvidence(investigateHead, containsPhrase(requestedText, investigatePhrases))
	execute := phraseEvidence(executeHead, containsPhrase(requestedText, executePhrases) || executeClause)

	// Precedence: investigate before execute (FUZZ is the lower authority
	// when a request asks for both); an explicit mutation clause overrides a
	// leading read/discovery clause, but a read opener still beats a merely
	// corroborating execution phrase; an unmatched request narrows to READ.
	winner := evidenceNone
	intent := IntentRead
	// Precedence combines the authority rules without a cycle: an explicit
	// mutation clause beats a leading read/discovery clause, but a mixed
	// investigate signal still lowers authority to FUZZ; a read opener with
	// no mutation clause keeps READ, even over an incidental investigative
	// phrase such as "edge case".
	switch {
	case executeHead:
		if investigate != evidenceNone {
			intent, winner = IntentInvestigate, investigate
		} else {
			intent, winner = IntentExecute, execute
		}
	case read != evidenceNone && !executeClause:
		intent, winner = IntentRead, read
	case executeClause:
		if investigate != evidenceNone {
			intent, winner = IntentInvestigate, investigate
		} else {
			intent, winner = IntentExecute, execute
		}
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

	// A read verb in the same request does not contest an explicit execution
	// clause: "check this project and create the missing files" asks for the
	// mutation, not for a decision between reading and executing it.
	decisiveMutation := intent == IntentExecute && (executeHead || executeClause) && competing == IntentRead

	// A leading investigative objective is the current phase, not a rival to
	// a later change: "investigate and fix the checkout failure" starts in
	// FUZZ and hands the confirmed change to BUILD. The reverse order
	// ("fix it and then try to break it") remains ambiguous because there the
	// leading objective is the change and the investigation may be its
	// verification.
	decisiveInvestigation := intent == IntentInvestigate && leadingInvestigation && winner == evidenceHead

	prediction := Prediction{Intent: intent, Competing: competing}
	switch {
	case winner == evidenceNone || (rival >= winner && !decisiveMutation && !decisiveInvestigation):
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
	// Allow means the turn runs under the active workspace.
	Allow Outcome = "allow"
	// Require means the request needs an authority the active workspace does
	// not hold, so it must be refused until the user changes workspace
	// explicitly (§3, §18).
	Require Outcome = "require_authority"
)

// Route is the authority decision for one turn.
//
// The active workspace is the turn's authority. Intent can refuse an operation
// that exceeds ASK or FUZZ, but it cannot silently narrow BUILD into another
// workspace (§3, §11).
type Route struct {
	// Active is the session's authoritative workspace. A route never changes
	// it; only an explicit user switch does.
	Active Workspace
	// Intent is the classified request.
	Intent Intent
	// Outcome is whether the turn may run.
	Outcome Outcome
	// Required names the workspace the request needs when Outcome is Require.
	Required Workspace
}

// Allowed reports whether the turn may run.
func (r Route) Allowed() bool { return r.Outcome == Allow }

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
// The guard is capability-based, not intent-handler equality:
//
//	ACTIVE   REQUIREMENT                    RESULT
//	ASK      READ                           ASK (read-only)
//	ASK      EXECUTE / MUTATE               refused -> BUILD
//	ASK      INVESTIGATE                    refused -> FUZZ
//	BUILD    READ / EXECUTE / INVESTIGATE   BUILD
//	BUILD    MUTATE                         BUILD
//	FUZZ     READ / EXECUTE / INVESTIGATE   FUZZ
//	FUZZ     MUTATE                         refused -> BUILD
//
// BUILD may inspect and investigate; the transition policy decides whether a
// narrower workspace is a better surface. It may not mutate from FUZZ because
// the capability is absent, not because the intent handler differs. Workspace
// changes remain explicit or control-plane decisions; this guard only prevents
// an authority from performing work outside its contract.
func RouteTurn(active Workspace, text string) Route {
	requirement := AnalyzeRequest(text)
	route := Route{Active: active, Intent: requirement.Intent, Outcome: Allow}
	if !active.Valid() {
		// An unresolved workspace is denied, never defaulted to one of the
		// three (see Workspace's doc comment).
		route.Outcome = Require
		return route
	}
	if IsConversationalAcknowledgement(text) {
		return route
	}
	switch active {
	case Ask:
		switch requirement.Capability {
		case CapabilityMutate, CapabilityExecute:
			route.Outcome, route.Required = Require, Build
		case CapabilityInvestigate:
			route.Outcome, route.Required = Require, Fuzz
		}
	case Fuzz:
		if requirement.Capability == CapabilityMutate {
			route.Outcome, route.Required = Require, Build
		}
	}
	return route
}

// IsConversationalAcknowledgement identifies short acknowledgements that are
// not workspace work. They can be answered within the selected workspace
// without routing the session into ASK.
func IsConversationalAcknowledgement(text string) bool {
	switch normalizeRequest(text) {
	case "hi", "hello", "hey", "good morning", "good afternoon", "good evening", "thanks", "thank you":
		return true
	default:
		return false
	}
}

// IsContinuation reports whether a request explicitly continues work already
// in progress rather than stating a new objective.
//
// A continuation is task-local. It carries no new intent for the classifier to
// place, so treating it as the READ fallback would force a workspace change or
// refusal for a turn the active task already owns. This predicate is vocabulary,
// not a grant: the turn still runs under the current authority, and every action
// remains subject to the security gate.
func IsContinuation(text string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
	switch normalized {
	case "continue", "continue.", "go on", "keep going", "resume", "resume.",
		"retry", "retry.", "try again", "finish", "finish it", "carry on",
		"verify", "verify the fix", "verify the change", "run the check",
		"run the verification", "run the tests", "run the test":
		return true
	}
	words := strings.Fields(normalized)
	if len(words) == 0 {
		return false
	}
	// A continuation verb is a request head, not an incidental word. "update my
	// resume" and "compare this with the previous task" are new objectives even
	// though they contain continuation vocabulary.
	switch strings.Trim(words[0], ".,!?;:") {
	case "continue", "continues", "resume", "resumes", "resuming",
		"retry", "retries", "retrying", "finish", "finishes", "finishing":
		return true
	}
	for _, phrase := range []string{"try again", "keep going", "go on", "carry on"} {
		if strings.HasPrefix(normalized, phrase) {
			return true
		}
	}
	return strings.Contains(normalized, "where we left off")
}

// IsTaskFollowUp reports whether a short request refers to the open task's own
// object rather than naming a new objective.
//
// It is narrower than continuation vocabulary. "Fix it" and "implement the
// fix" have no target of their own; they inherit the open task's result and
// evidence. "Fix the checkout bug" names a subject and is a new objective even
// if it is related. The predicate decides attachment only; it grants no
// authority, and the capability gate still evaluates every action.
func IsTaskFollowUp(text string) bool {
	normalized := normalizeRequest(text)
	if isTaskFollowUpCore(normalized) {
		return true
	}
	// A follow-up may append its own verification ("implement the fix and
	// verify it"). The head still refers to the open task's result; a named
	// subject ("fix the checkout bug and run tests") does not.
	for _, separator := range []string{" and ", " then "} {
		if index := strings.LastIndex(normalized, separator); index > 0 {
			if isTaskFollowUpCore(strings.TrimSpace(normalized[:index])) {
				return true
			}
		}
	}
	return false
}

// IsExecutionFollowUp reports whether a short request asks to perform the open
// task's execution again, rather than to check the task's result.
//
// "Run it" after "run this project" is a new attempt at the action, not a
// verification of the old one. It must attach to an open execution task so the
// request is not refused as a new conflicting objective; a finished task does
// not own it, so the attempt starts fresh and must produce its own observation.
func IsExecutionFollowUp(text string) bool {
	switch normalizeRequest(text) {
	case "run it", "run this", "run that", "run it again", "run this again",
		"start it", "start this", "start that", "start it again",
		"launch it", "launch this", "launch that", "launch it again":
		return true
	default:
		return false
	}
}

// isTaskFollowUpCore reports a short request whose object is the open task's
// own result.
func isTaskFollowUpCore(normalized string) bool {
	switch normalized {
	case "do it", "go ahead", "proceed", "make it so",
		"fix it", "fix this", "fix that", "fix the issue", "fix the bug",
		"fix the failure", "fix the problem", "resolve it", "resolve the issue",
		"implement it", "implement that", "implement the fix", "apply it",
		"apply the fix", "make the change", "make the fix", "correct it",
		"patch it", "reproduce it", "reproduce that", "investigate it",
		"verify it", "verify the fix", "verify the change", "check it",
		"check the fix", "test it", "address it",
		// A rework follow-up refers to the open task's own object. It is a
		// continuation of a task that owns a result, never a licence to
		// reinterpret an informational task as a mutation one.
		"redesign it", "redesign this", "redesign that",
		"rework it", "revamp it", "restyle it", "redo it",
		"overhaul it", "modernize it", "modernise it":
		return true
	}
	return false
}

// IsVerificationContinuation reports whether a request asks whether work
// already in progress now holds, rather than naming a new subject to inspect.
//
// It is the natural-language companion to IsContinuation: "check whether the
// fix worked" refers to the existing change, while "check the authentication
// flow" names a new READ request. That distinction needs both a check/question
// cue and a reference to the result of existing work; either half alone is not
// enough. Like IsContinuation this is vocabulary, not authority: the caller
// still decides whether an open task owns the turn, and the gate still admits
// every tool.
func IsVerificationContinuation(text string) bool {
	// Verification wording inside a new mutation request is part of that
	// objective, not a continuation of the old one. "implement the fix and
	// verify it" must still route as BUILD work requiring approval. A pure
	// check/run request is allowed: "run the verification" names the existing
	// contract, not a new change.
	if RequestsMutation(text) {
		return false
	}
	normalized := normalizeRequest(text)
	if normalized == "" {
		return false
	}
	if !hasExistingResultReference(normalized) {
		return false
	}
	if hasVerificationAction(normalized) {
		return true
	}
	return hasOutcomeQuestion(normalized) && hasOutcomeWord(normalized)
}

// hasVerificationAction reports an explicit check request.
//
// Launch verbs are deliberately absent. "Run it" asks to execute the task's
// object again; treating it as verification let a finished task answer
// "already proven" instead of running, and made a new execution request
// indistinguishable from a check of old work. A question about whether it is
// running is still verification: hasOutcomeWord names that state, and
// hasOutcomeQuestion supplies the question frame.
func hasVerificationAction(normalized string) bool {
	padded := " " + normalized + " "
	for _, phrase := range []string{
		"check", "checks", "checking", "recheck", "re check",
		"verify", "verifies", "verifying", "verification",
		"confirm", "confirms", "confirming", "validate", "validates", "validating",
		"test", "retest", "re test",
		"see", "prove", "proves",
	} {
		if strings.Contains(padded, " "+phrase+" ") {
			return true
		}
	}
	return false
}

// hasOutcomeQuestion reports a yes/no question about the result of work.
func hasOutcomeQuestion(normalized string) bool {
	words := strings.Fields(normalized)
	if len(words) == 0 {
		return false
	}
	switch strings.Trim(words[0], ".,!?;:") {
	case "did", "does", "do", "is", "are", "was", "were", "has", "have",
		"can", "could", "should", "will", "would":
		return true
	}
	return false
}

// hasOutcomeWord reports language that names a result state rather than a new
// subject. It is used only with a question head, so "are the tests flaky?"
// stays a new investigation while "are the tests passing?" is verification.
func hasOutcomeWord(normalized string) bool {
	padded := " " + normalized + " "
	for _, phrase := range []string{
		"work", "works", "worked", "working",
		"fix", "fixed", "fixes", "change", "changed", "changes",
		"pass", "passes", "passed", "passing", "green",
		"running", "listening",
		"fail", "fails", "failed", "failing",
		"compile", "compiles", "compiled", "build", "builds", "built",
		"succeed", "succeeds", "succeeded", "success", "successful",
		"done", "landed", "ready", "proven",
	} {
		if strings.Contains(padded, " "+phrase+" ") {
			return true
		}
	}
	return false
}

// hasExistingResultReference reports a reference to work already in progress:
// an anaphor ("it", "that") or the generic outcome of a task ("the fix", "the
// change", "the tests"). A new named subject does not qualify.
func hasExistingResultReference(normalized string) bool {
	padded := " " + normalized + " "
	for _, phrase := range []string{
		"it", "the fix", "this fix", "that fix",
		"the change", "this change", "that change", "the changes", "these changes",
		"the work", "this work", "that work",
		"the task", "this task", "that task",
		"the test", "the tests", "this test", "these tests",
		"the test suite", "the suite", "the build", "this build",
		"the verification", "the check", "the result", "the results",
		"the outcome", "tests", "verification",
	} {
		if strings.Contains(padded, " "+phrase+" ") {
			return true
		}
	}
	return false
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
		if r == ',' {
			// A comma is a clause boundary, not decoration. Keeping it as a
			// token is what lets "check the project, write README.md" expose
			// the mutation clause instead of collapsing both objectives into
			// one read-headed sentence.
			b.WriteString(" , ")
			space = true
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
//
// The question frames ("I want to ask", "I'd like to know") are wrappers too:
// stripping them leaves the complement clause that carries the actual question,
// so its interrogative head decides the effect. They never remove a request.
var politenessPrefixes = []string{
	"can you", "could you", "would you", "will you", "please",
	"i want you to", "i'd like you to", "i would like you to",
	"i need you to", "go ahead and", "help me",
	"i want to ask", "i'd like to ask", "i would like to ask",
	"i want to know", "i'd like to know", "i would like to know",
	"i'm asking", "i am asking", "i was wondering", "i'm wondering", "i wonder",
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

// ambiguousReadHeads are verbs whose meaning depends on whether the user
// wants an explanation, an investigation, or an action. They are useful
// signals, but not enough to silently choose a narrower workspace.
var ambiguousReadHeads = map[string]bool{
	"check": true, "inspect": true, "review": true,
}

// readRequestWords are explicit read verbs and question words that can name
// the objective after a leading whole-change prohibition. The set is narrower
// than readHeads: auxiliaries and generic nouns ("is", "can", "build") would
// make an ordinary statement look like a request for understanding.
var readRequestWords = map[string]bool{
	"what": true, "what's": true, "whats": true,
	"why": true, "how": true, "where": true, "when": true, "which": true,
	"who": true, "whose": true,
	"explain": true, "describe": true, "summarize": true, "summarise": true,
	"compare": true, "outline": true, "tell": true, "walk": true,
	"define": true, "clarify": true, "show": true, "list": true,
	"review": true, "analyze": true, "analyse": true, "inspect": true,
	"examine": true, "discuss": true, "elaborate": true, "explore": true,
	"read": true, "check": true,
}

// laterReadRequest reports whether the request contains a read-request word
// anywhere after its leading constraint.
func laterReadRequest(request string) bool {
	for _, token := range strings.Fields(request) {
		token = strings.Trim(token, ".,!?;:'\"")
		if headMatches(readRequestWords, token) {
			return true
		}
	}
	return false
}

// investigateHeads are leading verbs that ask to probe, trace, or reproduce
// rather than to understand or to change. They map to FUZZ semantics.
var investigateHeads = map[string]bool{
	"reproduce": true, "repro": true, "probe": true, "fuzz": true,
	"stress": true, "benchmark": true, "challenge": true, "break": true,
	"test": true, "attack": true, "exploit": true,
	// Diagnostic and discovery verbs. "Investigate why this fails" asks for
	// exploration, not for a repository change; mapping it to READ would let
	// the runtime answer from configuration without ever observing the
	// failure. "Trace" and "explore" ask for the same active evidence
	// gathering rather than a summary of what is already known.
	"investigate": true, "diagnose": true, "troubleshoot": true,
	"trace": true, "explore": true,
}

// investigativeHeadPhrases open a clause with an inquiry whose object is a
// cause, relation, or unknown rather than a summary. They are head evidence,
// not corroborating phrases: "analyze how this subsystem is connected" asks for
// active investigation, while "analyze what this project is about" asks for an
// explanation. The distinction is the complement, not the verb alone.
var investigativeHeadPhrases = []string{
	"find why", "find where", "find how", "find whether",
	"analyze how", "analyse how", "analyze why", "analyse why",
	"analyze where", "analyse where", "analyze whether", "analyse whether",
}

// clauseInvestigates reports whether a clause opens with an investigative
// objective. The head verb and the head construction are separate from the
// corroborating phrase set so a later read clause cannot make a leading
// investigation ambiguous.
func clauseInvestigates(clause string) bool {
	if headMatches(investigateHeads, clauseHead(clause)) {
		return true
	}
	for _, phrase := range investigativeHeadPhrases {
		if clause == phrase || strings.HasPrefix(clause, phrase+" ") {
			return true
		}
	}
	return false
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
	"disconnect": true, "set": true, "tidy": true, "ensure": true,
	"organize": true, "organise": true,
	// Running a service is execution, not understanding. "use", "serve",
	// "start", and "launch" are how a request names a long-running capability
	// ("use npx live-server test this project"); without them the classifier
	// fallback made the turn read-only, so the exact action the user asked for
	// was refused before the model could propose it.
	"use": true, "serve": true, "start": true, "launch": true,
	"spin": true, "boot": true, "host": true,
	// Rework verbs ask to make an existing object over. They carry a change,
	// not a request to describe one: "redesign it" after a design turn is the
	// implementation follow-up, and classifying it as the READ fallback let
	// the runtime answer with another plan and prove an informational contract
	// the user never asked for.
	"redesign": true, "rework": true, "revamp": true, "restyle": true,
	"redo": true, "overhaul": true, "modernize": true, "modernise": true,
	"redraw": true, "reimplement": true, "rearchitect": true,
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
	"make sure", "tidy up", "clean up",
	// Long-running service requests are execution even when the head token is
	// a generic connective ("please use live-server and check the page").
	"use the server", "use live-server", "use npx", "serve the", "serve this",
	"start the server", "start the dev server", "start the development server",
	"start the app", "start the application", "start it", "launch the",
	"launch this", "launch it", "spin up", "boot the",
	// Re work framing ("I want to redesign the page", "we should revamp it")
	// puts a non-verb token at the head. The phrase keeps the change visible
	// without promoting a question, which the clause model already filters.
	"redesign the", "redesign this", "redesign that", "redesign it",
	"rework the", "rework this", "rework it",
	"revamp the", "revamp this", "revamp it",
	"restyle the", "restyle this", "restyle it",
	"redo the", "redo this", "redo it",
	"overhaul the", "overhaul this", "overhaul it",
	"modernize the", "modernize this", "modernize it",
	"modernise the", "modernise this", "modernise it",
	"redraw the", "redraw this", "redraw it",
	"reimplement the", "reimplement this", "reimplement it",
	"rearchitect the", "rearchitect this", "rearchitect it",
}

// mutationHeads are the leading verbs that ask for a repository change rather
// than merely execution. The set is deliberately narrower than executeHeads:
// "run the tests" is EXECUTE for routing but not a mutation objective, while
// "fix the tests" is both.
var mutationHeads = map[string]bool{
	"implement": true, "fix": true, "modify": true, "change": true,
	"refactor": true, "restructure": true, "create": true, "add": true,
	"remove": true, "delete": true, "rename": true, "move": true,
	"apply": true, "write": true, "patch": true, "edit": true,
	"update": true, "upgrade": true, "install": true, "uninstall": true,
	"migrate": true, "commit": true, "revert": true, "replace": true,
	"introduce": true, "generate": true, "rewrite": true, "drop": true,
	"merge": true, "publish": true, "deploy": true, "bump": true,
	"wire": true, "format": true, "configure": true, "enable": true,
	"disable": true, "reset": true, "clean": true, "tidy": true,
	"init": true, "initialize": true, "bootstrap": true, "scaffold": true,
	"vendor": true, "pin": true, "port": true, "convert": true,
	"seed": true, "set": true, "organize": true, "organise": true,
	"ensure": true,
	// The rework family changes the existing object rather than describing it.
	"redesign": true, "rework": true, "revamp": true, "restyle": true,
	"redo": true, "overhaul": true, "modernize": true, "modernise": true,
	"redraw": true, "reimplement": true, "rearchitect": true,
}

// mutationPhrases are the multi-word forms that carry a change request even
// when their head is not a mutation verb ("make sure", "tidy up").
var mutationPhrases = []string{
	"make sure", "tidy up", "clean up",
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
	"bump the", "install the", "migrate the",
	// A rework phrase after a non-verb head ("I want to…", "we should…")
	// still requests the change; the clause model keeps questions out.
	"redesign the", "redesign this", "redesign that", "redesign it",
	"rework the", "rework this", "rework it",
	"revamp the", "revamp this", "revamp it",
	"restyle the", "restyle this", "restyle it",
	"redo the", "redo this", "redo it",
	"overhaul the", "overhaul this", "overhaul it",
	"modernize the", "modernize this", "modernize it",
	"modernise the", "modernise this", "modernise it",
	"redraw the", "redraw this", "redraw it",
	"reimplement the", "reimplement this", "reimplement it",
	"rearchitect the", "rearchitect this", "rearchitect it",
}

// RequestsMutation reports whether a request clearly contains a clause that
// asks for a repository change.
//
// It is a finite, deterministic contract, not natural-language understanding.
// A clause whose head is a read verb is not promoted by a phrase alone, so
// "explain how to make sure the tests pass" stays informational while
// "inspect X and make sure Y" does not lose its change request. Ambiguous
// wording is false: completion must not invent an objective the user did not
// state.
func RequestsMutation(text string) bool {
	request := stripPoliteness(normalizeRequest(text))
	if request == "" {
		return false
	}
	for _, entry := range requestClauses(request) {
		if clauseRequestsMutation(entry) {
			return true
		}
	}
	return false
}

// clauseRequestsMutation reports whether one parsed clause asks for a
// repository change.
//
// The clause is the unit because a question about a change is not a request for
// it, and a read-headed clause is not promoted by a mutation phrase alone. The
// helper is shared with the conditional-workflow analysis so the two cannot
// disagree about which clause carries the change.
func clauseRequestsMutation(entry clause) bool {
	if entry.asked {
		// A question about a change ("does this update GitHub?") does not
		// request the change. Stating it as a mutation objective would make
		// completion demand evidence the user never asked for.
		return false
	}
	head := clauseHead(entry.text)
	if headMatches(mutationHeads, head) {
		return true
	}
	if verificationObservationPhrase(entry.text) {
		// "make sure the site works" after starting a service is a
		// verification of observable behaviour, not a request to change the
		// repository. The same phrase still carries a change when it names an
		// effect ("make sure the timeout is applied").
		return false
	}
	return !headMatches(readHeads, head) && containsPhrase(entry.text, mutationPhrases)
}

// verificationObservationPhrases name observable behaviour a check can
// confirm. They disambiguate "make sure" so a serve-and-verify request is not
// misclassified as a mutation objective.
var verificationObservationPhrases = []string{
	"works", "working", "loads", "renders", "responds", "serves",
	"passes", "passing", "is ready", "is running", "starts correctly",
	"loads correctly", "works correctly",
}

func verificationObservationPhrase(text string) bool {
	if !containsPhrase(text, []string{"make sure"}) {
		return false
	}
	return containsPhrase(text, verificationObservationPhrases)
}

// serviceExecutionHeads are execution verbs whose object is normally a
// long-running service or application. They keep the completion contract an
// execution even when a later clause adds an investigative verb ("run the app
// and test it"): testing the running app is verification, not the deliverable.
var serviceExecutionHeads = map[string]bool{
	"run": true, "use": true, "serve": true, "start": true,
	"launch": true, "host": true, "boot": true, "spin": true,
}

// RequestsServiceExecution reports whether the request asks to run or serve a
// process whose result must be observed, even if an investigation verb also
// appears. It is a contract-kind signal only; authority still comes from the
// route and the live gate.
func RequestsServiceExecution(text string) bool {
	request := stripPoliteness(normalizeRequest(text))
	if request == "" {
		return false
	}
	for _, entry := range requestClauses(request) {
		if entry.asked {
			continue
		}
		if serviceExecutionHeads[clauseHead(entry.text)] {
			return true
		}
		if containsPhrase(entry.text, []string{
			"npx ", "live-server", "live server", "dev server", "development server",
			"local server", "run the app", "run the application", "serve the site",
		}) {
			return true
		}
	}
	return false
}

// RequestsRuntimeExecution reports whether the request asks to run the
// project's own long-running runtime: the application, server, site, or
// service, rather than a one-shot command or a named check.
//
// The distinction is the completion contract's, not the router's. "Run the
// tests" and "run the build" are execution objectives that a successful
// command proves. "Run this project" is not: a finished environment probe is
// an execution, but it is not the requested execution, so completion must
// require the runtime itself to be observed alive and answering a check.
//
// The classifier is deliberately finite. An explicit service phrase
// ("live-server", "dev server", "localhost") settles the question; otherwise a
// launch verb must name a runtime object or a deictic result ("run it"). A
// named check or artifact object vetoes the runtime reading, so "run the tests
// again" cannot demand a server.
func RequestsRuntimeExecution(text string) bool {
	request := stripPoliteness(normalizeRequest(text))
	if request == "" {
		return false
	}
	for _, entry := range requestClauses(request) {
		if entry.asked {
			continue
		}
		if runtimeExecutionClause(entry.text) {
			return true
		}
	}
	return false
}

// runtimeExecutionClause reports whether one clause asks for a live runtime.
func runtimeExecutionClause(clause string) bool {
	// An explicit service phrase overrides the rest of the clause: "test this
	// project with npx live-server" still needs the server observed, even
	// though its head verb is investigative.
	if containsPhrase(clause, explicitRuntimePhrases) {
		return true
	}
	if containsPhrase(clause, nonRuntimeObjects) {
		return false
	}
	head := clauseHead(clause)
	if !runtimeExecutionHeads[head] {
		return false
	}
	if containsPhrase(clause, runtimeObjects) {
		return true
	}
	return deicticExecutionObject(clause, head)
}

// deicticExecutionObject reports a launch verb whose whole object is a
// reference to the task's own result ("run it", "run this again"). The object
// is not named in the clause, so the veto list above is the only thing that can
// disagree; a named object would have matched runtimeObjects.
func deicticExecutionObject(clause, head string) bool {
	fields := strings.Fields(clause)
	index := 0
	for index < len(fields) && fields[index] != head {
		index++
	}
	rest := fields[index+1:]
	if len(rest) == 0 || len(rest) > 3 {
		return false
	}
	for _, token := range rest {
		token = strings.Trim(token, ".,!?;:'\"")
		switch token {
		case "it", "this", "that", "these", "those", "one", "again", "now", "please":
		default:
			return false
		}
	}
	return true
}

// runtimeExecutionHeads are the launch verbs whose object is a runtime.
//
// "Use" belongs to the broader service-execution vocabulary but not here: "use
// this project structure" names an object, not a process to start. An explicit
// service phrase still covers "use the dev server".
var runtimeExecutionHeads = map[string]bool{
	"run": true, "start": true, "serve": true, "launch": true,
	"host": true, "boot": true, "spin": true,
}

// explicitRuntimePhrases name a live service regardless of the clause's head
// verb or its other objects.
var explicitRuntimePhrases = []string{
	"live-server", "live server", "dev server", "development server",
	"local server", "web server", "http server", "preview server",
	"localhost", "serve the site", "serve the app", "serve this",
	// Package-manager start/dev scripts are long-running by convention. A
	// configuration read cannot prove them from an exit code, so they get the
	// live-runtime requirement even though the request names a command.
	"npm start", "npm run start", "npm run dev", "npm run serve", "npm run preview",
	"yarn start", "yarn dev", "pnpm start", "pnpm dev", "bun start", "bun dev",
	"docker compose up", "docker-compose up",
	"make run", "make serve", "make dev",
	"runserver", "uvicorn", "gunicorn",
}

// runtimeObjects name the project runtime a launch verb can start.
var runtimeObjects = []string{
	"project", "repository", "repo", "app", "application", "site", "website",
	"web app", "web application", "server", "service", "program", "process",
	"runtime", "preview", "dashboard", "backend", "frontend", "blog",
}

// nonRuntimeObjects name what a launch verb executes without starting a
// service. They veto the runtime reading even when the clause also names the
// project ("run the project tests").
var nonRuntimeObjects = []string{
	"test", "tests", "suite", "unit", "integration", "e2e", "test suite",
	"build", "compile", "bundle", "lint", "vet", "typecheck", "types",
	"format", "formatter", "check", "checks", "migration", "migrations",
	"install", "upgrade", "update", "setup", "seed", "deploy", "publish",
	"release", "version", "benchmark", "benchmarks", "command", "commands",
}

// headMatches reports whether token is a recognised head verb, including
// common inflected forms (plurals, past tense, gerunds/participles) of the
// heads in the set. The inflections are derived, not enumerated, so a new
// head verb gains its forms automatically and no verb is special-cased.
func headMatches(heads map[string]bool, token string) bool {
	if heads[token] {
		return true
	}
	for head := range heads {
		for _, form := range headForms(head) {
			if form == token {
				return true
			}
		}
	}
	return false
}

// headForms returns the inflected forms of a head verb that the classifier
// treats as the same intent-bearing verb.
func headForms(head string) []string {
	forms := []string{head}
	forms = append(forms, head+"s", head+"es")
	forms = append(forms, head+"ed", head+"d")
	forms = append(forms, head+"ing")
	if strings.HasSuffix(head, "e") && len(head) > 1 {
		forms = append(forms, head[:len(head)-1]+"ing")
	}
	// Double the final consonant for short vowel-consonant verbs such as
	// run -> running, so gerund objectives are not missed.
	if len(head) >= 3 {
		last := head[len(head)-1]
		mid := head[len(head)-2]
		if isVowel(mid) && !isVowel(last) && last != 'w' && last != 'x' && last != 'y' {
			forms = append(forms, head+string(last)+"ing")
		}
	}
	return forms
}

func isVowel(r byte) bool {
	switch r {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// clause is one objective in a normalized request.
//
// Answering and requesting are different effects, so the text alone is not
// enough: asked reports that the clause is part of a question or a condition
// rather than a request. A verb in an asked clause describes what the user is
// asking about; it is not evidence that the user asked the runtime to do it.
type clause struct {
	text  string
	asked bool
}

// requestClauses splits a normalized request into its objectives and marks the
// ones the user is asking about instead of asking for.
//
// The unit is the comma part, itself split on coordinators, because a comma is
// the only boundary normalizeRequest preserves. A part that opens with a
// coordinator ("or", "and", "but") continues the clause before it; any other
// part starts a new main clause, which ends a question. That is what keeps
// "if the tests fail, fix them" imperative while
// "does this run, or do I run it myself?" stays a question.
//
// A question opens when the leading clause is interrogative or when a
// non-imperative clause carries a complement opener ("I want to ask if ...").
// It continues through coordinated and alternative parts until a new main
// clause begins.
func requestClauses(request string) []clause {
	if strings.TrimSpace(request) == "" {
		return nil
	}
	var out []clause
	asked := false
	for partIndex, part := range strings.Split(request, " , ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		continues := partIndex > 0 && opensWithContinuation(part)
		if continues {
			// The coordinator belongs to the boundary, not to the clause: it
			// must not survive as a clause head and hide the clause's verb
			// ("review the package, and ensure it compiles").
			part = stripLeadingCoordinator(part)
		} else {
			asked = false
		}
		for subIndex, sub := range coordinatedClauses(part) {
			sub = strings.TrimSpace(sub)
			if sub == "" {
				continue
			}
			if subIndex == 0 && !continues {
				asked = opensQuestion(sub)
			}
			out = append(out, clause{text: sub, asked: asked})
		}
	}
	if len(out) == 0 {
		return []clause{{text: request}}
	}
	return out
}

// coordinatedClauses breaks one comma part into the clauses joined by an
// explicit coordinator. A sequencing word left at the head of a clause is
// dropped so "inspect X and then add Y" exposes "add Y" as the objective.
func coordinatedClauses(part string) []string {
	joined := part
	for _, separator := range []string{" and also ", " also ", " and ", " then ", " plus ", " & "} {
		joined = strings.ReplaceAll(joined, separator, " ,, ")
	}
	parts := strings.Split(joined, " ,, ")
	out := make([]string, 0, len(parts))
	for _, sub := range parts {
		sub = strings.TrimSpace(sub)
		for _, prefix := range []string{"then ", "also "} {
			sub = strings.TrimSpace(strings.TrimPrefix(sub, prefix))
		}
		if sub != "" {
			out = append(out, sub)
		}
	}
	return out
}

// opensWithContinuation reports whether a comma part is joined to the clause
// before it rather than starting a new main clause.
func opensWithContinuation(part string) bool {
	switch firstToken(part) {
	case "or", "nor", "and", "but", "so", "yet":
		return true
	}
	return false
}

// stripLeadingCoordinator removes the boundary word from a continuation part.
func stripLeadingCoordinator(part string) string {
	for _, coordinator := range []string{"or", "nor", "and", "but", "so", "yet"} {
		if part == coordinator {
			return ""
		}
		if strings.HasPrefix(part, coordinator+" ") {
			return strings.TrimSpace(part[len(coordinator)+1:])
		}
	}
	return part
}

// opensQuestion reports whether a clause frames what follows as something the
// user is asking about rather than something they asked for.
func opensQuestion(clause string) bool {
	if interrogativeHeads[clauseHead(clause)] {
		return true
	}
	// A clause that opens with a request verb is an instruction; an embedded
	// "if" in it is a condition on the action, not the request's frame.
	if requestsAction(clause) {
		return false
	}
	return containsPhrase(clause, complementOpeners)
}

// requestsAction reports whether a clause opens with a form that asks the
// runtime to do something.
func requestsAction(clause string) bool {
	head := clauseHead(clause)
	if headMatches(readHeads, head) || headMatches(executeHeads, head) ||
		clauseInvestigates(clause) {
		return true
	}
	for _, phrase := range requestPhraseHeads {
		if clause == phrase || strings.HasPrefix(clause, phrase+" ") {
			return true
		}
	}
	return false
}

// firstToken returns the first whitespace-delimited token of a normalized
// clause.
func firstToken(clause string) string {
	fields := strings.Fields(clause)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// complementOpeners introduce the thing a question is about. They are
// load-bearing: without them "I want to ask if this runs" has no verb the
// classifier recognises and would fall back to a workspace choice.
var complementOpeners = []string{"if", "whether"}

// requestPhraseHeads are multi-word request forms whose head token is not one of
// the classifier's verbs.
//
// "see if" is a directive to determine something, not a question about the
// world: the investigative phrase vocabulary already treats "see if it fails"
// as a probe, so it must not be swallowed by the "if" complement opener.
var requestPhraseHeads = []string{"make sure", "tidy up", "clean up", "see if", "see whether"}

// interrogativeHeads are clause heads that open a question about an operation.
// They are a subset of readHeads plus the auxiliaries that only appear in
// questions ("do", "am") and the complement openers.
var interrogativeHeads = map[string]bool{
	"what": true, "what's": true, "whats": true,
	"why": true, "why's": true,
	"how": true, "how's": true,
	"where": true, "where's": true,
	"when": true, "which": true, "who": true, "who's": true, "whose": true,
	"is": true, "are": true, "was": true, "were": true,
	"does": true, "did": true, "do": true, "has": true, "have": true,
	"can": true, "could": true, "would": true, "should": true, "will": true,
	"am": true, "if": true, "whether": true,
}

// clauseHead skips command and discourse wrappers that do not change the
// requested operation. In particular, "git commit" is execution intent, but
// "tell me how to git commit" remains informational because its clause starts
// with "tell".
func clauseHead(clause string) string {
	fields := strings.Fields(clause)
	if len(fields) == 0 {
		return ""
	}
	if fields[0] == "git" && len(fields) > 1 {
		return fields[1]
	}
	return fields[0]
}
