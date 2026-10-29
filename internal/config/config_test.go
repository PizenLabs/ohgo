package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelIDSplitsProviderAndID(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		provider string
		id       string
		ok       bool
	}{
		{"well formed", "anthropic/claude-sonnet-4-5", "anthropic", "claude-sonnet-4-5", true},
		{"empty", "", "", "", false},
		{"no separator", "claude-sonnet-4-5", "", "", false},
		{"trailing slash", "anthropic/", "", "", false},
		{"leading slash", "/claude", "", "", false},
		{"multiple slashes", "a/b/c", "a", "b/c", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, id, ok := Settings{Model: tc.value}.ModelID()
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if provider != tc.provider || id != tc.id {
				t.Errorf("got %q/%q, want %q/%q", provider, id, tc.provider, tc.id)
			}
		})
	}
}

func TestApprovalForPrefersAutoApproveOverDeny(t *testing.T) {
	// A user who lists a tool in AutoApprove has overridden an explicit deny;
	// silently prompting again would make the override useless.
	settings := Settings{
		Approvals:   map[string]string{"bash": "deny"},
		AutoApprove: []string{"bash"},
	}
	policy, found := settings.ApprovalFor("bash")
	if !found {
		t.Fatal("bash was not found")
	}
	if policy != "allow" {
		t.Errorf("policy = %q, want allow", policy)
	}
}

func TestApprovalForIsCaseInsensitive(t *testing.T) {
	settings := Settings{AutoApprove: []string{"  Bash  "}}
	policy, found := settings.ApprovalFor("bash")
	if !found || policy != "allow" {
		t.Errorf("got (%q, %v), want (\"allow\", true)", policy, found)
	}
}

func TestApprovalForReportsUnknownTool(t *testing.T) {
	if _, found := (Settings{}).ApprovalFor("read"); found {
		t.Error("expected an unknown tool to report no policy")
	}
}

func TestLoadReturnsErrNotFoundForMissingFile(t *testing.T) {
	// Load reads the real home directory, so the test must point HOME at an
	// empty directory or a developer's own ~/.ohgo/settings.json decides the
	// outcome.
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings, err := Load(t.TempDir())
	if err != ErrNotFound {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	// Defaults must still be usable when no file exists.
	if settings.MaxTokens <= 0 {
		t.Error("default max tokens were not populated")
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ohgo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ohgo", "settings.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err == nil {
		t.Fatal("expected malformed JSON to fail")
	}
}

func TestSaveRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := Settings{
		Model:           "openai/gpt-5",
		MaxTokens:       4096,
		Reasoning:       true,
		ReasoningEffort: "high",
		Approvals:       map[string]string{"bash": "prompt"},
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != want.Model {
		t.Errorf("model = %q, want %q", got.Model, want.Model)
	}
	if got.MaxTokens != want.MaxTokens {
		t.Errorf("maxTokens = %d, want %d", got.MaxTokens, want.MaxTokens)
	}
	if !got.Reasoning {
		t.Error("reasoning was lost")
	}
	if got.Approvals["bash"] != "prompt" {
		t.Errorf("approvals = %v, want bash:prompt", got.Approvals)
	}
}

func TestUserDefaultsRoundTripsUnderLegacyKey(t *testing.T) {
	// The machine-wide default name is global presentation metadata: it must
	// survive a save/load cycle without disturbing the rest of the document.
	// The on-disk key stays "identity" so files written by an earlier build
	// remain readable.
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := Default()
	want.Model = "openai/gpt-5"
	want.UserDefaults.Name = "Jack"
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.GlobalName() != "Jack" {
		t.Errorf("name = %q, want Jack", got.GlobalName())
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("model = %q, want it preserved alongside the default name", got.Model)
	}

	// A settings file from an earlier build uses the same key.
	raw, err := os.ReadFile(filepath.Join(home, ".ohgo", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"identity"`) {
		t.Errorf("saved settings lost the legacy key: %s", raw)
	}
}

func TestGlobalNameTrimsAndDefaultsEmpty(t *testing.T) {
	if got := (Settings{}).GlobalName(); got != "" {
		t.Errorf("empty settings: got %q, want empty", got)
	}
	s := Settings{}
	s.UserDefaults.Name = "  Alex  "
	if got := s.GlobalName(); got != "Alex" {
		t.Errorf("got %q, want Alex", got)
	}
}
