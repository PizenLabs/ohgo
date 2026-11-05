package tui

import (
	"fmt"
	"strconv"
	"strings"

	diffview "github.com/PizenLabs/ohgo/internal/tui/diff"
	"github.com/charmbracelet/lipgloss"
)

// Expanded code diff rendering.
//
// A mutation tool already carries the canonical unified diff in ToolEntry.Diff.
// The collapsed block stays compact; expanding it replaces the raw result text
// with this projection. The parser is the existing diff inspector's, so line
// numbers, sanitization, and hunk boundaries are shared rather than
// re-implemented in the transcript.

// codeDiffKind is the visual role of one rendered diff row.
type codeDiffKind int

const (
	codeDiffContext codeDiffKind = iota
	codeDiffAdd
	codeDiffDelete
	codeDiffHunk
	codeDiffMeta
)

// codeDiffLine is one rendered row of an expanded diff.
type codeDiffLine struct {
	kind   codeDiffKind
	number int
	text   string
}

// codeDiffView is the parsed projection of one mutation diff.
type codeDiffView struct {
	path  string
	lines []codeDiffLine
	add   int
	del   int
}

// parseCodeDiff reads a unified diff (with or without file headers) into the
// rows the transcript can render. Raw +/- hunks are accepted as a fallback so
// an approval preview that begins directly with changed rows still gets the
// standard semantics.
func parseCodeDiff(text string) codeDiffView {
	text = sanitizeBlock(text)
	view := codeDiffView{path: diffPathFromHeader(text)}
	if hunks, err := diffview.ParseUnifiedString(text); err == nil && len(hunks) > 0 {
		for _, hunk := range hunks {
			view.lines = append(view.lines, codeDiffLine{kind: codeDiffHunk, text: hunk.Header})
			for _, line := range hunk.Lines {
				switch line.Type {
				case diffview.LineAddition:
					view.add++
					view.lines = append(view.lines, codeDiffLine{kind: codeDiffAdd, number: line.NewLineNum, text: line.Content})
				case diffview.LineDeletion:
					view.del++
					view.lines = append(view.lines, codeDiffLine{kind: codeDiffDelete, number: line.OldLineNum, text: line.Content})
				case diffview.LineMeta:
					view.lines = append(view.lines, codeDiffLine{kind: codeDiffMeta, text: line.Content})
				default:
					view.lines = append(view.lines, codeDiffLine{kind: codeDiffContext, number: line.NewLineNum, text: line.Content})
				}
			}
		}
		// A preview may carry changed rows before its first hunk header (the
		// approval fixture does). The formal parser ignores everything before
		// @@, so fall through to the raw reader when no changed rows survived.
		if view.add != 0 || view.del != 0 || !hasRawDiffChanges(text) {
			return view
		}
		view = codeDiffView{path: view.path}
	}

	// Fallback for a preview that is a diff body without a parsable hunk
	// header, or for a short raw diff supplied directly by a caller.
	for _, raw := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line := sanitizeInline(raw)
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "diff --git"), strings.HasPrefix(trimmed, "index "),
			strings.HasPrefix(trimmed, "+++"), strings.HasPrefix(trimmed, "---"):
			continue
		case strings.HasPrefix(trimmed, "@@"):
			view.lines = append(view.lines, codeDiffLine{kind: codeDiffHunk, text: trimmed})
		case strings.HasPrefix(line, "+"):
			view.add++
			view.lines = append(view.lines, codeDiffLine{kind: codeDiffAdd, text: strings.TrimPrefix(line, "+")})
		case strings.HasPrefix(line, "-"):
			view.del++
			view.lines = append(view.lines, codeDiffLine{kind: codeDiffDelete, text: strings.TrimPrefix(line, "-")})
		case strings.HasPrefix(line, "\\"):
			view.lines = append(view.lines, codeDiffLine{kind: codeDiffMeta, text: trimmed})
		default:
			view.lines = append(view.lines, codeDiffLine{kind: codeDiffContext, text: strings.TrimPrefix(line, " ")})
		}
	}
	return view
}

// hasRawDiffChanges reports whether a text carries +/- body rows outside the
// formal file headers. It lets the fallback reader recover a preview whose
// hunk header is out of order while keeping the parser authoritative for a
// well-formed unified diff.
func hasRawDiffChanges(text string) bool {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			return true
		}
	}
	return false
}

// diffPathFromHeader extracts the target path from a unified diff header when
// one is present. The tool's structured path wins at the call site; this is a
// fallback for raw previews and copies.
func diffPathFromHeader(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if path == "/dev/null" {
				continue
			}
			path = strings.TrimPrefix(path, "b/")
			path = strings.TrimPrefix(path, "a/")
			if tab := strings.IndexByte(path, '\t'); tab >= 0 {
				path = path[:tab]
			}
			return path
		}
	}
	return ""
}

