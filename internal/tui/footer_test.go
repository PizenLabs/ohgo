package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A clean turn closes with one deterministic verified line: counts from
// observed outcomes, never the model's closing prose.
func TestCleanTurnFooterIsDeterministicTelemetry(t *testing.T) {
	turns := []agent.Turn{{
		Results: []agent.ToolOutcome{
			{ToolName: "read", Args: map[string]any{"path": "internal/tui/view.go"}},
			{ToolName: "edit", Args: map[string]any{"path": "internal/tui/view.go"}},
			// The same path edited twice is one changed file.
			{ToolName: "edit", Args: map[string]any{"path": "internal/tui/view.go"}},
			{ToolName: "bash", Args: map[string]any{"command": "go test ./..."}},
		},
	}}
	footer := verifiedFooter(turns, "build")
	if footer.Kind != FooterVerified {
		t.Fatalf("kind = %q, want verified", footer.Kind)
	}
	const want = "✓ verified · 4 tool calls · 1 file changed · all checks clean"
	if got := footerText(footer); got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
	// The same evidence always yields the same line.
	if again := footerText(verifiedFooter(turns, "build")); again != want {
		t.Fatalf("second derivation = %q, want %q", again, want)
	}
}

// An ASK turn states the read-only verdict instead of implying a check ran.
func TestCleanAskFooterNamesReadOnlyProvenance(t *testing.T) {
	footer := verifiedFooter(nil, "ask")
	const want = "✓ verified · 0 tool calls · read-only analysis · provenance clean"
	if got := footerText(footer); got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

// "Verified" is about the turn's checks, not the repository. A partial
// structural snapshot is named in the same line, so a shallow survey cannot
// read as full coverage; a complete one carries no note.
func TestVerifiedFooterNamesPartialStructuralCoverage(t *testing.T) {
	partial := []agent.Turn{{Results: []agent.ToolOutcome{
		{ToolName: "read", Args: map[string]any{"path": "app/page.tsx"}},
		{ToolName: "structure", Args: map[string]any{"view": "overview"}, Result: agent.Result{
			Text: "structural: UNPARSED",
			Details: map[string]any{
				"view": "overview", "parsed_files": 0, "pending_files": 77,
				"structural_complete": false, "inventory_complete": true, "truncated": true,
			},
		}},
	}}}
	footer := footerText(verifiedFooter(partial, "build"))
	if !strings.Contains(footer, "structural coverage 0/77 parsed") {
		t.Fatalf("footer = %q, want the coverage note", footer)
	}

	complete := []agent.Turn{{Results: []agent.ToolOutcome{{
		ToolName: "structure", Args: map[string]any{"view": "overview"},
		Result: agent.Result{Text: "structural: COMPLETE", Details: map[string]any{
			"view": "overview", "parsed_files": 77, "pending_files": 0,
			"structural_complete": true, "inventory_complete": true,
		}},
	}}}}
	if got := footerText(verifiedFooter(complete, "build")); strings.Contains(got, "structural coverage") {
		t.Fatalf("complete snapshot carried a coverage note: %q", got)
	}
}

// The renderer places the clean footer below the answer and directly above
// the composer, separated from the answer by one blank row.
func TestCleanTurnFooterRendersAtTheTurnTail(t *testing.T) {
	state := New()
	state.Apply(TerminalResized{Width: 100, Height: 40})
	state.Apply(UserSubmitted{Text: "ship it", Seq: 1})
	state.Apply(AssistantDelta{Text: "Shipped the parser fix.", Seq: 1})
	state.Apply(AssistantDone{Seq: 1, Footer: verifiedFooter(nil, "ask")})

	last := state.Entries[len(state.Entries)-1]
	if last.Kind != EntryFooter || last.Footer == nil {
		t.Fatalf("last entry = %+v, want the terminal footer", last)
	}
	if got := footerText(last.Footer); got != "✓ verified · 0 tool calls · read-only analysis · provenance clean" {
		t.Fatalf("footer = %q", got)
	}

	lines := strings.Split(ansi.Strip(Render(state)), "\n")
	footerRow := -1
	for i, line := range lines {
		if strings.Contains(line, "✓ verified") {
			footerRow = i
		}
	}
	if footerRow < 1 {
		t.Fatalf("the footer is missing from the frame:\n%s", strings.Join(lines, "\n"))
	}
	if strings.TrimSpace(lines[footerRow-1]) != "" {
		t.Fatalf("the footer is not separated from the answer by a blank row:\n%s", strings.Join(lines, "\n"))
	}
	nearest := ""
	for i := footerRow + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.Trim(line, "─") == "" || isComposerBorderHeader(line) {
			continue
		}
		nearest = line
		break
	}
	if !strings.HasPrefix(nearest, "›") {
		t.Fatalf("nearest row below the footer = %q, want the composer:\n%s", nearest, strings.Join(lines, "\n"))
	}
}

