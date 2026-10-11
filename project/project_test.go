package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesStableIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := Ensure(root)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !first.Valid() {
		t.Fatalf("identity = %+v, want a valid id and root", first)
	}
	if _, err := os.Stat(Path(root)); err != nil {
		t.Fatalf("identity file was not written: %v", err)
	}

	// Identity must survive a restart: a second Ensure returns the same id
	// rather than minting a new one (§18).
	second, err := Ensure(root)
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("id changed across Ensure: %q then %q", first.ID, second.ID)
	}
}

func TestRootUsesRepositoryRootForNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// A nested directory and the repository are one project, not two.
	if got := Root(nested); got != root {
		t.Errorf("Root(nested) = %q, want the repository root %q", got, root)
	}
	// The identity is anchored at the root, so both spellings see one file.
	if _, err := Ensure(nested); err != nil {
		t.Fatalf("Ensure(nested): %v", err)
	}
	if _, err := os.Stat(Path(root)); err != nil {
		t.Errorf("identity was not anchored at the repository root: %v", err)
	}
}

func TestLoadReportsMissingIdentity(t *testing.T) {
	root := t.TempDir()
	identity, found, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if found {
		t.Errorf("found = true for %+v, want false", identity)
	}
}

func TestLoadRejectsMalformedIdentityRatherThanRegenerating(t *testing.T) {
	// Regenerating a broken identity would change the project's id, which is
	// exactly the instability this package prevents.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte("{not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Fatal("a malformed identity file was accepted")
	}
	if _, err := Ensure(root); err == nil {
		t.Fatal("Ensure silently regenerated a malformed identity")
	}
}

func TestEnsurePreservesAPreExistingIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte("id: fixed-id\nroot: "+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := Ensure(root)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if identity.ID != "fixed-id" {
		t.Errorf("id = %q, want the pre-existing fixed-id", identity.ID)
	}
}
