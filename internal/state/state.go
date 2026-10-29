// Package state is the single owner of OHGO's machine-level state location.
//
// Machine state lives under <home>/.ohgo and its lifecycle is independent of
// any project. Every package that persists machine-scoped data resolves its
// directory through here rather than joining "home" and ".ohgo" itself: five
// private copies of that join were five chances to drift from the documented
// layout, and a location decided in two places is a boundary that can silently
// split.
//
// The project-local boundary is the counterpart, not a member of this scope:
// <project>/.ohgo is owned by the project package. Nothing in this package may
// read or write it.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirName is the machine-level OHGO directory under the user's home.
const DirName = ".ohgo"

// ErrNoHome means the user's home directory could not be located, so no
// machine-level path can be resolved.
var ErrNoHome = errors.New("state: cannot locate home directory")

// Dir returns the machine-level OHGO state directory, ~/.ohgo.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoHome, err)
	}
	if home == "" {
		return "", ErrNoHome
	}
	return filepath.Join(home, DirName), nil
}

// Path joins parts onto the machine-level OHGO directory, e.g.
// Path("cache", "openrouter-models.json").
func Path(parts ...string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, parts...)...), nil
}
