package agent

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Runtime-state isolation at the output boundary.
//
// The runtime state block is a request-side briefing, not project evidence. A
// model can nevertheless copy its headings or directives into the final answer,
// where they read as findings about the repository. Hiding the field in the UI
// does not protect the user: the answer itself is the publication.
//
// The boundary therefore has one implementation and two entry points: the live
// delta stream and the completed answer. Both run through answerStreamSanitizer,
// so a line withheld from the live stream is also withheld from the stored
// answer, and a line published live is a prefix of what the completed answer
// publishes. The sanitizer removes only unequivocal runtime-authored text: the
// state-block header, its internal field lines, and the fixed "no tool is
// available" directives. It never strips a technical limitation the model wrote
// in its own words, and it leaves fenced code untouched. If an answer consisted
// only of copied state, the result is empty and the existing empty-answer
// completion rule reports a review rather than publishing orchestration text as
// a project explanation.

// runtimeStateHeaderLead is the stable prefix of the heading RenderState emits.
// A line that begins with it is a copied state block even when truncation or a
// model edit removed the parenthetical suffix.
const runtimeStateHeaderLead = "# Runtime state"

var runtimeStateHeaderLower = strings.ToLower(runtimeStateHeaderLead)

// standaloneRuntimeDirective matches a complete line that is a runtime
// instruction rather than project prose. The line is matched after trimming, so
// indentation does not hide it.
var standaloneRuntimeDirective = regexp.MustCompile(
	`(?i)^(?:no further tools? (?:is|are) available\.?|` +
		`answer without (?:using )?(?:further |any )?tools?\.?|` +
		`do not call (?:any )?(?:more|further) tools?\.?|` +
		`the runtime (?:has )?closed the action surface\.?)$`)

// runtimeFieldPrefixes are the internal field labels RenderState emits. They
// are not project vocabulary: a line that starts with one of them is the
// runtime's own state, even without the heading. Ambiguous English headings
// such as "Verification:" and "Completion:" are deliberately absent; a model
// may legitimately write those, and the header still removes a copied block
// wholesale.
var runtimeFieldPrefixes = []string{
	"objective:",
	"workspace probe:",
	"claim evidence:",
	"claim rule:",
	"evidence frontier:",
	"task shape:",
	"named entities (preserve them):",
	"requested action:",
	"ambiguity (do not collapse into an invented objective):",
	"mutation scope:",
	"unrelated mutation:",
	"mutation: forbidden",
	"sufficiency:",
	"available actions:",
	"next action:",
	"withheld:",
	"available but cannot close the gap:",
	"next evidence:",
	"invocation corrections (",
	"failed capabilities (",
	"evidence missing:",
	"conflicting evidence:",
	"external evidence (untrusted data",
	"evidence known:",
}

// runtimeDirectiveStarts are the fixed sentence openings of a runtime
// instruction. A line that begins with one is held until its newline so the
// complete line can be matched exactly; a normal sentence that merely starts
// with the same words is published unchanged.
var runtimeDirectiveStarts = []string{
	"no further tool",
	"answer without",
	"do not call",
	"the runtime closed",
}

// answerStreamSanitizer is the single online implementation of the output
// boundary. It is safe across arbitrary chunking: a trigger split over several
// deltas is held until it is recognized or ruled out.
type answerStreamSanitizer struct {
	pending      strings.Builder
	started      bool
	lineStart    bool
	droppingRest bool
	droppingLine bool
	changed      bool
	inFence      bool
	fenceOpened  bool
	fenceLine    strings.Builder
	heldLines    int
}

func newAnswerStreamSanitizer() *answerStreamSanitizer {
	return &answerStreamSanitizer{lineStart: true}
}

// Write consumes one delta and returns the public text it establishes.
func (s *answerStreamSanitizer) Write(delta string) string {
	if delta == "" {
		return ""
	}
	var out strings.Builder
	for _, r := range delta {
		s.step(&out, r)
	}
	return out.String()
}

