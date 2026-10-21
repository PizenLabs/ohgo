// Command ohgo is a coding agent CLI.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/session"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: %v\n", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "ohgo",
		Short:   "A coding agent with the IDE wired in",
		Long:    "ohgo is a coding agent that reads, edits, and runs code in your terminal.",
		Version: version,
		// Running without a subcommand starts the interactive session.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSession(cmd, args)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringP("model", "m", "", "model as provider/id, e.g. anthropic/claude-sonnet-4-5")
	root.PersistentFlags().String("workspace", "", "execution authority: ask (read-only), fuzz (execute, no mutation), or build (mutation); default build")
	root.PersistentFlags().Int64("resume", 0, "resume a saved session by id")
	root.PersistentFlags().Bool("no-tui", false, "use the line REPL even on a terminal")
	root.PersistentFlags().Bool("plain", false, "plain output: no colour, no full-screen UI")
	root.PersistentFlags().StringP("dir", "d", "", "working directory")
	root.PersistentFlags().Bool("no-tools", false, "run without any tools")
	root.PersistentFlags().BoolP("yes", "y", false, "auto-approve every tool call")
	root.PersistentFlags().Bool("reasoning", false, "enable extended thinking")
	root.PersistentFlags().String("effort", "", "reasoning effort: minimal, low, medium, high")
	root.PersistentFlags().Int("max-depth", 1, "subagent nesting depth, counting this agent (1 disables delegation)")
	root.PersistentFlags().Int("max-turns", agent.DefaultMaxTurns, "maximum model turns per run (0 uses the default)")
	root.PersistentFlags().Duration("deadline", 0, "wall-clock budget for the whole run, e.g. 10m (0 = no limit)")
	root.PersistentFlags().String("mouse", "", "wheel handling in the full-screen UI: select (default, keeps drag-copy) or report (works inside tmux, needs Shift/Option to select)")
	root.PersistentFlags().Bool("live", false, "output-first UI: ohgo writes the transcript and the terminal owns scrolling, selection and copy")

	root.AddCommand(versionCmd())
	root.AddCommand(modelsCmd())
	root.AddCommand(runCmd())
	root.AddCommand(sessionsCmd())
	root.AddCommand(doctorCmd())
	root.AddCommand(initCmd())
	root.AddCommand(docsCmd())
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ohgo %s\n", version)
			return nil
		},
	}
}

// version is overridden at build time.
var version = "0.1.0-dev"

func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models [provider]",
		Short: "List the bundled models",
		Long: `List the bundled models, optionally filtered to one provider.

With no argument every model in the catalog is listed, which is over two hundred
lines; pass a provider name to narrow it, e.g. "ohgo models groq".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			// An unknown provider is an error rather than an empty list: an
			// empty list would read as "that provider has no models", which is
			// not what the user asked about.
			if len(args) == 1 {
				models, err := catalog.ListForProvider(args[0])
				if err != nil {
					return fmt.Errorf("%w\navailable providers: %s",
						err, strings.Join(catalog.ProviderNames(), ", "))
				}
				for _, model := range models {
					_, _ = fmt.Fprintf(out, "%-40s %8s ctx  %s\n",
						model.ID, formatInt(model.ContextWindow), model.API)
				}
				return nil
			}

			for _, model := range catalog.List() {
				_, _ = fmt.Fprintf(out, "%-38s %-10s %8s ctx  %s\n",
					model.ID, model.Provider, formatInt(model.ContextWindow), model.API)
			}
			return nil
		},
	}
}

func formatInt(value int) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%dk", value/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

// sessionsCmd lists stored sessions and exposes transcript inspection.
func sessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Inspect saved sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := session.DefaultPath()
			if err != nil {
				return err
			}
			store, err := session.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			list, err := store.List(50)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				_, _ = fmt.Fprintln(out, "No sessions yet.")
				return nil
			}
			for _, sess := range list {
				_, _ = fmt.Fprintf(out, "%-6d %-20s %-28s %s\n",
					sess.ID, sess.UpdatedAt.Format("2006-01-02 15:04"), sess.Model, sess.Cwd)
			}
			return nil
		},
	}
	cmd.AddCommand(sessionShowCmd())
	return cmd
}

// sessionShowCmd prints one stored transcript.
func sessionShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Print a saved session's transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid session id %q", args[0])
			}
			path, err := session.DefaultPath()
			if err != nil {
				return err
			}
			store, err := session.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// Resolve the session first: Messages on an unknown id returns an
			// empty list rather than failing, which would make a typo look like
			// an empty session instead of a missing one.
			if _, err := store.Get(id); err != nil {
				return fmt.Errorf("session %d: %w", id, err)
			}

			messages, err := store.Messages(id, 0)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(messages) == 0 {
				_, _ = fmt.Fprintf(out, "Session %d is empty.\n", id)
				return nil
			}
			for _, msg := range messages {
				_, _ = fmt.Fprintf(out, "%-11s %s\n", msg.Role+":", msg.Text)
			}
			return nil
		},
	}
}
