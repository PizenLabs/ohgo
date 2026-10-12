package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	tea "github.com/charmbracelet/bubbletea"
)

// ProviderRow is one provider for the /providers listing and the model control
// plane's provider pane.
type ProviderRow struct {
	// ID is the stable provider key.
	ID string
	// Name is the display name.
	Name string
	// KeyDetail names the credential source ("environment (X)", "credential
	// file", "none required", "missing: set X") without secret bytes.
	KeyDetail string
	// Count is the number of models the merged catalog lists for this
	// provider. It is a fact, not an estimate: it counts what the snapshot
	// would show.
	Count int
	// Configured reports a credential resolves for this provider. It is not a
	// health claim: reachability is a probe, and the plane never invents one.
	Configured bool
}

// DoctorFunc runs diagnostics for /doctor.
type DoctorFunc func() []DoctorLine

// ProvidersFunc lists providers with credential status for /providers.
type ProvidersFunc func() []ProviderRow

// ProbeFunc checks the current endpoint in the background. It runs off the
// update goroutine so a slow endpoint never freezes the UI; the outcome
// returns through the returned channel-less path below — the caller publishes
// ProbeFinished itself.
type ProbeFunc func(ctx context.Context) ProbeFinished

// Compactor summarises a history on demand for /compact.
type Compactor func(ctx context.Context, history []agent.Message) (newHistory []agent.Message, summary string, err error)

// CredentialDeleter removes a saved credential for /disconnect, reporting
// what it did (or why the credential is not removable here).
type CredentialDeleter func(provider string) (string, error)

// ConnectHooks drives the /connect setup flow. Each hook is nil-safe: a
// missing hook reports the step unavailable rather than panicking.
type ConnectHooks struct {
	// Providers lists setup candidates with status details.
	Providers func() []PickerItem
	// Sources lists credential choices for a provider. Values are opaque
	// handles: "new" pastes a key, anything else reuses a detected source.
	Sources func(provider string) []PickerItem
	// Validate checks a credential (empty key means the detected one) and
	// returns the model ids to choose from.
	Validate func(ctx context.Context, provider, key string) (models []string, detail string, err error)
	// Save persists the outcome; an empty key keeps the stored credential.
	Save func(provider, key, model string) error
}

// connectFlow tracks an in-progress setup. It lives on the Program and is
// touched only from the update goroutine, except ValidationFinished pairing
// which matches on provider+key rather than shared memory.
type connectFlow struct {
	provider string
	key      string
	keyIsNew bool
}

// statusReport renders /status from local state: no network, no providers,
// nothing that can fail.
func (p *Program) statusReport() string {
	p.mu.Lock()
	turns := len(p.history)
	p.mu.Unlock()
	var sb strings.Builder
	fmt.Fprintf(&sb, "model: %s\n", p.state.Status.Model)
	if pct := p.state.Status.Percent(); pct >= 0 {
		fmt.Fprintf(&sb, "context: %d%% of %s tokens\n", pct, formatCount(p.state.Status.ContextWindow))
	} else {
		sb.WriteString("context: unknown window\n")
	}
	fmt.Fprintf(&sb, "turns: %d", turns)
	if p.state.Status.Session != "" {
		fmt.Fprintf(&sb, " · session %s", p.state.Status.Session)
	} else if p.session != nil {
		fmt.Fprintf(&sb, " · session #%d", p.session.ID)
	}
	if p.state.Status.Cwd != "" {
		fmt.Fprintf(&sb, " · %s", p.state.Status.Cwd)
	}
	if p.state.Status.UsageText != "" {
		fmt.Fprintf(&sb, "\nusage: %s", p.state.Status.UsageText)
	}
	if len(p.state.Queued) > 0 {
		fmt.Fprintf(&sb, " · %d queued", len(p.state.Queued))
	}
	if p.state.Status.Busy {
		sb.WriteString(" · working")
	}
	fmt.Fprintf(&sb, "\ntheme: %s", NormalizeTheme(p.state.Theme))
	if p.state.ConnDetail != "" {
		state := "not connected"
		if p.state.ConnOK {
			state = "connected"
		}
		fmt.Fprintf(&sb, "\nconnection: %s (%s)", state, p.state.ConnDetail)
	}
	return sb.String()
}

// formatCount renders large token counts compactly.
func formatCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// showUsage reports the accumulated ledger, or its absence.
func (p *Program) showUsage() {
	if p.usage == nil {
		p.state.Apply(Notice{Text: "usage tracking is unavailable"})
		return
	}
	p.state.Apply(Notice{Text: p.usage.Report(false)})
}

