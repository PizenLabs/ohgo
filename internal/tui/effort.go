package tui

import (
	"strings"
	"unicode"
)

// knownEfforts is the bundled catalog vocabulary used only to recognize a
// trailing "(effort)" variant in a model reference. It is not a display
// allowlist: a configured endpoint may publish another native level, and the
// header must show the level the authority accepted.
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
// It trims space, lowercases, and returns "" for empty, "off", "none", or a
// token with unsafe characters. The level itself is model data, not a fixed
// catalog enum: a configured endpoint may publish "standard" or another native
// name, and hiding it would make AUTO's selected level invisible. Authority
// validation remains the harness's job; this function only protects rendering.
func NormalizeEffort(effort string) string {
	level := strings.ToLower(strings.TrimSpace(effort))
	if level == "" || level == "off" || level == "none" ||
		level == "auto" || level == "default" || level == "unset" {
		return ""
	}
	runes := []rune(level)
	if len(runes) > 32 {
		return ""
	}
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
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
// configured level is cleared; a global or invocation policy is replaced by
// demand. The current level is named only when it is a real observed level.
func effortAutoDetail(configured, current string, auto bool, mode string) string {
	current = NormalizeEffort(current)
	if auto {
		if current != "" {
			return "work demand · last " + current
		}
		return "follow the work's demand"
	}
	if strings.TrimSpace(configured) != "" {
		return "clear the role override"
	}
	switch mode {
	case EffortModeGlobal, EffortModeInvocation:
		if current != "" {
			return "enable work demand · currently " + current
		}
		return "enable work demand"
	case EffortModeOff:
		return "enable work demand"
	default:
		return "follow the work's demand"
	}
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
// mode, configured effort, and the bound model's authoritative ladder;
// effective is the last level the per-request policy projected. The report
// names the role and model first because adjusting the wrong role's thinking
// is the failure the command exists to prevent.
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
	state := effortState(assignment, effective)
	return role + " · " + model + " · effort: " + state +
		"\nlevels: " + strings.Join(effortLevels(assignment.Efforts), ", ") +
		" · /effort <level|off|auto>"
}

// effortState names the actual authority selecting effort. "auto" is reserved
// for demand; a global or invocation policy is reported as such even though the
// role stores no level.
func effortState(assignment RoleAssignment, effective string) string {
	level := strings.ToLower(strings.TrimSpace(assignment.Effort))
	if level == EffortOff {
		return EffortOff
	}
	if level != "" {
		return level
	}
	current := strings.ToLower(strings.TrimSpace(effective))
	if assignment.Auto {
		if current != "" {
			return "auto (last " + current + ")"
		}
		return "auto"
	}
	switch assignment.EffortMode {
	case EffortModeOff:
		return EffortOff
	case EffortModeGlobal:
		if current != "" {
			return "global (currently " + current + ")"
		}
		return "global"
	case EffortModeInvocation:
		if current != "" {
			return "run override (currently " + current + ")"
		}
		return "run override"
	default:
		// Assignments produced before the mode field existed still describe
		// an inherited role policy; keep the truthful old spelling rather than
		// claiming auto for an unknown mode.
		if current != "" {
			return "auto (last " + current + ")"
		}
		return "auto"
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

// FormatEffortApplied names the role, the applied mode or level, and the model
// the next request will call. An empty level is reported as "auto" only when
// demand owns the role; a global/cleared policy is reported as "default".
func FormatEffortApplied(result RoleAssignment, model string) string {
	if strings.TrimSpace(model) == "" {
		model = result.Model
	}
	level := strings.ToLower(strings.TrimSpace(result.Effort))
	if level == "" {
		switch {
		case result.Auto || result.EffortMode == "" || result.EffortMode == EffortModeAuto:
			level = "auto"
		case result.EffortMode == EffortModeInvocation:
			level = "run override"
		case result.EffortMode == EffortModeOff:
			level = EffortOff
		default:
			level = "default"
		}
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
