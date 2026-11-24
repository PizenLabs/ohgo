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

// findCandidate returns the superseded candidate carrying text, failing the
// test when the state no longer represents it. It also proves the candidate is
// display-only: it must never be projected as the answer.
func findCandidate(t *testing.T, s State, text string) (int, Entry) {
	t.Helper()
	for i, entry := range s.Entries {
		if entry.Kind == EntryAssistant && entry.Superseded && entry.SupersededText == text {
			if entry.AssistantText != "" || entry.Discarded || entry.Provisional {
				t.Fatalf("candidate %q leaked into the answer contract: %+v", text, entry)
			}
			if !strings.Contains(candidateText(entry), text) {
				t.Fatalf("candidate %q is not visible in the transcript:\n%s", text, candidateText(entry))
			}
			return i, entry
		}
	}
	t.Fatalf("candidate %q disappeared from state; entries: %s", text, describeEntries(s))
	return -1, Entry{}
}

func describeEntries(s State) string {
	var parts []string
	for _, entry := range s.Entries {
		parts = append(parts, string(entry.Kind)+"{"+entry.AssistantText+"|"+entry.SupersededText+"}")
	}
	return strings.Join(parts, " ")
}

// emitCandidate streams a candidate and then has the correction decision
// replace it, mirroring the runtime's superseded disposition.
func emitCandidate(s *State, text string) {
	s.Apply(AssistantDelta{Text: text, Seq: 1})
	s.Apply(AssistantReconcile{Text: text, Answer: agent.AnswerSuperseded, Seq: 1})
}

func TestAnswerCandidateEmittedIsRepresented(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantDelta{Text: "The project is a Go service.", Seq: 1})
	if open := s.lastProvisionalAssistant(); open < 0 {
		t.Fatal("streamed candidate is not represented as a provisional entry")
	}
	s.Apply(AssistantReconcile{Text: "The project is a Go service.", Answer: agent.AnswerSuperseded, Seq: 1})
	findCandidate(t, s, "The project is a Go service.")
}

func TestAnswerCandidateSurvivesToolStart(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, "The project is a Go service.")
	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	findCandidate(t, s, "The project is a Go service.")
}

func TestAnswerCandidateSurvivesPhaseChange(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, "The project is a Go service.")
	s.Apply(WorkspaceUpdated{Workspace: "FUZZ", HasWorkspace: true})
	findCandidate(t, s, "The project is a Go service.")
}

func TestAnswerCandidateSurvivesReasoningResume(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, "The project is a Go service.")
	s.Apply(ReasoningDelta{Text: "The cmd claim lacks evidence.", Seq: 1, At: time.Now()})
	findCandidate(t, s, "The project is a Go service.")
}

func TestAnswerCandidateSurvivesAdditionalEvidence(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, "The project is a Go service.")
	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	s.Apply(ToolFinished{ID: "c1", Text: "package main", Seq: 1, At: time.Now()})
	findCandidate(t, s, "The project is a Go service.")
}

func TestFinalAnswerExplicitlySupersedesCandidate(t *testing.T) {
	const candidate = "The project is a Go service."
	const final = "The project is a Go service: cmd/ohgo is the entrypoint."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, candidate)
	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	s.Apply(ToolFinished{ID: "c1", Text: "package main", Seq: 1, At: time.Now()})
	s.Apply(AssistantDelta{Text: final, Seq: 1})
	s.Apply(AssistantReconcile{Text: final, Answer: agent.AnswerCommitted, Seq: 1})

	_, entry := findCandidate(t, s, candidate)
	answerIndex := -1
	for i, e := range s.Entries {
		if e.Kind == EntryAssistant && e.AssistantText == final {
			answerIndex = i
		}
	}
	if answerIndex < 0 {
		t.Fatalf("final answer not committed: %s", describeEntries(s))
	}
	if want := s.Entries[answerIndex].ID; entry.SupersededBy != want {
		t.Fatalf("SupersededBy = %q, want the final answer %q", entry.SupersededBy, want)
	}
	if got := s.AssistantText(); got != final {
		t.Fatalf("AssistantText = %q, want only the final answer", got)
	}
}