// runDoctor prints the diagnostic rows. The rows arrive pre-scrubbed: Doctor
// lines name sources, never values, by construction.
func (p *Program) runDoctor() {
	if p.doctor == nil {
		p.state.Apply(Notice{Text: "diagnostics are unavailable"})
		return
	}
	var sb strings.Builder
	failed, total := 0, 0
	for _, line := range p.doctor() {
		total++
		status := "ok"
		if !line.OK {
			status = "FAIL"
			failed++
		}
		fmt.Fprintf(&sb, "%-4s %-12s %s\n", status, line.Name, line.Detail)
	}
	if failed > 0 {
		fmt.Fprintf(&sb, "%d of %d checks failed", failed, total)
	}
	p.state.Apply(Notice{Text: strings.TrimRight(sb.String(), "\n")})
}

// retryLast re-sends the latest user prompt. While busy it queues like any
// other submit: a retry that raced the running turn would interleave histories.
func (p *Program) retryLast() tea.Cmd {
	p.mu.Lock()
	history := append([]agent.Message(nil), p.history...)
	p.mu.Unlock()
	text, ok := agent.LastUserText(history)
	if !ok {
		p.state.Apply(Notice{Text: "nothing to retry: no prompt yet"})
		return nil
	}
	if p.state.Status.Busy {
		p.queue(text)
		return nil
	}
	p.state.Apply(InputChanged{Text: ""})
	return p.submit(text)
}

// compactNow summarises context on demand through the installed compactor.
// It refuses while busy: compacting under a running turn would rebase history
// the turn is still writing to.
func (p *Program) compactNow() tea.Cmd {
	if p.compactor == nil {
		p.state.Apply(Notice{Text: "manual compaction is unavailable"})
		return nil
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "wait for the turn to finish before compacting"})
		return nil
	}
	p.mu.Lock()
	history := append([]agent.Message(nil), p.history...)
	p.mu.Unlock()
	if len(history) == 0 {
		p.state.Apply(Notice{Text: "nothing to compact: the transcript is empty"})
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		compacted, summary, err := p.compactor(ctx, history)
		if err != nil {
			p.publish(Notice{Text: "compact: " + err.Error()})
			return nil
		}
		p.mu.Lock()
		p.history = compacted
		p.mu.Unlock()
		p.publish(HistoryReplaced{Entries: EntriesFromMessages(compacted)})
		p.publish(Notice{Text: "compacted: " + summary})
		return nil
	}
}

// showProviders prints credential status per provider and kicks a background
// probe of the current endpoint. The table answers instantly from local
// sources; the probe follows as a notice because network must never block
// the event loop.
func (p *Program) showProviders() {
	if p.providers == nil {
		p.state.Apply(Notice{Text: "provider listing is unavailable"})
		return
	}
	rows := p.providers()
	if len(rows) == 0 {
		p.state.Apply(Notice{Text: "no providers known"})
		return
	}
	var sb strings.Builder
	for _, row := range rows {
		marker := "  "
		if row.ID == currentProvider(p.state.Status.Model) {
			marker = "> "
		}
		fmt.Fprintf(&sb, "%s%-14s %s\n", marker, row.ID, row.KeyDetail)
	}
	p.state.Apply(Notice{Text: strings.TrimRight(sb.String(), "\n")})
	if p.probe != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result := p.probe(ctx)
			p.publish(ProbeFinished{OK: result.OK, Detail: result.Detail})
			if result.Detail != "" {
				p.publish(Notice{Text: "endpoint: " + result.Detail})
			}
		}()
	}
}

// currentProvider splits "provider/id" for the active marker.
func currentProvider(model string) string {
	provider, _, _ := strings.Cut(model, "/")
	return provider
}

// disconnect removes a saved credential. Only file-store and explicit keys
// are removable here: environment and shell credentials are read-only by
// design, and the notice says where to unset them instead.
func (p *Program) disconnect(args []string) {
	provider := currentProvider(p.state.Status.Model)
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		provider = strings.TrimSpace(args[0])
	}
	if provider == "" {
		p.state.Apply(Notice{Text: "usage: /disconnect [provider]"})
		return
	}
	if p.deleter == nil {
		p.state.Apply(Notice{Text: "credential removal is unavailable"})
		return
	}
	outcome, err := p.deleter(provider)
	if err != nil {
		p.state.Apply(Notice{Text: "disconnect: " + err.Error()})
		return
	}
	p.state.Apply(Notice{Text: outcome})
}

