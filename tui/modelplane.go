package tui

import (
	"strings"
)

// The model control plane is a persistent split-pane configuration surface,
// not a picker. It keeps provider context and model context visible at once,
// exposes a model's authoritative capabilities, and routes every change
// through the caller-supplied hooks — the program never owns durable
// configuration, and the plane never invents a model fact.
//
// Two modes share one topology:
//
//	model mode (default)      roles mode (Tab)
//	┌────────────┬──────────┐ ┌────────────┬──────────┐
//	│ PROVIDERS  │ MODELS   │ │ ROLES      │ASSIGNMENT│
//	└────────────┴──────────┘ └────────────┴──────────┘
//
// Focus moves Provider -> Model -> Role -> Effort with Tab (or ←/→) and
// Enter advances Provider into Models; Enter on a model assigns it to the
// staged role (opening the variant prompt only when the model needs a
// variant choice), while Enter on a role stages it and returns to model
// search so its replacement can be found. The Roles pane only displays each
// role's current model, and effort is a visible radio on its own stop —
// never a second stacked popup.

// PlaneMode selects what the control plane is editing.
type PlaneMode string

// Plane modes.
const (
	// PlaneModels browses providers and models and applies a model to a role.
	PlaneModels PlaneMode = "model"
	// PlaneRoles inspects and assigns the five model roles.
	PlaneRoles PlaneMode = "roles"
)

// PlaneFocus names which control owns the keyboard in the control plane.
//
// Focus is explicit state: every key is interpreted against it, and Tab
// cycles through it deterministically. Search owns all printable input;
// no other focus does.
type PlaneFocus int

// Plane focus targets.
const (
	// PlaneFocusProviders puts the keyboard on the provider pane.
	PlaneFocusProviders PlaneFocus = iota
	// PlaneFocusSearch puts the keyboard in the search/filter input.
	PlaneFocusSearch
	// PlaneFocusModels puts the keyboard on the model pane.
	PlaneFocusModels
	// PlaneFocusEffort puts the keyboard on the thinking-effort control.
	PlaneFocusEffort
	// PlaneFocusRoles puts the keyboard on role assignment.
	PlaneFocusRoles
)

// planeFocusOrder is the deterministic Tab cycle for the streamlined flow:
// providers, models, roles, effort, and back to providers. Search is not a
// Tab stop: typing anywhere in model mode focuses it, Esc leaves it, so the
// main path stays Provider -> Model -> Role -> Effort without an extra stop.
// Search remains last for Shift+Tab reachability from providers.
var planeFocusOrder = []PlaneFocus{
	PlaneFocusProviders,
	PlaneFocusModels,
	PlaneFocusRoles,
	PlaneFocusEffort,
	PlaneFocusSearch,
}

// EffortOff is the thinking option that disables reasoning for a model that
// otherwise exposes a reasoning ladder. It is a first-class choice, not an
// empty string: "off" is a deliberate decision, distinct from "unset".
const EffortOff = "off"

// AllProviders is the synthetic provider row that disables provider filtering,
// so a user can search the whole roster without first choosing a provider. It
// is honest: it is labelled as a scope, not a provider.
const AllProviders = ""

// RoleAssignment is one role's effective configuration, for display and edit.
//
// It is built by the caller (which owns the catalog and the configuration
// authority); the plane only renders it and hands edits back. Every field that
// describes the model comes from authoritative metadata: the plane never
// fabricates a capability or an effort ladder.
type RoleAssignment struct {
	// Role is the role name, e.g. "build".
	Role string
	// Model is the effective "provider/id" reference, or "" when unset.
	Model string
	// Provider is the provider key of the effective model.
	Provider string
	// Name is the model's display name.
	Name string
	// Effort is the effective thinking effort, or "" when none is set.
	Effort string
	// Source names which configuration layer won: "role", "workspace",
	// "default", or "builtin". It is shown so the layering is never hidden.
	Source string
	// Efforts is the assigned model's authoritative effort ladder. Empty
	// means the model exposes no explicit levels.
	Efforts []string
	// Reasoning reports the assigned model accepts thinking.
	Reasoning bool
	// ReasoningKnown reports Reasoning is authoritative (unsupported vs
	// unknown).
	ReasoningKnown bool
	// DefaultModel reports the reference the session resolved for this role
	// at startup, when it differs from Model (a session snapshot override).
	SessionModel string
	// Err carries the last assignment error for this role, if any.
	Err string
}

