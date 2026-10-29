package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/history"
	"github.com/PizenLabs/ohgo/startup"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/workspace"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// runCmd handles `ohgo run` for one-shot execution.
func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run [prompt]",
		Short: "Run one prompt and exit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnce(cmd, args[0])
		},
	}
}

// bootProject resolves the project identity for a one-shot run without ever
// creating project state.
//
// It is the read-only counterpart to startup.Ensure: a command that cannot
// show the initialization gate must not initialize silently, so an
// uninitialized directory is a fact to report, not a directory to create.
func bootProject(root string) (string, error) {
	d := startup.Discover(root)
	if !d.ProjectFound {
		return "", fmt.Errorf(
			"project not initialized in %s: run `ohgo` there to complete initialization", root)
	}
	return d.Project.ID, nil
}

// runOnce executes a single prompt.
func runOnce(cmd *cobra.Command, text string) error {
	opts, registry, err := resolveOptions(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// A one-shot run cannot satisfy the initialization gate: it never
	// prompts. Discovery is therefore read-only and an uninitialized project
	// is reported rather than created — creating .ohgo/ here would initialize
	// as a side effect of running, exactly the bypass startup exists to
	// prevent.
	projectID, err := bootProject(opts.root)
	if err != nil {
		return err
	}
	opts.projectID = projectID

	resumeID, _ := cmd.Flags().GetInt64("resume")
	store, sess, closeStore, err := openSession(opts, resumeID)
	if err != nil {
		return err
	}
	defer closeStore()

	if resumeID > 0 {
		if err := applySessionAuthority(cmd, &opts, dynamicResolver(context.Background(), registry), store, sess); err != nil {
			return err
		}
	}

	// The one-shot path routes the same way an interactive turn does: a
	// request the active workspace may not answer is refused before the model
	// is called, never handed to it to attempt (§3, §11).
	route := workspace.RouteTurn(opts.authority(), text)
	if !route.Allowed() {
		outcome := refuseHeadless(out, route)
		return &headlessExit{outcome: outcome}
	}
	var history []agent.Message
	if sess != nil && resumeID > 0 {
		history, err = loadHistory(store, sess)
		if err != nil {
			return err
		}
	}

	loop, err := buildLoop(opts, registry, out, store, sess, nil)
	if err != nil {
		return err
	}
	defer opts.closeMCP()
	// A one-shot command is interruptible like an interactive turn. The
	// cancellation path is not a new orchestration layer: it makes the exit
	// status honest when the process is stopped.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	taskID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	if sess != nil {
		taskID = fmt.Sprintf("sess-%d", sess.ID)
	}
	grySession := attachGRYEvidence(ctx, opts.root, taskID, text, loop)
	defer finishGRYEvidence(grySession)

	outcome, _ := runHeadlessTurnAs(ctx, loop, opts.root, out, text, history, route.Active.String())
	if outcome.State == runCompleted {
		return nil
	}
	return &headlessExit{outcome: outcome}
}

