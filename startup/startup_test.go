package startup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/project"
	"github.com/PizenLabs/ohgo/vcs"
)

// Discovery pins the fast-path contract: an existing .ohgo/ with a complete
// project.yml skips initialization entirely.

func TestNeedsInitSkipsWhenInitialized(t *testing.T) {
	root := t.TempDir()
	if _, err := project.EnsureWith(root, project.Metadata{Name: "proj", Worker: "Alex"}); err != nil {
		t.Fatalf("EnsureWith: %v", err)
	}
	d := Discover(root)
	if !d.OhgoDirExists || !d.ProjectFound {
		t.Fatalf("discovery = %+v, want .ohgo present and project found", d)
	}
	if NeedsInit(d) {
		t.Error("NeedsInit = true for an initialized project, want false")
	}
}

func TestNeedsInitWhenMissing(t *testing.T) {
	d := Discover(t.TempDir())
	if d.OhgoDirExists || d.ProjectFound {
		t.Fatalf("discovery = %+v, want nothing found", d)
	}
	if !NeedsInit(d) {
		t.Error("NeedsInit = false for a fresh directory, want true")
	}
}

func TestNeedsInitWithOhgoDirButNoFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, project.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	d := Discover(root)
	if !d.OhgoDirExists || d.ProjectFound {
		t.Fatalf("discovery = %+v, want dir without project", d)
	}
	if !NeedsInit(d) {
		t.Error("NeedsInit = false with no project.yml, want true")
	}
}

func TestNeedsInitWithIncompleteProject(t *testing.T) {
	// An id/root-only file is valid (its id must survive) but not complete:
	// the gate must run to establish the project-local metadata.
	root := t.TempDir()
	if _, err := project.Ensure(root); err != nil {
		t.Fatal(err)
	}
	d := Discover(root)
	if !d.ProjectFound {
		t.Fatal("ProjectFound = false for an id/root-only project")
	}
	if d.Project.Complete() {
		t.Fatal("an id/root-only project reported Complete")
	}
	if !NeedsInit(d) {
		t.Error("NeedsInit = false for an incomplete project, want true")
	}
}

func TestMalformedProjectNeedsInitRatherThanRegenerating(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, project.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.Path(root), []byte("{not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !NeedsInit(Discover(root)) {
		t.Error("NeedsInit = false for a malformed project.yml, want true")
	}
}

func TestGitDetection(t *testing.T) {
	plain := Discover(t.TempDir())
	if plain.HasGit {
		t.Error("HasGit = true outside a repository")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := Discover(root)
	if !repo.HasGit {
		t.Error("HasGit = false inside a .git directory")
	}
	nested := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if !Discover(nested).HasGit {
		t.Error("HasGit = false inside a nested worktree directory")
	}
}

// Greeting pins the deterministic clock contract.

func TestGreetingRanges(t *testing.T) {
	cases := []struct {
		hour int
		want string
	}{
		{5, "Good morning, Jack."},
		{11, "Good morning, Jack."},
		{12, "Good afternoon, Jack."},
		{17, "Good afternoon, Jack."},
		{18, "Good evening, Jack."},
		{2, "Good evening, Jack."},
		{4, "Good evening, Jack."},
	}
	for _, tc := range cases {
		now := time.Date(2026, 10, 12, tc.hour, 30, 0, 0, time.Local)
		if got := Greeting("Jack", now); got != tc.want {
			t.Errorf("hour %d: got %q, want %q", tc.hour, got, tc.want)
		}
	}
}

func TestGreetingWithoutName(t *testing.T) {
	now := time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local)
	if got := Greeting("", now); got != "Good evening." {
		t.Errorf("got %q, want %q", got, "Good evening.")
	}
	if got := Greeting("  ", now); got != "Good evening." {
		t.Errorf("whitespace name: got %q, want %q", got, "Good evening.")
	}
}

// testDeps builds Deps with in-memory settings and a stubbed project commit,
// so flows never touch the real home directory.
type testDeps struct {
	settings  config.Settings
	committed *project.Metadata
	ensure    func(dir string, meta project.Metadata) (project.Identity, error)
	gitInit   *bool
	gitErr    error
	avail     *bool
}

