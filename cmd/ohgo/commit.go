package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/commit"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tui"
)

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
		Effort:    commitEffort(model, effort),
		MaxTokens: commit.DefaultProposalTokens,
		// The bounded interactive budget lives in the commit package so the
		// TUI, the REPL, and tests share one policy. The request context owns
		// lifecycle only; this timeout owns the wait.
		Timeout: commit.DefaultProposalTimeout,
	}, nil
}

// commitEffort selects the least reasoning the bound model can be asked for.
//
// COMMIT writes one short message, so the role's coding-turn effort is not the
// right default. A model with a published ladder uses its lowest level; a model
// with a provider-native disable (some endpoints reason by default) uses that
// value; a non-reasoning model is left switched off. Only a mandatory-reasoning
// model keeps the role effort, because claiming it was disabled would be false.
func commitEffort(model ai.Model, roleEffort string) string {
	roleEffort = strings.TrimSpace(roleEffort)
	if !model.Reasoning {
		return "off"
	}
	// Anthropic's thinking control is a slice of the same max_tokens budget.
	// A COMMIT-sized completion has no room for a valid thinking budget, so the
	// transport must omit thinking rather than send an invalid request.
	if model.API == ai.APIAnthropicMessages {
		return "off"
	}
	// "off" means omit the field, but omission is only a truthful disable for
	// a provider that reasons solely when asked. A model that publishes a
	// native disable or an effort ladder reasons by default: sending nothing
	// selects the provider default, which can exhaust the COMMIT completion on
	// hidden tokens. The cheapest published control is the honest translation
	// of "off" for those models.
	if model.ReasoningDisable != "" {
		return model.ReasoningDisable
	}
	if len(model.ReasoningEfforts) > 0 {
		return model.ReasoningEfforts[0]
	}
	if roleEffort == "off" {
		return "off"
	}
	if model.ReasoningMandatory {
		return roleEffort
	}
	// No known control: omit the field. That is the only truthful disable for
	// a model that does not publish a ladder.
	return "off"
}

