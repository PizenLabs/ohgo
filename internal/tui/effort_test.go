package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Normalizing the effort level keeps the header honest: surrounding space and
// case collapse to one spelling, disabled and unsafe tokens stay absent, and a
// native endpoint level remains visible.
func TestNormalizeEffortCollapsesSpellingAndOmitsDisabledOrUnsafe(t *testing.T) {
	cases := map[string]string{
		"max":       "max",
		" MAX ":     "max",
		"High":      "high",
		"low":       "low",
		"medium":    "medium",
		"xhigh":     "xhigh",
		"minimal":   "minimal",
		"":          "",
		"   ":       "",
		"off":       "",
		" OFF ":     "",
		"none":      "",
		"standard":  "standard",
		"bad level": "",
		"bad/level": "",
	}
	for in, want := range cases {
		if got := NormalizeEffort(in); got != want {
			t.Errorf("NormalizeEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

// A trailing "(level)" variant supplies the badge without duplicating it in
// the model id, while any other parenthetical stays part of the id so a model
// that merely contains parens never loses characters.
func TestSplitEffortVariantParsesOnlyKnownLevels(t *testing.T) {
	cases := []struct{ ref, base, effort string }{
		{"openrouter/vendor/base(max)", "openrouter/vendor/base", "max"},
		{"openrouter/vendor/base(MAX)", "openrouter/vendor/base", "max"},
		{"openrouter/vendor/base:free(high)", "openrouter/vendor/base:free", "high"},
		{"openrouter/vendor/base ( low )", "openrouter/vendor/base", "low"},
		{"openrouter/vendor/base(off)", "openrouter/vendor/base", ""},
		{"openrouter/vendor/base", "openrouter/vendor/base", ""},
		{"openrouter/vendor/base(free)", "openrouter/vendor/base(free)", ""},
		{"openrouter/vendor/base(bogus)", "openrouter/vendor/base(bogus)", ""},
		{"openrouter/vendor/base()", "openrouter/vendor/base()", ""},
		{"(max)", "(max)", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		base, effort := SplitEffortVariant(tc.ref)
		if base != tc.base || effort != tc.effort {
			t.Errorf("SplitEffortVariant(%q) = (%q, %q), want (%q, %q)",
				tc.ref, base, effort, tc.base, tc.effort)
		}
	}
}

// An explicit level wins over a variant suffix, so the header never renders
// the level twice; without an explicit level the suffix supplies it, and
// "(off)" strips without badging since disabled thinking has no level.
func TestEffectiveModelEffortPrefersExplicitOverVariant(t *testing.T) {
	if base, effort := EffectiveModelEffort("openrouter/vendor/base(max)", "low"); base != "openrouter/vendor/base" || effort != "low" {
		t.Errorf("explicit wins = (%q, %q), want (base, low)", base, effort)
	}
	if base, effort := EffectiveModelEffort("openrouter/vendor/base(max)", ""); base != "openrouter/vendor/base" || effort != "max" {
		t.Errorf("variant supplies = (%q, %q), want (base, max)", base, effort)
	}
	if base, effort := EffectiveModelEffort("openrouter/vendor/base(off)", ""); base != "openrouter/vendor/base" || effort != "" {
		t.Errorf("off strips = (%q, %q), want (base, empty)", base, effort)
	}
	if base, effort := EffectiveModelEffort("openrouter/vendor/base", ""); base != "openrouter/vendor/base" || effort != "" {
		t.Errorf("bare stays bare = (%q, %q), want no badge", base, effort)
	}
	if base, effort := EffectiveModelEffort("openrouter/vendor/base", "bad level"); base != "openrouter/vendor/base" || effort != "" {
		t.Errorf("unsafe token omits = (%q, %q), want no badge", base, effort)
	}
	if base, effort := EffectiveModelEffort("openrouter/vendor/base", "standard"); base != "openrouter/vendor/base" || effort != "standard" {
		t.Errorf("native level = (%q, %q), want the custom badge", base, effort)
	}
}

// The plain model segment is the layout contract the styled header wraps:
// a level appends " ◈ <level>", anything else leaves the id untouched.
func TestFormatModelWithEffortAppendsBadgeOnlyWhenSet(t *testing.T) {
	cases := []struct{ model, effort, want string }{
		{"openrouter/cohere/north-mini-code:free", "max", "openrouter/cohere/north-mini-code:free ◈ max"},
		{"openrouter/cohere/north-mini-code:free", "low", "openrouter/cohere/north-mini-code:free ◈ low"},
		{"openrouter/cohere/north-mini-code:free", "", "openrouter/cohere/north-mini-code:free"},
		{"openrouter/cohere/north-mini-code:free", "off", "openrouter/cohere/north-mini-code:free"},
		{"openrouter/cohere/north-mini-code:free", "bad level", "openrouter/cohere/north-mini-code:free"},
		{"openrouter/cohere/north-mini-code:free", "standard", "openrouter/cohere/north-mini-code:free ◈ standard"},
		{"openrouter/vendor/base(max)", "", "openrouter/vendor/base ◈ max"},
		{"openrouter/vendor/base(max)", "low", "openrouter/vendor/base ◈ low"},
	}
	for _, tc := range cases {
		if got := FormatModelWithEffort(tc.model, tc.effort); got != tc.want {
			t.Errorf("FormatModelWithEffort(%q, %q) = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
}

// The badge survives provider-prefix shedding but never hard truncation: when
// even the compact leaf plus the badge overflows, the badge drops and the
// core id receives the full budget.
func TestFitHeaderModelWithEffortDropsBadgeBeforeTruncating(t *testing.T) {
	const model = "groq/deepseek-r1-distill-llama-70b"
	const leaf = "deepseek-r1-distill-llama-70b"

	// Room for everything: full id plus badge.
	if m, e := FitHeaderModelWithEffort(model, "max", 200); m != model || e != "max" {
		t.Errorf("roomy = (%q, %q), want full id plus max", m, e)
	}
	// Room for the leaf plus badge but not the full id: the prefix sheds and
	// the badge survives.
	leafPlusBadge := CellWidth(leaf + " ◈ max")
	fullPlusBadge := CellWidth(model + " ◈ max")
	if fullPlusBadge <= leafPlusBadge {
		t.Fatalf("fixture has no shedding window: full %d leaf %d", fullPlusBadge, leafPlusBadge)
	}
	budget := leafPlusBadge + 2
	if m, e := FitHeaderModelWithEffort(model, "max", budget); m != leaf || e != "max" {
		t.Errorf("shedding = (%q, %q), want leaf plus max", m, e)
	}
	// One cell tighter than leaf-plus-badge: the badge drops and the core id
	// receives the whole budget instead of truncating around a badge.
	if m, e := FitHeaderModelWithEffort(model, "max", leafPlusBadge-1); e != "" {
		t.Errorf("tight = (%q, %q), want the badge dropped, not a truncated badge", m, e)
	} else if m != fitHeaderModel(model, leafPlusBadge-1) {
		t.Errorf("tight model = %q, want the same id the badgeless path fits", m)
	}
	// Without a level the helper degrades to the plain fitter.
	if m, e := FitHeaderModelWithEffort(model, "", 10); e != "" || m != fitHeaderModel(model, 10) {
		t.Errorf("badgeless = (%q, %q), want the plain fit", m, e)
	}
	// A non-positive budget never overflows.
	if m, e := FitHeaderModelWithEffort(model, "max", 0); m != "" || e != "" {
		t.Errorf("zero budget = (%q, %q), want empty", m, e)
	}
}

// The header shows the badge directly after the model when a level applies,
// and omits it cleanly otherwise: no stray glyph, no double space, no
// placeholder.
func TestHeaderShowsEffortBadgeOnlyWhenSet(t *testing.T) {
	newState := func(model, effort string) State {
		s := New()
		s.Status.Model = model
		s.Status.Effort = effort
		s.Apply(TerminalResized{Width: 140, Height: 24})
		return s
	}
	with := ansi.Strip(strings.Split(Render(newState("openrouter/cohere/north-mini-code:free", "max")), "\n")[0])
	if !strings.Contains(with, "openrouter/cohere/north-mini-code:free ◈ max") {
		t.Errorf("header with effort = %q, want the model plus the badge", with)
	}
	for _, tc := range []struct{ model, effort string }{
		{"openrouter/cohere/north-mini-code:free", ""},
		{"openrouter/cohere/north-mini-code:free", "off"},
		{"openrouter/cohere/north-mini-code:free", "bad level"},
	} {
		header := ansi.Strip(strings.Split(Render(newState(tc.model, tc.effort)), "\n")[0])
		if strings.Contains(header, "◈") {
			t.Errorf("header(%q, %q) = %q, want no badge", tc.model, tc.effort, header)
		}
		if !strings.Contains(header, tc.model) {
			t.Errorf("header(%q, %q) = %q, want the model id kept", tc.model, tc.effort, header)
		}
	}
	// A variant suffix in the id badges without an explicit level, and never
	// renders twice when both supply the same level.
	variant := ansi.Strip(strings.Split(Render(newState("openrouter/vendor/base(max)", "")), "\n")[0])
	if !strings.Contains(variant, "openrouter/vendor/base ◈ max") {
		t.Errorf("variant header = %q, want the stripped base plus the badge", variant)
	}
	if strings.Contains(variant, "(max)") {
		t.Errorf("variant header = %q, want the suffix stripped, not duplicated", variant)
	}
	both := ansi.Strip(strings.Split(Render(newState("openrouter/vendor/base(max)", "low")), "\n")[0])
	if !strings.Contains(both, "openrouter/vendor/base ◈ low") || strings.Contains(both, "(max)") {
		t.Errorf("explicit-over-variant header = %q, want base plus low only", both)
	}
}

// A narrow terminal sheds the badge before it truncates the leaf: the header
// stays one row, the leaf head survives, and the badge is the first detail
// to go.
func TestHeaderDropsEffortBeforeTruncatingModel(t *testing.T) {
	const model = "groq/deepseek-r1-distill-llama-70b"
	const leafHead = "deepseek-r"
	for _, width := range []int{70, 60, 55, 54, 50, 40} {
		s := New()
		s.Status.Model = model
		s.Status.Effort = "max"
		s.Apply(TerminalResized{Width: width, Height: 24})
		if ComputeLayout(s).Mode == LayoutMini {
			continue
		}
		header := ansi.Strip(strings.Split(Render(s), "\n")[0])
		if got := CellWidth(header); got > width {
			t.Errorf("width %d: header is %d cells, want one row: %q", width, got, header)
		}
		if !strings.Contains(header, leafHead) {
			t.Errorf("width %d: header lost the leaf head %q: %q", width, leafHead, header)
		}
		// At the tightest widths the badge must be gone: keeping it would
		// force the leaf itself to truncate harder.
		if width <= 50 && strings.Contains(header, "◈") {
			// The badge may only survive here if the leaf still fits whole
			// beside it; a truncated leaf beside a badge means the budget
			// went to decoration instead of orientation.
			if !strings.Contains(header, "deepseek-r1-distill-llama-70b") {
				t.Errorf("width %d: header kept the badge over the leaf: %q", width, header)
			}
		}
	}
}

// The badge styles through the chrome roles: the glyph in the check amber,
// the level in muted context, so it stays distinct from the blue-slate model
// without competing with the active phase. Stripping returns the plain
// contract, so the viewport budget measured on plain text still holds.
func TestHeaderEffortBadgeUsesChromeRoles(t *testing.T) {
	chrome := ChromeFor(ThemeDark)
	s := New()
	s.Status.Model = "groq/m"
	s.Status.Effort = "max"
	s.Apply(TerminalResized{Width: 140, Height: 24})
	out := Render(s)
	header := strings.Split(out, "\n")[0]
	if !strings.Contains(header, chrome.Check.Render("◈")) {
		t.Errorf("header missing the check-styled glyph:\n%s", ansi.Strip(header))
	}
	if !strings.Contains(header, chrome.Context.Render("max")) {
		t.Errorf("header missing the context-styled level:\n%s", ansi.Strip(header))
	}
	if got := ansi.Strip(header); !strings.Contains(got, "groq/m ◈ max") {
		t.Errorf("stripped header = %q, want the plain badge contract", got)
	}
}

// /effort must name the role and the model the runtime is actually calling,
// and list only the levels that model exposes. The report is where a user
// confirms which workspace role the change will land on.
func TestFormatEffortReportNamesRoleModelAndLadder(t *testing.T) {
	report := FormatEffortReport(
		"build",
		"anthropic/claude-sonnet-4-5",
		RoleAssignment{Role: "build", Efforts: []string{"minimal", "low", "high", "high"}},
		"high",
	)
	for _, want := range []string{
		"BUILD", "anthropic/claude-sonnet-4-5",
		"effort: auto (last high)",
		"levels: off, minimal, low, high",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report = %q, want %q", report, want)
		}
	}
	if strings.Count(report, "high") != 2 {
		// Once in "last high", once in the deduplicated ladder.
		t.Errorf("report = %q, want the ladder deduplicated", report)
	}
	off := FormatEffortReport("ask", "m", RoleAssignment{Role: "ask", Effort: EffortOff}, "")
	if !strings.Contains(off, "effort: off") || strings.Contains(off, "effort: auto") {
		t.Errorf("off report = %q, want the explicit disabled state", off)
	}
}

// effortNotice concatenates the transcript notices a command emitted.
func effortNotice(p *Program) string {
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice + "\n"
		}
	}
	return text
}

// Bare /effort opens the active workspace role's picker. The card names the
// role and live model, lists only that model's ladder, and marks Auto current.
func TestEffortCommandOpensPickerForTheActiveWorkspace(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 88, Height: 28})
	p.state.Status.Model = "anthropic/claude-haiku-4-5"
	p.state.Status.Workspace = "ASK"
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{
				{Role: "ask", Model: "anthropic/claude-haiku-4-5", Efforts: []string{"low", "high"}, Auto: true, EffortMode: "auto"},
				{Role: "build", Model: "anthropic/claude-sonnet-4-5", Efforts: []string{"medium", "xhigh"}},
			}
		},
		ActiveRole: func() string { return "ask" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			return RoleAssignment{Role: role, Effort: effort}, nil, nil
		},
	})

	Command{Name: "effort"}.runOn(p)

	if p.state.Picker == nil || p.state.Picker.Kind != PickerEfforts {
		t.Fatalf("picker = %+v, want the effort chooser", p.state.Picker)
	}
	if p.state.Picker.Cursor != 0 || !p.state.Picker.Items[0].Active {
		t.Errorf("picker cursor/active = %d/%v, want the current Auto row",
			p.state.Picker.Cursor, p.state.Picker.Items[0].Active)
	}
	if got := p.state.Picker.Items[0].Detail; got != "follow the work's demand" {
		t.Errorf("Auto detail = %q, want the demand description", got)
	}
	out := ansi.Strip(Render(p.state))
	for _, want := range []string{
		"EFFORT", "ASK · anthropic/claude-haiku-4-5",
		"Auto", "Off", "Low", "High", "Enter", "Esc",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("effort card = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "sonnet") || strings.Contains(out, "xhigh") {
		t.Errorf("effort card = %q, want only the active ASK model's ladder", out)
	}
	if notes := effortNotice(p); notes != "" {
		t.Errorf("opening the picker wrote notices: %q", notes)
	}
}

// Selecting a row applies it to the role that opened the card and updates the
// badge; the fixed-row picker has no search state to clear first.
func TestEffortPickerSelectionAppliesToTheActiveRole(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 88, Height: 28})
	p.state.Status.Model = "anthropic/claude-haiku-4-5"
	p.state.Status.Workspace = "ASK"
	var gotRole, gotEffort string
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{{
				Role: "ask", Model: "anthropic/claude-haiku-4-5",
				Effort: "high", Efforts: []string{"low", "high"},
			}}
		},
		ActiveRole: func() string { return "ask" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			gotRole, gotEffort = role, effort
			return RoleAssignment{Role: role, Model: "anthropic/claude-haiku-4-5", Effort: effort}, nil, nil
		},
	})

	Command{Name: "effort"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("effort picker did not open")
	}
	// Auto, Off, Low, High: the explicit high level starts highlighted.
	if p.state.Picker.Cursor != 3 || !p.state.Picker.Items[3].Active {
		t.Fatalf("cursor/active = %d/%v, want the current High row",
			p.state.Picker.Cursor, p.state.Picker.Items[3].Active)
	}
	if got := p.state.Picker.Items[0].Detail; got != "clear the role override" {
		t.Errorf("Auto detail = %q, want the override-clearing description", got)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp}) // Low
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp}) // Off
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})

	if gotRole != "ask" || gotEffort != EffortOff {
		t.Fatalf("SetEffort(%q, %q), want (ask, off)", gotRole, gotEffort)
	}
	if p.state.Picker != nil {
		t.Error("effort picker stayed open after selection")
	}
	if p.state.Status.Effort != "" {
		t.Errorf("status effort = %q, want disabled", p.state.Status.Effort)
	}
	if text := effortNotice(p); !strings.Contains(text, "ASK effort: off") {
		t.Errorf("notice = %q, want the applied off state", text)
	}
}

