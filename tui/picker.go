package tui

import (
	"strings"

	"github.com/PizenLabs/ohgo/agent"
)

// Picker key precedence (single handler, one meaning per key).
//
// The picker is always search-focused: there are no competing modes, so no
// two handlers consume the same message.
//
//  1. esc: clear a non-empty query first, close only when already empty.
//     ctrl+c always closes.
//  2. enter: select the highlighted item.
//  3. up/down, ctrl+p/ctrl+n: move one. pgup/pgdn: move one viewport.
//     home/end: first/last result.
//  4. left/right, ctrl+b/ctrl+f: move the query cursor. backspace/ctrl+h
//     deletes before it, delete/ctrl+d deletes under it.
//  5. ctrl+r: refresh a stale model catalog (models picker only).
//  6. Printable input, including space, inserts into the query at the cursor.
//
// Up/Down navigate the list rather than moving the text cursor by deliberate
// policy: the query is a single line, Left/Right serve text movement, and a
// filter box whose arrows edit text cannot be scrolled. The footer states
// this contract so it is discoverable, not surprising.

// Picker kinds. The kind tells the program what choosing means.
const (
	// PickerSessions resumes a stored session.
	PickerSessions = "sessions"
	// PickerModels switches the model.
	PickerModels = "models"
	// PickerJobs inspects background jobs.
	PickerJobs = "jobs"
	// PickerNewSession creates a fresh session. Choosing the start row
	// creates exactly one session; choosing cancel or dismissing (Esc)
	// creates none.
	PickerNewSession = "new-session"
	// PickerTasks resumes a resumable durable task. Choosing attaches the
	// selected task to the current session.
	PickerTasks = "tasks"
	// PickerMCP manages MCP server enablement. Space stages a change and
	// Enter applies it; no network work happens while navigating.
	PickerMCP = "mcp"
)

// PickerItem is one choice in a selection overlay.
type PickerItem struct {
	// Title is the primary line, e.g. a session id and model.
	Title string
	// Detail is the secondary line, e.g. the working directory.
	Detail string
	// Value is the opaque payload the program acts on: a session id, a
	// model reference, or a job id.
	Value string
	// Provider identifies the source catalog for model rows, e.g.
	// "openrouter". Empty on non-model rows. Matching covers it, so
	// searching "openrouter" finds that provider's models.
	Provider string
	// Name is the display name for model rows, distinct from the canonical
	// id in Value. Empty defaults to Title for matching.
	Name string
	// Free reports verified zero pricing: every known price leg is zero.
	Free bool
	// PriceKnown reports pricing metadata backs Free. When false, Free is
	// meaningless and the row must not claim any price; the "free" query
	// token then falls back to matching "free" in the name or id text.
	PriceKnown bool
	// Context is the context window in tokens, or zero when unknown.
	Context int
	// Active marks the current model row.
	Active bool
	// State is the management state for an MCP row: "enabled", "disabled", or
	// "error". Empty for other picker kinds.
	State string
	// Trust is MCP provenance metadata, shown without implying authority.
	Trust string
	// Count is an optional capability count for an MCP row. Cached reports
	// whether Count is known.
	Count int
	// Cached reports that Count came from a capability snapshot.
	Cached bool
	// Connected reports a live MCP connection. Enabled alone is not a
	// connection claim.
	Connected bool
	// Pending reports an unapplied MCP enable/disable change on this row.
	Pending bool
}

// searchText folds every matchable field of an item into one haystack.
func (item PickerItem) searchText() string {
	name := item.Name
	if name == "" {
		name = item.Title
	}
	return strings.ToLower(strings.Join([]string{
		item.Title, item.Detail, item.Value, item.Provider, name,
	}, "\n"))
}

