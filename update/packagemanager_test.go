package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectManagerRequiresAHomebrewReceipt(t *testing.T) {
	prefix := t.TempDir()
	versionDir := filepath.Join(prefix, "Cellar", "ohgo", "1.2.3")
	if err := os.MkdirAll(filepath.Join(versionDir, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	exe := filepath.Join(versionDir, "bin", "ohgo")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager := DetectManager(exe)
	if manager.Managed {
		t.Fatal("a Cellar path without a receipt must not be treated as managed")
	}
	if !manager.Ambiguous {
		t.Fatal("a Cellar-shaped path without a receipt should be reported ambiguous")
	}

	if err := os.WriteFile(filepath.Join(versionDir, "INSTALL_RECEIPT.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile receipt: %v", err)
	}
	manager = DetectManager(exe)
	if !manager.Managed || manager.Name != "Homebrew" {
		t.Fatalf("manager = %+v, want managed Homebrew", manager)
	}
	if manager.Command != "brew upgrade ohgo" {
		t.Fatalf("command = %q", manager.Command)
	}
}

func TestDetectManagerDoesNotGuessFromOrdinaryPaths(t *testing.T) {
	for _, path := range []string{
		"/usr/local/bin/ohgo",
		"/opt/homebrew/bin/ohgo",
		"/home/user/bin/ohgo",
		"/tmp/ohgo",
	} {
		if manager := DetectManager(path); manager.Managed {
			t.Fatalf("DetectManager(%q) = %+v, want unmanaged", path, manager)
		}
	}
}

func TestDetectManagerRecognizesImmutableStores(t *testing.T) {
	cases := map[string]string{
		"/nix/store/0123456789abcdef-ohgo-1.0.0/bin/ohgo":        "Nix",
		"/home/u/.asdf/installs/ohgo/1.0.0/bin/ohgo":             "asdf",
		"/home/u/.local/share/mise/installs/ohgo/1.0.0/bin/ohgo": "mise",
		"/snap/ohgo/12/bin/ohgo":                                 "snap",
	}
	for path, want := range cases {
		manager := DetectManager(path)
		if !manager.Managed || manager.Name != want {
			t.Fatalf("DetectManager(%q) = %+v, want managed %s", path, manager, want)
		}
		if manager.Command != "" {
			t.Fatalf("DetectManager(%q) fabricated a command %q", path, manager.Command)
		}
	}
}
