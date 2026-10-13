// Package project establishes a stable project identity for OHGO.
//
// A project owns its identity (§46): the runtime must distinguish two
// projects even when their source trees look alike, and that identity must
// survive a restart. This package anchors identity in a project-local
// `.ohgo/project.yml` (§17, I11) rather than in transient process state such
// as the current working directory (§18).
//
// The file is the project-local boundary for OHGO metadata. It carries the
// project's stable identity, its human-readable name, and the human working
// with *this* project. It deliberately does not duplicate the global session
// store or the machine-global settings: a worker name belongs to the
// project, so one machine can work as Alice in one project and Bob in another.
package project

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// Identity is a project's stable identity plus its project-local
// initialization metadata.
type Identity struct {
	// ID is a generated identifier, unique per project and stable across
	// restarts. It is the project's identity and never depends on the project
	// name, the worker name, the directory name, the absolute path, or the
	// Git remote.
	ID string `yaml:"id"`
	// Name is the human-readable project name the initialization gate
	// established. It is independent of ID: renaming the project must not
	// change its identity.
	Name string `yaml:"name,omitempty"`
	// Root is the project root the identity was anchored at. It is metadata
	// that tolerates going stale when the tree moves; it is not identity.
	Root string `yaml:"root"`
	// User is the project-local worker identity: the human working with this
	// project. It is project-local, never machine-global, so two projects on
	// one machine may name two different workers.
	User User `yaml:"user,omitempty"`
}

// User is the human working with a project.
type User struct {
	// Name is the worker's display name for this project. Empty means the
	// initialization gate has not established it yet.
	Name string `yaml:"name,omitempty"`
}

// IsZero reports whether the worker record carries no name, so yaml omitempty
// drops an unestablished worker instead of writing an empty `user: {}`.
func (u User) IsZero() bool { return strings.TrimSpace(u.Name) == "" }

// Metadata is the project-local initialization metadata the startup gate
// collects from a human. Identity (ID, Root) is minted or preserved by the
// project package; metadata is the part only a human decides.
type Metadata struct {
	// Name is the human-readable project name.
	Name string
	// Worker is the human working with this project.
	Worker string
}

// Valid reports whether the identity has a non-empty id and root.
func (i Identity) Valid() bool {
	return i.ID != "" && i.Root != ""
}

// Complete reports whether the identity carries the project-local
// initialization metadata this build requires: a project name and a worker
// name.
//
// A file written by an earlier build has only id and root. It stays Valid —
// its id must never change — but it is incomplete, and the initialization
// gate owns completing it.
func (i Identity) Complete() bool {
	return i.Valid() && strings.TrimSpace(i.Name) != "" && !i.User.IsZero()
}

// WorkerName returns the project-local worker display name, trimmed.
func (i Identity) WorkerName() string {
	return strings.TrimSpace(i.User.Name)
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
//
// An id/root-only file is found and Valid; whether it is Complete is a
// separate question the initialization gate answers.
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
	return identity, true, nil
}

// Ensure returns the identity for a working directory, creating it when it
// does not exist.
//
// It preserves an existing id and any existing project-local metadata. It does
// not establish a project or worker name: only the initialization gate may do
// that, through EnsureWith.
func Ensure(dir string) (Identity, error) {
	return EnsureWith(dir, Metadata{})
}

// EnsureWith returns the identity for a working directory, creating it when it
// does not exist and preserving an existing id.
//
// Non-empty metadata fields supplied by the initialization gate are written
// into the file; empty fields leave the existing value untouched. It is the
// commit step of the initialization gate: recovering an id/root-only file adds
// the missing metadata without changing the id, and an unchanged identity is
// returned without a rewrite.
//
// Creation is atomic (write-temp-then-rename): a crash mid-write must not
// leave a half-written identity file that a later run would refuse to read (or
// worse, treat as a new project).
func EnsureWith(dir string, meta Metadata) (Identity, error) {
	root := Root(dir)
	identity, found, err := Load(root)
	if err != nil {
		return Identity{}, err
	}
	if !found {
		id, err := newID()
		if err != nil {
			return Identity{}, err
		}
		identity = Identity{ID: id, Root: root}
	}

	original := identity
	if name := strings.TrimSpace(meta.Name); name != "" {
		identity.Name = name
	}
	if worker := strings.TrimSpace(meta.Worker); worker != "" {
		identity.User.Name = worker
	}
	if found && identity == original {
		return identity, nil
	}
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