// PickerState is an open selection overlay.
//
// The picker owns a small list state machine: Filter is the query,
// FilterCursor the rune offset within it, Cursor the selected index into the
// filtered result set, Offset the first visible row, and VisibleRows the
// rows available for results after header, search, help, and footer.
// Loading/Refreshing/ErrMsg/Origin describe catalog status for model rows;
// other kinds leave them zero. Provider scoping stays in the query text:
// plain tokens match provider ids and names, and "provider:<name>" tokens
// pin one provider while composing with the rest of the query.
type PickerState struct {
	// Kind selects what choosing means.
	Kind string
	// Title heads the overlay.
	Title string
	// Items are all choices, in the order the provider gave.
	Items []PickerItem
	// Filter narrows the visible items.
	Filter string
	// FilterCursor is the rune offset in Filter where typing lands.
	FilterCursor int
	// Cursor selects within the visible items.
	Cursor int
	// Offset is the first visible row of the filtered items.
	Offset int
	// VisibleRows bounds the rendered result window. Zero or negative
	// renders everything (unknown terminal size).
	VisibleRows int
	// Loading reports the first catalog snapshot has not arrived yet.
	Loading bool
	// Refreshing reports a background refresh is in flight.
	Refreshing bool
	// ErrMsg carries a failed refresh in words, keeping cached rows.
	ErrMsg string
	// Origin names the snapshot behind the rows: "live", "cached",
	// "bundled", "loading", or "" when the kind has no catalog.
	Origin string
}

// NewPicker opens an overlay over items.
func NewPicker(kind, title string, items []PickerItem) *PickerState {
	return &PickerState{Kind: kind, Title: title, Items: append([]PickerItem(nil), items...)}
}

// queryTokens splits the filter on whitespace for AND matching. Empty query
// yields no tokens, matching everything (subject to the provider filter).
func queryTokens(filter string) []string {
	return strings.Fields(strings.ToLower(filter))
}

// Filtered returns the visible items: provider filter first, then every
// query token matched case-insensitively.
//
// A "provider:<name>" token sets the provider filter instead of matching
// text, so it composes with other tokens. A "free" token matches verified
// zero-price rows, falling back to "free" appearing in the name or id when
// pricing is unknown — and an unknown price never renders as a verified
// "free" claim. All other tokens must each appear somewhere in the item's
// searchable text, so "deepseek free", "qwen", "vision", and
// "openrouter/vendor/model-id" all narrow naturally without exact ids.
func (p *PickerState) Filtered() []PickerItem {
	if p == nil {
		return nil
	}
	var out []PickerItem
	for _, item := range p.Items {
		if matchItem(item, queryTokens(p.Filter)) {
			out = append(out, item)
		}
	}
	return out
}

// Visible returns the filtered items, in order.
func (p *PickerState) Visible() []PickerItem {
	return p.Filtered()
}

// matchItem reports whether every token matches the item.
func matchItem(item PickerItem, tokens []string) bool {
	return matchFields(item.searchText(), item.Provider, item.PriceKnown, item.Free, tokens)
}

// matchFields reports whether every query token matches a haystack, provider
// identity, and pricing flags.
//
// It is the single owner of token semantics: a "provider:<name>" token pins a
// provider, a "free" token matches verified zero pricing and falls back to the
// text only when pricing is unknown (never presenting an unknown price as a
// verified claim), and every other token must appear in the haystack. The
// picker and the model control plane both route through it so a search cannot
// mean one thing in one surface and something else in the other.
func matchFields(hay, provider string, priceKnown, free bool, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	for _, tok := range tokens {
		if pinned, ok := strings.CutPrefix(tok, "provider:"); ok {
			if !strings.EqualFold(provider, pinned) &&
				!strings.Contains(strings.ToLower(provider), pinned) {
				return false
			}
			continue
		}
		if tok == "free" {
			if priceKnown {
				if !free {
					return false
				}
				continue
			}
			if !strings.Contains(hay, "free") {
				return false
			}
			continue
		}
		if !strings.Contains(hay, tok) {
			return false
		}
	}
	return true
}

// Count reports the filtered result size.
func (p *PickerState) Count() int {
	return len(p.Filtered())
}

