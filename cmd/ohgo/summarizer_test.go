package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The context summarizer is a stream consumer like any other. It must drain
// Events and not wait on Result alone: a summary longer than the event buffer
// otherwise deadlocks the whole run, which is exactly what a large tool result
// triggers when it makes compaction necessary.
func TestSummarizerDrainsAStreamLongerThanTheEventBuffer(t *testing.T) {
	var body strings.Builder
	const chunks = 128
	for i := 0; i < chunks; i++ {
		body.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n")
	}
	body.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	body.WriteString("data: [DONE]\n\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, body.String())
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := ai.NewOpenAIProvider("sk-test")
	provider.BaseURL = server.URL
	streamFn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		stream, err := provider.Stream(ctx, req)
		if err != nil {
			return nil, err
		}
		return ai.NormalizeStream(ctx, stream, ai.NormalizeOptions{Provider: "test"}), nil
	}

	summarize := newSummarizer(streamFn, ai.Model{ID: "test", Provider: "test"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	summary, err := summarize(ctx, []agent.Message{agent.UserMessage{Text: "a long history"}})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("summarize took %s; the stream was not drained", elapsed)
	}
	if len(summary) != chunks {
		t.Fatalf("summary length = %d, want %d assembled deltas", len(summary), chunks)
	}
}
