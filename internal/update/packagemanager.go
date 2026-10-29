package update

import (
	"os"
	"path/filepath"
	"strings"
)

// Manager describes an external package manager that owns the running binary.
//
// Managed is true only for ownership this package can prove from a receipt or
// an immutable store path. Resemblance alone (for example a binary in
// /usr/local/bin) is never enough: overwriting a system installation is a
// decision, and a guess is not authority.
type Manager struct {
	// Name is the human-facing package manager name. Empty when unmanaged.
	Name string
	// Command is the documented upgrade command, or empty when the correct
	// invocation cannot be named without guessing.
	Command string
	// Managed reports proven external ownership.
	Managed bool
	// Ambiguous reports a path that resembles a managed layout without a
	// receipt. It is diagnostic only; callers do not treat it as managed.
	Ambiguous bool
}

// DetectManager inspects an executable path for external ownership.
//
// The function reads the filesystem only. It never invokes the package
// manager, never mutates anything, and never assumes ownership from a path
// prefix alone.
func DetectManager(executable string) Manager {
	clean := filepath.Clean(executable)
	unix := filepath.ToSlash(clean)

	if manager, ok := detectHomebrew(clean, unix); ok {
		return manager
	} else if manager.Ambiguous {
		return manager
	}
	// A Nix store path is an immutable, content-addressed artifact; replacing
	// it in place is never the right operation, and the path itself is the
	// receipt.
	if strings.Contains(unix, "/nix/store/") {
		return Manager{Name: "Nix", Managed: true}
	}
	if strings.Contains(unix, "/.asdf/installs/ohgo/") {
		return Manager{Name: "asdf", Managed: true}
	}
	if strings.Contains(unix, "/.local/share/mise/installs/ohgo/") ||
		strings.Contains(unix, "/.mise/installs/ohgo/") {
		return Manager{Name: "mise", Managed: true}
	}
	if strings.Contains(unix, "/snap/ohgo/") {
		return Manager{Name: "snap", Managed: true}
	}
	return Manager{}
}

// detectHomebrew requires a Cellar receipt, not just a "/Cellar/" segment.
//
// Homebrew's layout is <prefix>/Cellar/<formula>/<version>/bin/<binary> with
// INSTALL_RECEIPT.json beside the binary's version directory. Both must exist
// for this to be a Homebrew installation rather than a directory someone named
// "Cellar".
func detectHomebrew(clean, unix string) (Manager, bool) {
	const marker = "/Cellar/"
	index := strings.LastIndex(unix, marker)
	if index < 0 {
		return Manager{}, false
	}
	prefix := clean[:index]
	rest := unix[index+len(marker):]
	parts := strings.Split(rest, "/")
	if len(parts) < 3 || parts[0] != "ohgo" {
		return Manager{}, false
	}
	versionDir := filepath.Join(prefix, "Cellar", parts[0], parts[1])
	receipt := filepath.Join(versionDir, "INSTALL_RECEIPT.json")
	if info, err := os.Stat(receipt); err != nil || info.IsDir() {
		return Manager{Ambiguous: true}, false
	}
	return Manager{Name: "Homebrew", Command: "brew upgrade ohgo", Managed: true}, true
}