// ProviderScope names the pinned provider when the query carries a
// "provider:<name>" token, or "" for all providers. The header renders it
// so the scope is always explicit.
func (p *PickerState) ProviderScope() string {
	for _, tok := range queryTokens(p.Filter) {
		if provider, ok := strings.CutPrefix(tok, "provider:"); ok && provider != "" {
			return provider
		}
	}
	return ""
}

// Move shifts the highlight, wrapping at both ends, keeping it visible.
func (p *PickerState) Move(delta int) {
	visible := p.Filtered()
	if len(visible) == 0 {
		p.Cursor = 0
		p.Offset = 0
		return
	}
	p.Cursor = (p.Cursor + delta) % len(visible)
	if p.Cursor < 0 {
		p.Cursor += len(visible)
	}
	p.EnsureVisible()
}

// Page shifts the highlight by approximately one viewport without wrapping:
// paging past an end parks at that end, which is what "one screenful" means.
func (p *PickerState) Page(delta int) {
	visible := p.Filtered()
	if len(visible) == 0 {
		p.Cursor = 0
		p.Offset = 0
		return
	}
	step := p.pageStep()
	p.Cursor += delta * step
	p.clampCursor(len(visible))
	p.EnsureVisible()
}

// Top parks the highlight on the first result; Bottom on the last.
func (p *PickerState) Top() {
	if len(p.Filtered()) == 0 {
		p.Cursor, p.Offset = 0, 0
		return
	}
	p.Cursor = 0
	p.EnsureVisible()
}

// Bottom parks the highlight on the last result.
func (p *PickerState) Bottom() {
	visible := p.Filtered()
	if len(visible) == 0 {
		p.Cursor, p.Offset = 0, 0
		return
	}
	p.Cursor = len(visible) - 1
	p.EnsureVisible()
}

// pageStep moves approximately one viewport, or a fixed step when the size
// is unknown (a test, or a terminal that never reported it).
func (p *PickerState) pageStep() int {
	if p.VisibleRows > 1 {
		return p.VisibleRows
	}
	return 10
}

