package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The footer glyphs are the terminal markers every settled turn carries. One
// glyph column names the state without colour, so the footer survives a
// monochrome terminal and a screenshot.
const (
	footerVerifiedGlyph  = "✓"
	footerReviewGlyph    = "⚠"
	footerCancelledGlyph = "○"

	footerReviewLabel    = "needs review:"
	footerCancelledLabel = "cancelled · turn interrupted by user"
)

// TurnFooterKind discriminates the one terminal status line a turn renders.
type TurnFooterKind string

const (
	// FooterVerified closes a turn whose post-turn verification passed with
	// no warnings and no unrecovered failure.
	FooterVerified TurnFooterKind = "verified"
	// FooterReview closes a turn that stopped at an evidence boundary. The
	// detail is the runtime's reason, never model prose.
	FooterReview TurnFooterKind = "review"
	// FooterCancelled closes a turn the user aborted.
	FooterCancelled TurnFooterKind = "cancelled"
)

// TurnFooter is the single terminal footer of a settled turn.
//
// A turn owns exactly one slot: the reducer renders either a review, a
// verified summary, or a cancellation, never two and never none. Detail is
// deterministic telemetry for a verified turn or the evidence reason for a
// review; it is never the model's closing sentence.
type TurnFooter struct {
	Kind   TurnFooterKind
	Detail string
}

// turnFacts is the bounded, observed evidence a verified footer may state.
//
// Only successful calls contribute change/read counts: a failed write changed
// nothing, and a refused call ran nothing. Call counts include every outcome,
// because the footer describes the turn's effort, not only its successes.
type turnFacts struct {
	calls   int
	changed []string
	read    []string
	checks  int
}

// collectTurnFacts folds a run's tool outcomes into footer telemetry.
func collectTurnFacts(turns []agent.Turn) turnFacts {
	var facts turnFacts
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			facts.calls++
			name := strings.ToLower(strings.TrimSpace(outcome.ToolName))
			if name == "bash" {
				command := footerStringArg(outcome.Args, "command")
				if command == "" {
					continue
				}
				if !isCheckCommand(command) {
					continue
				}
				if outcome.Refused() || outcome.Result.IsError {
					continue
				}
				facts.checks++
				continue
			}
			if outcome.Refused() || outcome.Result.IsError {
				continue
			}
			switch name {
			case "write", "edit", "delete":
				if path := footerPathArg(outcome.Args); path != "" {
					facts.changed = appendUnique(facts.changed, path)
				}
			case "read":
				if path := footerPathArg(outcome.Args); path != "" {
					facts.read = appendUnique(facts.read, path)
				}
			}
		}
	}
	return facts
}

// turnFooterForOutcome selects the one terminal footer from the runtime's own
// verdict. A paused or failed turn never claims verification merely because it
// returned without a loop error: its non-complete action is a review boundary.
//
// A cancelled turn is closed by TurnCancelled, which owns the same slot, so
// the boundary event returns no footer for it. A hard failure already
// published its error line and likewise leaves the slot to that report.
func turnFooterForOutcome(action taskAction, reason, reviewNotice string, loopErr error, turns []agent.Turn, authority string) *TurnFooter {
	if notice := strings.TrimSpace(reviewNotice); notice != "" {
		return &TurnFooter{Kind: FooterReview, Detail: notice}
	}
	if loopErr != nil {
		return nil
	}
	switch action {
	case taskActionComplete, taskActionPhaseComplete:
		return verifiedFooter(turns, authority)
	case taskActionPause, taskActionFail:
		if detail := strings.TrimSpace(reason); detail != "" {
			return &TurnFooter{Kind: FooterReview, Detail: detail}
		}
	}
	return nil
}

