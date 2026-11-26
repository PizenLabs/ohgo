package agent

// ReasoningFacts is the runtime evidence available before one provider
// request. It is deliberately neutral: the loop reports capacity and context,
// while the host owns the policy that maps work onto a model's effort ladder.
type ReasoningFacts struct {
	// RemainingTurns is the run's remaining model exchanges, or -1 when
	// unmetered.
	RemainingTurns int
	// RemainingModelCalls is the run's remaining provider requests, or -1.
	RemainingModelCalls int
	// ContextWindow is the model's total context, or zero when unknown.
	ContextWindow int
	// ContextUsed is the measured input cost of the request about to be sent.
	ContextUsed int
}

// ReasoningPolicy selects the thinking setting for one provider request.
//
// Returning (false, "") omits reasoning configuration. The policy is consulted
// immediately before each request, after the request shape and budget are
// known, so a host can lower effort for the last calls in a run or raise it as
// task evidence accumulates. It must not block: it runs on the request path.
type ReasoningPolicy func(ReasoningFacts) (enabled bool, effort string)

// SetReasoningPolicy installs the per-request reasoning selector, or nil to
// use the loop's configured setting. The policy is read before every provider
// request; it does not mutate the loop's stored configuration.
func (l *Loop) SetReasoningPolicy(policy ReasoningPolicy) {
	l.cfg.ReasoningPolicy = policy
}

// RequestFacts reports the capacity and context a policy would see for the
// supplied conversation. It is the same measurement the loop uses before a
// request, so a host can make a turn-level initial decision without inventing
// a second context estimate.
func (l *Loop) RequestFacts(messages []Message) ReasoningFacts {
	facts := ReasoningFacts{RemainingTurns: -1, RemainingModelCalls: -1}
	if l.budget != nil {
		snapshot := l.budget.Snapshot()
		facts.RemainingTurns = snapshot.Remaining.Turns
		facts.RemainingModelCalls = snapshot.Remaining.ModelCalls
	}
	window := l.model().ContextWindow
	if window <= 0 {
		return facts
	}
	measurement := l.measureContext(messages, window, l.cfg.Compaction)
	facts.ContextWindow = measurement.Window
	facts.ContextUsed = measurement.ContextTokens + measurement.SystemTokens +
		measurement.ToolTokens + measurement.TaskStateTokens
	return facts
}
