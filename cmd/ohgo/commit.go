package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/commit"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/tui"
)

// commitMaxTokens bounds the commit-authoring completion.
//
// The message itself is tiny, but reasoning models spend hidden reasoning
// tokens from this same budget before emitting visible text. A 512-token cap
// made the COMMIT model finish with empty content and a length stop reason.
// The budget is reserved, not consumed: only generated tokens are billed.
const commitMaxTokens = 8192

// newCommitService resolves the dedicated COMMIT role and builds the
// deterministic executor.
//
// The role is resolved through config.Settings.RoleModel, never through the
// active workspace model. An unassigned role follows the same documented
// fallback chain the /model control plane displays, so the model named there is
// the model /commit actually calls.
func newCommitService(ctx context.Context, root string, settings config.Settings, registry *ai.Registry) (*commit.Service, error) {
	if registry == nil {
		return nil, fmt.Errorf("/commit: provider registry is unavailable")
	}
	// Resolution shares the request context: a dynamic catalog lookup is part
	// of starting /commit, so cancelling the command must stop it too.
	model, effort, err := resolveRoleModel(settings, config.RoleCommit, "/commit",
		dynamicResolver(ctx, registry))
	if err != nil {
		return nil, err
	}
	return &commit.Service{
		Root:      root,
		Stream:    registry.StreamFn(),
		Model:     model,
		Effort:    effort,
		MaxTokens: commitMaxTokens,
		// The bounded interactive budget lives in the commit package so the
		// TUI, the REPL, and tests share one policy. The request context owns
		// lifecycle only; this timeout owns the wait.
		Timeout: commit.DefaultProposalTimeout,
	}, nil
}

// commitHooks wires /commit into the full-screen UI.
//
// settings is a pointer so a /model role reassignment applies to the next
// invocation without restarting the session. Prepare snapshots it per command,
// which keeps that freshness without racing the async request.
func commitHooks(root string, settings *config.Settings, registry *ai.Registry) tui.CommitHooks {
	return tui.CommitHooks{
		// Prepare runs on the UI goroutine. It copies the configuration
		// fields the role resolver reads, so a /model assignment can never
		// race the provider request that is using the previous snapshot.
		Prepare: func() (func(context.Context) (*commit.Request, error), error) {
			if settings == nil {
				return nil, fmt.Errorf("/commit: configuration is unavailable")
			}
			snapshot := cloneCommitSettings(*settings)
			return func(ctx context.Context) (*commit.Request, error) {
				svc, err := newCommitService(ctx, root, snapshot, registry)
				if err != nil {
					return nil, err
				}
				return svc.Propose(ctx)
			}, nil
		},
		Execute: func(ctx context.Context, request *commit.Request) (commit.Result, error) {
			// Execution must not re-resolve the commit model: the message was
			// approved, and a model/credential change between approval and
			// execution must not block the approved Git operation.
			return (&commit.Service{Root: root}).Execute(ctx, request)
		},
	}
}

// cloneCommitSettings copies the configuration a commit proposal can read.
//
// The maps are copied because the model picker mutates them on the update
// goroutine while the proposal command reads its snapshot off-goroutine. String
// fields are immutable, so a shallow copy of the rest is sufficient.
func cloneCommitSettings(settings config.Settings) config.Settings {
	clone := settings
	clone.Models = maps.Clone(settings.Models)
	clone.Roles = maps.Clone(settings.Roles)
	return clone
}

// commitREPL runs the same proposal/approval/execution flow for the plain
// REPL, where there is no modal surface. The approval is an explicit stdin
// answer and the resting answer is no.
func (h *harness) commitREPL(out io.Writer, reader *bufio.Reader) error {
	settings := h.settingsOrDefault()
	// The context owns lifecycle, not the wait: Service.Propose applies the
	// bounded COMMIT timeout itself, so the REPL and the TUI cannot drift.
	proposeCtx, proposeCancel := context.WithCancel(context.Background())
	defer proposeCancel()
	service, err := newCommitService(proposeCtx, h.root, settings, h.registry)
	if err != nil {
		return err
	}

	request, err := service.Propose(proposeCtx)
	proposeCancel()
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(out, commit.FormatProposal(request))
	_, _ = fmt.Fprint(out, "\nCreate this commit? [y/N] ")
	line, readErr := reader.ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return fmt.Errorf("read approval: %w", readErr)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		_, _ = fmt.Fprintln(out, "commit cancelled.")
		return nil
	}

	executeCtx, executeCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer executeCancel()
	result, err := service.Execute(executeCtx, request)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, commit.FormatResult(result))
	return nil
}
