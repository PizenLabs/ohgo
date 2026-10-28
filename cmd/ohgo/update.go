package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/update"
	"github.com/spf13/cobra"
)

// updateService is the slice of the update authority the CLI presents.
//
// The command is a thin presenter over this interface so a test can exercise
// every user-visible path without a network, a release, or a real executable
// replacement.
type updateService interface {
	Check(ctx context.Context) (update.Status, error)
	Update(ctx context.Context) (update.Result, error)
}

// reportedExit ends the process with code after the command has already
// printed a complete user-facing explanation. main must not add a second
// "ohgo: ..." line to a refusal the user has already read.
type reportedExit struct{ code int }

func (e *reportedExit) Error() string { return "update reported" }

func updateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ohgo to the latest release",
		Long: `Update ohgo to the latest published release.

The update is explicit: normal ohgo invocations only observe that a newer
release exists and never download or install it. Installation downloads a
temporary artifact, verifies the published SHA-256, extracts the platform
binary, replaces the executable atomically, and confirms the new binary reports
the requested version before reporting success.

An installation managed by a package manager is never overwritten; the command
names the manager instead. Windows is not part of the published release matrix,
so self-update reports the platform as unsupported.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			var last update.Stage
			svc := update.NewService(update.Config{
				CurrentVersion: version,
				Progress: func(stage update.Stage) {
					if stage == last {
						return
					}
					last = stage
					_, _ = fmt.Fprintln(out, stageLine(stage))
				},
			})
			return executeUpdate(cmd, svc, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report availability without installing")
	return cmd
}

// executeUpdate renders one update operation.
func executeUpdate(cmd *cobra.Command, svc updateService, checkOnly bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// A whole update is bounded: a stalled endpoint must fail rather than
	// hold the command open forever.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out := cmd.OutOrStdout()

	if checkOnly {
		status, err := svc.Check(ctx)
		if err != nil {
			return fmt.Errorf("check for updates: %w", err)
		}
		printUpdateCheck(out, status)
		return nil
	}

	_, _ = fmt.Fprintf(out, "Current version: %s\n", displayVersion(version))
	result, err := svc.Update(ctx)
	if err != nil {
		return reportUpdateError(out, err)
	}
	if result.AlreadyCurrent {
		if result.Status.Reason == update.ReasonPrerelease {
			_, _ = fmt.Fprintf(out, "ohgo is already up to date (%s); the newest release is a prerelease.\n", displayVersion(version))
			return nil
		}
		_, _ = fmt.Fprintf(out, "ohgo is already up to date (%s).\n", displayVersion(version))
		return nil
	}
	_, _ = fmt.Fprintf(out, "Updated ohgo to %s.\n", displayVersion(result.InstalledVersion))
	return nil
}

func printUpdateCheck(out io.Writer, status update.Status) {
	switch {
	case !status.PlatformSupported || status.Reason == update.ReasonUnsupported:
		_, _ = fmt.Fprintf(out, "Self-update is not supported for this platform (%s/%s).\n", runtime.GOOS, runtime.GOARCH)
	case status.Reason == update.ReasonDevelopment:
		_, _ = fmt.Fprintf(out, "Current version: %s (development build)\n", displayVersion(status.CurrentVersion))
		_, _ = fmt.Fprintln(out, "Self-update requires a released version.")
	case status.Reason == update.ReasonPrerelease:
		_, _ = fmt.Fprintf(out, "Current version: %s\n", displayVersion(status.CurrentVersion))
		_, _ = fmt.Fprintf(out, "The newest release is a prerelease (%s).\n", displayVersion(status.LatestVersion))
		_, _ = fmt.Fprintln(out, "ohgo does not move a stable build onto a prerelease implicitly.")
	case !status.Available:
		_, _ = fmt.Fprintf(out, "Current version: %s\n", displayVersion(status.CurrentVersion))
		_, _ = fmt.Fprintf(out, "ohgo is already up to date (%s).\n", displayVersion(status.CurrentVersion))
	default:
		_, _ = fmt.Fprintf(out, "Current version: %s\n", displayVersion(status.CurrentVersion))
		_, _ = fmt.Fprintf(out, "Latest version:  %s\n\n", displayVersion(status.LatestVersion))
		_, _ = fmt.Fprintf(out, "A newer release is available (%s → %s).\n",
			displayVersion(status.CurrentVersion), displayVersion(status.LatestVersion))
		_, _ = fmt.Fprintln(out, "Run `ohgo update` to install.")
	}
}

// reportUpdateError turns the typed refusals into the exact guidance the user
// needs. Anything else keeps its cause chain and reaches main as a normal
// failure.
func reportUpdateError(out io.Writer, err error) error {
	var managed *update.ManagedError
	if errors.As(err, &managed) {
		_, _ = fmt.Fprintf(out, "This installation is managed by %s.\n", managed.Manager)
		if managed.Command != "" {
			_, _ = fmt.Fprintf(out, "Use `%s` to upgrade it.\n", managed.Command)
		} else {
			_, _ = fmt.Fprintln(out, "Use that package manager to upgrade it.")
		}
		return &reportedExit{code: 1}
	}
	switch {
	case errors.Is(err, update.ErrUnsupportedPlatform):
		_, _ = fmt.Fprintf(out, "Self-update is not supported for this platform (%s/%s).\n", runtime.GOOS, runtime.GOARCH)
		return &reportedExit{code: 1}
	case errors.Is(err, update.ErrDevelopmentVersion):
		_, _ = fmt.Fprintf(out, "Current build is a development version (%s); self-update requires a release build.\n", strings.TrimSpace(version))
		return &reportedExit{code: 1}
	case errors.Is(err, update.ErrUpdateBusy):
		_, _ = fmt.Fprintln(out, "Another ohgo update is already running.")
		return &reportedExit{code: 1}
	case errors.Is(err, update.ErrLockUnavailable):
		_, _ = fmt.Fprintln(out, "Cannot locate a safe location for the update lock; nothing was changed.")
		return &reportedExit{code: 1}
	}
	if strings.HasPrefix(err.Error(), "update:") {
		return err
	}
	return fmt.Errorf("update: %w", err)
}

func stageLine(stage update.Stage) string {
	switch stage {
	case update.StageResolving:
		return "Resolving release..."
	case update.StageDownloading:
		return "Downloading..."
	case update.StageVerifyingArtifact:
		return "Verifying checksum..."
	case update.StageExtracting:
		return "Extracting..."
	case update.StageInstalling:
		return "Installing..."
	case update.StageVerifyingInstallation:
		return "Verifying installation..."
	default:
		return string(stage)
	}
}

// displayVersion spells a version the way every other ohgo surface does: a
// leading "v" for a release, the raw spelling for a development build.
func displayVersion(raw string) string {
	if parsed, ok := update.ParseVersion(raw); ok {
		return parsed.Display()
	}
	return strings.TrimSpace(raw)
}
