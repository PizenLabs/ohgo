package tui

import (
	"math"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	tea "github.com/charmbracelet/bubbletea"
)

// usageViewFixture is a populated inspector snapshot for the render tests.
func usageViewFixture() UsageView {
	return UsageView{
		Provider:    "openrouter",
		Model:       "openrouter/anthropic/claude-sonnet-4-5",
		MaxTokens:   64_000,
		HasUsage:    true,
		TurnCount:   4,
		LastRequest: ai.Usage{Input: 123, Output: 456, CacheRead: 78, CacheWrite: 9, Total: 666},
		CostKnown:   true,
		TotalCost:   0.1234,
		SessionCost: 0.0100,
		Providers: []ProviderRow{
			{ID: "openrouter", Name: "OpenRouter", Configured: true},
			{ID: "groq", Name: "Groq", Configured: true},
			{ID: "ollama", Name: "Ollama", Configured: true},
			{ID: "anthropic", Name: "Anthropic", Configured: true},
			{ID: "openai", Name: "OpenAI", Configured: true},
			{ID: "gemini", Name: "Gemini"},
		},
	}
}

// usageCard returns the plain inspector for a width, with the ANSI stripped.
func usageCard(view UsageView, width, height int) string {
	return stripANSI(RenderUsageModal(view, DarkPalette(), width, height))
}

// usageCardLines is the plain inspector split into rows.
func usageCardLines(view UsageView, width, height int) []string {
	return strings.Split(usageCard(view, width, height), "\n")
}

// The inspector carries both sections, the border-embedded title, the section
// rules, and the structured footer bar.
func TestRenderUsageModalShowsSections(t *testing.T) {
	lines := usageCardLines(usageViewFixture(), 84, 0)
	out := strings.Join(lines, "\n")

	for _, want := range []string{
		"USAGE & PROVIDER INSPECTOR",
		"USAGE INSPECTOR",
		"  Provider    · openrouter",
		"  Model       · openrouter/anthropic/claude-sonnet-4-5",
		"  Max Tokens  · 64k",
		"  Last Input  · 123",
		"  Last Output · 456",
		"  Cache Read  · 78",
		"  Cache Write · 9",
		"  Total Tokens · 666",
		"  Total Cost  · $0.1234",
		"  Session Cost · $0.0100",
		"PROVIDER STATUS",
		"[✓] openrouter — configured (active)",
		"[✓] groq — configured (inactive)",
		"[×] gemini — missing API key",
		"Provider switching is automatic via /models.",
		"Esc / q", "close modal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card missing %q:\n%s", want, out)
		}
	}

	// The title rides the top border, not a body row.
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") || !strings.Contains(lines[0], "USAGE & PROVIDER INSPECTOR") {
		t.Errorf("title is not embedded in the top border: %q", lines[0])
	}
	// The two sections are separated by a full-width rule.
	rules := 0
	for _, line := range lines[1 : len(lines)-1] {
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")
		if inner != "" && strings.Trim(inner, "─") == "" {
			rules++
		}
	}
	if rules < 1 {
		t.Errorf("card has no inter-section rule:\n%s", out)
	}
}

// The frame is rectangular: every row is exactly the card width, and the
// wider metric labels still share one value axis.
func TestRenderUsageModalFrameAlignment(t *testing.T) {
	lines := usageCardLines(usageViewFixture(), 84, 0)
	width := CellWidth(lines[0])
	for i, line := range lines {
		if got := CellWidth(line); got != width {
			t.Errorf("row %d is %d cells wide, want %d: %q", i, got, width, line)
		}
	}
	for _, want := range []string{
		"  Provider    · ", "  Max Tokens  · ", "  Last Input  · ",
		"  Total Tokens · ", "  Session Cost · ",
	} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Errorf("label column not padded to the shared axis: %q", want)
		}
	}
}

