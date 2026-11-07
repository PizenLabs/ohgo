package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/spf13/cobra"
)

// cleanCmd prunes old unprotected sessions from the machine-global store.
//
// It is the explicit counterpart to the startup pass: protection tags still
// win, and an explicit keep count overrides the persistent preference for this
// invocation only.
func cleanCmd() *cobra.Command {
	var keep int
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Prune old unprotected sessions",
		Long: `Permanently delete unprotected sessions beyond the configured retention count.

Protected sessions are never pruned. The default keep count comes from
~/.ohgo/settings.json (10 when unset); --keep overrides it for one run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep <= 0 {
				settings, err := config.Load("")
				if err != nil && !errors.Is(err, config.ErrNotFound) {
					return fmt.Errorf("clean: read settings: %w", err)
				}
				keep = settings.KeepRecentSessions()
			}
			path, err := session.DefaultPath()
			if err != nil {
				return err
			}
			store, err := session.Open(path)
			if err != nil {
				return err
			}
			// A close failure cannot undo the prune, but it can hide a failed
			// checkpoint or flush, so report it instead of discarding it.
			defer func() {
				if err := store.Close(); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "ohgo: clean: close session store: %v\n", err)
				}
			}()

			result, err := store.PruneSessions(keep)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "pruned %d session(s); kept %d recent unprotected, preserved %d protected\n",
				result.DeletedCount(), result.Kept, result.Protected)
			return nil
		},
	}
	cmd.Flags().IntVar(&keep, "keep", 0, "unprotected sessions to retain (default: configured value, or 10)")
	return cmd
}

// pruneOnStartup applies the configured retention pass before an interactive
// session begins. activeID is exempt from both the count and deletion: the
// conversation this process is about to load must never be housekept away.
//
// A failure is reported to stderr and never prevents the session from running;
// retention is housekeeping, not a startup precondition.
func pruneOnStartup(store *session.Store, settings config.Settings, activeID int64) {
	if store == nil || !settings.AutoPruneEnabled() {
		return
	}
	result, err := store.PruneSessionsExcept(settings.KeepRecentSessions(), activeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: session cleanup: %v\n", err)
		return
	}
	if result.DeletedCount() > 0 {
		fmt.Fprintf(os.Stderr, "ohgo: pruned %d old session(s)\n", result.DeletedCount())
	}
}
