package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	tea "github.com/charmbracelet/bubbletea"
)

// ProviderRow is one provider for the /usage inspector's provider roster, the
// model control plane's provider pane, and the /connect Provider Manager.
type ProviderRow struct {
	// ID is the stable provider key.
	ID string
	// Name is the display name.
	Name string
	// Driver names the wire protocol the provider is reached through, e.g.
	// "Anthropic Messages" or "OpenAI Chat". It is a display fact the producer
	// resolves; empty renders as unknown rather than a guessed transport.
	Driver string
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

// ProvidersFunc lists providers with credential status for /usage.
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
	// Providers lists setup candidates with credential status. It feeds the
	// Provider Manager's roster, so each row carries the display name, the
	// wire protocol, and whether a credential resolves.
	Providers func() []ProviderRow
	// Validate checks a credential (empty key means the detected one) and
	// returns the model ids to choose from.
	Validate func(ctx context.Context, provider, key string) (models []string, detail string, err error)
	// Save persists the outcome; an empty key keeps the stored credential.
	Save func(provider, key, model string) error
}

// connectFlow tracks an in-progress setup. It lives on the Program and is
// touched only from the update goroutine.
type connectFlow struct {
	provider string
	key      string
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

// startValidation checks a credential off the update goroutine and publishes
// the outcome. The modal owns the wait state; nothing is written to the
// transcript, so a secret can never be echoed there.
func (p *Program) startValidation(provider, key string, isNew bool) {
	if p.connect.Validate == nil {
		p.state.Apply(ConnectKeyFailed{Error: "credential validation is unavailable"})
		return
	}
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
// a provider the user already left would be a trap. A failure stays in the
// key modal so the credential can be fixed without redoing the flow.
func (p *Program) onValidationFinished(result ValidationFinished) {
	if p.flow == nil || p.flow.provider != result.Provider {
		return
	}
	if result.Err != "" {
		p.state.Apply(ConnectKeyFailed{Error: result.Err})
		return
	}
	if len(result.Models) == 0 {
		p.state.Apply(ConnectKeyFailed{Error: "no models listed for " + result.Provider})
		return
	}
	items := make([]PickerItem, 0, len(result.Models))
	for _, id := range result.Models {
		items = append(items, PickerItem{Title: id, Value: id})
	}
	if result.Detail != "" {
		p.state.Apply(Notice{Text: result.Detail})
	}
	p.state.Apply(ConnectKeyClosed{})
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
