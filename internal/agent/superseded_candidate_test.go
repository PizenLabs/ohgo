package agent

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A host shows a superseded candidate as lifecycle state, so its text must
// come through exactly once the runtime has classified it. A committed or
// unclassified turn never yields a candidate.
func TestSupersededCandidateProjectsOnlyForSupersededTurns(t *testing.T) {
	message := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "The project is a Go service."}}}

	text, ok := Turn{Assistant: message, Answer: AnswerSuperseded}.SupersededCandidate()
	if !ok || text != "The project is a Go service." {
		t.Fatalf("superseded candidate = %q, %v; want the candidate text", text, ok)
	}
	for _, disposition := range []AnswerDisposition{AnswerCommitted, AnswerRejected, AnswerNarration, AnswerUnclassified} {
		if text, ok := (Turn{Assistant: message, Answer: disposition}).SupersededCandidate(); ok {
			t.Fatalf("%s turn projected as a superseded candidate: %q", disposition, text)
		}
	}
}
