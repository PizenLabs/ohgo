package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/tui"
	"github.com/PizenLabs/ohgo/internal/update"
)

// updateCheckTimeout bounds the background refresh. It is short because the
// check is optional: a slow or unreachable endpoint must never hold a resource
// the process needs.
const updateCheckTimeout = 20 * time.Second

// updateChecksEnabled resolves the automatic-check switch.
//
// Precedence is environment, then configuration, then the built-in default
// (on). OHGO_UPDATE_CHECK=off is the deterministic CI switch; a non-empty CI
// environment disables the background check as well, because a pipeline has no
// one to notify and no reason to spend a request. The explicit `ohgo update`
// command is intentionally not governed by this switch.
func updateChecksEnabled(settings config.Settings, getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(getenv("OHGO_UPDATE_CHECK"))) {
	case "off", "0", "false", "no", "disabled":
		return false
	case "on", "1", "true", "yes", "enabled":
		return true
	}
	if strings.TrimSpace(getenv("CI")) != "" {
		return false
	}
	if settings.Update != nil && settings.Update.Enabled != nil {
		return *settings.Update.Enabled
	}
	return true
}

// startUpdateObserver reports a cached newer release and, when allowed,
// refreshes the cache once in the background.
//
// publish, when non-nil, receives a cached observation immediately and a
// refreshed observation from the background goroutine. It is the full-screen
// UI's thread-safe header hook. A plain REPL passes nil and uses the returned
// function instead, so nothing is ever written while the user is typing.
//
// The returned function, when non-nil, reads the local cache and prints a
// single hint per release. It performs no network I/O and is safe to call
// before every prompt.
func startUpdateObserver(settings config.Settings, current string, publish func(string)) func(io.Writer) {
	if !isTerminal(os.Stdout) {
		// Pipes, CI logs, and machine-readable output never receive an
		// update line.
		return nil
	}
	if !updateChecksEnabled(settings, os.Getenv) {
		return nil
	}
	svc := update.NewService(update.Config{CurrentVersion: current})
	status := svc.Observe()

	var announced string
	record := func(observation update.Status, out io.Writer) {
		if !observation.Available || observation.Reason != update.ReasonAvailable {
			return
		}
		if observation.LatestVersion == "" || observation.LatestVersion == announced {
			return
		}
		announced = observation.LatestVersion
		if publish != nil {
			publish(observation.LatestVersion)
			return
		}
		if out != nil {
			announceUpdate(out, current, observation.LatestVersion)
		}
	}
	if publish != nil {
		// The full-screen UI owns its own event channel; a cached observation
		// reaches it before the first frame.
		record(status, nil)
	}
	alert := func(out io.Writer) {
		record(svc.Observe(), out)
	}
	if status.Reason == update.ReasonDevelopment || !status.PlatformSupported {
		return alert
	}
	if status.Cached && !status.Stale {
		return alert
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()
		fresh, err := svc.Check(ctx)
		if err != nil || publish == nil {
			// Without a UI callback the result is simply persisted: the next
			// prompt (or the next launch) reads it locally.
			return
		}
		record(fresh, nil)
	}()
	return alert
}

// announceUpdate prints the plain-REPL notification. It is one line plus the
// explicit command, so it stays subordinate to the user's actual work.
func announceUpdate(out io.Writer, current, latest string) {
	_, _ = fmt.Fprintf(out, "A newer ohgo release is available: %s → %s\n",
		displayVersion(current), displayVersion(latest))
	_, _ = fmt.Fprintln(out, "Run `ohgo update` to install.")
}

// installUpdateLifecycle wires the presentation surfaces to the one update
// authority: /update, the informational observer, and the pending post-update
// acknowledgement. It never installs anything during startup; only a human
// invoking /update crosses that boundary.
func installUpdateLifecycle(program *tui.Program, settings config.Settings, current string) {
	svc := update.NewService(update.Config{
		CurrentVersion: current,
		Progress: func(stage update.Stage) {
			program.PublishUpdateProgress(string(stage))
		},
	})
	program.SetUpdateFunc(svc.Update)
	startUpdateObserver(settings, current, func(v string) {
		program.PublishUpdateAvailable(v)
	})
	showPendingPostUpdate(program, current)
}

// showPendingPostUpdate presents the one-shot What's New card only when the
// persisted transition says an explicit update installed the running version
// and its notice is still pending.
//
// Everything here is local: the transition record and the changelog embedded
// in this binary. Release discovery is a separate concern and is never called
// on this path.
func showPendingPostUpdate(program *tui.Program, current string) {
	store, err := update.NewTransitionStore()
	if err != nil {
		return
	}
	notice, acknowledge, ok := pendingPostUpdate(store, current, update.LoadChangelog)
	if !ok {
		return
	}
	program.ShowPostUpdateNotice(notice, acknowledge)
}

// pendingPostUpdate is the testable decision seam: the transition record
// authorizes the card, the local changelog only supplies its content.
func pendingPostUpdate(store *update.TransitionStore, current string, load func() (update.Changelog, error)) (tui.PostUpdateView, func() error, bool) {
	if store == nil {
		return tui.PostUpdateView{}, nil, false
	}
	transition, ok := store.Pending(current)
	if !ok {
		return tui.PostUpdateView{}, nil, false
	}
	notice := tui.PostUpdateView{Version: displayVersion(transition.To)}
	// Missing or malformed release notes must not corrupt update state or
	// block the acknowledgement: the card still truthfully reports the
	// version and says no notes are available.
	if load != nil {
		if changelog, err := load(); err == nil {
			if release, found := changelog.Release(transition.To); found {
				notice.Title = strings.TrimSpace(release.Title)
				for _, change := range release.Changes {
					notice.Changes = append(notice.Changes, tui.PostUpdateChange{
						Type:  string(change.Type),
						Title: strings.TrimSpace(change.Title),
					})
				}
			}
		}
	}
	acknowledge := func() error {
		_, err := store.Acknowledge(current)
		return err
	}
	return notice, acknowledge, true
}
