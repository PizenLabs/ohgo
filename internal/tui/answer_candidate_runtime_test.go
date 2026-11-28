package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

const runtimeCandidate = "The project is a Go service with a CLI."
const runtimeFinal = "The project is a Go service; cmd/ohgo is the CLI entrypoint."

// The real runtime path, not a hand-written event script: runTurn drives the
// agent loop, the evidence review supersedes the first candidate, the loop
// resumes with tool calls, and the final answer commits. Every event the
// program publishes is applied to the authoritative state in publication
// order. The candidate must be buffered while it streams, never painted, then
// withdrawn when the audit supersedes it; only the committed answer may remain.
func TestRuntimeEvidenceRepairWithdrawsSupersededCandidate(t *testing.T) {
	responses := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: runtimeCandidate}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "cmd/ohgo/main.go"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: runtimeFinal}}, StopReason: ai.StopEndTurn},
	}
	next := 0
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if next >= len(responses) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		response := responses[next]
		index := next
		next++
		if index == 0 || index == 2 {
			// The first candidate and the correction answer stream live, so
			// the TUI has a provisional preview to withdraw.
			return ai.NewTextStream(response.Text()), nil
		}
		return ai.NewCompletedStream(response), nil
	}
	read := &orderReadTool{}
	loop, err := agent.New(agent.Config{
		Model:                   ai.Model{ID: "m", Provider: "p"},
		Stream:                  stream,
		Tools:                   []agent.Tool{read},
		CompletionReviewRequest: "Runtime completion review: {{reason}}. Correct the answer or state the limitation.",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	// The control plane's frontier stays open until the investigation has
	// read the project. runTurn installs the review from the prompt's intent,
	// so the gap is the same signal the live program reports.
	p.task.ProjectionReview = func() string {
		if read.calls == 0 {
			return "the project structure has not been read"
		}
		return ""
	}
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("describe the project") }()

	sawActive := false
	sawSuperseded := false
	apply := func(event Event) {
		p.state.Apply(event)
		if hasActiveCandidate(p.state, runtimeCandidate) {
			sawActive = true
			var painted strings.Builder
			for _, entry := range p.state.Entries {
				renderEntryContent(&painted, entry, PaletteFor(""), Layout{Width: 100, ContentWidth: 100}, entryRenderContext{})
			}
			if strings.Contains(painted.String(), runtimeCandidate) {
				t.Fatalf("buffered candidate painted before the publication decision after %T:\n%s", event, painted.String())
			}
		}
		if reconcile, ok := event.(AssistantReconcile); ok && reconcile.Answer == agent.AnswerSuperseded {
			sawSuperseded = true
			if hasActiveCandidate(p.state, runtimeCandidate) {
				t.Fatalf("superseded candidate is still active after %T: %s", event, describeEntries(p.state))
			}
		}
		if sawSuperseded && hasActiveCandidate(p.state, runtimeCandidate) {
			t.Fatalf("superseded candidate reappeared after %T: %s", event, describeEntries(p.state))
		}
	}
	timeout := time.After(5 * time.Second)
	for finished := false; !finished; {
		select {
		case event := <-p.events:
			apply(event)
		case <-done:
			finished = true
		case <-timeout:
			t.Fatal("runTurn hung")
		}
	}
	for drained := false; !drained; {
		select {
		case event := <-p.events:
			apply(event)
		default:
			drained = true
		}
	}

	if !sawActive {
		t.Fatalf("the active candidate never reached the TUI state: %s", describeEntries(p.state))
	}
	if !sawSuperseded {
		t.Fatal("the runtime never published the supersession decision")
	}
	index, entry := findSuperseded(t, p.state, runtimeCandidate)
	if entry.SupersededBy == "" {
		t.Fatalf("the final answer did not supersede the candidate at %d", index)
	}
	if got := p.state.AssistantText(); got != runtimeFinal {
		t.Fatalf("AssistantText = %q, want only the committed answer", got)
	}

	var transcript strings.Builder
	for _, entry := range p.state.Entries {
		renderEntryContent(&transcript, entry, PaletteFor(""), Layout{Width: 100, ContentWidth: 100}, entryRenderContext{})
	}
	rendered := transcript.String()
	if strings.Contains(rendered, runtimeCandidate) {
		t.Fatalf("the superseded candidate leaked into the transcript:\n%s", rendered)
	}
	if strings.Contains(rendered, "candidate · superseded") {
		t.Fatalf("the withdrawn candidate still claims a transcript label:\n%s", rendered)
	}
	if strings.Count(rendered, runtimeFinal) != 1 {
		t.Fatalf("authoritative answer count = %d, want exactly one:\n%s", strings.Count(rendered, runtimeFinal), rendered)
	}
}

func hasActiveCandidate(s State, text string) bool {
	for _, entry := range s.Entries {
		if entry.Kind != EntryAssistant || entry.Discarded || entry.Superseded || !entry.Provisional {
			continue
		}
		if strings.Contains(entry.AssistantText, text) {
			return true
		}
	}
	return false
}
