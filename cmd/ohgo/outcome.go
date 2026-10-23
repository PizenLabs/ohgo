package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/workspace"
)

// runState is the honest terminal verdict a stateless execution path reports.
//
// It is deliberately coarser than the durable task lifecycle: a one-shot run
// has no task identity to resume, but it still must not present an evidence
// gap, a resource stop, or a refusal as success.
type runState string

const (
	// runCompleted means the Phase 4 contract was satisfied by observed
	// evidence.
	runCompleted runState = "COMPLETED"
	// runNeedsReview means the run stopped without enough evidence. It is
	// non-terminal and resumable in the interactive task path; a one-shot run
	// reports it rather than inventing continuity it does not have.
	runNeedsReview runState = "NEEDS_REVIEW"
	// runPaused means a resource or interruption stopped the run before a
	// verdict could be reached.
	runPaused runState = "PAUSED"
	// runFailed means the run hit an execution or provider failure, or an
	// authority refusal.
	runFailed runState = "FAILED"
	// runCancelled means the user interrupted the process (SIGINT).
	runCancelled runState = "CANCELLED"
)

// Exit codes. They distinguish the outcomes scripts need to branch on without
// inventing a taxonomy per command: 0 success, 2 evidence review, 3 resource
// pause, 1 failure, and the conventional 130 for a SIGINT cancellation.
const (
	exitCompleted   = 0
	exitFailed      = 1
	exitNeedsReview = 2
	exitPaused      = 3
	exitCancelled   = 130
)

// runOutcome is the printable verdict and the process status it implies.
type runOutcome struct {
	State runState
	// Reason explains a non-success verdict in evidence terms.
	Reason string
}

// newOutcome builds a verdict with its fixed exit status.
func newOutcome(state runState, reason string) runOutcome {
	return runOutcome{State: state, Reason: reason}
}

// exitCode maps the verdict to the process status.
func (o runOutcome) exitCode() int {
	switch o.State {
	case runCompleted:
		return exitCompleted
	case runNeedsReview:
		return exitNeedsReview
	case runPaused:
		return exitPaused
	case runCancelled:
		return exitCancelled
	default:
		return exitFailed
	}
}

// line renders the one terminal line both headless paths print.
func (o runOutcome) line() string {
	if o.State == runCompleted {
		return string(o.State)
	}
	if o.Reason == "" {
		return string(o.State)
	}
	return string(o.State) + ": " + o.Reason
}

// headlessExit carries a non-success verdict to main without printing it twice.
// The verdict line has already reached stdout; main only needs the status.
type headlessExit struct {
	outcome runOutcome
}

// Error implements error.
func (e *headlessExit) Error() string { return e.outcome.line() }

// contextAuditEnabled reports whether the runtime should print projection
// decisions. It is an operator/debug switch, never a task input.
func contextAuditEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OHGO_DEBUG_CONTEXT"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// projectRun derives the stateless verdict from the shared Phase 4 evaluator.
//
// It is the one mapping used by `ohgo run` and the plain REPL. It never
// reimplements completion: every semantic decision is delegated to
// agent.EvaluateTaskCompletion or agent.EvaluateBudgetCompletion.
func projectRun(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver, deadlineExpired bool) runOutcome {
	input := agent.CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Resolver: resolver,
	}

	switch {
	case errors.Is(err, context.Canceled):
		// Cancellation is authoritative. Verified work is not completed after
		// the user interrupted the process; the TUI path pauses for the same
		// reason.
		return newOutcome(runCancelled, "interrupted")
	case errors.Is(err, context.DeadlineExceeded):
		return resourceOutcome(input, "deadline reached")
	case errors.Is(err, budget.ErrExhausted):
		reason := "budget exhausted"
		var exhausted *budget.ExhaustedError
		if errors.As(err, &exhausted) {
			reason = exhausted.Error()
		}
		return resourceOutcome(input, reason)
	case errors.Is(err, agent.ErrTurnLimit):
		return newOutcome(runPaused, "turn limit reached")
	case err != nil:
		return newOutcome(runFailed, err.Error())
	}

	if deadlineExpired {
		// A deadline is a resource stop, not a verdict. Phase 5's rule still
		// applies: already-complete evidence remains authoritative, and a
		// missing closing turn does not erase a verified mutation.
		completion := agent.EvaluateBudgetCompletion(input, true)
		if completion.Complete() {
			return newOutcome(runCompleted, "")
		}
		return newOutcome(runPaused, "deadline reached")
	}
	completion := agent.EvaluateTaskCompletion(input)
	if completion.Complete() {
		return newOutcome(runCompleted, "")
	}
	return newOutcome(runNeedsReview, completion.Reason)
}

