package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/creds"
)

// The registry now resolves through the credential chain: explicit, file,
// environment, shell files. Each test below pins one link.

func TestRegistryPrefersFileOverEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	store, err := creds.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("groq", "file-key"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROQ_API_KEY", "env-key")
	t.Setenv("HOME", dir)

	registry := NewRegistry()
	registry.SetCredentialsFile(path)

	key, source, err := registry.ResolveAPIKeySource("groq")
	if err != nil || key != "file-key" || source != "credential file" {
		t.Errorf("got (%q, %q, %v), want the file before the environment", key, source, err)
	}
}

func TestRegistryReadsShellFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROQ_API_KEY", "")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export GROQ_API_KEY=shell-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	registry := NewRegistry()
	registry.SetCredentialsFile(filepath.Join(home, "no-creds.json"))

	key, source, err := registry.ResolveAPIKeySource("groq")
	if err != nil || key != "shell-key" {
		t.Errorf("got (%q, %q, %v), want the shell discovery to find the key", key, source, err)
	}
	if source == "" || source == "explicit" {
		t.Errorf("source = %q, want the shell file named", source)
	}
}

func TestRegistryKeepsTheMissingKeyMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("HOME", home)

	registry := NewRegistry()
	registry.SetCredentialsFile(filepath.Join(home, "no-creds.json"))

	_, _, err := registry.ResolveAPIKeySource("groq")
	if err == nil {
		t.Fatal("a missing key resolved")
	}
	if got := err.Error(); !strings.Contains(got, "GROQ_API_KEY") {
		t.Errorf("err = %q, want it to name the variable", got)
	}
}

// TestOpenCodeKeySavedForOneGatewayResolvesForTheOther is the credential
// contract for one Console account with two endpoints. A user who connected
// Zen must not be told the key is missing after switching to Go, because the
// same key authenticates both.
func TestOpenCodeKeySavedForOneGatewayResolvesForTheOther(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	store, err := creds.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("opencode-zen", "oc_sk_shared"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("HOME", dir)

	registry := NewRegistry()
	registry.SetCredentialsFile(path)
	for _, provider := range []string{"opencode-zen", "opencode-go"} {
		key, source, err := registry.ResolveAPIKeySource(provider)
		if err != nil || key != "oc_sk_shared" || source != "credential file" {
			t.Errorf("%s resolved (%q, %q, %v), want the shared key from the file", provider, key, source, err)
		}
	}
}
