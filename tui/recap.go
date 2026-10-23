package tui

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/PizenLabs/ohgo/agent"
)

// recapPrefix is the single marker every completion recap carries.
//
// It is a text marker, not decoration: the recap must read as closure even in
// a monochrome terminal, and a user scanning scrollback finds the end of a
// task by one stable prefix rather than by a colour.
const recapPrefix = "※ recap:"

// recapGreeting is the narrative for a conversational turn that asked for no
// work. It is deliberately static: a greeting produced no files, commands, or
// findings to summarise, and inventing evidence would make the recap lie.
const recapGreeting = "No task in progress yet; you greeted and returned. Ready to help — got a specific file or bug?"

// recapNarrative derives a semantic completion summary from the run's own
// evidence.
//
// The recap never echoes the request and never reports duration or token
// counts: those are telemetry, available elsewhere, while the transcript needs
// a human sentence about what actually happened. Classification is by observed
// work, not by model identity or by wording: a turn that changed files is
// described as a change, a turn that only answered is described as an answer,
// and a greeting stays a greeting.
func recapNarrative(objective string, turns []agent.Turn) string {
	if recapIsGreeting(objective) {
		return recapGreeting
	}
	evidence := collectRecapEvidence(turns)
	switch {
	case len(evidence.changed) > 0:
		return mutationRecap(evidence)
	case len(evidence.commands) > 0:
		return executionRecap(evidence)
	default:
		return answerRecap(turns, evidence)
	}
}

// recapChange is one file the run successfully wrote, edited, or removed.
type recapChange struct {
	path   string
	remove bool
}

