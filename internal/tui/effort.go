package tui

import (
	"strings"
	"unicode"
)

// knownEfforts is the thinking-effort vocabulary the catalog exposes (see
// catalog.Thinking.Efforts: max, xhigh, high, medium, low, minimal). It is
// data, not per-model policy: the assignment seam validates a level against
// the bound model's own ladder, while the header only normalizes spelling so
// "MAX" and " max " read as one level and anything else stays absent rather
// than rendering as a badge.
var knownEfforts = map[string]bool{
	"max":     true,
	"xhigh":   true,
	"high":    true,
	"medium":  true,
	"low":     true,
	"minimal": true,
}

// isKnownEffort reports whether a lowercased, trimmed level belongs to the
// catalog vocabulary.
func isKnownEffort(level string) bool {
	return knownEfforts[level]
}

// NormalizeEffort canonicalizes a raw effort level for display.
//
// It trims space, lowercases, and returns "" for empty, "off", or any level
// outside the catalog vocabulary, so the header omits rather than invents a
// badge. "off" disables thinking outright and never renders as a level.
func NormalizeEffort(effort string) string {
	level := strings.ToLower(strings.TrimSpace(effort))
	if level == "" || level == "off" {
		return ""
	}
	if !isKnownEffort(level) {
		return ""
	}
	return level
}

// SplitEffortVariant separates a trailing "(effort)" suffix from a model
// reference, e.g. "openrouter/vendor/base(max)" into
// ("openrouter/vendor/base", "max").
//
// Only a trailing parenthetical holding a known level (or "off") splits: any
// other trailing text stays part of the model id, so a model that merely
// contains parentheses for another reason never loses characters. The base
// is trimmed; an empty base keeps the input untouched.
func SplitEffortVariant(ref string) (base, effort string) {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasSuffix(trimmed, ")") {
		return ref, ""
	}
	open := strings.LastIndex(trimmed, "(")
	if open < 0 || open+1 >= len(trimmed)-1 {
		return ref, ""
	}
	inside := strings.ToLower(strings.TrimSpace(trimmed[open+1 : len(trimmed)-1]))
	if inside == "" {
		return ref, ""
	}
	if inside == "off" {
		base := strings.TrimSpace(trimmed[:open])
		if base == "" {
			return ref, ""
		}
		return base, ""
	}
	if !isKnownEffort(inside) {
		return ref, ""
	}
	base = strings.TrimSpace(trimmed[:open])
	if base == "" {
		return ref, ""
	}
	return base, inside
}

// EffectiveModelEffort resolves the model id and effort badge for display.
//
// An explicit effort wins; otherwise a trailing "(effort)" variant in the
// model string supplies it. The returned base never carries the suffix, so
// the header cannot render the level twice, and the effort is "" when there
// is nothing to badge. An "(off)" suffix strips without badging, since
// disabled thinking has no level to show. Inputs are expected sanitized;
// this function only splits and normalizes.
func EffectiveModelEffort(model, effort string) (baseModel, effectiveEffort string) {
	if explicit := NormalizeEffort(effort); explicit != "" {
		base, _ := SplitEffortVariant(model)
		return base, explicit
	}
	// SplitEffortVariant strips "(off)" to its base with no level, and a
	// known "(level)" to its base with that level; anything else passes
	// through untouched.
	base, parsed := SplitEffortVariant(model)
	return base, parsed
}

// FormatModelWithEffort renders the plain-text model segment for the header:
// "model" alone, or "model ◈ effort" when a level applies. It is the plain
// contract the styled header wraps, so tests can assert layout without ANSI.
func FormatModelWithEffort(model, effort string) string {
	base, level := EffectiveModelEffort(model, effort)
	if level == "" {
		return base
	}
	return base + " ◈ " + level
}

// FitHeaderModelWithEffort shortens the model segment to a cell budget while
// keeping the effort badge legible.
//
// The badge survives provider-prefix shedding but never hard truncation: when
// even the compact leaf plus " ◈ effort" overflows, the badge drops and the
// core id receives the full budget via fitHeaderModel. A non-positive budget
// yields empty strings rather than an overflowing row.
func FitHeaderModelWithEffort(model, effort string, budget int) (modelShown, effortShown string) {
	if budget <= 0 {
		return "", ""
	}
	base, level := EffectiveModelEffort(model, effort)
	if level == "" {
		return fitHeaderModel(base, budget), ""
	}
	suffixWidth := CellWidth(" ◈ " + level)
	if suffixWidth >= budget {
		return fitHeaderModel(base, budget), ""
	}
	modelBudget := budget - suffixWidth
	if CellWidth(base) <= modelBudget {
		return base, level
	}
	if short := compactModel(base); CellWidth(short) <= modelBudget {
		return short, level
	}
	return fitHeaderModel(base, budget), ""
}

