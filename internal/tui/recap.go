package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// recapPrefix is the single recap marker every finished turn carries.
//
// It is a reference mark, not a completion badge: the line closes the turn
// without competing with the tool outcomes above it. The glyph names the state
// independently of colour, so the footer survives a monochrome terminal.
const recapPrefix = "※ recap ·"

// recapGreeting is the narrative for a conversational turn that asked for no
// work. It is deliberately short: a greeting produced no files, commands, or
// findings to summarise, and inventing evidence would make the recap lie.
const recapGreeting = "Greeted and returned to the prompt."

// recapNarrative derives a semantic completion summary from the run's own
// evidence.
//
// The recap never echoes the request and never reports duration or token
// counts: those are telemetry, available elsewhere, while the transcript needs
// a human sentence about what actually happened. Classification is by observed
// work, not by model identity or by wording: a turn that changed files is
// described as a change, a read-only turn names the inspected files and its
// finding, and a greeting stays a greeting. A turn with no evidence and no
// finding produces no footer rather than boilerplate.
//
// An inspection names the files the answer is about, not the first files the
// run happened to open: a survey that read five files to explain one of them
// must not cite only the README it skimmed. When more files were read than
// named, the count says so, and a read that returned a window rather than a
// whole file is disclosed instead of implying a complete reading.
func recapNarrative(objective string, turns []agent.Turn) string {
	if recapIsGreeting(objective) {
		return recapGreeting
	}
	evidence := collectRecapEvidence(turns)
	answer := finalAssistantText(turns)
	finding := recapFinding(answer)
	switch {
	case len(evidence.changed) > 0:
		return mutationRecap(evidence, finding)
	case evidence.runtime != nil:
		return runtimeRecap(evidence, finding)
	case len(evidence.commands) > 0:
		return executionRecap(evidence, finding)
	case len(recapReadPaths(evidence.read)) > 0:
		return inspectionRecap(evidence, finding, answer)
	default:
		return finishSentence(finding)
	}
}

// recapFinding is the answer's substantive finding plus one useful detail: when
// the answer includes fenced code, the recap names the worked example instead
// of leaving a reader to scroll back to discover it.
func recapFinding(answer string) string {
	finding := answerSubject(answer)
	if finding == "" || !strings.Contains(answer, "```") {
		return finding
	}
	finding = strings.TrimRight(finding, " .!?;:")
	return finding + "; included a worked example."
}

// recapChange is one file the run successfully wrote, edited, or removed.
type recapChange struct {
	path   string
	remove bool
}

// recapRuntime is the live project runtime the run actually observed.
//
// It is a separate fact from the commands that ran: an environment probe such
// as "node -v" is a command, while "npm run dev served and answered a check" is
// the outcome that proved the objective. Keeping them apart is what stops the
// recap from attributing a runtime result to an incidental probe.
type recapRuntime struct {
	command string
	url     string
	checks  int
}

// recapRead is one file the run actually read.
//
// chars records what the read returned, so a survey that read five files to
// explain one of them can name the file that carried the content instead of
// the first file opened. truncated/shown/total come from the read tool's own
// range, and exist so the footer never implies a whole-file reading it did
// not perform.
type recapRead struct {
	path      string
	order     int
	chars     int
	truncated bool
	// firstLine and lastLine bound the window the read returned; totalLines is
	// the file's own line count. A paged read starts above one, so the footer
	// can say "lines 200-400 of 512" instead of implying the file head.
	firstLine  int
	lastLine   int
	totalLines int
}

// recapEvidence is the bounded set of facts the narrator may use.
//
// Only successful calls contribute: a failed write did not change a file, and
// a denied call ran nothing, so neither may appear as accomplished work.
type recapEvidence struct {
	changed  []recapChange
	read     []recapRead
	commands []string
	runtime  *recapRuntime
}

