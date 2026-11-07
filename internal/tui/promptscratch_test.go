package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/history"
	tea "github.com/charmbracelet/bubbletea"
)

type orderedPromptHistory struct {
	entries []string
	order   *[]string
}

func (h *orderedPromptHistory) Entries() []string {
	return append([]string(nil), h.entries...)
}

func (h *orderedPromptHistory) Add(prompt string) {
	if h.order != nil {
		*h.order = append(*h.order, "history")
	}
	h.entries = append(h.entries, prompt)
}

func (h *orderedPromptHistory) Flush() error {
	if h.order != nil {
		*h.order = append(*h.order, "flush")
	}
	return nil
}

type fakePromptScratch struct {
	order          *[]string
	content        string
	offloadCalls   int
	releasedIDs    []int64
	releasedAll    int
	releasedAllErr error
}

func (f *fakePromptScratch) ShouldOffload(string) bool { return true }

func (f *fakePromptScratch) Offload(_ int64, prompt string) (string, string, bool, error) {
	if f.order != nil {
		*f.order = append(*f.order, "scratch")
	}
	f.offloadCalls++
	content := f.content
	if content == "" {
		content = prompt
	}
	return "/tmp/scratch.md", content, true, nil
}

func (f *fakePromptScratch) ReleaseSession(sessionID int64) error {
	f.releasedIDs = append(f.releasedIDs, sessionID)
	return nil
}

func (f *fakePromptScratch) ReleaseAll() error {
	f.releasedAll++
	return f.releasedAllErr
}

// TestPrepareTurnPromptWritesHistoryBeforeScratch proves the durability order:
// the full prompt reaches history before the disposable file is written, and
// the injected payload is the bytes returned by the scratch seam.
func TestPrepareTurnPromptWritesHistoryBeforeScratch(t *testing.T) {
	order := []string{}
	hist := &orderedPromptHistory{order: &order}
	scratch := &fakePromptScratch{order: &order, content: "INJECTED FROM SCRATCH"}
	p := NewProgram(nil, "test", 0, WithPromptHistory(hist))
	p.SetPromptScratch(scratch)

	text, recorded := p.prepareTurnPrompt("large prompt")
	if text != "INJECTED FROM SCRATCH" {
		t.Fatalf("payload = %q, want the scratch contents", text)
	}
	if !recorded {
		t.Fatal("turn preparation did not report the history write")
	}
	if len(order) != 3 || order[0] != "history" || order[1] != "flush" || order[2] != "scratch" {
		t.Fatalf("order = %v, want history flushed before scratch", order)
	}
	if len(hist.entries) != 1 || hist.entries[0] != "large prompt" {
		t.Fatalf("history = %v, want the full prompt", hist.entries)
	}
}

// TestSessionSwitchAndShutdownReleaseScratch proves the ephemeral lifecycle:
// switching conversations releases only the old session, and process shutdown
// releases whatever remains.
func TestSessionSwitchAndShutdownReleaseScratch(t *testing.T) {
	scratch := &fakePromptScratch{}
	p := NewProgram(nil, "test", 0)
	p.SetPromptScratch(scratch)
	p.AttachSession(nil, &session.Session{ID: 1}, nil)

	p.adoptSession(&session.Session{ID: 2}, OpenedSession{})
	if len(scratch.releasedIDs) != 1 || scratch.releasedIDs[0] != 1 {
		t.Fatalf("released = %v, want session 1 on switch", scratch.releasedIDs)
	}
	if scratch.releasedAll != 0 {
		t.Fatal("a session switch released every session's files")
	}

	p.closeDone()
	if scratch.releasedAll != 1 {
		t.Fatalf("ReleaseAll calls = %d, want 1 on shutdown", scratch.releasedAll)
	}
}

// TestFullPromptRecallSurvivesScratchPurge is the end-to-end recall contract:
// the UI's Ctrl+P reaches the entire multi-line prompt from durable history
// after every scratch file is gone.
func TestFullPromptRecallSurvivesScratchPurge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	prompt := "master prompt\n" + strings.Repeat("full detail line\n", history.ScratchLineThreshold-1) + "final line"

	first := history.Load(root)
	p := NewProgram(nil, "test", 0, WithPromptHistory(first))
	scratch := history.NewScratch(root)
	p.SetPromptScratch(scratch)

	injected, recorded := p.prepareTurnPrompt(prompt)
	if !recorded {
		t.Fatal("the prompt was not recorded in durable history")
	}
	if injected != prompt {
		t.Fatal("the injected prompt was truncated")
	}
	files, err := filepath.Glob(filepath.Join(scratch.Dir(), "prompt_0_*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("scratch files = %v, want exactly one", files)
	}
	p.closeDone()
	if remaining, _ := filepath.Glob(filepath.Join(scratch.Dir(), "prompt_0_*.md")); len(remaining) != 0 {
		t.Fatalf("scratch files survived shutdown: %v", remaining)
	}

	second := history.Load(root)
	defer func() { _ = second.Close() }()
	q := NewProgram(nil, "test", 0, WithPromptHistory(second))
	keyPress(q, tea.KeyMsg{Type: tea.KeyCtrlP})
	if q.state.Input != prompt {
		t.Fatalf("recalled %q, want the full prompt", q.state.Input)
	}
}