func (td *testDeps) deps(in string, out *strings.Builder) Deps {
	if td.gitInit != nil {
		*td.gitInit = false
	}
	return Deps{
		LoadSettings: func() (config.Settings, error) { return td.settings, nil },
		CommitProject: func(dir string, meta project.Metadata) (project.Identity, error) {
			if td.committed != nil {
				*td.committed = meta
			}
			if td.ensure != nil {
				return td.ensure(dir, meta)
			}
			return project.EnsureWith(dir, meta)
		},
		InitGit: func(dir string) error {
			if td.gitInit != nil {
				*td.gitInit = true
			}
			return td.gitErr
		},
		GitAvailable: func() bool {
			if td.avail != nil {
				return *td.avail
			}
			return true
		},
		Stdin:  strings.NewReader(in),
		Stdout: out,
		Now:    func() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local) },
	}
}

func TestFullFlowGitAccepted(t *testing.T) {
	root := t.TempDir()
	var committed project.Metadata
	var gitCalled bool
	td := &testDeps{committed: &committed, gitInit: &gitCalled}
	var out strings.Builder
	res, err := Ensure(root, td.deps("\nAlex\npayments-api\n", &out))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !res.CreatedProject || !res.AskedGit || !res.AskedWorker || !res.AskedProject {
		t.Errorf("result = %+v, want all of created/asked-git/asked-worker/asked-project", res)
	}
	if !gitCalled {
		t.Error("git init was not called after Enter (default yes)")
	}
	if committed.Worker != "Alex" || committed.Name != "payments-api" {
		t.Errorf("committed = %+v, want worker Alex and project payments-api", committed)
	}
	if res.Project.WorkerName() != "Alex" || res.Project.Name != "payments-api" {
		t.Errorf("project = %+v, want the confirmed worker and name", res.Project)
	}
	if res.Greeting != "Good evening, Alex." {
		t.Errorf("greeting = %q, want evening for Alex", res.Greeting)
	}
	raw, err := os.ReadFile(project.Path(project.Root(root)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Alex", "payments-api", "user:"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("project.yml missing %q: %s", want, raw)
		}
	}
}

func TestFullFlowGitDeclined(t *testing.T) {
	root := t.TempDir()
	var committed project.Metadata
	var gitCalled bool
	td := &testDeps{committed: &committed, gitInit: &gitCalled}
	var out strings.Builder
	res, err := Ensure(root, td.deps("n\nAlex\npayments-api\n", &out))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if gitCalled {
		t.Error("git init ran after an explicit n")
	}
	if !res.AskedGit || !res.CreatedProject {
		t.Errorf("result = %+v, want asked-git and created", res)
	}
	if committed.Worker != "Alex" {
		t.Errorf("committed worker = %q, want Alex", committed.Worker)
	}
}

func TestInvalidGitAnswerRepromptsWithoutInitializing(t *testing.T) {
	root := t.TempDir()
	var gitCalled bool
	td := &testDeps{gitInit: &gitCalled}
	var out strings.Builder
	_, err := Ensure(root, td.deps("maybe\ny\nAlex\nproj\n", &out))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !gitCalled {
		t.Error("git init never ran after the corrected yes")
	}
	if !strings.Contains(out.String(), "y or n") {
		t.Errorf("output lacks the re-prompt: %q", out.String())
	}
}

func TestExistingGitSkipsPrompt(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var gitCalled bool
	td := &testDeps{gitInit: &gitCalled}
	var out strings.Builder
	res, err := Ensure(root, td.deps("Alex\nproj\n", &out))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.AskedGit {
		t.Error("git prompt appeared despite an existing repository")
	}
	if gitCalled {
		t.Error("git init ran inside an existing repository")
	}
	if strings.Contains(out.String(), "Git repository") {
		t.Errorf("output mentions git setup: %q", out.String())
	}
}

