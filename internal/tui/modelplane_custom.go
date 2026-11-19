package tui

import (
	"sort"
	"strings"
)

// CustomProvider is the synthetic provider row that opens user-owned
// OpenAI-compatible endpoints. It is a control surface, never a model
// reference: no model's Provider equals this value.
const CustomProvider = "custom"

// CustomProviderName is the row label shown in the provider pane. The pane's
// glyph supplies the leading "+"; the label itself must not repeat it.
const CustomProviderName = "Custom"

// EndpointOption is one configured custom endpoint as the control plane
// renders it. It is a read-only projection of the global configuration; the
// plane never edits it in place.
type EndpointOption struct {
	// Key is the stable provider key and model-reference prefix.
	Key string
	// Name is the human display name.
	Name string
	// BaseURL is the OpenAI-compatible root.
	BaseURL string
	// APIKeyEnv names the environment variable carrying the credential.
	APIKeyEnv string
	// Models lists the configured model ids.
	Models []string
	// ContextWindow and MaxTokens are the facts the runtime uses for every
	// model on this endpoint. Zero means automatic.
	ContextWindow int
	MaxTokens     int
	// FactsAutomatic reports the two ceilings are conservative fallbacks,
	// not discovered or user-verified facts.
	FactsAutomatic bool
}

// DisplayName names the endpoint for humans, falling back to its key.
func (e EndpointOption) DisplayName() string {
	if name := strings.TrimSpace(e.Name); name != "" {
		return name
	}
	return e.Key
}

// EndpointDraft is one endpoint as the editor submits it. It carries no
// credential: only the environment variable name crosses this boundary.
type EndpointDraft struct {
	// Key is the existing provider key, or empty for a new endpoint.
	Key string
	// Name is the display name.
	Name string
	// BaseURL is the OpenAI-compatible root.
	BaseURL string
	// APIKeyEnv names the environment variable carrying the credential.
	APIKeyEnv string
	// Models lists the model ids the user explicitly selected.
	Models []string
}

// EndpointProbeResult is runtime evidence from one probe. It is never
// persistence authority: the editor may show it, and only Save writes.
type EndpointProbeResult struct {
	// Reachable reports the endpoint answered HTTP.
	Reachable bool
	// Authenticated reports discovery accepted the credential.
	Authenticated bool
	// Protocol reports discovery decoded into an OpenAI-compatible shape.
	Protocol bool
	// Models counts discovered entries.
	Models int
	// ModelIDs are the discovered ids, sorted.
	ModelIDs []string
	// Detail is the human summary naming the outcome and the fix.
	Detail string
}

// CustomView selects the right-pane custom-endpoint surface.
type CustomView int

// Custom-endpoint views.
const (
	// CustomViewEndpoints lists configured endpoints.
	CustomViewEndpoints CustomView = iota
	// CustomViewEndpoint shows one endpoint's models and metadata.
	CustomViewEndpoint
	// CustomViewEditor edits one endpoint.
	CustomViewEditor
	// CustomViewDiscovery selects discovered models to add.
	CustomViewDiscovery
)

// CustomDeleteState is the destructive-action confirmation for one endpoint.
// It follows the session manager's y/n convention rather than deleting on the
// first keypress.
type CustomDeleteState struct {
	// Key is the endpoint key to remove.
	Key string
	// Label names the endpoint in the question.
	Label string
}

// CustomEndpointEditor is the compact endpoint form. It is transient: Esc
// discards it, Ctrl+S submits it through the caller's configuration seam.
type CustomEndpointEditor struct {
	// Key is the existing endpoint key, or empty for a new endpoint.
	Key string
	// Name, BaseURL, and APIKeyEnv are the editable fields.
	Name      string
	BaseURL   string
	APIKeyEnv string
	// Field is the focused field: 0 name, 1 base URL, 2 API-key env.
	Field int
	// Cursors are the rune offsets of the three fields.
	Cursors [3]int
	// Models are the explicitly configured model ids. Probe discovery does
	// not add to it; the discovery overlay does, after the user selects.
	Models []string
	// Probing reports a probe is in flight.
	Probing bool
	// ProbeStatus is the last probe outcome, in words.
	ProbeStatus string
	// ProbeSeq identifies the in-flight probe so a stale result cannot
	// overwrite a newer edit.
	ProbeSeq int
	// Discovered is the last successful probe's model evidence.
	Discovered []string
}

