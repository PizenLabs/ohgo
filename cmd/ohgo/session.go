package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/workspace"
	"github.com/spf13/cobra"
)

// persistTurn appends a completed turn to the session transcript.
//
// A write failure is reported but never fails the run: the conversation is
// still correct in memory, and aborting a working turn because a log insert
// failed would be the worse trade.
func persistTurn(store *session.Store, sess session.Session, turn agent.Turn) {
	assistant, err := session.FromAgentMessage(agent.AssistantMessage{Message: turn.Assistant})
	if err == nil && assistant.Role != "" {
		if err := store.Append(sess.ID, assistant); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not save turn: %v\n", err)
		}
	}
	for _, outcome := range turn.Results {
		stored, err := session.FromAgentMessage(agent.ToolResultMessage{
			ToolCallID: outcome.CallID,
			ToolName:   outcome.ToolName,
			Content:    []ai.ContentBlock{ai.TextContent{Text: outcome.Result.Text}},
			IsError:    outcome.Result.IsError,
		})
		if err != nil || stored.Role == "" {
			continue
		}
		if err := store.Append(sess.ID, stored); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not save tool result: %v\n", err)
		}
	}
}

// openStore opens the session database without creating a session. A store
// that cannot be opened is not fatal: running without persistence is better
// than refusing to run at all, so the failure is reported and a nil store
// is returned. It is the shared seam behind openSession and the onboarding
// wizard, which must open the store before the project identity (and hence
// the session) exists.
func openStore() (*session.Store, func(), error) {
	path, err := session.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil, func() {}, nil
	}
	store, err := session.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil, func() {}, nil
	}
	return store, func() { _ = store.Close() }, nil
}

// openSession opens the store and either resumes or creates a session.
//
// A resume id that does not exist *is* fatal, because silently
// starting an empty session would look like the history was lost.
func openSession(opts options, resumeID int64) (*session.Store, *session.Session, func(), error) {
	store, closeStore, err := openStore()
	if err != nil {
		return nil, nil, nil, err
	}
	if store == nil {
		return nil, nil, closeStore, nil
	}

	if resumeID > 0 {
		sess, err := store.Get(resumeID)
		if err != nil {
			_ = store.Close()
			if errors.Is(err, session.ErrSessionNotFound) {
				return nil, nil, nil, fmt.Errorf("%w: %d", session.ErrSessionNotFound, resumeID)
			}
			return nil, nil, nil, err
		}
		return store, &sess, func() { _ = store.Close() }, nil
	}

	sess, err := store.CreateWith(session.CreateOptions{
		Cwd:       opts.root,
		Model:     opts.model.Provider + "/" + opts.model.ID,
		Workspace: opts.authority().String(),
		Project:   opts.projectID,
	})
	if err != nil {
		_ = store.Close()
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil, nil, func() {}, nil
	}
	return store, &sess, func() { _ = store.Close() }, nil
}

