package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	tea "github.com/charmbracelet/bubbletea"
)

// Unknown commands suggest neighbours instead of burning a turn.

func TestUnknownCommandSuggests(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "modle"}.runOn(p)
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "/model") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want a did-you-mean hint", p.state.Entries)
	}
}

func TestUnknownCommandWithoutNeighbourStaysPlain(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "xyzzy"}.runOn(p)
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "did you mean") {
			t.Errorf("entries = %+v, want no suggestion for a distant word", p.state.Entries)
		}
	}
}

func TestSuggestCommandThreshold(t *testing.T) {
	if near, ok := SuggestCommand("modle"); !ok || near != "model" {
		t.Errorf("SuggestCommand(modle) = (%q, %v), want (model, true)", near, ok)
	}
	if _, ok := SuggestCommand("zzzqqq"); ok {
		t.Error("a distant word must not suggest")
	}
}

// Secret entry masks and never becomes a prompt.

func TestSecretEntryMasksInput(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(SecretOpened{Label: "API key"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-secret")})
	out := Render(p.state)
	if strings.Contains(out, "sk-secret") {
		t.Errorf("rendered %q: the key must never appear", out)
	}
	if !strings.Contains(out, "••••") {
		t.Errorf("rendered %q: want the mask", out)
	}
}

func TestSecretSubmitReachesHandlerNotHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var mu sync.Mutex
	validated := ""
	p.SetConnectHooks(ConnectHooks{
		Validate: func(ctx context.Context, provider, key string) ([]string, string, error) {
			mu.Lock()
			defer mu.Unlock()
			validated = key
			return []string{"m/1"}, "ok", nil
		},
	})
	p.flow = &connectFlow{provider: "openrouter"}
	p.state.Apply(SecretOpened{Label: "label"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-1")})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	// Validation runs async: poll rather than asserting before it lands.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := validated
		mu.Unlock()
		if got != "" || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if validated != "sk-1" {
		t.Errorf("validated key = %q, want the secret delivered to the flow", validated)
	}
	if len(p.history) != 0 {
		t.Error("a secret reached the conversation history")
	}
	if p.state.Secret != nil {
		t.Error("the secret prompt stayed open after submit")
	}
}

func TestSecretEscapeAbandons(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.flow = &connectFlow{provider: "openrouter"}
	p.state.Apply(SecretOpened{Label: "label"})
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-")})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Secret != nil || p.state.Input != "" {
		t.Error("escape must close the secret prompt and clear it")
	}
	if p.flow != nil {
		t.Error("escape must abandon the setup flow")
	}
}

// Connect flow: provider → source → validate → model → save.

func stubConnectHooks() ConnectHooks {
	return ConnectHooks{
		Providers: func() []PickerItem {
			return []PickerItem{{Title: "OpenRouter", Detail: "no key", Value: "openrouter"}}
		},
		Sources: func(provider string) []PickerItem {
			return []PickerItem{
				{Title: "Paste a new key", Value: "new"},
				{Title: "Use detected credential", Detail: "environment (K)", Value: "known"},
			}
		},
		Validate: func(ctx context.Context, provider, key string) ([]string, string, error) {
			if key == "bad" {
				return nil, "", errors.New("rejected")
			}
			return []string{"m/one", "m/two"}, "2 models listed", nil
		},
	}
}

func TestConnectFullFlowSaves(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var savedProvider, savedKey, savedModel string
	hooks := stubConnectHooks()
	hooks.Save = func(provider, key, model string) error {
		savedProvider, savedKey, savedModel = provider, key, model
		return nil
	}
	p.SetConnectHooks(hooks)
	p.SetModelSwitcher(func(id string) error { return nil }, func() string { return "openrouter/m/one" })

	Command{Name: "connect"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("no provider picker")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter}) // choose openrouter
	if p.state.Picker == nil || p.state.Picker.Kind != "connect-src" {
		t.Fatal("no source picker after provider choice")
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter}) // choose "known"
	// Validation runs async and the sources picker closed with the choice:
	// drain events until the model picker opens or the deadline passes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.state.Picker != nil && p.state.Picker.Kind == "connect-models" {
			break
		}
		select {
		case event := <-p.events:
			p.state.Apply(event)
			if result, ok := event.(ValidationFinished); ok {
				p.onValidationFinished(result)
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if p.state.Picker == nil || p.state.Picker.Kind != "connect-models" {
		t.Fatalf("no model picker: %+v", p.state.Picker)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if savedProvider != "openrouter" || savedKey != "" || savedModel != "m/one" {
		t.Errorf("saved = %s/%s/%s, want the detected credential and chosen model", savedProvider, savedKey, savedModel)
	}
}

func TestConnectValidationFailureReports(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	hooks := stubConnectHooks()
	p.SetConnectHooks(hooks)
	p.flow = &connectFlow{provider: "openrouter", key: "bad", keyIsNew: true}
	p.startValidation("openrouter", "bad", true)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case event := <-p.events:
			p.state.Apply(event)
			if result, ok := event.(ValidationFinished); ok {
				p.onValidationFinished(result)
			}
			goto checked
		case <-time.After(100 * time.Millisecond):
		}
	}
checked:
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "rejected") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the rejection named", p.state.Entries)
	}
	if p.flow != nil {
		t.Error("a failed validation must abandon the flow")
	}
}