// customEditorFields is the number of editable fields.
const customEditorFields = 3

// fieldValue returns the address of one editable field.
func (e *CustomEndpointEditor) fieldValue(field int) *string {
	switch field {
	case 0:
		return &e.Name
	case 1:
		return &e.BaseURL
	case 2:
		return &e.APIKeyEnv
	}
	return &e.Name
}

// fieldCursor returns the cursor of one editable field.
func (e *CustomEndpointEditor) fieldCursor(field int) *int {
	field = clampIndex(field, customEditorFields)
	return &e.Cursors[field]
}

// Draft projects the editor into the configuration seam's shape.
func (e *CustomEndpointEditor) Draft() EndpointDraft {
	return EndpointDraft{
		Key:       strings.TrimSpace(e.Key),
		Name:      strings.TrimSpace(e.Name),
		BaseURL:   strings.TrimSpace(e.BaseURL),
		APIKeyEnv: strings.TrimSpace(e.APIKeyEnv),
		Models:    append([]string(nil), e.Models...),
	}
}

// CustomDiscoveryState is the explicit discovered-model selection overlay.
type CustomDiscoveryState struct {
	// Items is the union of configured and discovered models.
	Items []CustomDiscoveryItem
	// Cursor selects the highlighted item.
	Cursor int
}

// CustomDiscoveryItem is one selectable model id.
type CustomDiscoveryItem struct {
	// ID is the model id.
	ID string
	// Selected marks the draft selection. It is not persistence.
	Selected bool
	// Configured marks a model already in the endpoint's draft.
	Configured bool
	// Discovered marks a model the last probe reported.
	Discovered bool
}

// customEndpointModel returns the configured endpoint the right pane is
// showing, if any.
func (p *ModelPlaneState) customEndpointModel() (EndpointOption, bool) {
	key := p.customEndpointKey
	if key == "" {
		return EndpointOption{}, false
	}
	for _, endpoint := range p.Endpoints {
		if endpoint.Key == key {
			return endpoint, true
		}
	}
	return EndpointOption{}, false
}

// CustomActive reports whether the Custom row owns the right pane.
func (p *ModelPlaneState) CustomActive() bool {
	row, ok := p.SelectedProvider()
	return ok && row.Custom
}

// SetEndpoints replaces the configured endpoint roster and keeps the
// highlighted endpoint when it survives.
func (p *ModelPlaneState) SetEndpoints(endpoints []EndpointOption) {
	selected := p.customEndpointKey
	p.Endpoints = append([]EndpointOption(nil), endpoints...)
	p.EndpointCursor = clampIndex(p.EndpointCursor, len(p.Endpoints))
	if selected != "" {
		found := false
		for i, endpoint := range p.Endpoints {
			if endpoint.Key == selected {
				p.EndpointCursor = i
				found = true
				break
			}
		}
		if !found && p.CustomView == CustomViewEndpoint {
			p.CustomView = CustomViewEndpoints
			p.customEndpointKey = ""
		}
	}
	p.ensureEndpointVisible()
}

// SelectedEndpoint returns the highlighted configured endpoint.
func (p *ModelPlaneState) SelectedEndpoint() (EndpointOption, bool) {
	if p.EndpointCursor < 0 || p.EndpointCursor >= len(p.Endpoints) {
		return EndpointOption{}, false
	}
	return p.Endpoints[p.EndpointCursor], true
}

// OpenEndpoint opens one endpoint's model detail.
func (p *ModelPlaneState) OpenEndpoint(key string) bool {
	for i, endpoint := range p.Endpoints {
		if endpoint.Key == key {
			p.customEndpointKey = key
			p.CustomView = CustomViewEndpoint
			p.Focus = PlaneFocusModels
			p.EndpointCursor = i
			p.ModelCursor = 0
			p.ModelOffset = 0
			p.EndpointProbe = EndpointProbeResult{}
			p.EndpointProbing = false
			p.ensureEndpointVisible()
			return true
		}
	}
	return false
}