// Flush returns any held text that is safe to publish at end of stream.
func (s *answerStreamSanitizer) Flush() string {
	if s.droppingRest || s.droppingLine {
		s.pending.Reset()
		return ""
	}
	if s.pending.Len() == 0 {
		return ""
	}
	line := s.pending.String()
	s.pending.Reset()
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if standaloneRuntimeDirective.MatchString(trimmed) {
		s.changed = true
		return ""
	}
	if strings.HasPrefix(lower, runtimeStateHeaderLower) {
		s.changed = true
		return ""
	}
	if hasRuntimeFieldPrefix(lower) {
		s.changed = true
		return ""
	}
	// A nearly complete header at EOF is a leak even when the model stopped
	// mid-line. A shorter prefix such as "# Run" is ordinary prose and is
	// published normally.
	if strings.HasPrefix(runtimeStateHeaderLower, lower) &&
		len(lower) >= len("# runtime") {
		s.changed = true
		return ""
	}
	var out strings.Builder
	s.emit(&out, line)
	return s.flushHeld(&out)
}

func (s *answerStreamSanitizer) step(out *strings.Builder, r rune) {
	if !s.started {
		if unicode.IsSpace(r) {
			return
		}
		s.started = true
	}
	if s.droppingRest {
		return
	}
	if s.droppingLine {
		if r == '\n' {
			// The dropped line's newline is not emitted. The previous kept
			// line already ended with one, so joining the surrounding lines
			// matches the line-removal projection exactly.
			s.droppingLine = false
			s.lineStart = true
		}
		return
	}
	if s.inFence {
		s.emitRune(out, r)
		if r == '\n' {
			if !s.fenceOpened && isFenceDelimiter(s.fenceLine.String()) {
				s.inFence = false
			}
			s.fenceOpened = false
			s.fenceLine.Reset()
			s.lineStart = true
			return
		}
		if s.fenceLine.Len() < 64 {
			s.fenceLine.WriteRune(r)
		}
		return
	}
	if s.lineStart {
		s.stepLineStart(out, r)
		return
	}
	s.emitRune(out, r)
	if r == '\n' {
		s.lineStart = true
	}
}

// stepLineStart holds only the bytes that could still become a runtime line.
// Once a line diverges from every trigger, the held prefix and the rest of the
// line stream normally.
func (s *answerStreamSanitizer) stepLineStart(out *strings.Builder, r rune) {
	if r == '\n' {
		line := s.pending.String()
		s.pending.Reset()
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case standaloneRuntimeDirective.MatchString(trimmed):
			return
		case strings.HasPrefix(lower, runtimeStateHeaderLead):
			s.droppingRest = true
			return
		case hasRuntimeFieldPrefix(lower):
			return
		}
		s.emit(out, line)
		s.emitRune(out, '\n')
		return
	}
	s.pending.WriteRune(r)
	pending := strings.TrimLeftFunc(s.pending.String(), unicode.IsSpace)
	if pending == "" {
		return
	}
	lower := strings.ToLower(pending)
	// A fenced block is data the user asked to see. Entering it is recognized
	// at line start; inside it the sanitizer is transparent.
	if strings.HasPrefix(lower, "```") {
		s.emit(out, s.pending.String())
		s.pending.Reset()
		s.inFence = true
		s.fenceOpened = true
		s.fenceLine.Reset()
		s.lineStart = false
		return
	}
	if strings.HasPrefix("```", lower) {
		return
	}
	if hasRuntimeFieldPrefix(lower) {
		s.droppingLine = true
		s.changed = true
		s.pending.Reset()
		return
	}
	if couldStartRuntimeField(lower) {
		return
	}
	if strings.HasPrefix(lower, runtimeStateHeaderLower) {
		s.droppingRest = true
		s.changed = true
		s.pending.Reset()
		return
	}
	if strings.HasPrefix(runtimeStateHeaderLower, lower) {
		return
	}
	if couldStartRuntimeDirective(lower) {
		return
	}
	s.emit(out, s.pending.String())
	s.pending.Reset()
	s.lineStart = false
}

