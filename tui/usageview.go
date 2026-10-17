package tui

// The /usage modal: a centered, read-only inspector that reports the token
// ledger and the provider roster in one card.
//
// It is a modal in the same family as the /status card and the model control
// plane: while open it owns the keyboard and the middle of the screen, the
// composer hides behind it, and any key dismisses it without changing
// anything. It deliberately reuses the status card's frame primitives — the
// border-embedded title, the cyan section headers, full-width rules between
// blocks, and the structured footer bar — so the two surfaces read as one
// system. The facts are a snapshot taken when the card opens: an unmeasured
// metric stays omitted rather than rendering as a fabricated zero.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	tea "github.com/charmbracelet/bubbletea"
)

// usageModalTitle is the border-embedded title, matching the /status card's
// "SYSTEM STATUS" treatment.
const usageModalTitle = "USAGE & PROVIDER INSPECTOR"

// usageLabelWidth is the label column the usage metrics share. It is wider
// than the status card's grid column so the longer metric names ("Session
// Cost", "Total Tokens") keep every value on one axis.
const usageLabelWidth = 12

// usageHint is the provider block's footnote: it names the one path that
// actually switches a provider, so the roster never reads as an action list.
const usageHint = "Provider switching is automatic via /models."

// UsageView is the snapshot behind the /usage modal.
//
// It is a plain value so the render stays pure and a test can build one
// directly. Every field is producer-owned: the card reports what it is given
// and hides what is absent rather than substituting a guess.
type UsageView struct {
	// Provider is the active provider key, e.g. "openrouter". Empty renders
	// as "no model".
	Provider string
	// Model is the active "provider/id" reference.
	Model string
	// MaxTokens is the model's completion budget. A non-positive value renders
	// as unknown rather than a fabricated limit.
	MaxTokens int
	// HasUsage reports the ledger measured at least one request. False renders
	// the empty state instead of zeros that read as measured.
	HasUsage bool
	// TurnCount is the number of completed turns folded into the ledger.
	TurnCount int
	// LastRequest is the most recent measured request, valid when HasUsage.
	LastRequest ai.Usage
	// CostKnown reports a price schedule resolved, so a zero cost is a real
	// zero rather than an unknown price.
	CostKnown bool
	// TotalCost is the lifetime ledger cost in USD.
	TotalCost float64
	// SessionCost is the current session's cost in USD.
	SessionCost float64
	// Providers lists the known providers with credential status. The card
	// renders one row per entry.
	Providers []ProviderRow
}

// RenderUsageModal composes the /usage card as an ANSI string.
//
// width is the total rendered width, border included; height, when positive,
// bounds the total rendered height so the card never overflows a short
// viewport. The caller centers the returned block in the terminal. The frame
// is the shared modal frame, so it matches renderStatusModal exactly.
func RenderUsageModal(view UsageView, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = statusCardHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := usageModalContent(view, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	return renderModalFrame(usageModalTitle, content, pal, interior)
}

// usageModalContent builds the card body: the usage inspector first, then the
// provider roster, separated by a full-width rule.
func usageModalContent(view UsageView, pal *Palette, interior int) []string {
	field := func(label, value string) []string {
		return modalField(label, value, pal, interior, 0, usageLabelWidth)
	}
	usage := [][]string{
		field("Provider", statusValueOr(view.Provider, "no model")),
		field("Model", statusValueOr(view.Model, "no model")),
		field("Max Tokens", usageMaxTokensLine(view.MaxTokens)),
	}
	if view.HasUsage {
		usage = append(usage,
			field("Last Input", formatCount(view.LastRequest.Input)),
			field("Last Output", formatCount(view.LastRequest.Output)),
			field("Cache Read", formatCount(view.LastRequest.CacheRead)),
			field("Cache Write", formatCount(view.LastRequest.CacheWrite)),
			field("Total Tokens", formatCount(usageRequestTotal(view.LastRequest))),
			field("Turns", formatCount(view.TurnCount)),
		)
	} else {
		usage = append(usage, field("Activity", "no requests measured yet"))
	}
	usage = append(usage,
		field("Total Cost", usageCostLine(view.CostKnown, view.TotalCost)),
		field("Session Cost", usageCostLine(view.CostKnown, view.SessionCost)),
	)

	providers := usageProviderLines(view, pal, interior)
	providers = append(providers, usageHintLine(pal, interior))

	blocks := [][]string{
		statusBlock("USAGE INSPECTOR", pal, interior, usage...),
		statusBlock("PROVIDER STATUS", pal, interior, providers),
	}
	var rows []string
	for i, block := range blocks {
		if i > 0 {
			rows = append(rows, statusRule(pal, interior))
		}
		rows = append(rows, block...)
	}
	return rows
}

// usageProviderLines renders the provider roster with a status marker per
// provider: a check for a resolved credential, a cross otherwise. The active
// provider is named so the roster doubles as orientation, not just a list.
func usageProviderLines(view UsageView, pal *Palette, interior int) []string {
	if len(view.Providers) == 0 {
		return []string{strings.Repeat(" ", statusFieldIndent) + pal.Muted.Render("no providers known")}
	}
	rows := make([]string, 0, len(view.Providers))
	for _, provider := range view.Providers {
		rows = append(rows, usageProviderRow(provider, provider.ID == view.Provider, pal, interior))
	}
	return rows
}

// usageProviderRow renders one provider line: the credential marker, the
// provider id, and the plain-language status. The marker carries the state in
// text as well as colour, so a monochrome terminal loses nothing.
func usageProviderRow(provider ProviderRow, active bool, pal *Palette, interior int) string {
	marker := "[×]"
	markerStyle := pal.ToolError
	status := "missing API key"
	if provider.Configured {
		marker = "[✓]"
		markerStyle = pal.ToolDone
		status = "configured (inactive)"
		if active {
			status = "configured (active)"
		}
	}
	id := strings.TrimSpace(provider.ID)
	if id == "" {
		id = strings.TrimSpace(provider.Name)
	}
	line := strings.Repeat(" ", statusFieldIndent) + markerStyle.Render(marker) +
		" " + pal.Choice.Render(id) + pal.Muted.Render(" — "+status)
	return TruncateToWidth(line, interior)
}

// usageHintLine renders the provider block's footnote in the muted metadata
// style, flush with the field indent.
func usageHintLine(pal *Palette, interior int) string {
	line := strings.Repeat(" ", statusFieldIndent) + pal.Muted.Render(usageHint)
	return TruncateToWidth(line, interior)
}

// usageMaxTokensLine renders the completion budget, or unknown when the model
// published none.
func usageMaxTokensLine(maxTokens int) string {
	if maxTokens <= 0 {
		return "unknown"
	}
	return formatCount(maxTokens)
}

// usageCostLine renders a dollar amount, or an em dash when no price schedule
// resolved: an unknown price is not a free one.
func usageCostLine(known bool, cost float64) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("$%.4f", cost)
}