// BackCustom returns one level: discovery to editor, editor to list/detail,
// detail to list. It reports whether the custom pane consumed the key.
func (p *ModelPlaneState) BackCustom() bool {
	switch p.CustomView {
	case CustomViewDiscovery:
		p.Discovery = nil
		p.CustomView = CustomViewEditor
		return true
	case CustomViewEditor:
		key := ""
		if p.EndpointEditor != nil {
			key = p.EndpointEditor.Key
		}
		p.EndpointEditor = nil
		p.CustomView = CustomViewEndpoints
		p.Focus = PlaneFocusModels
		if key != "" {
			p.customEndpointKey = key
			p.CustomView = CustomViewEndpoint
		}
		return true
	case CustomViewEndpoint:
		p.CustomView = CustomViewEndpoints
		p.customEndpointKey = ""
		p.EndpointProbe = EndpointProbeResult{}
		p.Focus = PlaneFocusModels
		return true
	}
	return false
}

// OpenNewEndpoint starts the compact editor for a new endpoint.
func (p *ModelPlaneState) OpenNewEndpoint() {
	p.EndpointEditor = &CustomEndpointEditor{Field: 0}
	p.CustomView = CustomViewEditor
	p.Focus = PlaneFocusModels
	p.customEndpointKey = ""
}

// OpenEditEndpoint starts the compact editor for the highlighted endpoint.
func (p *ModelPlaneState) OpenEditEndpoint() bool {
	endpoint, ok := p.SelectedEndpoint()
	if !ok {
		if current, currentOK := p.customEndpointModel(); currentOK {
			endpoint = current
			ok = true
		}
	}
	if !ok {
		return false
	}
	editor := &CustomEndpointEditor{
		Key:         endpoint.Key,
		Name:        endpoint.DisplayName(),
		BaseURL:     endpoint.BaseURL,
		APIKeyEnv:   endpoint.APIKeyEnv,
		Models:      append([]string(nil), endpoint.Models...),
		Discovered:  append([]string(nil), p.EndpointProbe.ModelIDs...),
		ProbeStatus: p.EndpointProbe.Detail,
	}
	for i := range editor.Cursors {
		editor.Cursors[i] = len([]rune(*editor.fieldValue(i)))
	}
	p.EndpointEditor = editor
	p.CustomView = CustomViewEditor
	p.Focus = PlaneFocusModels
	return true
}

// MoveEditorFocus moves the editor's field focus.
func (p *ModelPlaneState) MoveEditorFocus(delta int) {
	editor := p.EndpointEditor
	if editor == nil {
		return
	}
	editor.Field = wrap(editor.Field+delta, customEditorFields)
}

// MoveEditorCursor moves the cursor inside the focused field.
func (p *ModelPlaneState) MoveEditorCursor(delta int) {
	editor := p.EndpointEditor
	if editor == nil {
		return
	}
	cursor := editor.fieldCursor(editor.Field)
	*cursor = clampIndex(*cursor+delta, len([]rune(*editor.fieldValue(editor.Field)))+1)
}

// InsertEditorText inserts text at the focused field's cursor.
func (p *ModelPlaneState) InsertEditorText(text string) {
	editor := p.EndpointEditor
	if editor == nil || text == "" {
		return
	}
	value := editor.fieldValue(editor.Field)
	runes := []rune(*value)
	cursor := editor.fieldCursor(editor.Field)
	cur := clampIndex(*cursor, len(runes)+1)
	*value = string(runes[:cur]) + text + string(runes[cur:])
	*cursor = cur + len([]rune(text))
}

// DeleteEditorBefore removes the rune before the focused cursor.
func (p *ModelPlaneState) DeleteEditorBefore() {
	editor := p.EndpointEditor
	if editor == nil {
		return
	}
	value := editor.fieldValue(editor.Field)
	runes := []rune(*value)
	cursor := editor.fieldCursor(editor.Field)
	if *cursor <= 0 || *cursor > len(runes) {
		return
	}
	*value = string(append(runes[:*cursor-1], runes[*cursor:]...))
	*cursor--
}

