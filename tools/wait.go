package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
)

// WaitTool collects the output of background shell jobs.
//
// It is the read half of background execution: `bash` with `background: true`
// starts work and returns immediately, and this tool blocks until that work
// produces output or finishes. Splitting the two is what lets the agent start
// a build and keep reading code while it runs, instead of sitting idle
// through every long command.
//
// The contract, in order of importance:
//
//  1. A wait with nothing to wait for returns immediately. Blocking until
//     timeout when no job can ever produce output is a hang with a delay,
//     which is the termination violation the testing rules forbid.
//  2. Output is incremental. Each wait returns only what arrived since the
//     last one, so repeated waits do not refill the context with the same
//     build log.
//  3. Completion is explicit. A finished job says so with its exit code;
//     a job that is still going says so with its elapsed time.
type WaitTool struct {
	Base
	// jobs is the table shared with the bash tool. It is injected rather
	// than constructed so both tools observe the same jobs.
	jobs *BackgroundJobs
}

// NewWaitTool returns a wait tool over a shared job table.
func NewWaitTool(jobs *BackgroundJobs) *WaitTool {
	if jobs == nil {
		jobs = NewBackgroundJobs()
	}
	tool := &WaitTool{jobs: jobs}
	tool.Base = NewBase("wait",
		"Wait for a background shell job to produce output or finish, and return "+
			"what arrived since the last wait. Pass a job id from bash, or nothing "+
			"to wait on the oldest job with unread output.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"jobId": map[string]any{
					"type":        "string",
					"description": "Background job id, e.g. job-1. Omit to wait on the oldest job with unread output.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "How long to wait in milliseconds before reporting what has arrived so far.",
					"minimum":     1.0,
					"maximum":     float64(WaitMaxTimeout.Milliseconds()),
				},
			},
		},
		// Observing a job never starts, stops, or alters one: it only reads
		// bytes the process already wrote. Read-tier is honest.
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// Execute implements agent.Tool.
func (t *WaitTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	jobID, err := strArg(args, "jobId", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	jobID = strings.TrimSpace(jobID)

	timeout := WaitDefaultTimeout
	if rawMs, err := intArg(args, "timeout", 0); err != nil {
		return agent.ErrorResult("%v", err)
	} else if rawMs > 0 {
		timeout = time.Duration(rawMs) * time.Millisecond
		if timeout > WaitMaxTimeout {
			timeout = WaitMaxTimeout
		}
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()

	for {
		if err := checkCancelled(ctx.Ctx); err != nil {
			return agent.ErrorResult("%v", err)
		}

		if jobID != "" {
			if result, done := t.pollOne(jobID); done {
				return result
			}
		} else if result, done := t.pollAny(); done {
			return result
		}

		if time.Now().After(deadline) {
			return t.onTimeout(jobID)
		}

		select {
		case <-ctx.Ctx.Done():
			return agent.ErrorResult("wait cancelled")
		case <-ticker.C:
		}
	}
}

// pollOne checks a named job. done reports the wait is over either way.
func (t *WaitTool) pollOne(jobID string) (agent.Result, bool) {
	snap := t.jobs.consume(jobID)
	if !snap.ok {
		return agent.ErrorResult("unknown background job %q", jobID), true
	}
	return renderWait(jobID, snap)
}

// pollAny checks the whole table. done reports the wait is over either way.
func (t *WaitTool) pollAny() (agent.Result, bool) {
	any := t.jobs.consumeAny()
	switch {
	case any.empty:
		// Nothing can ever arrive: returning now is what keeps this from
		// being a hang with a delay.
		return agent.ErrorResult("no background jobs: start one with bash first"), true
	case any.drained:
		return agent.Result{
			Text:    "All background jobs finished, no new output.",
			Useless: true,
		}, true
	case any.id == "":
		return agent.ErrorResult("no background jobs: start one with bash first"), true
	}
	return renderWait(any.id, any.jobSnapshot)
}

// onTimeout reports what arrived before the deadline.
//
// A timeout is not an error: the job is still running and the model should
// wait again, not treat the build as failed. Erroring here would turn every
// slow build into a false failure.
func (t *WaitTool) onTimeout(jobID string) agent.Result {
	if jobID != "" {
		snap := t.jobs.consume(jobID)
		if !snap.ok {
			return agent.ErrorResult("unknown background job %q", jobID)
		}
		if snap.text != "" || snap.done {
			return renderFinished(jobID, snap)
		}
		return agent.Result{
			Text: fmt.Sprintf("Job %s still running, no new output yet — wait again to keep waiting.", jobID),
		}
	}
	any := t.jobs.consumeAny()
	if any.empty || (any.drained && any.text == "") {
		return agent.Result{
			Text:    "All background jobs finished, no new output.",
			Useless: true,
		}
	}
	if any.id == "" {
		return agent.Result{
			Text:    "All background jobs finished, no new output.",
			Useless: true,
		}
	}
	if any.text != "" || any.done {
		return renderFinished(any.id, any.jobSnapshot)
	}
	return agent.Result{
		Text: fmt.Sprintf("Job %s still running, no new output yet — wait again to keep waiting.", any.id),
	}
}

// renderWait shapes one poll into a result.
func renderWait(id string, snap jobSnapshot) (agent.Result, bool) {
	if snap.text == "" && !snap.done {
		// Still running with nothing new: keep blocking.
		return agent.Result{}, false
	}
	return renderFinished(id, snap), true
}

// renderFinished formats output already in hand, whether it arrived just now
// or was waiting when the call began.
func renderFinished(id string, snap jobSnapshot) agent.Result {
	var sb strings.Builder
	if snap.truncationNotice {
		fmt.Fprintf(&sb, "[earlier output of %s truncated at %d bytes]\n", id, BashMaxOutput)
	}
	sb.WriteString(strings.TrimRight(snap.text, "\n"))
	if snap.done {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		if snap.timedOut {
			fmt.Fprintf(&sb, "[job %s killed after its timeout, partial output above]", id)
		} else {
			fmt.Fprintf(&sb, "[job %s finished, exit code %d, %s]", id, snap.exitCode, snap.elapsed.Round(time.Millisecond))
		}
		if strings.TrimSpace(snap.text) == "" {
			return agent.Result{Text: sb.String(), Useless: true}
		}
		return agent.Result{
			Text: sb.String(),
			Details: map[string]any{
				"job":      id,
				"done":     true,
				"exitCode": snap.exitCode,
			},
		}
	}
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "[job %s still running, %s elapsed — wait again for more]", id, snap.elapsed.Round(time.Millisecond))
	return agent.Result{
		Text: sb.String(),
		Details: map[string]any{
			"job":  id,
			"done": false,
		},
	}
}
