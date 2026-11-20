package session

import "github.com/PizenLabs/ohgo/internal/agent"

// MeasuredUsage folds one run's completed turns into the session ledger.
//
// A turn with no provider usage contributes nothing. The caller persists this
// value once at the run boundary; passing a process-cumulative log here would
// double-count earlier runs.
func MeasuredUsage(turns []agent.Turn) Usage {
	var usage Usage
	for _, turn := range turns {
		used := turn.Assistant.Usage
		if used.Total <= 0 && used.Input <= 0 && used.Output <= 0 {
			continue
		}
		usage.Turns++
		usage.Input += used.Input
		usage.Output += used.Output
		usage.CacheRead += used.CacheRead
		usage.CacheWrite += used.CacheWrite
	}
	return usage
}
