package main

import (
	"context"
	"errors"
	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/project"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/startup"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSummarizerSendsTheConversation(t *testing.T) {
	// The summarizer must give the model the actual history: a summary built
	// from anything else would lose the context compaction is meant to
	// preserve.
	var gotRequest ai.Request
	registry := ai.NewRegistry()
	registry.SetAPIKey("anthropic", "test")

	stub := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		gotRequest = req
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "a summary"}},
		}), nil
	}
	summarize := newSummarizer(stub, catalog.MustResolve("anthropic/claude-sonnet-4-5"))

	text, err := summarize(context.Background(), []agent.Message{
		agent.UserMessage{Text: "read a.go"},
		agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "reading"}},
		}},
		agent.ToolResultMessage{
			ToolCallID: "c1", ToolName: "read",
			Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}},
		},
		agent.Notification{Text: "UI chatter that must never reach the model"},
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if text != "a summary" {
		t.Errorf("summary = %q, want the model's output", text)
	}

	if !strings.Contains(gotRequest.System[1].Text, "Summarise") {
		t.Errorf("system prompt = %q, want the summarisation instruction", gotRequest.System[1].Text)
	}
	sent := gotRequest.Messages[0].(ai.UserMessage).Text()
	for _, want := range []string{"read a.go", "reading", "package a"} {
		if !strings.Contains(sent, want) {
			t.Errorf("summarizer input missing %q: %s", want, sent)
		}
	}
	// A UI-only message must not be summarised: it was never sent to the model.
	if strings.Contains(sent, "UI chatter") {
		t.Errorf("a UI-only message reached the summarizer: %s", sent)
	}
}

func TestNewSummarizerPropagatesStreamFailure(t *testing.T) {
	summarize := newSummarizer(func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return nil, errSummarizeStreamFailed
	}, catalog.MustResolve("anthropic/claude-sonnet-4-5"))
	if _, err := summarize(context.Background(), []agent.Message{agent.UserMessage{Text: "x"}}); err == nil {
		t.Fatal("expected a summarizer failure to surface")
	}
}

var errSummarizeStreamFailed = errors.New("stream unavailable")

func TestPersistTurnWritesTheTranscript(t *testing.T) {
	// A replayed session must show the model's answer, so the assistant turn
	// has to be persisted alongside the tool results.
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	sess, err := store.Create("/tmp", "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}

	turn := agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "here is the answer"},
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a"}},
			},
			StopReason: ai.StopToolUse,
			Model:      catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		},
		Results: []agent.ToolOutcome{{
			CallID:   "c1",
			ToolName: "read",
			Result:   agent.Result{Text: "file contents"},
		}},
	}
	persistTurn(store, sess, turn)

	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("persisted %d messages, want the assistant turn and the tool result", len(messages))
	}
	if messages[0].Role != "assistant" || !strings.Contains(messages[0].Text, "here is the answer") {
		t.Errorf("assistant turn = %+v, want the model's text", messages[0])
	}
	if len(messages[0].Calls) != 1 || messages[0].Calls[0].ID != "c1" {
		t.Errorf("assistant calls = %+v, want the tool call", messages[0].Calls)
	}
	if messages[1].Role != "toolResult" || messages[1].Text != "file contents" {
		t.Errorf("tool result = %+v, want the executed result", messages[1])
	}
}

func TestCompactionFromSettingsHonoursOverrides(t *testing.T) {
	disabled := false
	keep := 3
	pct := 70.0
	settings := configSettingsWithCompaction(&disabled, keep, pct)

	c := compactionFromSettings(settings)
	if c.Enabled {
		t.Error("an explicit disabled compaction was overridden")
	}
	if c.KeepRecent != 3 {
		t.Errorf("keepRecent = %d, want 3", c.KeepRecent)
	}
	if c.ThresholdPercent != 70 {
		t.Errorf("thresholdPercent = %v, want 70", c.ThresholdPercent)
	}
}

func TestCompactionFromSettingsDefaultsWhenUnset(t *testing.T) {
	c := compactionFromSettings(configSettingsWithCompaction(nil, 0, 0))
	if !c.Enabled {
		t.Error("compaction should default to enabled")
	}
	if c.KeepRecent <= 0 {
		t.Error("keepRecent should default to a positive value")
	}
}

// configSettingsWithCompaction builds settings with a compaction override.
func configSettingsWithCompaction(enabled *bool, keepRecent int, pct float64) config.Settings {
	return config.Settings{Compaction: &config.Compaction{
		Enabled:          enabled,
		KeepRecent:       keepRecentPtr(keepRecent),
		ThresholdPercent: pctPtr(pct),
	}}
}

func keepRecentPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func pctPtr(f float64) *float64 {
	if f <= 0 {
		return nil
	}
	return &f
}

// TestMachineStateSurvivesProjectDeletion pins the storage-scope boundary:
// rm -rf .ohgo deletes project state, never machine state. Machine settings and
// the session database live under HOME and must survive, while the project —
// including its project-local worker identity — re-initializes on the next run.
func TestMachineStateSurvivesProjectDeletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A genuinely machine-global setting, to prove it survives project deletion.
	if err := config.Save(config.Settings{Model: "anthropic/claude-sonnet-4-5"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := startup.Ensure(root, startup.Deps{
		Stdin:  strings.NewReader("n\nAlice\nproject-a\n"),
		Stdout: io.Discard,
	})
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if first.Project.ID == "" {
		t.Fatal("no project identity was established")
	}
	if first.Project.WorkerName() != "Alice" || first.Project.Name != "project-a" {
		t.Fatalf("project metadata = %+v, want Alice / project-a", first.Project)
	}

	sessionPath, err := session.DefaultPath()
	if err != nil {
		t.Fatalf("session path: %v", err)
	}
	store, err := session.Open(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.CreateWith(session.CreateOptions{
		Cwd: root, Model: "anthropic/claude-sonnet-4-5", Project: first.Project.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := session.FromAgentMessage(agent.UserMessage{Text: "remember"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(sess.ID, stored); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(project.Root(root), project.DirName)); err != nil {
		t.Fatal(err)
	}

	second, err := startup.Ensure(root, startup.Deps{
		Stdin:  strings.NewReader("n\nBob\nproject-b\n"),
		Stdout: io.Discard,
	})
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if !second.CreatedProject {
		t.Error("project did not re-initialize after .ohgo deletion")
	}
	if second.Project.WorkerName() != "Bob" {
		t.Errorf("worker = %q, want the re-established Bob", second.Project.WorkerName())
	}
	if second.Project.ID == first.Project.ID {
		t.Error("project identity was resurrected; deletion must mint a fresh one")
	}

	machine, err := config.Load(root)
	if err != nil {
		t.Fatalf("machine settings lost after project deletion: %v", err)
	}
	if machine.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("machine setting = %q, want it to survive .ohgo deletion", machine.Model)
	}
	if machine.GlobalName() != "" {
		t.Errorf("worker leaked into machine settings: %q", machine.GlobalName())
	}

	reopened, err := session.Open(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Get(sess.ID)
	if err != nil {
		t.Fatalf("session lost after project deletion: %v", err)
	}
	if got.Project != first.Project.ID {
		t.Errorf("session project = %q, want the original %q", got.Project, first.Project.ID)
	}
	msgs, err := reopened.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "remember" {
		t.Errorf("session transcript lost after project deletion: %+v", msgs)
	}
}

// TestSessionContextResolvesProjectIdentity pins the session/identity
// boundary: a session is stored globally and scoped by project id, and the
// project context it resolves is the current project's local identity — never
// another project's worker.
func TestSessionContextResolvesProjectIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	initProject := func(root, worker, name string) startup.Result {
		t.Helper()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := startup.Ensure(root, startup.Deps{
			Stdin:  strings.NewReader("n\n" + worker + "\n" + name + "\n"),
			Stdout: io.Discard,
		})
		if err != nil {
			t.Fatalf("Ensure(%s): %v", root, err)
		}
		return res
	}
	a, b := t.TempDir(), t.TempDir()
	fa := initProject(a, "Alice", "project-a")
	fb := initProject(b, "Bob", "project-b")

	path, err := session.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.CreateWith(session.CreateOptions{
		Cwd: a, Model: "anthropic/claude-sonnet-4-5", Project: fa.Project.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWith(session.CreateOptions{
		Cwd: b, Model: "anthropic/claude-sonnet-4-5", Project: fb.Project.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// Each project's session list sees only its own session.
	listA, err := store.ListByProject(fa.Project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].Project != fa.Project.ID {
		t.Fatalf("project A sessions = %+v, want only A's", listA)
	}
	listB, err := store.ListByProject(fb.Project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || listB[0].Project != fb.Project.ID {
		t.Fatalf("project B sessions = %+v, want only B's", listB)
	}

	// The identity a session in A resolves is A's worker, not B's.
	loaded, found, err := project.Load(a)
	if err != nil || !found {
		t.Fatalf("Load(A) = %v, %v, %v", loaded, found, err)
	}
	if loaded.WorkerName() != "Alice" {
		t.Errorf("A resolves worker %q, want Alice", loaded.WorkerName())
	}
	if listA[0].Project == fb.Project.ID {
		t.Error("A's session resolved project B's identity")
	}
}
