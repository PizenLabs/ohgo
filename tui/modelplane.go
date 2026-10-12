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
// Assignment is a linear, capability-aware transaction. Browsing mutates
// nothing; a pending assignment lives only in the plane's transient state until
// the user applies it. The two top-level modes share one flow:
//
//	model mode (default)          roles mode (Tab)
//	MODEL -> ROLE -> EFFORT ->    ROLES -> MODEL -> EFFORT ->
//	APPLY -> MODEL                APPLY -> ROLES
//
// The current workspace supplies the default focused role in the model-first
// flow; it is a navigation convenience, never an implicit assignment. An
// effort ladder comes only from the selected model's authoritative metadata,
// so the picker can never stage a value the model does not expose. Effort is
// part of the flow, not a separate Tab stop.

// PlaneMode selects what the control plane is editing.
type PlaneMode string

// Plane modes.
const (
	// PlaneModels browses providers and models and applies a model to a role.
	PlaneModels PlaneMode = "model"
	// PlaneRoles inspects and assigns the five model roles.
	PlaneRoles PlaneMode = "roles"
)

// PlaneStage names the step of the assignment transaction. Browsing is not a
// transaction; the other stages form the linear, capability-aware flow:
//
//	model mode: MODEL -> ROLE -> EFFORT -> APPLY
//	roles mode: ROLES -> MODEL -> EFFORT -> APPLY
//
// A stage holds only transient UI state. Nothing is persisted until APPLY, so
// Esc at any stage discards the pending assignment without touching
// configuration.
type PlaneStage int

// Assignment stages.
const (
	// PlaneStageBrowse is the resting state: MODEL or ROLES depending on Mode.
	PlaneStageBrowse PlaneStage = iota
	// PlaneStageRole chooses the role a selected model is assigned to.
	PlaneStageRole
	// PlaneStageModel chooses the model for a selected role.
	PlaneStageModel
	// PlaneStageEffort chooses the thinking effort; Enter applies.
	PlaneStageEffort
	// PlaneStageApply applies a model that exposes no effort ladder, where
	// there is no effort step to confirm.
	PlaneStageApply
)

// PlaneFocus names which control owns the keyboard while browsing.
//
// Focus is explicit state: every key is interpreted against it, and Tab
// switches between the MODEL and ROLES modes. Search owns all printable
// input; no other focus does.
type PlaneFocus int

// Plane focus targets.
const (
	// PlaneFocusProviders puts the keyboard on the provider pane.
	PlaneFocusProviders PlaneFocus = iota
	// PlaneFocusSearch puts the keyboard in the search/filter input.
	PlaneFocusSearch
	// PlaneFocusModels puts the keyboard on the model pane.
	PlaneFocusModels
	// PlaneFocusEffort is retained for callers that explicitly address the
	// effort control; the linear flow reaches it through PlaneStageEffort.
	PlaneFocusEffort
	// PlaneFocusRoles is retained for callers that explicitly address role
	// assignment; the flow reaches it through the role stage or ROLES mode.
	PlaneFocusRoles
)

