package tui

import (
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Live telemetry phrases.
//
// The bar names the concrete action the runtime is performing, not only the
// coarse phase. The phrase is derived from runtime evidence that already
// exists: an open reasoning trace's own channel, or a running tool's
// argument-derived invocation. It never reads model prose as an instruction and
// never invents an action the runtime did not start.
//
// Presentation stability is a separate concern: a phrase change is latched for
// telemetryStatusDwell so a burst of fast calls cannot flicker the row. The
// latch is frame-driven; it delays pixels, never execution.

// telemetryStatusDwell is the minimum time one phrase stays visible. A call
// that starts and ends inside the floor still contributes a readable action.
const telemetryStatusDwell = 500 * time.Millisecond

// telemetryStatus is the latched phrase. It is presentation state only: the
// runtime remains the owner of what is actually happening, and the next frame
// re-derives the candidate from that runtime state.
type telemetryStatus struct {
	text  string
	since time.Time
}

// telemetryStatusCandidate derives the phrase the runtime can currently prove.
//
// A running tool yields its concrete action ("reading retry.go"); an open
// reasoning trace yields the primary verb of its first clause ("inspecting the
// repository"). Whenever the evidence is too thin, the deterministic phase
// label remains the fallback, so the bar never goes blank mid-turn.
func telemetryStatusCandidate(s State, thinking ThinkingState) string {
	var phrase string
	switch thinking.Phase {
	case ThinkingTool:
		if tool := latestRunningTool(s); tool != nil {
			phrase = toolProgressPhrase(tool)
		}
	case ThinkingModel:
		if index := s.openReasoning(); index >= 0 && s.Entries[index].Reasoning != nil {
			phrase = reasoningProgressPhrase(s.Entries[index].Reasoning.Text)
		}
	}
	if phrase == "" {
		phrase = telemetryIntentLabel(s, thinking)
	}
	return normalizeTelemetryPhrase(phrase)
}

// normalizeTelemetryPhrase applies the status-line typography contract to one
// generated phrase: lowercase, no trailing sentence punctuation, and no
// dangling dots. Tokens that are paths, files, URLs, flags, or mixed-case code
// symbols keep their spelling; the sentence around them is lowercased.
func normalizeTelemetryPhrase(phrase string) string {
	phrase = strings.TrimSpace(strings.TrimRight(phrase, "…"))
	if phrase == "" {
		return ""
	}
	fields := strings.Fields(phrase)
	if len(fields) == 0 {
		return ""
	}
	// A code/path tail keeps literal dots ("./..." is part of the command);
	// ordinary prose loses the sentence period.
	last := fields[len(fields)-1]
	if strings.HasSuffix(last, ".") {
		n := 1
		for n < len(last) && last[len(last)-1-n] == '.' {
			n++
		}
		// A single period is sentence punctuation even after a path. A run of
		// dots is only kept when it belongs to a path-like token ("./...").
		if n == 1 || !strings.ContainsAny(last, `/\`) {
			last = last[:len(last)-n]
		}
	}
	if last == "" {
		fields = fields[:len(fields)-1]
	} else {
		fields[len(fields)-1] = last
	}
	for i, field := range fields {
		fields[i] = normalizeTelemetryToken(field)
	}
	return strings.Join(fields, " ")
}

// normalizeTelemetryToken lowercases prose while preserving explicit code and
// file references. It is deliberately conservative: a token that looks like a
// symbol keeps its bytes rather than being rewritten by presentation.
func normalizeTelemetryToken(token string) string {
	core := strings.Trim(token, "\"'`()[]{}.,;:!?")
	if core == "" {
		return token
	}
	if isCodeLikeTelemetryToken(core) {
		return token
	}
	return strings.ToLower(token)
}

func isCodeLikeTelemetryToken(token string) bool {
	if strings.ContainsAny(token, `/\`) || strings.Contains(token, "://") {
		return true
	}
	if strings.HasPrefix(token, "-") || strings.Contains(token, "_") || strings.Contains(token, "=") {
		return true
	}
	runes := []rune(token)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			return true
		}
	}
	if len(runes) > 1 {
		upper := strings.ToUpper(token)
		if upper == token && strings.IndexFunc(token, unicode.IsLetter) >= 0 {
			return true
		}
	}
	if dot := strings.IndexByte(token, '.'); dot > 0 && dot < len(token)-1 {
		return true
	}
	return false
}

// telemetryLabel returns the visible phrase. A latched phrase wins only while
// the current phase still has a candidate: when the runtime settles, the row
// must release rather than resurrecting the last action it remembered.
func telemetryLabel(s State, thinking ThinkingState) string {
	candidate := telemetryStatusCandidate(s, thinking)
	if candidate == "" {
		return ""
	}
	if s.telemetry.text != "" {
		return s.telemetry.text
	}
	return candidate
}

// noteTelemetryStatus advances the dwell latch to the current frame.
//
// A single pending candidate is enough: when several updates land inside the
// floor, the newest candidate is the one still true when the floor expires, so
// stale intermediates are never replayed. That is the queue — coalesced to the
// latest live action rather than a backlog lagging behind execution.
func (s *State) noteTelemetryStatus(at time.Time) {
	if at.IsZero() {
		at = s.frameAt
	}
	if at.IsZero() {
		at = time.Now()
	}
	candidate := telemetryStatusCandidate(*s, ProjectThinking(*s))
	if candidate == "" {
		s.telemetry = telemetryStatus{}
		return
	}
	if s.telemetry.since.After(at) {
		at = s.telemetry.since
	}
	if s.telemetry.text == candidate {
		if s.telemetry.since.IsZero() {
			s.telemetry.since = at
		}
		return
	}
	if s.telemetry.text != "" && at.Sub(s.telemetry.since) < telemetryStatusDwell {
		return
	}
	s.telemetry = telemetryStatus{text: candidate, since: at}
}

// resetTelemetryStatus releases the latch at a turn boundary. The next turn
// starts from its own evidence rather than inheriting the previous action.
func (s *State) resetTelemetryStatus() {
	s.telemetry = telemetryStatus{}
}

// toolProgressPhrase maps a running call to a lower-case present-participle
// progress description, applying the shared status typography. The mapping is
// deterministic and reads only the tool name and the argument-derived
// invocation that the runtime already published.
func toolProgressPhrase(tool *ToolEntry) string {
	return normalizeTelemetryPhrase(toolProgressPhraseRaw(tool))
}

func toolProgressPhraseRaw(tool *ToolEntry) string {
	if tool == nil {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	target := toolProgressTarget(tool)
	switch name {
	case "read":
		if hasLeadingAction(target) {
			return "Reading a file"
		}
		return progressPhrase("Reading", shortTarget(target), "a file")
	case "write":
		if hasLeadingAction(target) {
			return "Writing a file"
		}
		return progressPhrase("Writing", shortTarget(target), "a file")
	case "edit":
		if hasLeadingAction(target) {
			return "Editing a file"
		}
		return progressPhrase("Editing", shortTarget(target), "a file")
	case "delete":
		if hasLeadingAction(target) {
			return "Deleting a file"
		}
		return progressPhrase("Deleting", shortTarget(target), "a file")
	case "bash":
		return commandProgress(target)
	case "wait":
		return "Waiting for background jobs"
	case "verify":
		return "Verifying changes"
	case "glob":
		return progressPhrase("Finding files matching", target, "the workspace")
	case "grep", "search":
		return progressPhrase("Searching for", target, "a pattern")
	case "ls":
		if target == "" || target == "." || target == "/" || hasLeadingAction(target) {
			return "listing files"
		}
		return progressPhrase("Listing", target, "")
	case "symbols":
		return progressPhrase("Looking up", target, "symbols")
	case "graph":
		return "Tracing code relationships"
	case "refs":
		return progressPhrase("Finding references to", target, "a symbol")
	case "structure":
		return "Analyzing repository structure"
	case "retrieve":
		return progressPhrase("Searching code for", target, "the relevant code")
	case "investigate":
		return "Investigating the codebase"
	case "web_fetch":
		return progressPhrase("Fetching", target, "a URL")
	case "browser":
		if rawURL := urlTarget(target); rawURL != "" {
			return "Browsing " + rawURL
		}
		return "Browsing the page"
	case "mcp_list":
		return "Listing MCP tools"
	case "mcp_read":
		return progressPhrase("Reading MCP resource", target, "over MCP")
	case "mcp_call":
		return progressPhrase("Calling MCP tool", target, "over MCP")
	case "todo":
		return "Updating the task list"
	case "task":
		return "Delegating a subtask"
	case "artifact":
		if hasLeadingAction(target) {
			return "Reading an artifact"
		}
		return progressPhrase("Reading artifact", shortTarget(target), "")
	case "recall":
		return "Recalling session context"
	case "":
		return ""
	default:
		return progressPhrase("Running", strings.ReplaceAll(name, "_", " "), "a tool")
	}
}

// toolProgressTarget extracts the action target from the invocation, falling
// back to the compact summary for legacy callers that published only one.
func toolProgressTarget(tool *ToolEntry) string {
	raw := strings.TrimSpace(tool.Invocation)
	if raw == "" {
		raw = strings.TrimSpace(tool.Summary)
	}
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "external ") {
		raw = strings.TrimSpace(raw[len("external "):])
		lower = strings.ToLower(raw)
	}
	if name != "" && strings.HasPrefix(lower, name+" ") {
		raw = strings.TrimSpace(raw[len(name):])
	}
	return strings.Trim(raw, "\"'`")
}

// hasLeadingAction reports that a legacy summary carried its own participle
// ("inspecting backend/internal") instead of a bare target. Reusing that text
// would restate the entry and claim two surfaces for one action, so the caller
// falls back to its own generic phrase.
func hasLeadingAction(target string) bool {
	fields := strings.Fields(target)
	return len(fields) > 1 && strings.HasSuffix(strings.ToLower(fields[0]), "ing") && isActionWord(fields[0])
}

// shortTarget keeps the basename of a path-like target and leaves prose,
// commands, and URLs intact. The bar truncates to the terminal width later, so
// this only removes the directory noise that would hide the useful tail.
func shortTarget(target string) string {
	if target == "" {
		return ""
	}
	if strings.Contains(target, "://") {
		return target
	}
	trimmed := strings.TrimRight(strings.ReplaceAll(target, "\\", "/"), "/")
	if !strings.Contains(trimmed, "/") {
		return trimmed
	}
	return filepath.Base(trimmed)
}

// progressPhrase assembles "<verb> <target>" with a truthful fallback noun
// when the invocation carried no target. Typography is applied by the caller.
func progressPhrase(verb, target, fallback string) string {
	if target == "" {
		target = fallback
	}
	if target == "" {
		return verb
	}
	return verb + " " + target
}

// urlTarget returns the URL token inside a compound browser target such as
// "navigate https://example.com". An invocation with no URL stays empty so the
// caller can name the page rather than inventing a target.
func urlTarget(target string) string {
	for _, field := range strings.Fields(target) {
		if strings.Contains(field, "://") {
			return strings.Trim(field, "\"'`")
		}
	}
	return ""
}

// commandProgress maps a shell command to the phase a user recognises. It is a
// deterministic keyword mapping, not a model judgement: an unrecognised
// command still names what actually ran.
func commandProgress(command string) string {
	if command == "" {
		return "Running a command"
	}
	lower := strings.ToLower(command)
	switch {
	case containsAny(lower, "go test", "go vet", "pytest", "npm test", "yarn test", "cargo test", "make test", "bun test", "vitest", "jest"):
		return "Running tests"
	case containsAny(lower, "golangci-lint", "gofmt", "go fmt", "eslint", "ruff check", "prettier"):
		return "Linting the code"
	case containsAny(lower, "go mod", "go get", "go generate", "npm install", "npm ci", "yarn add", "pnpm add", "pip install", "cargo add", "cargo fetch", "bundle install"):
		return "Resolving dependencies"
	case containsAny(lower, "go build", "cargo build", "npm run build", "yarn build", "go install", "make ", "cmake"):
		return "Building the project"
	case strings.HasPrefix(lower, "git "):
		return "Inspecting git state"
	}
	return "Running " + clampProgress(command, 40)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// reasoningProgressPhrase extracts the primary action clause from a thought
// block and normalises its leading verb to the present participle. It returns
// "" when the trace has no recognisable action, so the caller can fall back to
// the phase label rather than presenting prose as a progress claim.
func reasoningProgressPhrase(text string) string {
	clause := firstReasoningClause(text)
	if clause == "" {
		return ""
	}
	verb, rest, ok := primaryAction(clause)
	if !ok {
		return ""
	}
	phrase := presentParticiple(verb)
	if rest = strings.TrimSpace(rest); rest != "" {
		phrase += " " + rest
	}
	phrase = clampProgress(strings.TrimRight(phrase, " .,;:!?…"), 56)
	if phrase == "" {
		return ""
	}
	return normalizeTelemetryPhrase(phrase)
}

// firstReasoningClause bounds extraction to the first sentence of the first
// non-empty line. A long trace should not make the status row churn through
// every later paragraph.
func firstReasoningClause(text string) string {
	// Only the opening sentence can name the action. Bounding the scan keeps
	// phrase extraction linear in the first delta rather than the whole trace.
	const scanLimit = 1024
	if len(text) > scanLimit {
		text = text[:scanLimit]
	}
	text = sanitizeInline(text)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*•>#0123456789. "))
		if line == "" {
			continue
		}
		if end := strings.IndexAny(line, ".!?;:"); end >= 0 {
			line = line[:end]
		}
		return strings.TrimSpace(line)
	}
	return ""
}

// reasoningLeadIns are discourse prefixes that sit between the model and the
// action it is about to take. They are stripped before verb selection so the
// phrase starts at the action rather than at "I need to".
var reasoningLeadIns = []string{
	"okay, ", "ok, ", "hmm, ", "alright, ", "sure, ", "first, ", "next, ",
	"now, ", "then, ", "so, ", "let me ", "let's ", "let us ",
	"i need to ", "i want to ", "i should ", "i will ", "i'll ", "i can ",
	"i must ", "i am going to ", "i'm going to ", "i have to ",
	"we need to ", "we want to ", "we should ", "we will ", "we'll ", "we can ",
	"the user wants me to ", "the user is asking me to ", "the user asked me to ",
}

// primaryAction selects the clause's leading verb and the text after it.
func primaryAction(clause string) (verb string, rest string, ok bool) {
	for {
		lower := strings.ToLower(clause)
		stripped := false
		for _, lead := range reasoningLeadIns {
			if strings.HasPrefix(lower, lead) {
				clause = strings.TrimSpace(clause[len(lead):])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	spans := wordSpans(clause)
	if len(spans) == 0 {
		return "", "", false
	}
	// Prefer an explicit infinitive: "wants me to check" names check, not
	// want. Search " to " from the start so the earliest action wins.
	if index := infinitiveVerb(spans); index >= 0 {
		span := spans[index]
		return clause[span.start:span.end], clause[span.end:], true
	}
	for index, span := range spans {
		if isActionWord(span.word) && !precededByDeterminer(spans, index) {
			return span.word, clause[span.end:], true
		}
	}
	return "", "", false
}

// precededByDeterminer filters nouns that share a spelling with a verb, such
// as "the tests". A verb after "to" is handled by the infinitive path first.
func precededByDeterminer(spans []wordSpan, index int) bool {
	if index == 0 {
		return false
	}
	switch strings.ToLower(spans[index-1].word) {
	case "a", "an", "any", "each", "every", "her", "his", "its", "my", "no",
		"our", "some", "that", "the", "their", "these", "this", "those", "your":
		return true
	}
	return false
}

type wordSpan struct {
	start int
	end   int
	word  string
}

// wordSpans finds letter/apostrophe runs, preserving offsets into the original
// clause so the rest of the phrase keeps its casing.
func wordSpans(text string) []wordSpan {
	var spans []wordSpan
	start := -1
	for index, r := range text {
		if unicode.IsLetter(r) || r == '\'' {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 {
			spans = append(spans, wordSpan{start: start, end: index, word: text[start:index]})
			start = -1
		}
	}
	if start >= 0 {
		spans = append(spans, wordSpan{start: start, end: len(text), word: text[start:]})
	}
	return spans
}

// infinitiveVerb finds "to <verb>" and returns the verb span. A lead-in strip
// handles the common case; this covers "the model wants me to inspect …".
func infinitiveVerb(spans []wordSpan) int {
	for index := 1; index < len(spans); index++ {
		if strings.EqualFold(spans[index-1].word, "to") && isActionWord(spans[index].word) {
			return index
		}
	}
	return -1
}

// isActionWord reports whether the word is a verb form the extractor trusts.
func isActionWord(word string) bool {
	lower := strings.ToLower(strings.Trim(word, "'"))
	if reasoningNonActionWords[lower] {
		return false
	}
	if _, ok := reasoningVerbForms[lower]; ok {
		return true
	}
	return strings.HasSuffix(lower, "ing") && len(lower) > 4
}

// reasoningNonActionWords look like participles but are nouns, pronouns, or
// connectives. Treating them as verbs would turn prose into a false action.
var reasoningNonActionWords = map[string]bool{
	"anything":   true,
	"during":     true,
	"everything": true,
	"nothing":    true,
	"something":  true,
	"thing":      true,
	"things":     true,
	"string":     true,
	"strings":    true,
}

// reasoningVerbs are the base verbs common in coding-agent traces. The table
// keeps normalisation deterministic; unknown words never become a claim.
var reasoningVerbs = []string{
	"act", "add", "adjust", "analyze", "answer", "apply", "assess", "audit",
	"build", "call", "check", "clarify", "clean", "collect", "compare",
	"compile", "confirm", "connect", "consider", "continue", "create", "debug",
	"decide", "define", "delete", "describe", "design", "detect", "determine",
	"document", "edit", "ensure", "establish", "evaluate", "examine", "execute",
	"explore", "extract", "fetch", "figure", "find", "fix", "follow", "gather",
	"generate", "get", "handle", "identify", "implement", "improve", "include",
	"inspect", "install", "investigate", "keep", "look", "make", "map", "match",
	"modify", "move", "open", "parse", "plan", "prepare", "print", "process",
	"provide", "read", "refactor", "remove", "rename", "replace", "resolve",
	"restart", "retrieve", "review", "rewrite", "run", "scan", "search", "see",
	"set", "show", "simplify", "start", "stop", "structure", "summarize",
	"test", "trace", "try", "understand", "update", "use", "validate",
	"verify", "walk", "work", "write",
}

// reasoningVerbForms maps every accepted inflection to its participle.
var reasoningVerbForms = buildReasoningVerbForms()

func buildReasoningVerbForms() map[string]string {
	forms := make(map[string]string, len(reasoningVerbs)*4)
	for _, base := range reasoningVerbs {
		participle := presentParticiple(base)
		forms[base] = participle
		forms[base+"ing"] = participle
		forms[base+"s"] = participle
		forms[base+"ed"] = participle
		if strings.HasSuffix(base, "e") {
			forms[base+"d"] = participle
		}
	}
	return forms
}

// irregularParticiples cannot be derived by the spelling rules below.
var irregularParticiples = map[string]string{
	"be":     "being",
	"begin":  "beginning",
	"commit": "committing",
	"debug":  "debugging",
	"drop":   "dropping",
	"embed":  "embedding",
	"get":    "getting",
	"map":    "mapping",
	"plan":   "planning",
	"put":    "putting",
	"run":    "running",
	"scan":   "scanning",
	"see":    "seeing",
	"set":    "setting",
	"ship":   "shipping",
	"skip":   "skipping",
	"stop":   "stopping",
	"submit": "submitting",
	"trim":   "trimming",
	"wrap":   "wrapping",
	"write":  "writing",
}

// presentParticiple normalises a verb to its -ing form. Unknown spellings use
// the regular e-drop rule and remain readable even when they are not perfect.
func presentParticiple(verb string) string {
	lower := strings.ToLower(strings.Trim(verb, "'"))
	if participle, ok := irregularParticiples[lower]; ok {
		return participle
	}
	if strings.HasSuffix(lower, "ing") && len(lower) > 4 {
		return lower
	}
	switch {
	case strings.HasSuffix(lower, "ies") && len(lower) > 4:
		return lower[:len(lower)-3] + "ying"
	case strings.HasSuffix(lower, "es") && len(lower) > 4:
		lower = lower[:len(lower)-2]
	case strings.HasSuffix(lower, "s") && !strings.HasSuffix(lower, "ss") && len(lower) > 3:
		lower = lower[:len(lower)-1]
	}
	if strings.HasSuffix(lower, "ed") && len(lower) > 4 {
		lower = lower[:len(lower)-2]
	}
	if strings.HasSuffix(lower, "e") && !strings.HasSuffix(lower, "ee") &&
		!strings.HasSuffix(lower, "ye") && !strings.HasSuffix(lower, "oe") && len(lower) > 2 {
		return lower[:len(lower)-1] + "ing"
	}
	return lower + "ing"
}

// clampProgress collapses whitespace and bounds a phrase by runes so the bar
// has a stable semantic string before width truncation runs.
func clampProgress(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimRight(string(runes[:limit]), " ,;:-")
}
