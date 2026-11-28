package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// candidateText renders one entry the way the transcript shows it, so these
// tests assert what the user can actually read rather than a private field.
func candidateText(entry Entry) string {
	var sb strings.Builder
	renderEntryContent(&sb, entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{})
	return sb.String()
}

// renderStateEntries renders the transcript projection without the surrounding
// chrome. It is the same entry renderer the full-screen and live surfaces use.
func renderStateEntries(s State) string {
	var sb strings.Builder
	for _, entry := range s.Entries {
		renderEntryContent(&sb, entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{})
	}
	return sb.String()
}

// renderFullSurface renders the actual user-facing frame, so a lifecycle bug
// cannot hide behind an entry-level helper.
func renderFullSurface(t *testing.T, s State) string {
	t.Helper()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	return Render(s)
}

func describeEntries(s State) string {
	var parts []string
	for _, entry := range s.Entries {
		parts = append(parts, string(entry.Kind)+"{"+entry.AssistantText+"|"+entry.SupersededText+"}")
	}
	return strings.Join(parts, " ")
}

// findSuperseded returns the audit tombstone carrying text, failing the test
// when the state no longer represents it. It also proves the candidate is
// non-authoritative: it holds no answer text and renders nothing.
func findSuperseded(t *testing.T, s State, text string) (int, Entry) {
	t.Helper()
	for i, entry := range s.Entries {
		if entry.Kind == EntryAssistant && entry.Superseded && entry.SupersededText == text {
			if entry.AssistantText != "" || entry.Discarded || entry.Provisional {
				t.Fatalf("candidate %q leaked into the answer contract: %+v", text, entry)
			}
			if rendered := candidateText(entry); strings.TrimSpace(rendered) != "" {
				t.Fatalf("superseded candidate %q rendered as transcript content:\n%s", text, rendered)
			}
			return i, entry
		}
	}
	t.Fatalf("candidate %q disappeared from state; entries: %s", text, describeEntries(s))
	return -1, Entry{}
}

// emitCandidate streams a candidate and then has the correction decision
// supersede it, mirroring the runtime's disposition.
func emitCandidate(s *State, text string) {
	s.Apply(AssistantDelta{Text: text, Seq: 1})
	s.Apply(AssistantReconcile{Text: text, Answer: agent.AnswerSuperseded, Seq: 1})
}

// Regression 1: candidate A → superseded A → candidate B → committed B.
// The transcript must contain B, not A followed by B.
func TestSupersededCandidateIsWithdrawnAndLinkedToCommittedAnswer(t *testing.T) {
	const candidate = "The project is a Go service."
	const final = "The project is a Go service: cmd/ohgo is the entrypoint."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantDelta{Text: candidate, Seq: 1})
	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerSuperseded, Seq: 1})

	if got := s.AssistantText(); got != "" {
		t.Fatalf("superseded candidate entered the answer projection: %q", got)
	}
	if got := renderStateEntries(s); strings.Contains(got, candidate) {
		t.Fatalf("superseded candidate rendered as an answer:\n%s", got)
	}
	index, _ := findSuperseded(t, s, candidate)

	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	s.Apply(ToolFinished{ID: "c1", Text: "package main", Seq: 1, At: time.Now()})
	s.Apply(AssistantDelta{Text: final, Seq: 1})
	s.Apply(AssistantReconcile{Text: final, Answer: agent.AnswerCommitted, Seq: 1})
	s.Apply(AssistantDone{Seq: 1, At: time.Now(), Footer: &TurnFooter{Kind: FooterVerified, Detail: "evidence repaired"}})

	answerIndex := -1
	for i, entry := range s.Entries {
		if entry.Kind == EntryAssistant && entry.AssistantText == final {
			answerIndex = i
		}
	}
	if answerIndex < 0 {
		t.Fatalf("final answer not committed: %s", describeEntries(s))
	}
	entry := s.Entries[index]
	if entry.SupersededBy != s.Entries[answerIndex].ID {
		t.Fatalf("SupersededBy = %q, want the final answer %q", entry.SupersededBy, s.Entries[answerIndex].ID)
	}
	if got := s.AssistantText(); got != final {
		t.Fatalf("AssistantText = %q, want only the final answer", got)
	}

	rendered := renderStateEntries(s)
	if strings.Contains(rendered, candidate) {
		t.Fatalf("repaired candidate leaked into the transcript:\n%s", rendered)
	}
	if strings.Count(rendered, final) != 1 {
		t.Fatalf("authoritative answer count = %d, want exactly one:\n%s", strings.Count(rendered, final), rendered)
	}
	if strings.Contains(rendered, "candidate · superseded") {
		t.Fatalf("superseded candidate still claims a transcript label:\n%s", rendered)
	}

	full := renderFullSurface(t, s)
	if strings.Contains(full, candidate) {
		t.Fatalf("superseded candidate leaked into the rendered frame:\n%s", full)
	}
	if strings.Count(full, final) != 1 {
		t.Fatalf("rendered answer count = %d, want exactly one:\n%s", strings.Count(full, final), full)
	}
}

