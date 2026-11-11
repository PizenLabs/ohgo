package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	diffview "github.com/PizenLabs/ohgo/internal/tui/diff"
)

// appearanceProbe builds a systemAppearance with no host access.
func appearanceProbe(goos string, env map[string]string, run appearanceRunner) systemAppearance {
	return systemAppearance{
		goos: goos,
		getenv: func(key string) string {
			return env[key]
		},
		run: run,
	}
}

func fixedRunner(out string, err error) appearanceRunner {
	return func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), err
	}
}

// macOS dark mode is the presence of AppleInterfaceStyle=Dark; an absent key is
// Aqua (light), while a missing tool is unknown rather than a false light
// claim.
func TestSystemAppearanceDarwin(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want string
		ok   bool
	}{
		{name: "dark", out: "Dark\n", want: ThemeDark, ok: true},
		{name: "light", out: "Dark\n", err: &exec.ExitError{}, want: "", ok: false},
		{name: "absent key is light", out: "The domain/default pair does not exist", err: &exec.ExitError{}, want: ThemeLight, ok: true},
		{name: "missing command", err: exec.ErrNotFound, want: "", ok: false},
		{name: "permission failure", out: "Failed to write domain", err: &exec.ExitError{}, want: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := appearanceProbe("darwin", nil, fixedRunner(tc.out, tc.err)).detect()
			if got != tc.want || ok != tc.ok {
				t.Fatalf("detect = (%q,%v), want (%q,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// A remote shell cannot see the client's desktop, even when the server is a
// supported OS. Auto must fall back rather than report a system result.
func TestSystemAppearanceRemoteIsUnknown(t *testing.T) {
	for _, env := range []map[string]string{
		{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22"},
		{"SSH_CLIENT": "10.0.0.1"},
		{"SSH_TTY": "/dev/ttys001"},
	} {
		got, ok := appearanceProbe("darwin", env, fixedRunner("Dark", nil)).detect()
		if ok || got != "" {
			t.Errorf("remote detect = (%q,%v), want unknown", got, ok)
		}
	}
}

// GNOME's explicit colour-scheme wins, the GTK theme name is the fallback, and
// an unavailable desktop yields unknown.
func TestSystemAppearanceLinux(t *testing.T) {
	t.Run("prefer dark", func(t *testing.T) {
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "gsettings" && strings.Contains(strings.Join(args, " "), "color-scheme") {
				return []byte("'prefer-dark'\n"), nil
			}
			return nil, exec.ErrNotFound
		}
		got, ok := appearanceProbe("linux", nil, run).detect()
		if !ok || got != ThemeDark {
			t.Fatalf("detect = (%q,%v), want dark", got, ok)
		}
	})
	t.Run("gtk theme fallback", func(t *testing.T) {
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "color-scheme") {
				return nil, errors.New("no schema")
			}
			if strings.Contains(joined, "gtk-theme") {
				return []byte("'Adwaita-dark'\n"), nil
			}
			return nil, exec.ErrNotFound
		}
		got, ok := appearanceProbe("linux", nil, run).detect()
		if !ok || got != ThemeDark {
			t.Fatalf("detect = (%q,%v), want dark", got, ok)
		}
	})
	t.Run("headless", func(t *testing.T) {
		got, ok := appearanceProbe("linux", nil, fixedRunner("", exec.ErrNotFound)).detect()
		if ok || got != "" {
			t.Fatalf("detect = (%q,%v), want unknown", got, ok)
		}
	})
}

// Windows exposes the app appearance as a per-user DWORD.
func TestSystemAppearanceWindows(t *testing.T) {
	cases := map[string]string{
		"0x0": ThemeDark,
		"0x1": ThemeLight,
	}
	for value, want := range cases {
		got, ok := appearanceProbe("windows", nil, fixedRunner("AppsUseLightTheme    REG_DWORD    "+value, nil)).detect()
		if !ok || got != want {
			t.Errorf("%s detect = (%q,%v), want %q", value, got, ok, want)
		}
	}
	if got, ok := appearanceProbe("windows", nil, fixedRunner("no such value", nil)).detect(); ok || got != "" {
		t.Errorf("malformed registry value = (%q,%v), want unknown", got, ok)
	}
}

// SetTheme separates preference from effective appearance and never asks the
// detector when the user forced a palette.
func TestThemePreferenceResolvesIndependently(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	calls := 0
	p.SetAppearanceDetector(func() (string, bool) {
		calls++
		return ThemeLight, true
	})

	p.SetTheme(ThemeAuto)
	if p.state.ThemePreference != ThemeAuto || p.state.Theme != ThemeLight || p.state.ThemeSource != ThemeSourceSystem {
		t.Fatalf("auto = pref %q theme %q source %q, want auto/light/system",
			p.state.ThemePreference, p.state.Theme, p.state.ThemeSource)
	}
	if calls != 1 {
		t.Fatalf("detector calls = %d, want one", calls)
	}

	p.SetTheme(ThemeDark)
	if p.state.ThemePreference != ThemeDark || p.state.Theme != ThemeDark || p.state.ThemeSource != ThemeSourceExplicit {
		t.Fatalf("dark = pref %q theme %q source %q, want dark/dark/explicit",
			p.state.ThemePreference, p.state.Theme, p.state.ThemeSource)
	}
	if calls != 1 {
		t.Fatalf("explicit preference consulted the detector %d times, want one total", calls)
	}
}

// An unavailable detector is a documented dark fallback, and the Auto
// preference is preserved rather than being rewritten to the resolved value.
func TestThemeAutoFallbackPreservesPreference(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetAppearanceDetector(func() (string, bool) { return "", false })
	p.SetTheme(ThemeAuto)

	if p.state.Theme != ThemeDark || p.state.ThemeSource != ThemeSourceFallback {
		t.Fatalf("fallback = theme %q source %q, want dark/fallback", p.state.Theme, p.state.ThemeSource)
	}
	if p.state.ThemePreference != ThemeAuto {
		t.Fatalf("preference = %q, want auto preserved", p.state.ThemePreference)
	}
	if !strings.Contains(themeSummaryFor(p.state), "fallback") {
		t.Fatalf("summary = %q, want the fallback named", themeSummaryFor(p.state))
	}
}

// A retained styling model receives appearance changes with the rest of the
// UI, so onboarding cannot stay on the previous palette.
func TestThemeChangeReachesOnboarding(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetTheme(ThemeDark)
	wizard := NewInitWizard(false, false, "", "")
	p.SetInitWizard(wizard, InitWizardHooks{})
	p.SetTheme(ThemeLight)
	if wizard.theme != ThemeLight {
		t.Fatalf("wizard theme = %q, want light", wizard.theme)
	}
}

// The diff inspector retains resolved styles, so it must follow an appearance
// change instead of keeping the palette it opened with.
func TestThemeChangeReachesDiffInspector(t *testing.T) {
	forceTrueColor(t)
	p := NewProgram(nil, "test", 0)
	p.SetTheme(ThemeDark)
	model := diffview.New(nil, diffview.Spec{}, "diff")
	p.state.Diff = model
	p.SetTheme(ThemeLight)
	if out := model.View(); !strings.Contains(out, rgbSeq(LightTokens().TextMuted)) {
		t.Fatalf("diff inspector did not adopt the light palette: %q", out)
	}
}

// The Auto poll applies a changed host appearance without rewriting the
// preference, and a probe issued for an older preference generation is stale.
func TestAppearancePollUpdatesAutoOnly(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	detected := ThemeDark
	p.SetAppearanceDetector(func() (string, bool) { return detected, true })
	p.SetTheme(ThemeAuto)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})

	p.lastAppearanceCheck = time.Now().Add(-appearancePollInterval - time.Second)
	cmd := p.appearancePoll(time.Now())
	if cmd == nil {
		t.Fatal("auto poll produced no probe")
	}
	detected = ThemeLight
	msg := cmd()
	p.Update(msg)

	if p.state.Theme != ThemeLight || p.state.ThemeSource != ThemeSourceSystem {
		t.Fatalf("poll = theme %q source %q, want light/system", p.state.Theme, p.state.ThemeSource)
	}
	if p.state.ThemePreference != ThemeAuto {
		t.Fatalf("preference = %q, want auto", p.state.ThemePreference)
	}

	// An explicit choice invalidates a probe already in flight.
	p.SetTheme(ThemeDark)
	stale := appearanceProbeMsg{Seq: p.appearanceSeq - 1, Theme: ThemeLight, OK: true}
	p.Update(stale)
	if p.state.Theme != ThemeDark || p.state.ThemePreference != ThemeDark {
		t.Fatalf("stale probe changed the explicit choice: theme %q pref %q", p.state.Theme, p.state.ThemePreference)
	}
}

// The direct /theme command persists the preference, not the resolved palette,
// and an unknown argument leaves the current choice untouched.
func TestThemeCommandPersistsPreference(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetAppearanceDetector(func() (string, bool) { return ThemeLight, true })
	var saved []string
	p.SetThemeChangedHandler(func(theme string) { saved = append(saved, theme) })

	Command{Name: "theme", Args: []string{"auto"}}.runOn(p)
	if p.state.ThemePreference != ThemeAuto || p.state.Theme != ThemeLight {
		t.Fatalf("auto = pref %q theme %q, want auto/light", p.state.ThemePreference, p.state.Theme)
	}
	if len(saved) != 1 || saved[0] != ThemeAuto {
		t.Fatalf("saved = %v, want auto", saved)
	}

	Command{Name: "theme", Args: []string{"solarized"}}.runOn(p)
	if p.state.ThemePreference != ThemeAuto || len(saved) != 1 {
		t.Fatalf("unknown argument changed pref %q or persisted %v", p.state.ThemePreference, saved)
	}
}
