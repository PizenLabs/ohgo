// Package startup owns OHGO's startup lifecycle: project discovery,
// project initialization, Git detection, and project-local metadata.
//
// It is the single bootstrap owner. The TUI renders runtime facts but never
// decides whether a project should be initialized; the CLI calls Ensure
// before constructing the runtime, and Ensure performs at most the prompts
// the current state requires. No network operation happens here.
//
// The gate establishes project-local metadata: a human-readable project name
// and the human working with this project. Both belong to
// `<project>/.ohgo/project.yml`. A machine-global default name is only ever a
// suggestion for the worker prompt, never the persisted value.
//
// Initialization also owns the two explicit repository decisions: a new
// repository starts on main, and project-local runtime state is proposed for
// .gitignore. Both are proposals until a human confirms them; discovery and
// the fast path never rewrite .gitignore or a branch.
package startup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/vcs"
	"github.com/PizenLabs/ohgo/project"
)

// ErrCanceled means the initialization flow was aborted before it committed
// anything: the user answered cancel/quit, stdin reached EOF, or a
// non-interactive caller supplied no confirmation. The project remains
// UNINITIALIZED and no Git mutation is performed.
var ErrCanceled = errors.New("startup: initialization canceled")

// State is where a working directory sits in the startup lifecycle.
//
// It exists so "is this project initialized?" is one explicit fact rather
// than a boolean each caller re-derives. Discovery only ever observes
// Uninitialized or Initialized; Initializing is the transient phase the
// mutation boundary (Ensure) enters once a human has confirmed the flow.
type State int

const (
	// Uninitialized means no complete project identity exists yet. Nothing has
	// been created and nothing may be created outside the initialization flow.
	Uninitialized State = iota
	// Initializing means the initialization flow is collecting the required
	// decisions. No project state is durable yet; a cancel returns to
	// Uninitialized.
	Initializing
	// Initialized means a valid project.yml with the project-local metadata
	// exists and startup may fast-path.
	Initialized
)

// String returns the lowercase state name used in diagnostics.
func (s State) String() string {
	switch s {
	case Uninitialized:
		return "uninitialized"
	case Initializing:
		return "initializing"
	case Initialized:
		return "initialized"
	default:
		return "unknown"
	}
}

// Discovery is the observed startup state for one working directory.
type Discovery struct {
	// Dir is the directory startup was asked about, absolute.
	Dir string
	// Root is the project root (repository root inside a repo, else Dir).
	Root string
	// State classifies the directory. It is derived, never authored.
	State State
	// OhgoDirExists reports whether <root>/.ohgo/ exists.
	OhgoDirExists bool
	// ProjectFound reports whether a valid project.yml was loaded. A found
	// project may still be incomplete: an id/root-only file from an earlier
	// build is valid but lacks the project-local metadata.
	ProjectFound bool
	// Invalid reports that .ohgo/ exists but project.yml is missing or
	// unreadable. It is distinct from a never-initialized directory only for
	// diagnostics: both require the initialization flow, and discovery must
	// never "repair" either by minting an identity.
	Invalid bool
	// Project is the loaded identity when ProjectFound.
	Project project.Identity
	// HasGit reports whether dir sits inside a Git worktree.
	HasGit bool
	// GitRoot is the repository root when HasGit.
	GitRoot string
	// GitignoreExists reports whether the project root has a .gitignore.
	GitignoreExists bool
	// OhgoIgnored reports whether the existing .gitignore already ignores
	// .ohgo/. It is false when the file is absent or unreadable.
	OhgoIgnored bool
}

// Classify derives the lifecycle state from observed facts. It is pure and
// read-only: calling it can never initialize a project.
//
// A found-but-incomplete identity is UNINITIALIZED: the gate must complete it
// (without changing its id) rather than fast-pathing over missing metadata.
func Classify(d Discovery) State {
	if d.OhgoDirExists && d.ProjectFound && d.Project.Complete() {
		return Initialized
	}
	return Uninitialized
}