// commitHooks wires /commit into the full-screen UI.
//
// settings is a pointer so a /model role reassignment applies to the next
// invocation without restarting the session. Prepare snapshots it per command,
// which keeps that freshness without racing the async request.
func commitHooks(root string, settings *config.Settings, registry *ai.Registry, objective ...func() string) tui.CommitHooks {
	return tui.CommitHooks{
		// Prepare runs on the UI goroutine. It copies the configuration
		// fields the role resolver reads, so a /model assignment can never
		// race the provider request that is using the previous snapshot.
		Prepare: func() (func(context.Context) (*commit.Request, error), error) {
			if settings == nil {
				return nil, fmt.Errorf("/commit: configuration is unavailable")
			}
			snapshot := cloneCommitSettings(*settings)
			// The task objective is authoritative continuity evidence. It is
			// read on the UI goroutine with the same snapshot, then used only
			// for grouping; the working tree remains the source of changes.
			objectiveText := ""
			if len(objective) > 0 && objective[0] != nil {
				objectiveText = strings.TrimSpace(objective[0]())
			}
			return func(ctx context.Context) (*commit.Request, error) {
				svc, err := newCommitService(ctx, root, snapshot, registry)
				if err != nil {
					return nil, err
				}
				svc.Objective = objectiveText
				return svc.Propose(ctx)
			}, nil
		},
		// PreparePlan binds the automatic plan and every per-group proposal to
		// one configuration snapshot, so a /model reassignment cannot change
		// the model halfway through a plan.
		PreparePlan: func() (tui.CommitPlanHooks, error) {
			if settings == nil {
				return tui.CommitPlanHooks{}, fmt.Errorf("/commit: configuration is unavailable")
			}
			snapshot := cloneCommitSettings(*settings)
			objectiveText := ""
			if len(objective) > 0 && objective[0] != nil {
				objectiveText = strings.TrimSpace(objective[0]())
			}
			serviceFor := func(ctx context.Context) (*commit.Service, error) {
				svc, err := newCommitService(ctx, root, snapshot, registry)
				if err != nil {
					return nil, err
				}
				svc.Objective = objectiveText
				return svc, nil
			}
			return tui.CommitPlanHooks{
				Plan: func(ctx context.Context) (*commit.Plan, error) {
					svc, err := serviceFor(ctx)
					if err != nil {
						return nil, err
					}
					return svc.Plan(ctx)
				},
				ProposeGroup: func(ctx context.Context, plan *commit.Plan, key string) (*commit.Request, error) {
					svc, err := serviceFor(ctx)
					if err != nil {
						return nil, err
					}
					return svc.ProposeGroup(ctx, plan, key)
				},
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
func (h *harness) commitREPL(out io.Writer, reader *bufio.Reader, args ...string) error {
	all := false
	for _, raw := range args {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "-a", "--all":
			all = true
		default:
			return fmt.Errorf("commit: unknown REPL argument %q", raw)
		}
	}
	settings := h.settingsOrDefault()
	// The context owns lifecycle, not the wait: Service.Propose applies the
	// bounded COMMIT timeout itself, so the REPL and the TUI cannot drift.
	proposeCtx, proposeCancel := context.WithCancel(context.Background())
	defer proposeCancel()
	service, err := newCommitService(proposeCtx, h.root, settings, h.registry)
	if err != nil {
		return err
	}

	if all {
		return commitPlanREPL(out, reader, service)
	}

	request, err := service.Propose(proposeCtx)
	proposeCancel()
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(out, commit.FormatProposal(request))
	approved, err := readCommitApproval(out, reader)
	if err != nil {
		return err
	}
	if !approved {
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

// commitPlanREPL walks a complete logical commit plan one approval at a time.
// It replans after every commit because HEAD, the index, and the worktree have
// changed; the plan is a proposal, Git is authoritative.
func commitPlanREPL(out io.Writer, reader *bufio.Reader, service *commit.Service) error {
	created := 0
	planned := 0
	staleRetries := 0
	for {
		planCtx, planCancel := context.WithCancel(context.Background())
		plan, err := service.Plan(planCtx)
		planCancel()
		if errors.Is(err, commit.ErrNoChanges) {
			if created == 0 {
				_, _ = fmt.Fprintln(out, "No changes to commit.")
			} else {
				_, _ = fmt.Fprintf(out, "Commit plan complete: %d %s created.\n", created, pluralWord("commit", created))
			}
			return nil
		}
		if err != nil {
			if created > 0 {
				_, _ = fmt.Fprintf(out, "commit plan stopped after %d commits; remaining changes are untouched.\n", created)
			}
			return err
		}
		if len(plan.Groups) == 0 {
			_, _ = fmt.Fprintln(out, commit.FormatPlan(plan))
			if created == 0 {
				_, _ = fmt.Fprintln(out, "No logical commits were planned.")
			} else {
				_, _ = fmt.Fprintf(out, "Commit plan complete: %d %s created.\n", created, pluralWord("commit", created))
			}
			return nil
		}
		if created == 0 {
			_, _ = fmt.Fprintln(out, commit.FormatPlan(plan))
		}
		planned = created + len(plan.Groups)
		group := plan.Groups[0]
		proposeCtx, proposeCancel := context.WithCancel(context.Background())
		request, err := service.ProposeGroup(proposeCtx, plan, group.Key)
		proposeCancel()
		if err == nil && request != nil && request.Progress != nil && created > 0 {
			request.Progress.Position += created
			request.Progress.Total = planned
		}
		if errors.Is(err, commit.ErrPlanStale) {
			// The repository moved between planning and this proposal. The
			// outer loop re-reads it; a stale scope is never staged.
			staleRetries++
			if staleRetries > 3 {
				return err
			}
			continue
		}
		if err != nil {
			if created > 0 {
				_, _ = fmt.Fprintf(out, "commit plan stopped after %d commits; remaining changes are untouched.\n", created)
			}
			return err
		}
		staleRetries = 0
		_, _ = fmt.Fprintln(out, commit.FormatProposal(request))
		approved, err := readCommitApproval(out, reader)
		if err != nil {
			return err
		}
		if !approved {
			if created > 0 {
				_, _ = fmt.Fprintf(out, "commit plan stopped after %d of %d commits; remaining changes are untouched.\n",
					created, planned)
			} else {
				_, _ = fmt.Fprintln(out, "commit plan cancelled.")
			}
			return nil
		}
		executeCtx, executeCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		result, err := service.Execute(executeCtx, request)
		executeCancel()
		if err != nil {
			if created > 0 {
				_, _ = fmt.Fprintf(out, "commit plan stopped after %d of %d commits; remaining changes are untouched.\n",
					created, planned)
			}
			return err
		}
		created++
		_, _ = fmt.Fprintln(out, commit.FormatResult(result))
	}
}

// readCommitApproval reads the explicit stdin answer. EOF is the resting
// answer: no newline and no approval must still cancel.
func readCommitApproval(out io.Writer, reader *bufio.Reader) (bool, error) {
	_, _ = fmt.Fprint(out, "\nCreate this commit? [y/N] ")
	line, readErr := reader.ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return false, fmt.Errorf("read approval: %w", readErr)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// pluralWord is the REPL's local noun helper.
func pluralWord(noun string, count int) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}