// ModelPlaneState is the persistent split-pane model control surface.
//
// It owns only presentation state: cursors, the filter, the pending effort,
// and the roster the caller handed it. It never persists anything itself.
type ModelPlaneState struct {
	// Mode selects model browsing or role assignment.
	Mode PlaneMode
	// Focus selects the pane the keyboard drives in model mode.
	Focus PlaneFocus

	// Providers is the provider pane's roster (including the All row).
	Providers []ProviderRow
	// ProviderCursor selects the highlighted provider.
	ProviderCursor int
	// ProviderOffset is the first visible provider row.
	ProviderOffset int

	// Models is the full model roster; the visible set is filtered by the
	// selected provider and the query.
	Models []ModelOption
	// ModelCursor selects the highlighted model.
	ModelCursor int
	// ModelOffset is the first visible model row.
	ModelOffset int

	// Roles is the role pane's roster.
	Roles []RoleAssignment
	// RoleCursor selects the highlighted role.
	RoleCursor int

	// Filter narrows the model list.
	Filter string
	// FilterCursor is the rune offset in Filter where typing lands.
	FilterCursor int

	// Effort is the thinking effort chosen for the highlighted model. It is
	// applied with the model when a role is assigned. Empty means the model
	// exposes no ladder.
	Effort string

	// Variant is the open choose-a-variant prompt, or nil. While set the
	// keyboard picks a variant row and nothing else assigns.
	Variant *PlaneVariantState

	// ActiveRole is the role an Enter applies to in model mode.
	ActiveRole string

	// VisibleRows bounds the rendered list window (both panes share it).
	VisibleRows int

	// Loading reports the first catalog snapshot has not arrived yet.
	Loading bool
	// Refreshing reports a background refresh is in flight.
	Refreshing bool
	// Origin names the snapshot: "live", "cached", "bundled", "loading".
	Origin string
	// Stale reports cached rows shown while a refresh runs.
	Stale bool
	// ErrMsg carries a failed refresh in words, keeping cached rows.
	ErrMsg string
	// Notice is the last confirmation or refusal, shown in the footer.
	Notice string
}

// NewModelPlane opens the control plane over a roster and role list.
func NewModelPlane(opened ModelPlaneOpened) *ModelPlaneState {
	plane := &ModelPlaneState{
		Mode:       PlaneModels,
		Focus:      PlaneFocusProviders,
		Providers:  append([]ProviderRow(nil), opened.Providers...),
		Models:     append([]ModelOption(nil), opened.Models...),
		Roles:      append([]RoleAssignment(nil), opened.Roles...),
		ActiveRole: opened.ActiveRole,
		Loading:    opened.Loading,
		Origin:     opened.Origin,
		Stale:      opened.Stale,
	}
	plane.selectActiveProvider()
	plane.syncEffort()
	return plane
}

// selectActiveProvider parks the provider cursor on the current model's
// provider, so the pane opens where the user already is.
func (p *ModelPlaneState) selectActiveProvider() {
	active := ""
	for _, model := range p.Models {
		if model.Active {
			active = model.Provider
			break
		}
	}
	if active == "" {
		return
	}
	for i, row := range p.Providers {
		if row.ID == active {
			p.ProviderCursor = i
			return
		}
	}
}

// SelectedProvider returns the highlighted provider row.
func (p *ModelPlaneState) SelectedProvider() (ProviderRow, bool) {
	if p.ProviderCursor < 0 || p.ProviderCursor >= len(p.Providers) {
		return ProviderRow{}, false
	}
	return p.Providers[p.ProviderCursor], true
}

// ProviderScope names the pinned provider, or "" when the All row is selected.
func (p *ModelPlaneState) ProviderScope() string {
	row, ok := p.SelectedProvider()
	if !ok {
		return ""
	}
	return row.ID
}