// Esc dismisses the chooser without persisting a level.
func TestEffortPickerEscapeDoesNotApply(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 88, Height: 28})
	p.state.Status.Workspace = "BUILD"
	called := false
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{{Role: "build", Model: "anthropic/claude-sonnet-4-5", Efforts: []string{"low", "high"}}}
		},
		ActiveRole: func() string { return "build" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			called = true
			return RoleAssignment{Role: role, Effort: effort}, nil, nil
		},
	})

	Command{Name: "effort"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyEscape})

	if called {
		t.Error("Esc persisted an effort level")
	}
	if p.state.Picker != nil {
		t.Error("Esc left the effort picker open")
	}
}

// /effort <level> changes the active role's effort, not another role's, and
// the header badge follows what the authority accepted.
func TestEffortCommandTargetsTheActiveRoleAndUpdatesTheBadge(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Model = "anthropic/claude-haiku-4-5"
	p.state.Status.Workspace = "ASK"
	var gotRole, gotEffort string
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{{Role: "ask", Model: "anthropic/claude-haiku-4-5", Efforts: []string{"low", "high"}}}
		},
		ActiveRole: func() string { return "ask" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			gotRole, gotEffort = role, effort
			return RoleAssignment{Role: role, Model: "anthropic/claude-haiku-4-5", Effort: effort}, nil, nil
		},
	})

	Command{Name: "effort", Args: []string{"high"}}.runOn(p)

	if gotRole != "ask" || gotEffort != "high" {
		t.Fatalf("SetEffort(%q, %q), want (ask, high)", gotRole, gotEffort)
	}
	if p.state.Status.Effort != "high" {
		t.Errorf("status effort = %q, want the applied level", p.state.Status.Effort)
	}
	text := effortNotice(p)
	for _, want := range []string{"ASK effort: high", "anthropic/claude-haiku-4-5"} {
		if !strings.Contains(text, want) {
			t.Errorf("notice = %q, want %q", text, want)
		}
	}
}

