package tui

import (
	"strings"
)

// IdentityFacts is the truthful runtime state the identity surface shows.
//
// Every field is display-ready plain text derived from actual state: the
// project worker display name, the greeting for it, the model reference, the
// workspace label, the working directory, and the git line. Empty means
// unknown and renders as an explicit placeholder, never as invented state.
type IdentityFacts struct {
	// User is the project worker display name, "" when unset.
	User string
	// Greeting is the time-of-day greeting, e.g. "Good evening, Jack."
	Greeting string
	// Model is the model reference, e.g. "openrouter/qwen3-coder".
	Model string
	// Workspace is the authority label, e.g. "BUILD".
	Workspace string
	// Cwd is the working directory, already shortened for display.
	Cwd string
	// Git is the one-line git state, e.g. "main · clean".
	Git string
	// Ready is runtime readiness, e.g. "ready".
	Ready string
}

// FactsFromStatus derives the identity facts from rendered state.
func FactsFromStatus(s State) IdentityFacts {
	return IdentityFacts{
		User:      s.Status.User,
		Greeting:  s.Status.Greeting,
		Model:     s.Status.Model,
		Workspace: s.Status.Workspace,
		Cwd:       s.Status.Cwd,
		Git:       gitLine(s),
		Ready:     readyLine(s),
	}
}

func gitLine(s State) string {
	branch := strings.TrimSpace(s.Status.GitBranch)
	status := strings.TrimSpace(s.Status.GitStatus)
	switch {
	case branch == "" && status == "":
		return ""
	case status == "":
		return branch
	case branch == "":
		return status
	default:
		return branch + " · " + status
	}
}

func readyLine(s State) string {
	if strings.TrimSpace(s.Status.Ready) != "" {
		return strings.TrimSpace(s.Status.Ready)
	}
	if s.Status.Busy {
		return "working"
	}
	return "ready"
}

// capabilityModes are OHGO's three interaction/authority modes. They are
// modes, not a pipeline: rendering keeps them parallel, never joined by an
// arrow.
var capabilityModes = []struct{ name, desc string }{
	{"ASK", "Understand, inspect, reason"},
	{"BUILD", "Authorized changes"},
	{"FUZZ", "$sup · fast assistance"},
}

// RenderIdentitySurface renders the runtime identity surface for facts at
// width. It is pure: semantic sections first, then a density policy per
// tier, then truncation so no line exceeds width and no border breaks.
//
// Tiers mirror LayoutForMode: wide (>=100) boxes a two-column composition,
// medium (70-99) boxes a dense vertical stack, narrow (<70) drops the border
// and keeps content over decoration. Non-positive widths render the medium
// stack unboxed.
func RenderIdentitySurface(f IdentityFacts, width int) string {
	if width <= 0 {
		return renderIdentityStack(f, 0, false)
	}
	switch {
	case width >= WideMinWidth:
		return renderIdentityBoxed(f, width, true)
	case width >= MediumMinWidth:
		return renderIdentityBoxed(f, width, false)
	default:
		return renderIdentityStack(f, width, false)
	}
}

// renderIdentityStack is the borderless semantic stack shared by narrow
// terminals and unbounded renders.
func renderIdentityStack(f IdentityFacts, width int, _ bool) string {
	lines := identityContentLines(f, false, 0)
	return fitLines(lines, width)
}

// renderIdentityBoxed frames the semantic content in a terminal-native box.
// split selects the two-column capability region on wide terminals.
func renderIdentityBoxed(f IdentityFacts, width int, split bool) string {
	inner := width - 4
	if inner < 10 {
		// Too small for a box: content beats decoration.
		return renderIdentityStack(f, width, false)
	}
	contentW := inner - 2
	content := identityContentLines(f, split, contentW)
	var sb strings.Builder
	sb.WriteString(fitOne("┌─ ohgo ─"+strings.Repeat("─", max(0, inner-8))+"┐", width) + "\n")
	sb.WriteString(fitOne("│"+strings.Repeat(" ", inner)+"│", width) + "\n")
	for _, line := range content {
		sb.WriteString(fitOne("│ "+truncateCells(line, inner-2)+strings.Repeat(" ", max(0, inner-2-CellWidth(truncateCells(line, inner-2))))+" │", width) + "\n")
	}
	sb.WriteString(fitOne("│"+strings.Repeat(" ", inner)+"│", width) + "\n")
	sb.WriteString(fitOne("└"+strings.Repeat("─", inner)+"┘", width))
	return sb.String()
}