func hasRuntimeFieldPrefix(lower string) bool {
	for _, prefix := range runtimeFieldPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func couldStartRuntimeField(lower string) bool {
	for _, prefix := range runtimeFieldPrefixes {
		if strings.HasPrefix(prefix, lower) {
			return true
		}
	}
	return false
}

func couldStartRuntimeDirective(lower string) bool {
	for _, prefix := range runtimeDirectiveStarts {
		if strings.HasPrefix(lower, prefix) || strings.HasPrefix(prefix, lower) {
			return true
		}
	}
	return false
}

func isFenceDelimiter(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return false
	}
	return strings.Trim(trimmed[3:], "` \t") == ""
}

func (s *answerStreamSanitizer) emit(out *strings.Builder, text string) {
	for _, r := range text {
		s.emitRune(out, r)
	}
}

// emitRune writes one rune and collapses a run of blank lines, matching the
// completed-answer projection so the live and final text cannot diverge on
// spacing alone.
func (s *answerStreamSanitizer) emitRune(out *strings.Builder, r rune) {
	if r == '\n' {
		if s.heldLines < 2 {
			s.heldLines++
		}
		return
	}
	s.flushHeld(out)
	out.WriteRune(r)
}

// flushHeld writes buffered newlines before the next visible rune. Holding
// them until then keeps trailing blank lines out of a stream that ends at a
// runtime projection, so the live text and the trimmed final answer agree.
func (s *answerStreamSanitizer) flushHeld(out *strings.Builder) string {
	for ; s.heldLines > 0; s.heldLines-- {
		out.WriteByte('\n')
	}
	return out.String()
}

// SanitizeAnswerText removes copied runtime projections from a final answer.
// It is deterministic and conservative; the caller decides what an empty result
// means.
func SanitizeAnswerText(answer string) string {
	if strings.TrimSpace(answer) == "" {
		return answer
	}
	s := newAnswerStreamSanitizer()
	cleaned := s.Write(answer) + s.Flush()
	if !s.changed {
		return answer
	}
	return strings.TrimSpace(cleaned)
}

// SanitizeAssistantMessage returns the message with runtime projections removed
// from its text blocks. Reasoning and tool-call blocks are untouched: they are
// not the public answer.
func SanitizeAssistantMessage(message ai.AssistantMessage) ai.AssistantMessage {
	if len(message.Content) == 0 {
		return message
	}
	content := make([]ai.ContentBlock, 0, len(message.Content))
	for _, block := range message.Content {
		text, ok := block.(ai.TextContent)
		if !ok {
			content = append(content, block)
			continue
		}
		cleaned := SanitizeAnswerText(text.Text)
		if cleaned == "" {
			// Drop an emptied text block rather than emitting an empty
			// assistant message block. A tool call in the same message stays.
			continue
		}
		text.Text = cleaned
		content = append(content, text)
	}
	content = trimOuterTextBlocks(content)
	message.Content = content
	return message
}

// trimOuterTextBlocks removes the answer's leading and trailing whitespace
// without trimming every text block independently. Text blocks are joined with
// a newline by AssistantMessage.Text, so trimming an internal block would delete
// the separation the model wrote between a recap and the next sentence.
func trimOuterTextBlocks(content []ai.ContentBlock) []ai.ContentBlock {
	for {
		first, last := -1, -1
		for index, block := range content {
			if _, ok := block.(ai.TextContent); ok {
				if first < 0 {
					first = index
				}
				last = index
			}
		}
		if first < 0 {
			return content
		}
		firstText := content[first].(ai.TextContent)
		if trimmed := strings.TrimLeftFunc(firstText.Text, unicode.IsSpace); trimmed == "" {
			content = append(content[:first], content[first+1:]...)
			continue
		} else {
			firstText.Text = trimmed
			content[first] = firstText
		}
		if last == first {
			last = first
		}
		lastText := content[last].(ai.TextContent)
		if trimmed := strings.TrimRight(lastText.Text, "\r\n"); trimmed == "" {
			content = append(content[:last], content[last+1:]...)
			continue
		} else {
			lastText.Text = trimmed
			content[last] = lastText
		}
		return content
	}
}