// DeleteEditorAt removes the rune under the focused cursor.
func (p *ModelPlaneState) DeleteEditorAt() {
	editor := p.EndpointEditor
	if editor == nil {
		return
	}
	value := editor.fieldValue(editor.Field)
	runes := []rune(*value)
	cursor := editor.fieldCursor(editor.Field)
	if *cursor < 0 || *cursor >= len(runes) {
		return
	}
	*value = string(append(runes[:*cursor], runes[*cursor+1:]...))
}

// ApplyEndpointProbe records a probe result only when it answers the latest
// request and the editor still targets the probed URL and credential. Empty
// target fields skip the freshness comparison.
func (p *ModelPlaneState) ApplyEndpointProbe(seq int, baseURL, apiKeyEnv string, result EndpointProbeResult) {
	editor := p.EndpointEditor
	if editor == nil || seq != editor.ProbeSeq {
		return
	}
	editor.Probing = false
	if baseURL != "" || apiKeyEnv != "" {
		if strings.TrimSpace(editor.BaseURL) != strings.TrimSpace(baseURL) ||
			strings.TrimSpace(editor.APIKeyEnv) != strings.TrimSpace(apiKeyEnv) {
			editor.ProbeStatus = "endpoint changed — probe again"
			return
		}
	}
	editor.Discovered = append([]string(nil), result.ModelIDs...)
	editor.ProbeStatus = result.Detail
	p.EndpointProbe = result
	if result.Models > 0 {
		p.Notice = ""
	}
}