// verifiedFooter derives the clean-turn footer from the run's own evidence.
//
// The summary is deliberately deterministic: counts of observed calls, files,
// and checks. It never reads the assistant's answer, so the footer cannot
// claim a completion the verifier did not observe, and the same run always
// renders the same line.
func verifiedFooter(turns []agent.Turn, authority string) *TurnFooter {
	facts := collectTurnFacts(turns)
	parts := []string{countLabel(facts.calls, "tool call")}

	switch {
	case authority == workspace.Ask.String():
		parts = append(parts, "read-only analysis")
	case len(facts.changed) > 0:
		parts = append(parts, countLabel(len(facts.changed), "file")+" changed")
	case len(facts.read) > 0:
		parts = append(parts, countLabel(len(facts.read), "file")+" read")
	case authority == workspace.Build.String():
		parts = append(parts, "no files changed")
	default:
		parts = append(parts, "read-only analysis")
	}

	switch {
	case authority == workspace.Ask.String():
		parts = append(parts, "provenance clean")
	case facts.checks == 0:
		parts = append(parts, "no checks run")
	default:
		parts = append(parts, "all checks clean")
	}

	return &TurnFooter{Kind: FooterVerified, Detail: strings.Join(parts, " · ")}
}

// isCheckCommand recognizes a command whose success is verification evidence.
//
// Token matching, not substring matching, so a path such as build.log or a
// prose argument does not turn an unrelated command into a check.
func isCheckCommand(command string) bool {
	for _, field := range strings.Fields(strings.ToLower(command)) {
		switch strings.Trim(field, "'\"`") {
		case "test", "tests", "build", "vet", "lint", "typecheck", "tsc", "pytest", "golangci-lint":
			return true
		}
	}
	return false
}

// countLabel renders one count with a correct singular noun.
func countLabel(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// footerPathArg returns the first non-empty path argument.
func footerPathArg(args map[string]any) string {
	for _, key := range []string{"path", "file"} {
		if value := footerStringArg(args, key); value != "" {
			return value
		}
	}
	return ""
}

// footerStringArg returns one trimmed string argument.
func footerStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
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

// footerText renders the footer's logical one-line form.
func footerText(footer *TurnFooter) string {
	if footer == nil {
		return ""
	}
	switch footer.Kind {
	case FooterReview:
		reason := strings.TrimSpace(sanitizeInline(footer.Detail))
		if reason == "" {
			return footerReviewGlyph + " " + footerReviewLabel
		}
		return footerReviewGlyph + " " + footerReviewLabel + " " + reason
	case FooterCancelled:
		return footerCancelledGlyph + " " + footerCancelledLabel
	default:
		detail := strings.TrimSpace(sanitizeInline(footer.Detail))
		if detail == "" {
			return footerVerifiedGlyph + " verified"
		}
		return footerVerifiedGlyph + " verified · " + detail
	}
}

// footerLines folds a footer to its display rows.
//
// A review may wrap: its reason is prose and hiding half of it would hide the
// evidence boundary. Continuation rows align under the narrative rather than
// under the glyph, so the block reads as one paragraph. Every other footer is
// a status anchor and is strictly one row, truncated with an ellipsis so a
// clean turn never consumes additional vertical space.
func footerLines(footer *TurnFooter, width int) []string {
	line := footerText(footer)
	if line == "" {
		return nil
	}
	if width <= 0 {
		return []string{line}
	}
	if footer.Kind != FooterReview || CellWidth(line) <= width {
		return []string{TruncateToWidth(line, width)}
	}
	prefix := footerReviewGlyph + " " + footerReviewLabel + " "
	body := strings.TrimPrefix(line, prefix)
	prefixWidth := CellWidth(prefix)
	bodyWidth := width - prefixWidth
	if bodyWidth < 8 {
		return WrapToWidth(line, width)
	}
	rows := WrapToWidth(body, bodyWidth)
	out := make([]string, 0, len(rows))
	indent := strings.Repeat(" ", prefixWidth)
	for i, row := range rows {
		if i == 0 {
			out = append(out, prefix+row)
			continue
		}
		out = append(out, indent+row)
	}
	return out
}

// footerStyle selects the footer's quiet presentation tier.
//
// A verified turn takes the dim reference accent; a review or cancellation
// stays in the muted italic notice tier. Neither uses the alert colour: the
// glyph and wording carry the state, so the footer never competes with the
// answer.
func footerStyle(footer *TurnFooter, pal *Palette) lipgloss.Style {
	if footer != nil && footer.Kind == FooterVerified {
		return pal.Footer
	}
	return pal.Notice
}