func TestGlobalDefaultSeedsWorkerPrompt(t *testing.T) {
	// A machine-global default name is a suggestion, not authority: the prompt
	// still runs and the confirmed value is written project-locally.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := config.Default()
	settings.UserDefaults.Name = "Jack"
	deps := Deps{
		LoadSettings:  func() (config.Settings, error) { return settings, nil },
		CommitProject: project.EnsureWith,
		Stdin:         strings.NewReader("\n\na-project\n"),
		Stdout:        &strings.Builder{},
		Now:           func() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local) },
	}
	res, err := Ensure(root, deps)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !res.AskedWorker {
		t.Error("worker prompt was skipped: the global default must only seed it")
	}
	if res.Project.WorkerName() != "Jack" {
		t.Errorf("worker = %q, want the confirmed default Jack", res.Project.WorkerName())
	}
	if res.Greeting != "Good evening, Jack." {
		t.Errorf("greeting = %q, want Jack", res.Greeting)
	}
	if settings.GlobalName() != "Jack" {
		t.Errorf("global default mutated to %q", settings.GlobalName())
	}
}

func TestExistingOhgoSkipsEntireInitialization(t *testing.T) {
	root := t.TempDir()
	first, err := project.EnsureWith(root, project.Metadata{Name: "proj", Worker: "Alex"})
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		LoadSettings: func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: func(string, project.Metadata) (project.Identity, error) {
			t.Error("CommitProject called on the fast path")
			return project.Identity{}, nil
		},
		InitGit: func(string) error {
			t.Error("InitGit called on the fast path")
			return nil
		},
		Stdin:  strings.NewReader("y\ny\n"),
		Stdout: &strings.Builder{},
		Now:    func() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local) },
	}
	res, err := Ensure(root, deps)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.CreatedProject || res.AskedGit || res.AskedWorker || res.AskedProject {
		t.Errorf("result = %+v, want a pure fast path", res)
	}
	if res.Project.ID != first.ID {
		t.Errorf("project id = %q, want the stable %q", res.Project.ID, first.ID)
	}
}

func TestProjectWorkerIsolation(t *testing.T) {
	// The worker belongs to a project: one machine, two projects, two workers.
	// Initializing B must not reuse A's worker and must not touch A's file.
	init := func(root, worker, name string) Result {
		t.Helper()
		res, err := Ensure(root, Deps{
			LoadSettings:  func() (config.Settings, error) { return config.Default(), nil },
			CommitProject: project.EnsureWith,
			Stdin:         strings.NewReader("n\n" + worker + "\n" + name + "\n"),
			Stdout:        &strings.Builder{},
			Now:           func() time.Time { return time.Date(2026, 10, 12, 8, 0, 0, 0, time.Local) },
		})
		if err != nil {
			t.Fatalf("Ensure(%s): %v", root, err)
		}
		return res
	}
	a, b := t.TempDir(), t.TempDir()
	first := init(a, "Alice", "project-a")
	second := init(b, "Bob", "project-b")

	if first.Project.WorkerName() != "Alice" || second.Project.WorkerName() != "Bob" {
		t.Fatalf("workers = %q, %q, want Alice, Bob", first.Project.WorkerName(), second.Project.WorkerName())
	}
	if first.Project.Name != "project-a" || second.Project.Name != "project-b" {
		t.Errorf("names = %q, %q, want project-a, project-b", first.Project.Name, second.Project.Name)
	}
	if first.Project.ID == second.Project.ID {
		t.Errorf("A and B share identity %q", first.Project.ID)
	}
	// Reload from disk: the project-local values are authoritative.
	for _, tc := range []struct {
		root, worker, name string
	}{{a, "Alice", "project-a"}, {b, "Bob", "project-b"}} {
		got, found, err := project.Load(tc.root)
		if err != nil || !found {
			t.Fatalf("Load(%s) = %v, %v, %v", tc.root, got, found, err)
		}
		if got.WorkerName() != tc.worker || got.Name != tc.name {
			t.Errorf("%s identity = %+v, want worker %s / name %s", tc.root, got, tc.worker, tc.name)
		}
	}
}

