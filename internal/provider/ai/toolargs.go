package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ToolCallArgumentError classifies a streamed tool call whose argument JSON
// could not be decoded.
//
// It is deliberately a distinct failure class, not a generic stream error:
// the transport succeeded and the model produced a call, but the call is not
// executable. The runtime must not execute it, must not treat the task as a
// failed run, and must be able to tell a truncated payload from a malformed
// one so its recovery instruction is truthful.
type ToolCallArgumentError struct {
	// Tool is the call's name when it was known before decoding failed.
	Tool string
	// Incomplete reports that the payload ended before the JSON value did.
	// A truncated write is the common case; the bytes are not malformed, they
	// are unfinished.
	Incomplete bool
	// OutputLimit reports that the provider said the response stopped at the
	// model's output ceiling. It is evidence that retrying the identical
	// payload would truncate again, so the recovery must reduce or split it.
	OutputLimit bool
	// Err is the underlying decode error, retained for diagnostics.
	Err error
}

func (e *ToolCallArgumentError) Error() string {
	name := "a tool call"
	if e.Tool != "" {
		name = fmt.Sprintf("tool call %q", e.Tool)
	}
	reason := "its arguments could not be decoded"
	if e.Incomplete {
		reason = "its argument JSON is incomplete"
	}
	if e.OutputLimit {
		reason += " (the response hit the model's output limit)"
	}
	return "ai: " + name + ": " + reason
}

// Unwrap exposes the decoder error to errors.Is/As without making it the
// classification: the runtime switches on ToolCallArgumentError first.
func (e *ToolCallArgumentError) Unwrap() error { return e.Err }

// decodeJSONArgs decodes accumulated tool-call argument fragments.
//
// It returns a non-nil map for an empty or whitespace-only payload, because a
// tool with no parameters legitimately streams no argument text. A truncated
// payload is reported as an incomplete ToolCallArgumentError rather than a
// generic syntax error; the decoder is used instead of json.Unmarshal because
// it exposes io.ErrUnexpectedEOF for the truncated case.
func decodeJSONArgs(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	var args map[string]any
	if err := decoder.Decode(&args); err != nil {
		incomplete := errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
		return nil, &ToolCallArgumentError{Incomplete: incomplete, Err: err}
	}
	// A complete value followed by another token is not one argument object.
	// It is a malformed envelope, not a truncation, and must not execute.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("trailing data after the argument object")
		}
		return nil, &ToolCallArgumentError{Incomplete: false, Err: err}
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// classifyToolArgumentError attaches the call identity and the provider's
// output-limit verdict to a decode failure. It preserves an existing typed
// error so the classification is created once.
func classifyToolArgumentError(tool string, stopReason StopReason, err error) error {
	var argumentErr *ToolCallArgumentError
	if !errors.As(err, &argumentErr) {
		return err
	}
	if argumentErr.Tool == "" {
		argumentErr.Tool = tool
	}
	argumentErr.OutputLimit = argumentErr.OutputLimit || stopReason == StopMaxTokens
	return argumentErr
}
