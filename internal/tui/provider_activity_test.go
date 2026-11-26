package tui

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

type tuiStatusErr struct{ status int }

func (e tuiStatusErr) Error() string   { return "provider rejected" }
func (e tuiStatusErr) StatusCode() int { return e.status }

// The loop's retry report must reach the TUI as a state event before the
// backoff sleep. Otherwise the bar keeps claiming workspace preparation for
// the entire rate-limit window.
func TestRunTurnPublishesProviderRetryBeforeBackoff(t *testing.T) {
	attempts := 0
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			attempts++
			if attempts == 1 {
				return nil, tuiStatusErr{status: 429}
			}
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
		Retry: agent.RetryPolicy{Attempts: 3, Base: 5 * time.Millisecond, Max: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("hello")

	var retry, streaming *ProviderStatusEvent
	for {
		select {
		case event := <-p.events:
			if status, ok := event.(ProviderStatusEvent); ok {
				copy := status
				switch status.Phase {
				case "retrying":
					retry = &copy
				case "streaming":
					streaming = &copy
				}
			}
		default:
			if retry == nil || streaming == nil {
				t.Fatalf("provider events = retry %+v streaming %+v, want both", retry, streaming)
			}
			if retry.Status != 429 || retry.Delay <= 0 || retry.Attempt != 1 || retry.Attempts != 3 {
				t.Fatalf("retry event = %+v, want 429 attempt 1/3 with a delay", retry)
			}
			return
		}
	}
}

// The retry hook must not swallow the terminal error: the turn still fails, so
// a terminal 429 cannot be mistaken for a completed answer.
func TestProviderRetryExhaustionStillEndsTheTurn(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return nil, tuiStatusErr{status: 429}
		},
		Tools: []agent.Tool{&idleTool{}},
		Retry: agent.RetryPolicy{Attempts: 2, Base: time.Millisecond, Max: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	p.runTurn("hello")
	var failed bool
	for {
		select {
		case event := <-p.events:
			if status, ok := event.(ProviderStatusEvent); ok && status.Phase == "failed" && status.Status == 429 {
				failed = true
			}
		default:
			if !failed {
				t.Fatal("no terminal provider failure event")
			}
			return
		}
	}
}
