package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/tools"
	"golang.org/x/term"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/creds"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/tui"
)

// harness is the shared backend behind slash commands in both interfaces.
//
// The full-screen UI and the plain REPL must agree on what a command means:
// a command recognised in one and ignored by the other is a bug by
// construction. Anything that needs the registry, the store, or settings
// lives here, so both paths call the same code. Presentation differs (the UI
// wraps answers in notices and pickers; the REPL prints), but decisions do
// not.
type harness struct {
	registry *ai.Registry
	loop     *agent.Loop
	store    *session.Store
	sess     *session.Session
	usage    *agent.UsageLog
	settings *config.Settings
	root     string
	model    ai.Model
}

// newHarness assembles the backend. usage and settings may be nil only where
// the caller guarantees those commands are unreachable; every method guards
// its own dependencies and says so rather than panicking.
func newHarness(registry *ai.Registry, loop *agent.Loop, store *session.Store, sess *session.Session, usage *agent.UsageLog, settings *config.Settings, root string, model ai.Model) *harness {
	return &harness{
		registry: registry, loop: loop, store: store, sess: sess,
		usage: usage, settings: settings, root: root, model: model,
	}
}

// ProviderRows lists every catalog provider with its credential status. No
// network is touched: reachability is a probe, and probes run on demand
// (see ProbeCurrent), never as a side effect of listing.
func (h *harness) ProviderRows() []tui.ProviderRow {
	var rows []tui.ProviderRow
	for _, name := range catalog.ProviderNames() {
		row := tui.ProviderRow{ID: name, Name: providerDisplayName(name)}
		_, source, err := h.registry.ResolveAPIKeySource(name)
		switch {
		case err == nil:
			row.KeyDetail = source
		case name == "ollama":
			row.KeyDetail = "none required"
		default:
			row.KeyDetail = "missing: " + keyHint(err)
		}
		rows = append(rows, row)
	}
	return rows
}

// providerDisplayName names a provider for humans from the catalog's own
// metadata. An undescribed key shows as itself rather than a guess.
func providerDisplayName(name string) string {
	if display, ok := catalog.Describe(name); ok {
		return display
	}
	return name
}

// keyHint extracts the actionable tail of a missing-credential error ("set
// X or Y") so listings stay one line.
func keyHint(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "(set "); i >= 0 {
		return strings.TrimSuffix(msg[i+len("(set "):], ")")
	}
	return "no credential"
}

// ProbeCurrent checks the active model's endpoint without spending a
// completion. Reachability and credential acceptance are all it claims;
// tool-call health needs a real turn, which it says rather than implies.
func (h *harness) ProbeCurrent(ctx context.Context) tui.ProbeFinished {
	baseURL := h.model.BaseURL
	if override, ok := h.registry.BaseURLOverride(h.model.Provider); ok {
		baseURL = override
	}
	key, _ := h.registry.ResolveAPIKey(h.model.Provider)
	result := ai.ProbeProvider(ctx, nil, h.model.API, baseURL, key)
	return tui.ProbeFinished{OK: result.Reachable && result.Authenticated, Detail: result.Detail}
}

// StatusReport renders /status from local state only.
func (h *harness) StatusReport(history []agent.Message, queued int, theme string, busy bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "model: %s/%s\n", h.model.Provider, h.model.ID)
	if h.model.ContextWindow > 0 {
		fmt.Fprintf(&sb, "context window: %s tokens\n", formatInt(h.model.ContextWindow))
	}
	fmt.Fprintf(&sb, "turns: %d", len(history))
	if h.sess != nil {
		fmt.Fprintf(&sb, " · session #%d", h.sess.ID)
	}
	if queued > 0 {
		fmt.Fprintf(&sb, " · %d queued", queued)
	}
	if busy {
		sb.WriteString(" · working")
	}
	fmt.Fprintf(&sb, "\ntheme: %s", theme)
	return sb.String()
}

// UsageReport renders /usage from the shared ledger.
func (h *harness) UsageReport() string {
	if h.usage == nil {
		return "usage tracking is unavailable"
	}
	return h.usage.Report(false)
}

// DoctorLines runs the same diagnostics as `ohgo doctor`, minus the workdir
// row the CLI adds around them. One implementation serves both: a check that
// passes in one interface and fails in the other would be a lie in one of
// them.
func (h *harness) DoctorLines() []tui.DoctorLine {
	registry := h.registry
	if registry == nil {
		registry = ai.NewRegistry()
	}
	reports := collectReports("", registry)
	out := make([]tui.DoctorLine, 0, len(reports))
	for _, rep := range reports {
		out = append(out, tui.DoctorLine{Name: rep.Name, OK: rep.OK, Detail: rep.Detail})
	}
	return out
}

func (h *harness) settingsOrDefault() config.Settings {
	if h.settings == nil {
		return config.Default()
	}
	return *h.settings
}