// resourceOutcome applies the Phase 5 budget-completion rule: a resource stop
// is not success by itself, but already-complete evidence remains
// authoritative.
func resourceOutcome(input agent.CompletionInput, reason string) runOutcome {
	completion := agent.EvaluateBudgetCompletion(input, true)
	if completion.Complete() {
		return newOutcome(runCompleted, "")
	}
	return newOutcome(runPaused, reason)
}

// refuseHeadless prints and returns the verdict for a route the active
// workspace refused. Authority is decided before the model, so this is a
// failure of the requested objective, not a model failure.
func refuseHeadless(out io.Writer, route workspace.Route) runOutcome {
	outcome := newOutcome(runFailed, route.Message())
	if out != nil {
		_, _ = fmt.Fprintln(out, outcome.line())
	}
	return outcome
}

// runHeadlessTurn executes one stateless turn through the same authority,
// budget, evidence, and completion path the TUI uses, then prints and returns
// the verdict.
//
// The turns are returned for the REPL's history and usage ledger; `ohgo run`
// ignores them. The function does not create a durable task: the stateless
// modes are explicitly non-durable and say so in their output.
func runHeadlessTurn(ctx context.Context, loop *agent.Loop, root string, out io.Writer, text string, history []agent.Message) (runOutcome, []agent.Turn) {
	intent := workspace.BuildIntentContract(text)
	resolver := structuralResolver(loop.Tools())

	var snapshot *agent.TargetSnapshot
	if intent.Kind == workspace.ObjectiveMutation && root != "" {
		snapshot = agent.CaptureTargetsContext(ctx, root, intent, resolver)
	}

	// The stateless paths get the same task shape and evidence projection as
	// the TUI, but the evidence lives for this run only: a one-shot command is
	// explicitly non-durable and must not pretend to resume knowledge it did
	// not persist.
	authority := "build"
	if intent.Kind == contract.ObjectiveInformational {
		authority = "ask"
	}
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{
		Authority: authority,
		Facts:     facts,
		Snapshot:  snapshot,
		Budget:    cognition.BudgetShapeFrom(loop.Budget().Snapshot()),
	})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)
	loop.SetProjectionHook(projector.Project)
	if contextAuditEnabled() {
		loop.SetProjectionReportHook(func(report agent.ProjectionReport) {
			_, _ = fmt.Fprintln(os.Stderr, "[ohgo context]\n"+cognition.RenderAudit(report))
		})
	} else {
		loop.SetProjectionReportHook(nil)
	}
	defer loop.SetProjectionHook(nil)
	defer loop.SetProjectionReportHook(nil)

	request := append(append([]agent.Message(nil), history...), agent.UserMessage{Text: text})
	turns, err := loop.Run(ctx, request)
	// The loop returns the context error when it observes it; make the
	// cancellation authoritative even if the final provider call happened to
	// return cleanly at the same instant.
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}

	deadline := loop.Deadline()
	deadlineExpired := !deadline.IsZero() && !time.Now().Before(deadline)
	outcome := projectRun(err, turns, intent, snapshot, resolver, deadlineExpired)
	if out != nil {
		_, _ = fmt.Fprintln(out, outcome.line())
	}
	return outcome, turns
}
