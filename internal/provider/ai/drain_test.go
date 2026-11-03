package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDrainStreamForwardsDeltasAndAssembles pins the shared drain contract.
//
// Both the agent loop and the $sup answer surface consume provider streams
// through DrainStream, so a drift here silently breaks streaming in both. The
// deltas a consumer sees must be the ones the provider emitted, in order, and
// the returned message must be the assembled one.
func TestDrainStreamForwardsDeltasAndAssembles(t *testing.T) {
	stream := NewTextStream("Hel", "lo, ", "world")

	var got []string
	msg, err := DrainStream(context.Background(), stream, func(delta string) {
		got = append(got, delta)
	})
	if err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if len(got) != 3 || got[0] != "Hel" || got[1] != "lo, " || got[2] != "world" {
		t.Errorf("deltas = %v, want [Hel lo,  world] in order", got)
	}
	if msg.Text() != "Hello, world" {
		t.Errorf("message = %q, want the assembled text", msg.Text())
	}
}

// TestDrainStreamHooksKeepsReasoningOutOfAssistantText pins the two-channel
// contract: reasoning travels to its own hook and never becomes visible answer
// text, while the assembled message keeps both blocks.
func TestDrainStreamHooksKeepsReasoningOutOfAssistantText(t *testing.T) {
	stream := newStream(4)
	ctx := context.Background()
	stream.publish(ctx, EventThinkingDelta{Delta: "plan "})
	stream.publish(ctx, EventThinkingDelta{Delta: "the work"})
	stream.publish(ctx, EventTextDelta{Delta: "answer"})
	message := AssistantMessage{Content: []ContentBlock{
		ThinkingContent{Thinking: "plan the work"},
		TextContent{Text: "answer"},
	}}
	stream.publish(ctx, EventDone{Message: message})
	stream.finish(message, nil)

	var text, thinking []string
	got, err := DrainStreamHooks(ctx, stream, DrainHooks{
		OnText:     func(delta string) { text = append(text, delta) },
		OnThinking: func(delta string) { thinking = append(thinking, delta) },
	})
	if err != nil {
		t.Fatalf("DrainStreamHooks: %v", err)
	}
	if strings.Join(thinking, "") != "plan the work" {
		t.Errorf("thinking deltas = %v, want the reasoning channel only", thinking)
	}
	if strings.Join(text, "") != "answer" {
		t.Errorf("text deltas = %v, want the answer channel only", text)
	}
	if got.Text() != "answer" || got.ThinkingText() != "plan the work" {
		t.Errorf("assembled message = text %q thinking %q", got.Text(), got.ThinkingText())
	}
}

// TestDrainStreamRejectsANilStream proves the shared path fails loudly instead
// of nil-panicking inside a caller.
func TestDrainStreamRejectsANilStream(t *testing.T) {
	if _, err := DrainStream(context.Background(), nil, nil); err == nil {
		t.Fatal("DrainStream(nil) = nil error, want a refusal")
	}
}

// TestDrainStreamHonoursCancellation proves a cancelled context stops the drain
// and surfaces the cancellation, which is how Ctrl+C interrupts a $sup turn.
func TestDrainStreamHonoursCancellation(t *testing.T) {
	stream := newStream(2)
	stream.publish(context.Background(), EventStart{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DrainStream(ctx, stream, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("DrainStream err = %v, want context.Canceled", err)
	}
}