func TestConnectStaleValidationDropped(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetConnectHooks(stubConnectHooks())
	p.flow = &connectFlow{provider: "openrouter"}
	// A result for a provider the user already left must not open a picker.
	p.onValidationFinished(ValidationFinished{Provider: "other", Models: []string{"m/x"}})
	if p.state.Picker != nil {
		t.Error("a stale validation opened a picker for an abandoned flow")
	}
}

// Providers, status, usage, doctor, retry, compact, disconnect.

// /providers is retired: its roster lives in the /usage inspector now, so the
// old command must not remain as a shadow path with its own output format.
func TestProvidersCommandRetired(t *testing.T) {
	if _, ok := LookupCommand("providers"); ok {
		t.Fatal("/providers is still registered")
	}
	p := NewProgram(nil, "test", 0)
	Command{Name: "providers"}.runOn(p)
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice
		}
	}
	if !strings.Contains(text, "unknown command /providers") {
		t.Errorf("notice = %q, want the command reported unknown", text)
	}
}

// /status opens a modal card instead of printing a transcript notice, and the
// card reports the local facts the session observed.
func TestStatusOpensTheCard(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Model = "groq/m"
	p.state.Status.Cwd = "/work/proj"
	p.state.Status.Workspace = "BUILD"
	p.state.Status.GitBranch = "main"
	p.state.Status.GitKnown = true
	p.state.Status.GitUntracked = 5
	p.state.SessionIn = 1000
	p.state.SessionOut = 2000

	Command{Name: "status"}.runOn(p)

	if p.state.StatusModal == nil {
		t.Fatal("/status did not open the modal")
	}
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			t.Errorf("status leaked into the transcript: %q", entry.Notice)
		}
	}
	out := stripANSI(Render(p.state))
	for _, want := range []string{"WORKSPACE STATUS", "groq/m", "/work/proj", "main · dirty", "5 untracked", "BUILD · Interactive", "↑1k"} {
		if !strings.Contains(out, want) {
			t.Errorf("status card = %q, want %q", out, want)
		}
	}
}

// /usage opens the inspector rather than printing a transcript notice, and the
// card reports the ledger and the provider roster in one surface.
func TestUsageOpensInspector(t *testing.T) {
	p := NewProgram(nil, "test", 200_000)
	p.state.Status.Model = "openrouter/anthropic/claude-sonnet-4-5"
	p.SetProvidersFunc(func() []ProviderRow {
		return []ProviderRow{
			{ID: "openrouter", Name: "OpenRouter", Configured: true},
			{ID: "gemini", Name: "Gemini"},
		}
	})
	log := &agent.UsageLog{}
	log.SetCost(ai.Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75})
	log.Add([]agent.Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{
		Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, Total: 3_000_000,
	}}}})
	p.SetUsageLog(log)

	Command{Name: "usage"}.runOn(p)

	if p.state.UsageModal == nil {
		t.Fatal("/usage did not open the inspector")
	}
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			t.Errorf("usage leaked into the transcript: %q", entry.Notice)
		}
	}
	out := stripANSI(Render(p.state))
	for _, want := range []string{
		"USAGE & PROVIDER INSPECTOR",
		"USAGE INSPECTOR",
		"openrouter/anthropic/claude-sonnet-4-5",
		"200k",
		"Last Input", "Last Output", "Cache Read", "Cache Write", "Total Tokens",
		"Total Cost", "Session Cost",
		"PROVIDER STATUS",
		"[✓] openrouter — configured (active)",
		"[×] gemini — missing API key",
		"Provider switching is automatic via /models.",
		"Esc / q", "close modal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage card = %q, want %q", out, want)
		}
	}
}

// A missing ledger still opens the inspector: the provider roster is useful
// without token telemetry, and the usage block must say so rather than panic
// or fabricate zeros.
func TestUsageInspectorWithoutLedger(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Model = "groq/llama"
	p.SetProvidersFunc(func() []ProviderRow {
		return []ProviderRow{{ID: "groq", Name: "Groq", Configured: true}}
	})
	Command{Name: "usage"}.runOn(p)

	if p.state.UsageModal == nil {
		t.Fatal("/usage did not open the inspector without a ledger")
	}
	out := stripANSI(Render(p.state))
	for _, want := range []string{"USAGE INSPECTOR", "no requests measured yet", "[✓] groq — configured (active)"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage card = %q, want %q", out, want)
		}
	}
}

func TestDoctorPrintsRows(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetDoctorFunc(func() []DoctorLine {
		return []DoctorLine{
			{Name: "config", OK: true, Detail: "loaded"},
			{Name: "credentials", OK: false, Detail: "set X"},
		}
	})
	Command{Name: "doctor"}.runOn(p)
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice
		}
	}
	if !strings.Contains(text, "FAIL") || !strings.Contains(text, "1 of 2 checks failed") {
		t.Errorf("doctor = %q, want rows plus the tally", text)
	}
}

