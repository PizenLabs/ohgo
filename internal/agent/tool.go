// Package agent implements the model/tool loop: it sends a context to a
// provider, dispatches any tool calls the model requests, appends the results,
// and repeats until the model stops calling tools or the caller aborts.
//
// The loop depends on the StreamFn seam rather than a concrete provider, so a
// test can drive it with a scripted sequence of turns and assert on the exact
// messages and results the loop produced.
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Tier classifies what a tool can do, for approval purposes.
type Tier string

// Capability tiers.
const (
	// TierRead is side-effect free.
	TierRead Tier = "read"
	// TierWrite mutates files.
	TierWrite Tier = "write"
	// TierExec runs processes.
	TierExec Tier = "exec"
)

// ApprovalPolicy decides how an approval gate treats a tier.
type ApprovalPolicy string

// Approval policies.
const (
	// PolicyAllow auto-approves the call.
	PolicyAllow ApprovalPolicy = "allow"
	// PolicyPrompt asks the user.
	PolicyPrompt ApprovalPolicy = "prompt"
	// PolicyDeny blocks the call.
	PolicyDeny ApprovalPolicy = "deny"
)

// Approval describes when a tool may run without prompting.
//
// The zero value requires a prompt for every call, which is deliberately
// conservative: a tool that forgets to declare approval is never silently
// auto-approved.
type Approval struct {
	// Tier is the capability the call exercises.
	Tier Tier
	// Reason is shown to the user in the approval prompt.
	Reason string
	// Policy overrides the gate's default for this tier when set.
	Policy ApprovalPolicy
	// Preview is the human-readable intended mutation, when available.
	Preview string
	// Diff mirrors Preview.
	Diff string
	// MutationPreview is the structured intended mutation.
	MutationPreview MutationPreview
	// PreviewMutation mirrors MutationPreview.
	PreviewMutation MutationPreview
}

// Result is what a tool returns.
type Result struct {
	// Text is the content the model sees.
	Text string
	// Details is the structured payload for UI rendering and logging.
	Details any
	// IsError marks a non-throwing failure.
	IsError bool
	// Mutates reports that the execution definitely changed the workspace. It
	// is set by the tool implementation from what it actually did, never from
	// model text, and is the runtime evidence completion uses to decide whether
	// a change still needs verification.
	Mutates bool
	// Executes reports that the tool started an arbitrary process. Such a
	// process may have changed the workspace even when the runtime cannot prove
	// it, so a mutation objective treats a successful execution as requiring a
	// later verification. It is not a claim that a change happened, and it is
	// not completion evidence on its own: a background spawn is started but
	// unfinished.
	Executes bool
	// Finished reports that the process described by Executes reached a
	// terminal state. For a background job, only the wait result that observes
	// the exit sets it; the spawn does not. Completion requires a finished,
	// successful execution, never merely a started one.
	Finished bool
	// Verifies reports that the execution successfully checked the workspace
	// (tests, build, vet). It is evidence for completion, not authority: a
	// verification cannot grant a capability or stand in for a mutation.
	Verifies bool
	// Verification records what the successful check actually covered. It is
	// nil for a verifying tool that cannot report a command scope (a delegated
	// child, for example), which satisfies a generic requirement but not a
	// target-scoped one.
	Verification *VerificationEvidence
	// Runtime reports a live project runtime the capability started, observed
	// ready, checked, and confirmed stayed alive. It is nil for a command that
	// merely finished: Verifies alone never proves a runtime objective.
	Runtime *RuntimeEvidence
	// Useless marks a result that adds nothing to context and may be elided by
	// compaction (zero matches, a timeout). Ignored when IsError is set.
	Useless bool
	// Reused marks a successful result that restates an observation already
	// current in this run. It is not a new repository observation: evidence
	// recording must skip it or a reuse notice would look like changed file
	// content and manufacture a conflict.
	Reused bool
	// FailureClass is the provider-agnostic category of an execution failure,
	// when the producer can classify it (a missing credential, an unknown
	// capability, a timeout). Empty means unclassified.
	//
	// It is separate from Error text because the control plane must reason about
	// retryability, not match prose. It narrows nothing and authorizes nothing.
	FailureClass string
	// FailureRetryable reports that another attempt with the same invocation
	// may succeed. It is meaningful only when FailureClass is set; the zero
	// value means non-retryable, which is the conservative default.
	FailureRetryable bool
	// Refused marks a call the tool itself refused on authority or contract
	// grounds. The invocation was well-formed, but the live workspace cannot
	// perform it, so nothing was produced. It is an error for the model, which
	// must choose another kind or action, but it is not an execution failure:
	// completion must not treat it as an unrecovered failed call.
	Refused bool
	// CandidateRejected marks a call the runtime withheld because the task
	// shape's action surface did not include the tool. It is an error for the
	// model, which must choose another action, but it is not an execution
	// failure: nothing ran, so completion must not treat it as a failed call.
	CandidateRejected bool
	// FailureRejected marks a call the runtime refused before execution because
	// the identical capability and arguments already failed and the retry
	// budget for that failure class is spent. Nothing ran; it is a control
	// decision, never an execution failure.
	FailureRejected bool
	// ObservedAbsence marks an error that is itself a successful observation:
	// a probe established that the named path does not exist. The model still
	// sees an error (absence is not a value), but completion must not treat it
	// as an unrecovered execution failure.
	ObservedAbsence bool
	// AnswerRequired marks a withheld call made when the runtime offered no
	// tools at all: the evidence lifecycle asked for an answer, not another
	// action. Nothing ran, and the assistant text in the same turn is the
	// candidate answer, so completion may consider that turn final. Unlike a
	// generic candidate rejection, this is not a request to choose another
	// tool; there is no other tool.
	AnswerRequired bool
	// Exhausted is set when the tool refused work because the root budget
	// could not admit it. The loop records it as a resource stop after the
	// turn, so a budget refusal inside a tool is visible to the root instead
	// of looking like an ordinary tool error.
	Exhausted *budget.ExhaustedError
}