// installSessionManager wires the /sessions Session Manager: the roster
// listing, the resume opener, and the write actions (new, rename, archive,
// delete). A nil store installs nothing, so the command reports itself
// unavailable rather than panicking.
func installSessionManager(program *tui.Program, store *session.Store, held *harness, opts options) {
	if store == nil {
		return
	}
	program.SetSessionBrowser(
		func() ([]tui.SessionItem, error) {
			// The browser reads the harness's project, not the startup
			// snapshot: onboarding commits the identity after the browser is
			// installed, and the listing must follow it.
			list, err := store.ListByProject(held.projectID, 50)
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(list))
			for _, sess := range list {
				ids = append(ids, sess.ID)
			}
			stats, err := store.Stats(ids)
			if err != nil {
				return nil, err
			}
			items := make([]tui.SessionItem, 0, len(list))
			for _, sess := range list {
				stat := stats[sess.ID]
				items = append(items, tui.SessionItem{
					ID:      sess.ID,
					Title:   sess.Title,
					Model:   sess.Model,
					Updated: sess.UpdatedAt.Format("2006-01-02 15:04"),
					Cwd:     sess.Cwd,
					Tokens:  stat.Tokens,
					Prompt:  stat.Prompt,
				})
			}
			return items, nil
		},
		func(id int64) (*session.Session, tui.OpenedSession, error) {
			sess, err := store.Get(id)
			if err != nil {
				return nil, tui.OpenedSession{}, err
			}
			stored, err := store.Messages(id, 0)
			if err != nil {
				return nil, tui.OpenedSession{}, fmt.Errorf("load session %d: %w", id, err)
			}
			history, err := session.Replay(stored)
			if err != nil {
				return nil, tui.OpenedSession{}, fmt.Errorf("load session %d: %w", id, err)
			}
			return &sess, tui.OpenedSession{
				History: history,
				Entries: tui.EntriesFromMessages(history),
				Notice:  fmt.Sprintf("resumed session %d (%d messages)", id, len(history)),
			}, nil
		},
	)
	program.SetSessionActions(tui.SessionActions{
		New: func() (*session.Session, tui.OpenedSession, error) {
			sess, err := store.CreateWith(session.CreateOptions{
				Cwd:       opts.root,
				Model:     held.model.Provider + "/" + held.model.ID,
				Workspace: opts.authority().String(),
				Project:   held.projectID,
			})
			if err != nil {
				return nil, tui.OpenedSession{}, err
			}
			return &sess, tui.OpenedSession{
				Notice: fmt.Sprintf("started new session %d", sess.ID),
			}, nil
		},
		Rename:  func(id int64, title string) error { return store.SetTitle(id, title) },
		Archive: func(id int64) error { return store.SetArchived(id, true) },
		Delete:  func(id int64) error { return store.Delete(id) },
	})
}

// applySessionAuthority restores a resumed session's effective workspace and
// model, then records the result as the session's snapshot.
//
// A running/resumable session owns its resolved model identity (§13): if the
// global configuration changed since the session was created, the session must
// not silently switch models on restart (I8). An explicit flag or environment
// override still wins, and is then written back so the snapshot stays truthful
// (§31).
//
// resolve is a parameter rather than a registry so a test can drive it with
// catalog.Resolve and stay offline.
func applySessionAuthority(cmd *cobra.Command, opts *options, resolve func(string) (ai.Model, error), store *session.Store, sess *session.Session) error {
	if sess == nil {
		return nil
	}
	explicitWorkspace := flagValue(cmd, "workspace") != ""
	explicitModel := flagValue(cmd, "model") != "" || strings.TrimSpace(os.Getenv("OHGO_MODEL")) != ""

	if explicitWorkspace {
		// The CLI already resolved and validated this; re-read it so the
		// function is self-consistent rather than depending on the caller.
		if ws, err := workspace.Parse(flagValue(cmd, "workspace")); err == nil {
			opts.setAuthority(ws)
		}
	} else if ws, err := workspace.Parse(sess.Workspace); err == nil {
		opts.setAuthority(ws)
	}
	if !explicitModel && strings.TrimSpace(sess.Model) != "" {
		resolved, err := resolve(sess.Model)
		if err != nil {
			return fmt.Errorf("resume: session model %q: %w", sess.Model, err)
		}
		opts.model = resolved
		opts.maxTokens = resolveMaxTokens(opts.settings, resolved)
	}

	// Snapshot the effective authority so a later run restores what actually
	// executed rather than what the configuration happened to say this time.
	effective := opts.model.Provider + "/" + opts.model.ID
	if store != nil && (sess.Workspace != opts.authority().String() || sess.Model != effective) {
		if err := store.SetAuthority(sess.ID, opts.authority().String(), effective); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not record session authority: %v\n", err)
		}
	}
	return nil
}

// loadHistory replays a stored transcript so a resumed session continues from
// where it left off.
//
// A replay failure is fatal: starting from an empty history would look
// identical to the session having been wiped.
func loadHistory(store *session.Store, sess *session.Session) ([]agent.Message, error) {
	if store == nil || sess == nil {
		return nil, nil
	}
	stored, err := store.Messages(sess.ID, 0)
	if err != nil {
		return nil, fmt.Errorf("load session %d: %w", sess.ID, err)
	}
	return session.Replay(stored)
}
