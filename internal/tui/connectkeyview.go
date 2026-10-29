package tui

// The /connect API-key modal: a centered overlay that collects a provider
// credential after the Provider Manager hands one over.
//
// It is a working modal in the same family as the Provider Manager and the
// Session Manager: while open it owns the keyboard and the middle of the
// screen, the composer hides behind it, and it owns a small edit state. It
// reuses the shared modal frame — a border-embedded title, cyan section
// headers, a full-width rule, and a structured footer bar — so the surface
// reads as one system with the rest of the workbench.
//
// The buffer is a secret: it renders as bullets only, and it never enters the
// transcript, history, or a session. Submitting hands the raw value to the
// connect hook, which validates it off the update goroutine; the modal reports
// the wait and any failure in place so the user can retry without losing what
// they pasted.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// connectKeyMaxRows bounds the masked field so a pasted credential can never
// push the frame past its budget. Only bullets render, so the tail is not the
// point: the length is.
const connectKeyMaxRows = 3

// ConnectKeyView is the open /connect API-key modal.
//
// It is a plain value embedded in the program state so the render stays pure
// and a test can build one directly. Provider is the credential row the
// Provider Manager selected; Key is the raw secret, never rendered.
type ConnectKeyView struct {
	// Provider is the credential row being connected.
	Provider ProviderRow
	// Key is the raw credential buffer. It is masked on every render and must
	// never reach the transcript or history.
	Key string
	// Validating reports an async credential check is in flight, so the field
	// is read-only and the frame says why.
	Validating bool
	// Error is the last validation failure, shown in place so the user can
	// edit the key and retry. Empty when nothing has failed.
	Error string
}

// connectKeyModalTitle embeds the provider in the modal title so the surface
// names what it is connecting, matching the plane's "MODEL CONTROL · …"
// treatment.
func connectKeyModalTitle(view ConnectKeyView) string {
	name := connectProviderName(view.Provider)
	if strings.TrimSpace(name) == "" {
		return "ADD API KEY"
	}
	return "ADD API KEY · " + strings.ToUpper(name)
}

// connectKeyInputLines renders the masked credential field: the label on the
// first row, bullets only, and a block cursor while the field is editable. The
// raw key is never rendered.
func connectKeyInputLines(view ConnectKeyView, pal *Palette, interior int) []string {
	col := modalValueCol(connectLabelWidth)
	fieldW := interior - col
	if fieldW < 4 {
		fieldW = 4
	}
	var parts []string
	if view.Key == "" {
		parts = []string{pal.Muted.Render("paste your key")}
	} else {
		masked := strings.Repeat("•", len([]rune(view.Key)))
		for _, row := range WrapToWidth(masked, fieldW) {
			parts = append(parts, pal.Choice.Render(row))
		}
		if len(parts) > connectKeyMaxRows {
			parts = append(parts[:connectKeyMaxRows-1], pal.Muted.Render("…"))
		}
	}
	if !view.Validating {
		parts[len(parts)-1] += pal.Cursor.Render("▌")
	}
	rows := make([]string, 0, len(parts))
	for i, part := range parts {
		prefix := strings.Repeat(" ", col)
		if i == 0 {
			prefix = strings.Repeat(" ", statusFieldIndent) + pal.Muted.Render(padModalLabel("Key", connectLabelWidth))
		}
		rows = append(rows, TruncateToWidth(prefix+part, interior))
	}
	return rows
}

// connectKeyHintLine explains what the blank field means: an undetected
// credential is used when one resolves, otherwise the key must be pasted.
func connectKeyHintLine(view ConnectKeyView, pal *Palette, interior int) string {
	var text string
	if view.Provider.Configured {
		detail := statusValueOr(sanitizeInline(view.Provider.KeyDetail), "a detected credential")
		text = "leave blank to use " + detail
	} else {
		id := statusValueOr(sanitizeInline(strings.TrimSpace(view.Provider.ID)), "this provider")
		text = "paste the key for " + id + " · stored only if you save it"
	}
	return TruncateToWidth(strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render(text), interior)
}

// connectKeyStatusLines reports the in-flight check or the last failure in
// place. Either way the modal stays open: a rejected key is something to fix,
// not a reason to lose the interface.
func connectKeyStatusLines(view ConnectKeyView, pal *Palette, interior int) []string {
	if view.Validating {
		line := strings.Repeat(" ", statusFieldIndent) +
			pal.ToolRun.Render("validating "+connectProviderName(view.Provider)+"…")
		return []string{TruncateToWidth(line, interior)}
	}
	if strings.TrimSpace(view.Error) == "" {
		return nil
	}
	width := interior - statusFieldIndent
	if width < 4 {
		width = 4
	}
	var rows []string
	for _, row := range WrapToWidth(sanitizeInline(view.Error), width) {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.ToolError.Render(row))
	}
	return rows
}