// effortTitle renders a provider effort token as a picker row label. The
// value stays the canonical token; only the label is title-cased.
func effortTitle(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case EffortAuto:
		return "Auto"
	case EffortOff:
		return "Off"
	case "xhigh":
		return "XHigh"
	}
	runes := []rune(level)
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// effortAutoDetail explains what Auto does for this role. An explicit
// configured level is cleared; with no override, the row names the level the
// role currently resolves to so selecting Auto is informed.
func effortAutoDetail(configured, current string) string {
	if strings.TrimSpace(configured) != "" {
		return "clear the role override"
	}
	if current = NormalizeEffort(current); current != "" {
		return "role default · currently " + current
	}
	return "role default"
}

// effortPickerSummary names the role and live model the effort card edits.
// The active row carries the level; the summary carries the target.
func effortPickerSummary(state State) string {
	role := strings.ToUpper(strings.TrimSpace(state.Status.Workspace))
	if role == "" {
		role = "ACTIVE ROLE"
	}
	model := strings.TrimSpace(state.Status.Model)
	if model == "" {
		return role
	}
	return role + " · " + model
}

// FormatEffortReport renders /effort's answer for one workspace role.
//
// model is the reference the runtime is calling; assignment carries the role's
// configured effort and the bound model's authoritative ladder; effective is
// the level the next turn would use, which differs from the configured level
// under AUTO. The report names the role and model first because adjusting the
// wrong role's thinking is the failure the command exists to prevent.
func FormatEffortReport(role, model string, assignment RoleAssignment, effective string) string {
	role = strings.ToUpper(strings.TrimSpace(role))
	if role == "" {
		role = "ROLE"
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(assignment.Model)
	}
	if model == "" {
		model = "no model"
	}
	state := effortState(assignment.Effort, effective)
	return role + " · " + model + " · effort: " + state +
		"\nlevels: " + strings.Join(effortLevels(assignment.Efforts), ", ") +
		" · /effort <level|off|auto>"
}

// effortState names the configured effort, or the role default with the level
// AUTO is currently applying when one is visible.
func effortState(configured, effective string) string {
	level := strings.ToLower(strings.TrimSpace(configured))
	switch level {
	case EffortOff:
		return EffortOff
	case "":
		if current := strings.ToLower(strings.TrimSpace(effective)); current != "" {
			return "auto (currently " + current + ")"
		}
		return "auto"
	default:
		return level
	}
}

// effortLevels lists the choices /effort can accept for a model: "off" plus
// the model's own ladder, deduplicated without inventing a level.
func effortLevels(efforts []string) []string {
	out := make([]string, 0, len(efforts)+1)
	seen := map[string]bool{}
	for _, candidate := range append([]string{EffortOff}, efforts...) {
		level := strings.ToLower(strings.TrimSpace(candidate))
		if level == "" || seen[level] {
			continue
		}
		seen[level] = true
		out = append(out, level)
	}
	return out
}

// FormatEffortApplied names the role, the applied level, and the model the
// next turn will call. An empty level reads as "auto": the role override was
// cleared, not set to an invisible value.
func FormatEffortApplied(result RoleAssignment, model string) string {
	if strings.TrimSpace(model) == "" {
		model = result.Model
	}
	level := strings.ToLower(strings.TrimSpace(result.Effort))
	if level == "" {
		level = "auto"
	}
	role := strings.ToUpper(strings.TrimSpace(result.Role))
	if strings.TrimSpace(model) == "" {
		return role + " effort: " + level
	}
	return role + " effort: " + level + " · " + model
}

// roleAssignmentFor finds one role's row in a roster, case-insensitively.
func roleAssignmentFor(roles []RoleAssignment, role string) (RoleAssignment, bool) {
	for _, assignment := range roles {
		if strings.EqualFold(assignment.Role, role) {
			return assignment, true
		}
	}
	return RoleAssignment{}, false
}

// ModelEffortUpdated records the active thinking effort for the header badge.
//
// Empty (or "off") clears the badge; the view never invents a level.
type ModelEffortUpdated struct {
	// Effort is the active reasoning effort, e.g. "max". Empty clears it.
	Effort string
}

func (ModelEffortUpdated) eventKind() string { return "model_effort_updated" }

// WithEffort records the active thinking effort for the header badge at
// construction. Empty hides the badge rather than rendering a placeholder.
func WithEffort(effort string) ProgramOption {
	return func(p *Program) { p.state.Status.Effort = NormalizeEffort(effort) }
}

// PublishEffort records the active thinking effort without touching the
// transcript. Safe before Run (applies directly) and usable for the event
// path used live.
func (p *Program) PublishEffort(effort string) {
	p.state.Apply(ModelEffortUpdated{Effort: effort})
}

// PublishModel records the active model without touching the transcript.
// Safe before Run and usable for the event path used live. Empty renders as
// "no model" in the header rather than a blank segment.
func (p *Program) PublishModel(model string) {
	p.state.Status.Model = strings.TrimSpace(model)
}

// SetEffortProvider installs the reporter behind the header effort badge.
// Nil clears it: the badge then reflects only Status.Effort and any variant
// suffix in Status.Model.
func (p *Program) SetEffortProvider(current func() string) {
	p.currentEffort = current
}