// Regression 2: a candidate still under review is buffered in state and never
// painted as prose. The telemetry line is the only visible sign that the
// runtime is verifying it, so a refused candidate cannot appear and vanish
// mid-read.
func TestActiveCandidateIsBufferedUntilCommit(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantDelta{Text: candidate, Seq: 1})

	if rendered := renderStateEntries(s); strings.Contains(rendered, candidate) {
		t.Fatalf("uncommitted candidate rendered as prose:\n%s", rendered)
	}
	if got := s.AssistantText(); got != "" {
		t.Fatalf("AssistantText = %q, want no answer before commit", got)
	}
	if status := liveStatusLine(s, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}); !strings.Contains(status, "verifying evidence…") {
		t.Fatalf("candidate review status missing from the telemetry line: %q", status)
	}
	if frame := renderFullSurface(t, s); strings.Contains(frame, candidate) || !strings.Contains(frame, "verifying evidence…") {
		t.Fatalf("full-screen frame did not buffer the candidate and name the review:\n%s", frame)
	}
	entry := s.Entries[len(s.Entries)-1]
	if !entry.Provisional || entry.Superseded || entry.Discarded {
		t.Fatalf("active candidate lifecycle = %+v", entry)
	}
	if !strings.Contains(entry.AssistantText, candidate) {
		t.Fatalf("candidate text was not buffered: %+v", entry)
	}
	if got := BlockCopyText(entry); got != "" {
		t.Fatalf("buffered candidate copied as an answer: %q", got)
	}
}

// Regression 3: a candidate committed without supersession remains the answer.
func TestCommittedCandidateWithoutSupersessionRemainsVisible(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantDelta{Text: candidate, Seq: 1})
	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerCommitted, Seq: 1})

	rendered := renderStateEntries(s)
	if strings.Count(rendered, candidate) != 1 {
		t.Fatalf("committed answer count = %d, want one:\n%s", strings.Count(rendered, candidate), rendered)
	}
	if strings.Contains(rendered, "draft ·") {
		t.Fatalf("committed answer kept a provisional label:\n%s", rendered)
	}
	if got := s.AssistantText(); got != candidate {
		t.Fatalf("AssistantText = %q, want the committed candidate", got)
	}
	for _, entry := range s.Entries {
		if entry.Kind == EntryAssistant && entry.Superseded {
			t.Fatalf("an unsuperseded candidate became an audit tombstone: %+v", entry)
		}
	}
}

// Regression 4: independent assistant responses are not replacements for one
// another and must all remain visible. This proves the fix is lineage-aware,
// not a global answer suppression or text deduplication.
func TestMultipleLegitimateAssistantResponsesRemainVisible(t *testing.T) {
	const first = "The project is a Go service."
	const second = "It has a terminal UI and a one-shot CLI."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantDelta{Text: first, Seq: 1})
	s.Apply(AssistantReconcile{Text: first, Answer: agent.AnswerCommitted, Seq: 1})
	s.Apply(AssistantDone{Seq: 1, At: time.Now()})

	s.Apply(UserSubmitted{Text: "and its surfaces?", Seq: 2})
	s.Apply(AssistantDelta{Text: second, Seq: 2})
	s.Apply(AssistantReconcile{Text: second, Answer: agent.AnswerCommitted, Seq: 2})
	s.Apply(AssistantDone{Seq: 2, At: time.Now()})

	rendered := renderStateEntries(s)
	for _, answer := range []string{first, second} {
		if strings.Count(rendered, answer) != 1 {
			t.Fatalf("legitimate answer %q count = %d, want one:\n%s", answer, strings.Count(rendered, answer), rendered)
		}
	}
	if got := s.AssistantText(); !strings.Contains(got, first) || !strings.Contains(got, second) {
		t.Fatalf("answer projection dropped a legitimate response: %q", got)
	}
}