// A refused level reaches the transcript with the authority's reason; the
// badge must not claim a level the role does not carry.
func TestEffortCommandSurfacesAnUnsupportedLevel(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Model = "groq/llama-3.1-8b-instant"
	p.state.Status.Workspace = "BUILD"
	refused := errors.New(`groq/llama-3.1-8b-instant does not expose thinking effort "high"`)
	p.SetModelControl(ModelControlHooks{
		ActiveRole: func() string { return "build" },
		SetEffort: func(string, string) (RoleAssignment, []RoleAssignment, error) {
			return RoleAssignment{}, nil, refused
		},
	})

	Command{Name: "effort", Args: []string{"high"}}.runOn(p)

	if p.state.Status.Effort != "" {
		t.Errorf("status effort = %q, want unchanged after a refusal", p.state.Status.Effort)
	}
	if text := effortNotice(p); !strings.Contains(text, refused.Error()) {
		t.Errorf("notice = %q, want the authority's reason", text)
	}
}

// A program without the persistence seam reports why /effort cannot write
// instead of silently accepting a level.
func TestEffortCommandWithoutASeamSaysUnavailable(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Workspace = "BUILD"
	Command{Name: "effort", Args: []string{"high"}}.runOn(p)
	if text := effortNotice(p); !strings.Contains(text, "configuration is unavailable") {
		t.Errorf("notice = %q, want the unavailable reason", text)
	}
}

