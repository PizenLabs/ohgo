package agent

import (
	"errors"
	"strings"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Run-stage sentinels. They name the stage that failed before the generic
// completion verdict is formed, so a shell can report whether anything
// executed at all. They deliberately do not grant authority or carry evidence:
// an error is a stop, never a successful observation.
var (
	// ErrNoModelOutput means the provider produced no tool call and no visible
	// assistant text, so there is nothing to dispatch and nothing to answer.
	ErrNoModelOutput = errors.New("agent: NO_MODEL_OUTPUT")
	// ErrExecutionIntentWithoutTool means the model announced an action it was
	// about to take but emitted no tool call, even after the bounded protocol
	// recovery request. The prose is never executed.
	ErrExecutionIntentWithoutTool = errors.New("agent: EXECUTION_INTENT_WITHOUT_TOOL")
	// ErrToolNormalizationFailed means provider tool-call syntax could not be
	// normalized into canonical calls after the bounded recovery request.
	ErrToolNormalizationFailed = errors.New("agent: TOOL_NORMALIZATION_FAILED")
)

// Failure classes attached to Result.FailureClass. They are strings because a
// tool result crosses the durable evidence boundary, but the constants keep
// the vocabulary from drifting into prose-matching.
const (
	ToolFailureUnsupported     = "tool_unsupported"
	ToolFailureAuthorization   = "tool_authorization_denied"
	ToolFailureDispatch        = "tool_dispatch_failed"
	ToolFailureStart           = "tool_start_failed"
	ToolFailureRuntime         = "tool_runtime_failed"
	ToolFailureTimeout         = "tool_timeout"
	ToolFailureCancelled       = "tool_cancelled"
	ToolFailureVerification    = "verification_failed"
	ToolFailureInsufficient    = "verification_insufficient"
	ToolFailureInvalidArgument = "tool_invalid_arguments"
)

// normalizationFailureReason renders structured normalization evidence without
// exposing the raw provider payload in an error string.
func normalizationFailureReason(failures []ai.ToolNormalizationFailure) string {
	if len(failures) == 0 {
		return "provider tool-call syntax could not be normalized"
	}
	first := failures[0]
	parts := []string{string(first.Status)}
	if first.Protocol != "" {
		parts = append(parts, first.Protocol)
	}
	if first.Reason != "" {
		parts = append(parts, first.Reason)
	}
	return strings.Join(parts, ": ")
}