// A review keeps the subdued notice style but gains the warning glyph, so it
// is scannable without alert colour.
func TestReviewFooterCarriesWarningGlyphAndStaysMuted(t *testing.T) {
	reason := "answer provenance: the answer presents documentation-only term gocommerce as an observed repository fact"
	footer := &TurnFooter{Kind: FooterReview, Detail: reason}
	want := "⚠ needs review: " + reason
	if got := footerText(footer); got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
	pal := DarkPalette()
	if got := colorName(footerStyle(footer, pal).GetForeground()); got != DarkTokens().TextMuted {
		t.Fatalf("review footer foreground = %q, want the muted token %s", got, DarkTokens().TextMuted)
	}
	if !footerStyle(footer, pal).GetItalic() {
		t.Fatal("review footer is not italic; it must not compete with the answer")
	}

	state := New()
	state.Apply(TerminalResized{Width: 120, Height: 40})
	state.Apply(UserSubmitted{Text: "check it", Seq: 1})
	state.Apply(AssistantDelta{Text: "It is gocommerce.", Seq: 1})
	state.Apply(AssistantDone{Seq: 1, Footer: footer})
	frame := ansi.Strip(Render(state))
	if !strings.Contains(frame, "⚠ needs review: answer provenance") {
		t.Fatalf("the review prefix is missing from the frame:\n%s", frame)
	}
	if strings.Contains(frame, "✓ verified") {
		t.Fatalf("a review turn also rendered the clean footer:\n%s", frame)
	}
}

// A review reason is prose, so it wraps word-safely at the content margin.
func TestReviewFooterWrapsWithoutBreakingWords(t *testing.T) {
	footer := &TurnFooter{
		Kind:   FooterReview,
		Detail: "answer provenance: the answer presents documentation-only term gocommerce as an observed repository fact",
	}
	rows := footerLines(footer, 48)
	if len(rows) < 2 {
		t.Fatalf("rows = %d, want a wrapped review", len(rows))
	}
	if !strings.HasPrefix(rows[0], "⚠ needs review: ") {
		t.Fatalf("first row = %q, want the review prefix", rows[0])
	}
	for _, row := range rows {
		if CellWidth(row) > 48 {
			t.Fatalf("row %q exceeds the 48-cell budget", row)
		}
	}
	joined := strings.Join(rows, " ")
	for _, word := range []string{"documentation-only", "gocommerce", "observed", "repository"} {
		if !strings.Contains(joined, word) {
			t.Fatalf("wrapping broke %q: %q", word, joined)
		}
	}
	for _, marker := range []string{"...", "…"} {
		if strings.Contains(joined, marker) {
			t.Fatalf("review footer was truncated with %q: %q", marker, joined)
		}
	}
	for _, row := range rows[1:] {
		if !strings.HasPrefix(row, "  ") || strings.HasPrefix(row, "   ") {
			t.Fatalf("continuation row %q lacks the two-space hanging indent", row)
		}
	}
}