func TestFailedInvestigationKeepsCandidateRepresented(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close Event
	}{
		{name: "cancelled", close: TurnCancelled{Seq: 1, At: time.Now()}},
		{name: "review footer", close: AssistantDone{Seq: 1, At: time.Now(), Footer: &TurnFooter{Kind: FooterReview, Detail: "evidence incomplete"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const candidate = "The project is a Go service."
			s := New()
			s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
			emitCandidate(&s, candidate)
			s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
			s.Apply(ToolFinished{ID: "c1", Text: "error", IsError: true, Seq: 1, At: time.Now()})
			s.Apply(tc.close)

			_, entry := findCandidate(t, s, candidate)
			if entry.SupersededBy != "" {
				t.Fatalf("unresolved candidate claims a final answer %q", entry.SupersededBy)
			}
			if got := s.AssistantText(); got != "" {
				t.Fatalf("AssistantText = %q, want no answer from a failed investigation", got)
			}
		})
	}
}

func TestRepeatedCandidatesAreAllRetained(t *testing.T) {
	const first = "The project is a Go service."
	const second = "The project is a Go service with a web UI."
	const final = "The project is a Go service with a terminal UI."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, first)
	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	s.Apply(ToolFinished{ID: "c1", Text: "package main", Seq: 1, At: time.Now()})
	emitCandidate(&s, second)
	findCandidate(t, s, first)
	s.Apply(ToolStarted{ID: "c2", Name: "bash", Seq: 1, At: time.Now()})
	s.Apply(ToolFinished{ID: "c2", Text: "ok", Seq: 1, At: time.Now()})
	s.Apply(AssistantDelta{Text: final, Seq: 1})
	s.Apply(AssistantReconcile{Text: final, Answer: agent.AnswerCommitted, Seq: 1})

	firstIndex, firstEntry := findCandidate(t, s, first)
	secondIndex, secondEntry := findCandidate(t, s, second)
	if firstIndex >= secondIndex {
		t.Fatalf("candidates reordered: first at %d, second at %d", firstIndex, secondIndex)
	}
	if firstEntry.SupersededBy == "" || secondEntry.SupersededBy == "" {
		t.Fatalf("candidates not linked to the final answer: first=%q second=%q",
			firstEntry.SupersededBy, secondEntry.SupersededBy)
	}
	if firstEntry.SupersededBy != secondEntry.SupersededBy {
		t.Fatalf("candidates linked to different answers: %q vs %q", firstEntry.SupersededBy, secondEntry.SupersededBy)
	}
}

func TestAssistantDoneWhileInvestigationActiveIsNotCompletion(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(WorkspaceUpdated{Workspace: "FUZZ", HasWorkspace: true})
	emitCandidate(&s, candidate)
	s.Apply(ToolStarted{ID: "c1", Name: "read", Seq: 1, At: time.Now()})
	s.Apply(AssistantDone{Seq: 1, At: time.Now(), Footer: &TurnFooter{Kind: FooterReview, Detail: "evidence incomplete"}})

	_, entry := findCandidate(t, s, candidate)
	if entry.SupersededBy != "" {
		t.Fatalf("AssistantDone linked the candidate to a final answer %q", entry.SupersededBy)
	}
	if got := s.AssistantText(); got != "" {
		t.Fatalf("AssistantDone promoted an answer: %q", got)
	}
}

func TestOnlyAuthoritativeCommitProducesAnAnswer(t *testing.T) {
	const candidate = "The project is a Go service."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(AssistantDelta{Text: candidate, Seq: 1})
	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerSuperseded, Seq: 1})
	s.Apply(AssistantDone{Seq: 1, At: time.Now()})
	if got := s.AssistantText(); got != "" {
		t.Fatalf("superseded candidate and AssistantDone produced an answer: %q", got)
	}
	findCandidate(t, s, candidate)

	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerCommitted, Seq: 1})
	if got := s.AssistantText(); got != candidate {
		t.Fatalf("committed AssistantText = %q, want the accepted answer", got)
	}
}

