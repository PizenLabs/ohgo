package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstallerReplacesAtomicallyAndRemovesBackup(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ohgo")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	source := filepath.Join(dir, ".download")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	installer := &Installer{VersionOf: func(context.Context, string) (string, error) { return "v2.0.0", nil }}
	if err := installer.Install(context.Background(), InstallRequest{
		SourcePath:  source,
		Destination: dest,
		Version:     "v2.0.0",
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "new" {
		t.Fatalf("installed body = %q", body)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %o, want 755", info.Mode().Perm())
	}
	if _, err := os.Stat(backupPath(dest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists after successful install: %v", err)
	}
}

func TestInstallerKeepsPreviousBinaryDuringVerification(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ohgo")
	if err := os.WriteFile(dest, []byte("known-good"), 0o755); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	source := filepath.Join(dir, ".download")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	var preserved string
	installer := &Installer{VersionOf: func(_ context.Context, path string) (string, error) {
		body, err := os.ReadFile(backupPath(path))
		if err != nil {
			return "", err
		}
		preserved = string(body)
		return "v2.0.0", nil
	}}
	if err := installer.Install(context.Background(), InstallRequest{
		SourcePath:  source,
		Destination: dest,
		Version:     "v2.0.0",
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if preserved != "known-good" {
		t.Fatalf("backup during verification = %q, want the previous binary", preserved)
	}
}

func TestInstallerRestoresPreviousBinaryWhenVerificationFails(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ohgo")
	if err := os.WriteFile(dest, []byte("known-good"), 0o755); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	source := filepath.Join(dir, ".download")
	if err := os.WriteFile(source, []byte("broken-new"), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	installer := &Installer{VersionOf: func(context.Context, string) (string, error) { return "v1.0.0", nil }}
	err := installer.Install(context.Background(), InstallRequest{
		SourcePath:  source,
		Destination: dest,
		Version:     "v2.0.0",
	})
	if !errors.Is(err, ErrInstallVerification) {
		t.Fatalf("Install error = %v, want ErrInstallVerification", err)
	}
	body, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("ReadFile after rollback: %v", readErr)
	}
	if string(body) != "known-good" {
		t.Fatalf("destination after rollback = %q", body)
	}
	if _, statErr := os.Stat(backupPath(dest)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup not consumed by restore: %v", statErr)
	}
}

func TestInstallerVerifiesByExecutingTheVerifiedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is Unix-only")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "ohgo")
	if err := os.WriteFile(dest, []byte("#!/bin/sh\necho ohgo v1.0.0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	source := filepath.Join(dir, ".download")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho ohgo v2.0.0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	if err := (&Installer{}).Install(context.Background(), InstallRequest{
		SourcePath:    source,
		Destination:   dest,
		Version:       "v2.0.0",
		VerifyTimeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := InstalledVersion(context.Background(), dest)
	if err != nil {
		t.Fatalf("InstalledVersion: %v", err)
	}
	if got != "v2.0.0" {
		t.Fatalf("version = %q", got)
	}
}

func TestInstallerReportsUnwritableDirectoryWithoutTouchingDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "ohgo")
	if err := os.WriteFile(dest, []byte("known-good"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, "probe")
	if file, err := os.Create(probe); err == nil {
		_ = file.Close()
		_ = os.Remove(probe)
		t.Skip("directory is writable despite 0500 (running as a privileged user)")
	}
	source, err := os.CreateTemp(t.TempDir(), "download")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = source.WriteString("new")
	_ = source.Close()
	err = (&Installer{VersionOf: func(context.Context, string) (string, error) { return "v2.0.0", nil }}).Install(context.Background(), InstallRequest{
		SourcePath:  source.Name(),
		Destination: dest,
		Version:     "v2.0.0",
	})
	if err == nil {
		t.Fatal("unwritable directory must fail")
	}
	if !strings.Contains(err.Error(), "preserve previous binary") {
		t.Fatalf("error = %v, want a preservation failure", err)
	}
	body, readErr := os.ReadFile(dest)
	if readErr != nil || string(body) != "known-good" {
		t.Fatalf("destination changed: %q, %v", body, readErr)
	}
}

func TestInstalledVersionParsesOnlyTheOhgoLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is Unix-only")
	}
	path := filepath.Join(t.TempDir(), "ohgo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho ohgo v3.2.1\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := InstalledVersion(context.Background(), path)
	if err != nil {
		t.Fatalf("InstalledVersion: %v", err)
	}
	if got != "v3.2.1" {
		t.Fatalf("version = %q", got)
	}
}
