package tui

// The /copy command.
//
// The default is content-aware because most copy requests are for the thing
// the answer just showed: a fenced code block is copied without its fences, a
// prose answer is copied whole, and a unified diff is copied without the
// explanation around it. The flags exist for the cases the heuristic cannot
// know: -a keeps the whole answer (explanation and every block), -c copies the
// conversation context the model is working from so it can move to another
// tool.
//
// Extraction is pure: it reads assistant text and agent messages and never
// touches the terminal, the loop, or session state. The transport stays in
// queueClipboard/clipboard.go, so a failed copy never looks like a success.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	tea "github.com/charmbracelet/bubbletea"
)

// CopyMode selects what /copy extracts.
type CopyMode int

const (
	// CopyModeSmart is the default: the most valuable content of the latest
	// answer — its largest code block, else a unified diff, else the text.
	CopyModeSmart CopyMode = iota
	// CopyModeAll copies the latest answer verbatim, fences and explanation
	// included.
	CopyModeAll
	// CopyModeContext copies the active conversation context.
	CopyModeContext
)

// CopyKind names the content a copy selected, so both interfaces can report
// what happened without inventing a second classification.
type CopyKind string

const (
	// CopyKindCode is a fenced code block's body.
	CopyKindCode CopyKind = "code block"
	// CopyKindDiff is a unified diff without its surrounding prose.
	CopyKindDiff CopyKind = "unified diff"
	// CopyKindResponse is the whole latest answer.
	CopyKindResponse CopyKind = "response"
	// CopyKindContext is the conversation context.
	CopyKindContext CopyKind = "context"
)

// ParseCopyMode resolves the /copy flags. No flags means the smart default;
// an unknown, duplicated, or conflicting flag is refused rather than guessed,
// because a copy of the wrong thing is silently useless.
func ParseCopyMode(args []string) (CopyMode, error) {
	mode := CopyModeSmart
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "":
			continue
		case "-a", "--all", "-all":
			if mode == CopyModeContext {
				return CopyModeSmart, fmt.Errorf("/copy cannot combine -a/--all with -c/--context")
			}
			mode = CopyModeAll
		case "-c", "--context", "-context":
			if mode == CopyModeAll {
				return CopyModeSmart, fmt.Errorf("/copy cannot combine -a/--all with -c/--context")
			}
			mode = CopyModeContext
		default:
			return CopyModeSmart, fmt.Errorf("unknown /copy option %q — use -a/--all or -c/--context", arg)
		}
	}
	return mode, nil
}

// CopyPayload resolves the smart or all payload for one response.
//
// editDiff is the canonical diff the latest turn's mutation tools reported, if
// any. It is a fallback, not a replacement: a code block or a diff the model
// actually wrote is still preferred, because that is the text the user just
// read. The context mode needs agent messages rather than text, so it is
// handled by ContextTranscript instead of this function.
func CopyPayload(mode CopyMode, response string, editDiff ...string) (string, CopyKind, bool) {
	diffFallback := ""
	if len(editDiff) > 0 {
		diffFallback = strings.TrimSpace(editDiff[0])
	}
	switch mode {
	case CopyModeAll:
		if strings.TrimSpace(response) == "" {
			return "", "", false
		}
		return response, CopyKindResponse, true
	case CopyModeSmart:
		if block, ok := largestCodeBlock(response); ok {
			return block, CopyKindCode, true
		}
		if diff, ok := unifiedDiff(response); ok {
			return diff, CopyKindDiff, true
		}
		if diffFallback != "" {
			return diffFallback, CopyKindDiff, true
		}
		if strings.TrimSpace(response) == "" {
			return "", "", false
		}
		return response, CopyKindResponse, true
	default:
		return "", "", false
	}
}

