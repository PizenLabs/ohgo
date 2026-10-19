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

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/jsonschema"
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
	// Useless marks a result that adds nothing to context and may be elided by
	// compaction (zero matches, a timeout). Ignored when IsError is set.
	Useless bool
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
	// OnProgress lets a long tool publish partial output.
	OnProgress func(Result)
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
