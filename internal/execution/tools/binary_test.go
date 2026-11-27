package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// A GUI or IDE process often has a truncated PATH. Resolution must still find
// a binary in the standard install directories.
func TestLookPathFallsBackToStandardDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a POSIX executable")
	}
	dir := t.TempDir()
	name := "ohgo-test-binary"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the first lookup from succeeding through the test process PATH.
	t.Setenv("PATH", t.TempDir())

	got, err := lookPath(name, []string{dir})
	if err != nil {
		t.Fatalf("lookPath: %v", err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
	if _, err := lookPath(name, []string{t.TempDir()}); err == nil {
		t.Fatal("missing binary resolved through an unrelated directory")
	}
}

// The fallback directory table must cover the platform's package-manager and
// toolchain locations without inventing relative paths from unset env vars.
func TestStandardBinaryDirsCoverPlatforms(t *testing.T) {
	has := func(dirs []string, want string) bool {
		return slices.Contains(dirs, want)
	}

	darwin := standardBinaryDirsFor("darwin", "/Users/test", func(string) string { return "" })
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join("/Users/test", ".local/bin"), filepath.Join("/Users/test", ".cargo/bin")} {
		if !has(darwin, want) {
			t.Errorf("darwin dirs omit %q: %v", want, darwin)
		}
	}

	linux := standardBinaryDirsFor("linux", "/home/test", func(string) string { return "" })
	for _, want := range []string{"/usr/local/bin", "/snap/bin", filepath.Join("/home/test", "go/bin"), filepath.Join("/home/test", ".cargo/bin")} {
		if !has(linux, want) {
			t.Errorf("linux dirs omit %q: %v", want, linux)
		}
	}

	windows := standardBinaryDirsFor("windows", `C:\Users\test`, func(key string) string {
		switch key {
		case "LOCALAPPDATA":
			return `C:\Users\test\AppData\Local`
		case "APPDATA":
			return `C:\Users\test\AppData\Roaming`
		case "ProgramFiles":
			return `C:\Program Files`
		case "ProgramData":
			return `C:\ProgramData`
		default:
			return ""
		}
	})
	for _, want := range []string{
		filepath.Join(`C:\Users\test`, "scoop", "shims"),
		filepath.Join(`C:\Users\test`, "go", "bin"),
		filepath.Join(`C:\Users\test\AppData\Local`, "Microsoft", "WinGet", "Links"),
		filepath.Join(`C:\Program Files`, "LLVM", "bin"),
	} {
		if !has(windows, want) {
			t.Errorf("windows dirs omit %q: %v", want, windows)
		}
	}
	if has(windows, "npm") || has(windows, "Programs") {
		t.Errorf("unset environment produced relative Windows dirs: %v", windows)
	}
}
