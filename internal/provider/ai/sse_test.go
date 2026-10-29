package ai

import (
	"context"
	"strings"
	"testing"
)

// TestReadSSEIgnoresReconnectionFields proves id: and retry: lines never
// disturb decoding: they govern reconnection, not events, so a reconnection
// line between data lines must neither flush nor contribute payload. Without
// this, a provider that sends ids would split one event into two.
func TestReadSSEIgnoresReconnectionFields(t *testing.T) {
	body := ": keep-alive\n" +
		"event: message\n" +
		"data: {\"a\":1}\n" +
		"id: 42\n" +
		"retry: 3000\n" +
		"data: {\"b\":2}\n" +
		"\n"
	var events []sseEvent
	if err := readSSE(context.Background(), strings.NewReader(body), func(event sseEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one joined event", events)
	}
	got := events[0]
	if got.Name != "message" || got.Data != "{\"a\":1}\n{\"b\":2}" {
		t.Errorf("event = %+v, want the name kept and both data lines joined", got)
	}
}