// MutationMetadata extracts the path, operation, and canonical diff a tool
// reported in its structured details. The three facts travel together so the
// expanded diff header never invents a path or guesses an operation.
func MutationMetadata(details any) (path, operation, diff string) {
	switch d := details.(type) {
	case map[string]any:
		path, _ = d["path"].(string)
		for _, key := range []string{"operation", "op", "type", "mutation_type"} {
			if value, ok := d[key].(string); ok && strings.TrimSpace(value) != "" {
				operation = value
				break
			}
		}
		diff = MutationDiff(d)
	case agent.MutationPreview:
		return d.Path, string(d.Operation), strings.TrimSpace(d.Diff)
	case *agent.MutationPreview:
		if d != nil {
			return d.Path, string(d.Operation), strings.TrimSpace(d.Diff)
		}
	case agent.MutationEvidence:
		return d.Path, string(d.Operation), strings.TrimSpace(d.Diff)
	case *agent.MutationEvidence:
		if d != nil {
			return d.Path, string(d.Operation), strings.TrimSpace(d.Diff)
		}
	}
	return strings.TrimSpace(path), strings.TrimSpace(operation), strings.TrimSpace(diff)
}

// MutationDiff extracts the canonical unified diff a tool reported in its
// structured details. Details is an arbitrary tool-owned payload, so this is
// the one boundary where the shape is discovered rather than assumed.
func MutationDiff(details any) string {
	switch d := details.(type) {
	case map[string]any:
		for _, key := range []string{"diff", "delta", "content_delta"} {
			if diff, ok := d[key].(string); ok && strings.TrimSpace(diff) != "" {
				return diff
			}
		}
	case agent.MutationPreview:
		return strings.TrimSpace(d.Diff)
	case *agent.MutationPreview:
		if d != nil {
			return strings.TrimSpace(d.Diff)
		}
	case agent.MutationEvidence:
		return strings.TrimSpace(d.Diff)
	case *agent.MutationEvidence:
		if d != nil {
			return strings.TrimSpace(d.Diff)
		}
	}
	return ""
}

// copyCommand implements /copy in the full-screen UI.
//
// A successful copy uses the existing transient header confirmation; it never
// enters the transcript, so the answer stays the answer. A missing transport or
// an empty selection is reported explicitly because a silent /copy looks like a
// successful one.
func (p *Program) copyCommand(args []string) tea.Cmd {
	mode, err := ParseCopyMode(args)
	if err != nil {
		p.state.Apply(Notice{Text: err.Error()})
		return nil
	}

	var payload string
	var kind CopyKind
	if mode == CopyModeContext {
		p.mu.Lock()
		history := append([]agent.Message(nil), p.history...)
		p.mu.Unlock()
		payload, kind = ContextTranscript(history), CopyKindContext
		if payload == "" {
			p.state.Apply(Notice{Text: "nothing to copy: the session context is empty"})
			return nil
		}
	} else {
		response, editDiff, ok := latestResponseEntries(p.state.Entries)
		if !ok {
			p.state.Apply(Notice{Text: "nothing to copy: no response yet"})
			return nil
		}
		payload, kind, ok = CopyPayload(mode, response, editDiff)
		if !ok {
			p.state.Apply(Notice{Text: "nothing to copy: the response is empty"})
			return nil
		}
	}

	if p.clipboard == nil {
		p.state.Apply(Notice{Text: fmt.Sprintf("clipboard is unavailable: cannot copy the %s", kind)})
		return nil
	}
	cmd := p.queueClipboard(payload)
	if cmd == nil {
		p.state.Apply(Notice{Text: fmt.Sprintf("copy: the terminal did not accept the %s", kind)})
		return nil
	}
	return cmd
}

