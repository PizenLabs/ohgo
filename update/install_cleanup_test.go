package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallerCleansOnlyStaleTemporaries(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ohgo")
	if err := os.WriteFile(dest, []byte("#!/bin/sh\necho ohgo v1.0.0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile destination: %v", err)
	}
	stale := filepath.Join(dir, ".ohgo-update-stale")
	fresh := filepath.Join(dir, ".ohgo-update-fresh")
	for _, path := range []string{stale, fresh} {
		if err := os.WriteFile(path, []byte("temp"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(stale, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	source := filepath.Join(t.TempDir(), "download")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho ohgo v2.0.0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	installer := &Installer{Now: func() time.Time { return now }}
	if err := installer.Install(context.Background(), InstallRequest{
		SourcePath:  source,
		Destination: dest,
		Version:     "v2.0.0",
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp was not removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp was removed: %v", err)
	}
}
