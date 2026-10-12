// Package project establishes a stable project identity for OHGO.
//
// A project owns its identity (§46): the runtime must distinguish two
// projects even when their source trees look alike, and that identity must
// survive a restart. This package anchors identity in a project-local
// `.ohgo/project.yml` (§17, I11) rather than in transient process state such
// as the current working directory (§18).
//
// The file is the project-local boundary for OHGO metadata. It deliberately
// does not duplicate the global session store: the identity is the only thing
// persisted here.
package project

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PizenLabs/ohgo/vcs"
	"go.yaml.in/yaml/v3"
)

// Directory and file names of the project-local OHGO boundary.
const (
	// DirName is the project-local OHGO directory.
	DirName = ".ohgo"
	// FileName is the project identity file inside DirName.
	FileName = "project.yml"
)

// Identity is a stable project identity.
type Identity struct {
	// ID is a generated identifier, unique per project and stable across
	// restarts.
	ID string `yaml:"id"`
	// Root is the project root the identity was anchored at.
	Root string `yaml:"root"`
}

// Valid reports whether the identity has a non-empty id and root.
func (i Identity) Valid() bool {
	return i.ID != "" && i.Root != ""
}

// ErrNotFound means no project identity file exists yet.
var ErrNotFound = errors.New("project: no identity")

// Root returns the project root for a working directory.
//
// Inside a repository the checkout root is the project: a nested directory and
// the repository it lives in are one project, not two. Outside a repository
// the directory itself is the root. The result is absolute, so identity does
// not depend on how the caller spelled the path.
func Root(dir string) string {
	if dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if repo, err := vcs.Discover(abs); err == nil && repo != nil {
		return repo.Root
	}
	return abs
}

// Path returns the identity file path for a project root.
func Path(root string) string {
	return filepath.Join(root, DirName, FileName)
}

// Load reads an existing identity for a working directory.
//
// It reports found=false with a nil error when no file exists, so a caller can
// distinguish "not initialised yet" from "unreadable". A malformed file is an
// error rather than a reason to regenerate: regenerating would change the
// project's identity, which is exactly the instability this package exists to
// prevent.
func Load(dir string) (Identity, bool, error) {
	root := Root(dir)
	path := Path(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Identity{}, false, nil
		}
		return Identity{}, false, fmt.Errorf("project: read %s: %w", path, err)
	}
	var identity Identity
	if err := yaml.Unmarshal(data, &identity); err != nil {
		return Identity{}, false, fmt.Errorf("project: parse %s: %w", path, err)
	}
	if !identity.Valid() {
		return Identity{}, false, fmt.Errorf("%w: %s is missing id or root", ErrNotFound, path)
	}
	if identity.Root == "" {
		identity.Root = root
	}
	return identity, true, nil
}

// Ensure returns the identity for a working directory, creating it when it
// does not exist.
//
// Creation is atomic (write-temp-then-rename): a crash mid-write must not
// leave a half-written identity file that a later run would refuse to read (or
// worse, treat as a new project).
func Ensure(dir string) (Identity, error) {
	root := Root(dir)
	identity, found, err := Load(root)
	if err != nil {
		return Identity{}, err
	}
	if found {
		return identity, nil
	}

	id, err := newID()
	if err != nil {
		return Identity{}, err
	}
	identity = Identity{ID: id, Root: root}
	if err := write(root, identity); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// write atomically creates the identity file under root.
func write(root string, identity Identity) error {
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("project: create %s: %w", dir, err)
	}
	data, err := yaml.Marshal(identity)
	if err != nil {
		return fmt.Errorf("project: encode identity: %w", err)
	}
	path := Path(root)
	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return fmt.Errorf("project: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("project: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("project: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("project: install %s: %w", path, err)
	}
	return nil
}

// newID mints a random project identifier.
func newID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("project: generate id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
