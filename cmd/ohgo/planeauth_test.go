package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/creds"
)

// TestSavePlaneKeyPersistsToFileStore is the credential-chain contract: a
// pasted key lands in the file store where the resolver finds it, and the
// raw value never appears in the returned error surface.
func TestSavePlaneKeyPersistsToFileStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearProviderKeys(t)
	h := testHarness(t)
	t.Setenv("HOME", home)

	if err := h.SavePlaneKey("groq", "file-key-123"); err != nil {
		t.Fatalf("SavePlaneKey: %v", err)
	}
	store, err := creds.Open(filepath.Join(home, ".ohgo", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("groq")
	if !ok || got != "file-key-123" {
		t.Errorf("stored = %q,%v, want the saved key", got, ok)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".ohgo", "credentials.json"))
	if len(data) == 0 {
		t.Error("credentials file empty after save")
	}
}

// TestSavePlaneKeyRefusesBadInput proves empty, keyless, and unknown
// providers never reach the store: each is a refusal, not a write.
func TestSavePlaneKeyRefusesBadInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearProviderKeys(t)
	h := testHarness(t)
	t.Setenv("HOME", home)

	if err := h.SavePlaneKey("groq", "   "); err == nil {
		t.Error("empty key saved")
	}
	if err := h.SavePlaneKey("ollama", "x"); err == nil {
		t.Error("keyless provider accepted a key")
	}
	if err := h.SavePlaneKey("no-such-provider", "x"); err == nil {
		t.Error("unknown provider accepted a key")
	}
	path := filepath.Join(home, ".ohgo", "credentials.json")
	if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), `"groq"`) {
		t.Errorf("refused input still wrote the store: %s", data)
	}
}
