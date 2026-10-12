package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/project"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/startup"
	"github.com/PizenLabs/ohgo/tui"
	"github.com/spf13/cobra"
)

// The wizard commits through the same seams as the stdin gate: declining
// Git writes project.yml with the confirmed worker and name, and publishes
// the identity the canvas renders.

// wizardTestOpts builds minimal options rooted at dir for the commit tests.
// The model never calls a provider here: completion only writes local state.
func wizardTestOpts(dir string) options {
	settings := config.Default()
	return options{
		root:     dir,
		settings: settings,
		model:    catalog.Default(),
	}
}

func TestWizardCompleteCommitsProjectWithoutGit(t *testing.T) {
	dir := t.TempDir()
	opts := wizardTestOpts(dir)
	registry := ai.NewRegistry()
	d := startup.Discover(dir)
	if d.HasGit {
		t.Skip("temp dir sits inside a repository")
	}
	program := tui.NewProgram(nil, "test", 0)
	held := newHarness(registry, nil, nil, nil, &agent.UsageLog{}, &opts.settings, dir, opts.model)
	complete := wizardComplete(&cobra.Command{}, &opts, registry, d, nil, held, program, 0)

	facts, notice, err := complete(tui.InitWizardDecision{WantGit: false, Worker: "Bob", Project: "demo"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if facts.User != "Bob" || facts.Ready != "ready" {
		t.Errorf("facts = %+v, want the committed worker and readiness", facts)
	}
	if !strings.Contains(notice, "Welcome, Bob") {
		t.Errorf("notice = %q, want the greeting the gate prints", notice)
	}
	got, found, err := project.Load(dir)
	if err != nil || !found {
		t.Fatalf("project.Load: %+v %v, want the committed identity", got, err)
	}
	if got.WorkerName() != "Bob" || got.Name != "demo" {
		t.Errorf("identity = %+v, want worker Bob and name demo", got)
	}
	if held.projectID != got.ID || opts.projectID != got.ID {
		t.Errorf("projectID = %q/%q, want both to follow %q", held.projectID, opts.projectID, got.ID)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Error("declining Git must not create a repository")
	}
}

func TestWizardCompleteCreatesTheSession(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	opts := wizardTestOpts(dir)
	registry := ai.NewRegistry()
	d := startup.Discover(dir)
	program := tui.NewProgram(nil, "test", 0)
	held := newHarness(registry, nil, store, nil, &agent.UsageLog{}, &opts.settings, dir, opts.model)
	complete := wizardComplete(&cobra.Command{}, &opts, registry, d, store, held, program, 0)

	if _, _, err := complete(tui.InitWizardDecision{Worker: "Bob", Project: "demo"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if held.sess == nil {
		t.Fatal("completion must create the session once the identity exists")
	}
	if _, err := store.Get(held.sess.ID); err != nil {
		t.Errorf("session %d not persisted: %v", held.sess.ID, err)
	}
	if held.projectID == "" || held.sess.Project != held.projectID {
		t.Errorf("session project = %q, want it scoped to %q", held.sess.Project, held.projectID)
	}
}

func TestWizardCompleteHonoursAGitYes(t *testing.T) {
	if !startup.GitAvailable() {
		t.Skip("git is unavailable")
	}
	dir := t.TempDir()
	opts := wizardTestOpts(dir)
	registry := ai.NewRegistry()
	d := startup.Discover(dir)
	if d.HasGit {
		t.Skip("temp dir sits inside a repository")
	}
	program := tui.NewProgram(nil, "test", 0)
	held := newHarness(registry, nil, nil, nil, &agent.UsageLog{}, &opts.settings, dir, opts.model)
	complete := wizardComplete(&cobra.Command{}, &opts, registry, d, nil, held, program, 0)

	if _, _, err := complete(tui.InitWizardDecision{WantGit: true, Worker: "Bob", Project: "demo"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("answering yes must create the repository: %v", err)
	}
}