// The effort event normalizes on apply, so " MAX " lands as "max" and "off"
// clears the badge rather than rendering a level for disabled thinking.
func TestModelEffortUpdatedNormalizesOnApply(t *testing.T) {
	s := New()
	s.Apply(ModelEffortUpdated{Effort: " MAX "})
	if s.Status.Effort != "max" {
		t.Errorf("effort = %q, want max", s.Status.Effort)
	}
	s.Apply(ModelEffortUpdated{Effort: "off"})
	if s.Status.Effort != "" {
		t.Errorf("effort after off = %q, want cleared", s.Status.Effort)
	}
	s.Apply(ModelEffortUpdated{Effort: "bad level"})
	if s.Status.Effort != "" {
		t.Errorf("effort after an unsafe token = %q, want cleared", s.Status.Effort)
	}
	s.Apply(ModelEffortUpdated{Effort: "standard"})
	if s.Status.Effort != "standard" {
		t.Errorf("effort after a native level = %q, want standard", s.Status.Effort)
	}
}

// A global or invocation policy is not Auto. The card must not mark the Auto
// row current merely because the role stores no fixed level.
func TestEffortPickerDoesNotCallGlobalPolicyAuto(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 88, Height: 28})
	p.state.Status.Model = "anthropic/claude-haiku-4-5"
	p.state.Status.Workspace = "BUILD"
	p.SetEffortProvider(func() string { return "medium" })
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{{
				Role: "build", Model: "anthropic/claude-haiku-4-5",
				Efforts: []string{"low", "medium", "high"}, EffortMode: "global",
			}}
		},
		ActiveRole: func() string { return "build" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			return RoleAssignment{Role: role, Effort: effort}, nil, nil
		},
	})

	Command{Name: "effort"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("effort picker did not open")
	}
	if p.state.Picker.Items[0].Active {
		t.Error("Auto is active under a global policy")
	}
	if got := p.state.Picker.Items[0].Detail; got != "enable work demand · currently medium" {
		t.Errorf("Auto detail = %q, want the global-policy explanation", got)
	}
}

