package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	tea "github.com/charmbracelet/bubbletea"
)

// The /new creator presents exactly the two title modes; the custom mode moves
// the highlight to the name entry line and renders the typed text in the card.
func TestNewSessionCreatorPresentsBothTitleModes(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.openNewSessionPicker(nil)
	picker := p.state.Picker
	if picker == nil || picker.Kind != PickerNewSession {
		t.Fatalf("picker = %+v, want the /new creator", picker)
	}
	if len(picker.Items) != 2 {
		t.Fatalf("items = %+v, want auto and custom only", picker.Items)
	}
	if picker.Items[0].Value != newSessionAutoValue || !strings.Contains(picker.Items[0].Title, "Auto") {
		t.Errorf("first row = %+v, want the auto title mode", picker.Items[0])
	}
	if picker.Items[1].Value != newSessionCustomValue || !strings.Contains(picker.Items[1].Title, "Custom") {
		t.Errorf("second row = %+v, want the custom title mode", picker.Items[1])
	}
	if picker.Cursor != 0 {
		t.Errorf("cursor = %d, want auto selected by default", picker.Cursor)
	}

	picker.SetFilter("Fix the parser")
	if item, ok := picker.Chosen(); !ok || item.Value != newSessionCustomValue {
		t.Fatalf("chosen = %+v, want custom after typing a name", item)
	}
	card := stripANSI(renderNewSessionPickerCard(picker, DarkPalette(), Layout{}, 64, 0))
	for _, want := range []string{"NEW SESSION", "Auto (Local Title)", "Custom Name...", "Fix the parser", "Enter create", "Esc cancel"} {
		if !strings.Contains(card, want) {
			t.Errorf("creator card missing %q:\n%s", want, card)
		}
	}
}

// Typing a name and submitting creates one session with that exact title.
func TestNewSessionCreatorCustomNameCreatesTitledSession(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	var created, renamed int
	var title string
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.SetSessionActions(SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			created++
			sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
			if err != nil {
				return nil, OpenedSession{}, err
			}
			return &sess, OpenedSession{}, nil
		},
		Rename: func(id int64, next string) error {
			renamed++
			title = next
			return store.SetTitle(id, next)
		},
	})
	p.openNewSessionPicker(nil)
	p.state.Picker.SetFilter("Fix the parser")
	if _, _ = p.choosePicker(); p.state.Picker != nil {
		t.Fatal("creator stayed open after submitting")
	}
	if created != 1 || renamed != 1 {
		t.Fatalf("created = %d, renamed = %d, want one of each", created, renamed)
	}
	if title != "Fix the parser" {
		t.Fatalf("title = %q, want the typed name", title)
	}
	if p.session == nil {
		t.Fatal("the new session was not adopted")
	}
	if p.session.Title != "Fix the parser" {
		t.Fatalf("in-memory title = %q, want the typed name", p.session.Title)
	}
	if sess, err := store.Get(p.session.ID); err != nil || sess.Title != "Fix the parser" {
		t.Fatalf("stored title = %q (err %v), want the typed name", sess.Title, err)
	}
}

// Submitting without a name is the auto path: one session is created and no
// title is written until the first prompt supplies one.
func TestNewSessionCreatorAutoCreatesUntitledSession(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	var created, renamed int
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.SetSessionActions(SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			created++
			sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
			if err != nil {
				return nil, OpenedSession{}, err
			}
			return &sess, OpenedSession{}, nil
		},
		Rename: func(int64, string) error {
			renamed++
			return nil
		},
	})
	p.openNewSessionPicker(nil)
	if _, _ = p.choosePicker(); created != 1 {
		t.Fatalf("created = %d, want exactly one session", created)
	}
	if renamed != 0 {
		t.Fatalf("renamed = %d, want no explicit title on the auto path", renamed)
	}
	if p.session == nil {
		t.Fatal("the new session was not adopted")
	}
	if p.session.Title != "" {
		t.Fatalf("in-memory title = %q, want empty for an auto session", p.session.Title)
	}
	if sess, err := store.Get(p.session.ID); err != nil || sess.Title != "" {
		t.Fatalf("stored title = %q (err %v), want an untitled session", sess.Title, err)
	}
}