// EnsureVisible adjusts the scroll offset so the highlight is on screen.
// The marker is therefore always rendered inside the viewport, never above
// it, below it, or on a row showing another item.
func (p *PickerState) EnsureVisible() {
	n := len(p.Filtered())
	if n == 0 {
		p.Cursor, p.Offset = 0, 0
		return
	}
	p.clampCursor(n)
	rows := p.VisibleRows
	if rows <= 0 || rows >= n {
		p.Offset = 0
		return
	}
	if p.Cursor < p.Offset {
		p.Offset = p.Cursor
	}
	if p.Cursor >= p.Offset+rows {
		p.Offset = p.Cursor - rows + 1
	}
	maxOffset := n - rows
	if p.Offset > maxOffset {
		p.Offset = maxOffset
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
}

// Window returns the rows to render: at most VisibleRows starting at Offset.
func (p *PickerState) Window() []PickerItem {
	visible := p.Filtered()
	if p.VisibleRows <= 0 || len(visible) <= p.VisibleRows {
		return visible
	}
	p.EnsureVisible()
	end := p.Offset + p.VisibleRows
	if end > len(visible) {
		end = len(visible)
	}
	return visible[p.Offset:end]
}

// windowOf slices a precomputed filtered set into the visible window,
// returning the rows and their starting offset. Computing the filter once
// keeps thousand-model catalogs cheap: one pass per render, not one per row.
func (p *PickerState) windowOf(filtered []PickerItem) ([]PickerItem, int) {
	if p.VisibleRows <= 0 || len(filtered) <= p.VisibleRows {
		return filtered, 0
	}
	p.EnsureVisible()
	end := p.Offset + p.VisibleRows
	if end > len(filtered) {
		end = len(filtered)
	}
	if p.Offset > len(filtered) {
		return nil, len(filtered)
	}
	return filtered[p.Offset:end], p.Offset
}

// SetVisibleRows resizes the result window, e.g. on WindowSizeMsg, keeping
// the highlight on screen.
func (p *PickerState) SetVisibleRows(rows int) {
	if rows < 0 {
		rows = 0
	}
	p.VisibleRows = rows
	p.EnsureVisible()
}

// SetItems replaces the roster (a catalog refresh), preserving the
// highlight by value when the selected model survives and clamping
// otherwise. Typing is never lost: the filter stays, and selection falls
// back to the top rather than pointing past the end.
func (p *PickerState) SetItems(items []PickerItem) {
	selected := ""
	if chosen, ok := p.Chosen(); ok {
		selected = chosen.Value
	}
	p.Items = append([]PickerItem(nil), items...)
	p.Cursor = 0
	if selected != "" {
		for i, item := range p.Filtered() {
			if item.Value == selected {
				p.Cursor = i
				break
			}
		}
	}
	p.clamp()
	p.EnsureVisible()
}

// SetFilter replaces the query, parking the text cursor at the end and
// re-clamping the highlight into the narrowed list.
func (p *PickerState) SetFilter(text string) {
	p.Filter = text
	p.FilterCursor = len([]rune(text))
	p.clamp()
	p.EnsureVisible()
}

// InsertFilterText inserts s at the query cursor.
func (p *PickerState) InsertFilterText(s string) {
	if s == "" {
		return
	}
	runes := []rune(p.Filter)
	cur := p.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	out := make([]rune, 0, len(runes)+len([]rune(s)))
	out = append(out, runes[:cur]...)
	out = append(out, []rune(s)...)
	out = append(out, runes[cur:]...)
	p.Filter = string(out)
	p.FilterCursor = cur + len([]rune(s))
	p.clamp()
	p.EnsureVisible()
}

// DeleteFilterBefore removes the rune ahead of the query cursor's left.
func (p *PickerState) DeleteFilterBefore() {
	runes := []rune(p.Filter)
	cur := p.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	if cur == 0 {
		return
	}
	p.Filter = string(append(runes[:cur-1], runes[cur:]...))
	p.FilterCursor = cur - 1
	p.clamp()
	p.EnsureVisible()
}

// DeleteFilterAt removes the rune under the query cursor.
func (p *PickerState) DeleteFilterAt() {
	runes := []rune(p.Filter)
	cur := p.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	if cur >= len(runes) {
		return
	}
	p.Filter = string(append(runes[:cur], runes[cur+1:]...))
	p.FilterCursor = cur
	p.clamp()
	p.EnsureVisible()
}

// MoveFilterCursor shifts the query cursor, clamped to the text.
func (p *PickerState) MoveFilterCursor(delta int) {
	p.FilterCursor += delta
	n := len([]rune(p.Filter))
	if p.FilterCursor < 0 {
		p.FilterCursor = 0
	}
	if p.FilterCursor > n {
		p.FilterCursor = n
	}
}

// ClearFilter empties the query and restores the highlight to the top.
func (p *PickerState) ClearFilter() {
	p.Filter = ""
	p.FilterCursor = 0
	p.Cursor = 0
	p.EnsureVisible()
}

// clampCursor bounds the highlight to the result set.
func (p *PickerState) clampCursor(n int) {
	if n <= 0 {
		p.Cursor = 0
		return
	}
	if p.Cursor >= n {
		p.Cursor = n - 1
	}
	if p.Cursor < 0 {
		p.Cursor = 0
	}
}

// clamp keeps the cursor inside the visible items. Filtering narrows the
// list under a parked cursor, so every filter change re-clamps.
func (p *PickerState) clamp() {
	p.clampCursor(len(p.Filtered()))
	if p.Offset < 0 {
		p.Offset = 0
	}
}

// TogglePending flips the highlighted MCP row's desired enabled state.
//
// It changes only picker state: no hook runs, no server starts, and no file is
// written. The change becomes real only when Enter applies it.
func (p *PickerState) TogglePending() {
	if p == nil || p.Kind != PickerMCP {
		return
	}
	chosen, ok := p.Chosen()
	if !ok || chosen.Value == MCPAddValue {
		return
	}
	// Chosen returns a copy from the filtered view, so the mutation has to
	// land on the owned roster. MCP has no filter, but matching by Value keeps
	// this correct if one is ever added.
	for i := range p.Items {
		item := &p.Items[i]
		if item.Value != chosen.Value {
			continue
		}
		item.Active = !item.Active
		item.Pending = true
		if item.Active {
			item.State = "enabled"
		} else {
			item.State = "disabled"
		}
		return
	}
}

// PendingCount reports how many MCP rows carry an unapplied change.
func (p *PickerState) PendingCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, item := range p.Items {
		if item.Pending {
			n++
		}
	}
	return n
}