// languageForPath maps a path to the highlighter's language vocabulary. The
// mapping is deliberately path-based: an extension is data, not a model claim.
func languageForPath(path string) string {
	base := strings.ToLower(strings.TrimSpace(path))
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	switch base {
	case "dockerfile":
		return "dockerfile"
	case "makefile", "gnumakefile":
		return "makefile"
	}
	ext := ""
	if i := strings.LastIndexByte(base, '.'); i >= 0 && i+1 < len(base) {
		ext = base[i+1:]
	}
	switch ext {
	case "go":
		return "go"
	case "md", "markdown":
		return "markdown"
	case "py":
		return "python"
	case "js", "mjs", "cjs":
		return "javascript"
	case "ts", "mts", "cts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "rs":
		return "rust"
	case "rb":
		return "ruby"
	case "sh", "bash", "zsh":
		return "sh"
	case "yml":
		return "yaml"
	case "json", "toml", "yaml", "html", "css", "xml", "sql", "lua", "php", "swift", "kt", "java", "c", "h", "cc", "cpp", "cs":
		return ext
	}
	return ""
}

// diffPathForTool resolves the header path. Structured evidence wins; a
// path-shaped token in the invocation is the fallback, never a guess from the
// model's prose.
func diffPathForTool(tool *ToolEntry) string {
	if tool == nil {
		return ""
	}
	if path := strings.TrimSpace(tool.Path); path != "" {
		return path
	}
	fields := strings.Fields(toolInvocation(tool))
	for _, field := range fields[1:] {
		candidate := strings.Trim(field, `"'()[]{}:,;`)
		if strings.ContainsAny(candidate, "/\\") || strings.Contains(candidate, ".") {
			return candidate
		}
	}
	return ""
}

// renderExpandedCodeDiff renders the expanded mutation block. It reports false
// when there is no usable diff, so the caller keeps the ordinary bounded body.
func renderExpandedCodeDiff(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout) bool {
	if tool == nil || strings.TrimSpace(tool.Diff) == "" {
		return false
	}
	// Bound the parse for a pathological generated diff. The header still
	// reports the rows actually parsed; the truncation marker makes the cut
	// visible rather than silently claiming a complete change set.
	diffText, _ := TruncateForDisplay(tool.Diff, 64*1024)
	view := parseCodeDiff(diffText)
	if len(view.lines) == 0 {
		return false
	}
	path := diffPathForTool(tool)
	if path == "" && view.path != "" {
		path = view.path
	}
	if path == "" {
		path = "diff"
	}
	width := textWidth(layout)
	if width <= 0 {
		width = 80
	}
	const indent = "    "
	inner := width - CellWidth(indent)
	if inner < 8 {
		inner = 8
	}

	sb.WriteString(renderCodeDiffHeader(path, view.add, view.del, tool.Operation, width, pal) + "\n")

	gutter := 2
	for _, line := range view.lines {
		if line.number > 0 {
			if n := len(strconv.Itoa(line.number)); n > gutter {
				gutter = n
			}
		}
	}
	limit := maxToolLines(layout)
	shown := 0
	for _, line := range view.lines {
		if shown >= limit {
			sb.WriteString(indent + pal.Truncated.Render("… [diff truncated]") + "\n")
			break
		}
		switch line.kind {
		case codeDiffHunk:
			sb.WriteString(indent + pal.Cyan.Render(TruncateToWidth(line.text, inner)) + "\n")
		case codeDiffMeta:
			sb.WriteString(indent + pal.Truncated.Render(TruncateToWidth(line.text, inner)) + "\n")
		default:
			sb.WriteString(renderCodeDiffRow(line, gutter, inner, languageForPath(path), pal) + "\n")
		}
		shown++
	}
	return true
}