func TestRetryResendsLastPrompt(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "again"), "test", 0)
	p.history = []agent.Message{agent.UserMessage{Text: "original"}}
	retryCmd := p.runCommand(Command{Name: "retry"})
	if retryCmd == nil {
		t.Fatal("no retry command produced")
	}
	retryCmd() // runs the turn synchronously
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case event := <-p.events:
			if user, ok := event.(UserSubmitted); ok {
				if user.Text != "original" {
					t.Fatalf("retried %q, want the last prompt", user.Text)
				}
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the retried prompt never submitted")
		}
	}
}

func TestRetryWithNoHistoryReports(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "retry"}.runOn(p)
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "nothing to retry") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the empty history reported", p.state.Entries)
	}
}

func TestCompactNeedsHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCompactor(func(ctx context.Context, history []agent.Message) ([]agent.Message, string, error) {
		return history, "0 summarised", nil
	})
	Command{Name: "compact"}.runOn(p)
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "nothing to compact") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the empty transcript reported", p.state.Entries)
	}
}

func TestCompactReplacesHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.history = []agent.Message{agent.UserMessage{Text: "old"}}
	p.SetCompactor(func(ctx context.Context, history []agent.Message) ([]agent.Message, string, error) {
		if len(history) != 1 {
			t.Errorf("compactor got %d messages, want the full history", len(history))
		}
		return []agent.Message{agent.UserMessage{Text: "summary-handover"}}, "1 summarised", nil
	})
	compactCmd := p.runCommand(Command{Name: "compact"})
	if compactCmd == nil {
		t.Fatal("no compact command produced")
	}
	compactCmd() // runs synchronously; publishes replay + notice
	if len(p.history) != 1 {
		t.Fatalf("history = %d messages, want the compacted one", len(p.history))
	}
	var replayed, noticed bool
	timeout := time.After(2 * time.Second)
	for !replayed || !noticed {
		select {
		case event := <-p.events:
			switch event.(type) {
			case HistoryReplaced:
				replayed = true
			case Notice:
				noticed = true
			}
		case <-timeout:
			t.Fatalf("replayed = %v noticed = %v, want both published", replayed, noticed)
		}
	}
}

func TestDisconnectRemovesFileCredentials(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Model = "groq/m"
	var deleted string
	p.SetCredentialDeleter(func(provider string) (string, error) {
		deleted = provider
		return "removed the saved groq credential", nil
	})
	Command{Name: "disconnect"}.runOn(p)
	if deleted != "groq" {
		t.Errorf("deleted = %q, want the current provider by default", deleted)
	}
	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "removed") {
			found = true
		}
	}
	if !found {
		t.Errorf("entries = %+v, want the outcome confirmed", p.state.Entries)
	}
}

// Header and narrow mode.

func TestHeaderShowsModelAndConnection(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	out := Render(s)
	if !strings.Contains(out, "ohgo") || !strings.Contains(out, "groq/m") {
		t.Errorf("header missing: %q", firstLine(out))
	}
	if !strings.Contains(out, "not checked") {
		t.Errorf("header = %q, want the unknown connection stated", firstLine(out))
	}
	s.Apply(ProbeFinished{OK: true, Detail: "3 models listed"})
	if out := Render(s); !strings.Contains(out, "3 models listed") {
		t.Errorf("header = %q, want the probe outcome", firstLine(out))
	}
}

func firstLine(out string) string {
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func TestNarrowStatusStaysOneLine(t *testing.T) {
	s := New()
	s.Status.Model = "groq/some-long-model-name"
	s.Status.ContextUsed = 10
	s.Status.ContextWindow = 100
	s.Apply(WorkspaceUpdated{Cwd: "/home/user/project", HasCwd: true, Session: "#7", HasSession: true})
	s.Apply(TerminalResized{Width: 40, Height: 24})
	out := Render(s)
	lines := strings.Split(out, "\n")
	// Identity lives in the header; the footer keeps workspace context and
	// must not spend narrow rows repeating the model or the budget prefix.
	header := lines[0]
	if !strings.Contains(header, "groq/some-long-model-name") {
		t.Errorf("header = %q, want the model kept there", header)
	}
	status := findStatusLine(t, out, "#7")
	if strings.Contains(status, "context") {
		t.Errorf("narrow status = %q, want the compact form", status)
	}
	if strings.Contains(status, "groq/some-long-model-name") {
		t.Errorf("narrow status = %q, want identity de-duplicated out of the footer", status)
	}
	if !strings.Contains(status, "project") {
		t.Errorf("narrow status = %q, want the working directory kept", status)
	}
}

// findStatusLine locates the footer by its session marker. The composer is
// a multi-row block (rules, input, suggestions), so the status line is no
// longer at a fixed offset from the end.
func findStatusLine(t *testing.T, out, marker string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("no status line containing %q in %q", marker, out)
	return ""
}
