package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/history"
	"github.com/PizenLabs/ohgo/internal/project"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/startup"
	"github.com/PizenLabs/ohgo/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

// runSessionWizard starts the full-screen UI with in-canvas onboarding.
//
// It is the TUI counterpart to startup.Ensure: the same four decisions in
// the same order (Git, runtime-state ignore, worker, project), collected by an
// InitWizardModel instead of stdin prompts, and committed through the same
// seams (startup.InitGit, startup.EnsureGitignore, project.EnsureWith).
// Nothing on disk changes before Complete runs, so quitting the wizard leaves
// nothing created behind, exactly like cancelling the stdin gate.
//
// The terminal is entered once: the wizard renders between the header and
// footer of the same Program that serves the session, and completion drops
// the modal onto the idle canvas without clearing or re-initializing the
// screen. No os.Stdin read happens on this path.
func runSessionWizard(cmd *cobra.Command, opts options, registry *ai.Registry, out io.Writer) error {
	d := startup.Discover(opts.root)

	// The store opens before the project identity exists; the session is
	// created in Complete, once the identity it belongs to is committed.
	store, closeStore, err := openStore()
	if err != nil {
		return err
	}
	defer closeStore()

	// The gate is created before the loop so the wizard's program can install
	// its approval callback once it exists.
	opts.security = opts.newSecurityGate(nil)
	loop, err := buildLoop(opts, registry, out, store, nil, nil)
	if err != nil {
		return err
	}
	defer opts.closeMCP()

	surface := newSupportSurface(supportBuilder(opts, registry))

	initialEffort := ""
	if opts.reasoning {
		initialEffort = opts.effort
	}
	programOpts := []tui.ProgramOption{tui.WithVersion(version), tui.WithEffort(initialEffort), tui.WithPromptHistory(history.Load(opts.root))}
	mouse := opts.settings.Mouse
	if flag := flagValue(cmd, "mouse"); flag != "" {
		mouse = flag
	}
	if mouse == "" {
		mouse = tui.MouseDefaultFor(os.Getenv("TMUX"))
	}
	if tui.NormalizeMouse(mouse) == tui.MouseReport {
		programOpts = append(programOpts, tui.WithMouseReporting())
	}
	program := tui.NewProgram(loop, opts.model.Ref(), opts.model.ContextWindow, programOpts...)
	recoverer := openRecoveryService(opts.root)
	defer func() { _ = recoverer.Close() }()
	program.SetRecovery(recoverer)
	program.SetCommitHooks(commitHooks(opts.root, &opts.settings, registry))
	attachShellEscape(program, opts.root, opts.security)
	controller := installTaskContinuity(program, opts.root, loop.Tools(), opts.budget)
	program.SetCompletionContext(opts.root, structuralResolver(loop.Tools()))
	opts.security.SetTaskSource(func() string {
		if current, ok := controller.Current(); ok {
			return current.ID
		}
		return ""
	})

	if live, _ := cmd.Flags().GetBool("live"); live {
		return program.RunLive()
	}

	held := newHarness(registry, loop, store, nil, &agent.UsageLog{}, &opts.settings, opts.root, opts.model)
	held.mcp = opts.mcpRuntime
	held.rec = recoverer
	held.usage.SetCost(opts.model.Cost)
	held.workspace = opts.authority().Label()
	held.gate = opts.gate
	held.security = opts.security
	held.support = surface
	program.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return surface.ask(ctx, question, onDelta)
	})
	program.SetSupportInfo(held.SupportInfo)
	program.SetMentionLister(mentionLister(opts.root))
	program.SetMentionReader(mentionReader(opts.root))

	program.SetEffortProvider(func() string {
		role := held.sessionRole()
		if role == "" {
			role = strings.ToLower(opts.authority().String())
		}
		enabled, level := held.reasoningFor(held.roleEffortFor(role))
		if !enabled {
			return ""
		}
		return level
	})

	program.SetModelSwitcher(
		func(id string) error {
			_, err := held.SwitchModel(context.Background(), id)
			return err
		},
		func() string {
			return held.model.Ref()
		},
	)
	program.SetUsageLog(held.usage)
	program.SetCompactor(func(ctx context.Context, history []agent.Message) ([]agent.Message, string, error) {
		return held.CompactHistory(ctx, history)
	})
	program.SetDoctorFunc(held.DoctorLines)
	program.SetMCPHooks(mcpHooks(opts))
	program.SetProvidersFunc(held.ProviderRows)
	program.SetProbeFunc(func(ctx context.Context) tui.ProbeFinished {
		return held.ProbeCurrent(ctx)
	})
	program.SetCredentialDeleter(func(provider string) (string, error) {
		return held.Disconnect(provider)
	})
	program.SetConnectHooks(connectHooks(held))
	program.SetModelLister(func() []string {
		var ids []string
		for _, model := range catalog.List() {
			ids = append(ids, model.Ref())
		}
		return ids
	})
	program.SetCatalogHooks(tui.CatalogHooks{
		Snapshot: func() tui.CatalogSnapshot { return catalogSnapshot() },
		Refresh: func(ctx context.Context, seq int) tea.Cmd {
			return refreshModelCatalog(ctx, held, seq)
		},
	})
	program.SetModelControl(tui.ModelControlHooks{
		Providers: func() []tui.ProviderRow { return held.ProviderPaneRows() },
		Roles:     func() []tui.RoleAssignment { return held.RoleAssignments() },
		ActiveRole: func() string {
			return strings.ToLower(opts.authority().String())
		},
		Assign: func(update tui.RoleAssignment) (tui.RoleAssignment, []tui.RoleAssignment, error) {
			return held.AssignRole(update)
		},
		EffectiveWindow: func() int { return held.model.ContextWindow },
		SaveKey: func(provider, key string) error {
			return held.SavePlaneKey(provider, key)
		},
	})
	program.SetWorkspaceSwitcher(tui.WorkspaceHooks{
		Current: func() string { return held.workspace },
		Switch: func(name string) error {
			ws, err := held.SwitchWorkspace(name)
			if err != nil {
				return err
			}
			opts.setAuthority(ws)
			style := ws.Style()
			program.PublishWorkspace(tui.WorkspaceUpdated{
				Workspace:       style.Label,
				WorkspaceAccent: style.Accent,
				HasWorkspace:    true,
			})
			program.PublishModel(held.model.Ref())
			program.PublishEffort(func() string {
				role := held.sessionRole()
				if role == "" {
					role = strings.ToLower(ws.String())
				}
				enabled, level := held.reasoningFor(held.roleEffortFor(role))
				if !enabled {
					return ""
				}
				return level
			}())
			program.PublishNotice("switched to " + style.Label)
			return nil
		},
		PrepareTurn: func(text string) tui.TurnAuthority { return turnAuthority(held, text) },
		Predict:     func(text string) (tui.TurnTransition, bool) { return turnTransition(held, text) },
	})
	program.SetJobsLister(func() []tui.JobItem {
		for _, tool := range loop.Tools() {
			bash, ok := tool.(*tools.BashTool)
			if !ok {
				continue
			}
			var items []tui.JobItem
			for _, job := range bash.Jobs().Snapshot() {
				items = append(items, tui.JobItem{
					ID:       job.ID,
					Command:  job.Command,
					Running:  job.Running,
					ExitCode: job.ExitCode,
					Elapsed:  job.Elapsed.Round(time.Millisecond).String(),
					Tail:     job.OutputTail,
				})
			}
			return items
		}
		return nil
	})
	if store != nil {
		installSessionManager(program, store, held, opts)
	}
	program.SetTheme(opts.settings.Theme)
	program.SetThemeChangedHandler(func(theme string) {
		opts.settings.Theme = theme
		if err := config.Save(opts.settings); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not save theme: %v\n", err)
		}
	})
	style := opts.authority().Style()
	program.PublishWorkspace(tui.WorkspaceUpdated{
		Cwd:             opts.root,
		HasCwd:          true,
		Workspace:       style.Label,
		WorkspaceAccent: style.Accent,
		HasWorkspace:    true,
	})

	resumeID, _ := cmd.Flags().GetInt64("resume")
	program.SetInitWizard(
		tui.NewInitWizard(!d.HasGit, startup.NeedsIgnore(d), startup.WorkerSuggestion(d, opts.settings), startup.ProjectSuggestion(d)),
		tui.InitWizardHooks{Complete: wizardComplete(cmd, &opts, registry, d, store, held, program, resumeID)},
	)

	if !opts.autoApprove {
		opts.security.SetAsk(program.ApprovalFunc())
	}
	loop.SetTurnHook(nil)
	loop.SetCompactHook(func(result agent.CompactResult) {
		program.PublishNotice(fmt.Sprintf("context compacted: %d messages summarised, %d retained",
			result.Compacted, result.Retained))
		program.SyncTask()
	})
	return program.Run()
}