// A short viewport drops content, never the structured footer bar, and never
// exceeds the requested height.
func TestRenderUsageModalKeepsFooterWhenShort(t *testing.T) {
	out := usageCard(usageViewFixture(), 84, 8)
	if !strings.Contains(out, "Esc / q") || !strings.Contains(out, "close modal") {
		t.Errorf("short card lost the footer bar:\n%s", out)
	}
	if got := strings.Count(out, "\n") + 1; got > 8 {
		t.Errorf("card emitted %d rows, want at most 8", got)
	}
}

// The last-request block maps the provider's usage fields one-to-one, and an
// absent total falls back to the billable sum instead of rendering zero.
func TestUsageTokenMetricsMapping(t *testing.T) {
	out := usageCard(usageViewFixture(), 84, 0)
	for _, want := range []string{
		"  Last Input  · 123",
		"  Last Output · 456",
		"  Cache Read  · 78",
		"  Cache Write · 9",
		"  Total Tokens · 666",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q:\n%s", want, out)
		}
	}

	if got := usageRequestTotal(ai.Usage{Input: 10, Output: 20, CacheRead: 3}); got != 33 {
		t.Errorf("usageRequestTotal fallback = %d, want 33", got)
	}
	if got := usageRequestTotal(ai.Usage{Input: 10, Output: 20, CacheRead: 3, Total: 99}); got != 99 {
		t.Errorf("usageRequestTotal = %d, want the provider total 99", got)
	}
}

// Costs render only when a price schedule resolved: an unknown price is not a
// free one, so it reads as an em dash.
func TestUsageCostRepresentation(t *testing.T) {
	view := usageViewFixture()
	out := usageCard(view, 84, 0)
	if !strings.Contains(out, "  Total Cost  · $0.1234") || !strings.Contains(out, "  Session Cost · $0.0100") {
		t.Errorf("known costs missing:\n%s", out)
	}

	view.CostKnown = false
	view.TotalCost = 0
	view.SessionCost = 0
	out = usageCard(view, 84, 0)
	if strings.Contains(out, "$0.0000") {
		t.Errorf("unknown price rendered as a free one:\n%s", out)
	}
	if !strings.Contains(out, "  Total Cost  · —") {
		t.Errorf("unknown price did not render as an em dash:\n%s", out)
	}
}

// The provider roster marks configuration and the active provider in text as
// well as colour, and never claims a missing key is configured.
func TestUsageProviderStatusRepresentation(t *testing.T) {
	out := usageCard(usageViewFixture(), 84, 0)
	for _, want := range []string{
		"[✓] openrouter — configured (active)",
		"[✓] groq — configured (inactive)",
		"[✓] ollama — configured (inactive)",
		"[✓] anthropic — configured (inactive)",
		"[✓] openai — configured (inactive)",
		"[×] gemini — missing API key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("provider row missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[✓] gemini") {
		t.Errorf("a missing key rendered as configured:\n%s", out)
	}

	// No roster is reported as unknown, not as an empty success.
	view := usageViewFixture()
	view.Providers = nil
	if empty := usageCard(view, 84, 0); !strings.Contains(empty, "no providers known") {
		t.Errorf("empty roster missing its unknown state:\n%s", empty)
	}
}

// The empty ledger renders an explicit empty state instead of zeros that read
// as measured.
func TestUsageEmptyLedgerState(t *testing.T) {
	view := usageViewFixture()
	view.HasUsage = false
	view.LastRequest = ai.Usage{}
	out := usageCard(view, 84, 0)
	if !strings.Contains(out, "  Activity    · no requests measured yet") {
		t.Errorf("empty ledger missing its state:\n%s", out)
	}
	for _, absent := range []string{"Last Input", "Last Output", "Cache Read"} {
		if strings.Contains(out, absent) {
			t.Errorf("empty ledger rendered %q:\n%s", absent, out)
		}
	}
}

