package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Auto is the appearance default for a new or unset configuration. The stored
// value is a preference, so it never claims a palette by itself.
func TestThemeDefaultsToAuto(t *testing.T) {
	if got := Default().ThemePreference(); got != ThemeAuto {
		t.Fatalf("default theme = %q, want auto", got)
	}
	if got := (Settings{}).ThemePreference(); got != ThemeAuto {
		t.Fatalf("zero-value theme = %q, want auto", got)
	}
}

func TestNormalizeThemePreference(t *testing.T) {
	cases := map[string]string{
		"auto":     ThemeAuto,
		"AUTO":     ThemeAuto,
		" dark ":   ThemeDark,
		"Light":    ThemeLight,
		"":         ThemeAuto,
		"solarize": ThemeAuto,
	}
	for in, want := range cases {
		if got := NormalizeThemePreference(in); got != want {
			t.Errorf("NormalizeThemePreference(%q) = %q, want %q", in, got, want)
		}
	}
}

// A settings file written before Auto existed still reads exactly as written:
// an omitted preference becomes Auto, and explicit dark/light stay explicit.
func TestLoadPreservesThemePreference(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "unset", body: `{"model":"a/b"}`, want: ThemeAuto},
		{name: "explicit dark", body: `{"theme":"dark"}`, want: ThemeDark},
		{name: "explicit light", body: `{"theme":"light"}`, want: ThemeLight},
		{name: "invalid", body: `{"theme":"solarized"}`, want: ThemeAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".ohgo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			settings, err := Load("")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := settings.ThemePreference(); got != tc.want {
				t.Fatalf("theme = %q, want %q", got, tc.want)
			}
		})
	}
}

// Theme changes persist as the preference and do not disturb other settings.
func TestThemePreferenceRoundTripsWithoutTouchingSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := Default()
	settings.Model = "anthropic/claude-sonnet-4-5"
	settings.Theme = ThemeLight
	settings.MaxTokens = 4096

	if err := Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ThemePreference() != ThemeLight {
		t.Fatalf("theme = %q, want light", got.ThemePreference())
	}
	if got.Model != "anthropic/claude-sonnet-4-5" || got.MaxTokens != 4096 {
		t.Fatalf("unrelated settings changed: model %q max %d", got.Model, got.MaxTokens)
	}
}