// wizardComplete commits one finished onboarding run. It collects the Git,
// ignore, worker, and project decisions first and mutates disk only
// afterwards, mirroring startup.Ensure: git init on main, then the optional
// .gitignore rule, then project.yml, then the session. It runs on the Bubble
// Tea update goroutine (see InitWizardHooks), so it attaches the session and
// publishes chrome directly. A failed commit returns an error and the wizard
// reopens its final step for retry.
func wizardComplete(cmd *cobra.Command, opts *options, registry *ai.Registry, d startup.Discovery, store *session.Store, held *harness, program *tui.Program, resumeID int64) func(tui.InitWizardDecision) (tui.IdentityUpdated, string, error) {
	return func(dec tui.InitWizardDecision) (tui.IdentityUpdated, string, error) {
		var notes []string
		if dec.WantGit {
			if !startup.GitAvailable() {
				notes = append(notes, "git is unavailable; continuing without a repository.")
			} else if err := startup.InitGit(d.Dir); err != nil {
				notes = append(notes, fmt.Sprintf("git init failed: %v; continuing without a repository.", err))
			} else {
				notes = append(notes, "Git initialized on main.")
			}
		}
		if dec.IgnoreOhgo {
			if _, err := startup.EnsureGitignore(d.Root); err != nil {
				notes = append(notes, fmt.Sprintf("Failed to update .gitignore: %v", err))
			} else if !startup.InspectGitignore(d.Root).IgnoresOhgo {
				notes = append(notes, "Failed to verify .gitignore: .ohgo/ is not ignored.")
			}
		}

		identity, err := project.EnsureWith(d.Dir, project.Metadata{Name: dec.Project, Worker: dec.Worker})
		if err != nil {
			return tui.IdentityUpdated{}, "", fmt.Errorf("commit project: %w", err)
		}
		held.projectID = identity.ID
		opts.projectID = identity.ID

		git := startup.DescribeGit(d.Dir)
		branch, gitStatus := git.BranchStatus()
		facts := tui.IdentityUpdated{
			User:         identity.WorkerName(),
			Greeting:     startup.Greeting(identity.WorkerName(), time.Now()),
			GitBranch:    branch,
			GitStatus:    gitStatus,
			GitStaged:    git.Summary.Staged,
			GitModified:  git.Summary.Unstaged,
			GitUntracked: git.Summary.Untracked,
			GitKnown:     git.Known,
			Ready:        "ready",
		}

		if store != nil && held.sess == nil {
			if serr := attachWizardSession(cmd, opts, registry, store, held, program, identity.ID, resumeID); serr != nil {
				return tui.IdentityUpdated{}, "", serr
			}
		}

		// Onboarding has finished, so the program is now the interactive
		// session /update belongs to. Wiring the lifecycle here keeps the
		// wizard from hiding a pending post-update acknowledgement behind its
		// own modal; the card opens after the dialog closes.
		installUpdateLifecycle(program, opts.settings, version)

		notice := "runtime ready"
		if name := identity.WorkerName(); name != "" {
			notice = "Welcome, " + name + " — runtime ready"
		}
		if len(notes) > 0 {
			notice = strings.Join(notes, "\n") + "\n" + notice
		}
		return facts, notice, nil
	}
}