// VisibleModels returns the models matching the provider scope and query.
func (p *ModelPlaneState) VisibleModels() []ModelOption {
	scope := p.ProviderScope()
	tokens := queryTokens(p.Filter)
	var out []ModelOption
	for _, model := range p.Models {
		if scope != "" && model.Provider != scope {
			continue
		}
		if !matchFields(modelHaystack(model), model.Provider, model.PriceKnown, model.Free, tokens) {
			continue
		}
		out = append(out, model)
	}
	return out
}

// modelHaystack folds a model's matchable fields into one lowercase string.
// Capabilities are included so "vision" and "think" narrow the list.
func modelHaystack(model ModelOption) string {
	return strings.ToLower(strings.Join([]string{
		model.Title, model.Detail, model.ID, model.Provider, model.Name,
		strings.Join(model.Capabilities, " "),
	}, "\n"))
}

// ChosenModel returns the highlighted model, if the visible set is non-empty.
func (p *ModelPlaneState) ChosenModel() (ModelOption, bool) {
	visible := p.VisibleModels()
	if p.ModelCursor < 0 || p.ModelCursor >= len(visible) {
		return ModelOption{}, false
	}
	return visible[p.ModelCursor], true
}

// ChosenRole returns the highlighted role in roles mode.
func (p *ModelPlaneState) ChosenRole() (RoleAssignment, bool) {
	if p.RoleCursor < 0 || p.RoleCursor >= len(p.Roles) {
		return RoleAssignment{}, false
	}
	return p.Roles[p.RoleCursor], true
}

// Move shifts the highlight in the focused pane, wrapping and keeping it in
// view. In roles mode the role cursor moves regardless of focus. Search
// focus navigates the model list without leaving the input; effort focus
// is driven by CycleEffort, so Move leaves it untouched.
func (p *ModelPlaneState) Move(delta int) {
	if p.Mode == PlaneRoles || p.Focus == PlaneFocusRoles {
		p.RoleCursor = wrap(p.RoleCursor+delta, len(p.Roles))
		return
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor = wrap(p.ProviderCursor+delta, len(p.Providers))
		// Provider selection re-scopes the model list: reset the model cursor
		// to the top of the new scope rather than leaving it pointing past the
		// end of a narrowed set.
		p.ModelCursor = 0
		p.ModelOffset = 0
		p.syncEffort()
		p.ensureProviderVisible()
		p.ensureModelVisible()
		return
	}
	if p.Focus == PlaneFocusEffort {
		// The effort stop is driven by CycleEffort, never by list motion:
		// a scroll wheel over the dialog must not move the model
		// highlight out from under the radio.
		return
	}
	p.ModelCursor = wrap(p.ModelCursor+delta, len(p.VisibleModels()))
	p.syncEffort()
	p.ensureModelVisible()
}

// focusIndex reports the position of the current focus in the Tab order.
func (p *ModelPlaneState) focusIndex() int {
	for i, focus := range planeFocusOrder {
		if focus == p.Focus {
			return i
		}
	}
	return 0
}

// setFocus moves the keyboard to a focus stop and keeps the mode in step:
// the roles panes render exactly when the roles stop owns the keyboard.
func (p *ModelPlaneState) setFocus(focus PlaneFocus) {
	p.Focus = focus
	if focus == PlaneFocusRoles {
		p.Mode = PlaneRoles
	} else if p.Mode == PlaneRoles {
		p.Mode = PlaneModels
	}
}

// NextFocus advances the keyboard to the next control in the Tab order.
func (p *ModelPlaneState) NextFocus() {
	p.setFocus(planeFocusOrder[(p.focusIndex()+1)%len(planeFocusOrder)])
}

// PrevFocus moves the keyboard to the previous control in the Tab order.
func (p *ModelPlaneState) PrevFocus() {
	p.setFocus(planeFocusOrder[(p.focusIndex()+len(planeFocusOrder)-1)%len(planeFocusOrder)])
}

// FocusName reports the focused control for the footer and tests. An open
// variant prompt owns the keyboard regardless of the underlying focus stop.
func (p *ModelPlaneState) FocusName() string {
	if p.Variant != nil {
		return "variant"
	}
	switch p.Focus {
	case PlaneFocusProviders:
		return "providers"
	case PlaneFocusSearch:
		return "search"
	case PlaneFocusModels:
		return "models"
	case PlaneFocusEffort:
		return "effort"
	case PlaneFocusRoles:
		return "roles"
	default:
		return "models"
	}
}