// SwitchModel resolves a reference (static, then live discovery) and moves
// the loop, the ledger pricing, and the harness model together. Splitting
// those updates would leave the status line naming a model the loop is not
// calling, or value new tokens at old prices.
func (h *harness) SwitchModel(ctx context.Context, ref string) (ai.Model, error) {
	resolve := dynamicResolver(ctx, h.registry)
	model, err := resolve(ref)
	if err != nil {
		return ai.Model{}, err
	}
	// A nil loop serves tests that exercise resolution without a runtime;
	// production always drives a real one.
	if h.loop != nil {
		h.loop.SetModel(model)
	}
	h.model = model
	if h.usage != nil {
		h.usage.SetCost(model.Cost)
	}
	return model, nil
}

// CompactHistory summarises a history on demand, returning the rebuilt
// conversation. An empty history is refused rather than summarised: a
// summary of nothing would replace the transcript with a handover to
// nowhere.
func (h *harness) CompactHistory(ctx context.Context, history []agent.Message) ([]agent.Message, string, error) {
	if len(history) == 0 {
		return nil, "", fmt.Errorf("nothing to compact: the transcript is empty")
	}
	window := h.model.ContextWindow
	if window <= 0 {
		return nil, "", fmt.Errorf("compact: the model has no context window to compact into")
	}
	result, err := agent.Compact(ctx, history, compactionFromSettings(h.settingsOrDefault()), window,
		newSummarizer(h.registry.StreamFn(), h.model))
	if err != nil {
		return nil, "", err
	}
	summary := fmt.Sprintf("%d messages summarised, %d retained", result.Compacted, result.Retained)
	return result.Messages, summary, nil
}

// Disconnect removes a saved credential. Only file-store and explicit keys
// are removable here: environment and shell credentials are read-only by
// design, and the answer says where to unset them instead of pretending.
func (h *harness) Disconnect(provider string) (string, error) {
	if strings.TrimSpace(provider) == "" {
		return "", fmt.Errorf("usage: /disconnect [provider]")
	}
	_, source, err := h.registry.ResolveAPIKeySource(provider)
	if err != nil {
		return "", err
	}
	switch source {
	case "explicit":
		h.registry.SetAPIKey(provider, "")
		return fmt.Sprintf("removed the in-session credential for %s", provider), nil
	case "credential file":
		path, pathErr := creds.DefaultPath()
		if pathErr != nil {
			return "", pathErr
		}
		store, openErr := creds.Open(path)
		if openErr != nil {
			return "", openErr
		}
		if err := store.Delete(provider); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed the saved credential for %s", provider), nil
	default:
		return "", fmt.Errorf("the %s credential comes from %s: unset it there rather than here", provider, source)
	}
}

// ResumeSession loads a stored session for /sessions resume in the REPL: the
// history continues the conversation and the session pointer moves with it.
func (h *harness) ResumeSession(id int64) (history []agent.Message, notice string, err error) {
	if h.store == nil {
		return nil, "", fmt.Errorf("no session store attached")
	}
	sess, err := h.store.Get(id)
	if err != nil {
		return nil, "", err
	}
	stored, err := h.store.Messages(id, 0)
	if err != nil {
		return nil, "", fmt.Errorf("load session %d: %w", id, err)
	}
	history, err = session.Replay(stored)
	if err != nil {
		return nil, "", fmt.Errorf("load session %d: %w", id, err)
	}
	h.sess = &sess
	return history, fmt.Sprintf("resumed session %d (%d messages)", id, len(history)), nil
}

// ListSessions prints stored sessions newest-first for the REPL.
func (h *harness) ListSessions(out io.Writer) {
	if h.store == nil {
		_, _ = fmt.Fprintln(out, "no session store attached")
		return
	}
	list, err := h.store.List(20)
	if err != nil {
		_, _ = fmt.Fprintf(out, "sessions: %v\n", err)
		return
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(out, "No sessions yet.")
		return
	}
	for _, sess := range list {
		title := sess.Title
		if title == "" {
			title = sess.Model
		}
		_, _ = fmt.Fprintf(out, "#%-5d %-16s %-28s %s\n",
			sess.ID, sess.UpdatedAt.Format("2006-01-02 15:04"), title, sess.Cwd)
	}
	_, _ = fmt.Fprintln(out, "resume with /resume <id>")
}

// JobsReport prints background jobs for the REPL. The table is shared with
// the UI's /jobs through the same bash tool instance the loop drives.
func (h *harness) JobsReport() string {
	for _, tool := range h.loop.Tools() {
		bash, ok := tool.(*tools.BashTool)
		if !ok {
			continue
		}
		return formatJobs(bash.Jobs().Snapshot())
	}
	return "job listing is unavailable"
}

