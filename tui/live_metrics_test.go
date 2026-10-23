package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
)

// Live download and context % must grow while the turn streams, not only at
// AssistantDone. The provider only reports measured usage at completion, so
// the footer derives the in-flight estimate from OutputTokens (the same
// estimate the activity line already shows) on every AssistantDelta.
func TestBusyTelemetryGrowsDuringStreaming(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Status.ContextWindow = 1000
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	// Seed provisional upload the way runTurn does: cumulative input estimate
	// visible while Responding, corrected to measured at the boundary.
	s.Apply(SessionMetricsUpdated{Input: 200, Output: 0})
	s.Apply(ContextUpdated{Used: 200, Window: 1000})

	before := busyTelemetry(s)
	if !strings.Contains(before, "↑200") {
		t.Fatalf("busy telemetry = %q, want provisional upload visible while streaming", before)
	}
	// First chunk: download appears and % lifts off the seeded baseline.
	s.Apply(AssistantDelta{Text: strings.Repeat("x", 400)})
	mid := busyTelemetry(s)
	if !strings.Contains(mid, "↓") {
		t.Fatalf("busy telemetry = %q, want live download while streaming", mid)
	}
	midPct := livePercent(s)
	if midPct <= 2 {
		t.Fatalf("live percent = %d, want growth from in-flight output", midPct)
	}
	// Second chunk: values must increase incrementally, not jump only at done.
	s.Apply(AssistantDelta{Text: strings.Repeat("y", 400)})
	after := busyTelemetry(s)
	if after == mid {
		t.Fatalf("busy telemetry did not update incrementally:\nmid=%q\nafter=%q", mid, after)
	}
	if got := livePercent(s); got <= midPct {
		t.Fatalf("live percent = %d, want > %d after more streamed text", got, midPct)
	}
}

// The busy hint row keeps live metrics alongside the cancel keys, but the
// session marker still yields until the turn ends (existing chrome contract).
func TestBusyHintRowKeepsLiveMetricsWithoutSessionMarker(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Status.ContextWindow = 1000
	s.Status.Session = "#9"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(SessionMetricsUpdated{Input: 200, Output: 0})
	s.Apply(AssistantDelta{Text: strings.Repeat("x", 400)})

	row := composerHintsFor(s)
	if !strings.Contains(row, "↓") || !strings.Contains(row, "esc cancel") {
		t.Fatalf("busy hint row = %q, want live download plus cancel keys", row)
	}
	if strings.Contains(row, "#9") {
		t.Fatalf("busy hint row = %q, want no session marker while busy", row)
	}
	if got := composerHintBar(s, 80); !strings.Contains(got, "↓") || !strings.Contains(got, "esc cancel") {
		t.Fatalf("busy hint bar = %q, want live metrics plus cancel keys", got)
	}
}

// The live-mode status line must show the same incrementally updating
// upload/download and context % while streaming.
func TestLiveStatusLineShowsLiveTokensWhileBusy(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Status.ContextWindow = 1000
	s.Status.Cwd = "/work/proj"
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(SessionMetricsUpdated{Input: 200, Output: 0})
	s.Apply(ContextUpdated{Used: 200, Window: 1000})
	s.Apply(AssistantDelta{Text: strings.Repeat("x", 400)})

	out := liveStatusLine(s, PaletteFor(s.Theme), ComputeLayout(s))
	if !strings.Contains(out, "↑200") || !strings.Contains(out, "↓") {
		t.Fatalf("live status = %q, want live upload/download while streaming", out)
	}
	if !strings.Contains(out, "ctx ") {
		t.Fatalf("live status = %q, want live context %% while streaming", out)
	}
}

// The live-mode status line orients with branch plus compact worktree state,
// matching the full-screen hint bar.
func TestLiveStatusLineShowsGitStatus(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Status.Cwd = "/work/proj"
	s.Status.GitBranch = "main"
	s.Status.GitKnown = true
	s.Status.GitModified = 6
	s.Status.GitUntracked = 1

	out := liveStatusLine(s, PaletteFor(s.Theme), ComputeLayout(s))
	if !strings.Contains(out, "main") || !strings.Contains(out, "!6 ?1") {
		t.Fatalf("live status = %q, want branch plus compact git status", out)
	}
}