// collectRecapEvidence folds a run's tool outcomes into recap facts.
//
// Paths and commands keep first-seen order and are deduplicated: a model that
// re-reads a file ten times performed one inspection, and a repeated command
// is still one command.
func collectRecapEvidence(turns []agent.Turn) recapEvidence {
	var evidence recapEvidence
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Refused() || outcome.Result.IsError {
				continue
			}
			// A live runtime is the outcome, not the commands that led to it.
			// Keep the newest observation: an earlier attempt may have failed.
			if runtime := outcome.Result.Runtime; runtime != nil {
				evidence.runtime = &recapRuntime{
					command: runtime.Command,
					url:     runtime.URL,
					checks:  runtime.Checks,
				}
			}
			name := strings.ToLower(strings.TrimSpace(outcome.ToolName))
			switch name {
			case "write", "edit", "delete":
				if path := recapPathArg(outcome.Args); path != "" {
					evidence.changed = appendUniqueChange(evidence.changed, recapChange{
						path:   path,
						remove: name == "delete",
					})
				}
			case "bash":
				if command := recapStringArg(outcome.Args, "command"); command != "" {
					evidence.commands = appendUnique(evidence.commands, recapCommand(command))
				}
			case "read":
				// Only a file read is a "Read X" claim. A listing, grep, or
				// structural query names a directory or a scope; calling that
				// a read would misdescribe the work.
				if path := recapPathArg(outcome.Args); path != "" {
					window := recapReadWindow(outcome.Result.Details)
					window.path = path
					window.order = len(evidence.read)
					window.chars = len(outcome.Result.Text)
					evidence.read = appendUniqueRead(evidence.read, window)
				}
			}
		}
	}
	return evidence
}

// recapIsGreeting reports whether the request was purely conversational.
//
// The table is a fixed literal set matched on the whole normalised request,
// deliberately not an intent classifier: "hi" produces no artifact to narrate,
// while anything unlisted is treated as a real turn. Politeness padding and
// trailing punctuation are stripped first, so "hello!" and "  Hi  " match.
func recapIsGreeting(text string) bool {
	normalised := strings.ToLower(sanitizeInline(text))
	normalised = strings.Join(strings.Fields(normalised), " ")
	normalised = strings.Trim(normalised, " .,!?~:;")
	switch normalised {
	case "hi", "hi there", "hello", "hello there", "hey", "hey there",
		"hiya", "howdy", "yo", "sup", "what's up", "whats up",
		"how are you", "how's it going", "hows it going", "how are you doing",
		"good morning", "good afternoon", "good evening", "good night",
		"thanks", "thank you", "thx", "ok", "okay", "cool", "nice", "great",
		"see you", "goodbye", "bye":
		return true
	}
	return false
}

// mutationRecap narrates a turn that changed repository files. The verb follows
// the observed operation: a pure deletion is never reported as an update.
func mutationRecap(evidence recapEvidence, finding string) string {
	mutations := 0
	for _, change := range evidence.changed {
		if change.remove {
			mutations++
		}
	}
	verb := "Updated"
	switch {
	case mutations == len(evidence.changed):
		verb = "Removed"
	case mutations > 0:
		verb = "Changed"
	}
	recap := verb + " " + recapList(recapChangePaths(evidence.changed, 2))
	if command := recapVerification(evidence.commands); command != "" {
		recap += ", then verified the change with " + command
	}
	return finishSentence(appendFinding(recap, finding))
}

// executionRecap narrates a turn that ran a process without changing files.
// The assistant's own closing sentence is appended as the observed outcome;
// without one the recap names only the command it can prove ran.
func executionRecap(evidence recapEvidence, finding string) string {
	recap := "Ran " + recapList(evidence.commands)
	return finishSentence(appendFinding(recap, finding))
}

// runtimeRecap narrates the live runtime observation that proved the objective.
//
// The environment probe a model runs while orienting itself ("node -v",
// "npm -v") is real work, but it is not the result. Naming those commands
// attributed the proof to the wrong action; the recap names the service that
// was launched and the address it answered on.
func runtimeRecap(evidence recapEvidence, finding string) string {
	observed := evidence.runtime
	recap := "Ran " + recapCommand(observed.command)
	switch {
	case observed.url != "" && observed.checks > 1:
		recap += fmt.Sprintf(" and verified %s answered %d checks", observed.url, observed.checks)
	case observed.url != "":
		recap += " and verified " + observed.url + " answered"
	case observed.checks > 1:
		recap += fmt.Sprintf(" and verified the live process with %d checks", observed.checks)
	default:
		recap += " and verified the live process"
	}
	return finishSentence(appendFinding(recap, finding))
}