// A long multi-clause reason stays fully visible at standard and narrow
// widths: the review must wrap, never clip a clause with an ellipsis.
func TestReviewFooterKeepsLongReasonAtStandardAndNarrowWidths(t *testing.T) {
	reason := `intent grounding: the answer treats "path" as absent without evidence covering the claim; the answer states "mock auth" as implementation state, but no source observation covers admin; the answer presents documentation-only term gocommerce as an observed repository fact`
	footer := &TurnFooter{Kind: FooterReview, Detail: reason}
	for _, width := range []int{120, 60} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			rows := footerLines(footer, width)
			if len(rows) < 2 {
				t.Fatalf("rows = %d, want a wrapped review", len(rows))
			}
			if !strings.HasPrefix(rows[0], "⚠ needs review: ") {
				t.Fatalf("first row = %q, want the review prefix", rows[0])
			}
			joined := strings.Join(strings.Fields(strings.Join(rows, " ")), " ")
			for _, phrase := range []string{
				`treats "path" as absent`,
				`states "mock auth" as implementation state`,
				"documentation-only term gocommerce",
			} {
				if !strings.Contains(joined, phrase) {
					t.Fatalf("row set lost %q: %q", phrase, joined)
				}
			}
			for _, marker := range []string{"...", "…"} {
				if strings.Contains(joined, marker) {
					t.Fatalf("review footer was truncated with %q: %q", marker, joined)
				}
			}
			for i, row := range rows {
				if CellWidth(row) > width {
					t.Fatalf("row %d = %q exceeds the %d-cell budget", i, row, width)
				}
				if i == 0 {
					continue
				}
				if !strings.HasPrefix(row, "  ") || strings.HasPrefix(row, "   ") {
					t.Fatalf("continuation row %d = %q lacks the two-space hanging indent", i, row)
				}
			}
		})
	}
}

// The composed frame carries every wrapped review row at standard and narrow
// widths; no render stage re-clamps the block to one row.
func TestReviewFooterRendersFullReasonInFrame(t *testing.T) {
	reason := `intent grounding: the answer treats "path" as absent without evidence covering the claim; the answer states "mock auth" as implementation state, but no source observation covers admin; the answer presents documentation-only term gocommerce as an observed repository fact`
	footer := &TurnFooter{Kind: FooterReview, Detail: reason}
	for _, width := range []int{120, 60} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			state := New()
			state.Apply(TerminalResized{Width: width, Height: 40})
			state.Apply(UserSubmitted{Text: "check it", Seq: 1})
			state.Apply(AssistantDelta{Text: "It is gocommerce.", Seq: 1})
			state.Apply(AssistantDone{Seq: 1, Footer: footer})
			frame := ansi.Strip(Render(state))
			for _, row := range footerLines(footer, textWidth(ComputeLayout(state))) {
				if !strings.Contains(frame, strings.TrimRight(row, " ")) {
					t.Fatalf("frame lost review row %q:\n%s", row, frame)
				}
			}
		})
	}
}

// Repeated clauses are collapsed and only the first distinct findings are
// rendered; the cap lands on a clause boundary, never mid-sentence.
func TestReviewFooterCompactsDistinctClauses(t *testing.T) {
	footer := &TurnFooter{
		Kind:   FooterReview,
		Detail: "first finding; second finding; First finding; third finding; fourth finding",
	}
	want := "⚠ needs review: first finding; second finding; third finding"
	if got := footerText(footer); got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

// A cancelled footer, like a verified one, is a one-row status anchor.
func TestCancelledFooterTruncatesToOneLine(t *testing.T) {
	rows := footerLines(&TurnFooter{Kind: FooterCancelled}, 24)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want exactly one", len(rows))
	}
	if CellWidth(rows[0]) > 24 {
		t.Fatalf("row %q exceeds the 24-cell budget", rows[0])
	}
	if !strings.HasSuffix(rows[0], "…") {
		t.Fatalf("row %q was not truncated with an ellipsis", rows[0])
	}
}