// usageRequestTotal sums a request's billable tokens. It prefers the
// provider's own total and falls back to input+output+cache-read, matching the
// agent's usage contract rather than trusting a possibly-absent field.
func usageRequestTotal(usage ai.Usage) int {
	if usage.Total > 0 {
		return usage.Total
	}
	return usage.Input + usage.Output + usage.CacheRead
}

// renderUsageArea draws the /usage card centered over the workbench, using the
// same placement as the /status card.
func renderUsageArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.UsageModal
	if view == nil {
		return
	}
	renderCardArea(sb, state, layout, func(width, height int) string {
		return RenderUsageModal(*view, pal, width, height)
	})
}

// openUsage opens the /usage inspector with a snapshot of the current facts
// and kicks a background probe of the active endpoint.
//
// It is read-only and safe mid-turn: the card reports the in-flight state
// instead of blocking on a turn boundary. The probe runs off the update
// goroutine so a slow endpoint never freezes the modal; its outcome reaches
// the header chrome through ProbeFinished.
func (p *Program) openUsage() {
	p.state.Apply(UsageModalOpened{View: p.usageView()})
	if p.probe != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result := p.probe(ctx)
			p.publish(ProbeFinished{OK: result.OK, Detail: result.Detail})
		}()
	}
}

// usageView captures the facts the card reports. History and the ledger are
// read through their own locks because the turn goroutine writes to them while
// the UI renders.
func (p *Program) usageView() UsageView {
	model := strings.TrimSpace(p.state.Status.Model)
	view := UsageView{
		Provider:  currentProvider(model),
		Model:     model,
		MaxTokens: p.state.Status.ContextWindow,
	}
	if p.providers != nil {
		view.Providers = p.providers()
	}
	if p.usage != nil {
		turns, _, _, _, _ := p.usage.Totals()
		view.TurnCount = turns
		view.HasUsage = turns > 0
		view.CostKnown = p.usage.CostKnown()
		view.TotalCost = p.usage.Cost()
		view.SessionCost = p.usage.CostOf(p.state.SessionIn, p.state.SessionOut, 0, 0)
		if last, ok := p.usage.Last(); ok {
			view.LastRequest = last
		}
	}
	return view
}

// handleUsageKey dismisses the card on any key.
//
// The card is read-only, so there is nothing to navigate and nothing to
// confirm: every key means "close". Swallowing the key here keeps a stray
// space or Enter from reaching the composer hidden underneath.
func (p *Program) handleUsageKey(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	p.state.Apply(UsageModalClosed{})
	return p, nil
}