// Regression 5: the real repair shape — answer A, grounding audit, supersede,
// verification/tool work, answer B, final. Only B is authoritative.
func TestSupersededCandidateDoesNotSurviveGroundingRepair(t *testing.T) {
	const candidate = "The project is a Go service with a CLI."
	const final = "The project is a Go service; cmd/ohgo is the CLI entrypoint."
	at := time.Now()
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(WorkspaceUpdated{Workspace: "FUZZ", HasWorkspace: true})
	s.Apply(ToolStarted{ID: "c1", Name: "structure", Seq: 1, At: at})
	s.Apply(ToolFinished{ID: "c1", Text: "internal/ cmd/", Seq: 1, At: at})

	s.Apply(AssistantDelta{Text: candidate, Seq: 1})
	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerSuperseded, Seq: 1})
	index, _ := findSuperseded(t, s, candidate)
	if got := renderStateEntries(s); strings.Contains(got, candidate) {
		t.Fatalf("superseded candidate is visible during repair:\n%s", got)
	}

	s.Apply(ReasoningDelta{Text: "The CLI claim needs a source read.", Seq: 1, At: at})
	s.Apply(ToolStarted{ID: "c2", Name: "read", Seq: 1, At: at})
	s.Apply(ToolFinished{ID: "c2", Text: "func main()", Seq: 1, At: at})
	if got := renderStateEntries(s); strings.Contains(got, candidate) {
		t.Fatalf("superseded candidate reappeared after tool work:\n%s", got)
	}

	s.Apply(AssistantDelta{Text: final, Seq: 1})
	s.Apply(AssistantReconcile{Text: final, Answer: agent.AnswerCommitted, Seq: 1})
	s.Apply(AssistantDone{Seq: 1, At: at, Footer: &TurnFooter{Kind: FooterVerified, Detail: "evidence repaired"}})

	entry := s.Entries[index]
	if entry.SupersededBy == "" {
		t.Fatal("the committed answer did not link the superseded candidate")
	}
	rendered := renderStateEntries(s)
	if strings.Contains(rendered, candidate) {
		t.Fatalf("superseded candidate survived the repair:\n%s", rendered)
	}
	if strings.Count(rendered, final) != 1 {
		t.Fatalf("authoritative answer count = %d, want exactly one:\n%s", strings.Count(rendered, final), rendered)
	}
	if got := s.AssistantText(); got != final {
		t.Fatalf("AssistantText = %q, want only the repaired answer", got)
	}
}

// A completed transport never streamed a preview. Supersession has nothing to
// withdraw, so the reducer must not invent a transcript artifact for it.
func TestSupersededCandidateWithoutPreviewLeavesNoArtifact(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerSuperseded, Seq: 1})

	for _, entry := range s.Entries {
		if entry.Kind == EntryAssistant {
			t.Fatalf("supersession invented an assistant artifact: %+v", entry)
		}
	}
	if got := s.AssistantText(); got != "" {
		t.Fatalf("AssistantText = %q, want no answer", got)
	}
	if got := renderStateEntries(s); strings.Contains(got, candidate) {
		t.Fatalf("non-streamed superseded candidate rendered:\n%s", got)
	}
}

// A superseded candidate is audit-only, not copyable text.
func TestSupersededCandidateIsNotCopyableAsAnswer(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe", Seq: 1})
	emitCandidate(&s, candidate)

	_, entry := findSuperseded(t, s, candidate)
	if got := BlockCopyText(entry); strings.TrimSpace(got) != "" {
		t.Fatalf("superseded candidate copied as an answer: %q", got)
	}
}

// An unresolved candidate that is superseded and then fails must leave no
// answer. The tombstone keeps the audit fact but the transcript stays empty.
func TestSupersededCandidateFromFailedInvestigationLeavesNoAnswer(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, candidate)
	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	s.Apply(ToolFinished{ID: "c1", Text: "error", IsError: true, Seq: 1, At: time.Now()})
	s.Apply(TurnCancelled{Seq: 1, At: time.Now()})

	_, entry := findSuperseded(t, s, candidate)
	if entry.SupersededBy != "" {
		t.Fatalf("unresolved candidate claims a final answer %q", entry.SupersededBy)
	}
	if got := s.AssistantText(); got != "" {
		t.Fatalf("AssistantText = %q, want no answer from a failed investigation", got)
	}
	if got := renderStateEntries(s); strings.Contains(got, candidate) {
		t.Fatalf("failed investigation rendered its superseded candidate:\n%s", got)
	}
}