// inspectionRecap narrates an informational turn from the files it actually
// read and the finding above it. Paths are removed from the finding first so
// "Inspected backend/go.mod — the module name in backend/go.mod is ecommerce"
// does not repeat the same file twice.
//
// The named files are ranked by how much they carried the answer (see
// rankRecapReads), the total is reported when it was larger, and a read that
// stopped at a window names the range. A footer that named only the first two
// files opened described the run's ordering, not its finding.
func inspectionRecap(evidence recapEvidence, finding, answer string) string {
	ranked := rankRecapReads(evidence.read, answer)
	names := recapPaths(recapReadPaths(ranked), 2)
	recap := "Inspected " + recapList(names)
	if len(ranked) > len(names) {
		recap = fmt.Sprintf("Inspected %d files, including %s", len(ranked), recapList(names))
	}
	if clause := recapTruncationClause(ranked, len(ranked)); clause != "" {
		recap += " (" + clause + ")"
	}
	finding = stripRecapPaths(finding, recapReadPaths(evidence.read))
	return finishSentence(appendFinding(recap, finding))
}

// rankRecapReads orders inspected files by how much they carried the answer.
//
// Priority is: a path the answer itself cites, then the file that returned the
// most content, then first-seen order. The answer's own citation is the
// strongest signal available without a model call, and content volume is the
// next best proxy for which file the finding came from. The order is total and
// deterministic, so the same run always narrates identically.
func rankRecapReads(reads []recapRead, answer string) []recapRead {
	ranked := append([]recapRead(nil), reads...)
	sort.SliceStable(ranked, func(i, j int) bool {
		citedI := answerMentionsRead(answer, ranked[i].path)
		citedJ := answerMentionsRead(answer, ranked[j].path)
		if citedI != citedJ {
			return citedI
		}
		if ranked[i].chars != ranked[j].chars {
			return ranked[i].chars > ranked[j].chars
		}
		return ranked[i].order < ranked[j].order
	})
	return ranked
}

// answerMentionsRead reports whether the answer cites a inspected file, by its
// display path or its base name. A path named in the conclusion is evidence
// the reader needs; a path the run merely skimmed is not.
func answerMentionsRead(answer, path string) bool {
	lower := strings.ToLower(answer)
	if lower == "" {
		return false
	}
	if display := strings.ToLower(recapPath(path)); display != "" && strings.Contains(lower, display) {
		return true
	}
	base := strings.ToLower(filepath.Base(filepath.ToSlash(strings.TrimSpace(path))))
	return base != "" && base != "." && base != "/" && strings.Contains(lower, base)
}

// recapTruncationClause names the read window that stopped short. It is silent
// when every read returned the whole file, and omits the path when the recap
// already names exactly one file.
func recapTruncationClause(ranked []recapRead, total int) string {
	for _, read := range ranked {
		if !read.truncated || read.totalLines <= 0 {
			continue
		}
		var rangeText string
		if read.firstLine > 1 {
			rangeText = fmt.Sprintf("lines %d-%d of %d read", read.firstLine, read.lastLine, read.totalLines)
		} else {
			rangeText = fmt.Sprintf("%d of %d lines", read.lastLine, read.totalLines)
		}
		if total == 1 {
			if read.firstLine > 1 {
				return rangeText
			}
			return "truncated at " + rangeText
		}
		if read.firstLine > 1 {
			return fmt.Sprintf("%s: %s", recapPath(read.path), rangeText)
		}
		return fmt.Sprintf("%s was truncated at %s", recapPath(read.path), rangeText)
	}
	return ""
}

// recapReadPaths projects reads to display paths, preserving the ranked order.
func recapReadPaths(reads []recapRead) []string {
	out := make([]string, 0, len(reads))
	for _, read := range reads {
		out = append(out, read.path)
	}
	return out
}

// recapReadWindow reports the line window a read returned, and whether it
// stopped short of the whole file. The read tool publishes the range; without
// it the recap claims no completeness either way.
func recapReadWindow(details any) recapRead {
	values, ok := details.(map[string]any)
	if !ok {
		return recapRead{}
	}
	total := recapInt(values["totalLines"])
	limit := recapInt(values["limit"])
	offset := recapInt(values["offset"])
	if offset <= 0 {
		offset = 1
	}
	if total <= 0 || limit <= 0 {
		return recapRead{}
	}
	end := offset + limit - 1
	if end >= total {
		return recapRead{}
	}
	if end > total {
		end = total
	}
	return recapRead{truncated: true, firstLine: offset, lastLine: end, totalLines: total}
}