func TestCandidateFromEarlierTurnIsNotLinkedToLaterAnswer(t *testing.T) {
	const earlier = "The project is a Go service."
	const later = "It is a Go service with one binary."
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	emitCandidate(&s, earlier)
	s.Apply(AssistantDone{Seq: 1, At: time.Now()})

	s.Apply(UserSubmitted{Text: "and the binaries?", Seq: 2})
	s.Apply(AssistantDelta{Text: later, Seq: 2})
	s.Apply(AssistantReconcile{Text: later, Answer: agent.AnswerCommitted, Seq: 2})

	_, entry := findCandidate(t, s, earlier)
	if entry.SupersededBy != "" {
		t.Fatalf("earlier turn's candidate was linked to a later turn's answer %q", entry.SupersededBy)
	}
}

// The transcript this regression reproduces: FUZZ investigates, emits a
// candidate, evidence repair sends it back, FUZZ resumes and gathers more
// evidence, and a final answer commits. The first candidate must stay
// represented at every step of the second investigation phase.
func TestCandidateSurvivesEvidenceRepairLifecycle(t *testing.T) {
	const candidate = "The project is a Go service with a CLI."
	const final = "The project is a Go service; cmd/ohgo is the CLI entrypoint."
	at := time.Now()
	s := New()
	s.Apply(UserSubmitted{Text: "describe the project", Seq: 1})
	s.Apply(WorkspaceUpdated{Workspace: "FUZZ", HasWorkspace: true})
	s.Apply(ToolStarted{ID: "c1", Name: "structure", Seq: 1, At: at})
	s.Apply(ToolFinished{ID: "c1", Text: "internal/ cmd/", Seq: 1, At: at})
	s.Apply(ToolStarted{ID: "c2", Name: "ls", Seq: 1, At: at})
	s.Apply(ToolFinished{ID: "c2", Text: "go.mod README.md", Seq: 1, At: at})

	s.Apply(AssistantDelta{Text: candidate, Seq: 1})
	s.Apply(AssistantReconcile{Text: candidate, Answer: agent.AnswerSuperseded, Seq: 1})
	findCandidate(t, s, candidate)

	s.Apply(ReasoningDelta{Text: "The CLI claim needs a source read.", Seq: 1, At: at})
	s.Apply(ToolStarted{ID: "c3", Name: "structure", Seq: 1, At: at})
	findCandidate(t, s, candidate)
	s.Apply(ToolFinished{ID: "c3", Text: "cmd/ohgo", Seq: 1, At: at})
	findCandidate(t, s, candidate)
	s.Apply(ToolStarted{ID: "c4", Name: "bash", Seq: 1, At: at})
	s.Apply(ToolFinished{ID: "c4", Text: "ohgo", Seq: 1, At: at})
	findCandidate(t, s, candidate)
	s.Apply(ToolStarted{ID: "c5", Name: "read", Seq: 1, At: at})
	s.Apply(ToolFinished{ID: "c5", Text: "func main()", Seq: 1, At: at})
	findCandidate(t, s, candidate)

	s.Apply(AssistantDelta{Text: final, Seq: 1})
	s.Apply(AssistantReconcile{Text: final, Answer: agent.AnswerCommitted, Seq: 1})
	s.Apply(AssistantDone{Seq: 1, At: at, Footer: &TurnFooter{Kind: FooterVerified, Detail: "evidence repaired"}})

	_, entry := findCandidate(t, s, candidate)
	if entry.SupersededBy == "" {
		t.Fatal("the final answer did not explicitly supersede the candidate")
	}
	if got := s.AssistantText(); got != final {
		t.Fatalf("AssistantText = %q, want only the final answer", got)
	}
}
