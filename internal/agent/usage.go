package agent

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// UsageLog accumulates token usage and cost across turns.
//
// It exists because neither interface shows totals on its own: the TUI needs
// them for /usage and the REPL for its own accounting, and both share one
// log so a session that moves between interfaces keeps one ledger. The cost
// needs the model's price schedule, which arrives separately because the
// model can switch mid-session.
//
// Besides the running totals it keeps the most recent measured request, so an
// inspector can report "last request" cache reads and writes without the
// caller re-deriving them from the transcript.
type UsageLog struct {
	mu     sync.Mutex
	input  int
	output int
	// cacheRead and cacheWrite accumulate prompt-cache tokens across turns.
	// They are additive telemetry, not part of the billed input total.
	cacheRead  int
	cacheWrite int
	// last is the most recent measured request, and hasLast reports whether
	// one exists. A zero-value Usage from a silent provider is not a
	// measurement, so it never becomes the "last request".
	last    ai.Usage
	hasLast bool
	cost    ai.Cost
	// turns counts completed turns, so /usage can say "no turns yet" rather
	// than showing zeros that read as measured.
	turns int
	// cache holds the most recent per-request cache samples, newest last. They
	// are separate from the turn totals because cache locality is a property of
	// consecutive requests inside a run, not of a session sum: a total cannot
	// show that request 7 re-read 30k tokens that request 6 had already sent.
	cache []CacheSample
}

// cacheSampleLimit bounds the retained per-request samples. Two are required to
// classify a regression; more than a handful is history nobody reads.
const cacheSampleLimit = 16

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
		l.cacheRead += used.CacheRead
		l.cacheWrite += used.CacheWrite
		l.last = used
		l.hasLast = true
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

// Snapshot copies the cumulative totals: completed turns and input/output
// tokens. Cache tokens are additive telemetry and are reported separately by
// Totals.
func (l *UsageLog) Snapshot() (turns, input, output int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns, l.input, l.output
}

// Totals copies the full cumulative ledger: turns plus input, output, cache
// read, and cache write tokens.
func (l *UsageLog) Totals() (turns, input, output, cacheRead, cacheWrite int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns, l.input, l.output, l.cacheRead, l.cacheWrite
}

// Last returns the most recent measured request's usage, or false when no
// request has been measured.
func (l *UsageLog) Last() (ai.Usage, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last, l.hasLast
}

// Cost values the accumulated tokens at the recorded schedule.
func (l *UsageLog) Cost() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cost.Total(l.usageLocked())
}

// CostKnown reports a price schedule resolved, so a zero cost is a real zero
// rather than an unknown price.
func (l *UsageLog) CostKnown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cost != (ai.Cost{})
}

// CostOf values an arbitrary measured usage at the recorded schedule. It lets a
// caller price the live session's own counters against the same schedule the
// lifetime total used, without duplicating the price arithmetic.
func (l *UsageLog) CostOf(input, output, cacheRead, cacheWrite int) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cost.Total(ai.Usage{
		Input:      input,
		Output:     output,
		CacheRead:  cacheRead,
		CacheWrite: cacheWrite,
		Total:      input + output + cacheRead,
	})
}

// usageLocked returns the accumulated totals as an ai.Usage. The caller holds
// the mutex.
func (l *UsageLog) usageLocked() ai.Usage {
	return ai.Usage{
		Input:      l.input,
		Output:     l.output,
		CacheRead:  l.cacheRead,
		CacheWrite: l.cacheWrite,
		Total:      l.input + l.output + l.cacheRead,
	}
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
		l.turns, kind, l.input, l.output, l.cost.Total(l.usageLocked()), kind)
}

// Observe records one measured request and its request shape.
//
// It is the write path for cache locality: the loop calls it once per provider
// request, so the ledger can answer "did this request repeat the last prefix"
// separately from "how many tokens did this session spend".
func (l *UsageLog) Observe(sample CacheSample) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache = append(l.cache, sample)
	if len(l.cache) > cacheSampleLimit {
		l.cache = append(l.cache[:0], l.cache[len(l.cache)-cacheSampleLimit:]...)
	}
}

// LastCache returns the most recent measured request, or false when the loop
// has not reported one.
func (l *UsageLog) LastCache() (CacheSample, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.cache) == 0 {
		return CacheSample{}, false
	}
	return l.cache[len(l.cache)-1], true
}

// CacheRegression compares the latest request with the one before it.
//
// It reports false when fewer than two requests are measured: a single request
// has no previous prefix, so there is nothing to regress from.
func (l *UsageLog) CacheRegression() (CacheRegression, CacheSample, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.cache) < 2 {
		return CacheRegression{}, CacheSample{}, false
	}
	current := l.cache[len(l.cache)-1]
	regression, ok := ClassifyCache(l.cache[len(l.cache)-2], current)
	if !ok {
		return CacheRegression{}, CacheSample{}, false
	}
	return regression, current, true
}