// recapInt reads an integer from tool details, which may carry any numeric
// type depending on the JSON decode path.
func recapInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case uint64:
		return int(typed)
	default:
		return 0
	}
}

// appendFinding joins the observed action with the answer's substantive
// finding. A finding that simply repeats the action ("Updated…", "Read…") adds
// no information and is omitted; a distinct conclusion is set off with an em
// dash so the footer reads as one short paragraph rather than two fragments.
func appendFinding(recap, finding string) string {
	finding = strings.TrimSpace(finding)
	if finding == "" || recapEchoesAction(finding) {
		return recap
	}
	return recap + " — " + lowerClause(finding)
}

// recapEchoesAction reports whether the answer merely restates the operation
// the recap already narrates.
func recapEchoesAction(finding string) bool {
	lowered := strings.ToLower(strings.TrimSpace(finding))
	for _, prefix := range []string{
		"updated ", "changed ", "removed ", "wrote ", "edited ", "deleted ",
		"ran ", "inspected ", "read ", "checked ",
	} {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

// lowerClause lowercases a clause's first letter when the first word is a
// common article or pronoun. Proper nouns ("Go", "GitHub") keep their case.
func lowerClause(text string) string {
	for _, prefix := range []string{
		"The ", "This ", "These ", "Those ", "It ", "Its ", "All ", "Every ",
		"Most ", "Some ", "No ", "There ", "A ", "An ", "If ", "When ",
	} {
		if strings.HasPrefix(text, prefix) {
			return strings.ToLower(prefix[:1]) + strings.TrimPrefix(text, prefix[:1])
		}
	}
	return text
}

// stripRecapPaths removes the inspected paths from a finding, including the
// preposition that introduced them, so the recap does not name the same file
// twice. Both the original path and the two-segment display form are removed.
func stripRecapPaths(text string, paths []string) string {
	for _, path := range paths {
		variants := []string{strings.TrimSpace(path)}
		if short := recapPath(path); short != "" {
			variants = append(variants, short)
		}
		for _, variant := range variants {
			if variant == "" {
				continue
			}
			// A parenthesised citation is removed whole: leaving "()" behind
			// would make the footer look like a code change.
			for _, wrapped := range []string{
				"(" + variant + ")",
				"(see " + variant + ")",
				"(in " + variant + ")",
				"(from " + variant + ")",
			} {
				text = strings.ReplaceAll(text, wrapped, "")
			}
			for _, lead := range []string{" in ", " from ", " at "} {
				text = strings.ReplaceAll(text, lead+variant, "")
			}
			text = strings.ReplaceAll(text, variant, "")
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, " .", ".")
	// Removing a parenthesised path would otherwise leave an empty pair.
	for _, empty := range []string{"()", "[]", "( )", "[ ]"} {
		text = strings.ReplaceAll(text, empty, "")
	}
	return strings.TrimSpace(text)
}

// answerSubject returns the first substantive, non-announcement sentence of an
// answer, extended with the next sentence when the first is short.
//
// It never cuts a sentence short: the recap is displayed as wrapped prose, so
// a long finding is allowed to take the rows it needs. Ending a recap in an
// ellipsis would read as unfinished work rather than completion.
func answerSubject(text string) string {
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "|") ||
			strings.HasPrefix(trimmed, "---") ||
			strings.HasPrefix(trimmed, "===") {
			continue
		}
		trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "-*+> "))
		if runes := []rune(trimmed); len(runes) > 2 &&
			runes[0] >= '0' && runes[0] <= '9' && (runes[1] == '.' || runes[1] == ')') && runes[2] == ' ' {
			trimmed = strings.TrimSpace(string(runes[3:]))
		}
		trimmed = strings.NewReplacer(
			"`", "", "**", "", "__", "", "*", "", "_", "",
		).Replace(trimmed)
		trimmed = strings.Join(strings.Fields(trimmed), " ")
		for _, lead := range []string{"sure,", "of course,", "certainly,", "absolutely,"} {
			if strings.HasPrefix(strings.ToLower(trimmed), lead) {
				trimmed = strings.TrimSpace(trimmed[len(lead):])
				break
			}
		}
		trimmed = strings.TrimSpace(strings.Trim(trimmed, "\"'“”‘’ "))

		var picked []string
		for _, sentence := range splitRecapSentences(trimmed) {
			sentence = strings.TrimSpace(sentence)
			if sentence == "" || answerLeadIn(sentence) {
				continue
			}
			picked = append(picked, sentence)
			if len([]rune(strings.Join(picked, " "))) >= 90 {
				break
			}
		}
		if len(picked) == 0 {
			continue
		}
		return strings.Join(picked, " ")
	}
	return ""
}

