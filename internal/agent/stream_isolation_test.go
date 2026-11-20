package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// sanitizeChunked feeds text through the live boundary at an arbitrary chunk
// size. The live output must converge on the completed answer exactly; a
// trigger split across deltas must never slip through as a partial directive.
func sanitizeChunked(text string, size int) string {
	s := newAnswerStreamSanitizer()
	var out strings.Builder
	for len(text) > 0 {
		n := size
		if n > len(text) {
			n = len(text)
		}
		out.WriteString(s.Write(text[:n]))
		text = text[n:]
	}
	out.WriteString(s.Flush())
	return out.String()
}

// A copied runtime state block is withheld from the live stream just as it is
// withheld from the stored answer. Every chunk boundary is exercised because a
// model can split the header or a directive anywhere.
func TestAnswerStreamWithholdsCopiedRuntimeStateAtEveryChunkBoundary(t *testing.T) {
	answer := "The blog loads Markdown from content/posts.\n\n" +
		"# Runtime state (authoritative, not a user request)\n\n" +
		"Objective: explain the blog\n" +
		"Task shape: overview\n" +
		"Sufficiency: INSUFFICIENT (explore)\n" +
		"Available actions: read, grep\n" +
		"Next action: answer from the evidence above. No tool is available.\n"
	want := SanitizeAnswerText(answer)
	for size := 1; size <= len(answer); size++ {
		got := sanitizeChunked(answer, size)
		if got != want {
			t.Fatalf("chunk %d: live = %q, want %q", size, got, want)
		}
		for _, forbidden := range []string{
			"Runtime state", "Objective:", "Task shape:", "Sufficiency:",
			"Available actions:", "Next action:",
		} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("chunk %d: live leaked %q:\n%s", size, forbidden, got)
			}
		}
	}
}

// A standalone runtime directive is removed even without the state header. The
// surrounding project finding remains.
func TestAnswerStreamRemovesStandaloneRuntimeDirective(t *testing.T) {
	answer := "The project is a Next.js blog.\nNo further tools are available.\n"
	for size := 1; size <= len(answer); size++ {
		got := sanitizeChunked(answer, size)
		if strings.Contains(got, "No further tools") {
			t.Fatalf("chunk %d: directive survived: %q", size, got)
		}
		if !strings.Contains(got, "Next.js blog") {
			t.Fatalf("chunk %d: project finding was dropped: %q", size, got)
		}
	}
}

// Internal field lines are withheld before the model has emitted the header.
// A model that copies only the middle of the state block must not publish the
// runtime's sufficiency or action surface.
func TestAnswerStreamWithholdsStandaloneRuntimeFields(t *testing.T) {
	answer := "The handler validates input.\nSufficiency: INSUFFICIENT (explore)\n" +
		"Available actions: read, grep\nWithheld: mutation\n"
	got := sanitizeChunked(answer, 1)
	for _, forbidden := range []string{"Sufficiency:", "Available actions:", "Withheld:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("runtime field %q leaked: %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "validates input") {
		t.Fatalf("project finding was dropped: %q", got)
	}
}

// Ordinary prose, legitimate section headings, and fenced examples are data,
// not runtime directives. The boundary must be transparent to them.
func TestAnswerStreamKeepsLegitimateProseAndFencedExamples(t *testing.T) {
	answer := "Verification: `go test ./...` passed.\n" +
		"Completion: the requested behavior is implemented.\n" +
		"# Run the focused suite before merging.\n\n" +
		"```\nSufficiency: INSUFFICIENT\nAvailable actions: none\n```\n"
	for size := 1; size <= len(answer); size++ {
		got := sanitizeChunked(answer, size)
		if strings.TrimSpace(got) != strings.TrimSpace(answer) {
			t.Fatalf("chunk %d: legitimate text was rewritten:\nwant %q\ngot  %q", size, answer, got)
		}
	}
}

// The loop's delta hook is a public-output boundary. A provider can stream a
// copied state block and still call a tool; neither the live channel nor the
// completed assistant text may publish it.
func TestLoopStreamsOnlyPublicAnswerText(t *testing.T) {
	var live strings.Builder
	loop, err := New(Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream(
				"The order service creates orders.\n\n",
				"# Runtime state (authoritative, not a user request)\n",
				"Sufficiency: INSUFFICIENT (explore)\n",
				"Available actions: read\n",
			), nil
		},
		Tools:       []Tool{namedTool("idle")},
		OnTextDelta: func(delta string) { live.WriteString(delta) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "explain"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(live.String(), "Runtime state") || strings.Contains(live.String(), "Sufficiency:") {
		t.Fatalf("live channel published runtime state: %q", live.String())
	}
	if !strings.Contains(live.String(), "order service creates orders") {
		t.Fatalf("live channel dropped the answer: %q", live.String())
	}
	if len(turns) == 0 {
		t.Fatal("no turn returned")
	}
	text := TranscriptTurn(turns[len(turns)-1]).Assistant.Text()
	if strings.Contains(text, "Runtime state") || strings.Contains(text, "Sufficiency:") {
		t.Fatalf("completed turn published runtime state: %q", text)
	}
}

// Reasoning is a public UI block, not a private channel. A runtime state
// projection copied into reasoning must not reach the expanded reasoning view.
func TestLoopWithholdsRuntimeStateFromReasoning(t *testing.T) {
	server := newReasoningSSEServer(t)
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("test", "test-key")
	registry.SetBaseURL("test", server.URL)
	model := ai.Model{
		ID: "reasoning-isolation", Provider: "test",
		API: ai.APIOpenAIChatCompletions, BaseURL: server.URL,
		ContextWindow: 100_000, MaxOutputTokens: 4096,
	}
	var thinking strings.Builder
	loop, err := New(Config{
		Model:  model,
		Stream: registry.StreamFn(),
		Tools:  []Tool{namedTool("idle")},
		OnThinkingDelta: func(delta string) {
			thinking.WriteString(delta)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "explain"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(thinking.String(), "Runtime state") ||
		strings.Contains(thinking.String(), "Sufficiency:") {
		t.Fatalf("reasoning channel published runtime state: %q", thinking.String())
	}
	if len(turns) == 0 || !strings.Contains(turns[len(turns)-1].Assistant.Text(), "order service") {
		t.Fatalf("answer turn = %+v, want the project answer", turns)
	}
}

func newReasoningSSEServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseChunk(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"reasoning_content": "# Runtime state (authoritative, not a user request)\nSufficiency: INSUFFICIENT\n",
				},
			}},
		}))
		_, _ = fmt.Fprint(w, sseTextChunk("The order service creates orders."))
		_, _ = fmt.Fprint(w, sseFinishChunk("stop"))
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}