// connectKeyModalContent builds the modal body: the provider facts, a rule,
// then the credential field and its status.
func connectKeyModalContent(view ConnectKeyView, pal *Palette, interior int) []string {
	field := func(label, value string) []string {
		return modalField(label, value, pal, interior, 0, connectLabelWidth)
	}
	rows := statusBlock("CONNECT PROVIDER", pal, interior,
		field("Provider", connectProviderPair(view.Provider)),
		field("Driver", connectDriverText(view.Provider)),
		field("Status", connectStatusLine(view.Provider)),
		field("Credential", connectDetailText(view.Provider)),
	)
	rows = append(rows, statusRule(pal, interior))
	rows = append(rows, statusSectionHeader("API KEY", interior))
	rows = append(rows, connectKeyInputLines(view, pal, interior)...)
	rows = append(rows, connectKeyHintLine(view, pal, interior))
	rows = append(rows, connectKeyStatusLines(view, pal, interior)...)
	return rows
}

// connectKeyFooterBar names the keys that work right now: validate while the
// field is editable, cancel always.
func connectKeyFooterBar(view ConnectKeyView, pal *Palette, interior int) string {
	line := strings.Repeat(" ", statusFieldIndent)
	if view.Validating {
		line += pal.Bold.Render("Esc") + pal.Muted.Render(" cancel")
	} else {
		line += pal.Bold.Render("↵") + pal.Muted.Render(" validate  ·  ") +
			pal.Bold.Render("Esc") + pal.Muted.Render(" cancel")
	}
	return TruncateToWidth(line, interior)
}

// RenderConnectKeyModal composes the /connect API-key modal as an ANSI string.
//
// width is the total rendered width, border included; height, when positive,
// bounds the total rendered height so the dialog never overflows a short
// viewport. The caller centers the returned block in the terminal.
func RenderConnectKeyModal(view ConnectKeyView, pal *Palette, width, height int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	if width <= 0 {
		width = connectCardHiWidth
	}
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}
	content := connectKeyModalContent(view, pal, interior)
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		content = fitStatusRows(content, height-4)
	}
	footer := connectKeyFooterBar(view, pal, interior)
	return renderModalFrameWithFooter(connectKeyModalTitle(view), content, footer, pal, interior)
}

// renderConnectKeyArea draws the API-key modal centered over the workbench,
// using the same placement as the Provider Manager.
func renderConnectKeyArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	view := state.ConnectKey
	if view == nil {
		return
	}
	renderCardAreaWidth(sb, state, layout, connectCardWidth, func(width, height int) string {
		return RenderConnectKeyModal(*view, pal, width, height)
	})
}

// openConnectKey opens the API-key modal for a chosen provider and resets the
// in-progress setup. The provider row is the snapshot the Provider Manager
// showed, so the two surfaces agree about what is being connected.
func (p *Program) openConnectKey(row ProviderRow) {
	p.flow = &connectFlow{provider: row.ID}
	p.state.Apply(ConnectKeyOpened{View: ConnectKeyView{Provider: row}})
}

// submitConnectKey hands the buffered credential to the validation hook. An
// empty buffer means "use the detected credential", matching the hook's
// contract; when nothing is detected there, the modal says so instead of
// spawning a check that is guaranteed to fail.
func (p *Program) submitConnectKey() {
	view := p.state.ConnectKey
	if view == nil || p.flow == nil || view.Validating {
		return
	}
	key := strings.TrimSpace(view.Key)
	if key == "" && !view.Provider.Configured {
		id := statusValueOr(sanitizeInline(strings.TrimSpace(view.Provider.ID)), "this provider")
		p.state.Apply(ConnectKeyFailed{Error: "paste a key for " + id})
		return
	}
	p.flow.key = key
	p.state.Apply(ConnectKeyValidating{})
	p.startValidation(view.Provider.ID, key, key != "")
}

// cancelConnectKey dismisses the modal and abandons the setup flow.
func (p *Program) cancelConnectKey() {
	p.state.Apply(ConnectKeyClosed{})
	p.flow = nil
}

// handleConnectKeyEntry routes keys to the open API-key modal.
//
// Precedence is fixed: esc/ctrl+c cancel the whole setup; enter validates
// (ignored while a check is in flight); backspace/ctrl+u edit the buffer;
// printable input appends. Nothing typed here may become a prompt — that path
// would persist the key into history.
func (p *Program) handleConnectKeyEntry(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	view := p.state.ConnectKey
	if view == nil {
		return p, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		p.cancelConnectKey()
	case "enter":
		p.submitConnectKey()
	case "ctrl+u":
		if !view.Validating {
			p.state.Apply(ConnectKeyChanged{Key: ""})
		}
	case "backspace", "ctrl+h":
		if !view.Validating {
			if runes := []rune(view.Key); len(runes) > 0 {
				p.state.Apply(ConnectKeyChanged{Key: string(runes[:len(runes)-1])})
			}
		}
	default:
		if view.Validating {
			return p, nil
		}
		switch msg.Type {
		case tea.KeyRunes:
			p.state.Apply(ConnectKeyChanged{Key: view.Key + string(msg.Runes)})
		case tea.KeySpace:
			p.state.Apply(ConnectKeyChanged{Key: view.Key + " "})
		}
	}
	return p, nil
}