// Page shifts the highlight by approximately one viewport, parking at the ends.
// On the effort stop it moves to the first or last option by direction.
func (p *ModelPlaneState) Page(delta int) {
	if p.Mode == PlaneRoles || p.Focus == PlaneFocusRoles {
		step := p.VisibleRows
		if step <= 1 {
			step = 10
		}
		p.RoleCursor += delta * step
		p.RoleCursor = clampIndex(p.RoleCursor, len(p.Roles))
		return
	}
	if p.Focus == PlaneFocusEffort {
		if delta < 0 {
			p.effortEnd(false)
		} else {
			p.effortEnd(true)
		}
		return
	}
	step := p.VisibleRows
	if step <= 1 {
		step = 10
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor += delta * step
		p.ProviderCursor = clampIndex(p.ProviderCursor, len(p.Providers))
		p.ModelCursor, p.ModelOffset = 0, 0
		p.syncEffort()
		p.ensureProviderVisible()
		return
	}
	p.ModelCursor += delta * step
	p.ModelCursor = clampIndex(p.ModelCursor, len(p.VisibleModels()))
	p.syncEffort()
	p.ensureModelVisible()
}

// Top parks the highlight on the first row of the focused pane, or the
// first effort option on the effort stop.
func (p *ModelPlaneState) Top() {
	if p.Mode == PlaneRoles || p.Focus == PlaneFocusRoles {
		p.RoleCursor = 0
		return
	}
	if p.Focus == PlaneFocusEffort {
		p.effortEnd(false)
		return
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor = 0
		p.ModelCursor, p.ModelOffset = 0, 0
		p.syncEffort()
		p.ensureProviderVisible()
		return
	}
	p.ModelCursor = 0
	p.syncEffort()
	p.ensureModelVisible()
}

// Bottom parks the highlight on the last row of the focused pane, or the
// last effort option on the effort stop.
func (p *ModelPlaneState) Bottom() {
	if p.Mode == PlaneRoles || p.Focus == PlaneFocusRoles {
		p.RoleCursor = clampIndex(len(p.Roles)-1, len(p.Roles))
		return
	}
	if p.Focus == PlaneFocusEffort {
		p.effortEnd(true)
		return
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor = clampIndex(len(p.Providers)-1, len(p.Providers))
		p.ModelCursor, p.ModelOffset = 0, 0
		p.syncEffort()
		p.ensureProviderVisible()
		return
	}
	p.ModelCursor = clampIndex(len(p.VisibleModels())-1, len(p.VisibleModels()))
	p.syncEffort()
	p.ensureModelVisible()
}

// FocusModels moves the keyboard from the provider pane to the model pane.
func (p *ModelPlaneState) FocusModels() { p.setFocus(PlaneFocusModels) }

// FocusProviders moves the keyboard to the provider pane.
func (p *ModelPlaneState) FocusProviders() { p.setFocus(PlaneFocusProviders) }

// FocusSearch moves the keyboard into the search input.
func (p *ModelPlaneState) FocusSearch() { p.setFocus(PlaneFocusSearch) }

// FocusEffort moves the keyboard onto the effort control.
func (p *ModelPlaneState) FocusEffort() { p.setFocus(PlaneFocusEffort) }

// FocusRoles moves the keyboard onto role assignment.
func (p *ModelPlaneState) FocusRoles() { p.setFocus(PlaneFocusRoles) }

// ToggleMode swaps between model browsing and role assignment, keeping the
// selected model so an assignment never requires re-finding it.
func (p *ModelPlaneState) ToggleMode() {
	if p.Mode == PlaneRoles {
		p.Mode = PlaneModels
		return
	}
	p.Mode = PlaneRoles
}

// SetFilter replaces the query, parking the text cursor at the end.
func (p *ModelPlaneState) SetFilter(text string) {
	p.Filter = text
	p.FilterCursor = len([]rune(text))
	p.ModelCursor, p.ModelOffset = 0, 0
	p.syncEffort()
}