// formatJobs renders snapshots for humans. It lives here (not in tools) so
// the UI picker and the REPL print agree without sharing a renderer they
// would then both have to import.
func formatJobs(snapshots []tools.JobInfo) string {
	if len(snapshots) == 0 {
		return "no background jobs"
	}
	var sb strings.Builder
	for _, job := range snapshots {
		status := fmt.Sprintf("exit %d", job.ExitCode)
		if job.Running {
			status = "running, " + job.Elapsed.Round(time.Millisecond).String()
		}
		fmt.Fprintf(&sb, "%s %s (%s)\n", job.ID, job.Command, status)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ModelList returns static catalog ids for the /models browser seed.
func (h *harness) ModelList() []string {
	var ids []string
	for _, model := range catalog.List() {
		ids = append(ids, model.Provider+"/"+model.ID)
	}
	sort.Strings(ids)
	return ids
}

// SaveConnect persists a /connect outcome: the pasted key goes to the
// credential file (0600, never the shell), the model goes to settings and
// the loop together. An empty key keeps whatever credential resolved.
func (h *harness) SaveConnect(provider, key, modelRef string) error {
	if strings.TrimSpace(key) != "" {
		path, err := creds.DefaultPath()
		if err != nil {
			return err
		}
		store, err := creds.Open(path)
		if err != nil {
			return err
		}
		if err := store.Set(provider, strings.TrimSpace(key)); err != nil {
			return err
		}
	}
	model, err := h.SwitchModel(context.Background(), modelRef)
	if err != nil {
		return err
	}
	if h.settings != nil {
		h.settings.Model = model.Provider + "/" + model.ID
		if err := config.Save(*h.settings); err != nil {
			return fmt.Errorf("connect: save settings: %w", err)
		}
	}
	return nil
}

// ConnectREPL runs guided setup on a line reader. Prompts are numbered
// choices rather than free text wherever the answer comes from a list: free
// text where a number belongs is how typos become configuration.
func (h *harness) ConnectREPL(out io.Writer, in io.Reader) error {
	reader := bufio.NewReader(in)
	rows := h.ProviderRows()
	if len(rows) == 0 {
		return fmt.Errorf("no providers known")
	}
	_, _ = fmt.Fprintln(out, "Choose a provider:")
	for i, row := range rows {
		_, _ = fmt.Fprintf(out, "  %2d  %-14s %s\n", i+1, row.ID, row.KeyDetail)
	}
	choice, err := readChoice(reader, out, len(rows))
	if err != nil {
		return err
	}
	provider := rows[choice-1].ID

	_, source, keyErr := h.registry.ResolveAPIKeySource(provider)
	options := []string{"paste a new key"}
	if keyErr == nil {
		options = append(options, "use detected credential ("+source+")")
	}
	_, _ = fmt.Fprintln(out, "Credential:")
	for i, option := range options {
		_, _ = fmt.Fprintf(out, "  %2d  %s\n", i+1, option)
	}
	useDetected := false
	if len(options) > 1 {
		pick, err := readChoice(reader, out, len(options))
		if err != nil {
			return err
		}
		useDetected = pick == 2
	}
	key := ""
	if !useDetected {
		key, err = readSecret(out, reader, in, "API key: ")
		if err != nil {
			return err
		}
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("empty key: paste a key or redo /connect to cancel")
		}
	}

	_, _ = fmt.Fprintln(out, "validating…")
	validateKey := ""
	if !useDetected {
		validateKey = key
	}
	models, _, err := h.validateProvider(context.Background(), provider, validateKey)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("no models listed for %s", provider)
	}
	_, _ = fmt.Fprintln(out, "Models (Enter for the first):")
	shown := models
	if len(shown) > 30 {
		shown = shown[:30]
	}
	for i, id := range shown {
		_, _ = fmt.Fprintf(out, "  %2d  %s\n", i+1, id)
	}
	_, _ = fmt.Fprint(out, "model [1]: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	model := models[0]
	if pick := strings.TrimSpace(line); pick != "" {
		if n, nerr := atoi(pick); nerr == nil && n >= 1 && n <= len(shown) {
			model = shown[n-1]
		} else {
			model = pick
		}
	}
	saveKey := ""
	if !useDetected {
		saveKey = key
	}
	if err := h.SaveConnect(provider, saveKey, provider+"/"+model); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "connected to %s as %s\n", provider, model)
	return nil
}

// readChoice reads a 1-based menu choice.
func readChoice(reader *bufio.Reader, out io.Writer, max int) (int, error) {
	_, _ = fmt.Fprintf(out, "choice [1-%d]: ", max)
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read input: %w", err)
	}
	n, nerr := atoi(strings.TrimSpace(line))
	if nerr != nil || n < 1 || n > max {
		return 0, fmt.Errorf("enter a number between 1 and %d", max)
	}
	return n, nil
}

// readSecret reads a credential, masked when the input is a terminal. Piped
// input echoes — unavoidable without a TTY — and the prompt says so rather
// than pretending otherwise.
func readSecret(out io.Writer, reader *bufio.Reader, in io.Reader, prompt string) (string, error) {
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(out, prompt)
		secret, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return string(secret), nil
	}
	_, _ = fmt.Fprintf(out, "%s (input will echo: not a terminal)\n> ", prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// atoi parses a menu number.
func atoi(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