// Discover inspects the filesystem without prompting, touching the network,
// or creating anything. It is safe to call repeatedly: a discovery call must
// never be the reason <root>/.ohgo/ comes into existence.
func Discover(dir string) Discovery {
	abs := dir
	if abs == "" {
		if cwd, err := os.Getwd(); err == nil {
			abs = cwd
		} else {
			abs = "."
		}
	}
	if resolved, err := filepath.Abs(abs); err == nil {
		abs = resolved
	}
	root := project.Root(abs)
	ohgoDir := filepath.Join(root, project.DirName)
	ohgoExists := false
	if info, err := os.Stat(ohgoDir); err == nil && info.IsDir() {
		ohgoExists = true
	}
	identity, found, err := project.Load(abs)
	if err != nil {
		// An unreadable or malformed project.yml is not a reason to
		// regenerate: the initialization flow owns recovery, and discovery
		// must leave the directory untouched. Record it for diagnostics.
		found = false
	}
	var hasGit bool
	var gitRoot string
	if repo, err := vcs.Discover(abs); err == nil && repo != nil {
		hasGit = true
		gitRoot = repo.Root
	}
	ignore := InspectGitignore(root)
	d := Discovery{
		Dir:             abs,
		Root:            root,
		OhgoDirExists:   ohgoExists,
		ProjectFound:    found,
		Invalid:         ohgoExists && !found,
		Project:         identity,
		HasGit:          hasGit,
		GitRoot:         gitRoot,
		GitignoreExists: ignore.Exists,
		OhgoIgnored:     ignore.IgnoresOhgo,
	}
	d.State = Classify(d)
	return d
}

// NeedsInit reports whether project initialization must run.
//
// A present .ohgo/ directory whose project.yml carries complete project-local
// metadata skips initialization entirely: no Git prompt, no metadata prompts,
// no rewrite. NeedsInit is the sole reader-side gate; it never mutates
// anything.
func NeedsInit(d Discovery) bool {
	return d.State != Initialized
}

// Greeting returns the time-of-day greeting for name at now (local time).
//
// Ranges are deterministic: 05:00-11:59 morning, 12:00-17:59 afternoon,
// 18:00-04:59 evening. An empty name yields "Good evening." with no trailing
// space or comma.
func Greeting(name string, now time.Time) string {
	hour := now.Hour()
	var day string
	switch {
	case hour >= 5 && hour < 12:
		day = "Good morning"
	case hour >= 12 && hour < 18:
		day = "Good afternoon"
	default:
		day = "Good evening"
	}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return day + ", " + trimmed + "."
	}
	return day + "."
}

// DefaultName returns the OS local account name for first-time setup. It never
// prompts and never touches the network.
func DefaultName() string {
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return strings.TrimSpace(u.Username)
	}
	if env := strings.TrimSpace(os.Getenv("USER")); env != "" {
		return env
	}
	return ""
}

// GitState is the truthful repository state shown on the identity surface.
type GitState struct {
	// Available reports whether git could be inspected at all.
	Available bool
	// Initialized reports whether dir sits inside a worktree.
	Initialized bool
	// Branch is the short branch name, short SHA when detached, or "".
	Branch string
	// Detached reports HEAD is detached.
	Detached bool
	// Summary counts when known. Known is false when status failed.
	Summary vcs.Summary
	Known   bool
}