// recapEvidence is the bounded set of facts the narrator may use.
//
// Only successful calls contribute: a failed write did not change a file, and
// a denied call ran nothing, so neither may appear as accomplished work.
type recapEvidence struct {
	changed  []recapChange
	read     []string
	commands []string
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
			case "read", "grep", "glob", "ls", "symbols", "search", "refs",
				"graph", "web_fetch", "browser", "mcp_read":
				if path := recapPathArg(outcome.Args); path != "" {
					evidence.read = appendUnique(evidence.read, path)
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
func mutationRecap(evidence recapEvidence) string {
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
	return finishSentence(recap)
}

// executionRecap narrates a turn that ran a process without changing files.
func executionRecap(evidence recapEvidence) string {
	recap := "Ran " + recapList(evidence.commands)
	return finishSentence(recap + " and reported the result")
}

// answerRecap narrates an informational turn from its final answer and the
// files it inspected.
//
// The subject comes from the assistant's own prose, never from the prompt, so
// the line describes what was explained rather than restating what was asked.
func answerRecap(turns []agent.Turn, evidence recapEvidence) string {
	answer := finalAssistantText(turns)
	gist := answerGist(answer)
	hasExample := strings.Contains(answer, "```")

	var recap string
	if len(evidence.read) > 0 {
		recap = "Read " + recapList(recapPaths(evidence.read, 2)) + " and explained"
	} else {
		recap = "Explained"
	}
	switch {
	case gist != "":
		recap += " the core idea: " + gist
	case hasExample:
		recap += " the key concepts and provided a worked example"
	default:
		recap += " the answer"
	}
	recap = finishSentence(recap)
	if hasExample && gist != "" {
		recap += " Included a worked example."
	}
	return recap
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

// answerGist condenses an answer to one display-sized clause.
//
// It skips headings, rules, tables, and fenced code, strips inline emphasis,
// and keeps the first prose sentence. The bound is a presentation budget, not
// a summary: the full answer remains directly above the recap.
func answerGist(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var prose strings.Builder
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			if prose.Len() > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "|") ||
			strings.HasPrefix(trimmed, "---") ||
			strings.HasPrefix(trimmed, "===") {
			if prose.Len() > 0 {
				break
			}
			continue
		}
		trimmed = stripListMarker(trimmed)
		if prose.Len() > 0 {
			prose.WriteString(" ")
		}
		prose.WriteString(trimmed)
		if endsSentence(trimmed) {
			break
		}
	}

	gist := stripInlineMarkdown(prose.String())
	gist = stripAnswerLeadIn(gist)
	gist = firstClause(gist)
	gist = strings.TrimSpace(strings.Trim(gist, "\"'“”‘’ "))
	gist = strings.TrimRight(gist, " .,!?;:")
	return truncateClause(gist, 88)
}

// stripListMarker removes a leading Markdown list or quote marker so a bullet
// answer reads as prose in the recap.
func stripListMarker(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "> ")
	for _, marker := range []string{"- ", "* ", "+ "} {
		line = strings.TrimPrefix(line, marker)
	}
	// Ordered markers ("1. ", "2) ") are one or two leading digits.
	runes := []rune(line)
	digits := 0
	for digits < len(runes) && digits < 2 && runes[digits] >= '0' && runes[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(runes) &&
		(runes[digits] == '.' || runes[digits] == ')') && runes[digits+1] == ' ' {
		line = string(runes[digits+2:])
	}
	return strings.TrimSpace(line)
}

// stripInlineMarkdown removes the emphasis syntax that would otherwise appear
// as literal punctuation inside the recap. Code spans and links lose their
// delimiters but keep their text.
func stripInlineMarkdown(text string) string {
	text = strings.NewReplacer(
		"`", "",
		"**", "",
		"__", "",
		"*", "",
		"_", "",
	).Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

// answerLeadIns are conversational openers that carry no content. Removing
// them is what makes the clause read as a recap instead of a quoted reply.
var answerLeadIns = []string{
	"sure,", "sure thing,", "of course,", "certainly,", "absolutely,",
	"great question,", "good question,", "yes,", "no,", "well,",
	"here's", "here is", "in short,", "short answer:", "to answer your question,",
}

// stripAnswerLeadIn removes one conversational opener, if present.
func stripAnswerLeadIn(text string) string {
	trimmed := strings.TrimSpace(text)
	lowered := strings.ToLower(trimmed)
	for _, lead := range answerLeadIns {
		if !strings.HasPrefix(lowered, lead) {
			continue
		}
		rest := strings.TrimSpace(trimmed[len(lead):])
		if rest == "" {
			continue
		}
		return rest
	}
	return trimmed
}

// firstClause returns the first sentence of text, without its terminator.
func firstClause(text string) string {
	runes := []rune(text)
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i+1 == len(runes) || unicode.IsSpace(runes[i+1]) {
			return string(runes[:i])
		}
	}
	return text
}

// endsSentence reports whether a line already closes a sentence.
func endsSentence(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	last := []rune(trimmed)[len([]rune(trimmed))-1]
	return last == '.' || last == '!' || last == '?'
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
	if last == '.' || last == '!' || last == '?' {
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

// taskRecapText returns the completion footer text for a task, or empty when
// the task has not actually completed.
//
// The status check is deliberate: the recap is metadata, not a lifecycle
// authority, so a projection carrying one while still RUNNING must never emit a
// footer. Completion is decided by the task transition, never by the presence
// of a string. The fallback closes an otherwise silent completed task without
// echoing the objective.
func taskRecapText(projection *TaskProjection) string {
	if projection == nil || !strings.EqualFold(strings.TrimSpace(projection.Status), "COMPLETED") {
		return ""
	}
	if recap := strings.TrimSpace(sanitizeInline(projection.Recap)); recap != "" {
		return recap
	}
	return "Closed the completed task and returned to the prompt."
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

// recapLines folds a footer to its allowed visual rows.
//
// A recap is one logical line, but a narrow terminal needs two rows to keep
// the sentence readable instead of cutting it at the column edge. The bound is
// hard: overflow is ellipsized on the second row, so the footer can never push
// the prompt by more than two rows. Continuation rows are indented under the
// narrative rather than the marker, so they read as the same sentence.
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
		// Too narrow to hold a readable narrative: one honest truncated row
		// beats a two-row sliver.
		return []string{TruncateToWidth(line, width)}
	}
	rows := WrapToWidth(body, bodyWidth)
	if len(rows) > 2 {
		overflow := strings.Join(rows[2:], " ")
		rows = rows[:2]
		rows[1] = TruncateToWidth(rows[1]+" "+overflow, bodyWidth)
	}
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