// renderCodeDiffHeader draws the compact identity row: operation, truncated
// path, change metrics, and the exact key that collapses the block.
func renderCodeDiffHeader(path string, add, del int, operation string, width int, pal *Palette) string {
	const indent = "    "
	label := diffOperationLabel(operation)
	metrics := fmt.Sprintf("[+%d, -%d]", add, del)
	hint := "ctrl+o to collapse"
	prefix := indent + "⏺ " + label + " ("
	close := ")"
	suffix := "  " + metrics
	trailer := "  ·  " + hint
	budget := width - CellWidth(prefix) - CellWidth(close) - CellWidth(suffix) - CellWidth(trailer)
	if budget < 8 {
		hint = "^o collapse"
		trailer = "  ·  " + hint
		budget = width - CellWidth(prefix) - CellWidth(close) - CellWidth(suffix) - CellWidth(trailer)
	}
	if budget < 4 {
		// Very narrow: preserve the operation and path identity, and let the
		// whole row truncate at the end rather than drawing past the column.
		trailer = ""
		budget = width - CellWidth(prefix) - CellWidth(close) - CellWidth(suffix)
	}
	if budget < 1 {
		budget = 1
	}
	path = TruncateMiddleToWidth(sanitizeInline(path), budget)
	line := prefix + path + close + suffix
	if trailer != "" {
		line += trailer
	}
	if width > 0 && CellWidth(line) > width {
		return pal.Muted.Render(TruncateToWidth(line, width))
	}
	// Style after the plain budget is fixed, so ANSI can never move the
	// metrics or the collapse hint off the row.
	return indent + pal.ToolDone.Render("⏺") + " " + pal.Status.Render(label) + " " +
		pal.Muted.Render("(") + pal.Cyan.Render(path) + pal.Muted.Render(")") +
		"  " + pal.Muted.Render("[") + pal.ToolDone.Render("+") + pal.Status.Render(strconv.Itoa(add)) +
		", " + pal.ToolError.Render("-") + pal.Status.Render(strconv.Itoa(del)) +
		pal.Muted.Render("]") + trailingDiffHint(trailer, pal)
}

// trailingDiffHint styles the already-budgeted hint suffix without altering
// its cells. The brackets around the metrics are added here because the
// budgeted suffix deliberately carries the plain form.
func trailingDiffHint(trailer string, pal *Palette) string {
	if trailer == "" {
		return ""
	}
	if strings.Contains(trailer, "ctrl+o to collapse") {
		return "  ·  " + pal.Muted.Render("ctrl+o ") + pal.Bold.Render("to collapse")
	}
	return "  ·  " + pal.Muted.Render(strings.TrimPrefix(trailer, "  ·  "))
}

// diffOperationLabel gives the header a readable action word while preserving
// an unknown operation verbatim.
func diffOperationLabel(operation string) string {
	switch strings.ToUpper(strings.TrimSpace(operation)) {
	case "CREATE":
		return "Create"
	case "MODIFY", "UPDATE", "":
		return "Update"
	case "DELETE":
		return "Delete"
	default:
		return sanitizeInline(strings.TrimSpace(operation))
	}
}

// renderCodeDiffRow draws one add, delete, or context row. The tint spans the
// full column, and syntax colour is layered over the same background so the
// row still reads as one stripe.
func renderCodeDiffRow(line codeDiffLine, gutter, inner int, lang string, pal *Palette) string {
	mark := " "
	kind := line.kind
	switch kind {
	case codeDiffAdd:
		mark = "+"
	case codeDiffDelete:
		mark = "-"
	}
	number := ""
	if line.number > 0 {
		number = strconv.Itoa(line.number)
	}
	if pad := gutter - len(number); pad > 0 {
		number = strings.Repeat(" ", pad) + number
	}
	lead := number + " " + mark + " "
	contentBudget := inner - CellWidth(lead)
	if contentBudget < 1 {
		contentBudget = 1
	}
	content := TruncateToWidth(line.text, contentBudget)

	rowStyle := pal.DiffContext
	tokenStyles := codeTokenStyles{
		plain:   pal.DiffContext,
		keyword: pal.DiffContext,
		string:  pal.DiffContext,
		comment: pal.DiffContext,
		number:  pal.DiffContext,
	}
	if kind == codeDiffAdd || kind == codeDiffDelete {
		bg := pal.DiffAddBG
		base := pal.DiffAdd
		if kind == codeDiffDelete {
			bg = pal.DiffDeleteBG
			base = pal.DiffDelete
		}
		rowStyle = lipgloss.NewStyle().Foreground(base.GetForeground()).Background(bg)
		tokenStyles = codeTokenStyles{
			plain:   rowStyle,
			keyword: lipgloss.NewStyle().Foreground(pal.CodeKeyword.GetForeground()).Background(bg).Bold(true),
			string:  lipgloss.NewStyle().Foreground(pal.CodeString.GetForeground()).Background(bg),
			comment: lipgloss.NewStyle().Foreground(pal.CodeComment.GetForeground()).Background(bg).Italic(true),
			number:  lipgloss.NewStyle().Foreground(pal.CodeNumber.GetForeground()).Background(bg),
		}
	}
	highlighted := highlightCodeWithStyles(content, lang, tokenStyles)
	pad := inner - CellWidth(lead) - CellWidth(content)
	if pad < 0 {
		pad = 0
	}
	return "    " + rowStyle.Render(lead) + highlighted + rowStyle.Render(strings.Repeat(" ", pad))
}