// Choosing Auto applies the role mode, not an empty level. The picker is
// fixed-row, so Auto is reached by navigation even when an explicit level was
// current.
func TestEffortPickerSelectionAppliesAutoMode(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 88, Height: 28})
	p.state.Status.Model = "anthropic/claude-haiku-4-5"
	p.state.Status.Workspace = "ASK"
	var gotRole, gotEffort string
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{{
				Role: "ask", Model: "anthropic/claude-haiku-4-5",
				Effort: "high", Efforts: []string{"low", "high"}, EffortMode: "explicit",
			}}
		},
		ActiveRole: func() string { return "ask" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			gotRole, gotEffort = role, effort
			return RoleAssignment{Role: role, Model: "anthropic/claude-haiku-4-5", Auto: true, EffortMode: "auto"}, nil, nil
		},
	})

	Command{Name: "effort"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp}) // Low
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp}) // Off
	keyPress(p, tea.KeyMsg{Type: tea.KeyUp}) // Auto
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})

	if gotRole != "ask" || gotEffort != EffortAuto {
		t.Fatalf("SetEffort(%q, %q), want (ask, auto)", gotRole, gotEffort)
	}
	if text := effortNotice(p); !strings.Contains(text, "ASK effort: auto") {
		t.Errorf("notice = %q, want the applied Auto mode", text)
	}
}