func TestWorkerIdentityNeverWrittenToGlobalSettings(t *testing.T) {
	// The worker is established by the gate and persists project-locally. The
	// machine-global settings must never be the authority for it.
	root := t.TempDir()
	settings := config.Default()
	deps := Deps{
		LoadSettings:  func() (config.Settings, error) { return settings, nil },
		CommitProject: project.EnsureWith,
		Stdin:         strings.NewReader("n\nAlice\npayments-api\n"),
		Stdout:        &strings.Builder{},
		Now:           time.Now,
	}
	if _, err := Ensure(root, deps); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if settings.GlobalName() != "" {
		t.Errorf("startup wrote the worker into global settings: %q", settings.GlobalName())
	}
	raw, err := os.ReadFile(project.Path(project.Root(root)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Alice") {
		t.Errorf("project.yml does not carry the worker: %s", raw)
	}
}

// Failures must be truthful: report, never claim, and keep valid state.

func TestGitUnavailableContinues(t *testing.T) {
	root := t.TempDir()
	avail := false
	var gitCalled bool
	td := &testDeps{gitInit: &gitCalled, avail: &avail}
	var out strings.Builder
	res, err := Ensure(root, td.deps("\nAlex\nproj\n", &out))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if gitCalled {
		t.Error("git init attempted though git is unavailable")
	}
	if !res.CreatedProject {
		t.Error("project creation did not continue without git")
	}
	if !strings.Contains(out.String(), "unavailable") {
		t.Errorf("output hides the missing git: %q", out.String())
	}
}

func TestGitInitFailureContinuesWithoutClaim(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder
	td := &testDeps{gitErr: errors.New("permission denied")}
	res, err := Ensure(root, td.deps("\nAlex\nproj\n", &out))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !res.CreatedProject {
		t.Error("project creation did not continue after git failure")
	}
	if !strings.Contains(out.String(), "failed") {
		t.Errorf("output hides the git failure: %q", out.String())
	}
}

func TestProjectCommitFailureSurfaces(t *testing.T) {
	root := t.TempDir()
	want := errors.New("cannot write")
	deps := Deps{
		LoadSettings: func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: func(string, project.Metadata) (project.Identity, error) {
			return project.Identity{}, want
		},
		Stdin:  strings.NewReader("n\nAlex\nproj\n"),
		Stdout: &strings.Builder{},
		Now:    time.Now,
	}
	if _, err := Ensure(root, deps); !errors.Is(err, want) {
		t.Errorf("Ensure err = %v, want the commit failure surfaced", err)
	}
}

func TestMalformedSettingsWarnsAndContinues(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder
	deps := Deps{
		LoadSettings:  func() (config.Settings, error) { return config.Default(), errors.New("parse settings.json: bad json") },
		CommitProject: project.EnsureWith,
		Stdin:         strings.NewReader("n\nAlex\nproj\n"),
		Stdout:        &out,
		Now:           func() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local) },
	}
	res, err := Ensure(root, deps)
	if err != nil {
		t.Fatalf("malformed settings must not be fatal: %v", err)
	}
	if !res.CreatedProject {
		t.Error("project creation did not continue after malformed settings")
	}
}

func TestNonInteractiveNeverCreatesOrPrompts(t *testing.T) {
	// NonInteractive suppresses prompts but cannot substitute for the human
	// decisions the gate collects: an uninitialized project must cancel, not
	// be created with defaults.
	root := t.TempDir()
	creates := 0
	deps := Deps{
		LoadSettings: func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: func(dir string, meta project.Metadata) (project.Identity, error) {
			creates++
			return project.EnsureWith(dir, meta)
		},
		InitGit:        func(string) error { t.Error("InitGit called"); return nil },
		NonInteractive: true,
		Stdout:         &strings.Builder{},
		Now:            time.Now,
	}
	if _, err := Ensure(root, deps); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Ensure err = %v, want ErrCanceled", err)
	}
	if creates != 0 {
		t.Error("NonInteractive created project state")
	}
	if _, statErr := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf(".ohgo created by NonInteractive: %v", statErr)
	}

	// On an initialized project it is a silent fast path: no prompts, no
	// writes, and the existing identity is returned unchanged.
	if _, err := project.EnsureWith(root, project.Metadata{Name: "proj", Worker: "Alex"}); err != nil {
		t.Fatalf("EnsureWith: %v", err)
	}
	res, err := Ensure(root, deps)
	if err != nil {
		t.Fatalf("fast-path Ensure: %v", err)
	}
	if res.AskedGit || res.AskedWorker || res.AskedProject || res.CreatedProject {
		t.Errorf("result = %+v, want a silent fast path", res)
	}
	if res.State != Initialized {
		t.Errorf("state = %v, want Initialized", res.State)
	}
}

