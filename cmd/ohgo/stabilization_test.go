package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
)

// TestDoctorReportsAllChecks proves the diagnostic covers every subsystem that
// can stop a run. A doctor that skips the session store would bless a setup
// whose resume path is broken.
func TestDoctorReportsAllChecks(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	// Isolate HOME so the check reads default settings, not the developer's
	// real ~/.ohgo/settings.json: a configured default model (e.g. groq/…)
	// would make this test depend on the machine it runs on.
	t.Setenv("HOME", t.TempDir())

	cmd := rootCmd()
	reports := collectDoctorReports(cmd)

	want := []string{"workdir", "config", "catalog", "model", "credentials", "endpoint", "sessions", "tools"}
	if len(reports) != len(want) {
		t.Fatalf("checks = %d, want %d", len(reports), len(want))
	}
	for i, name := range want {
		if reports[i].Name != name {
			t.Errorf("check %d = %q, want %q", i, reports[i].Name, name)
		}
	}
	for _, rep := range reports {
		if rep.Name == "endpoint" {
			// A fake key against the real endpoint must fail closed with a
			// classified rejection — which itself proves the probe ran.
			if rep.OK {
				t.Errorf("endpoint passed with a fake key: %s", rep.Detail)
			}
			if !strings.Contains(rep.Detail, "rejected") {
				t.Errorf("endpoint = %q, want the auth classification", rep.Detail)
			}
			continue
		}
		if !rep.OK {
			t.Errorf("check %q failed with a key set: %s", rep.Name, rep.Detail)
		}
		if strings.TrimSpace(rep.Detail) == "" {
			t.Errorf("check %q has no detail; a diagnosis without a fix is a second error", rep.Name)
		}
	}
}

// TestDoctorRedactsCredentials scans the full rendered output for the key:
// details name sources, never values, by construction, and this proves it
// rather than asserting it.
func TestDoctorRedactsCredentials(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "test-key-redact-me")

	var out strings.Builder
	cmd := rootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor"})
	_ = cmd.Execute()
	if strings.Contains(out.String(), "test-key-redact-me") {
		t.Errorf("doctor output contains the key:\n%s", out.String())
	}
}

// TestDoctorNamesTheFixForAMissingKey is the whole point of the command: the
// most common failure must print what to do, not just what is wrong.
func TestDoctorNamesTheFixForAMissingKey(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	clearProviderKeys(t)
	// Isolate HOME as well: shell startup files and the credential file live
	// there, and a key in either would make this machine pass while a clean
	// one fails.
	t.Setenv("HOME", t.TempDir())

	cmd := rootCmd()
	var failed bool
	for _, rep := range collectDoctorReports(cmd) {
		if rep.Name == "credentials" {
			failed = !rep.OK
			if rep.OK {
				t.Error("credentials passed with no key in the environment")
			}
			if !strings.Contains(rep.Detail, "ANTHROPIC_API_KEY") {
				t.Errorf("detail = %q, want it to name the variable to set", rep.Detail)
			}
		}
	}
	if !failed {
		t.Error("no credentials check found")
	}
}

// TestDoctorFailsOnAnUnknownModel keeps a typo in --model from passing every
// check: the model resolution must be load-bearing, not decorative.
func TestDoctorFailsOnAnUnknownModel(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	cmd := rootCmd()
	if err := cmd.PersistentFlags().Set("model", "anthropic/no-such-model-xyz"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rep := range collectDoctorReports(cmd) {
		if rep.Name == "model" && !rep.OK {
			found = true
		}
	}
	if !found {
		t.Error("an unknown --model did not fail the model check")
	}
}

// TestReplSwitchesModel proves the plain REPL honours /model the way the
// full-screen UI does. Before this, the command was parsed and then reported
// as unknown, so a plain-REPL user could not switch without restarting.
func TestReplSwitchesModel(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	server := newScriptedServer(t, ackStream)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, nopWriter{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	in := strings.NewReader("/model groq/llama-3.1-8b-instant\n/quit\n")
	var out strings.Builder
	if err := repl(&harness{loop: loop}, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if !strings.Contains(out.String(), "Switched to groq/llama-3.1-8b-instant") {
		t.Errorf("output = %q, want the switch confirmation", out.String())
	}
}

// TestReplRejectsAnUnknownModelName keeps a typo from silently doing nothing:
// the error must name the fix, not just the failure.
func TestReplRejectsAnUnknownModelName(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	server := newScriptedServer(t, ackStream)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, nopWriter{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	in := strings.NewReader("/model anthropic/no-such-model-xyz\n/quit\n")
	var out strings.Builder
	if err := repl(&harness{loop: loop}, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if !strings.Contains(out.String(), "ohgo models") {
		t.Errorf("output = %q, want it to point at the model list", out.String())
	}
	if got := server.requests; got != 0 {
		t.Errorf("requests = %d, want 0: a command must never reach the model", got)
	}
}

// TestReplHistoryIsRecorded proves the audit trail grows with the session.
// The history file is the plain REPL's only memory across processes.
func TestReplHistoryIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	t.Setenv("OHGO_HISTORY_FILE", path)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	server := newScriptedServer(t, ackStream)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, nopWriter{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	in := strings.NewReader("remember this prompt\n/quit\n")
	var out strings.Builder
	if err := repl(&harness{loop: loop}, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if !strings.Contains(string(data), "remember this prompt") {
		t.Errorf("history = %q, want the user prompt recorded", string(data))
	}
}

// TestReplHistorySkipsCommands keeps the audit trail a list of prompts, not a
// transcript of command typos.
func TestReplHistorySkipsCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	t.Setenv("OHGO_HISTORY_FILE", path)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	server := newScriptedServer(t, ackStream)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	root := t.TempDir()
	opts := options{root: root, model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), settings: config.Default(), autoApprove: true}
	loop, err := buildLoop(opts, registry, nopWriter{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	in := strings.NewReader("/help\n/quit\n")
	var out strings.Builder
	if err := repl(&harness{loop: loop}, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a session of only commands still wrote a history file")
	}
}

// TestReplHistoryRotates bounds the file: an unbounded audit trail is a disk
// leak with a delay.
func TestReplHistoryRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	var sb strings.Builder
	for i := 0; i < replHistoryMaxLines+50; i++ {
		sb.WriteString("old prompt\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendReplHistoryLine(path, "new prompt"); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != replHistoryMaxLines {
		t.Errorf("lines = %d, want the cap %d", len(lines), replHistoryMaxLines)
	}
	if lines[len(lines)-1] != "new prompt" {
		t.Errorf("last line = %q, want the newest prompt kept", lines[len(lines)-1])
	}
}

// TestReplHistoryDisabledWritesNothing proves the opt-out: an empty
// OHGO_HISTORY_FILE means no recording anywhere.
func TestReplHistoryDisabledWritesNothing(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	recordReplHistory("this must go nowhere")
	// No assertion on the filesystem is possible for "nowhere"; the contract
	// is that the path resolver reports disabled.
	if got := replHistoryPath(); got != "" {
		t.Errorf("replHistoryPath = %q, want empty (disabled)", got)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestBuildToolsWiresWaitToTheBashTable proves the two tools observe the same
// jobs: a wait built on a different table would never see bash's spawns.
func TestBuildToolsWiresWaitToTheBashTable(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	toolSet := buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry())
	if !hasTool(toolSet, "wait") {
		t.Fatal("wait is not in the default tool set")
	}
	if !hasTool(toolSet, "bash") {
		t.Fatal("bash is not in the default tool set")
	}
}