// openConnectPicker starts guided setup with the provider list.
func (p *Program) openConnectPicker() {
	if p.connect.Providers == nil {
		p.state.Apply(Notice{Text: "guided setup is unavailable"})
		return
	}
	items := p.connect.Providers()
	if len(items) == 0 {
		p.state.Apply(Notice{Text: "no providers known"})
		return
	}
	p.flow = nil
	p.state.Apply(PickerOpened{Kind: "connect", Title: "connect a provider", Items: items})
}

// continueConnect advances the setup flow after a provider is chosen.
func (p *Program) continueConnect(item PickerItem) {
	provider := item.Value
	if provider == "" {
		provider = item.Title
	}
	p.flow = &connectFlow{provider: provider}
	if p.connect.Sources == nil {
		p.state.Apply(Notice{Text: "credential setup is unavailable"})
		p.flow = nil
		return
	}
	items := p.connect.Sources(provider)
	if len(items) == 0 {
		p.state.Apply(Notice{Text: "no credential sources for " + provider})
		p.flow = nil
		return
	}
	p.state.Apply(PickerOpened{Kind: "connect-src", Title: "credential for " + provider, Items: items})
}

// continueConnectSource handles a credential-source choice: "new" opens
// masked entry, anything else validates the detected credential.
func (p *Program) continueConnectSource(item PickerItem) {
	if p.flow == nil {
		return
	}
	if item.Value == "new" {
		// The picker closes first: two overlays would fight over the keys,
		// and the masked entry needs the composer row back.
		p.state.Apply(PickerClosed{})
		p.state.Apply(SecretOpened{Label: "API key for " + p.flow.provider})
		return
	}
	p.startValidation(p.flow.provider, "", false)
}

// submitSecret delivers masked input to the connect flow. It is the only path
// that consumes composer text without creating a prompt: a secret must never
// become a UserSubmitted entry.
func (p *Program) submitSecret(text string) {
	if p.flow == nil {
		p.state.Apply(SecretClosed{})
		return
	}
	if strings.TrimSpace(text) == "" {
		p.state.Apply(Notice{Text: "empty key: paste a key or press esc to cancel"})
		return
	}
	provider := p.flow.provider
	p.flow.key = strings.TrimSpace(text)
	p.flow.keyIsNew = true
	p.state.Apply(SecretClosed{})
	p.startValidation(provider, p.flow.key, true)
}

// startValidation checks a credential off the update goroutine and publishes
// the outcome. The notice keeps the transcript honest about the wait: a
// silent 20 seconds reads as a hang.
func (p *Program) startValidation(provider, key string, isNew bool) {
	if p.connect.Validate == nil {
		p.state.Apply(Notice{Text: "credential validation is unavailable"})
		p.flow = nil
		return
	}
	p.state.Apply(Notice{Text: "validating " + provider + "…"})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, detail, err := p.connect.Validate(ctx, provider, key)
		result := ValidationFinished{Provider: provider, Key: key, KeyIsNew: isNew, Models: models, Detail: detail}
		if err != nil {
			result.Err = err.Error()
		}
		p.publish(result)
	}()
}

// onValidationFinished pairs an async result with its flow. A stale result
// from an abandoned attempt is dropped, never applied: opening a picker for
// a provider the user already left would be a trap.
func (p *Program) onValidationFinished(result ValidationFinished) {
	if p.flow == nil || p.flow.provider != result.Provider {
		return
	}
	if result.Err != "" {
		p.state.Apply(Notice{Text: "connect: " + result.Err})
		p.flow = nil
		return
	}
	if len(result.Models) == 0 {
		p.state.Apply(Notice{Text: "connect: no models listed for " + result.Provider})
		p.flow = nil
		return
	}
	items := make([]PickerItem, 0, len(result.Models))
	for _, id := range result.Models {
		items = append(items, PickerItem{Title: id, Value: id})
	}
	if result.Detail != "" {
		p.state.Apply(Notice{Text: result.Detail})
	}
	p.state.Apply(PickerOpened{Kind: "connect-models", Title: "default model", Items: items})
}

// finishConnect saves the outcome and switches to the chosen model.
func (p *Program) finishConnect(item PickerItem) {
	if p.flow == nil || p.connect.Save == nil {
		p.state.Apply(Notice{Text: "saving the connection is unavailable"})
		p.flow = nil
		return
	}
	flow := *p.flow
	p.flow = nil
	if err := p.connect.Save(flow.provider, flow.key, item.Value); err != nil {
		p.state.Apply(Notice{Text: "connect: " + err.Error()})
		return
	}
	p.state.Apply(Notice{Text: "connected to " + flow.provider + " as " + item.Value})
	p.applyModelSwitch(flow.provider + "/" + item.Value)
}