// OpenDiscovery builds the discovered-model selection from the editor's
// configured models plus the last probe evidence.
func (p *ModelPlaneState) OpenDiscovery() bool {
	editor := p.EndpointEditor
	if editor == nil {
		return false
	}
	seen := map[string]*CustomDiscoveryItem{}
	items := make([]CustomDiscoveryItem, 0, len(editor.Models)+len(editor.Discovered))
	add := func(id string, configured, discovered bool) {
		if id == "" {
			return
		}
		if item, ok := seen[id]; ok {
			item.Configured = item.Configured || configured
			item.Discovered = item.Discovered || discovered
			return
		}
		item := CustomDiscoveryItem{ID: id, Selected: configured, Configured: configured, Discovered: discovered}
		seen[id] = &item
		items = append(items, item)
	}
	for _, id := range editor.Models {
		add(id, true, false)
	}
	for _, id := range editor.Discovered {
		add(id, false, true)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if len(items) == 0 {
		p.Notice = "probe the endpoint to discover models"
		return false
	}
	p.Discovery = &CustomDiscoveryState{Items: items}
	p.CustomView = CustomViewDiscovery
	return true
}

// MoveDiscovery shifts the discovery highlight.
func (p *ModelPlaneState) MoveDiscovery(delta int) {
	if p.Discovery == nil || len(p.Discovery.Items) == 0 {
		return
	}
	p.Discovery.Cursor = wrap(p.Discovery.Cursor+delta, len(p.Discovery.Items))
}

// ToggleDiscovery flips the highlighted model's selection.
func (p *ModelPlaneState) ToggleDiscovery() {
	discovery := p.Discovery
	if discovery == nil || discovery.Cursor < 0 || discovery.Cursor >= len(discovery.Items) {
		return
	}
	discovery.Items[discovery.Cursor].Selected = !discovery.Items[discovery.Cursor].Selected
}

// AddDiscoverySelection copies the selected ids into the editor draft. This
// mutates only transient editor state; Save is still required to persist.
func (p *ModelPlaneState) AddDiscoverySelection() int {
	discovery := p.Discovery
	editor := p.EndpointEditor
	if discovery == nil || editor == nil {
		return 0
	}
	models := make([]string, 0, len(discovery.Items))
	for _, item := range discovery.Items {
		if item.Selected {
			models = append(models, item.ID)
		}
	}
	sort.Strings(models)
	editor.Models = models
	p.Discovery = nil
	p.CustomView = CustomViewEditor
	return len(models)
}

// AskDeleteEndpoint opens the y/n confirmation for the visible endpoint.
func (p *ModelPlaneState) AskDeleteEndpoint() bool {
	endpoint, ok := p.SelectedEndpoint()
	if current, currentOK := p.customEndpointModel(); currentOK {
		endpoint = current
		ok = true
	}
	if !ok {
		p.Notice = "select an endpoint first"
		return false
	}
	p.CustomDelete = &CustomDeleteState{Key: endpoint.Key, Label: endpoint.DisplayName()}
	return true
}

// CancelDeleteEndpoint dismisses the confirmation.
func (p *ModelPlaneState) CancelDeleteEndpoint() { p.CustomDelete = nil }

// EndpointDraft returns the editor's submitted draft, or false when no editor
// is open.
func (p *ModelPlaneState) EndpointDraft() (EndpointDraft, bool) {
	if p.EndpointEditor == nil {
		return EndpointDraft{}, false
	}
	return p.EndpointEditor.Draft(), true
}

// ApplyEndpointsSaved records a successful save/delete and returns to the
// endpoint list. The refreshed roster comes from the configuration authority,
// never from a locally fabricated merge.
func (p *ModelPlaneState) ApplyEndpointsSaved(endpoints []EndpointOption, providers []ProviderRow, notice string) {
	if providers != nil {
		p.Providers = append([]ProviderRow(nil), providers...)
	}
	p.SetEndpoints(endpoints)
	p.CustomDelete = nil
	p.EndpointEditor = nil
	p.Discovery = nil
	p.EndpointProbe = EndpointProbeResult{}
	p.EndpointProbing = false
	p.CustomView = CustomViewEndpoints
	p.customEndpointKey = ""
	p.Focus = PlaneFocusModels
	p.Notice = notice
	p.ensureProviderVisible()
	p.ensureEndpointVisible()
	p.ensureModelVisible()
}

// ensureEndpointVisible keeps the highlighted endpoint in the visible window.
func (p *ModelPlaneState) ensureEndpointVisible() {
	n := len(p.Endpoints)
	if n == 0 {
		p.EndpointCursor, p.EndpointOffset = 0, 0
		return
	}
	p.EndpointCursor = clampIndex(p.EndpointCursor, n)
	rows := p.VisibleRows
	if rows <= 0 || rows >= n {
		p.EndpointOffset = 0
		return
	}
	if p.EndpointCursor < p.EndpointOffset {
		p.EndpointOffset = p.EndpointCursor
	}
	if p.EndpointCursor >= p.EndpointOffset+rows {
		p.EndpointOffset = p.EndpointCursor - rows + 1
	}
	if max := n - rows; p.EndpointOffset > max {
		p.EndpointOffset = max
	}
	if p.EndpointOffset < 0 {
		p.EndpointOffset = 0
	}
}

// customModelOptions returns the configured endpoint's models as normal
// model rows, preserving any live facts already in the roster.
func (p *ModelPlaneState) customModelOptions() []ModelOption {
	endpoint, ok := p.customEndpointModel()
	if !ok {
		return nil
	}
	byID := map[string]ModelOption{}
	for _, model := range p.Models {
		if model.Provider == endpoint.Key {
			byID[model.ID] = model
		}
	}
	out := make([]ModelOption, 0, len(endpoint.Models))
	for _, id := range endpoint.Models {
		ref := endpoint.Key + "/" + id
		if model, ok := byID[ref]; ok {
			out = append(out, model)
			continue
		}
		context := endpoint.ContextWindow
		maxOutput := endpoint.MaxTokens
		detail := "configured endpoint"
		if context > 0 {
			detail = formatCount(context) + " ctx · configured endpoint"
		}
		out = append(out, ModelOption{
			ID:                 ref,
			Provider:           endpoint.Key,
			Title:              id,
			Name:               id,
			Detail:             detail,
			Context:            context,
			ContextAutomatic:   endpoint.FactsAutomatic,
			MaxOutput:          maxOutput,
			MaxOutputAutomatic: endpoint.FactsAutomatic,
			Origin:             "bundled",
			InputModalities:    []string{"text"},
			ReasoningKnown:     false,
		})
	}
	return out
}
