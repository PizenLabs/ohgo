package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/config"
)

func fakeGryBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gry")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runGryCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"gry"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestGryEnableStatusDisablePreserveOtherSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OHGO_GRY_HOOK", "")
	t.Setenv("OHGO_GRY_BIN", "")
	t.Setenv("OHGO_GRY_PROJECT", "")
	t.Setenv("OHGO_GRY_DEBUG", "")

	settings := config.Default()
	settings.UserDefaults.Name = "dev"
	settings.Model = "groq/openai/gpt-oss-20b"
	if err := os.MkdirAll(filepath.Join(home, ".ohgo"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"identity":{"name":"dev"},"model":"groq/openai/gpt-oss-20b","futureKey":{"keep":true}}`)
	if err := os.WriteFile(filepath.Join(home, ".ohgo", "settings.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	binary := fakeGryBinary(t)
	out, err := runGryCommand(t, "enable", "--project", "repo-e2e", "--binary", binary, "--timeout-ms", "2000")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	loaded, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Gry == nil || !loaded.Gry.Enabled || loaded.Gry.Project != "repo-e2e" {
		t.Fatalf("settings = %+v", loaded.Gry)
	}
	if loaded.Model != settings.Model || loaded.UserDefaults.Name != "dev" {
		t.Fatal("enabling GRY hooks must preserve unrelated settings")
	}
	after, err := os.ReadFile(filepath.Join(home, ".ohgo", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(after, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["futureKey"]; !ok {
		t.Fatal("an unknown settings key was dropped")
	}

	out, err = runGryCommand(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, out)
	}
	if report["enabled"] != true || report["project"] != "repo-e2e" || report["binary_found"] != true {
		t.Fatalf("status = %v", report)
	}
	if report["tui_wired"] != false {
		t.Fatal("status must not claim TUI wiring")
	}

	t.Setenv("OHGO_GRY_HOOK", "0")
	out, err = runGryCommand(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	report = nil
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report["enabled"] != false {
		t.Fatalf("OHGO_GRY_HOOK=0 must disable status: %v", report)
	}

	out, err = runGryCommand(t, "disable")
	if err != nil {
		t.Fatalf("disable: %v\n%s", err, out)
	}
	loaded, err = config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Gry != nil {
		t.Fatalf("disable left gry settings: %+v", loaded.Gry)
	}
	if loaded.Model != settings.Model || loaded.UserDefaults.Name != "dev" {
		t.Fatal("disabling GRY hooks must preserve unrelated settings")
	}
}

func TestGryEnableRejectsInvalidInputWithoutWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OHGO_GRY_HOOK", "")
	t.Setenv("OHGO_GRY_BIN", "")
	t.Setenv("OHGO_GRY_PROJECT", "")

	binary := fakeGryBinary(t)
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing project", []string{"enable", "--binary", binary}},
		{"invalid project", []string{"enable", "--project", "../repo", "--binary", binary}},
		{"bad timeout", []string{"enable", "--project", "repo", "--binary", binary, "--timeout-ms", "1"}},
		{"bad budget", []string{"enable", "--project", "repo", "--binary", binary, "--max-tokens", "1"}},
		{"missing binary", []string{"enable", "--project", "repo", "--binary", filepath.Join(home, "missing")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runGryCommand(t, test.args...); err == nil {
				t.Fatal("expected an error")
			}
			if _, err := os.Stat(settingsPath()); !os.IsNotExist(err) {
				t.Fatal("invalid input must not write settings")
			}
		})
	}
}

func TestGryStatusDoesNotStartAProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OHGO_GRY_HOOK", "1")
	t.Setenv("OHGO_GRY_BIN", fakeGryBinary(t))
	t.Setenv("OHGO_GRY_PROJECT", "repo")
	out, err := runGryCommand(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\"enabled\": true") {
		t.Fatalf("status = %s", out)
	}
}