// InsertFilterText inserts s at the query cursor.
func (p *ModelPlaneState) InsertFilterText(s string) {
	if s == "" {
		return
	}
	runes := []rune(p.Filter)
	cur := clampIndex(p.FilterCursor, len(runes)+1)
	if cur > len(runes) {
		cur = len(runes)
	}
	out := make([]rune, 0, len(runes)+len([]rune(s)))
	out = append(out, runes[:cur]...)
	out = append(out, []rune(s)...)
	out = append(out, runes[cur:]...)
	p.Filter = string(out)
	p.FilterCursor = cur + len([]rune(s))
	p.ModelCursor, p.ModelOffset = 0, 0
	p.syncEffort()
}

// DeleteFilterBefore removes the rune before the query cursor.
func (p *ModelPlaneState) DeleteFilterBefore() {
	runes := []rune(p.Filter)
	cur := p.FilterCursor
	if cur <= 0 || cur > len(runes) {
		return
	}
	p.Filter = string(append(runes[:cur-1], runes[cur:]...))
	p.FilterCursor = cur - 1
	p.ModelCursor, p.ModelOffset = 0, 0
	p.syncEffort()
}

// DeleteFilterAt removes the rune under the query cursor.
func (p *ModelPlaneState) DeleteFilterAt() {
	runes := []rune(p.Filter)
	cur := p.FilterCursor
	if cur < 0 || cur >= len(runes) {
		return
	}
	p.Filter = string(append(runes[:cur], runes[cur+1:]...))
	p.ModelCursor, p.ModelOffset = 0, 0
	p.syncEffort()
}

// MoveFilterCursor shifts the query cursor, clamped to the text.
func (p *ModelPlaneState) MoveFilterCursor(delta int) {
	p.FilterCursor = clampIndex(p.FilterCursor+delta, len([]rune(p.Filter))+1)
}

// ClearFilter empties the query.
func (p *ModelPlaneState) ClearFilter() {
	p.Filter = ""
	p.FilterCursor = 0
	p.ModelCursor, p.ModelOffset = 0, 0
	p.syncEffort()
}

// EffortOptions returns the thinking choices the highlighted model exposes.
//
// A model with an explicit ladder offers "off" plus each level; a model that
// accepts thinking but exposes no ladder offers no choices (support alone is
// reported); a model the metadata marks unsupported offers none. The plane
// never renders a control for a capability the model does not expose.
func (p *ModelPlaneState) EffortOptions() []string {
	model, ok := p.ChosenModel()
	if !ok {
		return nil
	}
	return effortOptions(model)
}

// effortOptions is the ladder a model exposes, derived only from authoritative
// metadata.
func effortOptions(model ModelOption) []string {
	if len(model.ThinkingEfforts) == 0 {
		return nil
	}
	out := make([]string, 0, len(model.ThinkingEfforts)+1)
	out = append(out, EffortOff)
	out = append(out, model.ThinkingEfforts...)
	return out
}

// CycleEffort moves the pending effort among the highlighted model's options.
// It is a no-op when the model exposes no ladder.
func (p *ModelPlaneState) CycleEffort(delta int) {
	options := p.EffortOptions()
	if len(options) == 0 {
		return
	}
	index := 0
	for i, option := range options {
		if option == p.Effort {
			index = i
			break
		}
	}
	index = wrap(index+delta, len(options))
	p.Effort = options[index]
}

// SplitModelVariant separates a "provider/id" reference into its base and
// its variant suffix. A variant is the trailing ":suffix" on the id — the
// ":free" / ":nitro" endpoint style — so "openrouter/foo/bar:free" splits
// into base "openrouter/foo/bar" and variant ":free", while a reference
// without a colon has an empty variant. The split is purely syntactic: it
// never claims a suffix exists as a callable row.
func SplitModelVariant(ref string) (base, variant string) {
	provider, id, ok := splitProviderRef(ref)
	if !ok {
		return ref, ""
	}
	name, suffix, ok := cutLast(id, ":")
	if !ok || suffix == "" {
		return ref, ""
	}
	return provider + "/" + name, ":" + suffix
}

// splitProviderRef cuts a "provider/id" reference. It reports false when the
// reference has no usable provider part.
func splitProviderRef(ref string) (provider, id string, ok bool) {
	cut := strings.Index(ref, "/")
	if cut <= 0 || cut+1 >= len(ref) {
		return "", "", false
	}
	return ref[:cut], ref[cut+1:], true
}