// DescribeGit inspects the repository containing dir without mutating it.
func DescribeGit(dir string) GitState {
	if _, err := exec.LookPath("git"); err != nil {
		return GitState{Available: false}
	}
	repo, err := vcs.Discover(dir)
	if err != nil || repo == nil {
		return GitState{Available: true, Initialized: false}
	}
	state := GitState{Available: true, Initialized: true}
	head, err := repo.ReadHead()
	if err == nil {
		state.Detached = head.Detached
		if head.Detached {
			if head.Commit == "" {
				state.Branch = "detached"
			} else {
				state.Branch = shortSHA(head.Commit)
				if state.Branch == "" {
					state.Branch = "detached"
				}
			}
		} else if head.Branch != "" {
			state.Branch = head.Branch
		} else if head.Commit == "" {
			state.Branch = "empty"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if summary, err := repo.Summary(ctx); err == nil {
		state.Summary = summary
		state.Known = true
	}
	return state
}

// BranchStatus splits a GitState into the branch and status fields the
// identity surface shows separately.
func (s GitState) BranchStatus() (branch, status string) {
	if !s.Available {
		return "", "unavailable"
	}
	if !s.Initialized {
		return "", "not initialized"
	}
	branch = strings.TrimSpace(s.Branch)
	if !s.Known {
		return branch, ""
	}
	if s.Summary.Clean() {
		return branch, "clean"
	}
	if s.Summary.Staged > 0 && s.Summary.Unstaged+s.Summary.Untracked > 0 {
		return branch, fmt.Sprintf("%d staged · %d modified", s.Summary.Staged, s.Summary.Unstaged+s.Summary.Untracked)
	}
	return branch, fmt.Sprintf("%d changed", s.Summary.Total())
}

// FormatGit renders a GitState in one line, e.g. "main · clean".
func FormatGit(s GitState) string {
	if !s.Available {
		return "unavailable"
	}
	if !s.Initialized {
		return "not initialized"
	}
	branch := strings.TrimSpace(s.Branch)
	if branch == "" {
		branch = "detached"
	}
	if !s.Known {
		return branch
	}
	if s.Summary.Clean() {
		return branch + " · clean"
	}
	if s.Summary.Staged > 0 && s.Summary.Unstaged+s.Summary.Untracked > 0 {
		return fmt.Sprintf("%s · %d staged · %d modified", branch, s.Summary.Staged, s.Summary.Unstaged+s.Summary.Untracked)
	}
	return fmt.Sprintf("%s · %d changed", branch, s.Summary.Total())
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ShortWorkspace renders a path with $HOME collapsed to ~ for density.
func ShortWorkspace(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if after, ok := strings.CutPrefix(path, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + after
	}
	return path
}

// Deps wires the seams Ensure needs. Zero values select production
// defaults, so callers only set what a test substitutes.
type Deps struct {
	// LoadSettings reads machine-global settings. Default: config.Load.
	LoadSettings func() (config.Settings, error)
	// CommitProject atomically writes .ohgo/project.yml with the collected
	// metadata, preserving an existing id. Default: project.EnsureWith.
	CommitProject func(dir string, meta project.Metadata) (project.Identity, error)
	// InitGit initializes a new repository on the default branch. Default:
	// vcs.Init(dir, vcs.DefaultBranch).
	InitGit func(dir string) error
	// EnsureGitignore applies the .ohgo/ ignore rule. Default: EnsureGitignore.
	EnsureGitignore func(dir string) (changed bool, err error)
	// GitAvailable reports whether the git binary exists. Default: LookPath.
	GitAvailable func() bool
	// Stdin is read for init answers. Default: os.Stdin.
	Stdin io.Reader
	// Stdout receives init prompts. Default: os.Stdout.
	Stdout io.Writer
	// Now supplies the clock for the greeting. Default: time.Now.
	Now func() time.Time
	// NonInteractive suppresses every prompt. It never authorizes creation:
	// without prompts there are no decisions, so an uninitialized project
	// cancels (ErrCanceled) rather than being created with defaults. It
	// affects only the initialization path; the fast path never prompts.
	NonInteractive bool
}

// Result is the outcome of Ensure.
type Result struct {
	// Settings is the resolved machine-global settings.
	Settings config.Settings
	// Project is the project identity, carrying the project-local metadata
	// (project name and worker) the gate established or loaded.
	Project project.Identity
	// Root is the project root.
	Root string
	// State is the lifecycle state this call observed or reached. A canceled
	// flow reports Uninitialized.
	State State
	// CreatedProject reports .ohgo/project.yml was written by this run, either
	// created or completed.
	CreatedProject bool
	// AskedGit reports the git prompt was shown.
	AskedGit bool
	// AskedIgnore reports the .ohgo/ ignore prompt was shown.
	AskedIgnore bool
	// IgnoredOhgo reports .ohgo/ is ignored after the run, either because a
	// rule already existed or because this run added one.
	IgnoredOhgo bool
	// AskedWorker reports the project worker-name prompt was shown.
	AskedWorker bool
	// AskedProject reports the project-name prompt was shown.
	AskedProject bool
	// Greeting is the time-of-day greeting for the project worker.
	Greeting string
	// Git is the observed repository state after initialization.
	Git GitState
}

// Ensure runs the explicit initialization lifecycle for dir. It fast-paths
// when a valid project.yml with complete project-local metadata already
// exists; otherwise it collects the Git, ignore, worker, and project-name
// decisions and only then writes project-local state. It never touches the
// network.
//
// The order is deliberate: decisions first, mutations last. A cancel at any
// prompt returns ErrCanceled with nothing created, so the flow can never
// leave a half-initialized .ohgo/, an unauthorized git init, or a .gitignore
// edit behind. The worker name is never persisted globally: it belongs to the
// project.
func Ensure(dir string, deps Deps) (Result, error) {
	load := deps.LoadSettings
	if load == nil {
		load = func() (config.Settings, error) { return config.Load(dir) }
	}
	commit := deps.CommitProject
	if commit == nil {
		commit = project.EnsureWith
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	out := deps.Stdout
	if out == nil {
		out = io.Discard
	}

	d := Discover(dir)
	settings, loadErr := load()
	if loadErr != nil && loadErr != config.ErrNotFound {
		_, _ = fmt.Fprintf(out, "ohgo: %v\n", loadErr)
	}

	if !NeedsInit(d) {
		return Result{
			Settings:    settings,
			Project:     d.Project,
			Root:        d.Root,
			State:       Initialized,
			IgnoredOhgo: d.OhgoIgnored,
			Greeting:    Greeting(d.Project.WorkerName(), now()),
			Git:         DescribeGit(d.Dir),
		}, nil
	}

	// Prompts run whenever a reader is supplied and the caller did not opt
	// out. A closed input reads EOF, which every prompt treats as cancellation
	// rather than as consent, so this is safe without a TTY probe.
	//
	// Creation requires decisions only a prompt can collect. A caller with no
	// reader — including a non-interactive one — has supplied none, so the
	// gate cannot complete and initialization cancels. There is deliberately
	// no "create with defaults" mode: that would be project creation without
	// the required human decisions, which is the bypass this flow exists to
	// prevent.
	in := deps.Stdin
	if deps.NonInteractive {
		in = nil
	}
	if in == nil {
		return Result{State: Uninitialized, Root: d.Root}, ErrCanceled
	}

	// One reader serves every prompt: separate buffered readers would
	// swallow each other's lines.
	reader := bufio.NewReader(in)

	// Phase 1 — collect decisions. Nothing is written here.
	wantGit := false
	askedGit := false
	if !d.HasGit {
		askedGit = true
		decision, err := promptGit(reader, out, deps)
		if err != nil {
			return Result{State: Uninitialized, Root: d.Root}, err
		}
		wantGit = decision
	}

	// Runtime state is local implementation detail, not project source. The
	// default is to keep it out of Git, but only an explicit answer authorizes
	// the .gitignore edit, and an already-ignored directory is never asked.
	wantIgnore := false
	askedIgnore := false
	if NeedsIgnore(d) {
		askedIgnore = true
		decision, err := promptIgnore(reader, out)
		if err != nil {
			return Result{State: Uninitialized, Root: d.Root}, err
		}
		wantIgnore = decision
	}

	// The worker belongs to the project. An existing project worker
	// (recovery) wins, then the machine-global default, then the OS account
	// name; none becomes authoritative without the human confirming it here.
	worker, err := promptValue(reader, out,
		"What should OHGO call you for this project?",
		"Name", workerDefault(d, settings))
	if err != nil {
		return Result{State: Uninitialized, Root: d.Root}, err
	}

	// The project name is the human-readable project identity. The directory
	// name is only a suggestion; the persisted value is the one confirmed
	// here.
	projectName, err := promptValue(reader, out,
		"What is this project called?",
		"Project", projectDefault(d))
	if err != nil {
		return Result{State: Uninitialized, Root: d.Root}, err
	}

	// Phase 2 — commit. Only now may anything on disk change.
	if wantGit {
		initGit := deps.InitGit
		if initGit == nil {
			initGit = gitInit
		}
		if gerr := initGit(d.Dir); gerr != nil {
			_, _ = fmt.Fprintf(out, "git init failed: %v; continuing without a repository.\n", gerr)
		} else {
			d.HasGit = true
			_, _ = fmt.Fprintf(out, "Git initialized on %s.\n", vcs.DefaultBranch)
		}
	}

	ignored := d.OhgoIgnored
	if wantIgnore {
		ensure := deps.EnsureGitignore
		if ensure == nil {
			ensure = EnsureGitignore
		}
		_, ierr := ensure(d.Root)
		switch {
		case ierr != nil:
			// A partial success is reported as one: the Git repository (if
			// any) and the project identity still stand, but the ignore rule
			// did not land and no later step silently retries it.
			_, _ = fmt.Fprintf(out, "Failed to update .gitignore: %v\n", ierr)
		case InspectGitignore(d.Root).IgnoresOhgo:
			// Verify the resulting file rather than trusting the write: the
			// claim on the surface must match what git will actually see.
			ignored = true
		default:
			_, _ = fmt.Fprintln(out, "Failed to verify .gitignore: .ohgo/ is not ignored.")
		}
	}

	identity, err := commit(d.Dir, project.Metadata{Name: projectName, Worker: worker})
	if err != nil {
		return Result{}, fmt.Errorf("startup: commit project: %w", err)
	}

	// Re-observe git after a possible init so the surface is truthful.
	git := DescribeGit(d.Dir)

	name := identity.WorkerName()
	greeting := Greeting(name, now())
	if name != "" {
		_, _ = fmt.Fprintf(out, "\nWelcome, %s.\n\nruntime ready\n", name)
	} else {
		_, _ = fmt.Fprintln(out, "\nruntime ready")
	}

	return Result{
		Settings:       settings,
		Project:        identity,
		Root:           d.Root,
		State:          Initialized,
		CreatedProject: true,
		AskedGit:       askedGit,
		AskedIgnore:    askedIgnore,
		IgnoredOhgo:    ignored,
		AskedWorker:    true,
		AskedProject:   true,
		Greeting:       greeting,
		Git:            git,
	}, nil
}

// workerDefault is the suggested worker name for the initialization prompt: an
// already-established project worker (recovery), else the machine-global
// default, else the OS account name. Every source is only a suggestion; the
// human confirms the value that is persisted.
func workerDefault(d Discovery, settings config.Settings) string {
	return WorkerSuggestion(d, settings)
}

// WorkerSuggestion is the worker name the initialization prompts offer. It
// is the shared source for the stdin gate and the TUI wizard, so both offer
// the same default: recovery first, then the machine-global default, then
// the OS account name. The value stays a suggestion until a human confirms
// it.
func WorkerSuggestion(d Discovery, settings config.Settings) string {
	if name := d.Project.WorkerName(); name != "" {
		return name
	}
	if name := settings.GlobalName(); name != "" {
		return name
	}
	return DefaultName()
}

// projectDefault is the suggested project name: an already-established name
// (recovery), else the directory name. It is never persisted without the human
// confirming it.
func projectDefault(d Discovery) string {
	return ProjectSuggestion(d)
}

// ProjectSuggestion is the project name the initialization prompts offer.
// It is the shared source for the stdin gate and the TUI wizard, so both
// offer the same default.
func ProjectSuggestion(d Discovery) string {
	if name := strings.TrimSpace(d.Project.Name); name != "" {
		return name
	}
	return filepath.Base(d.Root)
}

// promptGit asks until it gets y/n or a cancellation, and returns the decision
// without performing it. EOF without a newline is never an answer: a closed
// input must not authorize a Git mutation. q/quit/cancel abort the flow.
func promptGit(reader *bufio.Reader, out io.Writer, deps Deps) (bool, error) {
	_, _ = fmt.Fprintln(out, "This project is not a Git repository.")
	for {
		_, _ = fmt.Fprint(out, "Initialize Git on main? [Y/n/q] ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return false, fmt.Errorf("startup: read git answer: %w", rerr)
		}
		if line == "" && rerr == io.EOF {
			return false, ErrCanceled
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.EqualFold(trimmed, "y") || strings.EqualFold(trimmed, "yes"):
			if !gitAvailable(deps) {
				_, _ = fmt.Fprintln(out, "git is unavailable; continuing without a repository.")
				return false, nil
			}
			return true, nil
		case strings.EqualFold(trimmed, "n") || strings.EqualFold(trimmed, "no"):
			return false, nil
		case strings.EqualFold(trimmed, "q") || strings.EqualFold(trimmed, "quit") || strings.EqualFold(trimmed, "cancel"):
			return false, ErrCanceled
		}
		if rerr == io.EOF {
			return false, ErrCanceled
		}
		_, _ = fmt.Fprintln(out, "Please answer y or n.")
	}
}

// promptIgnore asks whether to keep project runtime state out of Git. The
// default is yes; EOF before an answer cancels rather than authorizing a
// .gitignore edit. q/quit/cancel abort the whole flow.
func promptIgnore(reader *bufio.Reader, out io.Writer) (bool, error) {
	_, _ = fmt.Fprintln(out, "ohgo stores local runtime state in .ohgo/.")
	for {
		_, _ = fmt.Fprint(out, "Add .ohgo/ to .gitignore? [Y/n/q] ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return false, fmt.Errorf("startup: read ignore answer: %w", rerr)
		}
		if line == "" && rerr == io.EOF {
			return false, ErrCanceled
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.EqualFold(trimmed, "y") || strings.EqualFold(trimmed, "yes"):
			return true, nil
		case strings.EqualFold(trimmed, "n") || strings.EqualFold(trimmed, "no"):
			return false, nil
		case strings.EqualFold(trimmed, "q") || strings.EqualFold(trimmed, "quit") || strings.EqualFold(trimmed, "cancel"):
			return false, ErrCanceled
		}
		if rerr == io.EOF {
			return false, ErrCanceled
		}
		_, _ = fmt.Fprintln(out, "Please answer y or n.")
	}
}

// promptValue asks one question with an editable default. Empty input keeps
// the default; EOF without a newline cancels initialization rather than
// minting identity from a closed pipe.
func promptValue(reader *bufio.Reader, out io.Writer, question, label, def string) (string, error) {
	_, _ = fmt.Fprintln(out, question)
	_, _ = fmt.Fprintf(out, "\n%s › %s", label, def)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("startup: read %s: %w", label, err)
	}
	if line == "" && err == io.EOF {
		return "", ErrCanceled
	}
	if trimmed := strings.TrimSpace(line); trimmed != "" {
		return trimmed, nil
	}
	return strings.TrimSpace(def), nil
}

func gitAvailable(deps Deps) bool {
	if deps.GitAvailable != nil {
		return deps.GitAvailable()
	}
	return GitAvailable()
}

// GitAvailable reports whether the git binary resolves. The TUI wizard
// checks it before honoring a "yes", exactly like the stdin gate, so both
// paths degrade to "continuing without a repository" together.
func GitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// InitGit initializes a new repository in dir with HEAD on main. It creates no
// commits, branches, or staging. It is the commit step the TUI wizard calls
// after collecting its decisions, sharing the stdin gate's implementation so
// the two paths can never drift.
func InitGit(dir string) error {
	return vcs.Init(dir, vcs.DefaultBranch)
}

// gitInit runs `git init -b main` without creating commits, branches, or
// staging.
func gitInit(dir string) error {
	return vcs.Init(dir, vcs.DefaultBranch)
}
