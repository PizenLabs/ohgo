package agent

import "context"

// EditJournal is the recovery seam around agent mutations.
//
// The loop calls BeginEdit once per run, immediately before it dispatches the
// first tool batch that can change the workspace, and commits the returned
// transaction when the run ends. The order is the contract: the pre-mutation
// state is captured and made durable before any mutating call is authorized, so
// a recovery point that does not exist cannot be assumed to exist.
//
// Implementations are host code. The loop never offers this interface to a
// model, a tool, or a projector: a journal observes execution, it does not
// authorize it, and it has no method that can restore or rewind anything.
type EditJournal interface {
	BeginEdit(ctx context.Context, boundary EditBoundary) (EditTransaction, error)
}

// EditTransaction is one in-progress edit boundary.
//
// Commit is called after the run's tools have finished, including when the run
// ended in error, because a mutation that already happened still needs its
// recovery point published. Abort is called when the journal itself gives up.
type EditTransaction interface {
	// Commit observes the post-mutation workspace and publishes the checkpoint.
	Commit(ctx context.Context) error
	// Abort discards the transaction without publishing a checkpoint.
	Abort()
}

// EditBoundary describes the turn whose tools are about to run.
//
// It is metadata for audit, never an instruction: a journal must not use it to
// decide whether a mutation is allowed. The security boundary makes that
// decision, and it runs later, per call.
type EditBoundary struct {
	// Turn is the zero-based model turn within the run that first proposed a
	// mutating call.
	Turn int
	// Tools names the tools in that batch, in the order the model emitted
	// them.
	Tools []string
}
