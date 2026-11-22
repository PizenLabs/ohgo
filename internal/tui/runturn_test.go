package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

type orderReadTool struct{ calls int }

func (*orderReadTool) Name() string                   { return "read" }
func (*orderReadTool) Description() string            { return "read a file" }
func (*orderReadTool) Parameters() *jsonschema.Schema { return nil }
func (*orderReadTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierRead} }
func (*orderReadTool) Exclusive(map[string]any) bool  { return false }
func (t *orderReadTool) Execute(agent.Context, map[string]any) agent.Result {
	t.calls++
	return agent.Result{Text: "content"}
}

// A completed turn must leave its answer in the transcript: the tool hook
// carries tool output, but the model's own words only exist on the turns, so
// runTurn publishes each turn's text. Without this the screen shows the
// user's prompts with blank space where the answers belong.
func TestRunTurnPublishesAssistantText(t *testing.T) {
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "hello golang"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() { defer close(done); p.runTurn("what is golang") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn hung")
	}
	timeout := time.After(2 * time.Second)
	found := false
	for !found {
		select {
		case event := <-p.events:
			if delta, ok := event.(AssistantDelta); ok && delta.Text == "hello golang" {
				found = true
			}
			p.state.Apply(event)
		case <-timeout:
			t.Fatal("no AssistantDelta published: the UI would look idle after a success")
		}
	}
	if got := p.state.AssistantText(); got != "hello golang" {
		t.Fatalf("AssistantText = %q, want %q", got, "hello golang")
	}
}

// A reused observation produces a tool outcome without reaching Execute. Its
// start/finish pair must publish in its own turn; deferring it to the
// post-run pass replays an early read after the final answer.
func TestRunTurnPublishesReusedOutcomeBeforeFinalAnswer(t *testing.T) {
	tool := &orderReadTool{}
	index := 0
	script := []ai.AssistantMessage{
		{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "r1", Name: "read", Arguments: map[string]any{"path": "README.md"}}},
			StopReason: ai.StopToolUse,
		},
		{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "r2", Name: "read", Arguments: map[string]any{"path": "README.md"}}},
			StopReason: ai.StopToolUse,
		},
	}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			if index == len(script) {
				index++
				// A live provider streams the answer before the run ends. A
				// completed stream with no text deltas cannot expose the
				// post-answer replay this test guards against.
				return ai.NewTextStream("the answer"), nil
			}
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
		Tools: []agent.Tool{tool},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("analyze")
	}()

	reusedFinished := false
	answerSeen := false
	answerBeforeReused := false
	process := func(event Event) {
		switch e := event.(type) {
		case ToolFinished:
			if e.ID == "r2" {
				reusedFinished = true
			}
		case AssistantDelta:
			if strings.Contains(e.Text, "the answer") {
				answerSeen = true
				answerBeforeReused = answerBeforeReused || !reusedFinished
			}
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			process(event)
		case <-done:
			// runTurn publishes synchronously before returning, so any event
			// still in the buffer belongs to this turn. Drain it before
			// asserting; selecting on done must not skip a buffered outcome.
			for {
				select {
				case event := <-p.events:
					process(event)
				default:
					if !answerSeen {
						t.Fatal("the final answer was never published")
					}
					if !reusedFinished {
						t.Fatal("the reused outcome was never published")
					}
					if answerBeforeReused {
						t.Fatal("the reused outcome was published after the final answer")
					}
					if tool.calls != 1 {
						t.Fatalf("read executed %d times, want the first call only", tool.calls)
					}
					return
				}
			}
		case <-timeout:
			t.Fatal("runTurn hung")
		}
	}
}

// The TUI must publish exactly one assistant answer for a task even when the
// model produces a rejected draft, acquires evidence, and answers again. The
// draft may exist in the internal provider context; it must never become a
// transcript delta.
func TestRunTurnPublishesOnlyTheCommittedAnswer(t *testing.T) {
	read := &orderReadTool{}
	index := 0
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "There is no database."}}},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "README.md"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "This project is a Go coding agent."}}},
	}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
		Tools:                   []agent.Tool{read},
		CompletionReviewRequest: "Runtime completion review: {{reason}}. Re-read the evidence or state the limitation.",
		MaxTurns:                6,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurn("analyze this project and report what it is")
	}()

	var published strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			if delta, ok := event.(AssistantDelta); ok {
				published.WriteString(delta.Text)
			}
			p.state.Apply(event)
		case <-done:
			for {
				select {
				case event := <-p.events:
					if delta, ok := event.(AssistantDelta); ok {
						published.WriteString(delta.Text)
					}
					p.state.Apply(event)
					continue
				default:
				}
				break
			}
			got := published.String()
			if strings.Contains(got, "no database") {
				t.Fatalf("a rejected draft reached the transcript: %q", got)
			}
			if strings.Count(got, "This project is a Go coding agent.") != 1 {
				t.Fatalf("published answer = %q, want exactly the committed answer", got)
			}
			audit := loop.LastResponseAudit()
			if audit.State != agent.ResponseCommitted {
				t.Fatalf("response state = %s, want COMMITTED", audit.State)
			}
			if audit.Rejected != 1 {
				t.Fatalf("rejected drafts = %d, want 1", audit.Rejected)
			}
			if read.calls != 1 {
				t.Fatalf("correction read calls = %d, want 1", read.calls)
			}
			return
		case <-timeout:
			t.Fatal("runTurn hung")
		}
	}
}

// The durable transcript is part of the one-response invariant, not only the
// visible frame. A rejected draft and its tool round never become assistant
// answers in the session store.
func TestRunTurnPersistsExactlyOneAssistantAnswer(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.Create(t.TempDir(), "m")
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}

	index := 0
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.TextContent{Text: "There is no database."}}},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "README.md"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "This project is a Go coding agent."}}},
	}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			if index >= len(script) {
				t.Fatalf("provider request %d exceeded the script", index+1)
			}
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
		Tools:                   []agent.Tool{&orderReadTool{}},
		CompletionReviewRequest: "Repair the answer: {{reason}}",
		MaxTurns:                6,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	p.AttachSession(store, &sess, nil)
	p.runTurn("analyze this project and report what it is")

	rows, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	var answers []string
	for _, row := range rows {
		if row.Role == "assistant" && strings.TrimSpace(row.Text) != "" {
			answers = append(answers, row.Text)
		}
	}
	if len(answers) != 1 {
		t.Fatalf("persisted %d assistant answers, want one: %#v", len(answers), answers)
	}
	if answers[0] != "This project is a Go coding agent." {
		t.Fatalf("persisted answer = %q, want the committed answer", answers[0])
	}
}
