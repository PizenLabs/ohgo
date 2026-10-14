package tui

import "strings"

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
