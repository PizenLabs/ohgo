package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/continuity/session"
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
	for _, want := range []string{"NEW SESSION", "Auto (LLM Generated)", "Custom Name...", "Fix the parser", "Enter create", "Esc cancel"} {
		if !strings.Contains(card, want) {
			t.Errorf("creator card missing %q:\n%s", want, card)
		}
	}
}

// Typing a name and submitting creates one session with that exact title.
func TestNewSessionCreatorCustomNameCreatesTitledSession(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
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
	if sess, err := store.Get(p.session.ID); err != nil || sess.Title != "Fix the parser" {
		t.Fatalf("stored title = %q (err %v), want the typed name", sess.Title, err)
	}
}

// Submitting without a name is the auto path: one session is created and no
// explicit title is written before the first turn.
func TestNewSessionCreatorAutoCreatesUntitledSession(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
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

// The Session Manager names an in-flight title instead of falling back to the
// model string, and a completed event replaces the placeholder in place.
func TestSessionManagerShowsGeneratingTitlePlaceholder(t *testing.T) {
	// A default-filler title (the model path) is still unassigned, so the
	// placeholder must replace it rather than displaying the model twice.
	view := SessionManagerState{
		Items:       []SessionItem{{ID: 7, Title: "p/m", TitlePending: true, Model: "p/m"}},
		VisibleRows: 5,
	}
	card := sessionManagerCard(view, 90, 0)
	if !strings.Contains(card, "(generating title…)") {
		t.Fatalf("card does not show the in-flight title placeholder:\n%s", card)
	}

	state := New()
	state.Apply(SessionManagerOpened{View: view})
	state.Apply(SessionTitleUpdated{ID: 7, Title: "Fix the parser"})
	if got := state.SessionModal.Items[0]; got.Title != "Fix the parser" || got.TitlePending {
		t.Fatalf("item = %+v, want the persisted title and no pending marker", got)
	}
}

// While a model title request is in flight the roster shows a deterministic
// local sanitization, not an open-ended waiting label: a slower ASK model must
// not make the manager look stuck, and conversational noise must not consume
// the title column.
func TestSessionManagerShowsLocalFallbackWhileTitling(t *testing.T) {
	view := SessionManagerState{
		Items: []SessionItem{{
			ID:            7,
			Model:         "p/m",
			TitlePending:  true,
			InitialPrompt: "check this project and analyze what this project is about",
		}},
		VisibleRows: 5,
	}
	card := sessionManagerCard(view, 90, 0)
	if strings.Contains(card, sessionTitlePlaceholder) {
		t.Fatalf("card still shows the waiting placeholder:\n%s", card)
	}
	if !strings.Contains(card, "check project analyze") {
		t.Fatalf("card does not show the sanitized local fallback title:\n%s", card)
	}
	if strings.Contains(card, "this project") {
		t.Fatalf("card still shows conversational noise:\n%s", card)
	}
}

// The local fallback is bounded and deterministic: conversational noise is
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
// label is not. This is the classification the background pass relies on.
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

// A blank title renders the explicit untitled marker, never the model path.
func TestSessionDisplayTitleNeverPromotesTheModelPath(t *testing.T) {
	item := SessionItem{Title: "", Model: "openrouter/stealth/crouton-alpha"}
	if got := sessionDisplayTitle(item); got != "(untitled)" {
		t.Fatalf("display title = %q, want (untitled)", got)
	}
	item.TitlePending = true
	if got := sessionDisplayTitle(item); got != sessionTitlePlaceholder {
		t.Fatalf("pending display title = %q, want %q", got, sessionTitlePlaceholder)
	}
}

// Opening the Session Manager must start a bounded backfill for legacy
// untitled sessions without waiting for any provider call. The roster renders
// the pending placeholder immediately and updates in place as titles land.
func TestSessionManagerBackfillsLegacyTitlesWithoutBlocking(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
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
	if err := store.Append(legacy.ID, session.StoredMessage{Role: "assistant", Text: "this is a Go e-commerce project"}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	stats, err := store.Stats([]int64{legacy.ID})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats[legacy.ID].InitialPrompt == "" {
		t.Fatal("fixture has no stored initial prompt")
	}

	release := make(chan struct{})
	started := make(chan struct{}, 1)
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
	p.SetSessionTitleGenerator(func(ctx context.Context, modelRef, firstPrompt string) (string, error) {
		if modelRef != legacy.Model {
			t.Errorf("modelRef = %q, want %q", modelRef, legacy.Model)
		}
		if firstPrompt != "check this project and analyze what this project is about?" {
			t.Errorf("firstPrompt = %q, want the stored initial prompt", firstPrompt)
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return "Analyze the Go project", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	opened := make(chan struct{})
	go func() {
		defer close(opened)
		p.openSessionManager()
	}()
	select {
	case <-opened:
	case <-time.After(2 * time.Second):
		t.Fatal("opening the Session Manager blocked on title generation")
	}
	if p.state.SessionModal == nil {
		t.Fatal("the Session Manager did not open")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy title backfill never started")
	}
	if item := p.state.SessionModal.Items[0]; !item.TitlePending {
		t.Fatalf("item = %+v, want a pending title placeholder before the request completes", item)
	}

	close(release)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			p.apply(event)
			if item := p.state.SessionModal.Items[0]; item.Title == "Analyze the Go project" && !item.TitlePending {
				stored, err := store.Get(legacy.ID)
				if err != nil || stored.Title != "Analyze the Go project" {
					t.Fatalf("stored title = %q (err %v), want the backfilled title", stored.Title, err)
				}
				return
			}
		case <-deadline:
			t.Fatalf("title never landed in the Session Manager: %+v", p.state.SessionModal.Items[0])
		}
	}
}

// A title request must not outlive the program. Shutdown cancels the in-flight
// provider call and clears the pending marker, so a closed UI never leaves a
// titling goroutine waiting on a dead session.
func TestSessionTitleRequestIsCancelledOnShutdown(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	started := make(chan struct{}, 1)
	cancelled := make(chan error, 1)
	p := NewProgram(nil, "test", 0)
	p.store = store
	p.SetSessionTitleGenerator(func(ctx context.Context, _, _ string) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		cancelled <- ctx.Err()
		return "", ctx.Err()
	})

	// The first-turn path is the real caller; it must pass a context derived
	// from the program, not an independent background context.
	p.generateSessionTitle(sess.ID, sess.Model, "analyze this project")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("title request never started")
	}
	p.closeDone()

	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("title request ended with %v, want context.Canceled from shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title request outlived program shutdown")
	}

	deadline := time.After(2 * time.Second)
	for p.titlingPending(sess.ID) {
		select {
		case <-deadline:
			t.Fatal("pending marker survived a cancelled title request")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stored, err := store.Get(sess.ID)
	if err != nil || stored.Title != "" {
		t.Fatalf("stored title = %q (err %v), want no title after cancellation", stored.Title, err)
	}
}

// A failed ASK-model title request must still leave a usable label: the local
// truncation is persisted and published, so the manager never keeps a pending
// placeholder after the bounded timeout.
func TestSessionTitleFallsBackToLocalPromptOnFailure(t *testing.T) {
	store, err := session.Open(t.TempDir() + "/sessions.db")
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
	p.SetSessionTitleGenerator(func(context.Context, string, string) (string, error) {
		return "", errors.New("provider down")
	})
	const prompt = "fix the parser in the session view"
	p.generateSessionTitleNow(sess.ID, sess.Model, prompt, false)

	stored, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := localSessionTitle(prompt)
	if stored.Title != want {
		t.Fatalf("stored title = %q, want the local fallback %q", stored.Title, want)
	}
	select {
	case event := <-p.events:
		updated, ok := event.(SessionTitleUpdated)
		if !ok || updated.ID != sess.ID || updated.Title != want {
			t.Fatalf("event = %#v, want the fallback SessionTitleUpdated", event)
		}
	default:
		t.Fatal("no SessionTitleUpdated event was published for the fallback")
	}
	if p.titlingPending(sess.ID) {
		t.Fatal("pending marker survived the fallback title")
	}
}

// The first completed turn derives a title, while a session with an explicit
// title never spends a background model call on one.
func TestFirstTurnGeneratesOnlyUntitledSessionTitles(t *testing.T) {
	cases := []struct {
		name      string
		preset    string
		wantCalls int32
		wantTitle string
	}{
		{name: "empty", wantCalls: 1, wantTitle: "Fix the parser"},
		{name: "untitled marker", preset: "(untitled)", wantCalls: 1, wantTitle: "Fix the parser"},
		{name: "default model path", preset: "p/m", wantCalls: 1, wantTitle: "Fix the parser"},
		{name: "custom", preset: "My explicit title", wantCalls: 0, wantTitle: "My explicit title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := session.Open(t.TempDir() + "/sessions.db")
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
			}

			var calls atomic.Int32
			generated := make(chan struct{}, 1)
			p := NewProgram(scriptedLoop(t, "answer"), "test", 0)
			p.store = store
			p.session = &sess
			p.SetSessionTitleGenerator(func(ctx context.Context, modelRef, firstPrompt string) (string, error) {
				calls.Add(1)
				if modelRef != "p/m" {
					t.Errorf("modelRef = %q, want the session snapshot", modelRef)
				}
				if firstPrompt != "fix the parser" {
					t.Errorf("firstPrompt = %q, want the submitted prompt", firstPrompt)
				}
				select {
				case generated <- struct{}{}:
				default:
				}
				return "  Fix the parser  ", nil
			})

			p.runTurn("fix the parser")
			if tc.wantCalls > 0 {
				select {
				case <-generated:
				case <-time.After(5 * time.Second):
					t.Fatal("the first turn never requested a title")
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				stored, err := store.Get(sess.ID)
				if err == nil && stored.Title == tc.wantTitle {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("stored title = %q (err %v), want %q", stored.Title, err, tc.wantTitle)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.wantCalls == 0 {
				// The background check must settle without spending a model
				// call. The wait is a negative contract: an explicit title is
				// final, not a suggestion the titler may replace.
				select {
				case <-generated:
					t.Fatal("a session with an explicit title still requested a generated title")
				case <-time.After(200 * time.Millisecond):
				}
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("title calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}
