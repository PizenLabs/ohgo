package state

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDirIsUnderHome(t *testing.T) {
	// Machine state must resolve under the user's home, not the working
	// directory: a project-relative path would make one machine look like
	// many and lose the shared identity/cache/session store.
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join(home, DirName); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}

func TestPathJoinsPartsUnderDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := Path("cache", "openrouter-models.json")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(home, DirName, "cache", "openrouter-models.json")
	if path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}
}

func TestDirReportsMissingHome(t *testing.T) {
	// An empty HOME must be a loud, matchable failure, never a path relative
	// to the process that silently writes state into the repository.
	t.Setenv("HOME", "")
	if _, err := Dir(); !errors.Is(err, ErrNoHome) {
		t.Fatalf("Dir with empty HOME = %v, want ErrNoHome", err)
	}
}