// estimateTurnInput must approximate the request so the provisional upload
// is visible at Responding start; measured totals correct it at the boundary.
func TestEstimateTurnInputCountsRequest(t *testing.T) {
	req := []agent.Message{
		agent.UserMessage{Text: strings.Repeat("hello ", 100)},
	}
	if got := estimateTurnInput(req, nil); got <= 0 {
		t.Fatalf("estimate = %d, want positive for non-empty request", got)
	}
	empty := estimateTurnInput(nil, nil)
	base := estimateTurnInput(req, nil)
	if base <= empty {
		t.Fatalf("estimate non-empty = %d, want > empty %d", base, empty)
	}
	blocks := []ai.SystemPromptBlock{{Text: strings.Repeat("sys ", 100)}}
	withSys := estimateTurnInput(req, blocks)
	if withSys <= base {
		t.Fatalf("estimate with system = %d, want > base %d", withSys, base)
	}
}

// runTurn must seed the upload estimate at Responding start and report the
// measured totals before the turn ends, so the statusline moves during the
// run instead of waiting for the terminal completion hook.
func TestRunTurnSeedsLiveMetricsBeforeDone(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "hello"}},
				Usage:   ai.Usage{Input: 120, Output: 30, Total: 150},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 1000)
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("what is golang") }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn hung")
	}
	// All events were published before runTurn returned (Done is last), so
	// draining after done is deterministic: no select race between the
	// event channel and the done channel can reorder them.
	var events []Event
drain:
	for {
		select {
		case e := <-p.events:
			events = append(events, e)
			p.state.Apply(e)
		default:
			break drain
		}
	}
	firstMetrics, firstContext, firstDone := -1, -1, -1
	for i, e := range events {
		switch v := e.(type) {
		case SessionMetricsUpdated:
			if firstMetrics < 0 && v.Input > 0 {
				firstMetrics = i
			}
		case ContextUpdated:
			if firstContext < 0 && v.Used > 0 {
				firstContext = i
			}
		case AssistantDone:
			if firstDone < 0 {
				firstDone = i
			}
		}
	}
	if firstDone < 0 {
		t.Fatalf("no AssistantDone among %d events", len(events))
	}
	if firstMetrics < 0 || firstMetrics > firstDone {
		t.Errorf("no live SessionMetricsUpdated before AssistantDone among %d events", len(events))
	}
	if firstContext < 0 || firstContext > firstDone {
		t.Errorf("no live ContextUpdated before AssistantDone among %d events", len(events))
	}
	if p.state.SessionIn <= 0 || p.state.SessionOut <= 0 {
		t.Errorf("session totals = in %d out %d, want measured totals after run", p.state.SessionIn, p.state.SessionOut)
	}
}

// A streaming turn with no measured usage must still seed a provisional
// upload while Responding (so ↑ appears immediately) and then revert it,
// so unknown stays absent rather than surviving as a fabricated total.
func TestRunTurnProvisionalRevertsWhenUnmeasured(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewTextStream("hello ", "world"), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 1000)
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("streaming with no usage") }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn hung")
	}
	var events []Event
drain:
	for {
		select {
		case e := <-p.events:
			events = append(events, e)
			p.state.Apply(e)
		default:
			break drain
		}
	}
	sawProvisional := false
	for _, e := range events {
		if metrics, ok := e.(SessionMetricsUpdated); ok && metrics.Input > 0 {
			sawProvisional = true
			break
		}
	}
	if !sawProvisional {
		t.Error("no provisional SessionMetricsUpdated while streaming: upload would stay hidden until completion")
	}
	if p.state.SessionIn != 0 || p.state.SessionOut != 0 {
		t.Errorf("session totals = in %d out %d after unmeasured turn, want 0 (unknown stays absent)", p.state.SessionIn, p.state.SessionOut)
	}
}