// Esc cancels even when a name was typed: the typed line is a draft, not a
// pending creation.
func TestNewSessionCreatorEscapeCancelsTypedName(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var created int
	p.SetSessionActions(SessionActions{
		New: func() (*session.Session, OpenedSession, error) {
			created++
			return &session.Session{ID: 1}, OpenedSession{}, nil
		},
	})
	p.openNewSessionPicker(nil)
	p.state.Picker.SetFilter("Do not create me")
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Picker != nil {
		t.Fatal("Esc left the creator open")
	}
	if created != 0 {
		t.Fatalf("created = %d, want none after Esc", created)
	}
}

// The Session Manager renders a deterministic local label immediately. There
// is no model request and therefore no waiting placeholder to represent.
func TestSessionManagerShowsDeterministicLocalTitleImmediately(t *testing.T) {
	view := SessionManagerState{
		Items: []SessionItem{{
			ID:            7,
			Model:         "p/m",
			InitialPrompt: "Use ls to inspect the project root and backend/",
		}},
		VisibleRows: 5,
	}
	card := sessionManagerCard(view, 90, 0)
	if !strings.Contains(card, "ls project root backend") {
		t.Fatalf("card does not show the deterministic local title:\n%s", card)
	}
	if strings.Contains(card, "generating title") {
		t.Fatalf("card still shows a waiting title placeholder:\n%s", card)
	}

	state := New()
	state.Apply(SessionManagerOpened{View: view})
	state.Apply(SessionTitleUpdated{ID: 7, Title: "Fix the parser"})
	if got := state.SessionModal.Items[0]; got.Title != "Fix the parser" {
		t.Fatalf("item = %+v, want the persisted title", got)
	}
}