// cutLast cuts s at its last occurrence of sep.
func cutLast(s, sep string) (before, after string, ok bool) {
	last := strings.LastIndex(s, sep)
	if last < 0 {
		return "", "", false
	}
	return s[:last], s[last+len(sep):], true
}

// PlaneVariantState is the choose-a-variant prompt for one base model. It is
// modal within the dialog: while non-nil the keyboard picks a variant and
// nothing else assigns.
type PlaneVariantState struct {
	// Base is the variant-less reference the prompt was opened from.
	Base string
	// Options are the callable rows sharing the base: the bare row first
	// when the roster has it, then each suffixed sibling in roster order.
	// Every option is a real catalog row, never a fabricated suffix.
	Options []ModelOption
	// Cursor selects the highlighted option.
	Cursor int
}

// VariantSiblings returns the callable rows sharing a model's base: the bare
// row when present plus each suffixed sibling. Matching is exact on the
// base, so "openrouter/foo/barista" never joins "openrouter/foo/bar".
func (p *ModelPlaneState) VariantSiblings(model ModelOption) []ModelOption {
	base, _ := SplitModelVariant(model.ID)
	var out []ModelOption
	for _, candidate := range p.Models {
		if candidate.Provider != model.Provider {
			continue
		}
		candidateBase, _ := SplitModelVariant(candidate.ID)
		if candidateBase != base {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

// NeedsVariantChoice reports whether assigning a model must first ask which
// variant: the highlighted row is a bare base with suffixed siblings in the
// roster. A suffixed row is already specific and assigns directly.
func (p *ModelPlaneState) NeedsVariantChoice(model ModelOption) bool {
	if _, variant := SplitModelVariant(model.ID); variant != "" {
		return false
	}
	siblings := p.VariantSiblings(model)
	if len(siblings) <= 1 {
		return false
	}
	for _, sibling := range siblings {
		if _, variant := SplitModelVariant(sibling.ID); variant != "" {
			return true
		}
	}
	return false
}

// OpenVariant starts the variant prompt for the highlighted model. It
// reports false — and changes nothing — when no choice is needed.
func (p *ModelPlaneState) OpenVariant() bool {
	model, ok := p.ChosenModel()
	if !ok || !p.NeedsVariantChoice(model) {
		return false
	}
	base, _ := SplitModelVariant(model.ID)
	p.Variant = &PlaneVariantState{Base: base, Options: p.VariantSiblings(model)}
	return true
}

// MoveVariant shifts the variant highlight, wrapping at both ends.
func (p *ModelPlaneState) MoveVariant(delta int) {
	variant := p.Variant
	if variant == nil || len(variant.Options) == 0 {
		return
	}
	variant.Cursor = wrap(variant.Cursor+delta, len(variant.Options))
}

// EndVariant parks the variant highlight on the first or last option.
func (p *ModelPlaneState) EndVariant(last bool) {
	variant := p.Variant
	if variant == nil || len(variant.Options) == 0 {
		return
	}
	if last {
		variant.Cursor = len(variant.Options) - 1
		return
	}
	variant.Cursor = 0
}

// ChosenVariant returns the highlighted variant row, if the prompt is open.
func (p *ModelPlaneState) ChosenVariant() (ModelOption, bool) {
	variant := p.Variant
	if variant == nil || len(variant.Options) == 0 {
		return ModelOption{}, false
	}
	cursor := clampIndex(variant.Cursor, len(variant.Options))
	variant.Cursor = cursor
	return variant.Options[cursor], true
}

// CancelVariant dismisses the prompt without choosing anything.
func (p *ModelPlaneState) CancelVariant() { p.Variant = nil }

// VariantAssignment builds the assignment for the chosen variant: the
// sibling row's full id — variant suffix included — with the pending effort
// re-validated against that row's own ladder. A pending effort the sibling
// does not expose falls back to its first option rather than failing at the
// authority seam.
func (p *ModelPlaneState) VariantAssignment() (RoleAssignment, bool) {
	variant, ok := p.ChosenVariant()
	if !ok {
		return RoleAssignment{}, false
	}
	role := p.ActiveRole
	if p.Mode == PlaneRoles {
		chosen, ok := p.ChosenRole()
		if !ok {
			return RoleAssignment{}, false
		}
		role = chosen.Role
	}
	effort := p.Effort
	options := effortOptions(variant)
	if len(options) == 0 {
		effort = ""
	} else {
		matched := false
		for _, option := range options {
			if option == effort {
				matched = true
				break
			}
		}
		if !matched {
			effort = options[0]
		}
	}
	return RoleAssignment{Role: role, Model: variant.ID, Effort: effort}, true
}

// effortEnd parks the pending effort on the first or last option.
func (p *ModelPlaneState) effortEnd(last bool) {
	options := p.EffortOptions()
	if len(options) == 0 {
		return
	}
	if last {
		p.Effort = options[len(options)-1]
		return
	}
	p.Effort = options[0]
}

// SetEffort selects an explicit effort option when the highlighted model
// exposes it. Unknown values are refused so the UI can never stage an
// effort the model does not accept.
func (p *ModelPlaneState) SetEffort(option string) {
	for _, candidate := range p.EffortOptions() {
		if candidate == option {
			p.Effort = option
			return
		}
	}
}

// syncEffort re-validates the pending effort against the highlighted model.
//
// A model change must never leave an effort the new model does not accept:
// that would persist an effort the provider rejects. When the pending effort
// is invalid, the active role's own effort is preferred (so re-opening a role
// shows what it is set to), otherwise "off".
func (p *ModelPlaneState) syncEffort() {
	options := p.EffortOptions()
	if len(options) == 0 {
		p.Effort = ""
		return
	}
	for _, option := range options {
		if option == p.Effort {
			return
		}
	}
	if role, ok := p.roleByModel(p.ActiveRole); ok && role.Effort != "" {
		for _, option := range options {
			if option == role.Effort {
				p.Effort = role.Effort
				return
			}
		}
	}
	p.Effort = options[0]
}

// roleByModel returns the role assignment for a role name.
func (p *ModelPlaneState) roleByModel(name string) (RoleAssignment, bool) {
	for _, role := range p.Roles {
		if role.Role == name {
			return role, true
		}
	}
	return RoleAssignment{}, false
}

// NextRole cycles the active role in model mode, so Enter applies elsewhere
// without a trip through roles mode.
func (p *ModelPlaneState) NextRole(delta int) {
	if len(p.Roles) == 0 {
		return
	}
	index := 0
	for i, role := range p.Roles {
		if role.Role == p.ActiveRole {
			index = i
			break
		}
	}
	index = wrap(index+delta, len(p.Roles))
	p.ActiveRole = p.Roles[index].Role
	p.syncEffort()
}

// StageRole picks the highlighted role as the assignment target and returns
// to model browsing so the user can search for and select its replacement.
// It is the Roles-mode Enter contract: the Roles pane only displays each
// role's current model, and choosing a role stages it instead of assigning
// a hidden model highlight. The model pane's search then acts as the small
// picker for that role; Enter there assigns to the staged ActiveRole.
// It reports false when no role is highlighted.
func (p *ModelPlaneState) StageRole() bool {
	chosen, ok := p.ChosenRole()
	if !ok {
		return false
	}
	p.ActiveRole = chosen.Role
	p.RoleCursor = clampIndex(p.RoleCursor, len(p.Roles))
	// Return to model browsing with the keyboard on the model list: typing
	// auto-focuses search, so the picker is one keystroke away without an
	// extra Enter from search to results.
	p.setFocus(PlaneFocusModels)
	p.syncEffort()
	p.ensureModelVisible()
	p.Notice = "select model for " + strings.ToUpper(chosen.Role) + " — Enter assigns"
	return true
}

// Assignment builds the assignment the user is about to apply: the highlighted
// model with the pending effort, targeting the active role (model mode) or the
// highlighted role (roles mode).
func (p *ModelPlaneState) Assignment() (RoleAssignment, bool) {
	model, ok := p.ChosenModel()
	if !ok {
		return RoleAssignment{}, false
	}
	role := p.ActiveRole
	if p.Mode == PlaneRoles {
		chosen, ok := p.ChosenRole()
		if !ok {
			return RoleAssignment{}, false
		}
		role = chosen.Role
	}
	return RoleAssignment{
		Role:   role,
		Model:  model.ID,
		Effort: p.Effort,
	}, true
}

// ApplyAssignment records the result of an assignment attempt: the applied
// role list replaces the plane's, the notice reports the outcome, and the
// active role parks on what was just assigned.
func (p *ModelPlaneState) ApplyAssignment(result RoleAssignment, roles []RoleAssignment, notice string) {
	if roles != nil {
		p.Roles = roles
	}
	if result.Role != "" {
		p.ActiveRole = result.Role
	}
	p.Notice = notice
	p.syncEffort()
	p.clampRoleCursor()
}

// SetRoles replaces the role roster (e.g. after a refresh).
func (p *ModelPlaneState) SetRoles(roles []RoleAssignment) {
	p.Roles = append([]RoleAssignment(nil), roles...)
	p.clampRoleCursor()
}

// SetModels replaces the model roster, preserving the highlighted model by id
// when it survives and clamping otherwise.
func (p *ModelPlaneState) SetModels(models []ModelOption) {
	selected := ""
	if model, ok := p.ChosenModel(); ok {
		selected = model.ID
	}
	p.Models = append([]ModelOption(nil), models...)
	p.ModelCursor, p.ModelOffset = 0, 0
	if selected != "" {
		for i, model := range p.VisibleModels() {
			if model.ID == selected {
				p.ModelCursor = i
				break
			}
		}
	}
	p.syncEffort()
	p.ensureModelVisible()
}

// SetVisibleRows resizes the list window, keeping the highlights in view.
func (p *ModelPlaneState) SetVisibleRows(rows int) {
	if rows < 0 {
		rows = 0
	}
	p.VisibleRows = rows
	p.ensureModelVisible()
	p.ensureProviderVisible()
}

// ensureModelVisible adjusts the model scroll offset so the highlight is shown.
func (p *ModelPlaneState) ensureModelVisible() {
	n := len(p.VisibleModels())
	if n == 0 {
		p.ModelCursor, p.ModelOffset = 0, 0
		return
	}
	p.ModelCursor = clampIndex(p.ModelCursor, n)
	rows := p.VisibleRows
	if rows <= 0 || rows >= n {
		p.ModelOffset = 0
		return
	}
	if p.ModelCursor < p.ModelOffset {
		p.ModelOffset = p.ModelCursor
	}
	if p.ModelCursor >= p.ModelOffset+rows {
		p.ModelOffset = p.ModelCursor - rows + 1
	}
	if max := n - rows; p.ModelOffset > max {
		p.ModelOffset = max
	}
	if p.ModelOffset < 0 {
		p.ModelOffset = 0
	}
}

// ensureProviderVisible adjusts the provider scroll offset so the highlight is
// shown.
func (p *ModelPlaneState) ensureProviderVisible() {
	n := len(p.Providers)
	if n == 0 {
		p.ProviderCursor, p.ProviderOffset = 0, 0
		return
	}
	p.ProviderCursor = clampIndex(p.ProviderCursor, n)
	rows := p.VisibleRows
	if rows <= 0 || rows >= n {
		p.ProviderOffset = 0
		return
	}
	if p.ProviderCursor < p.ProviderOffset {
		p.ProviderOffset = p.ProviderCursor
	}
	if p.ProviderCursor >= p.ProviderOffset+rows {
		p.ProviderOffset = p.ProviderCursor - rows + 1
	}
	if max := n - rows; p.ProviderOffset > max {
		p.ProviderOffset = max
	}
	if p.ProviderOffset < 0 {
		p.ProviderOffset = 0
	}
}

// clampRoleCursor keeps the role highlight inside the roster.
func (p *ModelPlaneState) clampRoleCursor() {
	p.RoleCursor = clampIndex(p.RoleCursor, len(p.Roles))
}

// window slices a list into the visible window with its start offset.
func window[T any](items []T, offset, rows int) ([]T, int) {
	if rows <= 0 || len(items) <= rows {
		return items, 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + rows
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end], offset
}

// wrap folds an index back into [0,n) without ever panicking on n<=0.
func wrap(index, n int) int {
	if n <= 0 {
		return 0
	}
	index %= n
	if index < 0 {
		index += n
	}
	return index
}

// clampIndex bounds an index to [0,n-1], or 0 when empty.
func clampIndex(index, n int) int {
	if n <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= n {
		return n - 1
	}
	return index
}