func TestClosedStdinCancelsInitialization(t *testing.T) {
	// A closed pipe reads EOF, which is not an answer. EOF must cancel the
	// gate rather than authorize anything: no git init, and — the core
	// invariant of this slice — no .ohgo/ created as a side effect.
	root := t.TempDir()
	var gitCalled bool
	td := &testDeps{gitInit: &gitCalled}
	var out strings.Builder
	res, err := Ensure(root, td.deps("", &out))
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("Ensure err = %v, want ErrCanceled", err)
	}
	if res.State != Uninitialized {
		t.Errorf("state = %v, want Uninitialized after cancel", res.State)
	}
	if gitCalled {
		t.Error("git init ran on EOF without explicit authorization")
	}
	if _, statErr := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf(".ohgo exists after a canceled flow: %v", statErr)
	}
}

func TestCancellationAfterGitAnswerCreatesNothing(t *testing.T) {
	// Answering "n" to Git and then cancelling at the worker prompt (EOF) must
	// leave the directory untouched: decisions are collected before any
	// mutation, so a cancel never leaves partial state behind.
	root := t.TempDir()
	td := &testDeps{}
	var out strings.Builder
	res, err := Ensure(root, td.deps("n\n", &out))
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("Ensure err = %v, want ErrCanceled", err)
	}
	if res.State != Uninitialized {
		t.Errorf("state = %v, want Uninitialized", res.State)
	}
	if res.AskedProject {
		t.Error("project-name prompt ran after cancellation at the worker prompt")
	}
	if _, statErr := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf(".ohgo exists after cancel: %v", statErr)
	}
}

func TestQuitAtGitPromptCreatesNothing(t *testing.T) {
	// The explicit cancel token at the Git prompt must abort the whole flow,
	// not be misread as "decline Git, continue".
	root := t.TempDir()
	var gitCalled bool
	td := &testDeps{gitInit: &gitCalled}
	var out strings.Builder
	if _, err := Ensure(root, td.deps("quit\n", &out)); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Ensure err = %v, want ErrCanceled", err)
	}
	if gitCalled {
		t.Error("git init ran after an explicit quit")
	}
	if _, statErr := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf(".ohgo exists after quit: %v", statErr)
	}
}

func TestNoReaderNoOptInCancels(t *testing.T) {
	// Without a reader and without the explicit NonInteractive opt-in there
	// is no consent at all, so initialization must not happen.
	root := t.TempDir()
	deps := Deps{
		LoadSettings:  func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: project.EnsureWith,
	}
	if _, err := Ensure(root, deps); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Ensure err = %v, want ErrCanceled", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf(".ohgo exists without confirmation: %v", statErr)
	}
}

func TestDiscoverIsReadOnly(t *testing.T) {
	// Discovery is the read side of the boundary: calling it on a fresh
	// directory must not create .ohgo/ no matter how many times it runs.
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		d := Discover(root)
		if d.State != Uninitialized {
			t.Fatalf("iteration %d: state = %v, want Uninitialized", i, d.State)
		}
	}
	if _, err := os.Stat(filepath.Join(root, project.DirName)); !os.IsNotExist(err) {
		t.Errorf(".ohgo created by discovery: %v", err)
	}
}

func TestStateTransitionsAcrossInitialization(t *testing.T) {
	// The lifecycle is explicit: Uninitialized, then Initialized after the
	// human completes the flow. A reorg of the gate must not silently skip a
	// transition.
	root := t.TempDir()
	if got := Discover(root).State; got != Uninitialized {
		t.Fatalf("fresh state = %v, want Uninitialized", got)
	}
	td := &testDeps{}
	res, err := Ensure(root, td.deps("n\nAlex\nproj\n", &strings.Builder{}))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.State != Initialized {
		t.Errorf("result state = %v, want Initialized", res.State)
	}
	if got := Discover(root).State; got != Initialized {
		t.Errorf("post-init state = %v, want Initialized", got)
	}
}