// attachWizardSession creates (or resumes) the session for a newly
// onboarded project and wires it into the running program. The store
// predates the project identity, so this runs at commit time rather than
// before the UI starts.
func attachWizardSession(cmd *cobra.Command, opts *options, registry *ai.Registry, store *session.Store, held *harness, program *tui.Program, projectID string, resumeID int64) error {
	if resumeID > 0 {
		sess, err := store.Get(resumeID)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				return fmt.Errorf("%w: %d", session.ErrSessionNotFound, resumeID)
			}
			return err
		}
		history, err := loadHistory(store, &sess)
		if err != nil {
			return err
		}
		if err := applySessionAuthority(cmd, opts, dynamicResolver(context.Background(), registry), store, &sess); err != nil {
			return err
		}
		// The loop predates the authority restore, so it moves when the
		// session names a different model than the resolved one.
		if current := held.model.Ref(); current != opts.model.Ref() {
			if _, err := held.SwitchModel(context.Background(), opts.model.Ref()); err != nil {
				return fmt.Errorf("resume: session model: %w", err)
			}
		}
		held.workspace = opts.authority().Label()
		// The loop predates the authority restore, so its gate follows
		// the restored workspace.
		held.applyAuthority(opts.authority())
		sessCopy := sess
		held.sess = &sessCopy
		program.AttachSession(store, held.sess, history)
		program.PublishWorkspace(tui.WorkspaceUpdated{
			HasSession: true,
			Session:    sessionLabel(held.sess),
		})
		return nil
	}

	sess, err := store.CreateWith(session.CreateOptions{
		Cwd:       opts.root,
		Model:     opts.model.Ref(),
		Workspace: opts.authority().String(),
		Project:   projectID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil
	}
	sessCopy := sess
	held.sess = &sessCopy
	program.AttachSession(store, held.sess, nil)
	program.PublishWorkspace(tui.WorkspaceUpdated{
		HasSession: true,
		Session:    sessionLabel(held.sess),
	})
	return nil
}
