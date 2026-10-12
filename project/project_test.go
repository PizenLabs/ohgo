package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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

func TestEnsureWithRecordsMetadata(t *testing.T) {
	root := t.TempDir()
	identity, err := EnsureWith(root, Metadata{Name: "payments-api", Worker: "Alice"})
	if err != nil {
		t.Fatalf("EnsureWith: %v", err)
	}
	if identity.ID == "" || identity.Root == "" {
		t.Fatalf("identity = %+v, want id and root", identity)
	}
	if identity.Name != "payments-api" || identity.WorkerName() != "Alice" {
		t.Errorf("metadata = %+v, want payments-api / Alice", identity)
	}
	if !identity.Complete() {
		t.Errorf("identity %+v reports incomplete after metadata was recorded", identity)
	}
	reloaded, found, err := Load(root)
	if err != nil || !found {
		t.Fatalf("Load = %+v, %v, %v", reloaded, found, err)
	}
	if reloaded.Name != "payments-api" || reloaded.WorkerName() != "Alice" {
		t.Errorf("reloaded = %+v, want the recorded metadata", reloaded)
	}
}

func TestEnsureWithoutMetadataOmitsWorker(t *testing.T) {
	// A fresh id-only identity must not carry an empty `user: {}`: the worker
	// is absent until the gate establishes it.
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user:") {
		t.Errorf("id-only identity carried a worker record: %s", data)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		if key != "id" && key != "root" {
			t.Errorf("id-only project.yml carries %q, want only id and root", key)
		}
	}
}

func TestEnsureWithPreservesIDAndCompletes(t *testing.T) {
	// Recovery of an id/root-only file adds the missing metadata without
	// changing the id: that is the migration contract.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte("id: fixed-id\nroot: "+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := EnsureWith(root, Metadata{Name: "renamed", Worker: "Bob"})
	if err != nil {
		t.Fatalf("EnsureWith: %v", err)
	}
	if identity.ID != "fixed-id" {
		t.Errorf("id = %q, want the preserved fixed-id", identity.ID)
	}
	if identity.Name != "renamed" || identity.WorkerName() != "Bob" {
		t.Errorf("metadata = %+v, want renamed / Bob", identity)
	}
}

func TestMetadataChangeKeepsID(t *testing.T) {
	// project.id is independent of project.name and project.user.name.
	root := t.TempDir()
	first, err := EnsureWith(root, Metadata{Name: "before", Worker: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureWith(root, Metadata{Name: "after", Worker: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("id changed with metadata: %q then %q", first.ID, second.ID)
	}
	if second.Name != "after" || second.WorkerName() != "Bob" {
		t.Errorf("metadata = %+v, want after / Bob", second)
	}
}

func TestCompleteRequiresNameAndWorker(t *testing.T) {
	base := Identity{ID: "x", Root: "/x"}
	if base.Complete() {
		t.Error("id/root-only identity reported complete")
	}
	named := base
	named.Name = "proj"
	if named.Complete() {
		t.Error("an identity without a worker reported complete")
	}
	worked := named
	worked.User.Name = "Alice"
	if !worked.Complete() {
		t.Error("a named identity with a worker reported incomplete")
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

func TestIdentitySurvivesProjectMove(t *testing.T) {
	// A repository that is renamed or moved carries its .ohgo directory with
	// it. The recorded root becomes stale, but identity and metadata must
	// survive: re-minting an id on every move would make one project look like
	// a new one each time it moves, orphaning the project association of its
	// session history.
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := EnsureWith(src, Metadata{Name: "payments-api", Worker: "Alice"})
	if err != nil {
		t.Fatalf("EnsureWith: %v", err)
	}

	dst := t.TempDir()
	if err := os.Mkdir(filepath.Join(dst, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(src, DirName), filepath.Join(dst, DirName)); err != nil {
		t.Fatalf("move .ohgo: %v", err)
	}

	moved, found, err := Load(dst)
	if err != nil {
		t.Fatalf("Load after move: %v", err)
	}
	if !found {
		t.Fatal("identity vanished when the project moved")
	}
	if moved.ID != first.ID {
		t.Errorf("id = %q after move, want the stable %q", moved.ID, first.ID)
	}
	if moved.Name != "payments-api" || moved.WorkerName() != "Alice" {
		t.Errorf("metadata = %+v after move, want payments-api / Alice", moved)
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

func TestDistinctProjectsHaveDistinctIDs(t *testing.T) {
	// Project identity is project-local: two checkouts are two projects even
	// when their source trees are identical, so neither may mint the other's
	// id. A shared id would merge their session history.
	first, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatalf("Ensure first: %v", err)
	}
	second, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatalf("Ensure second: %v", err)
	}
	if first.ID == second.ID {
		t.Errorf("two projects share id %q", first.ID)
	}
}

func TestProjectLocalMetadataShape(t *testing.T) {
	// The project-local boundary holds the project identity and the human
	// working with it — never the machine-global defaults. The persisted shape
	// is id, name, root, and user.name.
	root := t.TempDir()
	if _, err := EnsureWith(root, Metadata{Name: "payments-api", Worker: "Alice"}); err != nil {
		t.Fatalf("EnsureWith: %v", err)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		t.Fatalf("parse identity: %v", err)
	}
	for key := range fields {
		switch key {
		case "id", "name", "root", "user":
		default:
			t.Errorf("project.yml carries %q, want only id/name/root/user", key)
		}
	}
	user, ok := fields["user"].(map[string]any)
	if !ok {
		t.Fatalf("user = %#v, want a mapping", fields["user"])
	}
	for key := range user {
		if key != "name" {
			t.Errorf("user carries %q, want only name", key)
		}
	}
}