func TestTwoProjectsStayIndependent(t *testing.T) {
	// Initializing one directory must never create state in a sibling, and
	// the two must mint distinct identities. A shared id would merge their
	// session history; a shared directory would make them one project.
	a, b := t.TempDir(), t.TempDir()

	initOne := func(root, worker, name string) Result {
		t.Helper()
		res, err := Ensure(root, Deps{
			LoadSettings:  func() (config.Settings, error) { return config.Default(), nil },
			CommitProject: project.EnsureWith,
			Stdin:         strings.NewReader("n\n" + worker + "\n" + name + "\n"),
			Stdout:        &strings.Builder{},
			Now:           time.Now,
		})
		if err != nil {
			t.Fatalf("Ensure(%s): %v", root, err)
		}
		return res
	}
	first := initOne(a, "Alice", "project-a")
	if _, statErr := os.Stat(filepath.Join(b, project.DirName)); !os.IsNotExist(statErr) {
		t.Errorf("initializing A created B's .ohgo: %v", statErr)
	}
	second := initOne(b, "Bob", "project-b")
	if first.Project.ID == "" || second.Project.ID == "" {
		t.Fatalf("missing identity: A=%q B=%q", first.Project.ID, second.Project.ID)
	}
	if first.Project.ID == second.Project.ID {
		t.Errorf("A and B share identity %q", first.Project.ID)
	}
	for _, root := range []string{a, b} {
		if _, err := os.Stat(project.Path(root)); err != nil {
			t.Errorf("project.yml missing in %s: %v", root, err)
		}
	}
}

func TestInvalidProjectIsUninitializedNotRepaired(t *testing.T) {
	// .ohgo/ present without a valid project.yml is UNINITIALIZED (invalid):
	// discovery must report it and leave the directory alone. Repair belongs
	// to the explicit flow.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, project.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	d := Discover(root)
	if d.State != Uninitialized {
		t.Errorf("state = %v, want Uninitialized", d.State)
	}
	if !d.Invalid {
		t.Error("Invalid = false for .ohgo/ without project.yml")
	}
	if _, err := os.Stat(project.Path(root)); !os.IsNotExist(err) {
		t.Errorf("discovery created project.yml: %v", err)
	}
}

// FormatGit pins the truthful one-line vocabulary.