// The local title is bounded and deterministic: conversational noise is
// stripped, words are never split, and a dangling article cannot survive.
func TestLocalSessionTitleBounds(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "short prompt", prompt: "fix the parser", want: "fix parser"},
		{name: "five words", prompt: "one two three four five", want: "one two three four five"},
		{name: "six words", prompt: "one two three four five six", want: "one two three four five"},
		{name: "whitespace", prompt: "  fix \n the\tparser  ", want: "fix parser"},
		{name: "long token", prompt: strings.Repeat("a", 60), want: strings.Repeat("a", 40)},
		{name: "request framing", prompt: "Use ls to inspect the project root", want: "ls project root"},
		{name: "trailing article", prompt: "Use ls to inspect the", want: "ls"},
		{name: "path preserved", prompt: "Use internal/promptopt/titler.go in the patch", want: "internal/promptopt/titler.go patch"},
		{name: "noise only", prompt: "please explain the", want: ""},
		{name: "empty", prompt: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := localSessionTitle(tc.prompt); got != tc.want {
				t.Fatalf("localSessionTitle(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

// Empty, marker, and default-model-path titles are all unassigned; a real
// label is not.
func TestSessionTitleAssignedClassification(t *testing.T) {
	cases := []struct {
		name  string
		title string
		model string
		want  bool
	}{
		{name: "empty", title: "", model: "p/m", want: false},
		{name: "whitespace", title: "   ", model: "p/m", want: false},
		{name: "untitled marker", title: " (untitled) ", model: "p/m", want: false},
		{name: "untitled marker case", title: "(Untitled)", model: "p/m", want: false},
		{name: "default model path", title: "p/m", model: "p/m", want: false},
		{name: "model path with padding", title: "  p/m  ", model: "p/m", want: false},
		{name: "real title", title: "Fix the parser", model: "p/m", want: true},
		{name: "title equals another model", title: "p/m", model: "x/y", want: true},
		{name: "unknown model", title: "p/m", model: "", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionTitleAssigned(tc.title, tc.model); got != tc.want {
				t.Fatalf("sessionTitleAssigned(%q, %q) = %v, want %v", tc.title, tc.model, got, tc.want)
			}
		})
	}
}

// A blank title renders the explicit untitled marker or a local label from the
// recorded prompt, never the model path.
func TestSessionDisplayTitleNeverPromotesTheModelPath(t *testing.T) {
	item := SessionItem{Title: "", Model: "openrouter/stealth/crouton-alpha"}
	if got := sessionDisplayTitle(item); got != "(untitled)" {
		t.Fatalf("display title = %q, want (untitled)", got)
	}
	item.InitialPrompt = "Use ls to inspect the project root and backend/"
	if got := sessionDisplayTitle(item); got != "ls project root backend" {
		t.Fatalf("display title = %q, want the local label", got)
	}
	item.Title = item.Model
	if got := sessionDisplayTitle(item); got != "ls project root backend" {
		t.Fatalf("model-path title = %q, want the local label instead", got)
	}
}

// Opening the Session Manager labels legacy sessions synchronously from their
// stored first prompt. No provider is configured, so a hidden model request
// would fail or hang instead of yielding the deterministic title.
func TestSessionManagerBackfillsLegacyTitlesLocally(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	legacy, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "openrouter/stealth/crouton-alpha"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Append(legacy.ID, session.StoredMessage{Role: "user", Text: "check this project and analyze what this project is about?"}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	stats, err := store.Stats([]int64{legacy.ID})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	p := NewProgram(nil, "test", 0)
	p.store = store
	p.SetSessionBrowser(func() ([]SessionItem, error) {
		stat := stats[legacy.ID]
		return []SessionItem{{
			ID:            legacy.ID,
			Title:         "",
			Model:         legacy.Model,
			Messages:      stat.Messages,
			InitialPrompt: stat.InitialPrompt,
			Prompt:        stat.Prompt,
		}}, nil
	}, func(id int64) (*session.Session, OpenedSession, error) {
		return &legacy, OpenedSession{}, nil
	})

	p.openSessionManager()
	if p.state.SessionModal == nil {
		t.Fatal("the Session Manager did not open")
	}
	if item := p.state.SessionModal.Items[0]; item.Title != "check project analyze" {
		t.Fatalf("roster title = %q, want the local derivative", item.Title)
	}
	stored, err := store.Get(legacy.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != "check project analyze" {
		t.Fatalf("stored title = %q, want the local derivative", stored.Title)
	}
}

// Submission itself stores the deterministic label, before the returned turn
// command runs. A consumer that reads the store immediately sees the title.
func TestSubmitStoresTheLocalTitleSynchronously(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	p := NewProgram(nil, "test", 0)
	p.store = store
	p.session = &sess
	_ = p.submit("Use ls to inspect the project root and backend/")

	stored, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != "ls project root backend" {
		t.Fatalf("stored title = %q, want the deterministic local label", stored.Title)
	}
	if p.session.Title != "ls project root backend" {
		t.Fatalf("in-memory title = %q, want the deterministic local label", p.session.Title)
	}
}

// The first submitted prompt assigns the title synchronously, before the
// provider call, and the turn spends exactly one request: the title itself is
// local. An explicit title is never replaced.
func TestFirstPromptAssignsLocalTitleWithoutAProviderCall(t *testing.T) {
	cases := []struct {
		name      string
		preset    string
		wantTitle string
	}{
		{name: "empty", wantTitle: "fix parser"},
		{name: "untitled marker", preset: "(untitled)", wantTitle: "fix parser"},
		{name: "default model path", preset: "p/m", wantTitle: "fix parser"},
		{name: "custom", preset: "My explicit title", wantTitle: "My explicit title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
			if err != nil {
				t.Fatalf("session.Open: %v", err)
			}
			defer func() { _ = store.Close() }()
			sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if tc.preset != "" {
				if err := store.SetTitle(sess.ID, tc.preset); err != nil {
					t.Fatalf("SetTitle: %v", err)
				}
				sess.Title = tc.preset
			}

			var calls int
			var titleAtProviderCall string
			stream := func(context.Context, ai.Request) (*ai.Stream, error) {
				calls++
				current, err := store.Get(sess.ID)
				if err != nil {
					t.Errorf("Get before provider call: %v", err)
				} else {
					titleAtProviderCall = current.Title
				}
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}},
				}), nil
			}
			loop, err := agent.New(agent.Config{
				Model:  ai.Model{ID: "m", Provider: "p"},
				Stream: stream,
				Tools:  []agent.Tool{&idleTool{}},
			})
			if err != nil {
				t.Fatalf("agent.New: %v", err)
			}

			p := NewProgram(loop, "test", 0)
			p.store = store
			p.session = &sess
			p.runTurn("fix the parser")

			if calls != 1 {
				t.Fatalf("provider calls = %d, want exactly the execution turn", calls)
			}
			if titleAtProviderCall != tc.wantTitle {
				t.Fatalf("title at provider call = %q, want %q", titleAtProviderCall, tc.wantTitle)
			}
			stored, err := store.Get(sess.ID)
			if err != nil {
				t.Fatalf("Get after turn: %v", err)
			}
			if stored.Title != tc.wantTitle {
				t.Fatalf("stored title = %q, want %q", stored.Title, tc.wantTitle)
			}
		})
	}
}