// A superseded candidate from an earlier turn is never linked to a later
// turn's answer.
func TestSupersededCandidateFromEarlierTurnIsNotLinkedToLaterAnswer(t *testing.T) {
	const earlier = "The project is a Go service."
	const later = "It is a Go service with one binary."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, earlier)
	s.Apply(AssistantDone{Seq: 1, At: time.Now()})

	s.Apply(UserSubmitted{Text: "and the binaries?", Seq: 2})
	s.Apply(AssistantDelta{Text: later, Seq: 2})
	s.Apply(AssistantReconcile{Text: later, Answer: agent.AnswerCommitted, Seq: 2})

	_, entry := findSuperseded(t, s, earlier)
	if entry.SupersededBy != "" {
		t.Fatalf("earlier turn's candidate was linked to a later turn's answer %q", entry.SupersededBy)
	}
	if got := s.AssistantText(); got != later {
		t.Fatalf("AssistantText = %q, want the later committed answer", got)
	}
}

// renderStateEntriesAt renders the transcript with a controlled presentation
// clock, so a reveal can be asserted without running a terminal.
func renderStateEntriesAt(s State, now time.Time) string {
	var sb strings.Builder
	for _, entry := range s.Entries {
		renderEntryContent(&sb, entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{now: now})
	}
	return sb.String()
}

// A committed answer is buffered until publication, then revealed word by word
// instead of appearing as one unreadable block. The full text remains the
// answer contract for copy and persistence from the first frame.
func TestCommittedAnswerRevealsProgressively(t *testing.T) {
	start := time.Date(2026, 11, 28, 1, 7, 0, 0, time.UTC)
	answer := strings.Repeat("The project is a Go service with a grounded answer. ", 60) + "ANSWER_END"
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1, At: start})
	s.Apply(AssistantDelta{Text: answer, Seq: 1, At: start})
	s.Apply(AssistantReconcile{Text: answer, Answer: agent.AnswerCommitted, Seq: 1, At: start})

	entry := s.Entries[len(s.Entries)-1]
	if entry.AssistantText != answer {
		t.Fatalf("settled answer contract = %q, want the full text", entry.AssistantText)
	}
	if got := BlockCopyText(entry); got != answer {
		t.Fatalf("committed answer copy = %q, want the full text", got)
	}

	partialAt := start.Add(100 * time.Millisecond)
	s.Apply(FrameTick{At: partialAt})
	partial := renderStateEntriesAt(s, partialAt)
	if !strings.Contains(partial, "The project") {
		t.Fatalf("reveal did not start:\n%s", partial)
	}
	if strings.Contains(partial, "ANSWER_END") {
		t.Fatalf("answer appeared as one block instead of revealing:\n%s", partial)
	}

	doneAt := start.Add(4 * time.Second)
	s.Apply(FrameTick{At: doneAt})
	done := renderStateEntriesAt(s, doneAt)
	if !strings.Contains(done, "ANSWER_END") {
		t.Fatalf("reveal never reached the end:\n%s", done)
	}
}

// A rejected draft keeps the same progressive reveal. The user must see the
// draft being written and then the review label, not a finished wall of text
// that appears after a long "formatting response".
func TestWithdrawnDraftRevealsProgressively(t *testing.T) {
	start := time.Date(2026, 11, 28, 1, 11, 0, 0, time.UTC)
	draft := strings.Repeat("This draft explains the project from observed evidence. ", 40) + "DRAFT_END"
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1, At: start})
	s.Apply(AssistantDelta{Text: draft, Seq: 1, At: start})
	s.Apply(AssistantReconcile{
		Text: draft, Answer: agent.AnswerRejected, Reason: "needs review", Seq: 1, At: start,
	})

	entry := s.Entries[len(s.Entries)-1]
	if !entry.Discarded || entry.WithdrawnText != draft {
		t.Fatalf("withdrawn draft contract = %+v, want the full draft retained", entry)
	}
	if entry.AssistantText != "" {
		t.Fatalf("withdrawn draft entered the answer contract: %q", entry.AssistantText)
	}

	partialAt := start.Add(100 * time.Millisecond)
	s.Apply(FrameTick{At: partialAt})
	partial := renderStateEntriesAt(s, partialAt)
	if !strings.Contains(partial, "draft withdrawn") {
		t.Fatalf("withdrawn draft lost its review label:\n%s", partial)
	}
	if !strings.Contains(partial, "This draft") || strings.Contains(partial, "DRAFT_END") {
		t.Fatalf("withdrawn draft did not reveal progressively:\n%s", partial)
	}

	doneAt := start.Add(4 * time.Second)
	s.Apply(FrameTick{At: doneAt})
	done := renderStateEntriesAt(s, doneAt)
	if !strings.Contains(done, "DRAFT_END") {
		t.Fatalf("withdrawn draft reveal never reached the end:\n%s", done)
	}
}

