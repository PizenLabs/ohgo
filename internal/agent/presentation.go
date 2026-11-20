package agent

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Runtime-state isolation at the output boundary.
//
// The runtime state block is a request-side briefing, not project evidence. A
// model can nevertheless copy its headings or directives into the final answer,
// where they read as findings about the repository. Hiding the field in the UI
// does not protect the user: the answer itself is the publication.
//
// The sanitizer removes only unequivocal runtime-authored text: the state-block
// header, its field lines, and the fixed "no tool is available" directives. It
// never strips a technical limitation the model wrote in its own words, and it
// leaves fenced code untouched. If an answer consisted only of copied state,
// the result is empty and the existing empty-answer completion rule reports a
// review rather than publishing orchestration text as a project explanation.

// runtimeStateHeader is the exact heading RenderState emits. A copied block is
// removed from that heading to the end of the answer: the block is the request's
// volatile tail, so anything after a verbatim header is the same projection.
const runtimeStateHeader = "# Runtime state (authoritative, not a user request)"

var standaloneRuntimeDirective = regexp.MustCompile(
	`(?i)^(?:no further tools? (?:is|are) available\.?|` +
		`answer without (?:using )?(?:further |any )?tools?\.?|` +
		`do not call (?:any )?(?:more|further) tools?\.?|` +
		`the runtime (?:has )?closed the action surface\.?)$`)

// runtimeDirectiveLine reports an unequivocal runtime field. These lines are
// the trigger for treating a group as a copied projection; a legitimate answer
// may use headings such as "Verification:" or "Completion:", so those labels
// are stripped only after a real directive line establishes the block.
func runtimeDirectiveLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{
		"sufficiency:", "available actions:", "next action:", "withheld:",
		"named entities (preserve them):", "available but cannot close the gap:",
		"task shape:", "workspace probe:", "mutation: forbidden",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// runtimeFieldLine reports an internal field line that is not project prose.
// The list is restricted to labels that the state block itself emits; generic
// words such as "Evidence:" or "Why:" are deliberately absent because a user
// answer may legitimately use them.
func runtimeFieldLine(line string) bool {
	if runtimeDirectiveLine(line) {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{
		"mutation scope:", "completion:", "verification:",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// SanitizeAnswerText removes copied runtime projections from a final answer.
// It is deterministic and conservative; the caller decides what an empty result
// means.
func SanitizeAnswerText(answer string) string {
	if strings.TrimSpace(answer) == "" {
		return answer
	}
	lines := strings.Split(answer, "\n")
	// A single "Verification:" or "Completion:" line can be legitimate model
	// prose. Treat the field vocabulary as a copied projection only when the
	// header is present, a fixed directive appears, or at least two field lines
	// occur together. This keeps the boundary from mangling an honest answer.
	directive, header := false, false
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(trimmed, runtimeStateHeader) {
			header = true
			break
		}
		if runtimeDirectiveLine(line) || standaloneRuntimeDirective.MatchString(trimmed) {
			directive = true
		}
	}
	if !header && !directive {
		return answer
	}
	out := make([]string, 0, len(lines))
	inFence = false
	dropRest := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			if !dropRest {
				out = append(out, line)
			}
			continue
		}
		if inFence {
			if !dropRest {
				out = append(out, line)
			}
			continue
		}
		if strings.HasPrefix(trimmed, runtimeStateHeader) {
			// A verbatim header begins the copied projection. The block is the
			// request's last section, so the rest of the answer is projection
			// as well. Keeping the lines after it would publish the very
			// orchestration state this boundary exists to contain.
			dropRest = true
			continue
		}
		if dropRest {
			continue
		}
		if runtimeFieldLine(line) || standaloneRuntimeDirective.MatchString(trimmed) {
			continue
		}
		out = append(out, line)
	}
	cleaned := strings.Join(out, "\n")
	// Collapse the blank runs left by removed lines without touching paragraph
	// structure the model authored.
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
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
	message.Content = content
	return message
}