// Chosen returns the highlighted item, if any.
func (p *PickerState) Chosen() (PickerItem, bool) {
	visible := p.Filtered()
	if p.Cursor < 0 || p.Cursor >= len(visible) {
		return PickerItem{}, false
	}
	return visible[p.Cursor], true
}

// samePickerItems reports whether two rosters are indistinguishable to the
// picker. A refresh that returns the same rows must not rebuild the list:
// replacing it would reset scroll state and redraw for no new information.
func samePickerItems(a, b []PickerItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// optionsToItems converts enriched catalog rows into picker rows,
// preserving provider identity, pricing flags, and context so search and
// free-filtering work on metadata, not on rendered strings.
func optionsToItems(options []ModelOption) []PickerItem {
	items := make([]PickerItem, 0, len(options))
	for _, opt := range options {
		title := opt.Title
		if title == "" {
			title = opt.ID
		}
		items = append(items, PickerItem{
			Title:      title,
			Detail:     opt.Detail,
			Value:      opt.ID,
			Provider:   opt.Provider,
			Name:       opt.Name,
			Free:       opt.Free,
			PriceKnown: opt.PriceKnown,
			Context:    opt.Context,
			Active:     opt.Active,
		})
	}
	return items
}

// SessionItem is one stored session for the picker and the Session Manager.
//
// The optional fields are producer-owned: the Session Manager renders what it
// is given and keeps an unmeasured fact absent rather than inventing one.
type SessionItem struct {
	// ID is the database identifier.
	ID int64
	// Title is the human label, falling back to the model and date.
	Title string
	// Model is the model the session used.
	Model string
	// Updated is a short human timestamp.
	Updated string
	// Cwd is the working directory.
	Cwd string
	// Active marks the session the program currently has loaded. It drives
	// the Session Manager's STATUS column.
	Active bool
	// Dirty reports the session's working tree held uncommitted changes when
	// the roster was listed.
	Dirty bool
	// DirtyKnown distinguishes a measured clean tree from an unmeasured one:
	// an unknown row renders a neutral marker, never a fabricated "clean".
	DirtyKnown bool
	// Tokens is the token count reconstructed from the stored transcript, or
	// zero when the provider did not measure one.
	Tokens int
	// Prompt is the latest user prompt, or empty when not loaded.
	Prompt string
}

// JobItem is one background job for the picker.
type JobItem struct {
	// ID is the stable handle, e.g. "job-1".
	ID string
	// Command is the shell text.
	Command string
	// Running reports the process has not exited.
	Running bool
	// ExitCode is valid only when Running is false.
	ExitCode int
	// Elapsed bounds the run so far.
	Elapsed string
	// Tail is the last output bytes, for the detail view.
	Tail string
}

// OpenedSession is what resuming a session hands the program: history for
// the model, entries for the transcript, and a notice confirming the switch.
type OpenedSession struct {
	// History continues the conversation.
	History []agent.Message
	// Entries replays the transcript.
	Entries []Entry
	// Notice confirms the resume in the transcript.
	Notice string
}