// /usage opens the card as an overlay, hides the composer, and centers the
// bordered box.
func TestUsageModalOwnsTheScreen(t *testing.T) {
	p := NewProgram(nil, "test", 200_000)
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	p.state.Status.Model = "groq/m"
	p.SetProvidersFunc(func() []ProviderRow {
		return []ProviderRow{{ID: "groq", Name: "Groq", Configured: true}}
	})
	p.state.Apply(InputChanged{Text: "typing"})

	Command{Name: "usage"}.runOn(p)

	if p.state.UsageModal == nil {
		t.Fatal("/usage did not open the card")
	}
	out := stripANSI(Render(p.state))
	if strings.Contains(out, "enter send") {
		t.Errorf("composer hints rendered under the card:\n%s", out)
	}
	if strings.Contains(out, "typing") {
		t.Errorf("composer text rendered under the card:\n%s", out)
	}
	centered := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "┌") && strings.Contains(line, "USAGE & PROVIDER INSPECTOR") && strings.HasPrefix(line, " ") {
			centered = true
		}
	}
	if !centered {
		t.Errorf("card is not centered:\n%s", out)
	}
}

// Every dismissal key closes the card without leaking into the composer or
// opening the quit confirmation.
func TestUsageModalClosesOnAnyKey(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyRunes, Runes: []rune("x")},
	} {
		p := NewProgram(nil, "test", 0)
		p.state.Apply(UsageModalOpened{View: usageViewFixture()})
		_, cmd := p.handleKey(key)
		if p.state.UsageModal != nil {
			t.Errorf("key %q did not close the card", key.String())
		}
		if cmd != nil {
			t.Errorf("key %q returned a command, want nil", key.String())
		}
		if p.state.QuitConfirm != nil {
			t.Errorf("key %q opened the quit confirmation", key.String())
		}
	}
}

// Apply is the only transition path: opening installs the snapshot and closing
// clears it, and /clear drops a card caught mid-session.
func TestUsageModalStateTransitions(t *testing.T) {
	s := New()
	s.Apply(UsageModalOpened{View: usageViewFixture()})
	if s.UsageModal == nil || s.UsageModal.Provider != "openrouter" {
		t.Fatalf("UsageModal = %+v, want the installed snapshot", s.UsageModal)
	}
	s.Apply(Cleared{})
	if s.UsageModal != nil {
		t.Error("/clear left the inspector open")
	}
	s.Apply(UsageModalOpened{View: usageViewFixture()})
	s.Apply(UsageModalClosed{})
	if s.UsageModal != nil {
		t.Error("close did not clear the inspector")
	}
}

// The view maps the live model and ledger facts: provider, model, budget, last
// request, and both cost figures.
func TestUsageViewMapsLedger(t *testing.T) {
	p := NewProgram(nil, "test", 128_000)
	p.state.Status.Model = "anthropic/claude-sonnet-4-5"
	p.state.SessionIn = 1_000
	p.state.SessionOut = 500
	log := &agent.UsageLog{}
	log.SetCost(ai.Cost{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 0.2})
	log.Add([]agent.Turn{{Assistant: ai.AssistantMessage{Usage: ai.Usage{
		Input: 2_000, Output: 1_000, CacheRead: 100, CacheWrite: 50, Total: 3_100,
	}}}})
	p.SetUsageLog(log)

	view := p.usageView()
	if view.Provider != "anthropic" || view.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("identity = %q/%q, want the active reference", view.Provider, view.Model)
	}
	if view.MaxTokens != 128_000 {
		t.Errorf("max tokens = %d, want 128000", view.MaxTokens)
	}
	if !view.HasUsage || view.TurnCount != 1 {
		t.Errorf("usage = %v/%d, want one measured turn", view.HasUsage, view.TurnCount)
	}
	if view.LastRequest.Input != 2_000 || view.LastRequest.Output != 1_000 ||
		view.LastRequest.CacheRead != 100 || view.LastRequest.CacheWrite != 50 {
		t.Errorf("last request = %+v, want the measured usage", view.LastRequest)
	}
	if want := (2_000*1.0 + 1_000*2.0 + 100*0.1 + 50*0.2) / 1_000_000; math.Abs(view.TotalCost-want) > 1e-9 {
		t.Errorf("total cost = %v, want %v", view.TotalCost, want)
	}
	if want := (1_000*1.0 + 500*2.0) / 1_000_000; math.Abs(view.SessionCost-want) > 1e-9 {
		t.Errorf("session cost = %v, want %v", view.SessionCost, want)
	}
}