func TestFormatGit(t *testing.T) {
	cases := []struct {
		name  string
		state GitState
		want  string
	}{
		{"unavailable", GitState{Available: false}, "unavailable"},
		{"not initialized", GitState{Available: true}, "not initialized"},
		{"clean", GitState{Available: true, Initialized: true, Branch: "main", Known: true}, "main · clean"},
		{"changed", GitState{Available: true, Initialized: true, Branch: "main", Summary: vcs.Summary{Unstaged: 3}, Known: true}, "main · 3 changed"},
		{"staged and modified", GitState{Available: true, Initialized: true, Branch: "main", Summary: vcs.Summary{Staged: 2, Unstaged: 3}, Known: true}, "main · 2 staged · 3 modified"},
		{"unknown counts", GitState{Available: true, Initialized: true, Branch: "main"}, "main"},
		{"detached", GitState{Available: true, Initialized: true, Branch: "a1b2c3d", Detached: true, Known: true}, "a1b2c3d · clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatGit(tc.state); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBranchStatusSplit(t *testing.T) {
	_, status := GitState{Available: false}.BranchStatus()
	if status != "unavailable" {
		t.Errorf("status = %q, want unavailable", status)
	}
	branch, status := GitState{Available: true, Initialized: true, Branch: "main", Known: true}.BranchStatus()
	if branch != "main" || status != "clean" {
		t.Errorf("got (%q, %q), want (main, clean)", branch, status)
	}
}

func TestShortWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := ShortWorkspace(home); got != "~" {
		t.Errorf("got %q, want ~", got)
	}
	sub := filepath.Join(home, "pizenlabs", "ohgo")
	if got := ShortWorkspace(sub); !strings.HasPrefix(got, "~") {
		t.Errorf("got %q, want a ~-collapsed path", got)
	}
	outside := t.TempDir()
	if got := ShortWorkspace(outside); got != outside {
		t.Errorf("got %q, want the path unchanged", got)
	}
}

// The deleted-.ohgo regression suite: removing project state must always
// re-enter initialization. These tests pin the exact reported failure —
// rm -rf .ohgo followed by a run must initialize again, never fast-path.

func TestDeleteOhgoReinitializes(t *testing.T) {
	// First initialization through the real commit, so the flow under test is
	// the production one, not a stub.
	root := t.TempDir()
	evening := func() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local) }
	first, err := Ensure(root, Deps{
		LoadSettings: func() (config.Settings, error) { return config.Default(), nil },
		Stdin:        strings.NewReader("n\nAlex\nproj\n"),
		Stdout:       &strings.Builder{},
		Now:          evening,
	})
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if !first.CreatedProject || !first.AskedGit || !first.AskedWorker || !first.AskedProject {
		t.Fatalf("first result = %+v, want a full initialization", first)
	}
	before, err := os.ReadFile(project.Path(project.Root(root)))
	if err != nil {
		t.Fatalf("project.yml missing after init: %v", err)
	}

	// The reported failure: delete the whole project directory.
	if err := os.RemoveAll(filepath.Join(project.Root(root), project.DirName)); err != nil {
		t.Fatal(err)
	}

	d := Discover(root)
	if d.OhgoDirExists {
		t.Error("OhgoDirExists = true after rm -rf .ohgo")
	}
	if d.ProjectFound {
		t.Error("ProjectFound = true with no project.yml: a missing project must stay distinguishable")
	}
	if !NeedsInit(d) {
		t.Fatal("NeedsInit = false after rm -rf .ohgo, want re-initialization")
	}

	// Initialization runs again. The worker identity was deleted with .ohgo,
	// so it is re-established project-locally rather than resurrected from a
	// machine-global field.
	var out strings.Builder
	second, err := Ensure(root, Deps{
		LoadSettings: func() (config.Settings, error) { return config.Default(), nil },
		Stdin:        strings.NewReader("n\nAlex\nproj\n"),
		Stdout:       &out,
		Now:          evening,
	})
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if !second.CreatedProject {
		t.Error("CreatedProject = false: the project was not re-established")
	}
	if !second.AskedGit {
		t.Error("AskedGit = false: the Git question did not repeat")
	}
	if !second.AskedWorker || !second.AskedProject {
		t.Error("project-local metadata was not re-established after .ohgo deletion")
	}
	if second.Project.WorkerName() != "Alex" {
		t.Errorf("worker = %q, want Alex re-established locally", second.Project.WorkerName())
	}
	if second.Greeting != "Good evening, Alex." {
		t.Errorf("greeting = %q, want Alex re-established", second.Greeting)
	}
	after, err := os.ReadFile(project.Path(project.Root(root)))
	if err != nil {
		t.Fatalf("project.yml was not recreated: %v", err)
	}
	if string(after) == string(before) {
		t.Error("project.yml is byte-identical: re-initialization must mint a fresh identity, not resurrect the deleted one")
	}
	if !strings.Contains(out.String(), "runtime ready") {
		t.Errorf("init completion missing from output: %q", out.String())
	}
}

func TestDeleteOhgoInGitRepoRepromptsWorker(t *testing.T) {
	// Existing Git survives .ohgo deletion, so the Git question stays gone.
	// The project-local worker identity does not survive, so its prompt must
	// return: the machine-global default is only a suggestion, never a reason
	// to skip establishing the project's own identity.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	evening := func() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.Local) }
	settings := config.Default()
	run := func(worker, name string) Result {
		t.Helper()
		res, err := Ensure(root, Deps{
			LoadSettings: func() (config.Settings, error) { return settings, nil },
			Stdin:        strings.NewReader(worker + "\n" + name + "\n"),
			Stdout:       &strings.Builder{},
			Now:          evening,
		})
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		return res
	}
	if _, err := os.Stat(project.Path(root)); !os.IsNotExist(err) {
		t.Fatal("project state existed before the first run")
	}
	run("Alex", "proj")
	if err := os.RemoveAll(filepath.Join(root, project.DirName)); err != nil {
		t.Fatal(err)
	}
	second := run("Alex", "proj")
	if !second.CreatedProject {
		t.Error("project was not re-established after rm -rf .ohgo")
	}
	if second.AskedGit {
		t.Error("Git prompt repeated though the repository still exists")
	}
	if !second.AskedWorker {
		t.Error("worker prompt skipped though the project-local identity was deleted")
	}
	if _, err := os.Stat(project.Path(root)); err != nil {
		t.Errorf("project.yml was not recreated: %v", err)
	}
}

