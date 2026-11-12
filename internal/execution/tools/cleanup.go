package tools

import "github.com/PizenLabs/ohgo/internal/agent"

// processStopper is the session-shutdown seam for a capability that owns
// detached OS processes. It is intentionally narrow: a tool that implements it
// promises StopProcesses is safe after the turn and idempotent.
type processStopper interface {
	StopJobs() int
}

// StopToolProcesses terminates detached processes owned by a tool set.
//
// It is called at session shutdown, not at a turn boundary: an explicit
// background job survives the turn so `wait` can observe it, but no process
// group may outlive the runtime that owns it. The verification capability's
// service is call-scoped and already stopped on every path; this covers the
// bash job table's intentional detachment.
func StopToolProcesses(toolSet []agent.Tool) int {
	stopped := 0
	for _, tool := range toolSet {
		if stopper, ok := tool.(processStopper); ok {
			stopped += stopper.StopJobs()
		}
	}
	return stopped
}