// LatestResponse returns the assistant text produced since the last user turn.
//
// A turn can contain several assistant messages when the model narrates a tool
// call and then answers. Joining everything after the last user message keeps
// the explanation that belongs to the answer; taking only the final assistant
// message would drop it. A transcript with no user turn (a synthetic or
// resumed fragment) falls back to the last assistant message alone, so older
// answers never leak into "the latest".
func LatestResponse(messages []agent.Message) (string, bool) {
	start := 0
	foundUser := false
	for i := len(messages) - 1; i >= 0; i-- {
		if _, ok := messages[i].(agent.UserMessage); ok {
			start = i + 1
			foundUser = true
			break
		}
	}
	if !foundUser {
		for i := len(messages) - 1; i >= 0; i-- {
			assistant, ok := messages[i].(agent.AssistantMessage)
			if ok && strings.TrimSpace(assistant.Message.Text()) != "" {
				return assistant.Message.Text(), true
			}
		}
		return "", false
	}
	var parts []string
	for _, message := range messages[start:] {
		assistant, ok := message.(agent.AssistantMessage)
		if !ok || strings.TrimSpace(assistant.Message.Text()) == "" {
			continue
		}
		parts = append(parts, assistant.Message.Text())
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n\n"), true
}

// latestResponseEntries is LatestResponse over the live transcript, plus the
// canonical diffs the same turn's mutation tools reported.
//
// Entries are used instead of history while a turn streams: history is rebased
// only after the turn settles, so the transcript is the only current copy of
// the answer.
func latestResponseEntries(entries []Entry) (string, string, bool) {
	start := 0
	foundUser := false
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == EntryUser {
			start = i + 1
			foundUser = true
			break
		}
	}
	if !foundUser {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Kind == EntryAssistant && strings.TrimSpace(entries[i].AssistantText) != "" {
				return entries[i].AssistantText, "", true
			}
		}
		return "", "", false
	}
	var parts []string
	for i := start; i < len(entries); i++ {
		entry := entries[i]
		if entry.Kind == EntryAssistant && strings.TrimSpace(entry.AssistantText) != "" {
			parts = append(parts, entry.AssistantText)
		}
	}
	diff := latestTurnDiff(entries, start)
	if len(parts) == 0 {
		if diff != "" {
			return "", diff, true
		}
		return "", "", false
	}
	return strings.Join(parts, "\n\n"), diff, true
}

// latestTurnDiff joins the canonical mutation diffs from one turn in call
// order. A duplicate is dropped so a retried identical edit does not paste the
// same hunk twice.
func latestTurnDiff(entries []Entry, start int) string {
	var diffs []string
	seen := map[string]bool{}
	for i := start; i < len(entries); i++ {
		if entries[i].Kind != EntryTool || entries[i].Tool == nil {
			continue
		}
		diff := strings.TrimSpace(entries[i].Tool.Diff)
		if diff == "" || seen[diff] {
			continue
		}
		seen[diff] = true
		diffs = append(diffs, diff)
	}
	return strings.Join(diffs, "\n")
}