func TestDeleteProjectFileReinitializes(t *testing.T) {
	// The directory alone is never sufficient evidence: .ohgo/ without a
	// valid project.yml must initialize.
	root := t.TempDir()
	if _, err := project.EnsureWith(root, project.Metadata{Name: "proj", Worker: "Alex"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(project.Path(root)); err != nil {
		t.Fatal(err)
	}
	d := Discover(root)
	if !d.OhgoDirExists {
		t.Error("OhgoDirExists = false: the directory itself was not deleted")
	}
	if d.ProjectFound {
		t.Error("ProjectFound = true with project.yml deleted")
	}
	if !NeedsInit(d) {
		t.Error("NeedsInit = false with project.yml deleted: directory existence alone must never suffice")
	}
	var gitCalled bool
	res, err := Ensure(root, Deps{
		LoadSettings:  func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: project.EnsureWith,
		InitGit:       func(string) error { gitCalled = true; return nil },
		Stdin:         strings.NewReader("n\nAlex\nproj\n"),
		Stdout:        &strings.Builder{},
		Now:           time.Now,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !res.CreatedProject || gitCalled {
		t.Errorf("result = %+v, gitCalled = %v, want recreated project without git init", res, gitCalled)
	}
}

func TestFastPathDoesNotRewriteProjectFile(t *testing.T) {
	// A complete project.yml must survive a fast-path run byte-identical:
	// resuming the runtime never rotates project identity or metadata.
	root := t.TempDir()
	if _, err := project.EnsureWith(root, project.Metadata{Name: "proj", Worker: "Alex"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Ensure(root, Deps{
		LoadSettings: func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: func(string, project.Metadata) (project.Identity, error) {
			t.Error("CommitProject called")
			return project.Identity{}, nil
		},
		Stdin:  strings.NewReader(""),
		Stdout: &strings.Builder{},
		Now:    time.Now,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.CreatedProject {
		t.Error("CreatedProject = true on a complete project")
	}
	if res.Project.WorkerName() != "Alex" {
		t.Errorf("fast path lost the worker: %q", res.Project.WorkerName())
	}
	after, err := os.ReadFile(project.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("project.yml rewritten:\nbefore %q\nafter  %q", before, after)
	}
}

func TestIncompleteProjectRecoveredWithoutChangingID(t *testing.T) {
	// An id/root-only file from an earlier build is completed through the gate
	// without changing its id. This is the migration contract.
	root := t.TempDir()
	original, err := project.Ensure(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "name:") {
		t.Fatalf("fixture already carried a name: %s", before)
	}

	res, err := Ensure(root, Deps{
		LoadSettings:  func() (config.Settings, error) { return config.Default(), nil },
		CommitProject: project.EnsureWith,
		Stdin:         strings.NewReader("n\nAlice\npayments-api\n"),
		Stdout:        &strings.Builder{},
		Now:           time.Now,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.Project.ID != original.ID {
		t.Errorf("id = %q, want the preserved %q", res.Project.ID, original.ID)
	}
	if res.Project.Name != "payments-api" || res.Project.WorkerName() != "Alice" {
		t.Errorf("recovered metadata = %+v, want payments-api / Alice", res.Project)
	}
	if Discover(root).State != Initialized {
		t.Error("project did not become Initialized after recovery")
	}
}

func TestDiscoveryKeepsMissingDistinctFromInitialized(t *testing.T) {
	// Load must not manufacture initialization: a missing project yields
	// the zero identity with found=false, and discovery must propagate
	// that distinction rather than upgrading an empty struct.
	root := t.TempDir()
	identity, found, err := project.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if found {
		t.Fatal("Load reported found for a missing project")
	}
	if identity.Valid() {
		t.Errorf("zero identity %+v reports valid", identity)
	}
	d := Discover(root)
	if d.ProjectFound {
		t.Error("discovery upgraded a missing project to found")
	}
	if d.Project.Valid() {
		t.Errorf("discovery carries a valid-looking project for a missing one: %+v", d.Project)
	}
	if !NeedsInit(d) {
		t.Error("NeedsInit = false for a missing project")
	}
}