// planeFocusOrder is the deterministic Tab cycle within MODEL browsing:
// providers and models, with search reached by typing. Tab between the two
// top-level modes is handled explicitly, not by this order; the order keeps
// Shift+Tab reachability for programmatic callers.
var planeFocusOrder = []PlaneFocus{
	PlaneFocusProviders,
	PlaneFocusModels,
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
// It owns only presentation state: cursors, the filter, the pending
// transaction, and the roster the caller handed it. It never persists
// anything itself.
type ModelPlaneState struct {
	// Mode selects model browsing or role assignment.
	Mode PlaneMode
	// Focus selects the pane the keyboard drives in model mode.
	Focus PlaneFocus
	// Stage names the step of the assignment transaction. Browse is the
	// resting state; the other stages are the linear flow. A stage owns only
	// transient state: nothing is persisted until Apply.
	Stage PlaneStage

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

	// Effort is the thinking effort chosen for the pending assignment. It is
	// applied with the model when the assignment is applied. Empty means the
	// model exposes no ladder.
	Effort string
	// EffortCursor selects the highlighted option in EffortOptions during the
	// effort stage.
	EffortCursor int

	// PendingModel is the model staged in the current assignment transaction
	// (model mode: the model picked before the role; roles mode: the model
	// picked for the staged role). It is UI state only: the transaction is not
	// persisted until Apply.
	PendingModel ModelOption

	// Variant is the open choose-a-variant prompt, or nil. While set the
	// keyboard picks a variant row and nothing else assigns.
	Variant *PlaneVariantState

	// ActiveRole is the role the pending assignment targets.
	ActiveRole string

	// VisibleRows bounds the rendered list window.
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

// ChosenRole returns the highlighted role.
func (p *ModelPlaneState) ChosenRole() (RoleAssignment, bool) {
	if p.RoleCursor < 0 || p.RoleCursor >= len(p.Roles) {
		return RoleAssignment{}, false
	}
	return p.Roles[p.RoleCursor], true
}

// activeModel returns the model the current transaction is about: the staged
// model during Role/Effort/Apply, else the highlighted model. Capability
// questions always resolve against this, so the flow never consults a stale
// highlight.
func (p *ModelPlaneState) activeModel() (ModelOption, bool) {
	if p.PendingModel.ID != "" {
		return p.PendingModel, true
	}
	return p.ChosenModel()
}

// Move shifts the highlight. Its target follows the stage: the role list while
// choosing a role, the effort ladder while choosing an effort, else the focused
// browse pane (or the model list in the roles flow).
func (p *ModelPlaneState) Move(delta int) {
	switch p.Stage {
	case PlaneStageRole:
		p.RoleCursor = wrap(p.RoleCursor+delta, len(p.Roles))
		return
	case PlaneStageEffort:
		p.MoveEffort(delta)
		return
	case PlaneStageModel:
		p.ModelCursor = wrap(p.ModelCursor+delta, len(p.VisibleModels()))
		p.ensureModelVisible()
		return
	case PlaneStageApply:
		return
	}
	if p.Mode == PlaneRoles {
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
		p.ensureProviderVisible()
		p.ensureModelVisible()
		return
	}
	p.ModelCursor = wrap(p.ModelCursor+delta, len(p.VisibleModels()))
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

// setFocus moves the keyboard to a browse focus stop. The mode follows so the
// roles panes render exactly when roles browsing owns the keyboard.
func (p *ModelPlaneState) setFocus(focus PlaneFocus) {
	p.Focus = focus
	if focus == PlaneFocusRoles {
		p.Mode = PlaneRoles
	} else if p.Mode == PlaneRoles && p.Stage == PlaneStageBrowse {
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
// variant prompt owns the keyboard regardless of the underlying stage.
func (p *ModelPlaneState) FocusName() string {
	if p.Variant != nil {
		return "variant"
	}
	switch p.Stage {
	case PlaneStageRole:
		return "role"
	case PlaneStageModel:
		return "model"
	case PlaneStageEffort:
		return "effort"
	case PlaneStageApply:
		return "apply"
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
func (p *ModelPlaneState) Page(delta int) {
	switch p.Stage {
	case PlaneStageRole:
		p.RoleCursor = clampIndex(p.RoleCursor+delta*p.planeStep(), len(p.Roles))
		return
	case PlaneStageEffort:
		p.effortEnd(delta > 0)
		return
	case PlaneStageModel:
		p.pageModel(delta)
		return
	case PlaneStageApply:
		return
	}
	if p.Mode == PlaneRoles {
		p.RoleCursor = clampIndex(p.RoleCursor+delta*p.planeStep(), len(p.Roles))
		return
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor = clampIndex(p.ProviderCursor+delta*p.planeStep(), len(p.Providers))
		p.ModelCursor, p.ModelOffset = 0, 0
		p.ensureProviderVisible()
		return
	}
	p.pageModel(delta)
}

// pageModel shifts the model highlight by about a viewport.
func (p *ModelPlaneState) pageModel(delta int) {
	p.ModelCursor = clampIndex(p.ModelCursor+delta*p.planeStep(), len(p.VisibleModels()))
	p.ensureModelVisible()
}

// planeStep is the page size, defaulting when no window has been measured.
func (p *ModelPlaneState) planeStep() int {
	if p.VisibleRows > 1 {
		return p.VisibleRows
	}
	return 10
}

// Top parks the highlight on the first row of the current control.
func (p *ModelPlaneState) Top() {
	switch p.Stage {
	case PlaneStageRole:
		p.RoleCursor = 0
		return
	case PlaneStageEffort:
		p.effortEnd(false)
		return
	case PlaneStageModel:
		p.ModelCursor = 0
		p.ensureModelVisible()
		return
	case PlaneStageApply:
		return
	}
	if p.Mode == PlaneRoles {
		p.RoleCursor = 0
		return
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor = 0
		p.ModelCursor, p.ModelOffset = 0, 0
		p.ensureProviderVisible()
		return
	}
	p.ModelCursor = 0
	p.ensureModelVisible()
}

// Bottom parks the highlight on the last row of the current control.
func (p *ModelPlaneState) Bottom() {
	switch p.Stage {
	case PlaneStageRole:
		p.RoleCursor = clampIndex(len(p.Roles)-1, len(p.Roles))
		return
	case PlaneStageEffort:
		p.effortEnd(true)
		return
	case PlaneStageModel:
		p.ModelCursor = clampIndex(len(p.VisibleModels())-1, len(p.VisibleModels()))
		p.ensureModelVisible()
		return
	case PlaneStageApply:
		return
	}
	if p.Mode == PlaneRoles {
		p.RoleCursor = clampIndex(len(p.Roles)-1, len(p.Roles))
		return
	}
	if p.Focus == PlaneFocusProviders {
		p.ProviderCursor = clampIndex(len(p.Providers)-1, len(p.Providers))
		p.ModelCursor, p.ModelOffset = 0, 0
		p.ensureProviderVisible()
		return
	}
	p.ModelCursor = clampIndex(len(p.VisibleModels())-1, len(p.VisibleModels()))
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

// ToggleMode swaps between model browsing and role assignment. Switching modes
// abandons any pending transaction: a mode change is a new context, not a step
// in the flow.
func (p *ModelPlaneState) ToggleMode() {
	p.resetTransaction()
	if p.Mode == PlaneRoles {
		p.Mode = PlaneModels
		p.Focus = PlaneFocusModels
		return
	}
	p.Mode = PlaneRoles
	p.Focus = PlaneFocusRoles
}

// SetFilter replaces the query, parking the text cursor at the end.
func (p *ModelPlaneState) SetFilter(text string) {
	p.Filter = text
	p.FilterCursor = len([]rune(text))
	p.ModelCursor, p.ModelOffset = 0, 0
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
}

// EffortOptions returns the thinking choices the model under the current
// transaction exposes.
//
// A model with an explicit ladder offers "off" plus each level; a model that
// accepts thinking but exposes no ladder offers no choices (support alone is
// reported); a model the metadata marks unsupported offers none. The plane
// never renders a control for a capability the model does not expose.
func (p *ModelPlaneState) EffortOptions() []string {
	model, ok := p.activeModel()
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

// MoveEffort moves the pending effort among the staged model's options. It is
// a no-op when the model exposes no ladder.
func (p *ModelPlaneState) MoveEffort(delta int) {
	options := p.EffortOptions()
	if len(options) == 0 {
		p.Effort = ""
		p.EffortCursor = 0
		return
	}
	p.EffortCursor = wrap(p.EffortCursor+delta, len(options))
	p.Effort = options[p.EffortCursor]
}

// effortEnd parks the pending effort on the first or last option.
func (p *ModelPlaneState) effortEnd(last bool) {
	options := p.EffortOptions()
	if len(options) == 0 {
		return
	}
	if last {
		p.EffortCursor = len(options) - 1
	} else {
		p.EffortCursor = 0
	}
	p.Effort = options[p.EffortCursor]
}

// SetEffort selects an explicit effort option when the staged model exposes it.
// Unknown values are refused so the UI can never stage an effort the model does
// not accept.
func (p *ModelPlaneState) SetEffort(option string) {
	options := p.EffortOptions()
	for i, candidate := range options {
		if candidate == option {
			p.Effort = option
			p.EffortCursor = i
			return
		}
	}
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

// OpenVariant starts the variant prompt for the given model. It reports false
// — and changes nothing — when no choice is needed.
func (p *ModelPlaneState) OpenVariant(model ModelOption) bool {
	if !p.NeedsVariantChoice(model) {
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

// ConfirmVariant commits the chosen sibling and advances the flow: in model
// mode to role selection, in roles mode straight to the effort step, since the
// role was already chosen. The variant's own ladder is resolved fresh, so a
// level carried from the base never survives into the Apply.
func (p *ModelPlaneState) ConfirmVariant(model ModelOption) {
	p.Variant = nil
	if model.ID == "" {
		return
	}
	p.PendingModel = model
	if p.Mode == PlaneRoles {
		p.stageEffort()
		return
	}
	p.Stage = PlaneStageRole
	p.RoleCursor = p.activeRoleIndex()
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

// activeRoleIndex returns the index of the active role, or 0.
func (p *ModelPlaneState) activeRoleIndex() int {
	for i, role := range p.Roles {
		if role.Role == p.ActiveRole {
			return i
		}
	}
	return 0
}

// NextRole cycles the active role. It backs the programmatic role cycle; the
// interactive flow selects a role explicitly in the role stage.
func (p *ModelPlaneState) NextRole(delta int) {
	if len(p.Roles) == 0 {
		return
	}
	index := p.activeRoleIndex()
	index = wrap(index+delta, len(p.Roles))
	p.ActiveRole = p.Roles[index].Role
}

// StageRole begins the roles-first transaction for the highlighted role and
// moves to model selection. It reports false when no role is highlighted.
func (p *ModelPlaneState) StageRole() bool {
	chosen, ok := p.ChosenRole()
	if !ok {
		return false
	}
	p.ActiveRole = chosen.Role
	p.PendingModel = ModelOption{}
	p.Stage = PlaneStageModel
	p.Focus = PlaneFocusModels
	p.ensureModelVisible()
	p.Notice = "select model for " + strings.ToUpper(chosen.Role)
	return true
}

// beginModelFirst begins the model-first transaction for a picked model: the
// model is staged and the flow moves to role selection. The default focused
// role is the active one (derived from the workspace); it is navigation only,
// never an implicit assignment.
func (p *ModelPlaneState) beginModelFirst(model ModelOption) bool {
	if model.ID == "" {
		return false
	}
	p.PendingModel = model
	p.Stage = PlaneStageRole
	p.RoleCursor = p.activeRoleIndex()
	p.Notice = ""
	return true
}

// chooseRole confirms the highlighted role and resolves the selected model's
// exact capability: an effort step when it exposes a ladder, an explicit apply
// step otherwise.
func (p *ModelPlaneState) chooseRole() bool {
	chosen, ok := p.ChosenRole()
	if !ok {
		return false
	}
	p.ActiveRole = chosen.Role
	p.stageEffort()
	return true
}

// chooseModel confirms the highlighted model for the staged role and resolves
// its capability into the next step.
func (p *ModelPlaneState) chooseModel() bool {
	model, ok := p.ChosenModel()
	if !ok {
		return false
	}
	p.PendingModel = model
	p.stageEffort()
	return true
}

// stageEffort resolves the staged model's authoritative ladder and enters the
// effort step, or the apply step when there is no ladder. The role's current
// effort is preselected only when the model exposes it; otherwise the model's
// own first option is shown, so an incompatible effort is never carried into
// Apply.
func (p *ModelPlaneState) stageEffort() {
	options := effortOptions(p.PendingModel)
	if len(options) == 0 {
		p.Effort = ""
		p.EffortCursor = 0
		p.Stage = PlaneStageApply
		return
	}
	p.Stage = PlaneStageEffort
	p.EffortCursor = 0
	if role, ok := p.roleByModel(p.ActiveRole); ok && role.Effort != "" {
		for i, option := range options {
			if option == role.Effort {
				p.EffortCursor = i
				break
			}
		}
	}
	p.Effort = options[p.EffortCursor]
	p.ensureModelVisible()
}

// PendingAssignment builds the assignment the user is about to apply. It only
// returns ok in an applyable stage (effort or apply), so a half-built
// transaction can never reach the authority seam.
func (p *ModelPlaneState) PendingAssignment() (RoleAssignment, bool) {
	if p.Stage != PlaneStageEffort && p.Stage != PlaneStageApply {
		return RoleAssignment{}, false
	}
	if p.ActiveRole == "" || p.PendingModel.ID == "" {
		return RoleAssignment{}, false
	}
	effort := p.Effort
	if p.Stage == PlaneStageApply {
		effort = ""
	}
	return RoleAssignment{
		Role:   p.ActiveRole,
		Model:  p.PendingModel.ID,
		Effort: effort,
	}, true
}

// Assignment builds the assignment for the highlighted model with the pending
// effort. It is a convenience for callers outside the staged flow; the staged
// flow uses PendingAssignment.
func (p *ModelPlaneState) Assignment() (RoleAssignment, bool) {
	model, ok := p.ChosenModel()
	if !ok {
		return RoleAssignment{}, false
	}
	role := p.ActiveRole
	if p.Mode == PlaneRoles {
		if chosen, ok := p.ChosenRole(); ok {
			role = chosen.Role
		}
	}
	return RoleAssignment{
		Role:   role,
		Model:  model.ID,
		Effort: p.Effort,
	}, true
}

// CancelStage backs out one step of the transaction: an effort/apply step
// returns to model or role selection, a selection step returns to browsing.
// It never persists and never closes the dialog.
func (p *ModelPlaneState) CancelStage() {
	switch p.Stage {
	case PlaneStageEffort, PlaneStageApply:
		if p.Mode == PlaneRoles {
			p.Stage = PlaneStageModel
			p.Focus = PlaneFocusModels
		} else {
			p.Stage = PlaneStageRole
		}
	case PlaneStageRole, PlaneStageModel:
		p.resetTransaction()
		p.browseFocus()
	default:
		p.resetTransaction()
		p.browseFocus()
	}
}

// browseFocus parks focus on the pane the current mode browses.
func (p *ModelPlaneState) browseFocus() {
	if p.Mode == PlaneRoles {
		p.Focus = PlaneFocusRoles
		return
	}
	p.Focus = PlaneFocusModels
}

// resetTransaction discards any pending assignment and returns to browsing.
func (p *ModelPlaneState) resetTransaction() {
	p.Stage = PlaneStageBrowse
	p.PendingModel = ModelOption{}
	p.Effort = ""
	p.EffortCursor = 0
}

// ApplyAssignment records the result of an assignment attempt. A successful
// assignment clears the transaction and returns to browsing; a refusal keeps
// the pending step so the user can adjust it without starting over.
func (p *ModelPlaneState) ApplyAssignment(result RoleAssignment, roles []RoleAssignment, notice string) {
	if roles != nil {
		p.Roles = roles
	}
	if result.Role != "" {
		p.ActiveRole = result.Role
		p.resetTransaction()
		p.browseFocus()
		p.clampRoleCursor()
	}
	p.Notice = notice
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