// identityContentLines builds the semantic sections: identity header, runtime
// meta, capability modes, affordances. split packs modes and affordances
// into two columns on wide terminals. contentW sizes the divider and the
// columns; non-positive falls back to a fixed 34-cell column.
func identityContentLines(f IdentityFacts, split bool, contentW int) []string {
	var lines []string
	title := "OHGO"
	if f.Greeting != "" {
		// Greeting carries the name; title stays product identity.
		lines = append(lines, title+"   "+f.Greeting)
	} else if f.User != "" {
		lines = append(lines, title+"   "+f.User)
	} else {
		lines = append(lines, title)
	}
	lines = append(lines, "agent runtime for real codebases")
	lines = append(lines, "")
	model := f.Model
	if model == "" {
		model = "no model"
	}
	git := f.Git
	if git == "" {
		git = "git unavailable"
	}
	cwd := f.Cwd
	if cwd == "" {
		cwd = "no workspace"
	}
	ready := f.Ready
	if ready == "" {
		ready = "ready"
	}
	col := 34
	if contentW > 0 {
		// Two columns share the content width around the divider.
		col = (contentW - 1) / 2
		if col < 10 {
			col = 10
		}
	}
	divider := strings.Repeat("─", col)
	if split {
		lines = append(lines, "model      "+model+"        git    "+git)
		lines = append(lines, "workspace  "+cwd+"        status "+ready)
		lines = append(lines, "")
		lines = append(lines, divider+"┬"+divider)
		lines = append(lines, padCells("ASK", col)+"│ "+padCells("BUILD", col))
		lines = append(lines, padCells(capabilityModes[0].desc, col)+"│ "+padCells(capabilityModes[1].desc, col))
		lines = append(lines, padCells("", col)+"│ "+padCells("", col))
		lines = append(lines, padCells("BUILD", col)+"│ "+padCells("FUZZ", col))
		lines = append(lines, padCells(capabilityModes[1].desc, col)+"│ "+padCells(capabilityModes[2].desc, col))
		lines = append(lines, padCells("", col)+"│ "+padCells("", col))
		lines = append(lines, padCells("Dynamic path. Static authority.", col)+"│ "+padCells("/model   choose model", col))
		lines = append(lines, padCells("Truthful state transition.", col)+"│ "+padCells("/help    commands", col))
	} else {
		// Stacked meta: one fact per line so narrow terminals truncate
		// values, never whole facts.
		lines = append(lines, "model      "+model)
		lines = append(lines, "git        "+git)
		lines = append(lines, "workspace  "+cwd)
		lines = append(lines, "status     "+ready)
		lines = append(lines, "")
		for _, m := range capabilityModes {
			lines = append(lines, padCells(m.name, 9)+m.desc)
		}
		lines = append(lines, "")
		lines = append(lines, "/model    choose model        /help    commands")
	}
	return lines
}

func fitLines(lines []string, width int) string {
	if width <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, fitOne(line, width))
	}
	return strings.Join(out, "\n")
}

func fitOne(line string, width int) string {
	if width <= 0 {
		return line
	}
	if CellWidth(line) > width {
		return truncateCells(line, width)
	}
	return line
}

func padCells(s string, width int) string {
	if w := CellWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return truncateCells(s, width)
}

func truncateCells(s string, width int) string {
	return TruncateToWidth(s, width)
}