// splitRecapSentences splits a prose line at sentence punctuation. A period
// inside a decimal or version number is not followed by a space, so "Go 1.11"
// stays one sentence.
func splitRecapSentences(text string) []string {
	runes := []rune(text)
	var out []string
	start := 0
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i+1 < len(runes) && runes[i+1] != ' ' {
			continue
		}
		if sentence := strings.TrimSpace(string(runes[start : i+1])); sentence != "" {
			out = append(out, sentence)
		}
		start = i + 1
	}
	if start < len(runes) {
		if sentence := strings.TrimSpace(string(runes[start:])); sentence != "" {
			out = append(out, sentence)
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return out
}

// answerLeadIn reports whether a line only announces the answer that follows.
// The subject must be the answer's own content; repeating "here is my analysis"
// in the footer adds no information and displaces the real finding.
func answerLeadIn(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, phrase := range []string{
		"here's my analysis", "here is my analysis",
		"here's the analysis", "here is the analysis",
		"here's a summary", "here is a summary",
		"here's what", "here is what",
		"below is", "the following is",
		"let me explain", "let me summarize", "let me summarise",
		"let me check", "let me verify", "let me look", "let me inspect",
		"let me read", "let me run", "let me see", "let me confirm",
		"i'll explain", "i will explain",
		"i'll inspect", "i will inspect", "i'll analyze", "i will analyze",
		"i'll analyse", "i will analyse", "i'll examine", "i will examine",
		"i'll check", "i will check", "i inspected", "i analyzed",
		"i analysed", "i examined", "i checked", "i reviewed",
		"based on the evidence", "based on my inspection", "based on my analysis",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// finalAssistantText returns the last completed assistant text in a run.
func finalAssistantText(turns []agent.Turn) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if text := strings.TrimSpace(turns[i].Assistant.Text()); text != "" {
			return text
		}
	}
	return ""
}

// truncateClause bounds a clause at a word boundary, without adding an
// ellipsis that would collide with the sentence punctuation the recap adds.
func truncateClause(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	cut := string(runes[:limit])
	if space := strings.LastIndex(cut, " "); space > limit/2 {
		cut = cut[:space]
	}
	return strings.TrimSpace(cut)
}

// recapPathArg returns the display path from a tool call's common argument
// keys. The first non-empty key wins, matching the tool layer's own precedence.
func recapPathArg(args map[string]any) string {
	for _, key := range []string{"path", "file"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return recapPath(value)
		}
	}
	return ""
}

// recapStringArg returns one trimmed string argument.
func recapStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

// recapPath shortens a path to its last two segments.
//
// A model may name an absolute path; the transcript already shows the working
// directory in its header, so repeating the whole prefix would crowd out the
// sentence. Two trailing segments keep the file identifiable. The working
// directory itself is not a subject worth naming, so "." and "/" collapse to
// empty and the narrator falls back to the answer.
func recapPath(path string) string {
	cleaned := filepath.ToSlash(strings.TrimSpace(path))
	cleaned = strings.TrimPrefix(cleaned, "./")
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return ""
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

// recapCommand bounds a command for the single-line narrative.
func recapCommand(command string) string {
	return truncateClause(sanitizeInline(command), 48)
}

// recapPaths maps paths through recapPath and keeps at most limit entries.
func recapPaths(paths []string, limit int) []string {
	out := make([]string, 0, min(limit, len(paths)))
	for _, path := range paths {
		if len(out) == limit {
			break
		}
		if shortened := recapPath(path); shortened != "" {
			out = append(out, shortened)
		}
	}
	return out
}

// recapChangePaths projects changed files to display paths, keeping limit.
func recapChangePaths(changes []recapChange, limit int) []string {
	paths := make([]string, 0, min(limit, len(changes)))
	for _, change := range changes {
		if len(paths) == limit {
			break
		}
		paths = append(paths, change.path)
	}
	return paths
}

// finishSentence terminates a narrative exactly once. Commands such as
// "go test ./..." already end in punctuation; appending another period would
// render "....".
func finishSentence(text string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(text), " ")
	if trimmed == "" {
		return ""
	}
	last := []rune(trimmed)[len([]rune(trimmed))-1]
	if last == '.' || last == '!' || last == '?' || last == '…' {
		return trimmed
	}
	return trimmed + "."
}

