package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
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

// projectRun derives the stateless verdict from the shared evaluator.
//
// It is the one mapping used by `ohgo run` and the plain REPL. It never
// reimplements completion or the stop classification: agent.ClassifyRun owns
// the decision, and this function only projects the verdict into the exit-code
// vocabulary a script can branch on.
func projectRun(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver, deadlineExpired bool, evidenceReview string) runOutcome {
	input := agent.CompletionInput{
		Turns:          turns,
		Contract:       intent,
		Snapshot:       snapshot,
		Resolver:       resolver,
		EvidenceReview: evidenceReview,
	}

	verdict, reason := agent.ClassifyRun(err, input, deadlineExpired)
	switch verdict {
	case agent.RunCompleted:
		return newOutcome(runCompleted, "")
	case agent.RunNeedsReview:
		return newOutcome(runNeedsReview, reason)
	case agent.RunPaused:
		return newOutcome(runPaused, reason)
	case agent.RunCancelled:
		// Cancellation is authoritative: verified work is not completed after
		// the user interrupted the process.
		return newOutcome(runCancelled, reason)
	default:
		return newOutcome(runFailed, reason)
	}
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
	return runHeadlessTurnAs(ctx, loop, root, out, text, history, "")
}

// runHeadlessTurnAs is runHeadlessTurn with the effective turn authority
// supplied by the router.
//
// The shape's authority must describe the authority the security gate is
// actually enforcing. Deriving it from the completion kind alone is wrong for
// an investigative turn delegated to FUZZ, and a state block that names BUILD
// while the gate is FUZZ is a false observation.
func runHeadlessTurnAs(ctx context.Context, loop *agent.Loop, root string, out io.Writer, text string, history []agent.Message, authority string) (runOutcome, []agent.Turn) {
	intent := workspace.BuildIntentContract(text)
	resolver := structuralResolver(loop.Tools())

	// The request's semantic contract is installed for this turn only. It
	// narrows the action space (no invented mutation) and gives the completion
	// evaluator the entities and ambiguities the user actually stated.
	loop.SetGrounding(intent.Grounding)
	defer loop.SetGrounding(nil)

	var snapshot *agent.TargetSnapshot
	if intent.Kind == workspace.ObjectiveMutation && root != "" {
		snapshot = agent.CaptureTargetsContext(ctx, root, intent, resolver)
	}

	// The stateless paths get the same task shape and evidence projection as
	// the TUI, but the evidence lives for this run only: a one-shot command is
	// explicitly non-durable and must not pretend to resume knowledge it did
	// not persist.
	authority = strings.ToLower(strings.TrimSpace(authority))
	switch authority {
	case "ask", "build", "fuzz":
	default:
		authority = "build"
		if intent.Kind == contract.ObjectiveInformational {
			authority = "ask"
		}
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
	var lastReport agent.ProjectionReport
	haveReport := false
	loop.SetProjectionReportHook(func(report agent.ProjectionReport) {
		lastReport = report
		haveReport = true
		if contextAuditEnabled() {
			_, _ = fmt.Fprintln(os.Stderr, "[ohgo context]\n"+cognition.RenderAudit(report))
		}
	})
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
	review := ""
	if haveReport {
		review = lastReport.EvidenceReview()
	}
	outcome := projectRun(err, turns, intent, snapshot, resolver, deadlineExpired, review)
	if out != nil {
		_, _ = fmt.Fprintln(out, outcome.line())
	}
	return outcome, turns
}
