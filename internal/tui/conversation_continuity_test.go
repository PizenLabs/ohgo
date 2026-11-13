package tui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// recordingStream scripts assistant turns and keeps every request it received,
// so a test can assert the exact outbound conversation.
type recordingStream struct {
	mu       sync.Mutex
	script   []ai.AssistantMessage
	index    int
	requests []ai.Request
}

func (s *recordingStream) next() ai.AssistantMessage {
	if s.index >= len(s.script) {
		return ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}}
	}
	message := s.script[s.index]
	s.index++
	return message
}

// record keeps the request's message list and system blocks for later
// assertions. The rest of the request (tool schemas, budgets) is irrelevant to
// the conversation contract.
func (s *recordingStream) record(req ai.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, ai.Request{
		Messages: append([]ai.Message(nil), req.Messages...),
		System:   append([]ai.SystemPromptBlock(nil), req.System...),
	})
}

func (s *recordingStream) messagesAt(index int) []ai.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.requests) {
		return nil
	}
	return s.requests[index].Messages
}

func (s *recordingStream) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// The host's turn boundary is part of the message contract: a turn that
// produced only reasoning is displayed but never replayed. Reproduces the
// observed BUILD-like session, including a restart from the session store.
func TestReasoningOnlyTurnNeverReachesTheNextRequest(t *testing.T) {
	root := t.TempDir()
	stream := &recordingStream{script: []ai.AssistantMessage{{
		Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "the project is running and answered 200"}},
		StopReason: ai.StopEndTurn,
	}}}
	// recordingStream must see the real request, not a placeholder.
	fn := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		stream.record(req)
		return ai.NewCompletedStream(stream.next()), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: fn,
		Tools:  []agent.Tool{&silentTool{name: "read"}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	store, err := session.Open(filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess, err := store.CreateWith(session.CreateOptions{Cwd: root, Model: "m", Workspace: "build"})
	if err != nil {
		t.Fatalf("CreateWith: %v", err)
	}

	p := NewProgram(loop, root, 0)
	p.AttachSession(store, &sess, nil)
	p.runTurn("run this project help me")

	if got := stream.count(); got != 1 {
		t.Fatalf("turn 1 requests = %d, want 1", got)
	}
	if got := len(p.history); got != 1 {
		t.Fatalf("history = %d messages, want the user request only: %#v", got, p.history)
	}
	stored, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	for _, row := range stored {
		if row.Role == "assistant" {
			t.Fatalf("persisted %+v, want no non-conversational assistant row", row)
		}
	}

	// Restart: the session is replayed from disk into a new program, exactly
	// as a resumed run does.
	history, err := session.Replay(stored)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	second := &recordingStream{script: []ai.AssistantMessage{{
		Content: []ai.ContentBlock{ai.TextContent{Text: "Convert the static pages to the Next.js app router."}},
	}}}
	loop2, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			second.record(req)
			return ai.NewCompletedStream(second.next()), nil
		},
		Tools: []agent.Tool{&silentTool{name: "read"}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p2 := NewProgram(loop2, root, 0)
	p2.AttachSession(store, &sess, history)
	p2.runTurn("How to convert this project to Next.js?")

	messages := second.messagesAt(0)
	if len(messages) != 2 {
		t.Fatalf("recovered request = %d messages, want both user turns and no assistant: %#v", len(messages), messages)
	}
	if err := ai.ValidateMessages(messages, nil); err != nil {
		t.Fatalf("recovered request is not canonical: %v", err)
	}
	for index, message := range messages {
		if assistant, ok := message.(ai.AssistantMessage); ok && !ai.HasSendableContent(assistant) {
			t.Fatalf("recovered message %d = %#v, want only sendable assistant turns", index, assistant)
		}
	}
}

// silentTool is a read-only capability the scripted turns never call. It
// satisfies the loop's requirement that a run be able to do something.
type silentTool struct{ name string }

func (t *silentTool) Name() string        { return t.name }
func (t *silentTool) Description() string { return "test tool" }
func (t *silentTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"value":{"type":"string"}}}`))
}
func (t *silentTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (t *silentTool) Exclusive(map[string]any) bool { return false }
func (t *silentTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "ok"}
}

// The previous task's objective must not be treated as the current one just
// because it is persisted. Both orderings are checked, including a role change.
func TestPreviousObjectiveIsNotTheCurrentObjective(t *testing.T) {
	previousText := "How to convert this project to Next.js?"
	currentText := "run this project help me"
	previous := TaskResume{
		ID:          "task-previous",
		Objective:   previousText,
		Status:      "FAILED",
		Role:        "build",
		HasContract: true,
		Contract:    buildIntent(previousText),
	}
	next := buildIntent(currentText)

	if decision, continuing := taskTurnDecision(previous, currentText, "ask", next); continuing {
		t.Fatalf("decision = %q, want the new request to start its own task", decision)
	}

	// The same objective is a continuation, whatever its position in the
	// session history.
	same := buildIntent(previousText)
	if _, continuing := taskTurnDecision(previous, previousText, "ask", same); !continuing {
		t.Fatal("the same objective was not recognized as a continuation")
	}
	different := buildIntent("unrelated new objective")
	if _, continuing := taskTurnDecision(previous, "unrelated new objective", "ask", different); continuing {
		t.Fatal("an unrelated objective continued the previous task")
	}

	// A role change alone does not transfer a different objective: identity is
	// the objective, not the role.
	roleChange := buildIntent(previousText)
	if _, continuing := taskTurnDecision(previous, previousText, "fuzz", roleChange); !continuing {
		t.Fatal("the same objective under a new role was not recognized as a continuation")
	}
}

// buildIntent is the deterministic contract the router derives from a prompt.
func buildIntent(text string) contract.IntentContract {
	return workspace.BuildIntentContract(text)
}

// Continuity orientation is a system block, not a message. The previous task
// and the objective may orient the model, but they must never become user or
// assistant content, and the message list stays canonical.
func TestContinuityContextIsASystemBlockNotAMessage(t *testing.T) {
	stream := &recordingStream{script: []ai.AssistantMessage{{
		Content: []ai.ContentBlock{ai.TextContent{Text: "acknowledged"}},
	}}}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			stream.record(req)
			return ai.NewCompletedStream(stream.next()), nil
		},
		Tools: []agent.Tool{&silentTool{name: "read"}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, t.TempDir(), 0)
	p.SetTaskHooks(TaskHooks{
		Context: func() []string {
			return []string{"Objective: run this project help me", "Previous objective: How to convert this project to Next.js?"}
		},
	})
	p.runTurn("run this project help me")

	if stream.count() != 1 {
		t.Fatalf("requests = %d, want 1", stream.count())
	}
	request := stream.requests[0]
	system := ""
	for _, block := range request.System {
		system += block.Text
	}
	if !strings.Contains(system, "Task continuity") || !strings.Contains(system, "Previous objective") {
		t.Fatalf("system blocks = %q, want the reconstructed continuity orientation", system)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("messages = %d, want the user request only: %#v", len(request.Messages), request.Messages)
	}
	user, ok := request.Messages[0].(ai.UserMessage)
	if !ok || user.Text() != "run this project help me" {
		t.Fatalf("message 0 = %#v, want the current user request", request.Messages[0])
	}
	if strings.Contains(user.Text(), "Previous objective") || strings.Contains(user.Text(), "※ recap ·") {
		t.Fatal("continuity or recap text became message content")
	}
	if err := ai.ValidateMessages(request.Messages, nil); err != nil {
		t.Fatalf("request is not canonical: %v", err)
	}
}