// ContextTranscript renders the conversation the model is working from as
// portable text.
//
// Role headings, tool calls, and tool results are kept because they are the
// context: dropping them would hand another tool an answer without the evidence
// or the request it belongs to. Message order is preserved, and a message that
// carries nothing copyable is skipped rather than rendered as an empty
// heading.
func ContextTranscript(messages []agent.Message) string {
	var b strings.Builder
	wrote := false
	for _, message := range messages {
		switch m := message.(type) {
		case agent.UserMessage:
			text := strings.TrimSpace(m.Text)
			if text == "" && len(m.Images) == 0 {
				continue
			}
			writeContextSection(&b, "User", text)
			for _, image := range m.Images {
				fmt.Fprintf(&b, "[image: %s]\n\n", image.MediaType)
			}
			wrote = true
		case agent.AssistantMessage:
			text := strings.TrimSpace(m.Message.Text())
			calls := m.Message.ToolCalls()
			if text == "" && len(calls) == 0 {
				continue
			}
			writeContextSection(&b, "Assistant", text)
			for _, call := range calls {
				writeToolCall(&b, call)
			}
			wrote = true
		case agent.ToolResultMessage:
			text := strings.TrimSpace(m.Text())
			if text == "" && !m.IsError {
				continue
			}
			fmt.Fprintf(&b, "## Tool result: %s\n\n", m.ToolName)
			if m.IsError {
				b.WriteString("(error)\n\n")
			}
			if text != "" {
				b.WriteString(text)
				b.WriteString("\n\n")
			}
			wrote = true
		}
	}
	if !wrote {
		return ""
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeContextSection writes one role heading and its text.
func writeContextSection(b *strings.Builder, role, text string) {
	fmt.Fprintf(b, "## %s\n\n", role)
	if text != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
}

// writeToolCall writes one model-requested invocation with stable JSON keys.
func writeToolCall(b *strings.Builder, call ai.ToolCall) {
	fmt.Fprintf(b, "### Tool call: %s\n\n", call.Name)
	if len(call.Arguments) == 0 {
		return
	}
	encoded, err := json.MarshalIndent(call.Arguments, "", "  ")
	if err != nil {
		fmt.Fprintf(b, "%v\n\n", call.Arguments)
		return
	}
	b.WriteString("```json\n")
	b.Write(encoded)
	b.WriteString("\n```\n\n")
}

// largestCodeBlock returns the largest fenced body, preferring the later block
// on a tie. Fence markers and the info string never enter the payload: the
// code is what the user wants to paste, not the Markdown around it.
func largestCodeBlock(text string) (string, bool) {
	blocks := fencedCodeBlocks(text)
	if len(blocks) == 0 {
		return "", false
	}
	best := 0
	bestLen := len([]rune(blocks[0]))
	for i := 1; i < len(blocks); i++ {
		if size := len([]rune(blocks[i])); size >= bestLen {
			best, bestLen = i, size
		}
	}
	return blocks[best], true
}

// fencedCodeBlocks returns every fenced body in order.
//
// The fence grammar matches the transcript renderer: a line whose trimmed form
// starts with three backticks opens or closes a block, and an unclosed block
// runs to the end of the text. An empty block contributes nothing.
func fencedCodeBlocks(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		if !isFenceLine(lines[i]) {
			continue
		}
		start := i + 1
		end := start
		for ; end < len(lines); end++ {
			if isFenceLine(lines[end]) {
				break
			}
		}
		if end > start {
			blocks = append(blocks, strings.Join(lines[start:end], "\n"))
		}
		i = end
	}
	return blocks
}

// isFenceLine reports whether a line is a code-fence delimiter.
func isFenceLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// unifiedDiff extracts a unified diff from an answer that did not fence it.
//
// The extraction starts at the first hunk header and walks back over the file
// headers that introduce it. It stops at the first line that cannot be diff
// content, so a sentence after the diff is not copied with it.
func unifiedDiff(text string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if !isHunkHeader(line) {
			continue
		}
		start = i
		for start > 0 && isDiffHeader(lines[start-1]) {
			start--
		}
		break
	}
	if start < 0 {
		return "", false
	}
	end := start
	for end < len(lines) && isDiffLine(lines[end]) {
		end++
	}
	diff := strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n")
	if strings.TrimSpace(diff) == "" {
		return "", false
	}
	return diff, true
}

// isHunkHeader reports whether a line is a unified-diff hunk header.
func isHunkHeader(line string) bool {
	return strings.HasPrefix(line, "@@ ")
}

// isDiffHeader reports whether a line belongs to the file header above a hunk.
func isDiffHeader(line string) bool {
	for _, prefix := range []string{
		"diff ", "index ", "--- ", "+++ ", "new file mode ", "deleted file mode ",
		"similarity index ", "rename from ", "rename to ", "old mode ", "new mode ",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// isDiffLine reports whether a line can belong to a unified diff body. An empty
// line ends the region: a real empty context line is written with a leading
// space, so an empty string is the prose separator.
func isDiffLine(line string) bool {
	if line == "" {
		return false
	}
	if isHunkHeader(line) || isDiffHeader(line) {
		return true
	}
	switch line[0] {
	case '+', '-', ' ', '\\':
		return true
	default:
		return false
	}
}