// recapVerification picks the command that best reads as verification.
//
// A recognised check (test, build, vet) is stronger evidence than an arbitrary
// command, so it wins even when a later command also ran. When none matches,
// the most recent command is still honest evidence of what closed the turn.
func recapVerification(commands []string) string {
	if len(commands) == 0 {
		return ""
	}
	for _, command := range commands {
		lowered := strings.ToLower(command)
		if strings.Contains(lowered, "go test") ||
			strings.Contains(lowered, "go build") ||
			strings.Contains(lowered, "go vet") ||
			strings.Contains(lowered, "test") {
			return command
		}
	}
	return commands[len(commands)-1]
}

// recapList joins items as prose, without an Oxford comma for two items.
func recapList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

// appendUnique appends value when it is not already present.
func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// appendUniqueChange appends a change when its path is not already present.
// A path is one subject even when the run edited it several times.
func appendUniqueChange(values []recapChange, change recapChange) []recapChange {
	for _, existing := range values {
		if existing.path == change.path {
			return values
		}
	}
	return append(values, change)
}

// appendUniqueRead records one inspected file. Repeated reads of the same file
// are one inspection; the widest range and the largest payload win, and any
// observed truncation is kept because the footer must not imply a whole-file
// reading the run never had.
func appendUniqueRead(values []recapRead, read recapRead) []recapRead {
	for i, existing := range values {
		if existing.path != read.path {
			continue
		}
		if read.chars > existing.chars {
			values[i].chars = read.chars
		}
		if read.totalLines > existing.totalLines || (read.truncated && !existing.truncated) {
			values[i].firstLine = read.firstLine
			values[i].lastLine = read.lastLine
			values[i].totalLines = read.totalLines
		}
		values[i].truncated = existing.truncated || read.truncated
		return values
	}
	return append(values, read)
}

// taskRecapText returns the completion footer text for a task, or empty when
// the task has not actually completed or carries no useful narrative.
//
// The status check is deliberate: the recap is metadata, not a lifecycle
// authority, so a projection carrying one while still RUNNING must never emit a
// footer. Completion is decided by the task transition, never by the presence
// of a string. A task with no evidence and no finding stays silent rather than
// printing a boilerplate sentence that says nothing.
func taskRecapText(projection *TaskProjection) string {
	if projection == nil || !strings.EqualFold(strings.TrimSpace(projection.Status), "COMPLETED") {
		return ""
	}
	return strings.TrimSpace(sanitizeInline(projection.Recap))
}

// recapLine renders a completion footer's display form: the stable marker and
// the narrative, with no trailing separator.
func recapLine(text string) string {
	text = strings.TrimSpace(sanitizeInline(text))
	if text == "" {
		return ""
	}
	text = strings.TrimPrefix(text, recapPrefix)
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return recapPrefix + " " + text
}

// recapLines folds a footer to its display rows.
//
// The marker owns the first row and every continuation is indented under the
// narrative rather than the marker, so a long recap reads as one paragraph.
// There is no row cap and no ellipsis: the transcript scrolls, and a recap cut
// at a column boundary would read as unfinished rather than completed. At a
// width too narrow to seat the marker beside the narrative, the whole line
// wraps rather than losing content.
func recapLines(text string, width int) []string {
	line := recapLine(text)
	if line == "" {
		return nil
	}
	if width <= 0 {
		return []string{line}
	}
	if CellWidth(line) <= width {
		return []string{line}
	}
	body := strings.TrimPrefix(line, recapPrefix+" ")
	prefixWidth := CellWidth(recapPrefix) + 1
	bodyWidth := width - prefixWidth
	if bodyWidth < 8 {
		return WrapToWidth(line, width)
	}
	rows := WrapToWidth(body, bodyWidth)
	out := make([]string, 0, len(rows))
	indent := strings.Repeat(" ", prefixWidth)
	for i, row := range rows {
		if i == 0 {
			out = append(out, recapPrefix+" "+row)
			continue
		}
		out = append(out, indent+row)
	}
	return out
}
