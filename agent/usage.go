package agent

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/ohgo/ai"
)

// UsageLog accumulates token usage and cost across turns.
//
// It exists because neither interface shows totals on its own: the TUI needs
// them for /usage and the REPL for its own accounting, and both share one
// log so a session that moves between interfaces keeps one ledger. The cost
// needs the model's price schedule, which arrives separately because the
// model can switch mid-session.
type UsageLog struct {
	mu     sync.Mutex
	input  int
	output int
	cost   ai.Cost
	// turns counts completed turns, so /usage can say "no turns yet" rather
	// than showing zeros that read as measured.
	turns int
}

// Add folds completed turns into the log. Turns with no reported usage
// contribute nothing: a missing measurement is not a zero measurement, but
// adding zero changes nothing either way, so they pass through silently.
func (l *UsageLog) Add(turns []Turn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, turn := range turns {
		used := turn.Assistant.Usage
		if used.Total <= 0 && used.Input <= 0 && used.Output <= 0 {
			continue
		}
		l.turns++
		l.input += used.Input
		l.output += used.Output
	}
}

// SetCost records the price schedule totals are valued at. The harness calls
// it whenever the model switches, so a mid-session switch does not value new
// tokens at old prices without anyone noticing — the log notes the switch.
func (l *UsageLog) SetCost(cost ai.Cost) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cost = cost
}

// Snapshot copies the totals.
func (l *UsageLog) Snapshot() (turns, input, output int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns, l.input, l.output
}

// Cost values the accumulated tokens at the recorded schedule.
func (l *UsageLog) Cost() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cost.Total(ai.Usage{Input: l.input, Output: l.output, Total: l.input + l.output})
}

// Report renders the /usage answer. Estimates are labeled: heuristic
// token counts are not provider measurements, and presenting them as exact
// would be a lie with decimal places.
func (l *UsageLog) Report(estimated bool) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.turns == 0 {
		return "no turns yet: usage appears here after the first answer"
	}
	kind := "measured"
	if estimated {
		kind = "estimated"
	}
	return fmt.Sprintf("%d turns · %s input %d, output %d ($%.4f) · %s",
		l.turns, kind, l.input, l.output, l.cost.Total(ai.Usage{Input: l.input, Output: l.output, Total: l.input + l.output}), kind)
}
