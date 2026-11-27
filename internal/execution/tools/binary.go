package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LookPath resolves an external binary the way exec.LookPath does, then
// searches the standard user and system install directories.
//
// A GUI or IDE process on macOS does not inherit the interactive shell's
// PATH, so a Homebrew or cargo-installed accelerator can be present and still
// invisible. The fallback directories make that launch mode behave like a
// terminal. It is a resolution convenience, never a privilege: callers still
// choose whether a missing binary is a fallback or a failure.
func LookPath(name string) (string, error) {
	return lookPath(name, standardBinaryDirs())
}

// lookPath is LookPath with the fallback directories injected for tests.
func lookPath(name string, dirs []string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range dirs {
		path, err := exec.LookPath(filepath.Join(dir, name))
		if err == nil {
			return path, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// standardBinaryDirs lists the well-known directories package managers and
// language toolchains install into. Order is preference, and duplicates are
// dropped so a large PATH cannot turn one lookup into repeated work.
func standardBinaryDirs() []string {
	home, _ := os.UserHomeDir()
	return standardBinaryDirsFor(runtime.GOOS, home, os.Getenv)
}

// standardBinaryDirsFor is the platform table with the environment injected,
// so tests can pin every OS without depending on the host they run on.
func standardBinaryDirsFor(goos, home string, getenv func(string) string) []string {
	var dirs []string
	seen := make(map[string]struct{})
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	addHome := func(rel string) {
		if home != "" {
			add(filepath.Join(home, rel))
		}
	}
	addEnv := func(base string, rel ...string) {
		if strings.TrimSpace(base) == "" {
			return
		}
		add(filepath.Join(append([]string{base}, rel...)...))
	}

	switch goos {
	case "windows":
		local := getenv("LOCALAPPDATA")
		appData := getenv("APPDATA")
		programFiles := getenv("ProgramFiles")
		programData := getenv("ProgramData")
		addHome("scoop/shims")
		addHome("go/bin")
		addHome(".cargo/bin")
		addEnv(appData, "npm")
		addEnv(local, "Microsoft", "WinGet", "Links")
		addEnv(local, "Programs")
		addEnv(programFiles, "LLVM", "bin")
		addEnv(programFiles, "Git", "cmd")
		addEnv(programData, "chocolatey", "bin")
	case "darwin":
		add("/opt/homebrew/bin")
		add("/opt/local/bin")
		add("/usr/local/bin")
		add("/usr/bin")
		add("/bin")
		addHome(".local/bin")
		addHome("bin")
		addHome("go/bin")
		addHome(".cargo/bin")
	default:
		add("/usr/local/bin")
		add("/usr/bin")
		add("/bin")
		add("/snap/bin")
		add("/nix/var/nix/profiles/default/bin")
		addHome(".local/bin")
		addHome("bin")
		addHome("go/bin")
		addHome(".cargo/bin")
		addHome(".npm-global/bin")
		addHome(".nix-profile/bin")
	}
	return dirs
}