// Rejected reports that the capability never ran the call because its
// arguments or operation shape did not satisfy the capability's own contract.
//
// A malformed invocation is a harness contract error, not an observation about
// the project: it neither establishes nor contradicts anything, and nothing
// executed. Completion must not let a late malformed continuation downgrade a
// proof that real evidence already supports, so it is not pending failure and
// not outstanding work. The failure ledger still counts it, because an
// identical malformed repeat is a loop the runtime must be able to refuse, but
// it records the rejection at request level rather than as capability health.
func (r Result) Rejected() bool {
	switch r.FailureClass {
	case ToolFailureInvalidArgument, ToolFailureInvalidSelector,
		ToolFailureUnknownTool, ToolFailureUnsupported,
		ToolFailureAuthorization:
		return true
	}
	return false
}

// ErrorResult builds an error result from a formatted message.
func ErrorResult(format string, args ...any) Result {
	return Result{Text: fmt.Sprintf(format, args...), IsError: true}
}

// Context is handed to a tool while it executes.
type Context struct {
	// Ctx is the request-scoped context; it carries the abort and deadline.
	Ctx context.Context
	// CallID is the id of the tool call this execution answers.
	CallID string
	// ToolName is the name of the running tool.
	ToolName string
	// SessionID identifies the owning session, for logging and telemetry.
	SessionID string
	// Approve reports whether the caller pre-approved this invocation.
	Approved bool
	// ReadOnly reports that the authorization decision forbids workspace
	// writes. An execution tool must enforce it independently of its
	// arguments, or refuse to run. It is derived from the authority in force,
	// not from a command-text guess.
	ReadOnly bool
	// OnProgress lets a long tool publish partial output.
	OnProgress func(Result)
	// Budget is the root-owned grant shared by this execution tree. It is
	// how a tool that starts detached work (a background job) charges that
	// work to the same root as the turn that requested it. Nil means the
	// caller installed no tree budget.
	Budget *budget.Budget
	// Deadline is the effective root deadline, inherited from the budget and
	// the run. Detached work must not be scheduled past it.
	Deadline time.Time
}

// CapabilityReporter lets a tool refine the capability a specific invocation
// exercises, when its declared Approval tier is only the default.
//
// The declared tier answers "what does this tool usually do"; this answers
// "what does this call do". A shell tool is exec by default but writes a file
// when its command redirects output, and the admission layer needs the
// invocation-accurate answer to enforce a mutation boundary.
type CapabilityReporter interface {
	// Capability returns the tier for one invocation.
	Capability(args map[string]any) Tier
}

// Tool is one capability the model can invoke.
type Tool interface {
	// Name is the wire name the model calls.
	Name() string
	// Description is shown to the model.
	Description() string
	// Parameters is the JSON Schema for the arguments.
	Parameters() *jsonschema.Schema
	// Approval declares the capability tier.
	Approval() Approval
	// Exclusive reports whether this tool must run alone, with no other tool
	// call of the same batch in flight. Tools that mutate shared state (git
	// operations, package installs) must declare it; the default is shared.
	//
	// It is resolved from the raw arguments so a tool that is usually
	// side-effect free can opt a specific invocation into exclusivity.
	Exclusive(args map[string]any) bool
	// Execute runs the tool. It must honour ctx cancellation.
	Execute(ctx Context, args map[string]any) Result
}

// Spec renders a tool as the wire-level tool definition.
func Spec(tool Tool) ai.ToolSpec {
	return ai.ToolSpec{
		Name:        tool.Name(),
		Description: tool.Description(),
		Parameters:  tool.Parameters().RawJSON(),
	}
}

// Specs renders every tool in order.
func Specs(tools []Tool) []ai.ToolSpec {
	out := make([]ai.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		out = append(out, Spec(tool))
	}
	return out
}

// ValidateArgs checks arguments against a tool's schema.
//
// A schema violation is converted into an error Result rather than aborting
// the turn, so the model can correct its own arguments on the next turn.
func ValidateArgs(tool Tool, args map[string]any) error {
	schema := tool.Parameters()
	if schema == nil {
		return nil
	}
	return schema.Validate(args)
}