// A cancelled turn owns the same slot and states the interruption explicitly.
func TestCancelledTurnFooterRendersOnce(t *testing.T) {
	state := New()
	state.Apply(UserSubmitted{Text: "work", Seq: 1})
	state.Apply(TurnCancelled{Seq: 1})
	state.Apply(AssistantDone{Seq: 1, Footer: verifiedFooter(nil, "build")})

	footers := 0
	var text string
	for _, entry := range state.Entries {
		if entry.Kind != EntryFooter {
			continue
		}
		footers++
		text = footerText(entry.Footer)
	}
	if footers != 1 {
		t.Fatalf("footers = %d, want exactly one slot", footers)
	}
	if text != "○ cancelled · turn interrupted by user" {
		t.Fatalf("footer = %q, want the canonical cancellation line", text)
	}
}

// A late task projection after cancellation must not push the footer away
// from the turn's tail.
func TestCancelledFooterStaysTerminalAfterLateProjection(t *testing.T) {
	state := New()
	state.Apply(UserSubmitted{Text: "work", Seq: 1})
	state.Apply(TurnCancelled{Seq: 1})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "PAUSED", Objective: "work",
	}})

	last := state.Entries[len(state.Entries)-1]
	if last.Kind != EntryFooter || last.Footer == nil || last.Footer.Kind != FooterCancelled {
		t.Fatalf("last entry = %+v, want the cancellation footer at the tail", last)
	}
	if got := countFooterEntries(state); got != 1 {
		t.Fatalf("footers = %d, want exactly one", got)
	}
	foundTask := false
	for _, entry := range state.Entries {
		if entry.Kind == EntryTask {
			foundTask = true
		}
	}
	if !foundTask {
		t.Fatal("the late task projection was dropped")
	}
}

// A turn that stopped without completing must never borrow the verified
// verdict: its non-complete action is a review boundary.
func TestPausedTurnDoesNotClaimVerification(t *testing.T) {
	footer := turnFooterForOutcome(taskActionPause, "turn limit reached", "", nil, nil, "build")
	if footer == nil || footer.Kind != FooterReview {
		t.Fatalf("footer = %+v, want the review verdict", footer)
	}
	if got := footerText(footer); got != "⚠ needs review: turn limit reached" {
		t.Fatalf("footer = %q, want the pause reason as a review boundary", got)
	}
}

// A completed turn selects the deterministic verified summary.
func TestCompletedTurnSelectsVerifiedFooter(t *testing.T) {
	footer := turnFooterForOutcome(taskActionComplete, "", "", nil, nil, "ask")
	if footer == nil || footer.Kind != FooterVerified {
		t.Fatalf("footer = %+v, want the verified verdict", footer)
	}
}

// A hard failure already published its error line; it owns no second footer.
func TestFailedTurnLeavesTheSlotToItsErrorLine(t *testing.T) {
	if footer := turnFooterForOutcome(taskActionFail, "boom", "", errors.New("boom"), nil, "build"); footer != nil {
		t.Fatalf("footer = %+v, want no second terminal line", footer)
	}
}

// A clean footer never grows the turn: it is truncated with an ellipsis at the
// terminal width instead of wrapping.
func TestCleanFooterTruncatesToOneLine(t *testing.T) {
	footer := &TurnFooter{
		Kind:   FooterVerified,
		Detail: "4 tool calls · 2 files changed · all checks clean",
	}
	rows := footerLines(footer, 24)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want exactly one", len(rows))
	}
	if CellWidth(rows[0]) > 24 {
		t.Fatalf("row %q exceeds the 24-cell budget", rows[0])
	}
	if !strings.HasSuffix(rows[0], "…") {
		t.Fatalf("row %q was not truncated with an ellipsis", rows[0])
	}
}

// The footer is display metadata: it never enters a clipboard payload.
func TestFooterIsNotPartOfBlockCopy(t *testing.T) {
	entry := Entry{Kind: EntryFooter, Footer: &TurnFooter{Kind: FooterVerified, Detail: "4 tool calls · all checks clean"}}
	if got := BlockCopyText(entry); got != "" {
		t.Fatalf("footer copy = %q, want empty metadata", got)
	}
}