// A configured endpoint may publish effort names outside the bundled
// vocabulary. Once the authority accepts one, the header must render it rather
// than hiding the level AUTO just selected.
func TestHeaderShowsACustomEndpointEffort(t *testing.T) {
	s := New()
	s.Status.Model = "vllm/qwen"
	s.Status.Effort = "standard"
	s.Apply(TerminalResized{Width: 120, Height: 24})
	header := ansi.Strip(strings.Split(Render(s), "\n")[0])
	if !strings.Contains(header, "vllm/qwen ◈ standard") {
		t.Errorf("header = %q, want the custom native effort badge", header)
	}
}

// A global or invocation disable is still Off. The card must mark the Off row
// from the effective mode, not only from a stored per-role "off" string.
func TestEffortPickerMarksEffectiveOffMode(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 88, Height: 28})
	p.state.Status.Model = "anthropic/claude-haiku-4-5"
	p.state.Status.Workspace = "BUILD"
	p.SetModelControl(ModelControlHooks{
		Roles: func() []RoleAssignment {
			return []RoleAssignment{{
				Role: "build", Model: "anthropic/claude-haiku-4-5",
				Efforts: []string{"low", "high"}, EffortMode: EffortModeOff,
			}}
		},
		ActiveRole: func() string { return "build" },
		SetEffort: func(role, effort string) (RoleAssignment, []RoleAssignment, error) {
			return RoleAssignment{Role: role, Effort: effort}, nil, nil
		},
	})

	Command{Name: "effort"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("effort picker did not open")
	}
	if p.state.Picker.Items[0].Active {
		t.Error("Auto is active while effort is off")
	}
	if !p.state.Picker.Items[1].Active {
		t.Error("Off is not active despite the effective Off mode")
	}
}