// The retained full-screen layout must recompute while the answer reveal
// grows; otherwise the entry-level renderer types while the actual screen
// stays frozen at the first frame.
func TestRevealingAnswerRefreshesFullScreenLayout(t *testing.T) {
	start := time.Date(2026, 11, 28, 1, 11, 30, 0, time.UTC)
	answer := strings.Repeat("This is a progressive answer. ", 10) + "END"
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "describe", Seq: 1, At: start})
	s.Apply(AssistantDelta{Text: answer, Seq: 1, At: start})
	s.Apply(AssistantReconcile{Text: answer, Answer: agent.AnswerCommitted, Seq: 1, At: start})

	partialAt := start.Add(100 * time.Millisecond)
	s.Apply(FrameTick{At: partialAt})
	partial := Render(s)
	if strings.Contains(partial, "END") {
		t.Fatalf("full-screen layout showed the completed answer at the first frame:\n%s", partial)
	}

	doneAt := start.Add(4 * time.Second)
	s.Apply(FrameTick{At: doneAt})
	done := Render(s)
	if !strings.Contains(done, "END") {
		t.Fatalf("full-screen layout never picked up the reveal:\n%s", done)
	}
}

// TranscriptLayout keeps cached block heights. A settling answer is animated
// while it reveals, so the retained layout must keep re-rendering it until the
// last word is visible; otherwise the screen freezes after the entrance fade.
func TestSettledAnswerRevealRefreshesRetainedLayout(t *testing.T) {
	start := time.Date(2026, 11, 28, 1, 40, 0, 0, time.UTC)
	answer := strings.Repeat("A settled answer paragraph that wraps across the transcript. ", 60) + "LAYOUT_END"
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(FrameTick{At: start})
	s.Apply(UserSubmitted{Text: "describe", Seq: 1, At: start})
	s.Apply(AssistantDelta{Text: answer, Seq: 1, At: start})
	s.Apply(AssistantReconcile{Text: answer, Answer: agent.AnswerCommitted, Seq: 1, At: start})
	s.Apply(AssistantDone{At: start.Add(10 * time.Millisecond)})

	layout := &TranscriptLayout{}
	layoutText := func() string {
		rows := layout.AllRows()
		var b strings.Builder
		for _, row := range rows {
			b.WriteString(row.Plain())
			b.WriteByte('\n')
		}
		return b.String()
	}

	earlyAt := start.Add(100 * time.Millisecond)
	s.Apply(FrameTick{At: earlyAt})
	layout.Refresh(&s, ComputeLayout(s))
	early := layoutText()
	if !strings.Contains(early, "A settled answer") || strings.Contains(early, "LAYOUT_END") {
		t.Fatalf("early reveal did not render a partial answer:\n%s", early)
	}

	// The entrance fade is shorter than the reveal. Refresh after the fade has
	// expired so a layout that treats only the fade as animated freezes on the
	// prefix it rendered at this point.
	stableAt := start.Add(500 * time.Millisecond)
	s.Apply(FrameTick{At: stableAt})
	layout.Refresh(&s, ComputeLayout(s))
	stable := layoutText()
	if strings.Contains(stable, "LAYOUT_END") {
		t.Fatalf("reveal skipped ahead to the end:\n%s", stable)
	}

	lateAt := start.Add(1500 * time.Millisecond)
	s.Apply(FrameTick{At: lateAt})
	layout.Refresh(&s, ComputeLayout(s))
	late := layoutText()
	if late == stable {
		t.Fatalf("retained layout froze after the entrance fade:\n%s", late)
	}
	if strings.Contains(late, "LAYOUT_END") {
		t.Fatalf("reveal skipped ahead to the end:\n%s", late)
	}

	doneAt := start.Add(4 * time.Second)
	s.Apply(FrameTick{At: doneAt})
	layout.Refresh(&s, ComputeLayout(s))
	if done := layoutText(); !strings.Contains(done, "LAYOUT_END") {
		t.Fatalf("retained layout never reached the full answer:\n%s", done)
	}
}