// useInteractiveUI reports whether the full-screen UI should be used.
//
// It requires a TTY on both stdin and stdout, plus the absence of --no-tui or
// --plain. Detection reads the process's own descriptors, so it is a function
// of the environment rather than a flag a test could leave set.
func useInteractiveUI(cmd *cobra.Command) bool {
	if disabled, _ := cmd.Flags().GetBool("no-tui"); disabled {
		return false
	}
	if plain, _ := cmd.Flags().GetBool("plain"); plain {
		return false
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// isTerminal reports whether a file is a real terminal.
//
// The check is an ioctl, not a file-mode test: /dev/null is a character device
// and would otherwise count, which would start the full-screen UI with its
// output discarded.
func isTerminal(file *os.File) bool {
	return term.IsTerminal(int(file.Fd()))
}

// runSession starts the interactive REPL.
func runSession(cmd *cobra.Command, args []string) error {
	opts, registry, err := resolveOptions(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// The full-screen UI owns onboarding for an uninitialized project: raw
	// stdin prompts would scroll the terminal and then jump into the
	// alt-screen. The wizard collects the same setup decisions in-canvas
	// and commits through the same seams. Every other path keeps the
	// stdin gate below, which fast-paths initialized projects either way.
	if useInteractiveUI(cmd) && startup.NeedsInit(startup.Discover(opts.root)) {
		return runSessionWizard(cmd, opts, registry, out)
	}

	// Startup owns project initialization, Git detection, and local
	// identity. It runs before the runtime so the identity surface below is
	// truthful, and it fast-paths when .ohgo/project.yml already exists.
	//
	// A cancel at the initialization gate is a clean exit, not an error: the
	// user chose not to initialize, and nothing was created.
	boot, err := startup.Ensure(opts.root, startup.Deps{
		Stdin:  os.Stdin,
		Stdout: out,
	})
	if errors.Is(err, startup.ErrCanceled) {
		_, _ = fmt.Fprintln(out, "initialization canceled; nothing was created.")
		return nil
	}
	if err != nil {
		return err
	}
	opts.settings = boot.Settings
	opts.projectID = boot.Project.ID

	resumeID, _ := cmd.Flags().GetInt64("resume")
	store, sess, closeStore, err := openSession(opts, resumeID)
	if err != nil {
		return err
	}
	defer closeStore()

	if resumeID > 0 {
		if err := applySessionAuthority(cmd, &opts, dynamicResolver(context.Background(), registry), store, sess); err != nil {
			return err
		}
	}

	// The gate is built here, before the loop, because the full-screen UI
	// installs its approval callback after the program exists. The plain REPL
	// keeps the stdin approver as the default.
	opts.security = opts.newSecurityGate(func(call ai.ToolCall, approval agent.Approval) (bool, string) {
		return approveToolCall(out, opts, call, approval)
	})
	loop, err := buildLoop(opts, registry, out, store, sess, nil)
	if err != nil {
		return err
	}
	defer opts.closeMCP()

	// One ephemeral surface serves both interfaces. It is created here, not in
	// buildLoop, because it owns its own SUP-role runtime and is deliberately
	// absent from the session's persistence path.
	surface := newSupportSurface(supportBuilder(opts, registry))

	// The full-screen UI needs the loop before it can supply its approval
	// hook, so the routing happens here rather than inside buildLoop. A pipe or
	// a test never reaches this branch and keeps plain line output.
	if useInteractiveUI(cmd) {
		// The header effort badge shows the active reasoning level, or hides
		// when thinking is disabled: a default install (reasoning off) keeps
		// the legacy header rather than inventing a level.
		initialEffort := ""
		if opts.reasoning {
			initialEffort = opts.effort
		}
		programOpts := []tui.ProgramOption{tui.WithVersion(version), tui.WithEffort(initialEffort), tui.WithPromptHistory(history.Load(opts.root))}
		// Precedence: flag, then settings, then the environment default. The
		// environment matters because the wheel reaches the program by
		// different routes in different terminals, and a multiplexer that does
		// not forward alternate scroll needs mouse reporting to deliver it at
		// all — see tui.MouseDefaultFor for the measurement behind that.
		// An unrecognised value degrades to select so a typo never silently
		// costs the user drag-copy.
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
		program := tui.NewProgram(loop, opts.model.Provider+"/"+opts.model.ID, opts.model.ContextWindow, programOpts...)
		// /update is a presentation surface over the same authority the CLI
		// command uses. Progress lands in header chrome while the operation
		// runs off the update goroutine; nothing installs until the human
		// invokes /update. A pending post-update transition is presented here
		// as a one-shot local acknowledgement.
		installUpdateLifecycle(program, boot.Settings, version)
		// The recovery store is opened before the UI runs; /undo and /redo
		// move through the same session timeline the loop records into.
		recoverer := openRecoveryService(opts.root)
		defer func() { _ = recoverer.Close() }()
		program.SetRecovery(recoverer)
		program.SetCommitHooks(commitHooks(opts.root, &opts.settings, registry))
		attachShellEscape(program, opts.root, opts.security)
		controller := installTaskContinuity(program, opts.root, loop.Tools(), opts.budget)
		program.SetCompletionContext(opts.root, structuralResolver(loop.Tools()))
		// A grant is bound to the task it was approved for, when the task layer
		// knows one. The task remains authoritative for continuity; the gate
		// only reads its identity.
		opts.security.SetTaskSource(func() string {
			if current, ok := controller.Current(); ok {
				return current.ID
			}
			return ""
		})

		// Output-first mode is a different interaction boundary, not a
		// different program: the same loop, the same state machine, the same
		// approval and command paths. The terminal scrolls, selects and
		// copies; ohgo writes the transcript and keeps the composer. It
		// rejects mouse reporting outright, so the mouse policy above is
		// ignored there.
		if live, _ := cmd.Flags().GetBool("live"); live {
			return program.RunLive()
		}

		// One harness serves the UI's commands; the REPL builds its own
		// further down from the same pieces, so both agree.
		held := newHarness(registry, loop, store, sess, &agent.UsageLog{}, &opts.settings, opts.root, opts.model)
		held.mcp = opts.mcpRuntime
		held.usage.SetCost(opts.model.Cost)
		held.workspace = opts.authority().Label()
		held.gate = opts.gate
		held.security = opts.security
		held.projectID = opts.projectID
		held.support = surface
		program.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
			return surface.ask(ctx, question, onDelta)
		})
		program.SetSupportInfo(held.SupportInfo)
		program.SetMentionLister(mentionLister(opts.root))
		program.SetMentionReader(mentionReader(opts.root))

		// The header effort badge follows the session role's effective level.
		// The reporter reads the configuration authority (role effort, then
		// the global default) through the same reasoning mapping the loop
		// uses, so the badge and the request agree. Disabled thinking
		// reports "" and the badge hides.
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

		// A resumed session replays its transcript, so the UI continues rather
		// than restarts. Loading it here means a failure is fatal rather than a
		// silently empty session.
		var history []agent.Message
		if sess != nil && resumeID > 0 {
			history, err = loadHistory(store, sess)
			if err != nil {
				return err
			}
		}
		if store != nil && sess != nil {
			program.AttachSession(store, sess, history)
		}

		program.SetModelSwitcher(
			func(id string) error {
				// The mid-session switcher shares the catalog's resolver, so a
				// model offered by `ohgo models` is guaranteed to be switchable
				// and a typo gets the same message either way.
				_, err := held.SwitchModel(context.Background(), id)
				return err
			},
			func() string {
				return held.model.Provider + "/" + held.model.ID
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
				ids = append(ids, model.Provider+"/"+model.ID)
			}
			return ids
		})
		program.SetCatalogHooks(tui.CatalogHooks{
			Snapshot: func() tui.CatalogSnapshot { return catalogSnapshot() },
			Refresh: func(ctx context.Context, seq int) tea.Cmd {
				return refreshModelCatalog(ctx, held, seq)
			},
		})
		// The model control plane: the provider pane, the role assignments,
		// and the one seam /model writes through. The program owns none of
		// this — it renders what the harness reports and hands edits back.
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
		// Workspace commands route through the harness seam: the program never
		// owns authority, it asks the owner to move it at a turn boundary.
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
				// A workspace move rebinds the session's model and effort:
				// the header follows the newly effective role rather than
				// naming a model the loop no longer calls.
				program.PublishModel(held.model.Provider + "/" + held.model.ID)
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
			HasSession:      sess != nil,
			Session:         sessionLabel(sess),
			Workspace:       style.Label,
			WorkspaceAccent: style.Accent,
			HasWorkspace:    true,
		})
		// The runtime identity surface shows who, where, and what state.
		// All values come from startup's observed state, never invented.
		branch, gitStatus := boot.Git.BranchStatus()
		program.PublishIdentity(tui.IdentityUpdated{
			User:      boot.Project.WorkerName(),
			Greeting:  boot.Greeting,
			GitBranch: branch,
			GitStatus: gitStatus,
			// The measured counts let /status name which kind of change is
			// present; Known marks them real so an unknown status never
			// renders as a clean tree.
			GitStaged:    boot.Git.Summary.Staged,
			GitModified:  boot.Git.Summary.Unstaged,
			GitUntracked: boot.Git.Summary.Untracked,
			GitKnown:     boot.Git.Known,
			Ready:        "ready",
		})

		if !opts.autoApprove {
			// The trusted approval interaction belongs to the program that is
			// actually running. Wiring it anywhere else deadlocks: the tool
			// would block on an answer channel no UI is reading. The policy
			// still runs for every call; this only supplies the human answer
			// for an APPROVE decision.
			opts.security.SetAsk(program.ApprovalFunc())
		}
		// The program owns turn output and persistence here (transcript via
		// events, store via runTurn): the loop's hooks must stay silent, or
		// every turn prints raw text over the alt-screen and persists twice.
		loop.SetTurnHook(nil)
		loop.SetCompactHook(func(result agent.CompactResult) {
			program.PublishNotice(fmt.Sprintf("context compacted: %d messages summarised, %d retained",
				result.Compacted, result.Retained))
			// Compaction rebuilds context; the checkpoint must survive it
			// unchanged. Syncing re-records the same task state, so a crash
			// between compaction and the next turn resumes the same task.
			program.SyncTask()
		})
		return program.Run()
	}

	_, _ = fmt.Fprintf(out, "ohgo %s · %s/%s · %s · %s\n", version, opts.model.Provider, opts.model.ID, opts.authority().Label(), opts.root)
	if boot.Greeting != "" {
		_, _ = fmt.Fprintln(out, boot.Greeting)
	}
	_, _ = fmt.Fprintln(out, "Type your request, or /quit to exit.")
	// The plain REPL reads a terminal, so a cached update hint is safe to
	// print once here. A background refresh is persisted and picked up at the
	// next prompt boundary, never while the user is typing.
	updateAlert := startUpdateObserver(boot.Settings, version, nil)

	var history []agent.Message
	if sess != nil && resumeID > 0 {
		history, err = loadHistory(store, sess)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "resumed session %d (%d messages)\n", sess.ID, len(history))
	}

	plain := newHarness(registry, loop, store, sess, &agent.UsageLog{}, &opts.settings, opts.root, opts.model)
	plain.mcp = opts.mcpRuntime
	plain.rec = openRecoveryService(opts.root)
	defer func() { _ = plain.rec.Close() }()
	plain.usage.SetCost(opts.model.Cost)
	plain.workspace = opts.authority().Label()
	plain.gate = opts.gate
	plain.security = opts.security
	plain.projectID = opts.projectID
	plain.support = surface
	plain.updateNotice = updateAlert
	// A plain-REPL prompt is one run, exactly like a TUI turn: it starts a
	// fresh root grant, while the holder keeps every spawner pointed at the
	// grant the loop is currently enforcing.
	plain.newBudget = func() *budget.Budget {
		grant := opts.newBudget()
		opts.budget.Set(grant)
		return grant
	}
	return repl(plain, out, os.Stdin, history)
}
